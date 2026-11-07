# 02 — Register Map

The PS reaches every PL function through memory-mapped AXI registers in the HPM window at `0x8000_0000`. All
acquisition, trigger, analog-control, display and front-panel actions reduce to 16-bit register values in two
**register planes** (ACQ and MAP), plus a standard Xilinx AXI DMA for sample data and two AXI VDMAs for video. This
document defines the addressing model, the access rules, and the registers by function; it is the register reference
the firmware uses. Detailed sequences and values live in the specs named in each section.

---

## 1. Bus and addressing model

### 1.1 Mapping

| Item | Value |
|---|---|
| Device node | `/dev/mem`, opened `O_RDWR \| O_SYNC` (= 0x101002 on aarch64; `O_LARGEFILE` is not set) |
| Window per block | `mmap(NULL, 0x10000, PROT_READ\|PROT_WRITE, MAP_SHARED, fd, phys_base)` — 64 KiB, one mapping per block, kept for the life of the process |
| ACQ plane | phys `0x8003_0000` |
| MAP plane | phys `0x8004_0000` |
| Register address | `phys_base + 4 × idx`, `idx` = 0x000 … 0x1FF (512 registers per plane) |
| Access width | one aligned 32-bit load/store per access; only bits 15:0 carry data, write bits 31:16 as 0 |

Notation in all specs: **ACQ 0x019** = ACQ-plane register index 0x019 = byte address `0x8003_0064`; **MAP 0x14D** =
`0x8004_0534`. Wider quantities are split low-word-first over consecutive (or listed) indices.

### 1.2 Rules the implementation must follow

- Serialise all accesses to a plane behind one lock; several operations are read-modify-write sequences or
  select-then-access pairs (configuration banks §4.3, scope-ID read §3, readout setup §5).
- Use volatile accesses; never merge, split or elide them. Many registers are **strobes** whose effect is the write
  itself (0→1 pulses), so writing the same value twice is meaningful.
- **Keep a software shadow** of every control register you write and do read-modify-write on the shadow. The
  hardware read value of a control index is not guaranteed to equal what was written: several indices carry status
  bits in the read direction and control bits in the write direction (e.g. ACQ 0x019, 0x01F, 0x07D). Read hardware
  only for status: the polls of §1.3 and the registers marked R.
- **Write rule (authoritative for every spec).** The firmware writes **unconditionally**; re-writing an unchanged
  value is assumed harmless for every non-strobe register (not yet confirmed on a unit; for the configuration banks
  both write rules of §4.3 are correct). Strobes and pulses (0→1→0 sequences, bank commits) are always written. Where
  a spec lists the register writes of an operation, a register whose value does not change may be missing from the
  list; writing it again gives the same resulting register values (e.g. after start-up, `03` §2). Where a spec says a
  value is written "only if it changed", it may be written every time unless the spec states an extra side effect
  (e.g. a settle wait).
- Reset value of every register: assume 0 / unknown; the firmware initialises everything it uses (§11, `03` §2).

### 1.3 Status polls

Every poll uses one primitive: read the register from hardware, sleep at least 1 µs, repeat until the bit is set
(or, where noted, clear), at most `N + 1` reads. Unless a spec states otherwise, a timeout is logged and the sequence
**continues** (no error path). Unless stated otherwise the sleep is the shortest the OS offers, so the wall time per
iteration is dominated by the scheduler (≈ 50–100 µs on the target, not yet confirmed). The poll budgets N below are
our defaults. Poll table (all raw hardware reads):

| Register | Bit(s) | Wait for | N | Used by |
|---|---|---|---|---|
| ACQ 0x021 | 0 | 1 | 300 000 | before every arm; after soft reset when a digital slave board is present |
| ACQ 0x019 | 9 | 1 | 30 000 | stop ready (frame done) after a stop request; not the frame-ready test (MAP 0x082) |
| ACQ 0x019 | 0 | 1 | 30 000 | calibration (trigger interpolation) value ready |
| ACQ 0x0A4 | 0 | 1 | 100 000 | calibration value invalidated after read start |
| ACQ 0x01F | 8 | 1 | 5 000 | ADC PLL locked |
| ACQ 0x01F | 12, 13 | 1 | 5 000 each | xPLL locked (start-up) |
| ACQ 0x07C | 0 (ADC0) / 1 (ADC1) | 1 | 1 000 (1 ms sleep), after a 10 ms settle; up to 3 ADC-PLL reset retries | ADC data PLL ready, phase training (`06` §9.4; only bit 0 is polled on this board) |
| ACQ 0x0FD | 7 | **0** | 5 002 | map engine ready |
| ACQ 0x116 | 0 | 1 | 20 000 | draw stopped |
| ACQ 0x181 | 0 | 1 | 1 000 (1 ms sleep) | scope-ID ready |
| ACQ 0x1E7 | 0 | 1 | 30 000 | board-test AC-line / EXT test done (`15`) |
| ACQ 0x06E | 0 (auto gate) / 4 (period gate) | 1 | caller-defined | frequency counter gate done; the bit follows the measurement mode (`15` §4) |
| MAP 0x082 | 0 | 1 | single read per check (≈ 220 ms period) | last-frame flag: a complete frame is held (`03` §3.6) |
| MAP 0x187 | 3:0 | ≠ 0 | 5 002 | wave move done |
| MAP 0x188 | 3:0 | 0 | 50 002 | resize done |
| MAP 0x188 | 8 | 0 | 50 002 | clear-preparing done |
| MAP 0x01A | 0 | 1 | 5 002 | distance data valid |
| MAP 0x0B8 | 3:0 | ≠ 0 | 10 002 | search done |

## 2. Address blocks

| Phys range | Block | Owner of semantics |
|---|---|---|
| `0x8000_0000`–`0x8000_FFFF` | UI VDMA: UI framebuffer → LCD (MM2S), screen capture for VNC (S2MM) | §10, `07` |
| `0x8001_0000`–`0x8001_FFFF` | Xilinx AXI DMA (direct register mode, S2MM only): record readout to PS DRAM | §5, `03` |
| `0x8002_0000`–`0x8002_FFFF` | waveform VDMA: waveform layer (S2MM + MM2S at `0x1E40_0000`) | §10, `07` |
| `0x8003_0000`–`0x8003_07FF` | **ACQ plane**: acquisition FSM, record memory, readout, sample rate, ADC interface, offset DACs, trigger, counters, identity | this doc §3–§8 |
| `0x8004_0000`–`0x8004_07FF` | **MAP plane**: waveform mapping/rendering, persistence, LCD reset/enable/brightness, key matrix, encoders, LEDs, VNC capture enable | §9, `07`, `08` |

The `siglent_dma` kernel driver's interrupt acknowledge at `0x8003_0580` is ACQ 0x160 (read clears); it is not a
separate block (`14`). Nothing in the window outside the five blocks is used.

## 3. Identity and bring-up registers (ACQ plane)

| Idx | Dir | Field | Meaning |
|---|---|---|---|
| 0x000, 0x001, 0x002 | R | 15:0 each | FPGA version, 48 bits (0x002 = most significant); for display only, never compared. Values of the factory PL image not yet confirmed (read `devmem 0x80030000/4/8` on a unit). 0x002 bits 15:8 = board-test byte: 0xBD = a board-test bitstream (treat as not production: no scope ID, `15`), 0 on production units |
| 0x151 | R | 3:0, 7:4 | PCB / BOM version register. Present in the image but **not used** on this board: PCB and BOM version come from the hw-id GPIO byte (below) |
| 0x180 | W | — | Scope-ID read request: write 0, then 1 |
| 0x181 | R | bit 0 | Scope-ID ready (poll ≤ 1000 × 1 ms) |
| 0x182–0x185 | R | 15:0 each | 64-bit scope ID (device DNA), 0x182 least significant |
| 0x1FB | W | value | ADC type: 0 = ADC12D1000, 1 = ADC12DL1500 (from the BOM/ADC detection, `06`) |
| 0x1FA | W (RMW) | bit 8 | bit 8 = ADC PLL sync pulse (1, wait 10 ms, 0, wait 10 ms; `06` §9.4). No other bit is used on this board: the ADC mode is set over SPI (`06` §9.2) |
| 0x01F | W / R | W bit 0 (ADC0), bit 1 (ADC1) / R bits 8, 12, 13 | W: ADC PLL reset pulse (1 then 0). R: bit 8 ADC PLL locked, bits 12/13 xPLL locked; "ADC working" = bits 8 and 12 both set |
| 0x04F | W | value | ADC ISERDES reset: 1 = assert, 0 = release |
| 0x1EF | W | value | AXI enable: written 1 once during chip init (PL effect not yet confirmed) |

Board identity: the SDS800X HD has a 12-bit ADC and 16-bit sample words, and comes as a 4-channel or a 2-channel
board (hw-id bit 7, below); the constants of `04` are identical for both, and the board-specific rules of other
specs (e.g. `03` §4.3 compression, `03` §6.4 roll payload, `15`) apply to both variants.
No ACQ register identifies the model. The **hw-id byte** is read from the eight PS GPIO lines exposed as sysfs
GPIOs 334 … 341 (bit n = value of GPIO 334 + n, `01`/`13`): bit 7 = 1 → 2-channel model, 0 → 4-channel model;
bits 3:0 = PCB version, bits 7:4 = BOM version. Within the BOM nibble the bits are also used individually, and the
uses do not conflict: bit 4 (BOM bit 0) selects the ADC part (0 = ADC12D1000, 1 = ADC12DL1500, written to 0x1FB,
`06` §9.1); bits 5:4 both set select the newer clock-chip variant and its programming sequence (`13` §4.1), so a
board with the newer clock chip always carries the ADC12DL1500; bit 7 is the channel-count strap. Bandwidth
(70/100/200 MHz) and the "edu" flag come from the identity data in `09`, not from a register. ACQ 0x059 is not used
on this board.

## 4. Acquisition control registers (ACQ plane)

### 4.1 Run control

| Idx | Dir | Bits | Meaning |
|---|---|---|---|
| 0x003 | W | bit 0 | Record-memory reset / soft reset: write 1 then 0 |
| 0x003 | W | bit 4 | Arm strobe: write 0x10 then 0 (arm sequence `03` §3.2) |
| 0x019 | W | bit 3 | Stop request: 1 = stop the acquisition (`03` §3.4). Also set right after arming when no analog and no digital channel is enabled (`03` §3.2 step 7). Written as RMW of bits 7:3 with value `n & 0x1F` (only n = 0/1 used) |
| 0x019 | W | all | 0 written in the arm sequence (clears the stop request) |
| 0x019 | R | bit 0 | calibration (trigger-interpolation) value ready (`03` §4.1). In roll mode bits 9 and 0 both set = a normal trigger occurred (`05` §2.1) |
| 0x019 | R | bit 3 | stop status |
| 0x019 | R | bit 9 | frame done: record complete and acquisition stopped (also read as "triggered" and "stop ready": one bit) |
| 0x021 | R | bit 0 | record memory ready (DDR calibrated / FSM idle-ready). Must be 1 before arming |
| 0x021 | R | bit 8 | record-memory compare error (DDR self-test) |
| 0x06F | R | 15:0 | trigger/acquire state; read once during arm (value not used) |
| 0x03E | R | 15:0 | valid parameter of the current frame |
| 0x1C9 | R | 15:0 | time-base index of the current frame |
| 0x115 | W | value | draw stop: 1 = the map engine keeps drawing the held record (stored-record redraw, `03` §3.7), 0 = live drawing; written 0 by every arm (`03` §3.2 step 0) |
| 0x116 | R | bit 0 | draw-stop ready: poll = 1 (§1.3) before writing 0x115 ← 1 |
| 0x01E | W | value | wave-save enable: 1 = freeze the current frame against overwrite during a record readout (`03` §4.3 steps 1/10), 0 = release; 0 at roll start |
| 0x02E | W | value | switch index: 0 before and 1 after the per-frame re-arm (`03` §3.6); 0 at start-up (`03` §2) |
| 0x187 | W | value | MSO master mode: 1, written once by the trigger source setup at start-up (`05` §7) |
| 0x0B1 | W | 3:0 | analog channel enable mask, bit 0 = CH1 … bit 3 = CH4 (determines the route, `04` §1). Not read back from hardware: keep a software copy |
| 0x0F8 | W | 0, 8 | bit 0 history direction (1 = forward), bit 8 history pause (0x100 at register init; 0x001 with sequence on, 0x101 off; `03` §6.2–§6.3) |
| 0x0F4 / 0x10F | W | 15:0 / 15:0 | history frame interval T in µs, bits 15:0 / 31:16 (`03` §6.3) |
| 0x0F5 / 0x0F6 | W | 15:0 | history first / last segment, bits 15:0; bit 16 of each in 0x0F7 bit 0 / bit 8 (RMW) |
| 0x0F9 / 0x0FA | R | 15:0 / 0 | segment being shown / played = 0x0F9 \| (0x0FA bit 0) << 16 |
| 0x0EA | W | bit 0 | timestamp reset: pulse 1 → 0 at register init and after arming in some paths |
| 0x03C | W | — | 1 at register init |
| 0x128 | W | — | function not known on this board; 0 at register init and not written afterwards |
| 0x018 | W/R | 9:0, 10, 12, 13 | sequence segment number bits 9:0 (bits 16:10 in 0x05B[6:0]; the number of segments of sequence mode, `03` §6.2), sequence mode enable (10), segment-select enable (12), read start (13, pulse) |
| 0x04D | W | 15:0 | sequence read segment (segment selected for readout / timestamp read), bit 16 in 0x05B[8] (`03` §6.2) |
| 0x026 | R | 15:0 | segment count bits 15:0 |
| 0x05B | R / W (RMW) | 9 (R); 8, 7, 6:0 (W) | R: bit 9 = segment count bit 16 (bits 15:10 ignored). W: bit 8 = sequence read segment (0x04D) bit 16; bit 7 = segment length (0x027) bit 16; bits 6:0 = sequence segment number (0x018[9:0]) bits 16:10. Each field is written by RMW preserving the others |
| 0x120 | R | 0, 8 | bit 0 ping-pong (double-buffer) state; bit 8 previous-buffer sequence overflow |
| 0x121 / 0x122 | R | bit 0 / 15:0 | previous-buffer segment count = 0x122[15:0] \| (0x121 bit 0) << 16 |
| 0x047 | W | 11:0, 12 | acquisition start delay: bits 11:0 value, bit 12 enable (`03` §3.2 step 1) |
| 0x01C / 0x01D / 0x0E8 | R | 15:0 each | frame timestamp, one 48-bit counter = 0x0E8 << 32 \| 0x01D << 16 \| 0x01C; reset by the 0x0EA pulse; tick 1 µs (assumed, `03` §6.3). Per segment: select the segment (0x018 bit 12, 0x04D), read-start pulse, wait 0x0A4 bit 0, then read (`03` §6.3) |

### 4.2 Record geometry and horizontal position

Written by the timebase setup (`04` §8). Units are not all known; see `04` Open.

| Idx | Meaning |
|---|---|
| 0x005 | store-length code |
| 0x006 / 0x028 | pre-trigger count low 16 / high 16 |
| 0x007 / 0x025 | trigger delay count, low 16 / high 16 |
| 0x01A / 0x01B | record depth low 16 / high 16 |
| 0x027 | segment length |
| 0x00C / 0x00D | no-trigger (auto) timeout low / high |
| 0x02A / 0x02B | triggered timeout low / high |
| 0x09A / 0x09B | normal-record start address low / high |
| 0x08A, 0x08C, 0x08E, 0x09E, 0x0A7–0x0A9 | horizontal position helpers |
| 0x092, 0x094, 0x10E | trigger-position helper counts |
| 0x0A0 | readout interval (1 if I < 2, else 4; `04` §8.2 step 2) |
| 0x039 | trigger-interpolation insert count |
| 0x081 | display interpolation mode, bit 0 (1 = sin(x)/x, the default; 0 = linear, not yet confirmed), bank-windowed (`04` §8.3) |
| 0x118 | stored-record search enable: 1 in the stored-record bank pass (`04` §12) |
| 0x040 / 0x041 | R: PL pre-trigger address bits 15:0 / 31:16; enters the readout fine value while decimating (`03` §4.3) |
| 0x010 | bit 15 scan enable, bits 11:0 scan number (0x8000 written at setup) |
| 0x083, 0x085, 0x086, 0x090, 0x096, 0x098, 0x0B6, 0x0CE, 0x1C7 | timebase-setup parameters (values in `04` §8) |
| 0x1EE | record-memory ("DDR4") delay setting, written by every timebase setup: 1 while the acquisition-rate mode is SLOW (`:ACQuire:AMODe SLOW`, `09`); otherwise 0x1A8 when the rate is 2 GSa/s and the screen sample count S = 1000 (50 ns/div at 1 channel), else 0x400; 10 in roll (`04` §7). A rate-mode change rewrites it at once with the same rule, outside any bank window (`04` §8.2 step 10) |
| 0x1DF–0x1E4 | resize (vertical code mapping) per configuration bank: 0x1DF/0x1E0 offset (32 bit, low/high), 0x1E1/0x1E2 scale (32 bit, 16384 = 1.0), 0x1E3 min code, 0x1E4 max code. Main banks 0 … C − 1: scale 16384, offset 0, min 0x080, max 0xF7F (the screen limits, `03` §5) — except in the stored-record pass, where scale and offset map the held record's V/div and position to the current ones (`04` §12; restored by the live pass). Zoom banks (vertical zoom with gain g): scale = round(16384 · g), offset and limits per `04` §11 (not yet confirmed on a unit). The PL maps sample codes within the screen limits (`03` §5) to display rows directly (8 codes per row); the vertical position is applied in the analog offset (`06` §5), not here |
| 0x03D, 0x03F, 0x070, 0x071, 0x074, 0x076, 0x082, 0x0B8, 0x0BA, 0x0BE, 0x0C0 | waveform-search / sync configuration (search compression, sync large/small cycle) |
| 0x089 | zoom enable (1 on / 0 off, with MAP 0x10A; `03` §6.5) |
| 0x097, 0x099 | zoom part enable (1) / zoom section count, written in zoom banks 4 + n (`04` §11) |
| 0x198 | XY mode: 0 off, 1 XY-only display, 0x10 YT+XY display (same value as MAP 0x192; `04` §10) |
| 0x199 | XY channel pairs active: 1 = a channel of CH1/CH2 on and none of CH3/CH4, 4 = only CH3/CH4 on, 5 = both pairs, 0 = none; written before 0x198 on every XY switch and acquisition-mode change |
| 0x095, 0x09F | YT+XY record set: 0x095 ← 1 (0 in ADC calibration mode), 0x09F ← XY section count (`04` §10, not yet confirmed on a unit) |

### 4.3 Configuration banks — ACQ 0x110

Per-channel parameters (resize, search configuration, per-channel timebase values, trigger skew) are written into
a staging register set and **latched into a bank** selected by ACQ 0x110:

| Bits | Field |
|---|---|
| 0 | bank write enable |
| 7:4 | bank number: 0–3 = CH1–CH4 main time base; 4–7 = CH1–CH4 zoom (and XY) |
| 8 | commit pulse |

Sequence for bank `n` (must be followed exactly):

1. `0x110[0] = 1`, then `0x110[7:4] = n` (RMW, two writes: `…|1`, then `(n<<4)|1`).
2. Write the per-channel registers for channel `n`.
3. `0x110[8] = 1`; sleep 10 µs; `0x110[8] = 0`; `0x110[0] = 0`.

Run this for **every channel of the model**, n = 0 … C − 1 (C = 4 or 2, independent of which channels are enabled
in 0x0B1), on every timebase change, then for n = 4 … 4 + C − 1 when zoom (or XY) is configured. Note the step-1
order: bit 0 is set while bits 7:4 still hold the previous bank number (e.g. 0x30 → 0x31, then 0x01 for bank 0).

Which registers are banked and which are global is not yet known. Whatever a bank commit latches, a register left
unwritten in window n keeps the value written in an earlier window, so writing all timebase registers in bank 0's
window and, in banks 1 … C − 1, only the values that differ (in practice the skew-dependent `04` §9 registers) sets
every channel correctly. Hence two write rules are correct; use one of them inside every bank window
n = 0 … C − 1, in the order of `04` §8:

- **Full set** (recommended): write every register of the set in every window, unconditionally. Independent of
  any shadow state; costs ≈ 50 extra writes per bank.
- **Changed values only**: write in each window every register whose value differs from the value last
  written to it in any window, with a shadow that starts empty (so the first pass after start-up writes the full
  set in bank 0) and is never assumed equal to hardware for registers not yet written.

Strobes (0x110 commit, pulses) are never suppressed (§1.2).

## 5. Record readout and the sample-data DMA

### 5.1 ACQ-plane readout registers

| Idx | Dir | Bits | Meaning |
|---|---|---|---|
| 0x0D3 | W (RMW) | 1:0 | read mode: 0 = DMA stream (real-time/roll), 1 = DDR record readout, 2 = screen-bitmap readout; other bits preserved |
| 0x0C9 | W | — | DMA buffer init: 0 then 1 |
| 0x0CA | W | — | DMA buffer enable: 1 before a readout, 0 after the last block |
| 0x0C1 | W | 15:0 | readout channel select: 0–3 = CH1–CH4 record (zoom reads use the same 0–3), 4 = digital (MSO) |
| 0x0C3 / 0x0C4 | W | 15:0 / 6:0 | readout start offset from the record origin, in units of Δ samples (the DDR interval of `04` §8.1), bits 15:0 / 22:16 |
| 0x0C5 / 0x0C6 | W | 15:0 / 9:0 | readout length in 4-sample units, bits 15:0 / 25:16 (the transfer itself may be shorter, `03` §4.3) |
| 0x0C2 | W | — | record readout start: 0 then 1 |
| 0x0CF / 0x0D0 | W | 15:0 / 7:0 | DMA transfer length in 8-byte words = ⌈bytes / 8⌉, bits 15:0 / 23:16 |
| 0x0C8 | W | — | DMA transfer start: 0 then 1 — the PL starts streaming the requested bytes to the AXI DMA |
| 0x0D3 | W (RMW) | 8 | 1 − compression type (1 for every analog readout) |
| 0x0D5 | W | 15:0 | DMA compression rate r: 0 = none (factor 1); 0 < r < 0x1000 → the PL delivers every 2r-th point (`03` §4.3, §6.4) |
| 0x0D4 | W | — | DMA compressor reset: 0, 1, 0 after 0x0D5 |
| 0x024 | W | — | scan fast mode: written at roll start (1 or 0), 0 when leaving roll and for normal runs (`03` §6.4) |
| 0x0CB | R | 10:0 | roll stream: data available in 4-byte units; total available bytes = 4·(0x0CB & 0x7FF) + 8·(MAP 0x047 & 0x7FF) (`03` §6.4) |

### 5.2 AXI DMA at `0x8001_0000`

A standard Xilinx AXI DMA in **direct register mode**; only the S2MM (stream → memory) channel is used. It is not in
the device tree; the firmware maps it through `/dev/mem` (64 KiB). Offsets (bytes):

| Offset | Register | Use |
|---|---|---|
| 0x30 | S2MM_DMACR | bit 0 RS (run/stop), bit 2 Reset (self-clearing), bit 3 Keyhole (left 0), bit 4 Cyclic BD Enable (cleared; scatter-gather only), bit 12 IOC_IrqEn, bit 14 Err_IrqEn |
| 0x34 | S2MM_DMASR | interrupt status; written `0xFFFF_FFFF` by the `dmadata_irq` kernel driver's interrupt handler to acknowledge |
| 0x48 | S2MM_DA | destination address = `0x1E80_0000` (udmabuf0 / reserved `rawdata`) |
| 0x4C | S2MM_DA_MSB | 0 |
| 0x58 | S2MM_LENGTH | transfer length in bytes (multiple of 8); writing it starts the channel |

The transfer completion interrupt (GIC SPI 89) is delivered to userspace by `/dev/dmadata_irq` (`14`). The
destination buffer is mapped by userspace from `/dev/udmabuf0` (`mmap` 0x70_0000 bytes, RW, shared). The maximum
size of one transfer is 7 000 000 bytes. The exact transfer sequence is in `03` §4.

## 6. Sample rate, decimation and filter registers (ACQ plane)

Values and rules are in `04` §5–§6.

| Idx | Bits | Meaning |
|---|---|---|
| 0x022 | 14:12 | ADC speed code: 3 = 2 GSa/s, 2 = 1 GSa/s, 1 = other |
| 0x022 | 9:8 | switch select |
| 0x004 | 5:0 | decimation type = decimation-ratio index (0 = no decimation, `04` §5.2) |
| 0x004 | 8 | peak-detect enable |
| 0x05D / 0x05E | 15:0 / 11:0 | down-sample ratio (`500 / rate`, ≥ 1), 28 bits |
| 0x04B / 0x04C | 15:0 / 11:0 | ADC-clock divisor (`2000 / rate`), 28 bits |
| 0x04E | 0 | down-sample enable |
| 0x1FF | 15:0 | bandwidth-limit FIR selector |

## 7. Front-end, ADC and calibration registers (ACQ plane)

Semantics and sequences belong to `06` / `10`; the indices:

| Idx | Dir | Meaning |
|---|---|---|
| 0x141 | W | offset-DAC controller control: 4 at offset-init start, 8 at end (2 ms waits around the zeroing) |
| 0x146, 0x148, 0x149, 0x147 | W | offset DAC code (DAC80501, 16 bit, serialised by the FPGA) for CH1, CH2, CH3, CH4 respectively |
| 0x142 | W | function not known; written 0 during offset init only |
| 0x143, 0x145 | W | function not known; written 0 during offset init only |
| 0x144 | W | external-trigger level DAC code (`05` §2.1 EXT_LEVEL); no EXT input on this product, written 0 during offset init only |
| 0x19B | W (RMW) | bits 3:0 digital ×2 gain enable per ADC lane (used at 500 µV/div) |
| 0x055 | W | GND-coupling substitution enable mask: 0x0001 / 0x0010 / 0x0100 / 0x1000 per channel, 0x1111 all lanes (2 GSa/s) |
| 0x053, 0x056, 0x05A, 0x05F | W | GND-substitute constant code CH1..CH4 = clamp(2048 ∓ 8 × vpos_px, 0, 4095) |
| 0x00F | W | channel invert mask. Bits 3:0 = invert the samples of ADC lane n: at 2 GSa/s (one channel) CH1/CH2 → 0x3, CH3/CH4 → 0xC; otherwise bit ch (0 = CH1) for each enabled inverted channel. Bits 11:8 = display-invert flag of channel ch (set transiently while a held record is redrawn, e.g. 0x100 → 0x103 → 0x003 when CH1 invert is switched on; 0 while acquiring). Write it when invert or the channel set/route changes |
| 0x036 | W | probe BWL filter switch |
| 0x0E7 | W | trigger-type auxiliary code, 9 or 10 per trigger type (`05` §1, TYPE_AUX) |
| 0x020, 0x023 | W | ADC skew calibration enable, ADC skew channel |
| 0x09C, 0x09D | W | AD1 / AD2 skew points (also "trigger skew"; 1 at register init) |
| 0x07A | W | ADC phase enable strobe: 0 then 0x01 (interface 0) / 0 then 0x10 (interface 1). The phase search uses only the interface-0 form (0 then 0x01), which applies the phase of both ADCs |
| 0x07B | W (RMW) | bits 2:0 ADC capture phase 0..7 |
| 0x07C | R | bit 0 / 1: ADC0 / ADC1 data PLL ready |
| 0x07D | W / R | W: error-clear sequence 0, 1, 0, 2; bit 8 phase-calibration enable (0x102 then 0x002 on every ADC-mode change). R: bit 4 ADC0 error, bit 5 ADC1 error |
| 0x0AF | W (RMW) | bit 8 bypass trigger (calibration) |
| 0x0A4 | R | bit 0 calibration value invalid |
| 0x1E5–0x1EB | W / R | board-test helpers: 0x1E5 test start (0, 10 ms, 1); 0x1E7 R bit 0 done (poll §1.3), bit 8 result; 0x1E6 latch ADC test codes (0 then 1, wait 50 ms); 0x1E8…0x1EB ADC test code CH1…CH4 (`15`) |
| 0x1AE–0x1B6 | W / R | PL mean engine (used by self-calibration, `10`): 0x1AF window length N; 0x1AE start (0 then 1); poll 0x1B1 bit 0 (data valid, ≤ N × 1 ms); 0x1B0 channel select; 0x1B1 bits 12…15 overflow CH1…CH4; sum (48 bit) = 0x1B4[15:0] << 32 \| 0x1B3 << 16 \| 0x1B2; count (32 bit) = 0x1B6 << 16 \| 0x1B5; mean code = round(sum / count) & 0xFFF |
| 0x189 / 0x18A | W / R | self-test mode / sbus sense |

The per-lane input delays of the ADC capture are fixed in the bitstream (`12` §6.2); no register moves an
individual lane. Software alignment is only by the capture clock phase (0x07B, applied by 0x07A).

## 8. Trigger, counter and measurement registers (ACQ plane, summary)

The trigger engine registers (indices 0x008–0x017, 0x029–0x02F, 0x030–0x03B, 0x040–0x048, 0x060–0x07F,
0x0A1–0x0BF, 0x0D6–0x0EF, 0x0F0–0x14F partly, serial-trigger blocks) are specified in `05`, with their indices and
access types. Frequently needed:

| Idx | Meaning |
|---|---|
| 0x00A | TRIG_EN: bit 0 AUTO (auto-trigger enable), bit 8 holdoff enable, bit 15 AC-line/EXT comparator path (`05` §2.1, §3.4) |
| 0x00B | TDC reset (3): only for an external-trigger source, which this board does not have — never written |
| 0x033 | bit 0 TDC enable: 1 only for an external-trigger source (none on this board), 0 for channel and AC-line sources; bit 3 pass/fail enable, bit 8 display type |
| 0x00E | bits 3:0 trigger type, bits 6:4 frequency-counter source, bits 10:8 trigger source, bit 12 mode |
| 0x011 | bits 9:8 edge slope, bit 7 pulse polarity, bits 1:0 pulse limit range |
| 0x0E0 / 0x0E1 | trigger level 1 code (12-bit, 2048 = mid-screen) / level-1 hysteresis |
| 0x0E2 / 0x0E3 | trigger level 2 code / level-2 hysteresis |
| 0x0D6–0x0DD, 0x0F0–0x0F3, 0x100–0x103 | per-channel comparators, level 1 and level 2, code / hysteresis (`05` §2.1; CH1 level 1 = 0x0D7 code / 0x0D6 hysteresis). The totalizer and pattern-trigger setups write the same per-channel comparators |
| 0x0DA | also written (0x3E) by the frequency-counter gate-level setup (it is the CH2 level-1 hysteresis of the comparator block, `05` §2.1; role for the counter not yet known) |
| 0x0E5/0x0E6, 0x0EB/0x0EC, 0x10A/0x10B, 0x10C/0x10D | serial-decoder slicer hysteresis / code CH1…CH4 (`05` §2.1 SER_LEVEL) |
| 0x080 | QUAL_CFG, W (RMW): trigger qualifier configuration (`05` §2.1 QUAL_CFG: bits 4:0 source, 5 polarity, 6 event select, 7 edge polarity, 9:8 limit, 14:12 mode); values in use 0…3, 0x20, 0x21, 0x1001. Not part of the ADC interface: the ADC bring-up neither reads nor writes 0x080 |
| 0x0B0, 0x0B2, 0x0B4 | waveform-search source / hysteresis / level (0x0B4 = search level code); search comparator 1: 0x045 hysteresis, 0x0CC level code; comparators 2/3 (pattern-family trigger types only): 0x108/0x104, 0x109/0x106. They are loaded with copies of the trigger levels (`05` §3.3) |
| 0x029 | pass/fail trigger-out (1 / 0) |
| 0x00E bits 6:4 | frequency-counter source (RMW) |
| 0x063 / 0x064 | low-frequency gate: 0x063 = period count N = 1, 5, 10, 100, 1000 or 10000 from the expected frequency (`05` §9, `15` §4); 0x064 = 0 |
| 0x066 | auto frequency measurement restart: 0 then 1 |
| 0x067 | auto-measurement gate count |
| 0x068 / 0x069 | auto frequency count low / high (R) |
| 0x06A / 0x06B | low-frequency count low / high (R) |
| 0x06E | R: gate status: bit 0 = auto gate (0x067) done, bit 4 = period gate (0x063) done — chosen by the measurement mode, not by the source (poll §1.3, `15` §4) |
| 0x111 | totalizer control: 1:0 count source, 2 edge, 3 gate type, 5:4 gate source, 6 gate pulse, 7 gate, 8 lock, 12 clear (each a pulse: write the word with the bit set, then clear — e.g. 0x0104 → 0x0004 and 0x1004 → 0x0004), 13 shield |
| 0x11B–0x11E | totalizer count, 56 bit = (0x11E & 0xFF) << 48 \| 0x11D << 32 \| 0x11C << 16 \| 0x11B (R) |

## 9. MAP plane (display / panel / LCD)

Display semantics: `07`; keys, encoders and LEDs: `08`. Registers also used by the boot scripts and kernel
modules, and the ones a minimal firmware needs first:

| Idx | Byte addr | Dir | Meaning |
|---|---|---|---|
| 0x004 / 0x005 | `0x8004_0010` / `0x014` | W / R | map PLL reset bits 0/4/8; locked bits 0/4/8. **Not used on this board**: a firmware must not write 0x004 and need not read 0x005; the PL's map clocks run from configuration on |
| 0x082 | `0x8004_0208` | R | last-frame flag: 1 = a complete frame is held and the per-frame cycle may run (`03` §3.6) |
| 0x015 | `0x8004_0054` | W | LCD reset: 1 then 0 |
| 0x016 | `0x8004_0058` | W | LCD enable / backlight PWM enable (1 = on) |
| 0x14D | `0x8004_0534` | W | backlight brightness = 18 × b + 199 for b = 0..100 (boot value 0x700) |
| 0x158 | `0x8004_0560` | W | PL → PS video stream for VNC capture: 0 then 1 |
| 0x1B0 | `0x8004_06C0` | W | 1 after the VNC capture VDMA is configured |
| 0x160–0x163 | `0x8004_0580`–`058C` | R | key matrix, 4 × 16 bits, active low (64 keys) |
| 0x169 | `0x8004_05A4` | R | encoder step count (low byte) |
| 0x16E | `0x8004_05B8` | R | key-interrupt acknowledge (read clears; GIC SPI 90) |
| 0x16A / 0x16F | `0x8004_05A8` / `05BC` | W | LED shift data bits 15:0 / 31:16 |
| 0x16C | `0x8004_05B0` | W | LED latch: 0 then 1 after each data write |
| 0x16D | `0x8004_05B4` | W | knob period: 75 (0x4B) |
| 0x16B | `0x8004_05AC` | — | button long-push time / R bit 12 long press; for boards with FPGA long-press detection only — **not used** on this board |
| 0x0FF | `0x8004_03FC` | W | bit 0 waveform blank: 1 = waveform layer suppressed, 0 = shown (1 at start-up, 0 once the UI is ready) |
| 0x190 | `0x8004_0640` | W (RMW, 16-bit masks) | map config: bit 0 enable, bits 7:4 set number, bit 6 zoom valid, bit 12 valid, bit 15 gain/offset valid pulse (`07` §7.3) |
| 0x103 | `0x8004_040C` | W | map software reset |
| 0x0AF | `0x8004_02BC` | W | search-clear strobe: 0 then 1, part of the clear block (`07` §7.2); PL effect not yet confirmed |
| 0x170 | `0x8004_05C0` | W | pass/fail-mask load (bits 8 and 0): 0x101 then 0x001 in the clear block (`07` §7.2); PL effect not yet confirmed |
| 0x189, 0x1A9, 0x13A, 0x13E (zoom 0x1AA, 0x14C, 0x13F) | | W | DPO valid and DPO parameters x / ud / a (`07` §7.9) |
| 0x060, 0x061, 0x110, 0x120, 0x153, 0x154, 0x19A–0x1AF, 0x1B5–0x1B7, 0x1BD, 0x1BE | | W | map timebase set (`07` §7.8) |
| 0x110–0x115, 0x120–0x125, 0x140–0x14F, 0x187–0x1AF, 0x1B1–0x1FF | | | rendering, persistence, peak readback, wave move/resize, XY, roll stop mode, preview, labels (`07`) |

The ACQ register 0x0FD bit 7 is the "map busy" flag that the map engine exposes in the ACQ plane (poll until 0
before re-arming).

## 10. AXI VDMA blocks and siglent_dma

Both VDMAs are standard Xilinx AXI VDMA (PG020) register files; programming values and frame geometry are in `07`
and `13`:

| Block | Channel | Registers written (byte offsets) | Values |
|---|---|---|---|
| UI VDMA `0x8000_0000` | MM2S (UI → LCD) | CR 0x00, VSIZE 0x50, HSIZE 0x54, STRIDE 0x58, START 0x5C | CR 4 (reset) then 0x1008B; 1024 × 600 × 4 B, stride 0x1000; start `0x1F40_0000`, later `0x1FC0_0000` |
| UI VDMA | S2MM (VNC capture) | CR 0x30, VSIZE 0xA0, HSIZE 0xA4, STRIDE 0xA8, START 0xAC | 1024 × 600 × 2 B to `0x1F80_0000` |
| waveform VDMA `0x8002_0000` | S2MM + MM2S (waveform layer) | CR 0x30/0x00, sizes 0xA0–0xAC / 0x50–0x5C | CR 4 then 0x18003 (S2MM) / 0x1008B (MM2S); 2000-byte lines × 480, buffer `0x1E40_0000` |

`siglent_dma` (IRQ GIC SPI 94) is a kernel driver whose interrupt handler only reads ACQ 0x160 (`0x8003_0580`,
read-to-clear). Its interrupt is the PL's **frame-ready signal for a pending read-data request** (MAP 0x148 ← 0
starts a request, ← 1 stops it, `03` §3.6): wait for it with `select()` on `/dev/siglent_dma` (readable, no
timeout), then clear it with `ioctl(fd, 1, 0)` and read the frame (`03` §4.3). Its 4 MiB buffer
(the reserved `wavedata` region at `0x1E40_0000`) is not used for sample data: the record itself is moved by the
AXI DMA of §5.2. See `14`.

## 11. Bring-up order summary

Register-level summary of the start-up order; the authoritative, complete list is `03` §2.

1. hw-id byte from GPIOs 334…341 (§3); map ACQ and MAP planes; read 0x000–0x002.
2. 0x1FB (ADC type); offset-DAC init (0x141 = 4, 0x146/0x148/0x149/0x147 and 0x144/0x143/0x142/0x145 = 0,
   0x141 = 8); read 0x002; stop-mode RMW on 0x019; 0x1EF = 1.
3. VGA and ADC chip init over SPI (`06`); ISERDES reset (0x04F = 1, 0); GPIO 343.
4. ADC PLL sync (0x1FA bit 8 pulse) and phase training (0x04F, 0x01F, 0x07A–0x07D; `06` §9.4).
5. Register init: xPLL lock (0x01F bits 12/13), record-memory reset (0x003 bit 0 pulse), history pause
   (0x0F8 = 0x100), 0x03C = 1, 0x0EA pulse, 0x128 = 0, trigger skew (0x09C/0x09D = 1).
6. Channel enable (0x0B1), gains/offsets, trigger level and source, ADC mode (0x07D 0x102 → 0x002), then the
   timebase setup in configuration banks for all C channels (§4.3), 0x1EE (§4.2), the map timebase sets (`07` §7.8)
   and the FIR selector (0x1FF); trigger configuration; soft reset.
7. Arm (`03` §3.2).

## Open

- Hardware reset values of the planes are unknown; the firmware does not rely on them except where it polls. A real
  unit dump of both planes after bitstream load would settle it.
- The meaning and units of many timebase-setup registers (§4.2) are known only by role and value.
- 0x019 is written by read-modify-write over bits 7:3; whether writing back status bits 0/9 has an effect is
  unknown. The firmware starts with a zero shadow and writes 0x0008 for the first stop request (only bit 3 is a
  control bit in use).
- Bits 31:16 of every register are assumed to be ignored; not verified.
- MAP byte offsets 0xAC and 0xD8 (index 0x036) play no part in the VNC capture; MAP 0x036 carries serial-trigger
  data.
- What ACQ 0x142, 0x143/0x145, 0x1EF and 0x128 do in the PL is unknown;
  the firmware writes them only at init (0x1EF = 1, the others 0).
- Which registers are latched per configuration bank (§4.3) and which are global is unknown; both write rules of
  §4.3 avoid depending on it.
- The record memory is in PS DDR4 (the PL has no DDR interface, `12` §1, §4); its address range and size are
  **not yet confirmed** — most likely DRAM above 0x2000_0000 that the factory device tree keeps out of Linux (`01`
  §1). A replacement kernel must not extend the memory node beyond 0x1F7F_FFFF until this is settled on a unit (`01`
  §13, `12` Open). 0x021 bit 0 behaves like a memory-ready bit.
- The role of 0x0DA for the frequency counter (gate level written into a trigger comparator register) is not known.
