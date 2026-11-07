# 05 — Triggering

The SDS800X HD trigger is digital and lives in the FPGA (PL). The FPGA compares the calibrated 12-bit
sample stream of the selected channel against programmable level codes with hysteresis, qualifies
the resulting edges by time, logic pattern, video timing or a serial decoder, applies holdoff and
fires the record FSM (spec 03), which places the trigger in the record by the pre/post-trigger counts.
The CPU only programs registers.

The SDS800X HD has **no external trigger input**: trigger sources are CH1–CH4 and AC line. The PL
still has the register fields of an EXT/EXT5 source path; it is unreachable on this product and is described in
§3.4 for completeness only. An implementation MUST NOT offer EXT or EXT/5.

The trigger uses two register blocks. A `n` is register ACQ `n` and M `n` is register MAP `n` of
spec 02 (same index, same addressing):

| Block | Base | Address of register `n` | Content |
|---|---|---|---|
| **A** (acquisition) | `0x80030000` | `0x80030000 + 4·n` | type, source, levels, times, holdoff, mode, pattern, video, qualified, status |
| **M** (map / display) | `0x80040000` | `0x80040000 + 4·n` | serial (protocol) trigger decoder |

All trigger registers are 16 bits wide, held in the low half of a 32-bit word (write the value
zero-extended, read the low 16 bits). Register numbers in this document are `n` (e.g. A `0x0E` is
the word at `0x80030038`; M `0x30` is `0x800400C0`).

Software model: register writes follow `02` §1.2. Keep a 16-bit shadow per register plus a "written" flag
(initially nothing written); a field update ("set bits x:y of `n`") reads the shadow, or the hardware if `n` was
never written, modifies the field and writes the result. Writes may be issued unconditionally; suppressing writes equal to the
shadow gives identical register values. The ordering and strobe rules of this spec are mandatory.

Enumerations used below (API values; SCPI names in brackets):

| Enum | Values |
|---|---|
| Trigger type | 0 Edge, 1 Slope, 2 Pulse, 3 Video, 4 Window, 5 Interval, 6 Dropout, 7 Runt, 8 Pattern, 9 Serial, 10 Qualified, 11 Nth edge, 12 Delay, 13 Setup/Hold |
| Source | 0–3 CH1–CH4, 6 AC line (4 EXT, 5 EXT/5: not present on SDS800X HD); 7–22 D0–D15: the logic channels of the optional SLA1016 probe (`15` §2). A digital trigger is executed by the probe (its settings go to the probe over USB, `15` §2), not by the trigger engine of this spec |
| Slope | 0 rising/positive, 1 falling/negative, 2 alternate (either) |
| Limit (time condition) | 0 less than, 1 greater than, 2 inside range, 3 outside range |
| Coupling | 0 DC, 1 AC, 2 LF reject, 3 HF reject |
| Sweep mode | 0 Auto, 1 Normal, 2 Single, 3 Force (FTRIG) |
| Serial protocol | 0 I2C, 1 SPI, 2 UART, 3 CAN, 4 LIN — API values; the FPGA protocol codes differ (§2.2) (5 FlexRay, 6 CAN FD, 7 I2S, 8 1553B, 9 SENT, 12 ARINC 429: **no trigger hardware**, see §10.7) |

---

## 1. Supported trigger types

| Type | FPGA type code (A `0x0E[3:0]`) | A `0xE7` | Hardware used |
|---|---|---|---|
| Edge | 0 | 9 | level 1, edge select |
| Slope | 1 | 9 | levels 1+2, time window |
| Pulse | 2 | 9 | level 1, time window |
| Video | 3 | 9 | level 1, video sync separator, pulse engine (sync-tip width, §5.6) |
| Window | 0 | 9 | levels 1+2 (edge engine with "window" edge type) |
| Interval | 6 | 9 | level 1, time window |
| Dropout | 5 | 10 | level 1, timeout |
| Runt | 7 | 9 | levels 1+2, time window |
| Pattern | 4 | 10 | per-channel levels, pattern logic, time window |
| Serial | 9 | 9 | per-channel serial levels, M-block protocol decoder |
| Qualified | 8 | 9 | levels, qualifier engine A `0x80` |
| Nth edge | 10 (`0xA`) | 9 | level 1, edge counter A `0x87`, idle time |
| Delay | 8 | 9 | qualifier engine in mode 7 (event A = channel pattern, event B = edge) |
| Setup/Hold | 8 | 9 | qualifier engine in mode 1 (setup) or 3 (hold) |
| (any type, sweep mode Auto and `smp` = 0) | 15 (`0xF`) | 9/10 per type, unchanged | trigger disabled |

Type code 15 is written instead of the table value when the sweep mode is Auto (enum 0) **and** the
acquisition sample mode `smp` (§7) is 0, i.e. Auto in roll mode — the same condition that clears A
`0x0E` bit 12 and AUTO in §7. A `0xE7` still gets the type's value. The set of displayed channels plays
no part. Because the code depends on the sweep mode, every sweep-mode change re-runs the type sequence
(§7).

Holdoff and coupling are per-type settings (§5.1 step 6): Video, Serial, Qualified, Delay and Setup/Hold always
run with holdoff off; Video, Pattern, Serial, Qualified, Delay and Setup/Hold always run with DC coupling.

Product level: on SDS800X HD the firmware offers Qualified, Delay, Setup/Hold and Nth edge, and does not offer
zone trigger, holdoff by events, or the serial triggers of §10.7.

---

## 2. Register summary

### 2.1 A block

| n | Name | Fields (W = written by trigger code) |
|---|---|---|
| `0x08`,`0x09`,`0x52` | HOLDOFF_0/1/2 | 48-bit holdoff count in 4 ns ticks: `0x08` = bits 15:0, `0x09` = 31:16, `0x52` = 47:32 (§6) |
| `0x0A` | TRIG_EN | bit 0 AUTO (auto-trigger enable, §7); bit 8 HOLDOFF_EN; bit 15 EXT_PATH (1 = AC-line (or EXT) comparator path, §3.4) |
| `0x0B` | TDC_RESET | written 3 only by the unreachable EXT/EXT5 source path (§3.4); never written for channel or AC-line sources |
| `0x0C`,`0x0D` | NTR_TIMEOUT lo/hi | 32-bit "no-trigger" timeout; written 1 in Force mode (§7) |
| `0x0E` | TRIG_CTRL | [3:0] TYPE (§1); [6:4] frequency-counter source; [10:8] SRC (§3.1); bit 12 MODE_CONT (§7). Other bits: acquisition (spec 02) |
| `0x11` | EDGE_CFG | [1:0] pulse limit (§5.3); bit 7 pulse polarity; [9:8] EDGE_TYPE: 0 rising, 1 falling, 2 alternate, 3 window. Bits 7 and 1:0 are written by Pulse and Video only and keep their last value for other types |
| `0x12`,`0x13` + `0x72[3:0]` | T_A | 36-bit time, ns: `0x12` = 15:0, `0x13` = 31:16, `0x72[3:0]` = 35:32 |
| `0x14`,`0x15` + `0x72[11:8]` | T_B | 36-bit time, ns: `0x14` = 15:0, `0x15` = 31:16, `0x72[11:8]` = 35:32 |
| `0x16` | VIDEO_LINE | [10:0] line; bit 11 any-line; [14:12] field (FPGA numbering, §5.6) |
| `0x17` | VIDEO_CFG | [9:0] line period (64 ns units, §5.6); [15:12] active-field code (§5.6); bits 11:10: no field of their own — written 0 (§5.6) |
| `0x19` | STATUS (R) / stop (W) | R: bit 0 trigger-interpolation value ready (spec 03 §4.1); bit 3 stop status; bit 9 triggered / frame done / stop ready (one bit, spec 02 §4.1; the frame-ready signal of the per-frame cycle is MAP 0x082, spec 03 §3.6); bits 9 and 0 both set in roll mode = a normal trigger occurred. W: bit 3 stop request (spec 02 §4.1) |
| `0x29` | PF_TRIG_OUT | 1 while pass/fail is on (together with M `0x174` bit 3), 0 otherwise (spec 15). There is no other trigger-output register on this product |
| `0x2A`,`0x2B` | TR_TIMEOUT lo/hi | 32-bit auto-trigger timeout (§7) |
| `0x2C` | SLOPE_DROP | bit 0 slope direction; [2:1] slope limit; bit 8 dropout polarity; bit 12 dropout overtime type |
| `0x2D` | INTV_RUNT | bit 0 interval polarity; [2:1] interval limit; bit 8 runt polarity; [13:12] runt limit |
| `0x33` | (acq) | bit 0 TDC_EN: 0 for channel and AC-line sources (1 only on the unreachable EXT/EXT5 path, §3.4) |
| `0x42` | POS_PRE_ADDR | written 26 (`0x1A`) on every type change |
| `0x65` | TRIG_FSM (R) | trigger state machine state (spec 03) |
| `0x6F` | ACQ_STATE (R) | trigger/acquire state (spec 03) |
| `0x80` | QUAL_CFG | [4:0] qualifier source; bit 5 qualifier state / edge (per mode, §5.9); bit 6 event select (1 Delay, Setup/Hold); bit 7 trigger edge (1 rising; Hold: 1 = data Low); [9:8] time limit; [14:12] mode (§5.9) |
| `0x87` | NTH_EDGE | edge count N (1–65535) |
| `0xAC` | PATTERN_POL | 2 bits per channel at bits `4·ch+1 : 4·ch` (§5.8) |
| `0xAD` | PATTERN_CFG | [1:0] logic; [5:4] pattern limit; bit 8 (pattern enable) is never written (0) |
| `0xAF` | (acq) | bit 8 BYPASS_TRIG (acquire without trigger; used by acquisition code, spec 03) |
| `0xB0` | SEARCH_SRC | [2:0] waveform-search source (informative, §3.3) |
| `0xB4`/`0xB2`, `0xCC`/`0x45`, `0x104`/`0x108`, `0x106`/`0x109` | SEARCH_LEVEL 0–3 code / hysteresis | waveform-search comparators; the firmware copies the trigger levels into them (§3.3). Not used by the trigger |
| `0xB5`,`0xCD`,`0x105`,`0x107` | EDGE_SEL 0–3 | edge select code: 0 none, 1 rising, 2 falling, 3 either. Single-source types use `0xB5` (comparator 1) and `0xCD` (comparator 2); Pattern, Qualified, Delay and Setup/Hold use all four as CH1–CH4, derived from the pattern states (§5.8) |
| `0xD7`/`0xD6` | CH1 level 1 code / hysteresis | per-channel comparator 1 (§3.3) |
| `0xD9`/`0xD8` | CH1 level 2 code / hysteresis | per-channel comparator 2 |
| `0xDB`/`0xDA`, `0xDD`/`0xDC` | CH2 level 1, level 2 | |
| `0xF1`/`0xF0`, `0xF3`/`0xF2` | CH3 level 1, level 2 | |
| `0x101`/`0x100`, `0x103`/`0x102` | CH4 level 1, level 2 | |
| `0xE0`/`0xE1` | LEVEL1 code / hysteresis | trigger-source comparator 1 |
| `0xE2`/`0xE3` | LEVEL2 code / hysteresis | trigger-source comparator 2 |
| `0xE4` | COUPLING | trigger coupling filter 0–3 (§4) |
| `0xE6`/`0xE5`, `0xEC`/`0xEB`, `0x10B`/`0x10A`, `0x10D`/`0x10C` | SER_LEVEL CH1–CH4 code / hysteresis | serial-decoder slicer thresholds (§10.1) |
| `0xE7` | TYPE_AUX | 9 or 10 per type (§1) |
| `0x144` | EXT_LEVEL | EXT comparator DAC code; on SDS800X HD written 0 once at offset-DAC init (spec 06 §5) and never again |

### 2.2 M block (serial trigger decoder)

| n | Fields |
|---|---|
| `0x30` | [4:0] DATA_SRC; [7:5] DATA_COMPARE (0 =, 1 >, 2 <); [11:8] PROTOCOL: I2C 0, SPI 1, UART 2, LIN 3, CAN 5 (FPGA codes, not the API enum); bit 12 POSITION_MODE (0 any, 1 value) |
| `0x31`–`0x36` | DATA[5..0]: `0x31` = data word 5 … `0x36` = data word 0 (16 bits each) |
| `0x3D`–`0x42` | MASK[5..0]: same order as DATA (1 = compare this bit) |
| `0x43` | DATA_LEN (bits or bytes, per protocol, §10) |
| `0x44` | [4:0] CS_SRC; [9:5] CLK_SRC |
| `0x45`,`0x46` | BAUD_WORD bits 31:16, 15:0 (§10.2) |
| `0x47` | [9:0] I2C address; [11:10] I2C R/W code; bit 12 I2C address width (0 7-bit, 1 10-bit) |
| `0x48` | [2:0] I2C condition; [5:4] I2C ACK code; [15:6] I2C address mask |
| `0x49` | [3:0] idle-time bits 19:16; [14:4] data position; bit 15 idle-time enable |
| `0x4E` | idle-time bits 15:0 |
| `0x4F` | [1:0] SPI CS mode; bit 2 SPI CPHA (written 0); bit 3 SPI clock latch edge (1 falling, 0 rising) |
| `0x50` | [1:0] UART condition; bit 2 UART idle level; [4:3] stop bits; [6:5] parity; [10:7] data width; [14:13] mark/space |
| `0x51` | [2:0] CAN condition; [4:3] CAN ID type; [7:5] CAN ID compare; bit 12 bit order |
| `0x52` | [1:0] LIN condition; bit 2 LIN spec; bit 3 header-parity error; bit 4 sync-byte error; bit 5 checksum error; LIN ID compare (see §10.6) |
| `0x53` | bit 0 HIGH_BAUD; [4:1] SAMPLE_POINT; [9:5] BIT_LEN (§10.2) |

Source fields (DATA_SRC, CLK_SRC, CS_SRC): the source enum value (0–3 = CH1–CH4) is written as is; a
digital source (7–22, SLA1016 only) is replaced by 4.

---

## 3. Sources, levels and hysteresis

### 3.1 Source code (A `0x0E[10:8]`)

| Source | SRC code |
|---|---|
| CH1–CH4 | 0–3 (the channel index; no interleave remapping) |
| (EXT, EXT/5: not on this product) | 4 |
| AC line | 5 |
| any other | 6 (codes > 6 are clamped to 6) |
| Pattern type | 0 (pattern uses all channels) |

Only displayed channels and AC line are valid sources. The UI refuses a source whose channel is off; a
"next/previous source" request moves to the next enabled source in that direction (CH1–CH4 if on, AC line
always valid). Each trigger type stores its own source and re-applies it on a type change (§5.1 step 5).
For Qualified the edge (trigger) source goes here and the qualifier source to A `0x80[4:0]`; for Delay
event B; for Setup/Hold the clock (Setup) or data (Hold) channel (§5.9).

### 3.2 Level unit

The firmware carries every analog trigger level as an integer **level in screen pixels** relative to
the vertical screen centre, 60 pixels per division:

```
L_px = round( V · 60 / Vdiv_display ) + P           round = half away from zero (adds/subtracts 0.5001, truncates)
```

`V` is the level in volts at the probe tip and `Vdiv_display` the displayed channel scale in V/div,
i.e. `probe · Vdiv_BNC` (spec 06 §12); equivalently `V · 60 / (Vdiv_BNC · probe)`. Do not multiply by
the probe factor a second time when the displayed scale is used. `P` is the
channel's vertical position in the same pixel unit (an integer: the channel keeps its position as whole pixels), added for coupling DC and HF reject only (AC and
LF reject: `P = 0`, the level is relative to the signal mean). The accepted level range is
±4.1 div about the screen centre (`[−4.1·Vdiv − offset, +4.1·Vdiv − offset]`).

**Level storage.** Per type and per source channel the firmware keeps two values: the level in volts (probe tip)
and the integer *display level* `L_disp = L_px − P` (pixels relative to the channel's position marker). A level
command converts its volts to `L_disp` with the source channel's current scale, clamps `L_px` to ±246 px (±4.1 div
about the screen centre), and stores both (the volts of a clamped command are replaced by the volts of the clamped
pixel value).

After every change of the **current source** channel's V/div, probe factor, offset/position or coupling, the firmware
recomputes `L_px` from the stored volts and clamps it to ±246 px, stores the new `L_disp` but **keeps the stored
volts** (a later larger V/div restores the unclamped level), recomputes the noise value `h` (§3.5) from the source's
current V/div class, and rewrites LEVEL1/LEVEL2, the per-channel pairs and the search levels (§3.3). For Pattern
this applies to every channel that has a pattern level, for Serial to every slicer threshold. Levels of channels
that are not the current source are **not** updated by such changes: their `L_disp` stays as it was when last set.
Example: level 0.1 V on CH1, `SCALe 0.01` → A `0xE0` = 0xF34 (L = +246 px, h = 124); `SCALe 0.002` →
h = 240. Only the final values of one such change matter.

The re-apply runs in its own stop/arm cycle after the channel or source change has been
applied: stop (A `0x19` ← 8), level writes, A `0x2E` ← 0, re-arm (spec 03 §3.2 steps 2–7), A `0x2E` ← 1 — the
switch-index toggle of the per-frame cycle (spec 03 §3.6); the arm that ends a settings change (spec 03 §3.3)
has no such toggle. Which trigger changes run this cycle: §5.10.

On a source change the new source starts at its **stored display level** plus its current position, `L_px =
L_disp + P` — not at its stored volts, and not at the old source's level. So a channel whose V/div changed while it
was not the source keeps its old screen position (example: Slope level 1 at its factory −3 px on C2, C2 changed to
0.5 V/div beforehand, `SLOPe:SOURce C2` with falling slope → A `0xE0` = 0x826, i.e. −3 px = −0.025 V, not the
−0.05 V that −3 px means at 1 V/div; Window → 0x6AE = −50 px). C2 never set → 0 px (A `0xE0` = 0x83E with falling
slope, h = 62). ~0.1 s later A `0x0E[6:4]` (counter source, §9) ← the new source; A `0xB0` (search source) is
written with the source.

**Factory-default levels.** After a factory-default start-up every type and channel has `L_disp` = 0 except: Slope
level 1 and Runt level 1 = −3 px, Window level 1 (absolute low) = −50 px (the appendix SCPI defaults −0.05 V and
−0.833 V are these pixel values at CH1's default 1 V/div), and the Video rule of §5.6. These are pixel values for
every channel: a replacement MUST store them per channel as `L_disp`, not as volts, so that a channel whose V/div
changed before it becomes the source starts at that pixel level.

For AC line the same formula is used with `Vdiv = 0.5 V`, `probe = 1` (the unreachable EXT/EXT5 path
uses 0.2 V / 1.0 V).

**Map-side copy (M `0x1E7`/`0x1E8`/`0x1E9`).** A 12-bit level pair also goes into the MAP
plane (rule in the MAP appendix, spec 02 §9): level_n = clamp((0x800 + 8·L − s) & 0xFFF, h, 0xFFF − h) into
M `0x1E7` (level 1) / M `0x1E8` (level 2), each write followed by M `0x1E9` ← h; s = +h rising, −h falling, 0
alternate; h = 0 → 8. Level 1 is written only for the type enums 0, 2, 7 (Edge, Pulse, Runt; slope as given)
and 1, 6 (Slope, Dropout; slope inverted), level 2 for every type with the slope inverted except type 7.
These registers change on V/div and offset changes of the displayed channel (e.g. CH1 0.5 V/div,
offset +0.2 V → `0x1E7` = 0x702, `0x1E8` = 0x77E, i.e. L = −24 px = −vpos), not on trigger-level changes, so
L is not the trigger level of §3.2 (**UNVERIFIED** what L and the registers mean to the PL; they are not part
of the trigger comparators). A replacement reproduces the start-up values (spec 07 §7.10 group 6) and may
leave them unchanged afterwards.

### 3.3 Level code and hysteresis

The sample code axis is 12-bit with screen centre 2048 and 8 codes per pixel (480 codes per
division; the 8-division screen spans codes 128 … 3967, the screen limits of spec 03 §5; 3968 = 0xF80 is the first code above). For a comparator with level `L_px`, slope
`s` and hysteresis `h` (codes):

```
code = 2048 + trunc( (float)L_px · 8.0f ) − d          d = +h for rising, −h for falling, 0 otherwise
```

The comparator register pair gets `code` (low 16 bits) in the level register and `h` in the
hysteresis register. So the code register holds the threshold the signal must cross *from the
far side of the hysteresis band*: rising edges arm below `L − h`, falling edges arm above `L + h`
(**UNVERIFIED** exact FPGA arming rule; the encoding is exact).

`h` is the firmware's noise value (§3.5); if it is 0 the firmware uses 128. For the single-source types
(every row of the table below except Pattern, Qualified, Delay and Setup/Hold) the per-channel pair
uses `h' = min(h, 240)` for both the code and the hysteresis register. The Pattern, Qualified, Delay and
Setup/Hold path writes the per-channel pair with the unclamped `h` (≤ 255). LEVEL1/LEVEL2
(`0xE0`–`0xE3`) always use the unclamped `h`.

Which registers a level write touches (`level1`/`level2` = the type's first/second level):

| Type | level 1 | level 2 |
|---|---|---|
| Edge, Slope, Video, Interval, Dropout, Nth edge | LEVEL1 (`0xE0/0xE1`) with the UI slope, and the source channel's level-1 pair | Slope only: LEVEL2 (`0xE2/0xE3`) and level-2 pair, UI slope |
| Pulse, Runt | LEVEL1 and level-1 pair with slope inverted (`1 − s`) | Runt: LEVEL2 and level-2 pair with `1 − s` |
| Window | LEVEL1 + pair with slope = falling | LEVEL2 + pair with slope = rising |
| Pattern | for every channel with a pattern state: that channel's level-1 pair, slope from §5.8 | — |
| Qualified | level 1 = qualifier level (`QLEVel`): level-1 pair of the qualifier channel (A `0x80[4:0]`) | level 2 = edge level (`ELEVel`): level-1 pair of the edge (trigger) source channel; both with slope = UI edge slope and `h_off` of the **edge** source's V/div class |
| Delay | level (event A): level-1 pair of the named pattern channel | level 2 (event B): level-1 pair of the B channel; **both** pairs biased by the event-B slope (A `0x80[7]`: rising `−h`, falling `+h`), not by the pattern state |
| Setup/Hold | clock threshold: level-1 pair of the clock channel | data threshold: level-1 pair of the data channel; both with slope = falling for data High, rising for data Low |
| Serial | none (serial levels §10.1) | — |

Pattern, Qualified, Delay and Setup/Hold write only the per-channel pairs; LEVEL1/LEVEL2 (`0xE0`–`0xE3`)
are not written for them.

SCPI names: for Slope, Runt and Window level 1 is `LLEVel` (the low level) and level 2 is `HLEVel` (the high
level); Window relative: §5.7; Qualified as in the table.

**Minimum gap (Slope, Runt, Window absolute).** A level command keeps the high level at least 3 px above the low
level, compared in volts with the source's displayed scale (3 px = 3·Vdiv_display/60): after a low-level command,
if low + 3 px > high, the high level is set to low + 3 px; after a high-level command, if low > high − 3 px, the
low level is set to high − 3 px. The pushed level is stored (volts and `L_disp`, §3.2) and written like a level
command, after the commanded level. The rule is not applied by the re-apply of §3.2 (V/div, offset, coupling or
source change). Example: Runt at 1 V/div with HLEVel 0 V, `RUNT:LLEVel 0.1` → level 1 = +6 px and level 2
pushed to 0.15 V (+9 px, A `0xE2` = 0x886 with the inverted slope of positive polarity). (Window absolute follows
the same rule; not yet confirmed on a unit.)

Each trigger type keeps one level per source channel; a source change first writes the new channel's pair
with the old code, then LEVEL1 and the pair with the new channel's own stored level.

Inverted source channel (spec 06): the firmware programs the opposite slope (EDGE_SEL and hysteresis bias)
and keeps the level in the displayed (inverted) pixel domain; toggling invert negates the stored pixel level
and re-writes level and slope (rising on an inverted channel: EDGE_SEL 2, code `+h`). How the FPGA comparator
sits relative to the invert stage is **UNVERIFIED**.

Source AC line (enum 6): LEVEL1 (`0xE0/0xE1`) carries the threshold. AC line has no per-channel
pair: only `0xE0/0xE1` are written (a write of the same code and `h'` to the CH3 level-2 pair `0xF3/0xF2` is
harmless unless CH3 level 2 is in use).

Waveform search (search-level step). After every level, slope, source, noise-reject or pattern-state change,
and once after every timebase setup, the firmware copies the trigger levels into the waveform-search comparators
(hysteresis = the noise value `h`, clamped to 240):

- Single-source types: search comparator 0 (`0xB4` ← A `0xE0`, `0xB2` ← `h`) and/or 1 (`0xCC` ← A `0xE2`,
  `0x45` ← `h`): Slope uses 1 for rising and 0 otherwise; Runt 0 for rising and 1 otherwise; Window both; every
  other type 0, or both when its slope is alternate. `0xB0[2:0]` ← search source.
- Pattern, Qualified, Delay, Setup/Hold: for each channel `ch` = 0…3, search comparator `ch` (`0xB4`/`0xB2`,
  `0xCC`/`0x45`, `0x104`/`0x108`, `0x106`/`0x109`) ← that channel's level-1 code and `h`, and then the channel's
  EDGE_SEL from the pattern state (§5.8).

The search comparators do not affect triggering; a replacement without waveform search need not write them
(spec 02 §8). The EDGE_SEL writes of the second case are trigger state and MUST be reproduced.

On a slope change of Slope/Window types, and on a type change, both levels are rewritten. For
Pattern and Delay the firmware keeps one level per channel; `LEVel <ch>,<v>` writes only that channel's pair,
and a type change rewrites all channels that have one.

### 3.4 Source path bits (AC line; EXT informative)

On every source write the firmware sets:

| Source | A `0x0A` bit 15 | A `0x33` bit 0 | A `0x0B` |
|---|---|---|---|
| CH1–CH4 | 0 | 0 | not written |
| AC line | 1 | 0 | not written |
| (EXT, EXT/5 — unreachable on this product) | 1 | 1 | 3 |

Then, if the sweep mode is Auto and `smp` = 0 (§7), A `0x0A` bit 15 is cleared again for every source.

Informative, EXT/EXT5 only (not reachable on SDS800X HD): the EXT threshold would be a DAC code in A
`0x144`, `EXT_LEVEL = trunc(L_px · gain + 0.5) + offset`, with `gain`/`offset` taken from the
front end's EXT calibration table at index `coupling` (EXT) or `coupling + 8` (EXT/5); with negative-edge
calibration support, separate pairs for rising/falling and the mean of both for alternate. On this
product A `0x144` is only written 0 at init (spec 06 §5).

### 3.5 Noise reject (hysteresis value)

```
codes_per_div = 480
h_off = (uint16) ( codes_per_div · c_off(Vdiv) )      truncation of a float32 product
h_on  = (uint16) ( codes_per_div · c_on(Vdiv) )
```

| V/div | `c_off` | `c_on` | `h_off` | `h_on` |
|---|---|---|---|---|
| > 10 mV | 0.13 | 0.33 | 62 | 158 |
| 10 mV | 0.26 | 0.33 | 124 | 158 |
| 5 mV | 0.26 | 0.33 | 124 | 158 |
| ≤ 2 mV | 0.5 | 0.5 | 240 | 240 |

Rows are the V/div classes of spec 06 §6.1 of the trigger source channel: > 10 mV = class ≥ 12,
10 mV = class 11, 5 mV = class 10, ≤ 2 mV = class ≤ 9 (including 500 µV). 50 Ω input multiplies the coefficient by 1.0 on this model. Both values
are clamped to 255. The noise value used is `h_on` when noise reject is ON and `h_off` when it is
OFF — except for Qualified, Setup/Hold and Delay, which always use `h_off`. `h` is recomputed after a
noise-reject change, a source change and a V/div (or probe) change of the source channel (§3.2); each
recomputation rewrites level 1, level 2 and the search level. Pattern and Delay event A have no single
source: see §5.8 for the channel whose class is used.

---

## 4. Coupling

Coupling (DC/AC/LF reject/HF reject) is written as the enum value to A `0xE4`. AC and LF reject are
digital filters in the FPGA trigger path (**UNVERIFIED** corner frequencies); the level is then
relative to the filtered mean (§3.2). Coupling is a per-type setting. When it is written:

- a coupling change writes A `0xE4` for every type except Qualified, Delay and Setup/Hold (for those
  the change is only stored);
- a source change, and therefore every type change (§5.1 step 5), writes A `0xE4` = the stored coupling
  for every type except Delay;
- at the end of every type change (§5.1 step 6), Video, Pattern, Serial, Qualified, Delay and Setup/Hold
  write A `0xE4` = 0 (DC). The other types keep the coupling written in step 5, so changing back to e.g.
  Edge restores its own coupling.

---

## 5. Per-type programming

### 5.1 Type change sequence

On a type change the firmware, in this order:

1. writes A `0x11[9:8]` = 0 (bits 7 and 1:0 keep their last value; that the FPGA ignores them for types
   other than Pulse and Video is **UNVERIFIED**);
2. Delay: A `0x80[14:12]` = 7, A `0x80[6]` = 1, A `0xAD[1:0]` = 0. Setup/Hold: A `0x80[6]` = 1.
   All other types: A `0x80[6]` = 0;
3. A `0x0E[3:0]` = type code (§1), A `0x42` = 26, A `0xE7` = 9/10 (§1);
4. type-specific setup: Video: Pulse polarity negative (A `0x11[7]` = 0), Pulse limit greater-than
   (A `0x11[1:0]` = 1), T_B = 150 ns (sync-tip width threshold), edge slope rising; Qualified/Setup-Hold:
   qualifier source, mode, state/edge (§5.9); Delay: event A slope (§5.9);
5. the type's stored source (§3.1, §3.4, coupling §4), slope (§5.2), level 1, level 2 (§3.3), and the
   type's stored times (§5.3). Serial has no source of its own here: its sources go to the M block (§10) and
   A `0x0E[10:8]` is not set to the data source (e.g. an I2C entry with SDA = C2 leaves it 0; whether it is set to
   the clock source is UNVERIFIED, SCL = C1 = 0 in that case);
6. holdoff and coupling: Video, Serial, Qualified, Delay and Setup/Hold switch holdoff off (A `0x0A` bit 8
   = 0); Video, Pattern, Serial, Qualified, Delay and Setup/Hold set coupling DC (A `0xE4` = 0). Every other
   type re-applies its own stored holdoff switch (§6) and keeps the coupling of step 5 (so going back to
   Edge restores its holdoff and coupling).

### 5.2 Slope and edge-select encodings

| Type | A `0x11[9:8]` | Other field | EDGE_SEL `0xB5` | EDGE_SEL `0xCD` |
|---|---|---|---|---|
| Edge, Nth edge | slope (0/1/2) | — | slope + 1 | — |
| Slope | slope | A `0x2C[0]` = slope | slope + 1 | — |
| Pulse | — | A `0x11[7]` = 1 if positive | positive: 2, negative: 1 | — |
| Video | 0 | A `0x11[7]` = 0, A `0x11[1:0]` = 1 (§5.1 step 4) | 1 | — |
| Interval | — | A `0x2D[0]` = 1 if rising | slope + 1 | — |
| Dropout | — | A `0x2C[8]` = 1 if rising | rising: 1, falling: 2 | — |
| Window | 3 | — | 2 | 1 |
| Runt | — | A `0x2D[8]` = 1 if positive | positive: 2, negative: 1 | — |

Pattern, Qualified, Delay and Setup/Hold: EDGE_SEL per channel, §5.8.

### 5.3 Time conditions

Limit codes are written raw: Slope A `0x2C[2:1]`, Pulse A `0x11[1:0]`, Interval A `0x2D[2:1]`, Runt
A `0x2D[13:12]`, Pattern A `0xAD[5:4]`, Qualified/Delay/Setup-Hold A `0x80[9:8]`.

Times are integer nanoseconds: the UI value rounded to the nearest ns (3.7 ns → 4, 1.23456789 µs →
1235, 5.0000009 µs → 5000). T_A and T_B are 36-bit (19.5 s → T_B = 0x4_8A4A_6300: A `0x14` = 0x6300,
`0x15` = 0x8A4A, `0x72[11:8]` = 4). The two time values go to T_A / T_B by limit:

| Limit | Lower time (TLOWer) → | Upper time (TUPPer) → |
|---|---|---|
| less than | not written | T_A |
| greater than | T_B | not written |
| inside / outside | T_A | T_B |
| no limit (Dropout timeout, Nth-edge idle time) | Dropout: T_A | Nth edge: T_B |

Each type keeps a (lower, upper) pair per limit (4 pairs). Defaults: inside/outside lower 2 ns, upper
3 ns; greater-than value (lower) 2 ns; less-than value (upper) 2 ns. The default limit of Slope, Pulse,
Interval, Runt, Qualified and Setup/Hold is *less than* (entering such a type writes T_A = 2, T_B
untouched); Pattern starts in *greater than* (`TYPE PATTern` writes A `0xAD[5:4]` = 1 and T_B = 2). A limit
change writes the limit code, then re-sends the new limit's pair by the table. A type change re-sends the
type's current pair (§5.1 step 5).

Range check (inside / outside limits only): a lower-time command with a value ≥ the stored upper time is not
rejected but **clamped** to upper − one knob step; an upper-time command with a value ≤ the stored lower time is
clamped to lower + one knob step. The knob step is 1 ns for values below 100 ns and one tenth of the decade above
100 ns (10 ns for 100 ns … 1 µs, 100 ns for 1 … 10 µs, …; the exact step at decade boundaries is **UNVERIFIED**).
The clamped value is stored and written to T_A / T_B like a normal command; if it equals the stored value nothing
changes. Example: Slope inside, lower 2 ns / upper 3 ns, `TLOWer 1E-6` → lower = 3 − 1 = 2 ns (T_A stays 2,
no write); `TUPPer 3E-6` then writes only T_B = 3000. Values are first rounded to the time step (1 ns) and clamped
to the range below. For less-than / greater-than limits there is no cross-check.

Ranges (SDS800X HD): 2 ns … 20 s (Nth-edge idle time 8 ns … 20 s).

### 5.4 Dropout

A `0x2C[12]` = 1 for overtime type EDGE (enum 0), 0 for STATE; A `0x2C[8]` = 1 rising, 0 falling.
Only `DROPout:TYPE` (an overtime-type change) writes bit 12; the type change of §5.1 does not re-send it.
With the built-in defaults bit 12 is 0 after start-up (A `0x2C` = 0, §7) and after entering Dropout, and
`DROPout:TYPE STATe` then writes nothing, so the stored default behaves as STATE; whether the factory
default is STATE or the register merely lags an EDGE default is **UNVERIFIED**. A replacement should write
bit 12 from its stored overtime type on entering Dropout.
The timeout (2 ns … 20 s, default 20 ns) is written to T_A (A `0x12`/`0x13`/`0x72[3:0]`, §5.3); T_B is not
used. Level 1 uses the UI slope (§3.3).

### 5.5 Nth edge

A `0x87` = N (1–65535). The idle time (8 ns … 20 s, default 8 ns) goes to T_B (§5.3). Slope handling is
that of Edge (A `0x11[9:8]`, EDGE_SEL `0xB5` = slope + 1).

### 5.6 Video

A `0x17[9:0]` = line period in 64 ns units, by standard:

| Standard | value |
|---|---|
| NTSC | 992 |
| PAL | 1000 |
| 720p/50 | 416 |
| 720p/60 | 347 |
| 1080p/50 | 277 |
| 1080p/60 | 231 |
| 1080i/50 | 555 |
| 1080i/60 | 462 |
| custom | `(15 625 000 / rate) / lines`, rate 25/30/50/60 by frame-rate setting (0/1/2/3), `lines` = lines per frame |

The custom period must fit in 10 bits: it exceeds 1023 below about 611 lines at 25 Hz (the custom defaults,
300 lines at 25 Hz, already give 2083).

An implementation MUST clamp the period to 1023 and MUST compose the whole word from the current settings
each time, `A 0x17 = activefield << 12 | min(period, 1023)`, i.e. write bits 11:10 = 0 rather than
preserve them. Reason: bits 11:10 have no field of their own — they could only receive the overflow of an
unclamped period, and every built-in standard is ≤ 1023 with them 0 — so the clean word is the only
value whose meaning is defined; preserving them would make the register depend on the setting history.
Whether the FPGA's line-period counter is wider than 10 bits (so that an unclamped value would be meaningful
for the custom standard itself) is **UNVERIFIED**.

Custom defaults: 300 lines, frame rate 25 Hz; interlace 1 and FCNT 1 (**UNVERIFIED**).

A `0x17[15:12]` = active-field code: 1 for NTSC, PAL, 1080i/50, 1080i/60; 0 for 720p and 1080p; for
custom the interlace code (interlace 1 → 0, 2 → 1, 4 → 2, 8 → 3). Bits 11:10 = 0 (above).

Line/field conversion to A `0x16`. Per standard: `adj` (lines before the FPGA's field start), `F`
(fields) and `M(f)` (lines in UI field `f`):

| Standard | adj | F | M(f) |
|---|---|---|---|
| NTSC | 4 | 2 | 262 for f = 1, 263 for f = 2 |
| PAL | 4 | 2 | 312 / 313 |
| 720p/50, 720p/60 | 2 | 1 | 750 |
| 1080p/50, 1080p/60 | 2 | 1 | 1125 |
| 1080i/50, 1080i/60 | 1 for f = 1, 2 for f = 2 | 2 | 562 / 563 |
| custom | 0 | FCNT (1, 2, 4, 8) | ⌈LCNT / interlace⌉ (interlace 1, 2, 4, 8) |

With UI line `l` (1-based) and UI field `f` (1-based, clamped to 1…F):

```
if l > adj:  LINE = l − adj − 1,          FIELD = f mod F      (f if f < F, else 0)
else:        LINE = M(f) + l − adj − 1,   FIELD = f − 1
A 0x16 = FIELD << 12 | ANY << 11 | LINE    (LINE not masked to 11 bits)
```

Examples (verified): NTSC f 1 l 2 → 0x0103, f 1 l 10 → 0x1005, f 2 l 4 → 0x1106; PAL f 1 l 4 → 0x0137;
720p l 2 → 0x02ED; 1080p l 2 → 0x0464; custom (4 fields) f 3 l 10 → 0x3009. A field change writes FIELD by
the same rule with the current line, then re-sends the line. Sync ANY sets bit 11 and leaves line/field;
sync SELect clears bit 11 and re-sends line and field. In the progressive standards a UI field 2 is
clamped to 1.

The video type also programs the pulse engine (negative polarity, greater than 150 ns, §5.1 step 4) and
edge slope rising; the level is level 1 with the UI slope (§3.3).

**First Video level after start-up.** The *first* Video level application after start-up that is not part of a
setup recall (normally the first `TYPE VIDeo`) uses a sync-tip default instead of the stored level, when no
Video level has been set since start-up: `L_disp = trunc(−150 mV · 60 / Vdiv_display)` px of the source channel (C1 at 1 V/div →
−9 px, A `0xE0` = 0x77A with h = 62, rising), and it is stored (volts and `L_disp`). Later Video level applications
use the stored level. An explicit `:TRIGger:VIDeo:LEVel` command is never overridden. The SCPI default 0 V of the appendix is the value before this first entry.

### 5.7 Window

Window uses the edge engine (type code 0) with EDGE_TYPE 3 and the two levels of §3.3. The relative
window (centre `C`, half-width `D`) is converted by the firmware: level 1 (low) = `C − D`, level 2 (high) =
`C + D` (in pixels, §3.2); the window type (absolute/relative) itself writes no register.

### 5.8 Pattern

| Field | Encoding |
|---|---|
| A `0xAD[1:0]` logic | AND → 0, OR → 2, NAND → 1, NOR → 3 |
| A `0xAC` state per channel `ch` (bits `4ch+1:4ch`) | Low → 0, High → 1, don't care → 2 |
| A `0xAD[5:4]` | limit (§5.3; times T_A/T_B by limit) |

A `0xAC` is updated one channel at a time (read-modify-write of that channel's field, CH1 first; each
update is followed by that channel's level write and the search-level step, §3.3). Each channel keeps
its own pattern level; `LEVel <ch>,<v>` writes only that channel's level-1 pair.

Per-channel edge select (`0xB5`, `0xCD`, `0x105`, `0x107` for CH1–CH4, written by the search-level step
of §3.3 for Pattern, Qualified, Delay and Setup/Hold, computed from the current A `0xAC`/`0xAD`) and the
slope used for the level code: with FPGA logic code `t` and state `p`:

| `p` | `t` ∈ {0, 2} (AND, OR) | `t` ∈ {1, 3} (NAND, NOR) |
|---|---|---|
| Low (0) | EDGE_SEL 1, level slope rising (`−h`) | EDGE_SEL 2, falling (`+h`) |
| High (1) | EDGE_SEL 2, falling (`+h`) | EDGE_SEL 1, rising (`−h`) |
| don't care (2) | EDGE_SEL not written, no bias (code = 2048 + 8·L) | same |

For Qualified and Setup/Hold A `0xAC` normally holds all don't-care (no EDGE_SEL write); for Delay it holds
event A's pattern.

Noise value for Pattern: one `h` for all four channels, computed by §3.5 from the V/div class of the
channel most recently addressed (`LEVel <ch>,…`, or the last channel written by an `INPut` list, i.e. CH4).
Every such change rewrites all four per-channel pairs (with the new bias) and the search hysteresis.
Example: C1 at 0.2 V/div, C4 at 5 mV/div: `LEVel C1,…` → h = 62 in A `0xD6`/`0xDA`/`0xF0`/`0x100`;
`LEVel C4,…` → h = 124 in all four. A per-channel `h` (each channel's own class) would be a defensible
alternative but gives different register values.
Delay event A is assumed to follow the same rule with `h_off` (**UNVERIFIED**).

### 5.9 Qualified, Delay, Setup/Hold

| Field | Encoding |
|---|---|
| A `0x80[4:0]` qualifier source | CH1–CH4 → 0–3 (other sources `src + 12`) |
| A `0x0E[10:8]` | edge (trigger) source, as §3.1 |
| A `0x80[14:12]` mode | Qualified: State 0, State-delay 1, Edge 2, Edge-delay 3; Setup 1, Hold 3; Delay 7 |
| A `0x80[5]` | Qualified: qualifier state High 1 / Low 0, or qualifier edge Rising 1 / Falling 0 |
| A `0x80[7]` | Qualified: trigger edge, 1 rising, 0 falling (= 1 − slope enum) |
| A `0x80[9:8]` | time limit (§5.3) |
| A `0x80[6]` | 1 for Delay and Setup/Hold, 0 otherwise |

Qualified: levels per §3.3 (edge channel and qualifier channel, each its own level, both biased by the UI
edge slope); noise value always `h_off` (§3.5); times by limit (§5.3).

Setup/Hold:

| | Setup (mode 1) | Hold (mode 3) |
|---|---|---|
| A `0x80[4:0]` | data channel | clock channel |
| A `0x0E[10:8]` | clock channel | data channel |
| A `0x80[5]` | data state (1 High, 0 Low) | clock edge (1 rising, 0 falling) |
| A `0x80[7]` | clock edge (1 rising, 0 falling) | 1 for data state Low, 0 for High |

A Setup/Hold type change re-sends sources, levels, slope, state and limit. Levels per §3.3 (clock
threshold → clock channel pair, data threshold → data channel pair, both with the bias of the data state:
Low `−h`, High `+h`). The setup or hold time limit and times as §5.3 (stored per limit).

Delay: A `0x80[14:12]` = 7, bit 6 = 1. Event A is a channel pattern: A `0xAC` states as §5.8 (logic AND,
A `0xAD[1:0]` = 0), its slope in A `0x80[5]` (1 rising). Event B is the trigger source A `0x0E[10:8]`, its
slope in A `0x80[7]` (1 rising). A `0x80[4:0]` is not rewritten by Delay. Delay time limit A `0x80[9:8]`,
times §5.3. Levels: event A → the named channel's level-1 pair, event B → the B channel's level-1 pair (§3.3),
both biased by the event-B slope: entering Delay with the default rising B slope writes every pair with `−h`; a
pattern-state change (`DELay:SOURce H,X,X,X`) rewrites no level; `DELay:SLOPe2 FALLing` rewrites both the event-A
channel's pair and the B channel's pair with `+h` (example: C1 0.5 V at 1 V/div → A `0xD7` 0x8B2 → 0x92E; C3 0.05 V →
A `0xF1` 0x856).

### 5.10 Apply frames (which change runs what)

Every trigger change runs inside a stop/arm frame (stop request A `0x19` ← 8 first, arm of spec 03 §3.2 last).
Within it, per kind of change:

| Change | Frame |
|---|---|
| Type change (§5.1), including a serial protocol change (I2C ↔ SPI ↔ UART ↔ CAN ↔ LIN) and the type sequence re-run by a sweep-mode change (§7) | type sequence; **position pass**; **level re-apply cycle** |
| Source command of any non-serial type (edge/trigger source, Qualified edge source, Delay `SOURce2`, Setup/Hold `CSource`/`DSource`), coupling command; Pattern `INPut`, `LOGic`, `LEVel`; Delay `SOURce` (event-A pattern) and `LEVel` | the writes; **position pass**; **level re-apply cycle**. A source command runs this even when the value is unchanged (e.g. `SHOLd:CSource C1` with C1 already the clock source) |
| Level commands (all types, incl. `ELEVel`, `QLEVel`, `LEVel2`, Setup/Hold thresholds), holdoff switch and time, Video standard / sync / line / field / custom settings, Dropout overtime type, Qualified `QSource`, Setup/Hold data state (`SHOLd:LEVel`), every I2C setting | the writes; **level re-apply cycle** |
| Limit, time values (`TLOWer`, `TUPPer`, Dropout `TIME`, Nth-edge `IDLE`), Qualified `TYPE`, Setup/Hold type entry and `SHOLd:TYPE` | **stored-record redraw** (spec 03 §3.7, with its live pass); the writes; **level re-apply cycle** |
| Slope / polarity (`SLOPe`, `POLarity`, `ESLope`, `SLOPe2`), Nth-edge count, every SPI, UART, CAN and LIN setting | the writes; plain arm (no `0x2E` toggle) |

- **Position pass**: the horizontal-position pass of spec 03 §3.3 step 5 (banks n = 0 … C − 1 with the spec 04 §9
  values) followed by the map position set (MAP `0x190` bank brackets). Its values depend on the trigger: the
  Dropout skew term and the HF/LF-reject term c of spec 04 §9. A replacement MUST re-evaluate spec 04 §9 after
  every type change and every coupling change (writing it after the other rows of this table is harmless).
- **Level re-apply cycle**: the stop / level writes / A `0x2E` ← 0 / arm / A `0x2E` ← 1 cycle of §3.2 (levels,
  per-channel pairs and search levels of the current type rewritten). A replacement SHOULD reproduce the `0x2E`
  toggle where the table has it (its FPGA meaning is UNVERIFIED; it is the per-frame switch-index toggle of spec 03
  §3.6).
- **Stored-record redraw**: optional for a replacement, as in spec 03 §3.3 step 7.

With writes equal to the shadow suppressed, a pass touches only the registers whose values change
(e.g. the Dropout skew changes bank 0's A `0x09E`/`0x08E`/`0x08A`/`0x0A7` on entering and leaving Dropout).

---

## 6. Holdoff

Only time holdoff exists in this FPGA. The holdoff selection OFF / EVENts / TIME maps to A `0x0A`
bit 8 = 0 / 1 / 1: selecting EVENts enables the *time* holdoff with the stored holdoff time (there is no
event counter). The event count and the holdoff start (LAST_TRIG / ACQ_START) write nothing; holdoff by
events is not offered on this product. An implementation SHOULD offer only OFF and
TIME.

```
ticks = holdoff_ns / 4               (integer division; holdoff_ns = holdoff time in integer ns, rounded as §5.3)
A 0x08 = ticks[15:0], A 0x09 = ticks[31:16], A 0x52 = ticks[47:32]
A 0x0A bit 8 = 1 holdoff on, 0 off
```

Examples (verified): 1.0000003 µs → 1000 ns → 250 ticks; 8 ns → 2; 30 s → 7 500 000 000 = 0x1_BF08_EB00
(A `0x52` = 1, A `0x09` = 0xBF08, A `0x08` = 0xEB00). Default holdoff time 80 ns (20 ticks).

Range: 8 ns … 30 s. The holdoff switch is per trigger type and forced off for Video, Serial, Qualified,
Delay and Setup/Hold (§5.1 step 6). Force mode clears A `0x0A` bit 8.

---

## 7. Sweep modes

`smp` below is the acquisition sample mode held by the firmware: 0 while roll mode is on, 1 otherwise
(the horizontal roll-mode switch sets it; spec 04). It is 1 at boot. Values in roll (100 ms/div, edge type):

| Roll event | A `0x0E` | Other |
|---|---|---|
| enter roll from Auto | 0, then 0x0F (type code 15) | — |
| Normal in roll | 0 (edge code, bit 12 = 0) | — |
| Auto in roll | 0x0F | — |
| type change in roll Auto | unchanged (code 15) | only the type's own registers (e.g. Pulse A `0x11`) |
| source change in roll Auto | code 15 with the new source bits: 0x10F / 0x11F (C2), 0x51F / 0x55F (LINE) | A `0x0A` bit 15 set and cleared again (§3.4) |
| leave roll (`:ACQuire:MODE YT`) | 0x1000 | A `0x0A` bit 0 ← 1 |

A sweep-mode change, in this order: (1) when segmented (sequence) acquisition is open, Single and Force
are programmed as Normal (with sequence on, `:TRIGger:MODE SINGle` and `FTRIG` write no trigger register;
switching sequence on itself changes an Auto sweep to Normal, A `0x0A` bit 0 ← 0, spec 03 §6.2); (2) the mode bits of the table below are written; (3) the full type sequence
of §5.1 is re-run with the current type (so A `0x0E[3:0]` is re-evaluated, §1); (4) the source is
re-applied (§3.4); (5) one soft reset follows (spec 03 §3.5: MAP `0x15A` pulse and
clear block, MAP `0x103` ← 1, A `0x003` ← 1, 0, MAP `0x103` ← 0) and a re-arm. The whole change runs inside a
stop/arm frame (stop request first; e.g. `:TRIGger:MODE NORMal`: A `0x19` ← 8, A `0x0A`, A `0x11` writes,
soft reset, DPO recall, arm).

| Mode | A `0x0E` bit 12 | A `0x0A` bit 0 (AUTO) | Other |
|---|---|---|---|
| Auto | 1 if `smp ≠ 0`, else 0 | 1 if `smp ≠ 0`, else 0 | — |
| Normal | 1 if `smp ≠ 0`, else 0 | 0 | — |
| Single | 0 | 0 | bit 12 = 0 (non-continuous); the PS does not re-arm after the frame and issues the stop request itself (A `0x19` ← 8; `:TRIG:STAT?` → Stop, spec 03 §3.6); whether the FSM would stop by itself is UNVERIFIED |
| Force | 0 | 1 | A `0x0A` bit 8 = 0; NTR_TIMEOUT (`0x0C/0x0D`) = 1; TR_TIMEOUT (`0x2A/0x2B`) = 1 |

When the MSO path is active (not on SDS800X HD) bit 12 is always cleared.

Force is a one-shot: the firmware programs the Force row, then (≈ 70–150 ms later)
re-programs the previously selected mode (its bit 12 and AUTO values, and A `0x0A` bit 8 back on when
holdoff is on; from Auto, AUTO stays 1). The timeouts written by Force (1) remain until the next timebase setup
rewrites them: in Auto/Normal that is the live pass of the next frame's redraw (spec 03 §3.7), right after the
re-program (e.g. 200 ns/div: NTR ← 0xDAC, TR ← 0x17D_7840 ≈ 10 ms later); in Single they stay 1. The selected mode
(reported by the host interface) stays the previous one.

Timeouts are rewritten on every timebase setup (spec 04), indexed by the time/div enum `tb` of spec 04
(0 = 200 ps/div, 9 = 200 ns/div; the same value the timebase setup writes to A `0x1C7`), using the table

```
T[tb], tb = 0..26: 1, 1, 9, 18, 44, 88, 175, 438, 875, 1750, 4375, 8750, 17500, 43750, 87500,
                   175000, 437500, 875000, 1750000, 4375000, 3500000, 7000000, 13125000,
                   26250000, 52500000, 131250000, 250000000
tb 1..21:   NTR_TIMEOUT = 2·T[tb],  TR_TIMEOUT = 2·tr_overtime
tb 22..25:  NTR_TIMEOUT = TR_TIMEOUT = 2·T[tb]
otherwise:  NTR_TIMEOUT = TR_TIMEOUT = 2·250 000 000
```

`tr_overtime` defaults to 12 500 000 (TR_TIMEOUT = 25 000 000 = 100 ms if the tick is 4 ns,
**UNVERIFIED** tick unit); the auto-trigger overtime setting v (default 12 500 000) gives TR_TIMEOUT = 2·v,
written directly when v changes. Force mode overrides both with 1 until the next timebase setup. The table
has a drop at tb 20 (T[19] = 4 375 000 > T[20] = 3 500 000); tb 27…35 (200 ms … 100 s/div) give the
"otherwise" value.

Status: A `0x19` bit 9 = triggered; A `0x65` trigger FSM state, A `0x6F` acquire state (spec 03).
Arming (start of acquisition) is spec 03.

**Start-up restore.** At start-up (spec 03 §2 step 17) the restored configuration of **every** trigger type is
written, whatever type is active, so all registers below hold defined values. Final values after a
factory-default start-up (they come from the restored settings, not from constants):

| Registers | Final value | Set by |
|---|---|---|
| `0x0F` (invert mask) | 0 | channel invert |
| `0x187` (A block = ACQ plane, `02` §4.1) | 1 | MSO master mode in the source setup |
| `0xAF` bit 8 | 0 (0x100 transiently) | bypass trigger (spec 03) |
| `0x198`, `0x199` | 0 | XY switch |
| `0x33` bit 8 | 1 (0x100) | display type |
| `0xDB` / `0xDA` | 0x7C2 / 0x3E | counter gate level (§9) |
| `0x111` | 0 (0x1000 → 0 pulses) | totalizer reset |
| `0x0E` | 0x1000 (after per-source passes 0x1100 … 0x1500) | source restore |
| `0xB0` | 0 (after 1, 2, 3) | search source |
| `0xB4` / `0xB2` | 0x7C2 / 0x3E | search level (§3.3) |
| `0xF1`/`0xF0`, `0x101`/`0x100`, `0xF3`/`0xF2` | 0x7C2 / 0x3E each | per-channel level restore |
| `0x0A` | 1 (0x8001 transiently) | AUTO |
| `0x12`/`0x13`, `0x72` | 2 / 0, 0 | T_A (§5.3) |
| `0x14`/`0x15` | 2 / 0 | T_B |
| `0x17`, `0x16` | 0x13E0, 0x1902 | video standard / line, any-line, field |
| `0x2C` | 0 | dropout overtime type |
| `0xAC`, `0xAD` | 0x2222, 0 | pattern polarity / type |
| `0xE6`/`0xE5`, `0xEC`/`0xEB`, `0x10B`/`0x10A`, `0x10D`/`0x10C` | 0xAE1 / 0x3E each | serial slicers (§10.1) |
| `0x80` | 0x1001 | qualifier (§5.9) |
| `0x87` | 1 | Nth edge |
| `0x44` | 0 | pass/fail operation |
| `0x42` | 0x1A | trigger-type set (§5.1 step 3; written by every type set, so the start-up value is part of it) |
| `0x29` | 0 | pass/fail trigger-out |
| M `0x30` | 0x0001 | serial: I2C protocol (0), DATA_SRC = 1 |
| M `0x32` | 0x0040 | serial data word 4 |
| M `0x31`, `0x33`–`0x36`, `0x3D`–`0x42`, `0x43`, `0x44` | 0 | serial data / mask words, length, CS/CLK source |
| M `0x45` / `0x46` | 0x0032 / 0x54E6 | baud word (19 200 Bd, the LIN restore, §10.2) |
| M `0x47` | 0x0480 | I2C address 0x80 ("any"), R/W code 1 |
| M `0x48` | 0x0010 | I2C condition 0, ACK code 1, address mask 0 |
| M `0x49` / `0x4E` | 0x800F / 0xFFFF | idle time enabled, 0xFFFFF |
| M `0x4F` | 0 | SPI CS mode / latch |
| M `0x50` | 0x0420 | UART: condition 0, idle 0, stop 0, parity code 1 (None), 8 data bits |
| M `0x51` | 0x1000 | CAN condition 0, bit 12 (LSB-first) set |
| M `0x52` | 0x003C | LIN: condition 0, spec, parity, sync and checksum error flags set |
| M `0x53` | 0x012A | bit timing below 10 MBd (BIT_LEN 9, SAMPLE_POINT 5) |

The M-block rows come from the serial restore, which programs every protocol in turn (I2C, SPI, UART, CAN, LIN, then
I2C as the active protocol); M `0x37`–`0x3C` and `0x4A`–`0x4D` are not written. A replacement may write only these
final values. The frequency-counter registers (`0x63`, `0x64`, `0x66`, `0x67`)
are not start-up values: the counter's periodic task writes them once a channel is enabled (§9). For A `0x16`
an implementation writes the §5.6 value of the restored line and field (0x1902 above, and 0x1903 after entering
Video, do not follow §5.6).

---

## 8. Trigger position

Pre- and post-trigger depth are acquisition registers: A `0x06`/`0x28` (pre-trigger count, bits
15:0 / 31:16) and A `0x07`/`0x25` (trigger delay count). Their computation from the horizontal
position is specified in spec 04 §9 (stored-record variant: spec 04 §12); the delay range is spec 09 §3.3.

---

## 9. Trigger frequency counter

The hardware frequency counter counts the trigger source: A `0x0E[6:4]` = counter source; gate
length A `0x63` = N from the expected frequency f₀: <10 Hz → 1, <100 Hz → 5, <1 kHz → 10,
<10 kHz → 100, <100 kHz → 1000, <1 MHz → 10000, ≥ 1 MHz → 1 (then `0x64` = 0; spec 15 §4); A `0x66` clear, A `0x67` gate count, results A `0x68`/`0x69` and
A `0x6A`/`0x6B`, A `0x6E` done flag (details spec 15). First writes after the first channel is switched on:
A `0x67` = 900 (0x384), A `0x66` pulsed 0 → 1, then A `0x63` = 1, A `0x64` = 0; afterwards A `0x66`
is pulsed 0 → 1 about every 1.7 s, and A `0x0E[6:4]` follows the counter source (= the trigger source; written
after the level re-apply of a source change, §3.2). For Pattern (A `0x0E[10:8]` = 0) the counter source is the
channel most recently addressed by a pattern command: `INPut H,L,X,H` → 3 (the last channel of the list), then
`LEVel C2,…` → 1; Delay and Setup/Hold use the channel in A `0x0E[10:8]`. The counter gate-level setup also writes the
CH2 level-1 hysteresis register A `0xDA` (0x3E); whether the counter uses that comparator is UNVERIFIED.

---

## 10. Serial (protocol) triggers

Supported in hardware: **I2C, SPI, UART, CAN, LIN.** Programming a serial trigger always sets the
trigger type to Serial (A `0x0E[3:0]` = 9) first, then writes the A-block slicer levels and the M
block.

Selecting a serial trigger switches holdoff off and coupling to DC for every protocol (§5.1 step 6).

### 10.1 Slicer levels

For every analog channel that the protocol uses, the per-channel serial threshold is written:

```
code = 2048 + trunc( (float)L_px · 8.0f ) − (A 0xE1 >> 1)          (half the current LEVEL1 hysteresis)
SER_LEVEL code register = code, SER_LEVEL hysteresis register = A 0xE1
```

(registers in §2.1; `L_px` is the protocol's threshold for that channel in the unit of §3.2). A threshold command
writes only its own channel's slicer. A protocol entry writes the slicer of every used channel (that it rewrites at
least the first source's slicer — SCL / CLK — is confirmed).

### 10.2 Bit timing (UART, CAN, LIN)

```
BAUD_WORD (M 0x45:0x46) = floor( baud · 2^32 / 25 000 000 )
if baud < 10 000 000:  M 0x53[0] = 0, BIT_LEN (M 0x53[9:5]) = 9,  SAMPLE_POINT (M 0x53[4:1]) = 5
else:                  N = 1 000 000 000 / (4 · baud); M 0x53[0] = 1, BIT_LEN = N − 1, SAMPLE_POINT = (N − 1) >> 1
```

(250 MHz decoder clock: 25 000 000 = 250 MHz / 10.)

### 10.3 Data/mask words

DATA and MASK each hold up to 96 bits in six 16-bit words; word `k` is written to M `0x36 − k`
(data) and M `0x42 − k` (mask). A mask bit 1 means "compare this bit". The protocol sections give
the word contents. A data byte value of 256 (`0x100`) in the API means "don't care".

### 10.4 I2C (PROTOCOL = 0)

Order: M `0x30[11:8]` = 0, `0x30[12]` = 0, CLK_SRC = SCL source, DATA_SRC = SDA source, then the
condition:

| Condition (API) | M `0x48[2:0]` | Further writes |
|---|---|---|
| Start 0, Stop 1, Restart 2 | 0, 1, 2 | `0x48[5:4]` = 1 |
| No-ACK 3 | 3 | `0x48[5:4]` = 3 |
| EEPROM 4 | 4 | `0x48[5:4]` = 1; `0x47[12]` = 0; `0x47[11:10]` not written (keeps the last R/W code); `0x30[7:5]` = data compare; `0x47[9:0]` = 0x50; `0x48[15:6]` = 0x78; data word 0 = data byte 1 (the don't-care value 256 is written as is, 0x100); mask word 0 = 0xFF (0 if don't care); `0x43` = 1 (0 if don't care) |
| 7-bit addr + data 5 | 4 | `0x48[5:4]` = 1; `0x30[7:5]` = 0; `0x47[9:0]` = address; `0x47[12]` = 0; `0x48[15:6]` = 0x7F (0 if address = 0x80 "any") |
| 10-bit addr + data 6 | 4 | as 5 with `0x47[12]` = 1 and mask 0x3FF (0 if address = 0x400 "any"; then `0x47[9:0]` ends 0: the value 0x400 reaches bit 10, which the following R/W field write clears) |
| Data length 7 | 5 | `0x48[5:4]` = 1; `0x48[15:6]` = 0; mask word 0 = 0; `0x30[7:5]` = 0; data word 0 = length byte; `0x47[12]` = address width; `0x47[11:10]` = 1 |

The address is not written in condition 7. For conditions 5 and 6, with data bytes `d1`, `d2`: both given → data word 0 = `d1 << 8 | d2`,
mask 0xFFFF, `0x43` = 2; one given → that byte, mask 0xFF, `0x43` = 1; none → 0, mask 0, `0x43` = 0;
`0x49[14:4]` = 0; data word 4 (M `0x32`) = 0; R/W `0x47[11:10]`: write → 0, read → 3, any → 1. Example:
`RWBit READ` (M `0x47` = 0x0C50), then `CONDition EEPRom` writes only M `0x48` = 0x1E14 and M `0x47` stays 0x0C50.

### 10.5 SPI (PROTOCOL = 1)

Inputs: CLK, MISO, MOSI, CS, NCS sources; trigger on MISO or MOSI; CS type (NCS 0, CS 1, timeout 2);
idle timeout; CLK latch edge; data length `n` bits (≤ 96); bit order; per-bit value 0/1/X.

Data and mask: with the `n` bits listed first-to-last as `v[0] … v[n−1]`: MSB-first → pattern bit
`n − 1 − i` = `v[i]`; LSB-first → pattern bit `i` = `v[i]`. Pattern bit `k` goes to word `k / 16`, bit
`k mod 16` (word 0 = M `0x36`, word 1 = M `0x35`, …, §10.3). Value 1: data and mask bit set; value 0:
mask bit set; X: neither. When the length grows, the stored list is padded with X at the end. Example
(verified): `n` = 8, `1,0,1,X,0,0,1,1` → LSB-first data 0xC5 / mask 0xF7, MSB-first 0xA3 / 0xEF.

Order: `0x30[11:8]` = 1; CLK_SRC; DATA_SRC = MISO or MOSI source; `0x4F[1:0]` = NCS → 0, CS → 3,
timeout → 1; for NCS/CS: CS_SRC = NCS/CS source, `0x49[15]` = 1, idle time = `0x7FFFFF >> 3` = 0xFFFFF; for
timeout: `0x49[15]` = 1, idle time = `timeout_ns >> 3` (8 ns units; 1 µs → 125, 250 ns → 31);
`0x30[7:5]` = 0; DATA; MASK; position: any → `0x30[12]` = 0, value → `0x30[12]` = 1 and
`0x49[14:4]` = position; `0x43` = `n`; `0x4F[3]` = 1 for latch edge falling, 0 rising; `0x4F[2]` = 0.

Idle time (bits 19:0) = `0x49[3:0]` : `0x4E`.

### 10.6 UART (PROTOCOL = 2), CAN (PROTOCOL = 5), LIN (PROTOCOL = 3)

**UART.** Order: `0x30[11:8]` = 2; DATA_SRC = RX or TX source; `0x50[1:0]` = condition (Start 0,
Stop 1, Data 2, Error 3); `0x51[12]` = 1 for LSB-first, 0 for MSB-first; for Data: `0x30[7:5]` =
compare, `0x49[14:4]` = 0, the stored value `v` is first clamped to `2^bits` (`2^bits` = "don't care";
the clamped value replaces the stored one, also when the width changes), then data word 0 = `v & 0xFF`, mask
word 0 = `2^bits − 1` if `v < 2^bits`, else 0 (verified: 165 at 7 bits → data 0x80, mask 0; then 5 bits →
0x20, mask 0; then 8 bits → 0x20, mask 0xFF; 256 at 8 bits → data 0, mask 0); bit timing (§10.2) with baud 600/1200/2400/4800/9600/19200/38400/57600/115200 or custom;
`0x50[10:7]` = data bits; parity (with condition Data, Mark and Space are swapped first):

| Parity | `0x50[6:5]` | `0x50[14:13]` |
|---|---|---|
| None | 1 | 0 |
| Odd | 0 | 0 |
| Even | 3 | 0 |
| Mark | 0 | 2 |
| Space | 0 | 3 |

`0x50[4:3]` stop bits: 1 → 0, 1.5 → 1, 2 → 3; `0x50[2]` idle level; `0x43` = data bits.

**CAN.** Order: `0x30[11:8]` = 5; DATA_SRC = CAN_H or CAN_L source; `0x51[2:0]` = condition (Start 0,
Remote 1, ID 2, ID+Data 3, Error 4). For conditions 1–3: 11-bit ID → data word 5 = `id << 2`, mask
word 5 = `0x1FFC` (0 if id = `0x800` "any"); 29-bit → data words 4/5 = id low/high, mask
`0x1FFFFFFF` (0 if id = `0x20000000`); condition 3 adds data bytes in word 0 as for I2C (two bytes:
`d1 << 8 | d2`, mask 0xFFFF, length 16; one byte: mask 0xFF, length 8; otherwise length 16, mask 0);
conditions 1, 2: length 0; `0x30[7:5]` = 0; `0x49[14:4]` = 0; DATA; MASK; `0x43` = length;
`0x51[4:3]` = 0 for 11-bit, 3 for 29-bit. Error condition writes nothing further (no hardware).
Bit timing §10.2 with baud 5k/10k/20k/50k/100k/125k/250k/500k/800k/1M or custom.

**LIN.** Order: `0x30[11:8]` = 3; DATA_SRC; `0x52[1:0]` = condition (Break 0, ID 1, ID+Data 2,
Data error 3); `0x52[5]` checksum error, `0x52[4]` sync-byte error, `0x52[3]` header-parity error,
`0x52[2]` LIN spec (enable flags of the error condition); then for ID / ID+Data: data word 4 = id,
mask word 4 = `0x3F` (0 if id = `0x40` "any"); ID+Data adds data bytes in word 0 as CAN with length
codes 2/1/0 (two/one/none); for Data error: word 4 = error id, mask `0x3F`, `0x43` = configured count;
`0x30[7:5]` = 0; DATA; MASK; `0x43`; bit timing §10.2 with baud 600/1200/2400/4800/9600/19200 or
custom. The LIN ID-compare write to `0x52` clears bits 15:13 and ORs the value in at bit 6;
it has no known effect.

### 10.7 Protocols without trigger hardware

FlexRay, CAN FD, I2S, MIL-STD-1553B, SENT, Manchester, ARINC 429, USB 2.0, CAN XL (and the CAN error
sub-types): the PL image has no trigger hardware for them; their settings write no register, and they are
not offered on SDS800X HD (nor is zone trigger). An implementation MUST NOT offer them as
hardware triggers on this FPGA image (decode-only).

---

## Open

- FPGA arming rule of the level/hysteresis pair (is `code` the arm or the fire threshold?); settle
  with a unit measurement of trigger point vs level at large hysteresis.
- AC / LF-reject / HF-reject filter characteristics in the FPGA (A `0xE4`): measure on a unit.
- Units of TR_TIMEOUT (`0x2A/0x2B`) and NTR_TIMEOUT; FPGA meaning of A `0x0E` bit 12; settle by timing
  auto-trigger on a unit (Auto/Normal/roll register values are known).
- Video sync polarity and the FPGA behaviour for custom line periods > 1023 (§5.6); whether the FPGA
  ignores A `0x11` bits 7 and 1:0 for non-Pulse types; comparator position relative to channel invert (§3.3).
- Dropout overtime-type default (§5.4): read the factory setup record of a unit.
- Range-check knob step at decade boundaries (§5.3); whether a protocol entry rewrites every used slicer (§10.1).
- Map-side level copy M `0x1E7`–`0x1E9` (§3.2): what quantity L is and what the PL uses the
  pair for (it follows the channel's vertical position).
- Not yet confirmed on a unit: FPGA-side behaviour (arming rule, filter corners, timeout
  tick), `:TRIG:STATus` (software FSM), pass/fail trigger out.
