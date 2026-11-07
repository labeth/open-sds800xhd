# 11 — Host Interface

The SDS800X HD exposes one SCPI engine over four transports: a raw TCP socket, a telnet-style TCP line
interface, VXI-11 (ONC RPC), and a USB-TMC/USB488 device. The web server, the LXI identification
page and a VNC server sit beside it. The web UI reaches the instrument only through VXI-11 on the loopback
interface. All SCPI transports feed the same parser, the same error queue and the same status
registers. A replacement firmware must present the transports, identities and byte formats below. It may
implement them any way it likes.

The command inventory, with parameters and SDS800X HD ranges, is in
[`appendix/scpi-commands.tsv`](appendix/scpi-commands.tsv). §6 states the rules that apply to every
command. §7 covers waveform transfer and §8 the screen dump.

---

## 1. Network services

| Port | Proto | Service | Server |
|---|---|---|---|
| 5025 | TCP | Raw SCPI socket (§2) | instrument application |
| 5024 | TCP | Telnet SCPI line interface (§3) | instrument application |
| 111 | TCP+UDP | ONC RPC portmapper (rpcbind) | system `rpcbind` |
| ephemeral | TCP+UDP | VXI-11 DEVICE_CORE / DEVICE_ASYNC / DEVICE_INTR (§4) | instrument application, registered with the portmapper |
| 80 | TCP | HTTP: web UI, LXI identification (§10, §11) | HTTP server |
| 5900 | TCP | RFB (VNC) 3.x, also WebSocket for noVNC (§12) | VNC server |
| 21 | TCP | FTP, anonymous enabled | system FTP server **UNVERIFIED** that it is reachable/useful |
| 5353 | UDP | mDNS/DNS-SD (§10) | avahi-daemon |

SSH, a telnet login (port 23) and Samba are not offered. A firmware needs only the SCPI ports, VXI-11
and, optionally, HTTP/VNC.

The raw-socket port is 5025; no SCPI command for changing it is documented. The VNC port is configurable
with `:SYSTem:COMMunicate:VNCPort <5900..5999>`.

---

## 2. Raw SCPI socket (TCP 5025)

- Listen on all addresses, TCP port **5025**, backlog 10.
- **One client is served at a time.** The server accepts a connection and serves it until the peer
  closes, then accepts the next. While a client is served, further connections wait in the listen backlog.
- **Input:** received bytes are appended to a line buffer. When a received chunk ends in LF and no further
  bytes are pending, the buffer minus that final LF is passed to the SCPI parser; embedded LFs terminate program messages inside the parser, so
  `A?\nB?\n` in one segment yields both responses. An LF is required at the end of a segment; CR is whitespace.
- **Output:** after a message is parsed, the server collects the pending output (§6.3) and sends it as
  is. There is no extra framing: responses end with the parser's `\n`, and binary blocks are sent raw.
- **No prompt and no echo.**
- **End of session:** a peer close or a `recv` error closes the client socket.

## 3. Telnet SCPI line interface (TCP 5024)

- **Listener:** a plain TCP listener (no telnet option negotiation), port **5024**, backlog **1**,
  `SO_REUSEADDR`.
- **Single session:** a new connection makes the server close any previous telnet client.
- **Banner:** on accept the server sends the 7-bit ASCII text
  `Welcome to the SCPI instrument '<manufacturer> <model>'` CR LF `>>`. There is one space between the
  two fields. The model is product-information id 12 (§6.6). The manufacturer is the OEM manufacturer
  string, or `Siglent` when none is set (no ` Technologies` suffix here). Example with defaults:
  `Welcome to the SCPI instrument 'Siglent SDS120X'\r\n>>`.
- **Input is handled per `recv()` chunk** (up to 2048 bytes), not per character. The server never echoes
  typed characters; the client is expected to echo locally. The chunk length selects the action:

  | Chunk | Action |
  |---|---|
  | 0 bytes or error | close the session |
  | 1 byte, BS (0x08) | cursor at the end of the line: delete the last character; the server redraws the line as `\r>>` + line + space, then sends `\b`. Cursor inside the line: the line is unchanged, and the server sends back the character before the cursor. Cursor at position 0: the server sends `>` |
  | 1 byte, other | stored at the cursor (overwrite inside the line, append at the end); nothing is sent. When the line already holds 512 bytes, the server sends `\r\n Reach the max buffer size!` and drops the byte |
  | 2 bytes, CR LF | the line buffer goes to the parser as one program message. The server sends the response bytes (if any), then the prompt `\r>>`. A non-empty line that differs from the previous history entry is added to the history |
  | 2 bytes, other | appended to the line; the server sends one byte from beyond the line end (normally 0x00) |
  | 3 bytes `ESC [ C` / `ESC [ D` | cursor right / left |
  | 3 bytes `ESC [ A` | recall the next older history entry into the line buffer; the server sends `\r>>` + entry twice (`\r>>*IDN?\r>>*IDN?`). The history is an unbounded list; the recall index restarts at the newest entry after each CR LF submit and wraps past the oldest. Before the first CR LF submit of the session `ESC [ A` does nothing. `ESC [ B` is ignored |
  | any other chunk (≥ 3 bytes) | the chunk itself is one complete program message: the server sends one byte from beyond the line end (normally 0x00), passes the chunk to the parser, and sends the response. **No prompt** follows, the line buffer is cleared, and the chunk is not added to the history |

  A client that sends whole lines (`*IDN?\r\n` in one segment) therefore receives `\x00<response>` and
  no prompt. A character-mode client gets the prompt after each CR LF.
- A firmware may implement a cleaner line discipline. Clients should ignore NUL bytes and must not wait
  for `>>`.
- The programming guide's generic description shows a `SCPI>` prompt. The instrument sends `>>`.

## 4. VXI-11

Standard VXI-11 1.0 over ONC RPC.

### 4.1 Registration

The loopback interface must be up (the web UI reaches the instrument on `127.0.0.1`, §11). Then:

1. remove any stale portmapper registration of programs 0x0607B0 (DEVICE_ASYNC), 0x0607AF (DEVICE_CORE)
   and 0x0607B1 (DEVICE_INTR), version 1;
2. register all three programs on one UDP transport (protocol 17);
3. register all three programs on one TCP transport (protocol 6).

Both transports listen on ephemeral ports. The portmapper is the system rpcbind on port 111, and clients
find the server with GETPORT.

### 4.2 Links

| Call | Behaviour |
|---|---|
| `create_link` | **Device name:** compared byte-for-byte with `inst0`. A mismatch returns error **3** (device not accessible), but a link is created anyway. **Link setup:** first, every existing link on the same TCP connection (same transport socket) is destroyed; the connection's lock is released only if that link held it. The new `lid` is a global counter that starts at 1 and increments for each link. **Reply:** `abortPort` is the TCP port of the RPC transport, and `maxRecvSize` is **0x800000** (8 MiB). **Ignored:** `clientId`, `lockDevice` and `lock_timeout`. |
| `destroy_link` | An unknown `lid` returns error 4. If the link held the lock, the lock is released. The link is then removed and error 0 is returned. |
| `device_write` | **Accumulation:** the data is appended to a single process-wide input buffer. **Submit on END:** when `flags` bit 3 (END, 0x08) is set, the whole buffer goes to the parser as one program message and the buffer is cleared. The message is queued for the parser (parsing is asynchronous, so SCPI errors such as an undefined header still return 0). Error **9** is returned only when the parser input queue (50 pending messages, shared by all transports, §6.1) stays full for 2 s (20 tries, 100 ms apart); otherwise 0. **Reply:** the reply `size` always equals the data length. **Side effects:** each write first discards the unread part of a response that this link has already fetched (output still in the shared output queue, §6.3, is not discarded), and the write's `io_timeout` is stored as the link's read timeout. **Not checked:** the lock, `lock_timeout` and the `waitlock` flag (no error 11 is ever returned from write). |
| `device_read` | **Partial reads:** if the link still holds an unread part of an earlier response, the next at most `requestSize` bytes of it are returned. **Fresh response:** otherwise the server waits until the shared parser output queue (§6.3) holds bytes, then takes all bytes queued so far. If none arrives before the stored `io_timeout` (from the last `device_write`, default 2000 ms), it returns error **15** (I/O timeout) with `reason` = 4 and no data. **Reason:** `reason` = 4 (END) when the returned bytes complete the response, otherwise 1 (REQCNT). **Ignored:** `termChar` and the `termChrSet` flag (reason 2 is never set) and the read's own `io_timeout`. |
| `device_readstb` | Returns error 0 and `stb` = the IEEE 488.2 status byte (§6.5). |
| `device_clear` | Discards the unread part of a response this link has already fetched and returns 0. Output still waiting in the shared output queue is kept: `*OPC?` write, `device_clear`, `device_read` returns `1\n`. It does not clear the parser input, the status registers or the error queue. |
| `device_trigger`, `device_remote`, `device_local`, `device_abort`, `device_enable_srq`, `create_intr_chan`, `destroy_intr_chan`, `device_docmd` | All accepted and answered with error 0; they have no effect. **No SRQ** interrupt RPC is ever sent. |
| `device_lock` | Checked in this order: error 11 if any link holds the lock, then error 4 for an unknown `lid`. Otherwise the server marks this link as the lock holder and returns 0. It never waits; `flags`/`lock_timeout` are ignored. |
| `device_unlock` | Checked in this order: error **8** if no link holds the lock (VXI-11 would say 12), then error 4 for an unknown `lid`. If this link holds the lock it is released; the call returns 0 either way. |

The lock is advisory only: no data call checks it. `device_readstb` and `device_clear` with an unknown `lid`: **UNVERIFIED**. The instrument serves one RPC at a time: a `device_read` that waits blocks every other link until it returns. A firmware may serve links concurrently.

### 4.3 Loopback client

The instrument is also a VXI-11 client: it controls external Siglent generators for Bode plots (spec 15 §1).
The web UI is a VXI-11 client of the instrument on `127.0.0.1` (§11).

## 5. USB-TMC device (USB488)

### 5.1 Gadget

The rear USB-B port is the ZynqMP USB controller at `fe300000.dwc3`, configured as `dr_mode =
"peripheral"`. The controller at `fe200000.dwc3` is the host port. The firmware loads the USBTMC gadget
module (a legacy gadget derived from the Linux printer gadget) after a configurable start-up delay. Load
sequence:

1. If `/sys/module/g_usbtmc` already exists, skip to step 4.
2. If `/sys/module/f_usbtmc` is absent, run `insmod /usr/bin/siglent/drivers/f_usbtmc.ko` (the USBTMC
   function driver).
3. Load the gadget:

   ```
   insmod /usr/bin/siglent/drivers/g_usbtmc.ko idVendor=0xf4ec idProduct=0xee38 \
          iManufacturer="Siglent" iProduct="<model, spaces removed>" iSerialNum=<serial> enableSuperSpeed=<0|1>
   ```

   `enableSuperSpeed` is not a parameter of this module (the kernel ignores it) and may be omitted.
4. Poll for `/sys/module/g_usbtmc` once per second, up to 21 times. Give up if it never appears.
5. Try to open `/dev/g_usbtmc` up to 6 times, 1 s apart.

- **Parameter defaults:** VID, PID and manufacturer come from the OEM configuration when it provides them;
  otherwise the defaults above apply. When the model string is empty the product string is `123`.
- **Serial string:** `iSerialNum` and `iSerialNumber` are both module parameters and both set the same composite
  serial-number string (the device descriptor's iSerialNumber). Neither changes the IEEE 1284 `iPNPstring`, whose
  `SN:1` stays fixed. `qlen` defaults to 160.
- **Buffer size:** the gadget's transfer buffer size is read from
  `/sys/class/usb_tmc_gadget/g_usbtmc/send_buffer_size`.

| Descriptor field | Value |
|---|---|
| bcdUSB | set by the composite core (0x0200 HS / 0x0300 if SS) |
| bDeviceClass/SubClass/Protocol | 0/0/0 (per interface) |
| idVendor / idProduct | **0xF4EC / 0xEE38** (module defaults 0xF4EC / 0x1013 when no parameter) |
| bcdDevice | 0x0000 |
| iManufacturer / iProduct / iSerialNumber | `Siglent` / model without spaces (e.g. `SDS824XHD`, **UNVERIFIED**) / serial number (from `iSerialNum`) |
| bNumConfigurations | 1; configuration value 1, bmAttributes 0xC0 (self-powered) |
| Interface | 0, alt 0, 3 endpoints, class **0xFE**, subclass **0x03**, protocol **0x01** (USBTMC USB488) |
| EP bulk IN | 0x8n, max packet 64 FS / 512 HS / 1024 SS |
| EP bulk OUT | 0x0n, max packet 64 FS / 512 HS / 1024 SS |
| EP interrupt IN | 0x8n, max packet 64, bInterval 2 |
| SS companion (each EP) | bMaxBurst 0, bmAttributes 0 |
| Gadget queue length (`qlen`) | 160 |
| IEEE 1284 device ID (`iPNPstring`) | `MFG:linux;MDL:g_usbtmc;CLS:USBTMC;SN:1;` |

`GET_CAPABILITIES` response (24 bytes, answered by the kernel function driver):

```
01 00 00 01 00 00 00 00 00 00 00 00 | 00 01 04 0C 00 00 00 00 00 00 00 00
```

That is: status SUCCESS, bcdUSBTMC 1.00, no USBTMC interface or device capabilities (no
INDICATOR_PULSE, not talk/listen-only, no TermChar), bcdUSB488 1.00, USB488 interface capability 0x04
(488.2 interface), USB488 device capability 0x0C (SCPI compliant, SR1). The other class requests
(INITIATE_ABORT_*, CHECK_*_STATUS, INITIATE_CLEAR, READ_STATUS_BYTE, …) are handled by the kernel
function driver, not by user space (**UNVERIFIED**: exact replies).

### 5.2 Bulk protocol (handled in user space)

The firmware reads raw bulk-OUT transfers from `/dev/g_usbtmc` and parses the 12-byte USBTMC header itself:

- **Tag check:** a header whose `bTagInverse` != `~bTag & 0xFF` is dropped.
- **MsgID 1 (DEV_DEP_MSG_OUT):** the 32-bit LE `TransferSize` counts payload bytes after the header,
  which is padded to a multiple of 4. Payload is accumulated across bulk reads until `TransferSize` bytes
  have arrived; those bytes (padding dropped) are then handed to the parser as **one program message**.
  `bmTransferAttributes` (EOM) is not examined: every DEV_DEP_MSG_OUT is a complete message, so a host
  must not split one program message over several DEV_DEP_MSG_OUT transfers. A trailing LF is whitespace
  to the parser.
- **MsgID 2 (REQUEST_DEV_DEP_MSG_IN):** the host's `TransferSize` is the maximum to return. The
  instrument answers on bulk IN with a 12-byte header and the data:
  - `MsgID`=2 and the request's `bTag`/`~bTag`;
  - `TransferSize` = min(available, requested);
  - `bmTransferAttributes` bit0 (EOM) = 1 when this transfer contains the last byte of the response;
  - the data, padded with zero bytes to a multiple of 4.

  The rest of a response that was larger than requested is returned on following requests.
  When no response is pending and the shared parser output queue (§6.3) is empty, the instrument sends
  **no bulk-IN transfer** and the host's read times out (a firmware should rather answer a zero-length
  transfer with EOM = 1). The request's `TermChar`/`bmTransferAttributes`
  bit 1 are ignored.
- **Other MsgIDs:** vendor-specific MsgIDs (126/127) and TRIGGER (128) are not implemented.
- **SRQ:** the interrupt-IN endpoint exists, but no SRQ notification is generated.

## 6. SCPI engine

### 6.1 Parser

The parser behaves like the open-source **libscpi** (j123b567 scpi-parser, v2.x) with a growable input
buffer. Its standard behaviour applies:

- **Program messages:** a message is one or more program units separated by `;`. A unit is a header plus
  optional parameters. Headers are case-insensitive, and each node matches its short form (upper-case
  letters of the pattern) or its long form.
- **Pattern syntax:** `[...]` marks optional nodes and `#` a numeric suffix (`:CHANnel#` matches
  `CHAN1`, `CHANnel2`). A missing suffix means 1.
- **Header path:** a unit without a leading `:` continues the header path of the previous unit
  (`:CHAN1:SCAL?;OFFS?`). Common commands (`*XXX`) do not change the path.
- **Parameters:** parameters are comma-separated. Numbers may use NR1/NR2/NR3 and SI suffixes
  (`m`, `u`, `n`, `k`, `M`=mega via `MA`). Units are accepted where the command declares one; the
  engine's unit table includes `V`, `S`, `HZ`, `KHZ`, `MS/S`, `PCT`, `DB*`, …. `MIN`/`MAX`/`DEF` are
  accepted where the handler allows them.
- **Strings:** quoted strings use `"` or `'`.
- **Command set:** the SDS800X HD set is listed in the appendix (§6.7).

### 6.2 Errors

Errors go into a FIFO error queue that holds **16 errors plus the overflow marker** (17 slots). When a
17th error arrives, it is discarded and `-350,"Queue overflow"` is queued after the 16 kept errors.
`:SYSTem:ERRor[:NEXT]?` returns the oldest entry as `<code>,"<text>"` (NR1, comma, quoted text), and an
empty queue returns `0,"No error"`. `SYSTem:COUNt?` returns the number of queued entries (the overflow
marker counts: 17 after an overflow). `SYSTem:ERRor:COUNt?` is not registered. The texts are the
SCPI-99 list in [`appendix/scpi-errors.tsv`](appendix/scpi-errors.tsv) (codes −800…0); any other code
has the text `Unknown error`.

**Device-dependent information.** Only `-113` carries it: the text is `Undefined header;` followed by the
whole program unit as received, **including its trailing `;` separator** and with trailing CR/LF removed.
Examples: `FOO:BAR?` → `-113,"Undefined header;FOO:BAR?"`; `A1;A2` → `-113,"Undefined header;A1;"` then
`-113,"Undefined header;A2"`. All other entries have no `;<info>` part.

The parser itself raises:

| Condition | Entry |
|---|---|
| Unknown header | `-113,"Undefined header;<unit>"` (no handler runs, the output state is untouched, §6.3) |
| Invalid character in a header | `-101,"Invalid character"` |
| Parameters left over after a handler that queued no error itself | `-108,"Parameter not allowed"` |
| Handler returned failure and queued no error itself during this unit | `-200,"Execution error"` (also when the queue already holds older errors) |
| Input buffer cannot grow | `-363,"Input buffer overrun"` |

**Missing or bad parameters (per handler class).**

- Common and status commands (`*ESE`, `*SRE`, `STATus:…:ENABle`) take a mandatory parameter: missing →
  `-109,"Missing parameter"`; wrong type → `-104,"Data type error"`.
- Instrument commands (the `:…` long-form commands and the legacy short forms): a missing parameter gives
  `-200,"Execution error"` (`:CHAN1:SCALe`, `:TIM:SCAL`, `:CHAN1:COUP`, `:TRIG:MODE`, `:ACQ:MDEP`,
  `:WAV:SOUR`, `:FORM:DATA`, `:SYST:BUZZ`, `TDIV` all observed). An unknown choice for an enumerated
  parameter gives `-224,"Illegal parameter value"` (`:CHAN1:COUP XYZ`) and no -200. A non-numeric value
  for a numeric setting (`:CHAN1:SCAL abc`) is ignored without any error. A surplus parameter
  (`:CHAN1:INV ON,OFF`, `:TIM:SCAL 1E-6,2`) gives `-108`.
- A channel suffix beyond the model (`:CHAN5:SCAL?` on a 4-channel model) gives `-200` and no reply; a
  bad source name (`:WAV:SOUR C5`) gives `-224`.

Handlers may push other standard codes (`-222,"Data out of range"`, `-221,"Settings conflict"`, …). A
query that fails or is unknown produces no response bytes, so the client times out. Each error also sets
the ESR bit of its class (§6.5).

### 6.3 Responses

**Separator and terminator algorithm.** The parser keeps an output counter `out` (bytes written by the
current unit). For each program message:

1. `out = 0`.
2. For each program unit, in order:
   - unknown header: queue -113 (§6.2); `out` is **not** changed;
   - known header: if `out > 0` **and** the unit is a query (its header ends in `?`), write `;`. Then set
     `out = 0` and run the handler. Values written by one handler are separated by `,`; every byte the
     handler writes adds to `out`.
3. After the last unit: if `out > 0`, write `\n`.

Consequences (all observed): `*IDN?;*OPC?` → `<idn>;1\n`; `*IDN?;*CLS` → `<idn>` with **no** LF;
`*IDN?;*CLS;*OPC?` → `<idn>1\n` (no `;`); `*OPC?;*ESE 0;*OPC?;*ESE?` → `11;0\n`; `*IDN?;FOO;*OPC?` →
`<idn>;1\n`; `*OPC?;FOO` → `1\n`; `:TIM:SCAL?;:TIM:SCAL 1E-6;:TIM:SCAL?` → `1.00E-061.00E-06\n` (at 1 µs/div). A
firmware must reproduce this byte stream to stay compatible with existing clients; clients must not rely on a
LF after a message whose last unit is a command.

- **Output queue.** All transports share one parser input queue (at most 50 pending messages) and one
  output byte queue. A transport collects output by taking **all bytes queued so far**. The instrument
  does not bind a response to the transport or VXI-11 link that sent the message: a client that
  interleaves messages on two transports can receive the other's output. A firmware should bind output
  to the session that sent the message.
- **Number formats:** numbers use the handler's own formatting: `%E`-style NR3 with the number of
  **significant digits** set by `:FORMat:DATA` (§6.3.1), default 3, e.g. `5.00E-01`. Some settings answer
  NR1 (the tsv reply column).
- **Enumerated replies** are the choice spelled as in the command pattern (mixed case: `ONEMeg`, `NORMal`,
  `POSition`, `FALLing`, `ENGLish`, `VECTor`, `DEFault`), except `:FORMat:DATA?` (`CUSTOM`).
- **Long-form commands** (`:…`) always answer value-only, without header or unit, whatever `CHDR` is:
  `:CHAN1:SCAL?` → `1.00E+00`; `:CHAN1:SCAL?;OFFS?` → `1.00E+00;0.00E+00`. Waveform blocks of the long
  form (`:WAV:DATA?`, `:WAV:PRE?`) have no header either.

#### 6.3.1 `:FORMat:DATA`

`:FORMat:DATA {SINGle|DOUBle|CUSTom}[,<digits>]` sets the significant digits of NR3 replies: SINGle = 7
(`1.000000E-06`), DOUBle = 14 (`1.0000000000000E-06`), CUSTom,`<n>` = n (CUST,6 → `1.00000E-06`), default
CUSTom,3. The query always answers `CUSTOM,<digits>` (`CUSTOM,3`, `CUSTOM,7`, `CUSTOM,14`). The setting
is global and applies to every NR3 reply, including legacy ones.

#### 6.3.2 Legacy short-form replies (`CHDR`)

`CHDR {SHORT|LONG|OFF}` (`COMM_HEADER`), default SHORT, controls only the legacy short-form commands.
`CHDR?` → `CHDR SHORT` in SHORT, `COMM_HEADER LONG` in LONG, `OFF` (no header) in OFF — the query obeys its own
rule. Replies at defaults:

| Query | CHDR SHORT | CHDR LONG | CHDR OFF |
|---|---|---|---|
| `C1:VDIV?` | `C1:VDIV 1.00E+00V` | `C1:VOLT_DIV 1.00E+00V` | `1.00E+00` |
| `C1:OFST?` | `C1:OFST 0.00E+00V` | — | — |
| `C1:CPL?` | `C1:CPL D1M` | `C1:COUPLING D1M` | `D1M` |
| `C1:BWL?` | `C1:BWL OFF` | — | — |
| `C1:ATTN?` | `C1:ATTN 1` | — | — |
| `C1:TRA?` | `C1:TRA OFF` | `C1:TRACE OFF` | — |
| `C1:TRLV?` | `C1:TRLV 0.00E+00V` | — | `0.00E+00` |
| `TDIV?` | `1.00E-06S` | `1.00E-06S` | `1.00E-06` |
| `TRMD?` | `TRMD AUTO` | — | — |
| `TRSE?` | `TRSE EDGE,SR,C1,HT,OFF` | `TRSE EDGE,SR,C1,HT,OFF` | `EDGE,SR,C1,HT,OFF` |
| `MSIZ?` | `MSIZ 10M` | `MEMORY_SIZE 10M` | — |
| `SARA?` | `SARA 2.00E+09Sa/s` | `SAMPLE_RATE 2.00E+09Sa/s` | `2.00E+09` |
| `INR?` | `INR 0` | — | — |
| `ALST?` | `ALST STB,0,ESR,0,INR,0,DDR,0,CMR,0,EXR,0,URR,0` | — | — |
| `TMPL?` | `TMPL "` + template text + `"` CR LF (§7.5) | — | — |
| `C1:WF? DESC` | `C1:WF DESC,#9000000346…` (§7.5) | — | — |
| `CYMT?` | `CYMT 2.50E+04Hz` (25 kHz; NR3 with the §6.3.1 digits); `CYMT 10Hz` below 10 Hz / no signal (spec 15 §4) | — (long header `CYMOMETER`) | — |

Rules: SHORT = `<short header> <value><unit>`; LONG = the long header (`VOLT_DIV`, `COUPLING`, `TRACE`,
`MEMORY_SIZE`, `SAMPLE_RATE`; `TRSE` has no long name); OFF = value only **without unit**. `TDIV?` never has
a header but keeps the `S` unit in SHORT and LONG. "—" = not observed; follow the rules.

### 6.4 IEEE 488.2 common commands

| Command | Behaviour |
|---|---|
| `*IDN?` | §6.6 |
| `*CLS` | Clears ESR, the OPERation/QUEStionable event registers and the error queue (§6.5). |
| `*ESE <n>` / `*ESE?` | Event-status enable mask (NR1). The value is stored as 16 bits without a range check; only bits 7:0 matter. Missing value → −109. |
| `*ESR?` | Returns ESR (NR1) and clears it. |
| `*SRE <n>` / `*SRE?` | Service-request enable mask, stored like `*ESE`. |
| `*STB?` | Status byte (NR1), §6.5. |
| `*OPC` | Sets ESR bit 0. Commands complete synchronously, so this is immediate. |
| `*OPC?` | Returns `1`. |
| `*WAI` | No-op (all commands are sequential). |
| `*RST` | Instrument default setup (same as the front-panel Default key; **UNVERIFIED** on a unit). |
| `*TST?` | Returns `0`. |
| `*OPT?` | One comma-separated field per option type the product supports, in option-type order (spec 15 §8): the option name (`FG`, `16LA`, `PA`, … table in spec 15 §8) when its licence state is "installed" (licence type 2), otherwise `0`; no trailing comma. SDS800X HD without options: `0,0,0` (three supported types; which three is **UNVERIFIED**). |
| `*SAV <n>`, `*RCL <n>` | Save/recall an internal setup slot (the same store as the UI's internal setups). |
| `*CAL?` | IEEE 488.2 calibration query; not documented for this model. |

Also standard: `SYSTem:VERSion?` (returns the SCPI version, `1999.0`), `STATus:OPERation[:EVENt]?`,
`STATus:OPERation:CONDition?`, `STATus:OPERation:ENABle[?]`, `STATus:QUEStionable[:EVENt]?`,
`STATus:QUEStionable:CONDition?`, `STATus:QUEStionable:ENABle[?]` and `STATus:PRESet`.

### 6.5 Status model

The registers are 16-bit. Writing a register with the value it already holds does nothing (no
propagation).

- **ESR** (event status register): bit0 OPC, bit1 RQC, bit2 QYE, bit3 DDE, bit4 EXE, bit5 CME, bit6 URQ,
  bit7 PON. Queuing an error sets the bit of its class: −100…−199 CME, −200…−299 EXE, −300…−399 DDE,
  positive codes DDE, −400…−499 QYE, −500…−599 PON, −600…−699 URQ, −700…−799 RQC, −800…−899 OPC.
  Then STB bit 2 is set.
- **STB** (status byte): bit2 EAV = error queue not empty (set on every queued error, cleared when the
  queue is emptied), bit3 = QUES summary, bit5 = ESB, bit6 = MSS, bit7 = OPER summary.
- **Summary bits are edge-updated.** ESB is recomputed only when ESR *changes value*: then
  bit5 = (ESR & ESE) ≠ 0. Writing `*ESE` does **not** recompute it. So with ESR already 0x30, `*ESE 255`
  followed by another error (ESR unchanged) leaves ESB clear (`*STB?` = 4); `*ESE 32` before the first
  error gives `*STB?` = 36. Reading `*ESR?` clears ESR and thereby ESB. The OPER/QUES summaries (bits 7
  and 3) follow the same rule for their event registers.
- **MSS** is recomputed whenever STB or SRE changes value: bit6 = (STB & SRE & ~0x40) ≠ 0 (`*SRE 4` with
  a queued error → `*STB?` = 68; with ESB set and `*SRE 32` → 100).
- **`*CLS`** clears the error queue, ESR and the OPERation/QUEStionable event registers (not ESE, SRE or
  the enable registers).
- **Event registers:** OPERation and QUEStionable are 16-bit condition/event/enable triples. Which bits the
  instrument sets is **UNVERIFIED**; a firmware may leave them 0.
- **Legacy Siglent status queries:** `INR?` (internal state change register), `ALL_STATUS?`/`ALST?`
  (format `ALST STB,<n>,ESR,<n>,INR,<n>,DDR,<n>,CMR,<n>,EXR,<n>,URR,<n>`), `CMR?`, `EXR?`, `DDR?`.
  Bit meanings follow the older Siglent manuals (**UNVERIFIED** for this model).

A firmware may implement level-triggered summaries (IEEE 488.2) instead; clients that poll `*STB?` for
ESB after changing `*ESE` see the instrument's behaviour only with the edge rule.

### 6.6 Identity

`*IDN?` returns four comma-separated fields:

`<manufacturer>,<model>,<serial>,<version>`

- **manufacturer:** the OEM manufacturer string, default `Siglent`. If it is exactly `Siglent`,
  ` Technologies` is appended, giving `Siglent Technologies`.
- **model:** product-information id 12 (the model name, e.g. `SDS824X HD`, **UNVERIFIED** spelling;
  spec 09 holds the model table). If the stored model is the text `empty`, the field is a single space.
  If it starts with the Cyrillic OEM prefix `АКИП`, that prefix is replaced by the Latin `AKIP`.
- **serial:** product-information id 10 (14-character serial, e.g. `SDS08HBQ7R0123`, form
  **UNVERIFIED**).
- **version:** product-information id 1. The string is built once at start-up as
  `<boot>.<rootfs>.<kernel>.<app>`:

  | Component | Source | Example |
  |---|---|---|
  | `<boot>` | value after `boot_version=` in `/proc/cmdline`, up to the next space, `"` or LF. U-Boot passes `uboot_version=4` on the kernel command line, which this search matches | `4` |
  | `<rootfs>` | integer after `rootfs_version=` in `/etc/version` | `8` |
  | `<kernel>` | value after `siglent_version=` in `/proc/version`, up to `@`; `0` if absent | `14` |
  | `<app>` | the application software version (four dotted numbers) | `1.0.0.0` |

  So the prefix on a unit is `4.8.14.` (**UNVERIFIED** on a unit), followed by the four application
  components. A system without the `boot_version=` and `siglent_version=` strings gives the prefix `.8.0.`.
  A firmware reports its own four-part application version in the last four components and keeps the
  three-component prefix, because host software may parse the last four. The UI's version page shows the same components (spec 09).

**No factory data.** With an empty or missing factory partition the model defaults to `SDS120X` and the
serial to `0123456789` (`Siglent Technologies,SDS120X,0123456789,…`). A replacement must not copy these
placeholders as real identities.

The same strings feed the USB descriptor (§5.1), the telnet banner (§3), LXI (§10) and mDNS TXT records.

### 6.7 Command set

`appendix/scpi-commands.tsv` lists the documented command set: the commands of the public programming
guides, current long form and legacy short form. Columns:

| Column | Content |
|---|---|
| `command` | command header (pattern syntax of §6.1) |
| `query` | query form, `-` if none |
| `kind` | `command`, `query` or `both` |
| `params` | set syntax with parameter names |
| `values` | parameter values and SDS800X HD ranges |
| `reply` | query reply format |
| `sds800xhd` | applicability to this model: `yes` (implement as documented), `guide-only` (below), `no` (not on this model), `unclear` (the guide is ambiguous); rows of the legacy short-form set and of the IEEE 488.2 common commands are marked `legacy` / `standard` |
| `default` | the reply right after a fresh start without a recalled setup; `-` = not known; identity and clock-dependent cells are marked |

**`guide-only` rows** (the newer cursor model `:CURSor:MANual…`, `:CURSor:XY…`, `:CURSor:TRACk<n>…`,
`:CURSor:MEASure<n>…`, and `:MEASure:DITMe<n>…`): the guide lists the command for SDS800X HD, but the
instrument answers −113 (Undefined header). A firmware may implement them; compatibility with existing
clients does not require it.

**Settings of inactive functions.** The instrument ignores, without an error, a setting of a function that is off
(a decode bus that is off, a math channel that is off, a decode threshold whose source channel is off) and clamps
gate positions (`:MEASure:GATE:GB` rule UNVERIFIED); `:WAVeform:SEQuence` keeps frame −1 (reported 4294967295)
until a sequence has been acquired. Clients set the owning function ON first; a firmware may behave the same
way or apply the setting.

Model-specific limits a firmware must enforce:

| Item | SDS800X HD |
|---|---|
| Analog channels | `C1`–`C2` (SDS802X/812X/822X HD) or `C1`–`C4` (SDS804X/814X/824X HD) |
| Memory depth `:ACQuire:MDEPth` | {10k, 100k, 1M, 10M, max}; max = 100M (1 ch) / 50M (2 ch) / 25M (4 ch) on 200 MHz and edu models, 50M / 25M / 25M on the 70/100 MHz models (spec 04 §3); reduced with digital on and in ROLL |
| Bandwidth limit `:CHANnel<n>:BWLimit` | FULL, 20M. `200M` raises no error (whether it changes the setting is **UNVERIFIED**) |
| Input impedance | 1 MΩ only. `:CHANnel<n>:IMPedance FIFTy` is accepted without error and ignored: `IMPedance?` stays `ONEMeg` |
| Coupling | DC, AC, GND |
| Trigger/decode levels and thresholds | ±4.1 div × V/div − offset |
| Trigger time limits | 2 ns – 20 s; holdoff 8 ns – 30 s |
| Horizontal divisions | 10; vertical 8 |
| Decode buses | 2 (IIC, SPI, UART, CAN, LIN; others need options and are not listed for this model) |
| Digital | D0–D15 with the MSO option (SLA1016 probe), threshold groups D0–D7 and D8–D15 |
| Function generator (WGEN, `C1:BSWV` …) | only with an external SAG1021I; no on-board AWG (spec 15 §1) |

#### 6.7.1 Instrument behaviour of acquisition and trigger commands

How the instrument behaves where it differs from the guide. A firmware should behave the same unless a row
says otherwise. Hardware effects are in specs 03 and 05.

| Command | Behaviour |
|---|---|
| `:ACQuire:TYPE {AVERage[,<n>]\|ERES[,<bits>]\|HIRES[,<bits>]}` | Average/ERES are not acquisition modes on this product (spec 03 §6.1: only math operators `:FUNCtion<x>:OPERation AVERage\|ERES`). The command is parsed (the count/bits must be a valid choice, else `-224`; missing → `-200`), then ignored: no register write, no error queued, `:ACQuire:TYPE?` keeps the previous mode (NORMal or PEAK). Example: after `PEAK`, `AVERage,16`, `ERES,2.0` → `TYPE?` = `PEAK`, error queue empty; `AVERage` or `HIRES` without the second parameter → `-200`. A firmware must accept these forms the same way, or queue `-221,"Settings conflict"`; it must not report AVERage/ERES in `TYPE?`. |
| `:ACQuire:MODE YT` after `XY` | Leaves XY and returns to the horizontal mode that was active before XY: with roll on before XY, `:ACQuire:MODE?` then answers `ROLL`, not `YT`. A firmware should do the same (or switch to YT and document it). |
| `:TIMebase:SCALe <t>` | The instrument moves to the requested time/div unless it is below the minimum of spec 04 §2, which it evaluates for both the rate in use and the rate of the requested setting and takes the larger enum. A large jump from a slow time base can therefore land above the true minimum (1 ch 10k at 1 ms/div, 1 MSa/s: `:TIM:SCAL 2E-7` → `:TIM:SCAL?` = `5.00E-07`). No error is queued. A firmware uses the requested setting's rate only (spec 04 §2), so its `:TIM:SCAL?` after such a jump may differ from the instrument's answer. |
| `:TIMebase:DELay <τ>` | Clamped to the delay range of spec 09 §3.3 (while running −5 … +10 000 div × t/div; another range while a record is held); no error is queued. |
| `:ACQuire:INTerpolation {ON\|OFF}`, `:ACQuire:AMODe {FAST\|SLOW}` | Register effects in spec 09 §3.3 (ACQ 0x081 bit 0, ACQ 0x1EE). |
| `:TRIGger:MODE FTRIG` | One-shot: forces one trigger, ≈ 70–150 ms later the previous sweep mode is re-programmed. `:TRIGger:MODE?` then answers the previous mode (SINGle, NORMal or AUTO), never `FTRIG`. |
| `:TRIGger:STATus?` | The software trigger state: `Arm`, `Ready`, `Auto`, `Trig'd`, `Stop`, `Roll`, `FStop`, `Run` or `Pause` (state codes 0–8 in that order; no reply and `-200` if the state cannot be read). Examples: `Arm` in AUTO, `Stop` after SINGle + FTRIG, `Roll` in roll mode, `Stop` after SINGle, a further RUN and STOP, `Arm` after returning to AUTO. The legacy `TRST?` answers `ON` in state `Trig'd`, else `OFF`, with the CHDR header of §6.3.2 (`TRST`/`TRIG_STATUS`). |
| `:TRIGger:<type>:SOURce C<n>` with channel n off | Refused, no error queued. The request is treated as "next source": from C1 with only C1 on, `C2` lands on `LINE` (query → `LINE`); further requests for channels that are off write nothing. |
| `:TRIGger:EDGE:SOURce {EX\|EX5}` | No external trigger input on this product: refused like an off channel, the source is unchanged. Example: `EX` and `EX5` from C1 → `SOURce?` = `C1`, no error queued. `:TRIGger:EDGE:IMPedance` is stored and echoed (`FIFTy` → `FIFTy`) but has no effect. A firmware may answer `-224` instead. |
| `:TRIGger:SPI:CLK:LATChedge` (guide spelling) | `-113` Undefined header. The instrument accepts `:TRIGger:SPI:LATChedge {RISing\|FALLing}`. A firmware should accept both. |
| `:TRIGger:DELay:SOURce` | Delay event A is a **pattern state list** (`H`, `L`, `X` per source C1…Cn then D0…D15; only sources that are on are applied), e.g. `:TRIG:DEL:SOUR H,L,X,X`. A channel name (`C2`) gives `-224`. Event B is `:TRIGger:DELay:SOURce2 <ch>`. |
| `:TRIGger:<type>:HOLDoff EVENts`, `…:HLDEVent`, `…:HSTart` | Accepted and stored. EVENts acts as TIME (time holdoff with the stored `HLDTime`); the event count and holdoff start have no effect (spec 05 §6). |
| `:TRIGger:WINDow:TYPE RELative`, `CLEVel`, `DLEVel` | Accepted; UI-only. The centre/delta levels are converted to the absolute high/low levels (spec 05 §5.7). |
| `:TRIGger:MODE SINGle` | After the single frame the instrument stops the acquisition itself; `:TRIGger:STATus?` then answers `Stop`, also after a further `:TRIGger:RUN` (one more frame). |
| `:ACQuire:SEQuence ON` | Also switches the trigger sweep from AUTO to NORMal (`:TRIGger:MODE?` → NORMal; it stays NORMal after `SEQuence OFF`). With sequence on, `:TRIGger:MODE SINGle` and `FTRIG` program nothing (spec 05 §7). After the N segments the acquisition is stopped (spec 03 §6.2). |
| `:HISTORy ON` / `OFF` | ON stops the acquisition and shows the held frame (`:TRIGger:STATus?` → `Stop`); OFF resumes run mode (spec 03 §6.3). |
| `:HISTORy:TIME?` | Reply `hh: mm: ss. uuuuuu` — two-digit hours, minutes and seconds and six-digit microseconds, with a space after each `:` and after the `.` (e.g. `02: 53: 59. 000000`), no header; the time of day (wall clock, UTC) of the shown frame. Only while history is on; otherwise no reply (UNVERIFIED which error is queued). |

**Legacy set:** the instrument also accepts the legacy Siglent short-form set. The bundled web UI uses it:
`TDIV`, `TRDL`, `C<n>:TRA`, `C<n>:VDIV`, `C<n>:OFST`, `C<n>:CPL`, `C<n>:BWL`, `C<n>:ATTN`, `TRSE`,
`TRSL`, `TRLV`, `TRLV2`, `TRMD`, `TRCP`, `TRNR`, `MSIZ`, `SANU`, `SARA`, `ACQUIRE_WAY`, `XY_DISPLAY`,
`PERSIST_SETUP`, `PACU`, `PAVA? CUSTn`, `ARM`, `STOP`, `INR?`, `CHDR`, `BUZZ`, `SCDP`, `C<n>:WF?`,
`WFSU`, `TMPL?`. A firmware that wants the bundled web UI to work must implement at least the commands
in §11.

---

## 7. Waveform transfer

### 7.1 Setup commands

| Command | Meaning |
|---|---|
| `:WAVeform:SOURce {C<n>\|F<x>\|D<d>}` | Source of the next PREamble/DATA (`C1`–`C4`, math `F1`–`F4`, digital `D0`–`D15`). |
| `:WAVeform:WIDTh {BYTE\|WORD}` | Sample size. On this 12-bit model the data words are 16 bits; WORD must be used for full resolution (§7.4). |
| `:WAVeform:BYTeorder {LSB\|MSB}` | Byte order of WORD data (default LSB). `:WAV:BYT LSB` sent on its own works after MSB. (The compound message `:WAV:BYT LSB;WIDT BYTE` returns −200 with the order left at MSB; clients should send BYTeorder as its own message.) |
| `:WAVeform:STARt <n>` | First point index (0-based) within the record (the WAVEDESC field "FIRST_POINT"). |
| `:WAVeform:INTerval <n>` | Take every n-th point (sparsing; 1 = all). |
| `:WAVeform:POINt <n>` | Number of points to return; 0 = all from STARt (default 0). DATA? clips the request (§7.3); with POINt 0 the query `:WAV:POIN?` still answers `0` afterwards. |
| `:WAVeform:MAXPoint?` | Largest number of points a single DATA? returns (NR1). Value for an analog source in BYTE mode: **5000000** at every memory depth 10k…10M; it may depend on source and width (**UNVERIFIED** for WORD, math and digital). Larger records must be read in pieces with STARt/POINt. (Readout detail: spec 03 §4.3 "Long records"; it does not affect the returned points.) |
| `:WAVeform:SEQuence <frame>,<start>` | Sequence-mode frame selection (frame 0 = all frames, starting at `<start>`). Default frame −1 (none), start 1; the query prints the frame unsigned: `4294967295,1`. |
| `:WAVeform:PREamble?` | 346-byte descriptor, 362 bytes in sequence mode (§7.2). |
| `:WAVeform:DATA?` | Data block (§7.3). |

**No valid record.** Outside ROLL mode, PREamble? and DATA? first wait for a valid acquisition record.
They check every 100 ms for up to the read timeout of 1 s. ROLL mode does not wait. If the timeout expires, or if no waveform can be read, they answer in
the legacy form (§7.5), with the header from `CHDR` (default `<src>:WF`):

- PREamble?: `<src>:WF DESC,#9000000346` + 346 bytes + message LF. The descriptor is stale or partly
  undefined (WAVE_ARRAY_COUNT = 0, 120 = −2, 128 = −1).
- DATA?: `<src>:WF DAT2,#9000000000` + LF + message LF, i.e. `C1:WF DAT2,#9000000000\n\n`.

Clients must treat WAVE_ARRAY_COUNT = 0 or a zero-length block as "no data".

**Reading while acquiring.** Before it reads, DATA? brings the source into a readable state: an analog source that is off is switched on (then 100 ms settle; a digital source: the
digital bus and the line are enabled, 500 ms); DVM and the wave histogram are switched off for the read;
and if the trigger is running and the acquisition is not already stopped, the acquisition is paused for
the read and restarted afterwards. DVM, histogram and the run
state are restored after the read; whether a source switched on for the read is switched off again is
**UNVERIFIED**. The read itself uses the last complete record.

- **Clients** commonly send `:TRIGger:STOP` before DATA? (and `:TRIGger:RUN` afterwards), because DATA?
  while the trigger is running is not reliable on every instrument (it may block and drop the connection;
  **UNVERIFIED** on a unit). A firmware must accept that sequence.
- **A firmware must** answer DATA? while running without stopping the user's run state: freeze one
  complete record for the duration of the transfer (the run-mode readout cycle of spec 03 §3.6: stop request,
  read, re-arm; or copy the last complete frame), transfer it, then resume; the read must not block longer than
  `:WAVeform:TOUT`, and `:TRIGger:STATus?` after the read reports the state from before the read.
  Successive pieces read with STARt/POINt while running may come from different frames unless the client
  stops first; a firmware should keep the same frame for consecutive DATA? queries that only change
  STARt.

Errors in the request make DATA? answer an ASCII error text instead of a block: the text, then the
message LF, no `#` header and no command header (e.g. `:WAV:STAR 30000000` on a 20 000-point record →
`fp > current memory depth!\n`). Texts: `fp > current memory depth!`, `sn > acquired sequence num!`,
`ss > acquired sequence num!`, `no sequence frame acquired!`, `not support eres/average`,
`fp > current math points!`.

### 7.2 Preamble

Outside sequence mode, `:WAVeform:PREamble?` answers with `#9000000346`, then 346 descriptor bytes, then
the message `\n`. With sequence acquisition enabled, the block is `#9000000362`: the 346-byte descriptor,
with 144 and 148 taken from the sequence parameters, followed by the 16-byte trigger time of the selected frame
at offsets 346–361 in the LeCroy TIME_STAMP layout: float64 seconds (with fraction), uint8 minutes, uint8 hours,
uint8 day, uint8 month, int16 year, int16 0. Example (`:ACQ:SEQ:COUNt 37`, `:WAV:SEQuence 1,1`):
`00000000 00804d40 35 02 05 0b ea07 0000` = 59.0 s, 53 min, 2 h, day 5, month 11, 2026 — the wall-clock (UTC)
time of the frame; 140 = 1, 144 = 1, 148 = 37, 174 = 1. Whether a unit fills the seconds' fraction from the PL
timestamp (spec 03 §6.3) is UNVERIFIED.
The long-form PREamble? has **no command header** in any `CHDR` mode.
The descriptor follows the LeCroy WAVEDESC layout (template "WAVEACE"). All multi-byte fields are
**little-endian**. The descriptor is zeroed before it is filled, so fields not listed here are zero.

| Offset | Type | Field | Value |
|---|---|---|---|
| 0 | char[16] | DESCRIPTOR_NAME | `WAVEDESC`, NUL-padded |
| 16 | char[16] | TEMPLATE_NAME | `WAVEACE`, NUL-padded |
| 32 | int16 | COMM_TYPE | 0 = BYTE, 1 = WORD (`:WAVeform:WIDTh`) |
| 34 | int16 | COMM_ORDER | 0 = LSB, 1 = MSB (`:WAVeform:BYTeorder`) |
| 36 | int32 | WAVE_DESCRIPTOR | 346 |
| 40–59 | int32×5 | USER_TEXT…RES_ARRAY1 | 0 |
| 60 | int32 | WAVE_ARRAY_1 | Bytes of sample data: analog = record points × (1 + COMM_TYPE); digital = ⌊digital record points / 8⌋ (truncated). For digital sources, 112, 116, 120 and 128 also use the digital record length. |
| 64–75 | | | 0 |
| 76 | char[16] | INSTRUMENT_NAME | `Siglent SDS` |
| 92 | int32 | INSTRUMENT_NUMBER | 0xCDAB (52651) |
| 112 | int32 | (reserved) | record points |
| 116 | int32 | WAVE_ARRAY_COUNT | record points (per frame in sequence mode) |
| 120 | int32 | PNTS_PER_SCREEN | record points − 2 |
| 124 | int32 | FIRST_VALID_PNT | 0 |
| 128 | int32 | LAST_VALID_PNT | record points − 1 |
| 132 | int32 | FIRST_POINT | `:WAVeform:STARt` |
| 136 | int32 | SPARSING_FACTOR | `:WAVeform:INTerval` |
| 140 | int32 | SEGMENT_INDEX | selected sequence frame (`:WAVeform:SEQuence` frame; −1 = default outside sequence mode) |
| 144 | int32 | frames in this transfer | Outside sequence mode: the `:WAVeform:POINt` value. In sequence mode: the frames in this transfer (e.g. 1 for `:WAV:SEQuence 1,1`) |
| 148 | int32 | frames acquired | 1 outside sequence mode. In sequence mode: the frames acquired (e.g. 37 = `:ACQ:SEQ:COUNt` after a completed sequence) |
| 152 | int32 | | 0 |
| 156 | float32 | VERTICAL_GAIN | V/div of the source without probe factor (V) |
| 160 | float32 | VERTICAL_OFFSET | Offset without probe factor (V) |
| 164 | float32 | codes per division (C) | **7680.0** in both BYTE and WORD mode: codes per division of the 16-bit data scale (§7.4) |
| 168 | float32 | | 0 |
| 172 | int16 | NOMINAL_BITS | **16** (data word width, not the 12-bit ADC resolution) |
| 174 | int16 | NOM_SUBARRAY_COUNT | selected sequence frame, as int16 (−1 = default) |
| 176 | float32 | HORIZ_INTERVAL | 1 / sample rate (s). Digital sources use the digital sample rate. |
| 180 | float64 | HORIZ_OFFSET | Trigger delay (s): time from the trigger to the screen centre reference |
| 188 | float64 | PIXEL_OFFSET | Copy of HORIZ_OFFSET |
| 196 | char[48] | VERTUNIT | `V` |
| 244 | char[48] | HORUNIT | `S` |
| 292 | float32 | HORIZ_UNCERTAINTY | 1.0e-9 |
| 296–311 | struct | TRIGGER_TIME | 0 |
| 312 | float32 | ACQ_DURATION | 0 |
| 316 | int16 | RECORD_TYPE | 0 |
| 322 | int16 | RIS_SWEEPS | 1 |
| 324 | int16 | TIMEBASE | Time/div enum (guide Table 2: 0 = 200 ps … 38 = 1000 s, 1-2-5 steps; spec 04 holds the model's range) |
| 326 | int16 | VERT_COUPLING | 0 = DC, 1 = AC, 2 = GND |
| 328 | float32 | PROBE_ATT | probe attenuation factor (1.0 for math/digital) |
| 332 | int16 | FIXED_VERT_GAIN | V/div enum index (spec 06) |
| 334 | int16 | BANDWIDTH_LIMIT | 0 = off, 1 = 20 MHz |
| 336 | float32 | VERTICAL_VERNIER | 1.0 |
| 340 | float32 | ACQ_VERT_OFFSET | same as 160 |
| 344 | int16 | WAVE_SOURCE | source enum: 0–3 = C1–C4, 8–11 = F1–F4, 20–35 = D0–D15, 52–55 = reference/memory waveforms (**UNVERIFIED** which reference maps to which value) |

For an FFT math source, 60 holds 4 × FFT points (float32 data). 112 and 116 hold the FFT point count,
120 and 128 hold count − 2 and count − 1. The vertical fields come from the FFT settings.

### 7.3 Data block

`:WAVeform:DATA?` returns:

- an IEEE 488.2 definite-length block `#<d><len>`, where `<d>` is the number of decimal digits of
  `<len>` (variable, e.g. `#41000`, `#72000000`);
- the sample bytes;
- a terminator of **two LF bytes**: the handler appends one LF after the samples, and the message
  terminator adds the second (`…<data>\n\n`).

The long-form DATA? has **no command header** in any `CHDR` mode (e.g. SHORT and OFF: `#3200…`); only
the legacy forms (§7.5) carry `<src>:WF DAT2,`.

**Size limit.** A single DATA? returns at most `:WAVeform:MAXPoint?` points (for example 5 000 000
at every memory depth). If POINt is 0, or if MAXPoint < POINt × INTerval, the request becomes
⌊MAXPoint / INTerval⌋ points; the block then holds the points that exist from STARt (e.g. POINt 0 on a
20 000-point record → `#520000` + 20 000 bytes). There is no byte cap: in WORD mode the block can exceed 16 MiB. The client reads
larger records with successive STARt values.

Sample order is oldest first, from index STARt, every INTerval-th point, POINt points.

- **Stored sample.** Each record word is a 16-bit value on the 16-bit data scale:
  screen centre **0x8000**, screen limits 0x0800…0xF800, 7680 codes per division, i.e. the 12-bit ADC code
  × 16 (12-bit screen 0x080…0xF80, centre 0x800, 480 codes/div; spec 06 §7.1). The transfer does no
  shift or conversion of its own. How the PL record word relates to the raw 12-bit code is defined by
  spec 03 §5 (**UNVERIFIED** there; the transfer is only correct if the word is the code × 16).
- **Analog, BYTE:** one signed 8-bit code per point: the high byte of the stored 16-bit sample minus the
  8-bit screen-centre code 0x80 (30 codes per division).
- **Analog, WORD:** one signed 16-bit code per point, in BYTeorder: the stored 16-bit sample with its high
  byte reduced by 0x80 (i.e. sample − 0x8000); the low byte is passed through unchanged (7680 codes per
  division). If the record is stored as 8-bit samples, WORD returns code × 256 (low byte 0).
- **Digital (`D<d>`):** 1 bit per point, packed LSB-first: point *i* is bit (*i* mod 8) of byte ⌊*i*/8⌋.
  A last partial byte is zero-padded.
- **Math:** same encodings as analog. FFT data is described by the FFT fields.

### 7.4 Scaling

```
volts(i)  = code(i) × VERTICAL_GAIN / C − VERTICAL_OFFSET
time(i)   = −(TIMEBASE_s × 10 / 2) + HORIZ_OFFSET + (FIRST_POINT + i × SPARSING_FACTOR) × HORIZ_INTERVAL
```

- `code` is signed and centred on 0; screen centre is code 0.
- C (field 164) is always **7680** = (0xF800 − 0x0800) / 8, the 16-bit scale, also in BYTE
  mode. WORD data: divide by 7680. **BYTE data: divide by 7680 / 256 = 30.** (0xF70 is the UI's drawable
  maximum, not a screen-code limit.)
- `TIMEBASE_s` is the time/div in seconds; the horizontal grid is 10 divisions.
- Probe attenuation is not included in VERTICAL_GAIN/VERTICAL_OFFSET; multiply volts by PROBE_ATT for
  probe-referred values (**UNVERIFIED** sign/inclusion).

### 7.5 Legacy waveform commands

The instrument also accepts the legacy LeCroy-style forms:

- **`C<n>:WF? <block>` / `C<n>:WAVEFORM?`:** also `MATH:WF?`, `F<x>:WF?` and `D<d>:WF?`. `<block>` is
  `DESC`, `TIME`, `DAT1`, `DAT2`, `TEXT` or `ALL`, and the answer is `C<n>:WF <block>,#9<9 digits><data>`
  + message LF (e.g. `C1:WF? DESC` in CHDR SHORT: `C1:WF DESC,#9000000346` + 346 bytes + LF, 369 bytes).
  The `C1:WF ` header follows `CHDR` like other legacy replies (§6.3.2; LONG/OFF forms **UNVERIFIED**:
  expected `C1:WAVEFORM ` / none).
- **`WFSU` / `WAVEFORM_SETUP SP,<n>,NP,<n>,FP,<n>`:** sets sparsing, number of points and first point.
- **`TMPL?` / `TEMPLATE?`:** returns a LeCroy-format WAVEDESC template text describing §7.2. Framing in
  CHDR SHORT: `TMPL "`, CR LF, then the template (CR LF line ends, last line `ENDTEMPLATE`), then `"` CR LF
  and the message LF. A firmware that implements `TMPL?` returns its own template text in this framing.
  Clients use it only for display.

Their DESC block is the same 346-byte descriptor. A firmware may omit these legacy forms; the guide
does not list them for this model.

## 8. Screen dump

`:PRINt? {BMP|PNG}[,{NORMal|INVerted}]` captures the 1024 × 600 display and returns the image file
bytes. The capture is taken 1 s after the command so the display can settle. INVerted inverts the colours. The
legacy forms are `SCDP` (BMP) and `PNGDP`.

- **No block header:** the image is returned as raw bytes, without a `#` header, followed by the
  message `\n`.
- **BMP format** (header observed, `:PRINt? BMP` and `SCDP` identical): **2 457 658 bytes**, then the
  message LF. Layout, little-endian:

  | Offset | Field | Value |
  |---|---|---|
  | 0 | bfType | `BM` |
  | 2 | bfSize | 2 457 658 (0x25803A) |
  | 6 | reserved | 0 |
  | 10 | bfOffBits | **58** |
  | 14 | biSize | 40 (BITMAPINFOHEADER) |
  | 18 / 22 | width / height | 1024 / **−600** (top-down rows) |
  | 26 / 28 | planes / bitcount | 1 / **32** |
  | 30 | compression | 0 (BI_RGB) |
  | 34 | biSizeImage | 2 457 600 |
  | 38–53 | resolution, colour counts | 0 |
  | 54–57 | padding | 0 |
  | 58 | pixels | 1024 × 600 × 4 bytes, rows top to bottom |

  Pixel byte order inside the 4 bytes is **UNVERIFIED** (BMP convention B, G, R, 0 expected). A firmware should write B, G, R, 0.
- **PNG format:** a firmware must return a standard PNG file: 1024 × 600, IHDR bit depth 8, colour
  type 2 (RGB, no alpha), no interlace, default compression and filters, rows top to bottom, then the
  message LF. The pixel source is the same capture as BMP (INVerted inverts the colours). Existing
  instruments may answer `:PRINt? PNG[,…]` and `PNGDP` with **only the message LF** (zero image bytes,
  no error queued; **UNVERIFIED** on a unit), so clients that need an image reliably use BMP.

The web UI takes its screenshots with `SCDP` (§11).

## 9. File transfer

The guide documents no SCPI file-transfer commands for this model. Saved files live under the user
partition and can be fetched over FTP or HTTP.

## 10. LXI and mDNS

- **LXI version:** the LXI identification states `1.5 LXI Device Specification 2016`.
- **Identification document:** the firmware writes `/tmp/identification.xml`, which is served at
  `http://<ip>/lxi/identification` with `Content-type: text/xml`. In the served document the root
  element's `xsi:schemaLocation` points at `http://<server-addr>/lxi/identificationSchema`.
  Document fields:
  - `Manufacturer` `SIGLENT TECHNOLOGIES`; `Model`, `SerialNumber` and `FirmwareRevision` from §6.6;
  - `ManufacturerDescription`;
  - `HomepageURL` and `DriverURL` `http://www.Siglent.com`;
  - `UserDescription`, `IdentificationURL`;
  - one `Interface` element: `InterfaceType="LXI"`, `IPType="IPv4"`, `InterfaceName="et0"` (sic).
    It carries two `InstrumentAddressString` entries, `TCPIP::<ip>::INSTR` and
    `TCPIP::<ip>::inst0::SOCKET`, plus `Hostname`, `IPAddress`, `SubnetMask`, `MACAddress`, `Gateway`,
    `DHCPEnabled` and `AutoIPEnabled`;
  - `IVISoftwareModuleName`, `LXIVersion`.
- **LXI state file:** the firmware writes `Running` to `/tmp/lxi_module.state` once LXI is up. The web
  pages show it.
- **mDNS:** the firmware (re)starts avahi-daemon (IPv4+IPv6, wide-area, no hinfo/workstation records)
  with `host-name=` set to the LXI host name, and writes one service file per service,
  `/etc/avahi/services/<name>.<type>.service`. The service name is the LXI mDNS description.

  | Type | Port | TXT records |
  |---|---|---|
  | `_http._tcp` | 80 | `txtvers`, `path=/` |
  | `_lxi._tcp` | 80 | `txtvers`, `Manufacturer`, `Model`, `SerialNumber`, `FirmwareVersion` |
  | `_vxi-11._tcp` | 111 | `Address=TCPIP::<ip>::inst0::INSTR` |
  | `_scpi-raw._tcp` | 5025 | `Address=` |
  | `_scpi-telnet._tcp` | 5024 | `Address=<ip>:5024` |

  The `txtvers` value is **UNVERIFIED** (1 expected).
- **Auto-IP:** auto-IP (`avahi-autoipd`, 169.254/16) and DHCP are driven by the LAN settings (spec 09).
- **SCPI:** LAN identity and LXI controls are `:SYSTem:COMMunicate:LAN:{HOSTname, DESCription,
  IDENtify, DNS, …}` and `:SYSTem:LXI[?]` (appendix).

## 11. Web server

- **Server:** HTTP on port 80, serving the web UI and `/lxi/identification` (§10). The request size is
  effectively unlimited (firmware `.ADS` upload).
- **Functions:** LAN settings (shows the MAC address), free SCPI entry, login/logout, firmware update
  upload, remote screen (noVNC, §12) and LXI identification.
- **Password:** when `/usr/bin/siglent/usr/webserver_password` is non-empty, the pages require a login
  session.
- **Instrument access:** the bundled web UI reaches the instrument only as a **VXI-11 client** on
  `127.0.0.1`, device `inst0` (§4), and sends legacy SCPI:

  | Function | SCPI sent |
  |---|---|
  | read/write settings | `TDIV`, `TRDL`, `C<n>:TRA/VDIV/OFST/CPL/BWL/ATTN`, `TRLV`, `TRSE`, `TRSL`, `TRMD`, `PACU`, `PAVA? CUSTn`, `ACQUIRE_WAY`, `MEMORY_SIZE`, `XY_DISPLAY`, `PERSIST_SETUP`, `TRIG_COUPLING`, `TRNR`, `TRLV2`, … (queries with `?`) |
  | screenshot | `SCDP` (§8) |
  | auto setup, default setup | auto setup, default setup |
  | SCPI entry | free SCPI text |
  | firmware update | firmware update trigger |

- **Firmware:** a firmware can keep the bundled web stack unchanged if it serves VXI-11 on loopback and
  implements the legacy commands listed. It can also replace the stack entirely.

## 12. VNC

- **Server:** RFB 3.8, no password, desktop name `Instrument`, on the port of
  `:SYSTem:COMMunicate:VNCPort` (default 5900). The same port also serves WebSocket for noVNC.
- **Frame source:** 1024 × 600, read from the capture buffer at physical `0x1F800000` (RGB565,
  1024 × 600 × 2 bytes). The UI VDMA's S2MM channel writes a copy of the composited LCD image there
  (spec 07 §4.2: S2MM start 0x1F800000, stride/hsize 0x800, vsize 600; the PL→PS stream is started by
  writing 0 then 1 to 0x80040560, then 1 to 0x800406c0).
- **Updates:** `poll()` on `/dev/siglent_vnc` for `POLLPRI` (events = 2) returns once per captured frame
  (IRQ GIC SPI 0x68, spec 14); each event is a full-screen update.
- **Pointer input:** pointer events are injected as touches into the touch-panel input device, found by
  its name `goodix-ts` among `/dev/input/event0..4`, using its ABS_X/ABS_Y ranges. The `input_event`s
  written into that event node:
  - **Press (button 1):** `ABS_MT_TRACKING_ID 0`, `ABS_MT_POSITION_X`, `ABS_MT_POSITION_Y`,
    `ABS_MT_TOUCH_MAJOR 20`, `ABS_MT_WIDTH_MAJOR 20`, `SYN_REPORT`.
  - **Drag:** X, Y, SYN.
  - **Release:** `ABS_MT_TRACKING_ID −1`, SYN.
  - **Coordinate mapping:** x' = xmin + (xmax − xmin)·x/1024, y' likewise over 600. With a rotated panel
    (y range larger than x range) the axes are swapped and rescaled.
  - **Wheel:** scroll buttons move Y by ±5 (10 steps).
- **Key input:** key events become Linux key codes (US layout mapping, digits, letters, keypad, Enter,
  BackSpace, …), sent on a uinput keyboard device (bus 3) with key codes 0–255 enabled.

A firmware may implement RFB any way it likes. Only the port, the 1024 × 600 frame and input
equivalence matter to clients.

---

## Open

- The exact `*IDN?` model and serial spelling, the version prefix on a unit (`4.8.14.` expected), and the
  USB `iProduct` string. Settle with `*IDN?` and `lsusb -v` on a unit.
- Whether a unit fills the TIME_STAMP seconds fraction (§7.2) from the PL timestamp.
- `:WAVeform:MAXPoint?` for WORD, math and digital sources.
- The PL record-word format (12-bit code × 16 as the transfer assumes, or right-aligned; spec 03 §5).
- Which three option types `*OPT?` reports, and its reply with options installed.
- Whether `:CHANnel<n>:BWLimit 200M` changes the stored setting.
- Pixel byte order of the `:PRINt? BMP` image.
- Kernel replies to the USBTMC class requests (INITIATE_CLEAR, READ_STATUS_BYTE, …), and how long the
  instrument waits for output on REQUEST_DEV_DEP_MSG_IN. Settle with a USB capture on a unit.
- Which OPERation/QUEStionable bits the instrument sets, and the legacy INR bit map.
- The mDNS `txtvers` value and the service names.
- How many raw-socket clients can be connected at once: the instrument serves one at a time; behaviour
  with queued clients is not yet confirmed on a unit.
- Defaults in the tsv `default` column are a fresh start without `factory_setting.xml`; the *Default* key
  recalls `factory_setting.xml`, whose content on a unit may differ.
