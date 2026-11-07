# 13 — FPGA Configuration

This spec defines the PL bitstream, how it is loaded at boot and from Linux, the clock-chip (PLL) programming
that must precede it, and the display-path setup that must follow it. It is complete enough for firmware to
perform the whole sequence itself.

---

## 1. Required order

| Step | What | Section |
|---|---|---|
| 0 | FSBL configures the PL with the BOOT.bin bitstream (boot-time image, spec 12 §2; configured before the clock chip) | §3.1 |
| 1 | Clock chip programmed over `/dev/spidev1.0` | §4 |
| 2 | Backlight off: PL 0x80040058 ← 0; wait 0.5 s | §5.5 |
| 3 | `insmod xilinx_dma.ko` (optional, §5.4) | §5.4 |
| 4 | Full PL reconfiguration with the runtime bitstream through fpga_manager | §3.3 |
| 5 | UI VDMA (MM2S), waveform VDMA, VNC capture (UI VDMA S2MM) programmed | §5.1–5.3 |
| 6 | LCD reset pulse, brightness, LCD enable | §5.5 |
| 7 | 1 s settle before anything else uses the PL | — |
| 8 | Kernel modules loaded (spec 14), app started; once the UI framebuffer holds the application's first frame, the UI VDMA is pointed at `/dev/fb0` memory | §5.1 |

The clock chip **must** be programmed
before the PL bitstream that uses its clocks is started; the PL **must** be configured before any PL register
(0x8000_0000…) is accessed; the VDMAs **must** be configured after every PL load (reconfiguration resets them).
Step 2 is a PL access on the boot-time (FSBL) bitstream, which decodes MAP 0x016; this is allowed. What is
forbidden is any access while the PL is unconfigured or being reconfigured (from the start of step 4 until its
`state` check passes): unmap or quiesce every PL mapping before step 4.

## 2. Device identity

- Every bitstream (BOOT.bin copy and runtime file) writes IDCODE **0x04A43093** (revision field 0) to the
  configuration port. This is the PL configuration-port IDCODE, which differs from the JTAG/CSU IDCODE listed in
  AMD UG1085 Table 1-2 (U-Boot's table uses the latter and cannot name the chip). The device rejects a bitstream
  whose IDCODE differs from its own.
- Configuration frame data: 14 964 frames × 93 words (UltraScale+ frame length), uncompressed; total bitstream
  5 568 668 bytes — the ZU2/ZU3 die.
- Part identification (IDCODE family mapping, PS configuration, package): spec 12 §1 — XCZU2CG-2SFVC784; ZU2 rather
  than ZU3 is **UNVERIFIED**. Reading CSU `IDCODE` (0xFFCA0040) on a unit settles it: 0x04711093 =
  ZU2, 0x04710093 = ZU3 (bits 31:28 are a revision field; compare bits 27:0).
- Firmware that loads the bitstream installed on the unit does not need the part name.

## 3. Bitstream

### 3.1 Files

| Copy | Location | Byte order | Loaded by |
|---|---|---|---|
| boot-time | BOOT.bin partition 1 | each 32-bit word byte-reversed (little-endian words) | FSBL at power-up |
| runtime | `/usr/bin/siglent/config/fpga/eva_top_fpga.bin` (p10), 5 568 668 bytes | big-endian words (raw `.bit` payload without the `.bit` header) | Linux fpga_manager |

The runtime copy must still be loaded after the clock chip is programmed (§1), because the boot-time copy was
configured before the ADC clocks were valid (spec 12 §2).

### 3.2 Runtime file format

The standard UltraScale+ full `.bin` layout: 0xFF padding, the bus-width detection words `000000BB 11220044`,
0xFF padding, the sync word `AA995566`, type-1 configuration packets (including IDCODE ← 0x04A43093 and the
options of spec 12 §2), a type-2 FDRI write of 1 391 652 words (frame data), CRC, startup and desync commands,
then NOOP (0x20000000) padding.

No encryption, no authentication, no compression, single image (no partial regions).

### 3.3 Loading from Linux (fpga_manager)

The kernel's ZynqMP fpga_manager (`CONFIG_FPGA_MGR_ZYNQMP_FPGA`, DT `pcap` node) passes the image to the PMU
firmware, which streams it through the PCAP. Procedure (all as root):

1. The file must be reachable by the kernel firmware loader: place it (or a symlink) in `/lib/firmware/`, e.g.
   `ln -sf /usr/bin/siglent/config/fpga/eva_top_fpga.bin /lib/firmware/` (needs `/` writable; §6), or set
   `/sys/module/firmware_class/parameters/path` to a directory on p12 or p10 (preferred: it needs no rw remount).
   A symlink may stay in place after the load.
2. `echo 0 > /sys/class/fpga_manager/fpga0/flags` — full reconfiguration, unencrypted, unauthenticated
   (flag bits: 1 = partial reconfiguration; other bits select encryption/authentication — must be 0).
3. `echo eva_top_fpga.bin > /sys/class/fpga_manager/fpga0/firmware` — synchronous; returns after the PL is
   configured (or with an error).
4. Check `/sys/class/fpga_manager/fpga0/state` = `operating`; on anything else the PL is unusable and no PL register
   may be touched.
5. **PL presence check**: read the AXI VDMA VERSION register (offset 0x2C, PG020)
   of `0x8000_0000`; a value of 0 or 0xFFFF_FFFF means the PL is absent or not running (the exact version value of
   this build is UNVERIFIED). The map-PLL registers MAP
   0x004/0x005 are never written or read on this product (spec 02 §9); a replacement must not pulse 0x004, and a
   read of MAP 0x005 bits 0/4/8 as a presence test is UNVERIFIED. The FPGA
   version words ACQ 0x000–0x002 are for display only (spec 02 §3); their values on a unit are **UNVERIFIED** and
   must not be used as a presence test.

Before step 3 no PS master may have an outstanding PL transaction and nothing may access PL registers until step
4 completes: stop every user of `/dev/mem` PL mappings, DMA engines and the VDMAs (at boot, by reloading before any user
starts). Reconfiguration resets all PL registers, so everything in §5 and all
application-level PL setup (spec 02/03) must be redone after a reload.

### 3.4 Boot-time load

FSBL loads partition 1 unconditionally at every power-up; it does not check the clock chip. Therefore the PL runs
on its PS-supplied clocks (FCLK0–3 enabled in the DT) and on the unprogrammed clock chip outputs until §4 runs.

## 4. Clock chip (PLL) programming

### 4.1 Board variant (HWID)

Read 8 strap inputs through sysfs GPIO: for i = 0…7, GPIO number 334 + i (export — skip the export if
`/sys/class/gpio/gpio<N>` already exists, an export of an exported line fails with EBUSY —, direction `in`, read
`value`; leave the lines exported, the application reads them again later);
HWID bit i = value of GPIO 334 + i = PS MIO i (the PS GPIO controller has base 334 under the unit's kernel, spec 12
§4.2; derived from the probe order, **UNVERIFIED** until `/sys/kernel/debug/gpio` is read on a unit — were the base
338, bits 0–3 would be the boot-mode pins and bits 4–7 MIO 0–3). A strap whose `value` file cannot be opened reads
as 0, so an unreadable strap set selects the word-format clock chip, 4-channel.

| HWID test | Result |
|---|---|
| (HWID & 0x30) == 0x30 | **paged clock chip** board: sequence §4.4 |
| otherwise | **word-format clock chip** board: sequence §4.3 |
| HWID & 0x80 set | 2-channel model; clear → 4-channel (the application reads the same byte for this, and bits 3:0 / 7:4 as PCB / BOM version; spec 02 §3) |

Only the straps select the sequence.

### 4.2 Bus

- `/dev/spidev1.0`: PS SPI1 (0xFF050000), chip select 0 = GPIO (PS GPIO line 34, active low), DT max 20 MHz.
- SPI mode 0 (CPOL 0, CPHA 0), 8-bit words, MSB first. Set mode with `SPI_IOC_WR_MODE` = 0 (and read it back
  with `SPI_IOC_RD_MODE`). The speed is the DT default (20 MHz).
- Each `write()` is one chip-select assertion. Nothing is read back; there is no lock polling and no status check.
  Delays below are `usleep` minimums between consecutive transfers.

### 4.3 Word-format clock chip (32-bit words)

13 transfers of 4 bytes, MSB first, no delays. The low 4 bits of each word are the register number (TI LMK0xxxx
word format; part identity **UNVERIFIED**):

`0x00000017`, `0x01030500`, `0x01090101`, `0x01020A02`, `0x01090103`, `0x01030504`, `0x00000007`, `0x0150000A`,
`0x0065012B`, `0xE078078C`, `0x0A04000D`, `0x0323001E`, `0x1C80028F`.

### 4.4 Paged clock chip

Protocol: one register write = four 2-byte transfers, each its own chip-select frame:
`{0x00, 0x01}` (set address = page register 0x01), `{0x40, page}` (write data), `{0x00, reg}` (set address),
`{0x40, value}` (write data). This is the Silicon Labs/Skyworks Si534x/Si538x SPI command set (0x00 set address,
0x40 write data). The page values used (up to 0xAB) exceed the Si5340/41/45 page range; exact part **UNVERIFIED**
(identify from the board, or read the chip's part-number registers with command 0x80 = read data).

Sequence, in order, as `PP:RR=VV` (page, register, value, hex); 549 writes. Writes on a line follow each other
without delay; `wait` lines are delays between the neighbouring writes. A recurring construct (page 0x31 registers
0x38–0x3E, then 0x31:0x3F = 0x55) is a command trigger and is always followed by a wait.

```
2F:E4=85 32:04=80 32:8C=00 32:DA=00 31:2C=1C 31:43=40 31:34=3B 31:35=58
31:36=9D 31:37=74 30:60=0E 30:61=0E 30:62=0E 30:63=0E 30:64=07 30:65=07
30:66=07 30:67=07 30:68=00 30:69=01 30:7A=03 30:08=10 30:15=40 30:26=00
2F:12=0B 06:05=42 31:38=F0 31:39=41 31:3A=19 31:3B=40 31:3C=78 31:3D=7D
31:3E=01 31:3F=55
-- wait 10 ms --
04:5F=04 04:58=46 04:59=C3 04:5A=23 06:04=A2 2F:13=00 07:0A=18
-- wait 2 ms --
07:0A=10 2F:13=01
-- wait 1 ms --
2F:13=02
-- wait 1 ms --
2F:13=03
-- wait 1 ms --
2F:13=04
-- wait 1 ms --
2F:13=05
-- wait 1 ms --
2F:13=06
-- wait 1 ms --
2F:13=07
-- wait 1 ms --
2F:13=08
-- wait 1 ms --
06:04=91 06:23=80 06:23=00 2F:E0=5C 31:38=E0 31:39=F7 31:3A=3A 31:3B=FB
31:3C=9B 31:3D=6F 31:3E=03 31:3F=55
-- wait 1 ms --
86:4C=01 8B:4C=02 90:4C=03 2F:1B=C0 2E:0F=00 2F:1A=3F 2F:19=3F 2F:1D=3F
2F:1C=3F 2E:41=03 2E:45=0C 2E:43=03 2E:47=03 2E:4B=0F 2E:0D=00 2E:13=00
2E:17=05 2E:27=00 2E:25=00 2E:2B=00 2E:29=00 2F:1B=C0 2E:0F=00 2F:1A=3F
2F:19=3F 2F:1D=3F 2F:1C=3F 2E:41=0F 2E:45=3C 2E:43=0F 2E:47=0F 2E:4B=0F
2E:0D=00 2E:13=00 2E:17=05 2E:27=00 2E:25=00 2E:2B=00 2E:29=00 2F:1B=C2
2F:1B=C3 2F:1F=F0 2E:48=00 06:0A=E2 2E:16=01 2E:40=03 2E:42=03 2E:44=0D
2E:46=03 2E:4B=0E 2F:1B=C7 06:0A=E6 2E:16=05 2E:40=0F 2E:42=0F 2E:44=3D
2E:46=0F 2E:4B=0C 2F:1B=CF 06:0A=EE 2E:16=15 2E:40=3F 2E:42=3F 2E:44=FD
2E:46=3F 2E:4B=08 2F:1B=DF 06:0A=FE 2E:16=55 2E:40=FF 2E:42=FF 2E:45=3F
2E:46=FF 2E:4B=00 2F:1B=FF 2E:C4=20 2E:C5=03 A8:81=BC A8:82=A0 A8:83=65
A8:84=01 A8:B8=33 A8:B9=33 A8:BA=33 A8:BB=33 A8:BC=33 A8:BD=01 A8:C0=20
31:38=F1 31:39=00 31:3A=A9 31:3B=00 31:3C=94 31:3D=35 31:3E=77 31:3F=55
-- wait 10 ms --
A9:F6=00 AA:07=80 AA:80=E1 AA:81=F5 AA:83=00 AA:8F=00 AA:B4=C7 AA:B5=1A
AA:B6=19 AA:B7=D7 AA:B8=04 AA:B9=00 AB:07=80 AB:80=E1 AB:81=F5 AB:83=00
AB:8F=00 AB:B4=C7 AB:B5=1A AB:B6=19 AB:B7=D7 AB:B8=04 AB:B9=00 31:38=E4
31:39=AA 31:3A=B4 31:3B=C4 31:3C=04 31:3D=00 31:3E=00 31:3F=55
-- wait 1 ms --
31:38=E4 31:39=AB 31:3A=B4 31:3B=C4 31:3C=04 31:3D=00 31:3E=00 31:3F=55
-- wait 1 ms --
A8:72=10 A8:C0=00 A9:58=16 A9:59=00 A9:5B=00 A9:5C=33 A9:5D=DE A9:5E=07
A9:5F=00 A9:60=1F A9:61=DE A9:62=07 A9:63=00 A9:70=36 A9:71=82 A9:72=00
A9:73=00 A9:74=34 A9:75=A1 A9:76=07 A9:77=00 A9:78=20 A9:79=A1 A9:7A=07
A9:7B=00 A9:A4=52 A9:A5=B4 A9:A6=2C A9:A7=00 A9:A8=DC A9:A9=B3 A9:AA=2C
A9:AB=00 A9:E4=03 A9:E5=12 A9:EA=00 AA:07=80 AB:07=80 06:23=01 06:23=00
06:09=44 06:10=22 06:11=22 2E:CE=B5 2E:D4=70 2E:D5=FF 2E:D6=FF 2E:D7=FF
06:09=CC 06:10=66 06:11=66 2E:DE=B5 2E:E4=70 2E:E5=FF 2E:E6=FF 2E:E7=FF
06:09=DC 06:0A=FF 06:10=EE 06:11=EE 2E:EE=B5 2E:F4=70 2E:F5=FF 2E:F6=FF
2E:F7=FF 32:7C=7D 32:7D=00 32:7E=E2 32:7F=04 32:8C=01
-- wait 10 ms --
32:8C=00 06:0E=FF 06:0F=3F 2F:1E=FF 2F:1F=FF 06:0C=01 06:24=01 06:0E=FD
2F:1E=FD 0B:06=70 0D:89=7F 31:38=F2 31:39=01 31:3A=00 31:3B=03 31:3C=00
31:3D=00 31:3E=00 31:3F=55
-- wait 1 ms --
0B:06=70 0B:06=70 0D:51=06 0B:54=00 0B:6B=00 0D:7A=25 0D:7E=03 06:0E=F9
2F:1E=F9 0E:06=70 10:89=7F 31:38=F2 31:39=02 31:3A=00 31:3B=03 31:3C=00
31:3D=00 31:3E=00 31:3F=55
-- wait 1 ms --
0E:06=70 0E:06=70 10:51=06 0E:54=00 0E:6B=00 10:7A=25 10:7E=03 06:0C=09
06:24=09 06:0E=E9 2F:1E=E9 14:06=70 16:89=7F 31:38=F2 31:39=04 31:3A=00
31:3B=F0 31:3C=00 31:3D=00 31:3E=00 31:3F=55
-- wait 1 ms --
14:06=70 14:06=70 16:51=00 14:54=00 14:6B=00 16:7A=25 16:70=01 16:68=00
16:69=00 16:6A=00 16:6B=00 16:6C=7F 16:6D=00 16:6E=00 16:6F=00 16:60=00
16:61=00 16:62=00 16:63=00 16:64=37 16:65=00 16:66=00 16:67=00 16:70=00
06:0E=C9 2F:1E=C9 17:06=70 19:89=7F 31:38=F2 31:39=05 31:3A=00 31:3B=F0
31:3C=00 31:3D=00 31:3E=00 31:3F=55
-- wait 1 ms --
17:06=70 17:06=70 19:51=00 17:54=00 17:6B=00 19:7A=25 19:70=01 19:68=00
19:69=00 19:6A=00 19:6B=00 19:6C=7F 19:6D=00 19:6E=00 19:6F=00 19:60=00
19:61=00 19:62=00 19:63=00 19:64=37 19:65=00 19:66=00 19:67=00 19:70=00
06:0C=49 06:24=49 06:0C=C9 06:24=C9 06:0D=11 06:25=01 06:0D=13 06:25=03
06:0D=17 06:25=07 06:0D=1F 06:25=0F 31:38=F3 31:39=01 31:3A=36 31:3B=00
31:3C=00 31:3D=00 31:3E=00 31:3F=55
-- wait 1 ms --
2E:B9=15
-- wait 2 ms --
2E:B9=55 0E:3A=10 0E:3A=12 0E:3A=12 14:3A=10 14:3A=12 14:3A=12 17:3A=10
17:3A=12 17:3A=12 31:58=00
-- wait 100 ms --
0B:3A=00 0B:3A=00 0E:3A=10 0E:3A=10 14:3A=10 14:3A=10 17:3A=10 17:3A=10
0B:68=01 0B:64=00 0B:65=00 0E:68=01 0E:64=00 0E:65=00 14:68=01 14:64=00
14:65=00 17:68=01 17:64=00 17:65=00 06:7C=10 06:7C=10 06:7C=10 16:74=10
16:8C=05 16:8C=05 16:8C=07 14:06=10 16:90=04 16:91=01 16:90=14 16:90=1C
16:90=1C 16:91=01 16:90=1C 16:91=00 30:7C=13
-- wait 1 ms --
30:7C=11 06:7C=10 06:7C=10 06:7C=10 19:74=10 19:8C=05 19:8C=05 19:8C=07
17:06=10 19:90=04 19:91=01 19:90=14 19:90=1C 19:90=1C 19:91=01 19:90=1C
19:91=00 30:7C=13
-- wait 1 ms --
30:7C=11 0D:4C=2C 0D:4E=00 0D:86=FF 0D:88=14 10:4C=2C 10:4E=00 10:86=FF
10:88=14 16:4C=2C 16:4E=00 16:86=FF 16:88=14 19:4C=2C 19:4E=00 19:86=FF
19:88=14 2E:C6=0C 0D:4D=40 10:4D=40 16:4D=40 19:4D=40 31:38=F4 31:39=36
31:3A=00 31:3B=00 31:3C=00 31:3D=00 31:3E=00 31:3F=55
-- wait 1 ms --
30:14=10 30:2C=01 30:15=60 30:25=01 0D:7A=24 10:7A=24 16:7A=24 19:7A=24
2E:B0=30 2E:B1=10 2E:B2=30 2E:B3=10 2E:B4=30 2E:B5=10 2E:B6=30 2E:B7=10
2E:BE=AD 2E:BF=00 2E:CE=A5 2E:CF=00 2E:DE=A5 2E:DF=00 2E:EE=A5 2E:EF=00
06:0C=FF 32:04=01 32:05=10 32:D0=00 32:D1=00 32:D2=00 32:D3=00 32:D8=0D
32:D9=84 32:DA=00 32:DB=00 32:DA=01 00:D4=12 00:D5=00 00:D6=00 00:D7=00
00:D8=00 00:D9=00 00:DA=00 00:DB=00 00:D0=FD 00:D1=C2
```

After the last write, wait 10 µs before closing the device. Total time ≈ 0.25 s.

### 4.5 When to (re)program

Once per power-up, before §3.3. Reprogramming while the PL is configured changes the PL/ADC clocks under the
running design; if a replacement must do it (e.g. recovery), it **must** afterwards reload the PL (§3.3) and redo
§5 and the ADC bring-up (spec 06). The 2-channel/4-channel and board-variant decisions are also needed by the
application (spec 09).

## 5. Post-load setup

All accesses are 32-bit writes to PL AXI addresses (busybox `devmem <addr> 32 <value>` = `/dev/mem` mapping).
Register names are those of the Xilinx AXI VDMA (PG020); spec 02 owns the full PL map.

### 5.1 Menu (UI) layer VDMA, base 0x80000000 — MM2S (read from DDR to LCD)

| Order | Address | Value | Meaning |
|---|---|---|---|
| 1 | 0x80000000 MM2S_VDMACR | 0x00000004 | soft reset |
| 2 | 0x80000000 MM2S_VDMACR | 0x0001008B | RS, circular, GenlockEn, GenlockSrc internal, IRQFrameCount 1 |
| 3 | 0x8000005C MM2S_START_ADDRESS1 | 0x1F400000 | U-Boot logo frame (kept until the app is up) |
| 4 | 0x80000058 MM2S_FRMDLY_STRIDE | 0x01001000 | frame delay 1, stride 4096 bytes |
| 5 | 0x80000054 MM2S_HSIZE | 0x00001000 | 4096 bytes per line (1024 px × 4 B) |
| 6 | 0x80000050 MM2S_VSIZE | 0x00000258 | 600 lines; this write starts the transfer |

Switch to the UI framebuffer (once the application has drawn its first frame): 0x8000005C ← **0x1FC00000** (`/dev/fb0` memory), then
0x80000050 ← 0x258 (VSIZE write latches the new address). The same two writes with 0x1F000000/0x1F400000 select the
U-Boot update/logo pictures.

### 5.2 Waveform layer VDMA, base 0x80020000

| Order | Address | Value | Meaning |
|---|---|---|---|
| 1 | 0x80020030 S2MM_VDMACR | 0x00000004 | soft reset |
| 2 | 0x80020030 S2MM_VDMACR | 0x00018003 | RS, circular, Repeat_En, IRQFrameCount 1 |
| 3 | 0x800200AC S2MM_START_ADDRESS1 | 0x1E400000 | wavedata pool |
| 4 | 0x800200A8 S2MM_FRMDLY_STRIDE | 0x000007D0 | stride 2000 bytes |
| 5 | 0x800200A4 S2MM_HSIZE | 0x000007D0 | 2000 bytes per line |
| 6 | 0x80020000 MM2S_VDMACR | 0x00000004 | soft reset |
| 7 | 0x80020000 MM2S_VDMACR | 0x0001008B | as §5.1 |
| 8 | 0x8002005C MM2S_START_ADDRESS1 | 0x1E400000 | same buffer |
| 9 | 0x80020058 MM2S_FRMDLY_STRIDE | 0x010007D0 | frame delay 1, stride 2000 |
| 10 | 0x80020054 MM2S_HSIZE | 0x000007D0 | 2000 bytes |
| 11 | 0x800200A0 S2MM_VSIZE | 0x000001E0 | 480 lines; starts S2MM |
| 12 | 0x80020050 MM2S_VSIZE | 0x000001E0 | 480 lines; starts MM2S |

The PL writes the waveform image into 0x1E400000 (S2MM) and reads it back for display (MM2S); 2000 B × 480 lines =
960 000 bytes (pixel format: spec 07).

### 5.3 VNC capture (UI VDMA S2MM), base 0x80000000

| Order | Address | Value | Meaning |
|---|---|---|---|
| 1 | 0x80000030 S2MM_VDMACR | 0x00000004 | soft reset |
| 2 | 0x80000030 S2MM_VDMACR | 0x00018003 | RS, circular, Repeat_En, IRQFrameCount 1 |
| 3 | 0x800000AC S2MM_START_ADDRESS1 | 0x1F800000 | VNC frame (`siglentfb1` memory) |
| 4 | 0x800000A8 S2MM_FRMDLY_STRIDE | 0x00000800 | stride 2048 bytes |
| 5 | 0x800000A4 S2MM_HSIZE | 0x00000800 | 2048 bytes (1024 px × 2 B, RGB565) |
| 6 | 0x800000A0 S2MM_VSIZE | 0x00000258 | 600 lines; starts S2MM |
| 7 | 0x80040560 | 0x0 then 0x1 | request the PL→PS composite-video stream (rising edge) |
| 8 | 0x800406C0 | 0x1 | enable (set after the VNC VDMA is configured) |

### 5.4 xilinx_dma.ko

The Xilinx dmaengine driver binds to the two VDMA DT nodes and resets their channels at probe. The VDMAs are
programmed directly as above, so firmware may omit the module; if it loads it, it must do so while the PL is
configured (probe reads VDMA registers).

### 5.5 LCD

| Order | Address | Value | Delay after |
|---|---|---|---|
| before PL load | 0x80040058 | 0 (LCD/backlight off) | 0.5 s |
| after §5.1–5.3 | 0x80040054 | 1 (LCD reset asserted) | none |
| | 0x80040054 | 0 (reset released) | none |
| | 0x80040534 | 0x700 (backlight brightness) | 0.1 s |
| | 0x80040058 | 1 (LCD/backlight on) | — |

Brightness range and later control: spec 07.

### 5.6 State left by U-Boot (boot-time bitstream)

U-Boot's board code runs this sequence once per boot with the FSBL bitstream loaded (delays are minimums):

1. Load `logo.bmp` (→ 0x200000) and `update_logo.bmp` (→ 0x600000) from eMMC p8 (FAT).
2. 0x80040534 ← 0 (brightness 0); 0x80040058 ← 0 (LCD off); 0x80040054 ← 1 (LCD reset asserted).
3. Check the BMP signature; on failure stop here (LCD stays off).
4. Convert each BMP (pixel data at file offset 54; width/height from the BMP header) into a 1024×600 frame: logo →
   0x1F400000, update picture → 0x1F000000 (pixel format: spec 07).
5. Menu VDMA: 0x80000000 ← 0x00018003 (RS, circular, Repeat_En, IRQFrameCount 1; no soft reset), 0x8000005C ←
   0x1F400000, 0x80000058 ← 0x1000, 0x80000054 ← 0x1000, 0x80000050 ← 0x258.
6. Wait 100 ms; 0x80040054 ← 0 (reset released).
7. Boot beep: PS TTC0 (0xFF110000) counter 1 set up as a square wave (clock control ← 0; counter control: wave
   enable, match mode, interval mode; interval 0x24 ← 16666, match 0x30 ← 8333), started (counter control bits 0 and
   4 cleared) for 100 ms, then stopped (bits 0 and 4 set). Beeper wiring: spec 15.
8. 0x80040058 ← 1 (LCD on); wait 200 ms; 0x80040534 ← 1999 (0x7CF, brightness); wait 120 ms.

Linux (§5.5) later turns the LCD off for the PL reload and back on with brightness 0x700.

## 6. Failure behaviour and traps

- fpga_manager returns an error and `state` ≠ `operating` if the file is missing (e.g. `/lib/firmware` symlink could
  not be created because `/` was already read-only — happens on the first boot after a rootfs flash only, spec 01 §5; `CONFIG_FW_LOADER_USER_HELPER` is off, so the
  request fails at once), if
  the IDCODE mismatches, or on CRC error. The PL is then left unconfigured or in the previous state, and no PL
  register may be touched.
- The clock chip programming has no error detection. Without `/dev/spidev1.0` the clock chip stays unprogrammed.

## Open

- Confirmation of the part (spec 12 §1) on a unit: CSU IDCODE/VERSION registers, package marking.
- Identity of both clock chips (package marking; for the paged chip, read its device-ID registers) and the meaning
  of the individual writes (output frequencies; spec 06/12 need the ADC sample clock and PL reference frequencies).
- HWID strap mapping confirmation (GPIO base 334 → MIO 0–7, spec 12 §4.2) and which board variant ships in which
  hardware revision (public release notes mention hardware versions `31`/`b1` that need a newer firmware; whether
  that is a third clock-chip variant is unknown).
- Whether PL reload without first stopping the VDMAs is always safe (§3.3 reloads only before any user starts).
- Pixel format of the wave layer and meaning of 0x80040560/0x800406C0 beyond the boot use (spec 07).
- Hardware behaviour of §4.3, §4.4 and §5.1–5.5 (lock, timing margins) is not confirmed on a unit.
