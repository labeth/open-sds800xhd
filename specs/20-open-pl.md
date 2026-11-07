# 20 — Open PL Design

Our own programmable-logic design for the SDS800X HD, replacing the original bitstream ("stage 2" of the port). It
keeps the interfaces of specs 02–08 and 12–14 and adds what the SDS1000CML+ gets from its own FPGA images:
hardware protocol and symbol triggers, the qualifier and zone triggers, gap-free decoded streams and stacking for
super-resolution. Sources are in `fpga/` (§13); the register ABI is `fpga/regs/open-pl.yml` (§10).

This spec is written from specs 00–16 and from the open RTL of the SDS1000CML+ and open-dso-core. A statement that
depends on a board fact the specs mark **UNVERIFIED** says so; the list is §14.

---

## 1. Goals and constraints

| # | Requirement | Source |
|---|---|---|
| G1 | Target XCZU2CG-2SFVC784, PL IDCODE `0x04A43093`, a full unencrypted uncompressed `.bin` loadable by `fpga_manager` with flags 0 | `12` §1, §2; `13` §3 |
| G2 | Stay within the XCZU2 column of `12` §1 (47 232 LUTs, 94 464 FFs, 150 RAMB36, 240 DSP) | `12` §1 |
| G3 | The unit's boot chain and kernel stay: the FSBL's PS configuration (clocks, HP widths, MIO) is used as is | `01` §12, `12` §3–§5 |
| G4 | The unit's kernel modules keep working against the open image: their interrupts and acknowledge addresses (0x8003_0580 read, 0x8001_0034 write, 0x8004_05B8 read) | `14` |
| G5 | The board code (`app/internal/scopeboard`) keeps the HAL contract; it changes only where registers change (§12) | task |
| G6 | Every trigger is decided in the PL; software only places the trigger point (the CML rule) | CML |
| G7 | Every trigger fires at every timebase: triggers read the undecimated samples whatever the record's decimation | CML ADR-TRIGGER-ANY-TIMEBASE |
| G8 | A lost word is a sticky, counted fault, never a silent gap | CML ADR-STREAM-DRUM |
| G9 | Simulation first: every block that does not depend on an UNVERIFIED board fact has a self-checking iverilog testbench in `make fpga-sim` | §13 |
| G10 | Device primitives only behind thin wrappers with behavioural models; synchronizers are primitives | §11 |

## 2. Block diagram

```
                       ┌──────────────────────────── PL ─────────────────────────────────────────────┐
 ADC0 DI/DQ 24 LVDS ──►│ capture0 (IDELAYE3+ISERDESE3 ×24, MMCM0) ──► cclk 250 MHz ┐                  │
 ADC1 DI/DQ 24 LVDS ──►│ capture1 (…, MMCM1) ──► async FIFO (freq-locked) ─────────┤                  │
                       │                                                           ▼                  │
                       │                     route & align ─► 4 channel streams, 8 lanes/clk          │
                       │                       │  invert, GND substitute                              │
                       │      ┌────────────────┼──────────────────────┬───────────────┐               │
                       │      ▼                ▼                      ▼               ▼               │
                       │  trigger engine   decimator/reducer      line slicers    mean engine        │
                       │  (comparators,    (pick, box, peak)      ─► protocol &                       │
                       │   interval, nth,      │                    symbol engines ─► event queue      │
                       │   video, zone) ──fire─┤                         │ match                      │
                       │      │                ▼                         ▼                            │
                       │      └──────► acquisition FSM ─► record packer ─► record writer ──HP0 128─────┼─► DDR record rings
                       │                                   stream framer ─► stream writer ──HP1 64 W───┼─► DDR stream ring
                       │  record reader ◄──HP0── readout engine (unpack, min/max, nth) ─► AXI DMA S2MM ┼─HP2─► udmabuf0
                       │                          stacking accumulator (BRAM)                          │
                       │  HPM0_LPD ─► interconnect ─► UI VDMA, AXI DMA, compat ACQ/MAP planes, open plane │
                       │  UI VDMA MM2S ──HP3 32── ► LCD timing ─► RGB666 (bank 44)                     │
                       │  panel / LED / offset-DAC serializers (bank 25/26, UNVERIFIED pins)           │
                       └────────────────────────────────────────────────────────────────────────────────┘
```

The PL holds no waveform renderer (§9): the PS draws traces into the UI framebuffer, which the PL scans out.

## 3. Clocks and resets

| Clock | Source | Frequency | Domain |
|---|---|---|---|
| `aclk` | pl_clk0 (`12` §3.2) | 100 MHz | M_AXI_HPM0_LPD, AXI-Lite slaves, register files, panel, LED, offset DAC, LCD control |
| `mclk` | pl_clk1 | 300 MHz | S_AXI_HP0–3, record writer/reader, stream writer, AXI DMA and UI VDMA memory side |
| `sclk0`, `fclk0` | ADC0 DCLK through MMCM0 | 250 / 500 MHz | ADC0 ISERDES CLKDIV / CLK |
| `sclk1`, `fclk1` | ADC1 DCLK through MMCM1 | 250 / 500 MHz | ADC1 ISERDES |
| `cclk` | = `sclk0` (BUFG) | 250 MHz | the sample core: route, triggers, protocol engines, acquisition FSM, packers, framer, stacker |
| `pclk` | MMCM on pl_clk0 | panel pixel clock, **UNVERIFIED** (`07` §1) | LCD timing and output registers |

- ADC0 exists on every model (`03` §2 step 8), so `cclk` is always ADC0's divided clock. ADC1's samples cross into
  `cclk` through a 16-word asynchronous FIFO. Both DCLKs come from the one clock chip (`13` §4), so the FIFO's fill
  stays constant; it is read from a fixed fill of 8 after both MMCMs lock, and a fill outside 4…12 sets the
  sticky fault `adc1_slip` (§10 SYS_FAULT).
- DCLK balls and MMCM settings are **UNVERIFIED** (`12` §6.2): AD5/AD4 (group A) and L7/L6
  (group B). The image measures each sample clock against `aclk` (SYS_CCLK_KHZ, SYS_ADC1_KHZ) so bring-up
  sees whether a clock runs before any capture is trusted.
- **Resets.** `pl_resetn0` (EMIO GPIO 95, active low) resets everything, synchronised into each domain by a
  reset synchronizer primitive (§11). Whether it is pulsed after an `fpga_manager` reload is **UNVERIFIED**
  (`12` §3.3): every register therefore also has a configuration-time initial value (GSR), equal to its reset value.
  The `cclk` domain is additionally held in reset while MMCM0 is unlocked, while compat ACQ 0x04F = 1 (ISERDES
  reset, `12` §6.4) and while SYS_CTRL bit 0 = 1 (`12` §2.2).

### 3.1 Clock-domain crossings

All crossings use the synchronizer primitives of §11; no other logic crosses a domain.

| From → to | What | How |
|---|---|---|
| `aclk` → `cclk` | configuration words (trigger, acquisition, protocol, stream, stacker) | **latched while idle**: the register file holds the words; a COMMIT strobe (ACQ_CTRL bit 4, also implied by ARM) crosses by a toggle synchronizer and `cclk` copies all words in the following clock. Software changes configuration only while the acquisition is stopped (`03` §3.3 already requires it) |
| `aclk` → `cclk` | strobes: arm, stop, force, clear, stack start | pulse synchronizer (toggle + 3 FF + edge) |
| `cclk` → `aclk` | status words, trigger timestamp, counters | handshake bus synchronizer: every register read requests a snapshot of the `cclk` and `mclk` status; the AXI-Lite read waits for both (at most 48 `aclk` cycles, then it answers with the last snapshot), so a status read after a strobe sees the strobe's effect |
| `cclk` → `aclk` | sticky faults, interrupt causes | toggle per event, accumulated in `aclk` |
| `cclk` → `mclk` | record groups, stream items | asynchronous FIFOs (gray pointers through the 2-FF primitive) |
| `mclk` → `aclk` | write pointers, AXI errors | handshake bus synchronizer |
| `sclk1` → `cclk` | ADC1 samples | 16-word asynchronous FIFO |

## 4. PS–PL interfaces

### 4.1 AXI ports

The FSBL fixes the HP data widths (`12` §4); the image uses each port at that width:

| Port | Width | Clock | Master in the image | Use |
|---|---|---|---|---|
| M_AXI_HPM0_LPD | 32 | `aclk` | — (PS master) | all registers, §4.2 |
| S_AXI_HP0_FPD | 128, R/W | `mclk` | record writer + record reader (arbitrated) | the record rings (§6) |
| S_AXI_HP1_FPD | 64, write only | `mclk` | stream writer | the stream ring (§7) |
| S_AXI_HP2_FPD | 64, R/W | `mclk` | AXI DMA (S2MM) | readout into udmabuf0 (`03` §4.4) |
| S_AXI_HP3_FPD | 32, R/W | `mclk` | UI VDMA (MM2S) | UI framebuffer scan-out (`07` §4.1) |

Bandwidth: the record writer needs at most 2 GSa/s × 12 bit = 3.0 GB/s (the aggregate rate is ≤ 2 GSa/s on every
route, `04` §1); HP0 at 128 bit × 300 MHz offers 4.8 GB/s. The DDR4 (32 bit, 1200 MT/s, `12` §1) peaks at
4.8 GB/s; whether it sustains 3.0 GB/s of PL writes next to the PS and the 0.15 GB/s scan-out is **UNVERIFIED**
(§14 U16). The writer's FIFO absorbs refresh and arbitration stalls; an overflow is a counted fault (G8), never a
gap. Records at aggregate rates ≤ 1 GSa/s need ≤ 1.5 GB/s.

### 4.2 Register address map (HPM0_LPD)

| Base | Size | Block | Compatibility |
|---|---|---|---|
| `0x8000_0000` | 64 KiB | UI VDMA (Xilinx AXI VDMA, PG020): MM2S only | `07` §4.1 unchanged; S2MM (capture) is not built (§9) |
| `0x8001_0000` | 64 KiB | AXI DMA (Xilinx, PG021, direct register mode, S2MM only) | `02` §5.2, `03` §4.4 unchanged |
| `0x8002_0000` | 64 KiB | — (the original image's waveform VDMA) | answers OKAY, reads 0 (§4.3) |
| `0x8003_0000` | 64 KiB | **compat ACQ plane** (§10.4) | the subset of `02` that the front end, ADC bring-up and kernel modules use |
| `0x8004_0000` | 64 KiB | **compat MAP plane** (§10.4) | LCD, backlight, keys, encoder, LEDs, key IRQ |
| `0x8010_0000` | 64 KiB | **open plane**: the open register ABI (§10) | new |
| `0x8011_0000` | 64 KiB | stacker bin memory window (read only, §8) | new |

### 4.3 Bus rules

- **Every address in `0x8000_0000`–`0x8011_FFFF` answers OKAY.** Unmapped words read 0 and ignore writes. A DECERR
  or SLVERR on HPM0_LPD becomes a synchronous external abort in the PS; drivers bound to the original image's devices (e.g.
  `xilinx_dma` probing the waveform VDMA node) must never take one from the open image. A register access never
  stalls longer than 64 `aclk` cycles (no wait on a stopped sample clock: `cclk`-side state is read from snapshots).
- Registers are 32-bit words; the open plane uses all 32 bits. The compat planes keep the 16-bit word rule of `02`
  §1.1 (bits 31:16 read 0, ignored on write).
- Write rule (`02` §1.2) holds unchanged: writes are unconditional; strobes are separate write-only bits that read 0.

### 4.4 Interrupts

| PL line | GIC SPI | Driven by | Acknowledge |
|---|---|---|---|
| pl_ps_irq0[0] | 89 | AXI DMA `s2mm_introut` | AXI DMA DMASR write (`14` §3), unchanged |
| pl_ps_irq0[1] | 90 | key change (compat MAP) | read MAP 0x16E (`14` §5), unchanged |
| pl_ps_irq0[2] | 91 | UI VDMA `mm2s_introut` | Xilinx driver |
| pl_ps_irq0[3], [4], [7] | 92, 93, 96 | 0 (no waveform VDMA, no capture S2MM) | — |
| pl_ps_irq0[5] | 94 | **open-plane interrupt**: OR of the enabled SYS_IRQ causes (acquisition done, stream block, stack done, readout done) | one 32-bit read of `0x8003_0580` (compat ACQ 0x160) moves every pending cause into SYS_IRQ_TAKEN and drops the line, so the `siglent_dma` module's handler (`14` §2) acknowledges it; the app then reads SYS_IRQ_TAKEN (read clears) to see which causes fired |
| pl_ps_irq0[6] | 95 | 0 | — |
| pl_ps_irq1[0] | 104 | 0 (no capture frame) | — |
| pl_ps_irq1[1..7] | 105–111 | 0 | — |

At load time no cause is pending (all IRQ enables reset to 0), so the `siglent_dma` load-order trap of `14` §2 cannot
occur with the open image.

### 4.5 EMIO and fixed I/O

As `12` §4.2 and §8: EMIO GPIO 0/1 → A6/A7 (ADC chip selects) and the SDO mux on J11 (`J11 = !cs0 ? A13 : !cs1 ? B13 :
0`), GEM0 MII + MDIO → bank 24, TTC0 wave-out 0 → AF11, EMIO GPIO 95 → `pl_resetn0`. DCLK_RST outputs B4/A4 and
A9/A8 held low (**UNVERIFIED** function, `12` §6.3). Every other ball not named in §14's pin list stays unused
(high impedance).

## 5. Sample path

### 5.1 Capture

Per ADC: 24 LVDS lanes (DI[11:0], DQ[11:0]) through IDELAYE3 (fixed delay, group A 24 taps, group B 0, `12` §6.2)
and ISERDESE3 1:4 DDR at DCLK 500 MHz, CLKDIV 250 MHz: 4 I samples and 4 Q samples per `sclk` cycle. In DES mode
(2 GSa/s, one channel) I and Q interleave into 8 consecutive samples. The lane-to-bit map, which lane group belongs
to which ADC, the DCLK balls, and the I/Q order in DES are **UNVERIFIED** (`12` §6.2, §14 U3–U5): the capture
block takes them from a generated lane-map include, and the bring-up procedure of §15 fills it in from ADC test
patterns. The capture controls the software expects (`12` §6.4: 0x01F, 0x04F, 0x07A–0x07D, 0x1FA, 0x1FB) are
implemented in the compat ACQ plane with the semantics of `12` §6.4: MMCM reset and lock, ISERDES reset, the
3-bit phase step applied by the 0x07A strobe through the MMCM dynamic phase shift, and per-ADC capture-error flags
from the ADC test-pattern checker.

### 5.2 Route and channel streams

The aggregate sample rate is at most 2 GSa/s on every route (`04` §1): 8 samples per `cclk` cycle in total. The
route stage turns the two ADCs' I/Q words into **channel streams**: per channel `c` an 8-lane vector of 12-bit
codes with `n_c` valid lanes (lanes `0 … n_c − 1`, oldest first):

| Route (`04` §1) | Channels | Lanes per channel per clock | Sample period | Tick per lane |
|---|---|---|---|---|
| 0 | 1 | 8 | 0.5 ns | 1 |
| 1 | 2 | 4 | 1 ns | 2 |
| 2 | 3–4 | 2 | 2 ns | 4 |

Channel-to-ADC-core mapping: CH1/CH2 on ADC0 and CH3/CH4 on ADC1, consistent with the invert lane masks of `02` §7
(0x00F: CH1/CH2 at 2 GSa/s → lanes 0x3); the I/Q core of each channel and the DES input selection are
**UNVERIFIED** (§14 U14). At route 2 the ADCs run 1 GSa/s per core and the route stage keeps every second sample.
Lanes of different channels with the same index are simultaneous samples (a per-ADC skew register, ACQ_SKEW,
delays one ADC's streams by 0…63 samples to align them).

Per-channel processing before anything else: **invert** (code → 4095 − code; ACQ_INVERT), **GND substitute**
(the channel's samples replaced by a constant code, ACQ_GNDSUB; `06` §8.3). Every consumer (trigger, record,
stream, protocol, mean engine) sees the same processed samples.

**Time base.** One **tick** is 0.5 ns, the sample period at 2 GSa/s. A sample's time is
`t = 8 · clk + lane · tick_per_lane` ticks, `clk` being a free-running 45-bit `cclk` counter cleared by COMMIT. All
trigger times, holdoffs, timestamps and stream marks are in ticks (48-bit).

### 5.3 Decimation and reduction

A per-channel decimator turns the channel stream into **record words** at the plan's rate `R = R_route / D`
(D = ACQ_DECIM, any integer 1 … 2^28 − 1, not limited to the list of `04` §5.2). Modes (ACQ_MODE):

| Mode | Word | Notes |
|---|---|---|
| pick | the first sample of each group of D | default; exact sample values |
| box | mean of D samples, 16-bit Q12.4 (`word = Σ / D · 16`, D a power of two; other D: pick) | adds up to 4 bits below the LSB in the word's low nibble, which `03` §5 leaves for "extra resolution" |
| peak | two words per group: maximum then minimum | the board's `sample.PeakPairs` content; the pair order is fixed here |

The record word is the 12-bit code (or Q12.4 box mean); the PS sees it as the 16-bit word of `03` §5 (`code << 4`,
or the Q12.4 value) after the readout engine unpacks it (§6.4), so the core's `CodeFormat` (Bits 12, Shift 4,
Mid 0x8000) is unchanged.

## 6. Acquisition and record memory

### 6.1 Record memory

Records live in **PS DDR4**, written through HP0 (`12` §1: the PL has no memory interface). The open image takes
the region from registers (REC_BASE, REC_GROUPS); it does not hard-code an address. The PS must keep that region
out of Linux: the unit's DT gives Linux `0x0000_0000`–`0x1F7F_FFFF` only and the DDR is 1 GiB per the FSBL
(**UNVERIFIED**, `01` §1, §14 U2), so the recommended region is `0x2000_0000`–`0x3FFF_FFFF` (512 MiB): record rings
from `0x2000_0000`, the stream ring at `0x3C00_0000` (64 MiB). The board refuses a depth that does not fit.

**Packed format "P12".** A *group* is 32 consecutive record words of one channel in 48 bytes (three 128-bit beats):
word `k` of the group occupies bits `[12k + 11 : 12k]` of the 384-bit little-endian group. Box-mean words keep their
upper 12 bits (Q12.4 → the stored code is `word >> 4`; the extra nibble is delivered only by the stream path). At 2 GSa/s
this is 3.0 GB/s instead of 4.0 GB/s for 16-bit words.

Layout: channel `c`'s ring for segment `s` starts at `REC_BASE + (s · N_ch + c) · REC_GROUPS · 48`, `N_ch` = channels
recorded (ACQ_ROUTE mask), and holds `REC_GROUPS · 32` words. A ring is written circularly while pre-trigger,
linearly after the trigger.

### 6.2 Acquisition state machine

| State | Entered by | Left by |
|---|---|---|
| IDLE | reset, STOP, CLEAR, last segment done | ARM |
| PRE | ARM (and each next segment) | ACQ_PRE words written → ARMED |
| ARMED | pre-trigger filled | trigger fire (§7.6) → POST; STOP → IDLE |
| POST | trigger | ACQ_POST words after the trigger word → DONE (or PRE of the next segment) |
| DONE | record complete | ARM, CLEAR |

- The trigger is accepted only in ARMED (the pre-trigger is always full; a FORCE in PRE is held until ARMED).
- On the trigger the FSM records, for the segment: the trigger time (ticks), the record word that holds it
  (TRIG_POS, ring-relative), the sub-sample fraction (§7.5) and whether it was an auto or forced trigger.
- The record word holding the trigger is the word whose sample group (D samples) contains the trigger sample.
  The trigger's offset inside that group, in ticks, is reported (TRIG_PHASE) so the PS places it exactly at any
  decimation (G7).
- STOP in PRE/ARMED abandons the record (IDLE, no DONE); STOP in POST finishes the record first (the stop is
  noted and the FSM goes IDLE after DONE). DONE raises the `acq_done` interrupt cause.
- Segments (ACQ_SEGMENTS > 1): after each segment's POST the FSM goes to PRE of the next segment's ring without
  the PS; DONE after the last. Per-segment trigger time/position/flags are kept in a 1024-entry BRAM table read
  through SEG_SEL (§10). This serves the HAL `Segments` capability (sequence mode, `03` §6.2) with hardware
  timestamps in ticks.
- The FSM never re-arms itself after DONE: Auto and Normal re-arm from the PS (`03` §3.6 replacement loop); Auto's
  free-run comes from the trigger's auto timeout (§7.6).

### 6.3 Record writer

Per channel a P12 packer (gearbox from the decimator's 0…8 words per clock to 384-bit groups) feeds a 512-group
asynchronous FIFO (`cclk` → `mclk`, 4 BRAM36 per channel). The writer drains the four FIFOs round-robin in
16-beat bursts (256 B, i.e. 5⅓ groups: bursts are issued on beat boundaries of a channel's linear address) on HP0
with up to 8 outstanding bursts. A FIFO above its high-water mark when a group must be pushed loses that group:
REC_LOST counts groups, SYS_FAULT bit `rec_overflow` is set, and the record's status carries `overflow`.

### 6.4 Readout engine

The PS reads records through the readout engine, which keeps the AXI DMA transfer of `03` §4.4 unchanged (the AXI
DMA, `/dev/dmadata_irq`, udmabuf0 and the cache rule stay as they are); only the window registers differ (they
replace ACQ 0x0C1–0x0D5):

- RD_SRC (channel, segment), RD_START (first ring word), RD_COUNT (words), RD_MODE, then RD_CTRL.start.
- The engine reads P12 groups through HP0 (read side, arbitrated with the writer, writer first), unpacks each word
  to the 16-bit word of `03` §5, and streams 64-bit beats (4 words, oldest in bits 15:0) to the AXI DMA S2MM.
- Modes: `words` (every word), `nth` (every k-th word), `minmax` (per bucket of k words: maximum, minimum — 1000
  buckets give the 1000-column envelope a software renderer draws, §9), `sum` (per bucket the sum, 32-bit, for
  the box mean of a long record).
- Ring wrap is handled by the engine; the PS gives ring-relative positions (the trigger word TRIG_POS and the plan's
  pre-trigger give every window).
- While the stacker runs (§8.2) the engine's words go to the stacker instead of the AXI DMA, between a start marker
  carrying the window's segment and an end marker; a sum or minmax result is never stacked.
- The PS may also map the record region directly through `/dev/mem` (it is outside Linux) for debugging; the
  mapping is uncached and slow, the engine is the normal path.

### 6.5 Mean engine

The self-calibration mean engine of `02` §7 (0x1AE–0x1B6: window N, start, valid, overflow, 48-bit sum, 32-bit
count per channel) is kept in the compat ACQ plane with the same semantics (`10` §5), running on the processed
channel streams.

## 7. Trigger engine

### 7.1 Structure

```
channel streams ─► comparators (2 per channel, Schmitt, per lane) ─► event vectors (per lane)
                                                                   ├─► interval engine ─┐
                                                                   ├─► Nth-edge engine ─┤
line slicers ─► protocol / symbol engines ─► match ────────────────┤                    ├─► holdoff ─► auto/force ─► FSM
                                                                   ├─► video engine ────┤
records' words ─► zone engine ─────────────────────────────────────┴─► zone qualifier ──┘
```

Every block works on the 8 lanes of a clock at once, so a trigger is placed at the sample (G6), and several events
in one clock (a 1 ns pulse at 2 GSa/s) are judged in order.

### 7.2 Comparators

Per channel two comparators A and B, each a Schmitt trigger per lane with explicit thresholds `hi > lo` (12-bit
codes): the state goes 1 at the first sample ≥ `hi` and 0 at the first sample ≤ `lo`, and otherwise keeps its value
from the previous lane (the chain runs through the clock's lanes and carries to the next clock). The events are the
state changes: **rise** at the lane where the state goes 0 → 1, **fall** where it goes 1 → 0.

The board computes the thresholds from the level `L` and hysteresis `h` of `05` §3.3 (codes): a rising-edge
comparator gets `hi = L`, `lo = L − h` (it arms below `L − h` and fires at `L`, the `05` §3.3 meaning of the stored
code); a falling one `lo = L`, `hi = L + h`; an either-edge or state comparator `hi = L + h/2`, `lo = L − h/2`.

The comparators run on every valid lane: 8 at 2 GSa/s, 4 at 1 GSa/s, 2 at 500 MSa/s (route), never on decimated
words (G7).

### 7.3 Event and state vectors

Each clock produces 32 **event vectors** of 8 lanes (bit `e` of the event masks below) and 9 **state vectors**:

| Event bit | Event | State bit | State |
|---|---|---|---|
| `4c + 0` | channel `c` comparator A rise | `c` | channel `c` comparator A |
| `4c + 1` | channel `c` comparator A fall | `4 + c` | channel `c` comparator B |
| `4c + 2` | channel `c` comparator B rise | 8 | pattern |
| `4c + 3` | channel `c` comparator B fall | | |
| 16 / 17 | pattern becomes true / false | | |
| 18 | protocol/symbol match (at lane 7 of its clock) | | |
| 19–31 | reserved, 0 | | |

**Pattern** (PATTERN_CFG): per channel "care" and "required level" of comparator A's state; logic AND or OR over the
cared channels, optionally inverted (NAND, NOR). The encoding of `05` §5.8 maps directly.

### 7.4 Interval engine

One engine covers every time-qualified type. Configuration: three event masks — OPEN, CLOSE, ABORT (an event
vector is the OR of the selected bits) — a qualifier (QUAL_MASK selects state bits, QUAL_REQ their required values;
a close counts only where every selected state has its required value at that lane), a condition and two times
`T_A ≤ T_B` in ticks (48 bits).

Per lane, in order: an **abort** clears the open interval; a **close** with an open interval judges its width
`w = t_close − t_open`; an **open** (re)starts the interval at `t_open = t_lane`. A lane that both closes and opens
(an interval trigger) closes first, then reopens. With ALWAYS_ARMED a close fires without an open (edge trigger).

| COND | Fires at a close when | 
|---|---|
| any (0) | always |
| less (1) | `w < T_A` |
| greater (2) | `w > T_B` |
| inside (3) | `T_A < w < T_B` |
| outside (4) | `w < T_A` or `w > T_B` |

With JUDGE = reach (for greater): the engine fires at the first lane where an open interval's age reaches `T_B`
without waiting for a close (timeout, dropout, pattern "longer than", state-delay); it fires once per interval.

The HAL nodes and the trigger types of `05` map onto it (the board's table, §12):

| Type (`05` §1) | OPEN | CLOSE | ABORT | QUAL | COND / JUDGE |
|---|---|---|---|---|---|
| Edge rising / falling / either | — | A rise / A fall / both | — | — | any, ALWAYS_ARMED |
| Window (exit / enter) | — | A fall + B rise / A rise + B fall | — | — | any, ALWAYS_ARMED; A = low level, B = high level |
| Pulse positive (negative) | A rise (A fall) | A fall (A rise) | — | — | limit |
| Interval rising (falling, either) | A rise | A rise (and fall for either: OPEN = CLOSE = both) | — | — | limit |
| Slope rising | A rise (low level) | B rise (high level) | A fall | — | limit |
| Slope falling | B fall | A fall | B rise | — | limit |
| Runt positive | A rise (low) | A fall | B rise (high) | — | limit (any = every runt) |
| Runt negative | B fall (high) | B rise | A fall | — | limit |
| Dropout / timeout, edge type | A rise or fall (slope) | — | — | — | greater, reach (`T_B` = timeout) |
| Dropout, state type (stays high) | A rise | — | A fall | — | greater, reach |
| Pattern (no limit) | — | pattern true | — | — | any, ALWAYS_ARMED |
| Pattern with limit | pattern true | pattern false | — | — | limit (greater: reach) |
| Qualified, state | — | trigger edge | — | qualifier state | any, ALWAYS_ARMED |
| Qualified, state-delay | qualifier enters state | trigger edge | qualifier leaves state | — | limit |
| Qualified, edge (-delay) | qualifier edge | trigger edge | — | — | any (limit) |
| Delay (`05` §5.9 mode 7) | pattern true (event A) | B edge | — | — | limit |
| Setup violation | data edge (any) | clock edge | — | — | less (`T_A` = setup time) |
| Hold violation | clock edge | data edge (any) | — | — | less (`T_A` = hold time) |

So "either" works for interval, qualified and Nth edge as well.

### 7.5 Other engines and placement

- **Nth edge**: counts CLOSE events after an idle time `T_B` with no CLOSE event; fires on the N-th (NTH_COUNT,
  1…65535). Lanes are counted in order (a prefix count within the clock).
- **Video**: the sync separator of the CML's qualifier image, on comparator A of the source (negative or positive
  sync): broad pulses (≥ 15 µs) start a field, line syncs (3.5–7 µs) after it are counted, the sync that closes
  line N fires (every line when N = 0). Field/standard selection per `05` §5.6 is mapped by the board to the line
  number and field parity.
- **Protocol and symbol**: §7.7. **Zone**: §7.8.
- **Placement.** A fire carries the lane time `t` (ticks). For edge-type fires on comparator `k` of channel `c`, an
  interpolator computes the crossing's fraction between the two samples around the threshold in 1/16 sample
  (restoring division, 4 clocks, `x0 < L ≤ x1`: `frac = ⌊(L − x0) · 16 / (x1 − x0)⌋`, 0 … 16; 16 is a level equal to
  `x1`, the crossing on the trigger sample itself), reported as TRIG_FRAC (five bits). The fraction is what
  super-resolution stacking (§8) bins on.

### 7.6 Holdoff, auto, force

- **Holdoff** (TRIG_CTRL bit 8, HOLDOFF ticks, 48 bit): after an accepted trigger at time `t0`, fires before
  `t0 + H` are dropped. Only time holdoff (`05` §6).
- **Auto** (TRIG_CTRL bit 9, AUTO timeout in `cclk` cycles, 48 bit): in ARMED, no accepted trigger for the
  timeout → an auto trigger at the current clock (flagged auto). This is the Auto sweep of `05` §7: the PS keeps
  re-arming every frame.
- **Force** (ACQ_CTRL bit 2): one trigger at the next clock in ARMED (in PRE: when ARMED is reached), flagged forced.
- Counters: TRIG_CANDIDATES (fires before holdoff), TRIG_ACCEPTED.

### 7.7 Protocol and symbol triggers

Line slicers (one per bus line, PROTO_SLICE: a Schmitt comparator with its own `hi`/`lo`, the serial slicer
levels of `05` §10.1) reduce a channel to one level per `cclk` (the state after the clock's last lane; 4 ns
resolution, sufficient for buses up to ≈ 25 MBd / 25 MHz clocks). The engines are the SDS1000CML+'s open RTL, made
device-neutral (no memory-style attributes, no device primitives):

| Engine | Source | Trigger type of `05` §10 |
|---|---|---|
| UART: 8N1 receiver, up to four-byte suffix match | CML `general/uart_trigger.v` | UART |
| SPI: two-wire, idle-gap framing, up to four bytes | CML `general/spi_trigger.v` | SPI (with CS: a third line gates framing, planned) |
| I2C: address + direction + up to four data bytes, repeated START | CML `general/i2c_trigger.v` | I2C |
| CAN / CAN FD: destuffing, CRC-15, FD data rate | CML `packet/can_trigger.v` (planned) | CAN |
| Symbol trigger: the 40+ buses of timed pulses (LIN, DMX512, 1-Wire, WS281x, IR, Manchester…) | CML `qualify/symbol_trigger.v` (planned) | LIN, and every bus `05` §10 has no trigger for |
| Sequence trigger over decoded events | CML `trigger/event_sequence.v` (planned) | — |

Each engine also emits decoded events (start, data byte, end, error) into an event queue; the stream framer (§7.9
payload `events`) carries them gap-free to the PS — the HAL's `PayloadEvents`.

### 7.8 Zone trigger

Up to four rectangular zones in (time relative to the candidate trigger, code range), each "must intersect" or
"must not intersect". Candidates are the main trigger's fires; the zone engine keeps, per zone, a running count of
record-word buckets with a sample inside the zone's code range (BRAM, 1024 buckets per zone, the CML's
`qualify/zone_qual.v` scheme rescaled to 12-bit codes and 8 lanes), and decides a fixed time after the candidate
(the zone window's end); the accepted trigger keeps the candidate's time, so placement is exact. RTL: planned.

### 7.9 Counter

The frequency counter / totalizer of `05` §9 and `15` §4 counts a selected event vector (any of §7.3) over a gate in
`cclk` cycles: COUNT (events), PERIOD_TICKS (ticks between the first and last counted event), from which the PS
computes frequency to the tick. Totalizer: free-running count with clear.

## 8. Stream path and stacking

### 8.1 Stream framer

Gap-free streams (roll, SOUND, decoded streams; HAL `Streamer`) bypass the record rings. The framer takes 64-bit
**items** — four 16-bit record words of one channel (payload `codes`), sixteen sliced periods of one line packed as
the CML's bits words (payload `bits`), or one decoded event (payload `events`) — and writes **blocks** to a ring in
DDR through HP1:

| Beat | Content |
|---|---|
| 0 | `[15:0]` magic 0x5354 ("ST"), `[23:16]` payload, `[31:24]` channel mask, `[63:32]` block sequence number |
| 1 | `[47:0]` time of the block's first item in ticks, `[63:48]` flags (bit 0: the previous block was lost) |
| 2 … B + 1 | B items (STREAM_BLOCK_BEATS) |

- The ring has STREAM_BLOCKS blocks at STREAM_BASE. The PL advances STREAM_HEAD (blocks written); the PS writes
  STREAM_TAIL (blocks consumed). A block that would overwrite an unconsumed block is **dropped whole**: its items are
  counted (STREAM_LOST), the next written block carries flag bit 0, and its sequence number jumps by the dropped
  blocks — the HAL's `StreamBlock.First` "a jump is a recorded fault" (G8). Never a silent gap.
- Each block's completion raises the `stream_block` interrupt cause (enabled for low rates; high rates poll HEAD).
- Codes for several channels: blocks alternate channels item-wise in the mask order (channel-major runs per block
  are rebuilt by the board, the HAL layout of `StreamBlock`).
- An `events` item is one decoded protocol event: `[47:0]` its time in ticks (the engine's output clock), `[55:48]`
  the data byte, `[63:56]` the kind (1 start, 2 data, 3 end, 4 error).
- Items leave the sample core at most one a `cclk` cycle (four record words a clock); the board refuses a stream
  above that rate. An item that still finds the crossing full is counted in STREAM_LOST and raises
  `stream_overflow` (a fault, never a mode).

Throughput: HP1 at 64 bit × 300 MHz = 2.4 GB/s; the stream is limited by what the PS consumes, not by the bus.

### 8.2 Stacking accumulator (super-resolution)

The stacker accumulates N triggered records into **phase-binned sums** in BRAM, the FPGA half of the core's
`superres` stack-and-crunch:

- Input: a record window read by the readout engine (§6.4) in its `words` mode into the stacker instead of the AXI
  DMA, with the trigger fraction φ (1/16 sample, §7.5) of the window's segment, from the per-segment table (§6.2;
  0 for an auto or forced trigger; φ = 16 bins as 15), quantised to `P` phases (STACK_PHASES, 1…16). N records are one sequence of N
  segments (one arm), each segment's window read into the stacker in turn.
- Word `i` of the window goes to bin `b = i · P + p`, `p = P − 1 − ⌊φ·P/16⌋`, so records of different trigger phase
  interleave onto a grid `P` times finer than the sample period.
- Per bin: a 32-bit sum of 12-bit codes and a 16-bit count (≤ 65 535 records). 8192 bins (11 RAMB36 for the sums and
  counts at 48 bits a bin): e.g. 512 words × 16 phases, or 2048 × 4.
- Read-modify-write pipeline of 4 clocks (block-RAM output register) with two levels of forwarding (consecutive
  updates of one bin are allowed).
- The PS reads the bins through the window at `0x8011_0000` (bin `b`: sum at `8b`, count at `8b + 4`) and computes
  the mean per bin. STACK_CTRL clears all bins (8192 clocks) and starts N records; the `stack_done` cause fires
  after the N-th. The HAL `Stacker.Stack(n)`.

## 9. Display: the waveform renderer decision

**Recommendation: draw waveforms in software; the open PL has no waveform renderer, no waveform VDMA, no mixer and
no capture path. It scans out the UI framebuffer only.**

Reasons:

1. The map engine of `07` is the least specified part of the PL interface (`07` Open: frame-store pixel format, palette,
   mixer equation, DPO semantics, persistence timing, the map transactions are static only). Reimplementing it
   would be guessing, and the board's software would have to follow those guesses.
2. open-dso-core renders the display at any resolution (the core's W6 rendering; the SDS1000CML+ draws everything
   in software). One renderer for every scope is the project's rule ("share by default"); a PL renderer would be a
   second, scope-only implementation with its own look.
3. Area: the ZU2 is small (`12` §1), and the
   features this design adds (protocol/symbol engines, stream framer, stacker, zone BRAM) need that room.
4. The PL makes software drawing cheap where it matters: the readout engine's `minmax` mode delivers the
   1000-column envelope per channel (4 × 1000 × 4 bytes a frame), so drawing is O(columns), not O(samples), at any
   depth. Two A53 cores at 1.3 GHz draw 1000 × 480 vector traces at the panel's rate.
5. With everything drawn in software the PS holds the whole screen in fb0: screenshots and remote view need no
   PL capture path, and the composition is defined (no UNVERIFIED alpha rule, `07` §3.2).

Cost: intensity grading / persistence at high waveform rates is CPU work. If measurements on a unit show the need,
a later block adds a **PL density accumulator** (per channel a 1000-column × 480-row hit-count histogram updated
from the reducer's per-column min/max, in DDR through HP0, read and coloured by software) — a histogram, never a
compositor. The HAL already allows both (`Display.Renderer()` returns `false`; `DisplaySpec.HWRenderer = false`).

Display path in the image: UI VDMA MM2S (unchanged, `07` §4.1, so the existing framebuffer bring-up of `13` §5.1
works) → AXI4-Stream → video timing generator → RGB666 output registers on bank 44 with the pixel clock on AD12.
Panel timing (pixel clock, porches, polarities), the colour/bit order of the 18 data balls and the DE/HSYNC/VSYNC
balls are **UNVERIFIED** (`07` §1, `12` §7.2, §14 U8): the timing generator takes them from parameters filled in at
bring-up. Backlight PWM and LCD reset/enable keep MAP 0x015/0x016/0x14D.

## 10. Register ABI

### 10.1 Schema

The source of truth is `fpga/regs/open-pl.yml`: a small YAML schema that maps one-to-one onto SystemRDL
(`addrmap` → file, `regfile` → block, `reg` → register, `field` with `sw` access `r`/`rw`/`w1`/`rc`/`w1c`, `reset`).
The generator `fpga/tools/regmap.mjs` emits, at build time (never committed): a Verilog-2005 include of
`localparam` addresses, field positions and reset values (`fpga/build/open_pl_regs.vh`), Go constants for the board
(`fpga/build/openpl/regs.go`) and the **manifest hash**: SHA-256 of the canonical YAML, the low 64 bits of which the
image reports in ID_MANIFEST. Register decode stays hand-written; latched-while-idle and read-clear behaviour is
documented per register, not generated.

### 10.2 Identity first

The first block of the open plane (offset 0) is identity; it never moves between ABI versions:

| Offset | Register | Value |
|---|---|---|
| 0x000 | ID_MAGIC | `0x4F50_4C31` ("OPL1") |
| 0x004 | ID_ABI | major (31:16), minor (15:0) of this ABI; the board refuses a different major |
| 0x008 | ID_IMAGE | image name, four ASCII bytes, first character in bits 7:0 ("full") |
| 0x00C | ID_BUILD | build identity (git commit, 32 bits); Vivado writes the same value into USR_ACCESS, never a timestamp |
| 0x010 / 0x014 | ID_MANIFEST_LO / HI | 64-bit manifest hash of the register YAML this image implements |
| 0x018 | ID_CAPS | feature bits (§10.3) |
| 0x01C | ID_GEOMETRY | lanes (7:0) = 8, channels (15:8) = 4, tick in ps (31:16) = 500 |
| 0x020 | ID_SCRATCH | read/write, bus test |
| 0x024 / 0x028 | ID_UPTIME_LO / HI | `aclk` cycle counter; reading LO latches HI |
| 0x02C … 0x034 | ID_DNA0 … 2 | 96-bit device DNA (DNA_PORTE2) |

The board identifies the image without risking a bus access to an address the original image does not decode:
compat ACQ 0x002 (`0x8003_0008`, a version word both images decode, `02` §3) reads `0x4Fxx` on the open image —
bits 15:8 = 0x4F ("O"), which the original image does not use —
and only then does the board read the open plane.

### 10.3 Blocks of the open plane

| Offset | Block | Registers |
|---|---|---|
| 0x000 | ID | §10.2 |
| 0x100 | SYS | SYS_CTRL (core reset, ADC MMCM resets), SYS_STATUS (locks, core clock alive), SYS_CCLK_KHZ, SYS_ADC1_KHZ, SYS_FAULT (sticky, write 1 clears: rec_overflow, stream_overflow, adc1_slip, axi_wr_err, axi_rd_err), SYS_IRQ_EN, SYS_IRQ_STATUS, SYS_IRQ_TAKEN (read clears) |
| 0x200 | ACQ | ACQ_CTRL (strobes: arm, stop, force, clear, commit), ACQ_STATUS, ACQ_ROUTE, ACQ_DECIM, ACQ_MODE, ACQ_INVERT, ACQ_GNDSUB, ACQ_GNDCODE01/23, ACQ_SKEW, ACQ_PRE, ACQ_POST, ACQ_SEGMENTS, REC_BASE, REC_GROUPS, REC_LOST, TRIG_TIME_LO/HI, TRIG_POS, TRIG_PHASE, TRIG_FRAC, TRIG_FLAGS, SEG_SEL and the per-segment view |
| 0x400 | TRIG | TRIG_CTRL, OPEN/CLOSE/ABORT masks, QUAL_MASK/REQ, COND, T_A, T_B, HOLDOFF, AUTO, NTH_COUNT, PATTERN_CFG, per-channel comparator thresholds (A hi/lo, B hi/lo), VIDEO_CFG, ZONE block, counters |
| 0x600 | PROTO | engine select, line/channel map, slicer thresholds, bit timing, options, pattern and length, match/event/error counters |
| 0x800 | STREAM | control, decimation, ring base/blocks/beats, HEAD, TAIL, SEQ, LOST, status |
| 0xA00 | STACK | control, N, phases, bins, status |
| 0xC00 | READOUT | control, source, start, count, mode, status |
| 0xE00 | COUNTER | event select, gate, count, period |

ID_CAPS bits: 0 acquisition, 1 interval engine, 2 Nth edge, 3 video, 4 zone, 5 UART, 6 SPI, 7 I2C, 8 CAN, 9 symbol,
10 sequence, 11 stream, 12 stacker, 13 readout reducer, 14 counter, 15 interpolator, 16 segments. The board reads
them into the profile (`TrigCaps`, `Streamer.Kinds`, `Absent`): a feature the image lacks is refused with "not in
this image", not guessed.

### 10.4 Compat planes

Kept, word-indexed and 16-bit as in `02`, so the board's front-end, ADC bring-up, panel code and the unit's kernel
modules run unchanged against the open image:

| Plane / index | Semantics (unchanged) |
|---|---|
| ACQ 0x000–0x002 | version words: w0 = ABI major, w1 = ABI minor, w2 = 0x4F00 \| image index (§10.2) |
| ACQ 0x01F, 0x04F, 0x07A, 0x07B, 0x07C, 0x07D, 0x1FA, 0x1FB | ADC capture control and status (`12` §6.4) |
| ACQ 0x021 | bit 0 = 1 (record memory ready) once REC_BASE is set and HP0 is idle; 0x003 bit 0 is accepted (clear) |
| ACQ 0x141–0x149 | offset-DAC controller and codes (`02` §7, `06` §5); the DAC serial pins are **UNVERIFIED** (§14 U10) |
| ACQ 0x160 | interrupt acknowledge by read (§4.4) |
| ACQ 0x180–0x185 | scope ID request/ready/DNA (`02` §3) |
| ACQ 0x1AE–0x1B6 | mean engine (`02` §7, §6.5) |
| ACQ 0x1EF | accepted, no effect |
| MAP 0x015, 0x016, 0x14D | LCD reset, enable, backlight (`07` §1, §6) |
| MAP 0x0FA, 0x0FF | accepted, no effect (no waveform layer) |
| MAP 0x160–0x163, 0x169, 0x16A–0x16F | keys, encoder, LED word, latch, knob period, key interrupt acknowledge (`08`); panel pins **UNVERIFIED** (§14 U9) |

Every other compat index reads 0 and ignores writes. Nothing else of the original planes is implemented: the
acquisition, timebase, trigger, serial M block and map engine registers are replaced by the open plane.

## 11. Primitives and constraints

Device primitives appear only inside thin wrappers in `fpga/prim/`, each with a behavioural model selected by
`` `ifdef SIM `` and a smoke testbench:

| Wrapper | Xilinx (zynqus) | Contract |
|---|---|---|
| `prim_sync` | two FFs with `ASYNC_REG` | `STAGES` ≥ 2 flops, output follows a stable input after `STAGES` destination clocks; no logic between stages |
| `prim_pulse_sync` | toggle + `prim_sync` + edge | one source pulse → one destination pulse; source pulses must be ≥ 3 destination clocks apart (documented, checked in simulation) |
| `prim_reset_sync` | `ASYNC_REG` FFs, asynchronous assert, synchronous release | reset held ≥ `STAGES` clocks after release |
| `prim_bus_sync` | handshake over two `prim_sync` | a word crosses whole: the destination sees old or new, never a mix; one transfer per round trip |
| `prim_async_fifo` | gray pointers through `prim_sync`, inferred dual-clock RAM | no data loss or duplication; full/empty conservative; level outputs in each domain |
| `prim_lvds_lane` | IBUFDS (DIFF_TERM per §14 U5) + IDELAYE3 (TIME, fixed) + ISERDESE3 (DDR, 1:4) | 4 bits per CLKDIV, oldest bit first; latency fixed after reset release |
| `prim_mmcm` | MMCME4_ADV with dynamic phase shift | locked output; outputs held low until lock; phase step pulse → one step |
| `prim_dna` | DNA_PORTE2 | 96-bit value after a read request, ready flag |

Constraints are attribute-driven, never by hierarchical path: the XDC loops over cells with
`ASYNC_REG == TRUE` and constrains the path into the first stage with `set_max_delay -datapath_only` (one source-clock
period) and the gray-pointer buses with `set_bus_skew`. A multicycle exception, where unavoidable, is marked in
RTL with `(* dso_mcp2 = "true" *)` and selected by that property. Clock definitions, I/O constraints and clock groups
live in `fpga/vivado/` with the pin map.

## 12. How the HAL board maps onto the open PL

The board keeps implementing `hal.Board`; the packages below the HAL change only where registers change.

| HAL facet | With the original PL | With the open PL |
|---|---|---|
| `Platform.BringUp` | takes over the original image | programs the clock chip (`13` §4, unchanged), loads the open `.bin` through `fpga_manager` (`13` §3.3), checks compat ACQ 0x002 bits 15:8 = 0x4F, then ID_MAGIC, ID_ABI major and ID_MANIFEST against the board's generated constants; any mismatch refuses bring-up (the board then reloads the original image) |
| front end (`frontend`, `spi`) | relays, VGAs, offset DACs, ADC SPI, phase training | unchanged: the same SPI traffic and the compat ADC/offset-DAC registers |
| `Planner` | the rate tables of `04` §4 | the same rates (the HAL contract and the SCPI dialect keep them) plus any integer decimation; depth limited by REC_GROUPS in the reserved region |
| `Acquirer` | ACQ 0x003/0x019/0x021, banks, MAP 0x082 | ACQ block: COMMIT, ARM/STOP/FORCE, ACQ_STATUS; frame-ready by the `acq_done` interrupt through `/dev/siglent_dma` (unchanged driver) |
| `Readout` | ACQ 0x0C1–0x0D5 + AXI DMA | READOUT block + the same AXI DMA transfer; trigger placement from TRIG_POS, TRIG_PHASE and TRIG_FRAC instead of the φ arithmetic of `03` §4.3 |
| `Trigger` | `05` types onto A/M blocks | the interval-engine table of §7.4, the Nth/video/zone engines, PROTO block; `TrigCaps` from ID_CAPS (adds zone, symbol, sequence, frame-start; the `Broken` list is empty) |
| `Streamer` | the roll stream (`03` §6.4) | STREAM block: codes, bits and decoded events, gap-free; the ring is mapped from the reserved region |
| `Segments` | sequence mode registers | ACQ_SEGMENTS + the per-segment table (hardware timestamps in ticks) |
| `Stacker` | absent | STACK block |
| `Images` | absent | one image ("full"); `Images` reports it, `Load` refuses others |
| `Counter` | ACQ 0x063–0x06E, 0x111–0x11E | COUNTER block |
| `Display` | PL map engine (`HWRenderer: true`) | software rendering (`HWRenderer: false`, `Renderer()` absent); framebuffer unchanged |
| `Input` | MAP key/LED registers | unchanged (compat MAP) |
| `Profile.Stage` | 1 | 2 |

Packages: `app/internal/board` gains the open plane (from the generated constants), `acq`, `trigger`, `timebase`
(register set) and `counter` gain open-PL backends, `mapengine` is not used on the open image; `frontend`, `spi`,
`panel`, `platform`, `mmio`, `display` are unchanged. Both images stay supported: the board picks the backend from
what it loaded.

## 13. Sources, simulation, build

| Path | What |
|---|---|
| `fpga/regs/open-pl.yml` | the register ABI (§10) |
| `fpga/tools/regmap.mjs` | the generator (Node ≥ 22, no dependencies) |
| `fpga/prim/` | primitive wrappers and their behavioural models (§11) |
| `fpga/rtl/` | the RTL by block: `regs/` (AXI-Lite, open and compat register files), `trig/`, `proto/`, `acq/`, `stream/`, `stack/` |
| `fpga/*/sim/tb_*.v` | self-checking testbenches; `fpga/sim.sh` runs them all |
| `fpga/sim.sh`, `lint.sh`, `neutral.sh`, `synth.sh` | the lanes below |
| `fpga/vivado/` | the Vivado project/build Tcl, the PS block design Tcl and the XDC (§11), not run without a Vivado install |
| `fpga/bitstreams/images.json` | per built image: tool and edition, host, seed/strategy, sha256, worst slack, input hashes, ABI and manifest hash, and the board facts of §14 it was built against (`board_facts`, `board_facts_verified`) |

| Lane | What | CI |
|---|---|---|
| `make fpga-sim` | generates the register include and runs every testbench with iverilog (`-g2012 -DSIM`) | `rtl-sim` |
| `make fpga-lint` | `verilator --lint-only -Wall` on every module as its own top; waivers with their reasons in `fpga/lint.vlt` | `rtl-sim` |
| `make fpga-neutral` | yosys generic synthesis of the blocks meant for open-dso-core: no device primitive, no unknown module, `check -assert` | `rtl-sim` |
| `make fpga-synth` | Vivado out-of-context synthesis, placement and routing of each block at its clock, `fpga/build/synth/summary.md` | builder only |
| `make cosim-smoke` | `opl_core` Verilated with the co-simulation harness (`fpga/cosim`), its self-test | `rtl-sim` |
| `make cosim-test` | the firmware in the tier-1 simulator with the Verilated `opl_core` as its PL (§16) | local (the simulator lives in a separate test workspace, `16`) |

Builds need Vivado (ML Standard covers the XCZU2CG; the flow is written against 2026.1); CI never runs Vivado: a bitstream is built on a builder machine and committed with its provenance. All outputs go to
`fpga/build/`. `fpga/vivado/build.tcl bitstream` refuses to build while `fpga/vivado/verified.tcl` leaves a board
fact of §14 open, and writes `open_pl.json` next to the `.bin`: image name, sha256, build commit, each fact's state
(`board_facts`) and `board_facts_verified`. The board loads an image through `fpga_manager` only when that manifest
declares every fact verified and its sha256 matches (`app/internal/plimage`); an image without one never reaches the PL.

## 14. UNVERIFIED facts the design depends on

| # | Fact | Spec | Blocks it gates | How to settle |
|---|---|---|---|---|
| U1 | Part XCZU2 (not ZU3), speed grade -2 and temperature grade (the flow uses xczu2cg-sfvc784-2-e) | `12` §1 | the Vivado part | read CSU IDCODE `0xFFCA0040`, package marking |
| U2 | DDR is 1 GiB and `0x2000_0000`–`0x3FFF_FFFF` is free for the PL | `01` §1, `12` Open | record and stream rings | read the DDR controller configuration / probe on a unit |
| U3 | lane group A/B → ADC0/ADC1 and the DI/DQ bit order | `12` §6.2 | capture, every sample | ADC test patterns (`06` §9) read through a capture debug register |
| U4 | DCLK balls (AD5/AD4, L7/L6) and the MMCM configuration; the ADC12DL1500 board's lanes and rate | `12` §6.2 | capture clocks | SYS_CCLK_KHZ on a unit with a debug image; scope the DCLK |
| U5 | LVDS standard and internal vs board termination; VCCO of banks 64/65 | `12` §1.1, Open | the I/O constraints | board inspection; a wrong DIFF_TERM shows as errors in the test pattern |
| U6 | B4/A4, A9/A8 are DCLK_RST (held low) | `12` §6.3 | I/O | board trace |
| U7 | forwarded clock C3/C2 and D11 targets | `12` §6.3, §7.2 | whatever they clock (candidates: offset DAC, panel chain) | board trace |
| U8 | LCD pixel clock, timing, colour/bit order, DE/HSYNC/VSYNC balls | `07` §1, `12` §7.2 | display output | measure on J15 with the original image |
| U9 | front-panel interface (bank 25/26 bus): key scan, encoders, LED shift chain | `12` §7.2, `08` | keys, LEDs | logic analyser on the panel connector with the original image |
| U10 | offset-DAC (DAC80501) serial pins | `02` §7, `12` §7.2 | offset | logic analyser while the original firmware changes offset |
| U11 | the AC-line trigger source signal (no ball identified) | `05` §3.4 | AC-line source | board trace; until then the open image does not offer AC line |
| U12 | function of pl_ps_irq0[6] | `12` §4.1 | none (tied 0) | — |
| U13 | `pl_resetn0` after an `fpga_manager` reload | `12` §3.3 | reset (handled by GSR values) | observe on a unit |
| U14 | channel ↔ ADC core (I/Q) mapping and DES input selection | `02` §7, `06` | route stage | ADC test patterns per channel |
| U15 | ADC output code format as configured by `06` §9 (offset binary assumed) | `06` §9 | route stage | test pattern |
| U16 | DDR4 sustains 3.0 GB/s of HP0 writes next to the PS | `12` §1, §4 | full-rate deep records | measure with the record writer's lost-group counter on a unit |
| U17 | HPM0_LPD clock source in the original image (the open image drives it from pl_clk0) | `12` §3.2 | none | — |

## 15. Bring-up order on a unit (outline)

1. Read CSU IDCODE and the DDR size (U1, U2); build for the right part.
2. Load a **bring-up image** (the open image with the capture lane map left open and a capture debug register)
   after the normal boot; check ID, SYS_CCLK_KHZ/SYS_ADC1_KHZ (U4); run the ADC init and phase training (`12` §6.4) through
   the compat registers; read test patterns to fill the lane map (U3, U5, U14, U15).
3. Measure the LCD and panel interfaces with the original image (U8–U10) and fill the timing/pin parameters.
4. Full image: records at each route, the lost-group counter at 2 GSa/s deep (U16), triggers against the CML's FPGA
   signal sources, streams, stacking.
5. A bad image is recovered by a power cycle: the FSBL loads the original bitstream at every boot, and the launch hook's
   disable switch (`01` §12) keeps the app from loading the open image.

## 16. Co-simulation (pre-hardware test bench)

`opl_core` (`fpga/rtl/top`) is the image without the parts that wait for board facts: everything from the ISERDES
outputs to the PS ports, with the crossings of §3.1. Verilated (`fpga/cosim`), it is the PL of the tier-1 simulator
(`simd -pl open`): the simulator forwards the compat ACQ plane, the open plane, the stacker window and the AXI DMA
registers to it, shares the PS DDR with it, feeds it the samples of its signal generators through the decoded front
end and raises the kernel modules' interrupt nodes from its lines. The harness stands for the Xilinx AXI DMA, the DDR
slaves and the capture (samples injected after the lane map; the channel-to-core mapping is the U14 assumption of
§5.2). The firmware runs stage 2 against it unchanged (§12). What it does not model: the capture and its training
(U3–U5), the display scan-out, panel and offset-DAC serializers (the simulator keeps its models of the original
planes for them), DDR bandwidth and refresh beyond configurable stalls (U16), ADC1's clock crossing (the injected
ADC1 runs on `cclk`), metastability, and the timing of the full image (§13 OOC only).

The co-simulation never reaches a unit: it loads no image (the simulator's PL is the open design from power-up; a
load through `fpga_manager` resets it), and its manifest `fpga/cosim/open_pl_cosim.json` declares no board fact
verified, so the image gate of §13 refuses it.

## Open

- Area and timing of the full image on the XCZU2 are estimates until Vivado runs (§13); the image may need to split
  into images by feature as on the SDS1000CML+.
- Timing in the full image: out of context the trigger engine closes 250 MHz with little margin (its worst path is
  the 48-bit holdoff-end sum); placed beside the capture and the record writer it may need one more pipeline stage
  (one clock of trigger latency, no loss).
- The SPI trigger with chip select, CAN, the symbol and sequence triggers, zones, video, the capture, display and
  panel blocks: specified here, RTL to follow (capture, display and panel after U3–U10).
