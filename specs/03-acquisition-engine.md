# 03 — Acquisition Engine

The acquisition engine is the PL logic between the ADC interfaces and the PS: it samples into the PL record
memory under control of a small arm/trigger/stop state machine, and on request streams a record (or part of it)
through the AXI DMA into PS DRAM. This spec gives the model, the exact register sequences with ordering, poll
limits and timeouts, and the data format. Register definitions: `02`; rate/depth/decimation: `04`; trigger
programming: `05`; rendering of acquired data on screen by the PL map engine: `07`.

## 1. Model

```
 ADC0/ADC1 ──ISERDES──► decimation / peak (04 §5) ──► record memory (PS DDR4 via S_AXI_HP, per channel)
                                                         │   ▲ arm / stop FSM (ACQ 0x003, 0x019, 0x021)
                                                         │   └── trigger engine (05)
                                                         ├──► map engine (waveform layer, 07) — autonomous
                                                         └──► readout: ACQ 0x0C1–0x0D3 ─► AXI S2MM DMA ─► 0x1E80_0000
```

- The record memory is not a PL-side DDR: the PL has no memory interface and writes the records into PS DDR4
  through the S_AXI_HP ports (`12` §1, §4). Its address range is UNVERIFIED (see Open); the PS never addresses it
  directly, only through the readout path (§4.3).
- The PL renders the waveform layer by itself from the record memory (`07`); the PS reads samples only for
  measurements, math, SCPI waveform transfer, save, and history/sequence playback. A replacement firmware that only
  displays waveforms does not need the readout path; anything that touches sample values does.
- One **frame** = one triggered (or auto-triggered) record of all enabled channels. Continuous ("run") operation is
  the PS re-arming after every frame it has read (§3.6). Whether the FSM stops by itself after one frame or keeps
  overwriting the record until the PS stops it is UNVERIFIED; the arm sequence does not set a stop bit while any
  channel is enabled (§3.2 step 7).
- Register writes take effect immediately except the per-channel banked parameters (`02` §4.3), which take effect
  at the bank commit pulse.

## 2. Start-up initialisation

This is the single authoritative start-up order (PL side and analog front end; `06` §11 refers here). It
applies after the clock chip is programmed and the bitstream is loaded (`13`). Write values even if they equal
a previous value (write rule of `02` §1.2).

**A. Chip creation and init**

1. Read the hw-id byte (GPIOs 334…341: 2/4-channel variant, PCB/BOM version, `02` §3); map ACQ and MAP planes;
   read version 0x000–0x002 (display only).
2. **ADC type** 0x1FB = HWID bit 4 (`06` §9.1).
3. **Offset-DAC init**: 0x141 = 4; wait 2 ms; write 0 to 0x146, 0x148, 0x149, 0x147, 0x144, 0x143, 0x142, 0x145;
   wait 2 ms; 0x141 = 8.
4. Read 0x002 (board-test byte; bits 15:8 = 0xBD → board-test bitstream, `02` §3, `15`).
5. **Stop**: RMW 0x019 bit 3 = 1; wait 10 ms.
6. **AXI enable** 0x1EF = 1 (PL effect UNVERIFIED).
7. VGA init: 0x040A to all four VGAs (`06` §4).
8. ADC init, ADC 0 then ADC 1 (ADC 0 only on 2-channel models) (`06` §9.2/§9.3).
9. ISERDES reset pulse: 0x04F = 1, 0x04F = 0.
10. Relay image prepared (no SPI frame yet, `06` §3.4); sysfs GPIO 343 export, direction `out`, value 1.
10a. sysfs GPIO 373 (probably a power enable; function UNVERIFIED, `01` §7): unexport, export, direction `out`,
    value 1 — immediately before step 11.

**B. Phase training**

11. ADC test pattern, PLL sync (0x1FA bit 8 pulse), phase scan and apply (`06` §9.4). No other register traffic
    in between.

**C. Register init and first configuration**

12. **Register init**: poll 0x01F bit 12, then bit 13 (≤ 5 000 polls each; on timeout continue); record-memory
    reset 0x003 = 1, 0x003 = 0; history pause 0x0F8 |= 0x100; 0x03C = 1; 0x0EA = 1, 0x0EA = 0; 0x128 = 0;
    trigger skew 0x09C = 1, 0x09D = 1; then the MAP-plane init (`07`).
13. Stop (§3.4).
13a. **Calibration load** (`10` §7 steps 1–5: factory DAC file, user self-cal file, skew data; defaults where a
    file is missing). Must be complete before step 14.
14. Channel enable 0x0B1 = mask; per channel the channel setup (`06` §11.1: VGA, relay image, offset DAC + 5 ms,
    full scale), trigger level (`05` §3.3), invert mask 0x00F (`02` §7), GND registers (`06` §8.3), VGA filter and
    relay BWL bits.
15. ADC mode words on both chips (`06` §9.2); check 0x01F bit 8; 0x07D = 0x102, then 0x002; ADC12DL1500 only:
    re-apply the phase (`06` §9.4 step 7).
16. **Timebase setup** (§3.3: ACQ banks, map timebase sets `07` §7.8) and the FIR selector 0x1FF (`04` §6).
17. Trigger configuration (`05`): source, slope, type, mode, coupling, and the boot defaults of every other
    trigger type (`05` §7). Zoom off (§6.5): MAP 0x10A ← 0 and the full-screen split (`07` §7.4), ACQ 0x089 ← 0.
18. Soft reset (§3.5) including the 0x0EA pulse.
19. **First arm** (§3.2), including the pending ADC internal calibration (`06` §9.5) and start delay 110.

The scope ID (0x180 = 0, 1; poll 0x181 bit 0 ≤ 1000 × 1 ms; read 0x182–0x185) may be read at any time after
step 1. The history window boot values of §6.3 (0x0F8 ← 0x100,
0x0F4 ← 1, 0x10F ← 0, 0x0F5/0x0F6/0x0F7 ← 0, 0x0F8 ← 0x101), the readout buffer select 0x05C ← 0 and the switch
index 0x02E ← 0 are written once at start-up, at the latest in step 12; otherwise these registers belong to the
stored-record redraw and re-arm paths (§3.3 step 7, §3.6). The map PLL registers MAP 0x004/0x005 are not part of
the start-up (`02` §9: never written or read).

**Completeness.** The order above is sufficient provided steps 12–19 write every register of their sets
unconditionally (rule at the top of this section): the timebase, search and resize set of `04` §8/§9, the
trigger-type defaults of `05` §7, the history values above and the map-init values of `07` all get their start-up
values there. The firmware must not arm before step 12, and its frame cycle (§3.6) must not run before the first
arm: the last-frame flag MAP 0x082 can read 1 right after process start, and a frame started on a partly
configured PL runs on undefined settings.

## 3. Arm / run / stop state machine

### 3.1 States

| State | Entered by | Left by | Observable |
|---|---|---|---|
| STOPPED | power-up stop bit; stop request; frame complete | arm | 0x021 bit 0 = 1 (ready), 0x019 bit 9 = 1 after a frame |
| ARMED / PRE-TRIGGER | arm strobe | trigger (or auto timeout 0x00C/0x00D) | 0x019 bit 9 = 0 |
| POST-TRIGGER | trigger | record full | 0x019 bit 9 = 0 |
| DONE (frame ready) | record full | readout + re-arm, or soft reset | 0x019 bit 9 = 1, 0x03E bit 0 = 1 (valid parameters) |

Exact pre/post-trigger behaviour of the auto timeout and holdoff are in `05`.

### 3.2 Arm sequence (exact)

0. 0x115 = 0 (draw stop released, §3.3 step 7). If the ADC calibration-pending flag is set, run the ADC internal
   calibration now (`06` §9.5).
1. Program the acquisition start delay in ACQ 0x047 (bits 11:0 = delay d, bit 12 = enable; unit ms, UNVERIFIED:
   the delay only holds off the start of acquisition by about d). Delay value, first matching rule:

   | Condition | Delay |
   |---|---|
   | sequence mode on | 0 (write bit 12 = 0) |
   | trigger type Video (FPGA type code 3, `05` §1) | 249 |
   | a "delayed acquisition" operation is pending (set by the acquisition sample-mode change and by switching XY on; consumed by this use) | 250 |
   | the ADC internal-calibration flag is set (the calibration has just run, `06` §9.5) | 110 |
   | otherwise | 10 |

   Relay switching needs no extra start delay. Write 0x047 ← 0x1000 | d on every arm requested by a settings
   change (d = 0: bit 12 ← 0 only). Setting bit 12 first (0x047 ← 0x1000 | (0x047 & 0xFFF)) and then the value is
   equally correct, and a write may be skipped when d equals the delay programmed last. Bit 12 is also cleared
   during self-calibration. Examples: 0x106E (110) at start-up and after V/div changes (calibration ran), 0x100A
   (10) after a channel switch-off, 0x10FA (250) after sample-mode / XY changes. The start delay and
   the pending calibration apply only to arms requested by a settings change, RUN or start-up, never to the
   per-frame re-arm of §3.6.
2. Poll 0x021 bit 0 = 1, ≤ 300 000 polls. **On timeout do not arm** (log and return).
3. 0x003 = 0x10 (arm strobe).
4. 0x019 = 0 (clear stop request).
5. Read 0x06F (trigger/acquire state; discard).
6. 0x003 = 0.
7. Only if **no analog channel is enabled** (0x0B1 = 0) **and no digital channel is open** (digital channels exist
   only with the SLA1016 probe, `15` §2; without it this condition is just 0x0B1 = 0): set 0x019 bit 3 = 1 (stop
   request) right after arming. Otherwise leave 0x019 = 0.
Nothing else follows the arm. (0x0D5, 0x024, 0x0CA belong to the readout-mode change, §4.3/§6.4; the 0x0EA
timestamp-reset pulse is issued only at register init, by the soft reset, and when the system date/time or time
zone changes.)

### 3.3 Applying horizontal settings

Any change of time/div, depth, channel set, acquisition mode or horizontal position is applied while stopped. The
order (for time/div, depth and channel-set changes):

1. **Stop** (§3.4): 0x019 bit 3 = 1, if acquiring. Stop ready is not polled at this point.
2. **ADC mode refresh** (only when the channel set or the sample rate changes; a time/div change that keeps the
   rate writes nothing here): ADC mode words, check 0x01F lock (bits 8, 12), then 0x07D = 0x102, 0x07D = 0x002, and
   the channel setups (`06` §9.2).
3. **Timebase pass**: for every channel of the model, n = 0 … C − 1 (C = 4 or 2, regardless of which channels are
   enabled), run the bank sequence of `02` §4.3 with bank n and write the timebase register set inside the
   window in the order of `04` §8: §8.2 steps 1–10 (speed / switch select 0x022, roll bit 0x010, down-sampling,
   interpolation set, 0x005, 0x096, 0x098, 0x10E, 0x092, 0x094, depth, segment count, timeouts, 0x039, 0x1C7,
   0x1EE), then the §9 position registers, the resize 0x1DF–0x1E4 (`02` §4.2) and the §8.3 search set. (`04` §8
   is authoritative for values and order; which registers a replacement must write in banks 1 … C − 1 is
   `02` §4.3.) After the ACQ banks, the map timebase sets (`07` §7.8).
4. **FIR selector** 0x1FF (`04` §6).
5. **Horizontal-position pass**: banks n = 0 … C − 1 again with the position registers (0x09A/0x09B, 0x08C, 0x09E,
   0x006/0x028).
6. If zoom (or XY) is enabled, banks 4 … 4 + C − 1 with the zoom parameters.
7. **Stored-record redraw (optional)** (§3.7): while a record is held for display, the PL map engine redraws it
   with the new settings from the record memory. A replacement that does not redraw stored records with the PL
   may skip this step.
8. Start delay and arm (§3.2).

No soft reset (§3.5) is part of this sequence. Every settings change runs in this frame:
stop (step 1; nested changes stop only once, at the outermost), the change itself (steps 2–6), the
stored-record redraw of step 7, then the arm of §3.2. A settings-change arm has **no** 0x02E toggle; the toggle
(0x02E ← 0 before, ← 1 after the arm) belongs to the per-frame cycle of §3.6 and to the trigger-level re-apply
of `05` (which runs its own stop/arm). Changes that do not touch the timebase follow the same frame without
steps 2–6; a channel bandwidth-limit change needs no stop at all (`06`, `09`).

**While stopped** (Stop, Single done, sequence complete, history on) steps 2–4 and 6 are **not** run with the new
settings and there is no arm:

| Step of the frame above | Running | Stopped, with redraw | Stopped, without redraw |
|---|---|---|---|
| 1 stop | yes | — | — |
| 2 ADC mode refresh, 3 timebase pass, 4 FIR, 6 zoom banks | yes | no (deferred to the next RUN) | no (deferred) |
| 5 position pass | yes | only for a horizontal-delay change, with the record's timebase (below) | no |
| 7 stored-record redraw (§3.7) | yes (with live pass) | yes: §3.7 steps 1–6 and 8, no live pass | no |
| 8 arm | yes | no | no |

So a time/div, V/div, offset or depth change while stopped runs only the stored-record redraw, which redraws the
held record under the new view. A replacement without stored-record redraw stores the new setting only and applies
it with the full frame at the next RUN; the screen keeps the last frame (it may render it in software from its PS
copy). A horizontal-delay change additionally runs the step-5 position pass first, evaluated with the
**record's** timebase (rate, D, S and I of the record, `04` §9) and the **current** delay τ — e.g. 1 ch 2 GSa/s
200 ns/div record, `:TIMebase:DELay 1E-7`: 0x09A = 2, 0x08C = 4, 0x09E = 0x24 (unchanged record start) and
0x006 ← 0x123 (= 0x13C − R·τ/q). The live timebase (0x01A, 0x027, 0x00C/0x00D, 0x02A/0x02B, 0x1C7, 0x092, 0x094,
0x10E …, ADC/front-end words of a rate change) is written only at the next RUN, which runs the full frame: steps 2–6
with the live settings, the map sets, a soft reset (§3.5), the §3.7 redraw with its live pass, then the arm.

### 3.4 Stop

- **Stop request**: RMW 0x019 bit 3 = 1. The FSM finishes (or abandons, UNVERIFIED) the current frame and stops.
  With an SLA1016 digital channel as trigger source the probe is stopped instead, over its USB
  protocol (not specified, `15` §2); without the probe ignore this case.
- With pass/fail "stop on fail" active, a failed frame stops acquisition the same way.

### 3.5 Soft reset

0x003 = 1, then 0x003 = 0 (record-memory reset). Only if a digital slave board is connected: poll 0x021 bit 0 = 1,
≤ 300 000 polls. Issue it during register init (§2 step 12), in step 18 of §2, twice (each pulse between a map
clear block and a map soft-reset pulse, `07` §7.2/§7.5) on every acquisition-mode change (normal / peak / average /
ERES) and when the last analog channel is switched off, and once on every trigger sweep-mode change
(Auto / Normal / Single, `05`; e.g. `:TRIGger:MODE NORMal`: stop, trigger writes, MAP 0x15A pulse and clear
block, MAP 0x103 ← 1, 0x003 ← 1, 0, MAP 0x103 ← 0, then the arm). It is not issued on time/div, depth,
position, channel-set, V/div, coupling or invert changes.

### 3.6 Frame cycle in run mode

**Frame-ready signal.** A complete frame is signalled by the **last-frame flag MAP 0x082** (bit 0 = 1: a complete
frame is held). Read it periodically (10…220 ms) and run the per-frame cycle below only when it reads 1. ACQ
0x019 bit 9 is the stop-ready bit polled *after* the stop request, not the frame-ready test; ACQ 0x03E bit 0 (valid
parameters) is read inside the cycle. There is no timeout while waiting: in Normal and Single the wait for a
trigger is unbounded.

**Replacement frame loop** (normative; Normal and Auto, and one pass for Single):

```
arm (§3.2, with start delay and pending calibration when requested by a settings change, RUN or start-up)
loop:
    wait: read MAP 0x082 every 10…220 ms until bit 0 = 1 (no timeout)
          — or, with a read-data request pending, block on /dev/siglent_dma (below)
    stop request: RMW 0x019 bit 3 ← 1 (0x019 ← 8)
    poll 0x019 bit 9 = 1 (≤ 30 000 polls, §4.1); on timeout skip this frame and re-arm
    0x02E ← 0
    poll ACQ 0x0FD bit 7 = 0 (map engine idle, ≤ 5 002 polls)
    read 0x1C9 and 0x03E; if 0x03E bit 0 = 0 skip the readout
    frame information (§4.2); read out what is needed (§4.3)
    [optional: stored-record redraw of §3.7 with this frame as the record]
    0x115 ← 0
    if Single: stop here (report Stop)
    arm: §3.2 steps 2–7, without start delay and without calibration
    0x02E ← 1
```

Register-level order of one pass with the stored-record redraw (Normal, 1 ch): MAP 0x082 read = 1; 0x019 ← 8; 0x02E ← 0; wait map ready;
read 0x1C9, 0x03E bit 0 and the frame information of §4.2 (0x120, 0x02F, 0x026/0x05B; 0x05C ← ping-pong buffer);
history / preview update (0x0F8, 0x0F4, 0x10F, 0x0F5–0x0F7 as needed); then the stored-record redraw of §3.7 with
the frame just taken as the record (MAP 0x172 ← 1, stored banks `04` §12, 0x116 / 0x115 ← 1,
trigger-interpolation read, live banks incl. the interpolation mode 0x081, MAP 0x172 ← 0); 0x115 ← 0; arm (§3.2
steps 2–7) and then 0x02E ← 1. A replacement that does not redraw stored frames needs only the reads and the arm.

Ways the PS can take frames in run mode:
- **Frame-ready cycle**: the periodic cycle above, gated by MAP 0x082 (stop, wait stop ready, wait map ready, read,
  re-arm).
- **Read-data request** (for a data consumer: a measurement, math, SCPI waveform reads in run mode …): MAP 0x148 ← 0;
  wait for the `siglent_dma` interrupt (`02` §10, `14` §2: `select()` on `/dev/siglent_dma`, then
  `ioctl(fd, 1, 0)`); read the frame (§4.3, all enabled channels); MAP 0x148 ← 1, wait 30 ms; next request.
  MAP 0x148 is 0 at power-up: write 1, then 0, for the first request.
- **No PS reads** (plain Auto): with A 0x00E bit 12 = 1 the PL free-runs and the map engine draws by itself.
  Whether a real PL keeps re-triggering without a PS re-arm is UNVERIFIED.

A replacement runs the loop above in both Auto and Normal — the stop request before the re-arm makes it independent
of whether the PL kept running — and never re-arms in Single.

**Run-mode readout for a consumer** (SCPI `:WAVeform:DATA?`, measurements while running): use the same cycle —
stop request, wait 0x019 bit 9 and 0x0FD bit 7 = 0, read the frame (§4.3 steps 0–10), re-arm (§3.2 steps 2–7
without start delay). 0x01E (wave save) and the history pause 0x0F8 bit 8 set by §4.3 do not stop the FSM; whether
they alone protect the record against overwrite while armed is UNVERIFIED, so a replacement stops first.

Single mode = one pass of the loop, with A 0x00E bit 12 = 0 (`05` §7: non-continuous mode; Auto/Normal set it to
1). After the Single frame the firmware issues the stop request (0x019 ← 8) itself and then reports Stop
(`:TRIGger:STATus?` → `Stop`; RUN in Single = one more arm, frame, stop). Whether bit 12 = 0 alone would make the FSM
stop is UNVERIFIED; a replacement must not re-arm in Single and issues the stop request (§3.4) after reading the
frame. Stop = do not re-arm.

### 3.7 Stored-record redraw (exact)

> Optional feature: not needed for a minimum viable firmware (`00` §5). Without it, follow the "replacement without
> redraw" column of §3.3.

**Overview.** The stored-record redraw makes the PL map engine draw a record already held in the record memory
under new display settings (time/div, V/div, offset, delay) without acquiring: the PS reprograms the configuration
banks with a *stored* set that describes the held record, sets draw stop, and (in run mode) restores the live set
before the next arm. Its parts are spread by owner: this section gives the order; the ACQ stored-record values are
`04` §12; the MAP stored-record timebase set is `07` §7.8 ("stored-record set") and the DPO value `07` §7.9; the
channel-mask side effect on the relay word is `06` §8.2; the delay limits while stopped are `09` §3.3.

Runs inside the §3.3 frame after steps 1–6 (and once per frame in the §3.6 cycle); while stopped it is the whole
change (§3.3 "While stopped": the steps 1–6 and 8 below, without the live pass of step 7 and without the arm). The "record" is the frame
held in the record memory (its rate R_r, time/div, depth D_r, channel mask and vertical settings are kept by the
firmware from the moment it was armed). Order:

1. Read the frame information: 0x120 (ping-pong bit 0), 0x02F (sequence ready / overflow, §4.2).
2. MAP 0x174 ← 0x20, MAP 0x172 ← 1 (pass/fail output and mask hold during the redraw, `07`).
3. **Stored bank pass**: for n = 0 … C − 1, bank n (`02` §4.3) with the stored-record set of `04` §12 (timebase,
   search set with 0x118 ← 1, 0x010 ← 0x8000, position, resize with the record's vertical ratio); with the XY
   display or the zoom window on, also banks 4 … 4 + C − 1 (stored timebase set; with zoom nothing new is
   written there, and the stored map sets 4 + n carry only 0x1BD ← 0, 0x1BE ← 0x167, `07` §7.8). Then the record's channel mask is applied
   (0x0B1 and the per-channel side effects of a channel switch, `06` §8.2, `07`), when it differs from the live
   one.
4. Stored map timebase sets (`07` §7.8, stopped-record variant) and the DPO value of `07` §7.9.
5. Poll 0x116 bit 0 = 1 (≤ 20 000 polls), then 0x115 ← 1 (draw stop: the map engine draws the held record).
6. Trigger-interpolation read: bit 13 pulse on the current 0x018 value (bits 9:0 = the §6.2 segment setting, bit 10
   sequence enable and bit 12 kept): 0x018 ← v | 0x2000, then 0x018 ← v (e.g. 0x2002 / 0x0002; with sequence on and
   N = 37: 0x2425 / 0x0425); poll 0x0A4 bit 0; read 0x0AA, 0x0A6.
7. **Live pass** (run mode only; skipped while stopped): the horizontal-position pass and the timebase pass of §3.3
   steps 3–5 again with the live settings (and the live channel mask), then the live map sets, then — with zoom on —
   the zoom banks 4 … 4 + C − 1 and zoom map sets of §3.3 step 6 again (the pass ends with bank select 0x70).
   It must rewrite at least every register the stored pass changed.
8. MAP 0x172 ← 0, MAP 0x174 ← 0; then (run mode only) the arm of §3.2 (its step 0 releases 0x115). While stopped
   0x115 stays 1 (the map engine keeps showing the held record).

Worked example (run mode, 1 ch, depth 100k or more — 2 GSa/s at both time/divs, `04` §4.2 — 200 ns/div frame held, then
`:TIMebase:SCALe 1E-6`). First the §3.3 timebase pass with the new live set: S = 20 000, Δ = 160, I = 1, so `04`
§8.2 gives N = 5000, 0x098 = 4, L0 = 0x502 (0x092), L1 = 0x4E2 (0x094), 0x10E = 160/4 + 0x502 = 0x52A, D = 0x88,
0x1C7 = 0xB. Then the redraw: MAP 0x174 ← 0x20, MAP 0x172 ← 1; stored banks for the 200 ns/div record (S_r = 4000,
Δ = 160: D = 0x20, 0x098 = 1, 0x092 = 0x408, 0x094 = 0x3E8, 0x10E = 0x430 from the `04` §12 stored set, 0x09A = 2);
MAP 0x1AE/0x1AF = 0x190/0x258; 0x116 / 0x115 ← 1; 0x018 = 0x2002 / 0x0002; the live pass (0x09A = 4, 0x08C = 8,
then 0x098 = 4, 0x10E = 0x52A, 0x092 = 0x502, 0x094 = 0x4E2, D = 0x88); MAP 0x172/0x174 ← 0; 0x115 ← 0; 0x047;
arm. (At depth 10k the same change would run at 1 GSa/s: S = 10 000, Δ = 80, 0x098 = 2, 0x10E = 0x516.)

## 4. Frame completion and readout

### 4.1 Completion polls

| Step | Poll | Limit |
|---|---|---|
| frame ready (complete frame held) | MAP 0x082 bit 0 = 1 | one read per check, no timeout (§3.6) |
| stop ready after the stop request | 0x019 bit 9 = 1 | 30 000 polls |
| triggered | 0x019 bit 9 = 1 (roll: bits 9 and 0 both = 1) | single read |
| valid frame parameters | 0x03E bit 0 = 1 | single read per UI tick |
| map engine idle | ACQ 0x0FD bit 7 = 0 | 5 002 polls |
| trigger-interpolation value ready | 0x019 bit 0 = 1 | 30 000 polls |

### 4.2 Frame information

- Current timebase index of the frame: 0x1C9.
- Ping-pong (double-buffer) state: 0x120 bit 0; write it to 0x05C (bits 7:0) to select that buffer for readout.
- Sequence overflow: 0x02F bit 15; sequence ready: 0x02F bit 12.
- Segment count (17 bit) = 0x026[15:0] | (0x05B bit 9) << 16. Bits 15:10 of 0x05B are not part of it.
- Previous buffer: sequence overflow = 0x120 bit 8; segment count (17 bit) = 0x122[15:0] | (0x121 bit 0) << 16.
- **Record memory holds valid data** (checked before a record readout, e.g. a SCPI waveform transfer): sequence
  overflow (0x02F bit 15) ≠ 0, or segment count ≠ 0, or previous-buffer overflow (0x120 bit 8) ≠ 0, or
  previous-buffer segment count ≠ 0. If all four are 0 there is no record to read. In normal (non-sequence) mode a
  completed frame reads segment count ≥ 1; the per-frame completion signal itself is MAP 0x082 (§3.6, §4.1).

### 4.3 Record readout (DDR record → PS)

A stopped frame (Single done, Stop, or a frame taken in run mode for measurements / SCPI with the run-mode readout
cycle of §3.6) is read channel by channel: display / measurement reads fetch every enabled channel, a
`:WAVeform:DATA?` only its `:WAVeform:SOURce` channel (e.g. 2 channels on, C1 source → one channel pass with
0x0C1 = 0). The readout always fetches the **screen record** of the channel: the n = S samples of the 10 displayed
divisions (`04` §8.1; zoom reads use the zoom window), plus lead-in and tail samples; selection of
`:WAVeform:STARt/POINt/INTerval` and conversion to the SCPI format are done on the PS copy (`11`).

**Frame selection** (once per frame, before the channels):

0. History pause, conditional: if 0x0F8 bit 8 is 0 (test the software copy), set it and remember
   the pause; if it is already 1, leave it.
1. 0x01E ← 1 (wave-save enable: freeze the frame against overwrite).
2. 0x018 bit 12 ← 1 (segment-select enable; bits 9:0 keep the sequence segment-number *setting* of §6.2, which is 2
   outside sequence mode, giving 0x1002 in normal readouts). 0x04D ← read segment index (0 for the
   current frame; the segment k in sequence/history playback, §6.2).
3. Screen-record reads only (not the piece reads below): 0x0D3 bit 8 ← 1 − compress type (read-modify-write;
   compress type 0 for every screen-record read, i.e. bit 8 = 1) and 0x0D5 ← compression rate (below). Then, for
   both kinds of read, the compressor reset pulse 0x0D4 ← 0, 1, 0.
4. Read the trigger-interpolation value (0x018 bit 13 pulse: 0x3002 then 0x1002; poll 0x0A4 bit 0; read 0x0AA,
   0x0A6; `05`) and the timestamp (same pulse; read 0x0E8, 0x01D, 0x01C).

**Per channel ch** (0–3; zoom reads use the same numbers; 4 = digital):

5. 0x0D3 bits 1:0 ← 1 (DDR record read mode; bit 8 kept, e.g. 0x101).
6. 0x0C9 ← 0, 1 (DMA buffer init); 0x0CA ← 1 (enable); 0x0C1 ← ch.
7. 0x0C3 ← O & 0xFFFF, 0x0C4 ← O >> 16 (7 bits); 0x0C5 ← L & 0xFFFF, 0x0C6 ← L >> 16 (10 bits), with O and L below.
8. 0x0C2 ← 0, 1 (record read start).
9. Transfer N samples = 2N bytes with §4.4 (blocks ≤ 7 000 000 bytes; the AXI length and 0x0CF/0x0D0 of each
   block are computed from its byte count as in §4.4).
10. After the last block of the last channel: 0x0CA ← 0; then 0x018 bit 12 ← 0 (0x0002), 0x01E ← 0, and
    MAP 0x148 ← 1 (read-data request stop, `07`); finally, only if a remembered pause is pending, history resume
    0x0F8 bit 8 ← 0 and forget it. The remembered flag is set by step 0 and by the history pause of the preview
    update that every settings change runs, and cleared only by this resume: after a time/div change (pause 0x0F8 ← 0x101)
    a `:WAVeform:DATA?` ends with 0x0F8 ← 0x001; after the sequence reset of §6.2 (0x0F8 ← 0x101 written directly,
    not remembered) the readout leaves 0x0F8 unwritten.

**Symbols of the readout formulas** (this section; §6.4 uses its own, named there):

| Symbol | Meaning | Unit | Source |
|---|---|---|---|
| R | table sample rate | MSa/s | `04` §4, §8.1 |
| S | screen sample count (10 divisions) | samples | `04` §8.1 |
| I | display interpolation factor | — | `04` §5.4, §8.1 |
| Δ | DDR interval | samples | `04` §8.1 |
| u | trigger interval unit, Δ/20 (`04` §8.1); in the φ formula below the PL interval unit u_t | — | `04` §8.1 |
| D | record depth | Δ-sample units | `04` §8.2 step 5 |
| σ | channel skew | µs | `04` §9 |
| τ | horizontal delay | µs | `04` §9 |
| f | trigger remainder | interpolated samples | `04` §9 |
| F | latency compensation term | samples | `04` §9 |
| k | first screen sample to read | samples | below |
| n | number of screen samples to read (S for a screen-record read) | samples | below |
| G | FIR lead-in groups, ⌊100/Δ⌋ + 1 | Δ units | below |
| φ, t | trigger fine value, t = ⌊φ/I⌋ | samples | below |
| P, p, e | first-screen-sample position, its Δ·I part, its remainder | interpolated samples | below |
| O | readout start offset (0x0C3/0x0C4) | Δ samples | below |
| λ | lead-in | interpolated samples | below |
| L | readout length (0x0C5/0x0C6) | 4-sample units | below |
| N | samples transferred | samples | below |
| c_f | compression factor (1, or 2r) | — | "Long records" below |
| M | piece size, 5 000 000 | samples | "Long records" below |
| Q | piece count | — | "Long records" below |
| j | record sample index | samples | "Time axis" below |

**Window arithmetic.** Δ, D, I, S, σ and the trigger remainder f come from the timebase of the frame (`04` §8, §9);
k = first screen sample to read (screen-record reads: 0 — the whole screen record is read, n = S,
whatever `:WAVeform:STARt/POINt/INTerval` say; piece reads below: k = `:WAVeform:STARt`); G = ⌊100 / Δ⌋ + 1 (100 =
lead-in for the math FIR filter, half its order 199; G = 1 / 2 / 3 for Δ = 160 / 80 / 40); t = ⌊φ / I⌋ (integer
division) with φ the integer trigger fine value of the frame:

  φ = ⌊φ_hw + φ_pre⌋ + φ_0

- φ_hw (from the trigger-interpolation read of step 4) = (0x0A6 & 7) · I · u_t + I · y / I_r, with
  y = 4 − (0x0AA & 0x1F) + 4 · ((0x0AA & 0xFFFF) >> 6) (the sign of the last term is negative for the EXT
  sources 4 and 5 of `05` §1, which do not exist on this product), I_r = the interpolation factor of the
  record (= I for a live readout), and u_t = the PL trigger interval unit: 8 / 4 / 2 for work mode m = 0 / 1 / 2
  (`04` §8.1) at switch select 0, 4 at switch select 1, 8 at switch selects 2 and 3.
- φ_pre (only while decimating, i.e. down-sample enable 0x04E = 1, `04` §5.2) = −(0x040 | 0x041 << 16) · c_pre ·
  (R / 1000) · I, where c_pre is the PL clock-period factor of the pre-trigger address (UNVERIFIED; not known).
  0 when not decimating.
- φ_0 = 2, a constant term (UNVERIFIED; with φ_hw = 4 and φ_pre = 0 it gives φ = 6).

Where a replacement gets each input: 0x0A6 and 0x0AA from the read of step 4 of this frame; I, m, switch select,
R and the decimation state from its own timebase setup of the frame (`04` §8); 0x040/0x041 read from the ACQ
plane after the frame (`02` §4.2). **Fallback**: if any term is unavailable (e.g. φ_pre while decimating), use
φ = 6 (t = ⌊6 / I⌋). The window over-fetches 150 samples on each side (L and N below), so an error |Δφ| < 150
only shifts the lead-in; the trigger is then placed with the "Time axis" relation below, with an error of ±|Δφ|
samples. A replacement must not treat t as a constant once φ_hw is available.

- P = (Δ·D − S)·I/2 − 32·I − I·σ·R: position of the first screen sample in the record in interpolated-sample units
  (`04` §9), p = ⌊P / (Δ·I)⌋, e = (P − p·Δ·I) − f + (k mod Δ) (interpolated-sample units, like P).
- O = ⌊k/Δ⌋ − G + p (Δ-sample units). Lead-in λ = I·G·Δ + e. If O ≥ 2: O = O − 1 and λ = λ + I·Δ.
- L = ⌊(λ/I + n + 300 + t + G·Δ) / 4⌋ + 20 (4-sample units; the 300 is 150 + the 150 tail of t + 150).
- N = min(λ/I + n + 150 + t, 4·L) samples (truncated to an integer), divided by the compression factor c_f
  (below); 2N bytes.

With I = 1 these reduce to λ = G·Δ + e (+ Δ). The PS copy then holds λ/I + t lead-in samples, the n screen samples
and 150 tail samples: the screen starts at buffer index λ/I + t (the trigger fine term t shifts it, see "Time
axis" below). For a held record redrawn with other settings (§3.7) p and e are those of the stored position set
(`04` §12). Worked examples (τ = 0, σ = 0; t = 6 except where noted):

| Frame | Δ | D | S | O | L | N (AXI bytes) | λ |
|---|---|---|---|---|---|---|---|
| 1 ch 2 GSa/s 200 ns/div 10k | 160 | 32 | 4000 | 1 | 1228 | 4524 (9048) | 368 |
| 2 ch 1 GSa/s 200 ns/div 10k | 80 | 32 | 2000 | 1 | 678 | 2324 (4648) | 168 |
| 1 ch 1 GSa/s 1 µs/div 10k | 80 | 136 | 10 000 | 2 | 2698 | 10 404 (20 808) | 248 |
| 1 ch 100 MSa/s 5 µs/div 10k | 40 | 160 | 5000 | 12 | 1423 | 5344 (10 688) | 188 |
| 1 ch 100 MSa/s 100 µs/div 100k | 40 | 2536 | 100 000 | 13 | 25 168 | 100 324 (200 648) | 168 |
| 1 ch 2 GSa/s 2 µs/div 10M | 160 | 256 | 40 000 | 1 | 10 208 | 40 444 (80 888) | 288 |
| 1 ch 100 MSa/s 10 µs/div 10k | 40 | 280 | 10 000 | 10 | 2668 | 10 324 (20 648) | 168 |
| 4 ch 500 MSa/s 500 ns/div 10k (I = 2, P = 316, t = 3) | 40 | 72 | 2500 | 0 | 790 | 2811 (5624) | 316 |

**Time axis of a readout** (I = 1, so λ/I = λ; σ = 0). Readout word i (counted from the first word after the 0x0C2 start,
continuing across the §4.4 blocks) is record sample j = Δ·O + c_f·i. Screen sample 0 is buffer index
⌊λ + t + fractional fine value⌋; the point k of a SCPI transfer (`:WAVeform:STARt k`, `11`) is buffer index
λ + t + k (check the edge position against j_trig below). Screen sample k is at time (k − S/2)/R + τ from the
trigger. The trigger instant lies at record sample

  j_trig = Δ·O + λ + t − 2 + S/2 − R·τ = u·(PRE − DELAY) − F − 34 + t

(PRE = 0x006 | 0x028 << 16, DELAY = 0x007 | 0x025 << 16, u = Δ/20, F of `04` §9; exact within the rounding of `04`
§9's v). Example, 1 ch 2 GSa/s 2 µs/div: Δ = 160, S = 40 000, O = 1, λ = 288, t = 6, PRE = 2556 → screen sample 0 =
buffer 292, j_trig = 20 452 = 8·2556 + 4; at 100 MSa/s 10 µs/div 10k (u = 2, F = −30.4, PRE = 2785) j_trig = 5572.
Use the same relation in reverse: the trigger is at buffer index λ + t + S/2 − R·τ (plus the sub-sample fraction of φ_hw + φ_pre, which
it may ignore at ±0.5 sample), whatever the PL's latency constants are, because F already compensates them.

**Long records: piece reads and compression.** M = 5 000 000 samples is the size of one piece (any M works); a
piece (2 bytes per sample) is moved in §4.4 blocks of ≤ 7 000 000 bytes through the 7 MiB udmabuf0 window into PS
memory. Two kinds of stopped-frame read exist:

- **Piece read** (for `:WAVeform:DATA?` and other raw-data requests when the frame's record
  depth D_pts ≥ 2·M, i.e. the piece count Q = max(1, ⌊D_pts / M⌋) ≥ 2; D_pts = the record's points, e.g. 5e7 at
  50M): uncompressed. Frame selection and the window registers (steps 1–8) are programmed once, for piece 0;
  0x0D3 bit 8 and 0x0D5 are not touched (0x0D5 stays 0; 0x0D3 = 0x0001). Piece 0 transfers
  round_up(λ/I + t + 150, 8) + M samples (the lead-in rounded up to 8, then M record samples); every further piece
  q ≥ 1 transfers M samples by continuing the same read stream (only the 0x0D4 pulse and new §4.4 blocks, no
  window registers); each piece is split into §4.4 blocks of ≤ 7 000 000 bytes. Step 10 runs after the last piece.
  The window starts at k = `:WAVeform:STARt` (O of the window arithmetic includes ⌊k/Δ⌋; L is that of k = 0, the
  record length, and is not rewritten when only k changes); piece q covers screen samples k + q·M … k + (q + 1)·M − 1.
  Worked example (1 ch 10M 1 ms/div, Δ = 80, Q = 2, `:WAV:POINt 1000`): STARt 6 000 000 → O = 0x1_2768 = 0x270 + 75 000,
  STARt 0 → O = 0x270, L = 0x26_2670 in both; each transfer read piece 0 (7 000 000 + 3 000 896 bytes), a 0x0D4
  pulse, piece 1 (7 000 000 + 3 000 000 bytes). Also at 1 ch 50M 10 ms/div, POINt 1000: 7 000 000 +
  3 001 200 bytes, a 0x0D4 pulse, 7 000 000 + 3 000 000 bytes; O = 1563, L = 0xBD_BD16 = 12 500 246 (λ/I + t =
  444 … 447). These transfers read two pieces although the 1000 points lie in piece 0; a firmware may read just
  the pieces it needs, in order, from piece 0.
- **Screen-record read** (Q = 1, and every display / measurement read): x = n / M. x ≤ 1: 0x0D5 ← 0, factor 1.
  Otherwise r = ⌊x/2⌋ (the rule for this board): 0x0D5 ← r and c_f = 2r when 0 < r < 0x1000,
  else 0x0D5 ← 0 and c_f = 1 (so compression starts at n ≥ 2·M). The PL then delivers every c_f-th point
  (compression type 0) — the exact PL decimation rule (sample pick vs min/max) is UNVERIFIED, and so is this compressed
  path as a whole. In the window arithmetic above (both kinds of read), n is first rounded down to a multiple of M when
  ⌊n/M⌋ ≥ 2, n is not a multiple of M and the programmed compression rate is 0.

**Sample format in the buffer.** One channel per readout, 16-bit little-endian words in time order, 2 bytes per
sample (12-bit code left-aligned, code << 4, §5). There is no channel interleave (the channel is selected by 0x0C1) and no
ADC-core interleave: at 2 GSa/s the PL merges the two DES halves before the record memory (§5). The layout is the
same for every route (1, 2 or 4 channels), every rate (2 G / 1 G / 500 M or decimated: one word per record sample
at the table rate R) and both acquisition modes: no route-, rate- or mode-dependent unpacking is needed (only
check that the frame's acquisition mode equals the current one). In peak detect the word count per
screen is unchanged (whether consecutive words form max/min pairs, and their order, is UNVERIFIED, §5). With c_f > 1 the words are every c_f-th point (pick vs min/max UNVERIFIED);
the 64-bit marker format of §6.4 is used only by the roll stream.

**Real-time stream** (read mode 0, roll, §6.4): 0x0D3 bits 1:0 = 0; 0x0C9 = 0, 0x0C9 = 1; 0x0CA = 1; 0x0C1 = a;
0x0B1 = a (the same value goes to the channel-enable register; update its software copy used by §3.2 step 7;
whether a is a mask or an index here is UNVERIFIED); then §4.4 blocks. (No 0x115
write: roll starts leave 0x115 alone.) 0x0C2–0x0C6 are not written.

### 4.4 One DMA block (exact)

```
n8 = round_up(bytes, 8)
drain: poll /dev/dmadata_irq with timeout 0 until it reports not ready   # discard a stale completion
AXI DMA (0x8001_0000):
    CR(0x30) &= ~1                  # stop
    CR(0x30) |= 4; sleep 20 µs      # reset
    DA_MSB(0x4C) = 0
    DA(0x48)     = 0x1E80_0000
    CR(0x30) = (CR & ~0x10) | 0x5001   # run, IOC + error interrupts, cyclic BD off
    LENGTH(0x58) = n8
ACQ plane:
    0x0CF = ceil(bytes/8) & 0xFFFF
    0x0D0 = (ceil(bytes/8) >> 16) & 0xFF
    0x0C8 = 0 ; 0x0C8 = 1           # start PL → DMA stream
wait: select() for readability on /dev/dmadata_irq, timeout 3 s
invalidate: make the CPU view of [0, n8) coherent (below)
copy n8 bytes from the start of the udmabuf0 mapping
```

- **Drain before start.** The `dmadata_irq` driver latches one "ready" flag per interrupt and clears it only when
  userspace consumes it; nothing clears it at transfer start. A completion that arrives after a timed-out wait would
  otherwise satisfy the next transfer's wait immediately. Drain it before every transfer (as above).
- **Timeout.** Treat a 3 s timeout as a failed read: reset the AXI DMA (CR bit 2, 20 µs), drain again, and retry
  or report the error.
- **Cache coherency.** `/dev/udmabuf0` opened without `O_SYNC` gives a cacheable mapping; the CPU must invalidate
  its cache lines for the buffer after the device wrote it and before reading. Do one of:
  1. open `/dev/udmabuf0` with `O_RDWR | O_SYNC` (the device tree sets `sync-mode = 1`, so the mapping is then
     uncached) and skip the invalidate; or
  2. keep a cached mapping and, after each completed transfer, write the u-dma-buf (4.x) sync command
     `offset << 32 | size | direction << 2 | 1` as text, `"0x%08X%08X" % (0, round_up(n8, 64) | (2 << 2) | 1)`, to
     `/sys/class/u-dma-buf/udmabuf0/sync_for_cpu`: offset 0, size rounded **up** to the 64-byte cache line (the driver
     masks the size with 0xFFFF_FFF0), direction 2 = DMA_FROM_DEVICE. Write only the string bytes (no NUL or
     padding). Equivalent: set `sync_offset` = 0, `sync_size` = round_up(n8, 64), `sync_direction` = 2, then write
     `1` to `sync_for_cpu` (`14` §7).

  A write to `sync_for_device` (direction 0, or a size rounded down) after the transfer is not a correct
  invalidate for a CPU read.
- The dmadata interrupt (GIC SPI 89) is acknowledged in the kernel by writing 0xFFFF_FFFF to AXI DMA 0x34; userspace
  does nothing. `/dev/dmadata_irq` reports readable (POLLPRI|POLLRDNORM) once per interrupt (`14`).
- The buffer at `0x1E80_0000` is 8 MiB reserved (`rawdata`); map 7 MiB (0x70_0000) of it. Never request a
  block larger than 7 000 000 bytes.
- For digital (MSO) data the length is in 8-sample units: `ceil(bytes/8)` is passed as the byte count to step 6.

## 5. Sample format and record layout

- Analog samples are 16-bit little-endian words, one channel per readout, in time order, holding the 12-bit ADC
  code **left-aligned**: word = code << 4 (bits 3:0 = 0 for a plain code). Mid-screen (0 div) = 0x8000; the
  displayed range ±4 divisions is 0x0800 … 0xF800, i.e. 0x0F00 << 4 / 8 = 7680 word units (480 codes) per division.
  PS processing works in this 16-bit domain throughout (16-bit words, 12-bit ADC): the SCPI encoding (`11` §7.3)
  uses word >> 8 / word − 0x8000 with no shift, math works with centre 0x8000, and a 12-bit PL quantity read by the
  PS (the PL mean engine of `10` §5.2) is converted with << 4 before use. The PL map engine and the resize registers (`02` §4.2) and the GND substitute constant
  0x800 work in the 12-bit domain inside the PL.
- **Screen limits** (the single definition all specs refer to): in the 12-bit display-code domain the 480 screen
  rows cover codes **0x080 (bottom) … 0xF7F (top)**, 8 codes per row, 60 rows = 480 codes per division, centre
  0x800 (8 divisions = 0xF00 codes). 0xF80 (3968) is the first code above the screen and 0x07F the first below. In
  the 16-bit word domain the same range is 0x0800 … 0xF7F0. Other constants near the top (e.g. 0xF70 written to
  MAP 0x1BE at roll stop, `07` §7.8) are register values, not the limit. A replacement converts a
  record word to the ADC code with word >> 4 (bits 3:0 may carry extra resolution in future modes; UNVERIFIED
  whether the PL ever sets them).
- The interleave of the two ADC cores at 2 GSa/s and 1 GSa/s is undone in the PL: the readout of a channel is a
  plain time-ordered sample stream (no core interleave in the record). (UNVERIFIED with data from a unit.)
- Peak detect: the word count per screen is the same as in normal mode (§4.3); whether consecutive words form
  max/min pairs, and their order, is UNVERIFIED. A replacement treats the words as a plain sample stream until
  confirmed.
- The record start (0x09A/0x09B), pre-trigger count (0x006) and trigger delay (0x007/0x025) define where the trigger
  lies in the record; the readout offset of §4.3 is relative to the record start.
- Trigger interpolation: after the frame, the PS reads the trigger-position fine value when 0x019 bit 0 is set
  (`02` §1.3, §4.1; read sequence §4.3 step 4, also §3.7 step 6); it is applied to the horizontal placement of the
  samples (φ, §4.3).

## 6. Acquisition modes

### 6.1 Normal, peak detect; average and ERES

Normal and peak detect are the only acquisition modes on this product; they are selected in the PL by `PEAK_EN`
(`04` §5.2; `DECIM_TYPE` depends only on the rate). `:ACQuire:TYPE AVERage[,n]`, `ERES[,b]` (and `HIRES`) are
**ignored**: the setting is not accepted, the mode stays as it was and no register is written.
No PL register exists for averaging or ERES.

Averaging and ERES are offered as **math operators** on a source channel (`09`), computed on the PS from successive
frames of that channel. Units: the frame's words x[i] (16-bit, §5) are converted to volts-proportional values
v[i] = (x[i] − c_s)·s_s with c_s = the source's centre code (float) and s_s = volts per code of the source (its
V/div × probe/unit factor ÷ codes per division); results are converted back to the math trace's code scale with
y = c_m + a / s_m (c_m, s_m the math trace's centre and volts per code, s_m = V/div of the math trace ÷ codes per
division), then clamped and **truncated**: y > 65535 → 0xFFFF, otherwise the integer part toward zero, and < 0 → 0.
All arithmetic in 32-bit float.
- **Average**, count N: k = frames since restart, k ← min(k + 1, N); k = 1: a[i] = v[i]; 1 < k < N:
  a[i] = ((k − 1)·a[i] + v[i]) · (1/k); k ≥ N: a[i] = α·v[i] + (1 − α)·a[i] with α = 2 / (N + 1). One float
  accumulator per sample. Restart (k ← 0) on any change of the source's frame parameters or of N.
- **ERES**, bits B ∈ {0.5, 1.0, …, 4.0} (enum e = 0 … 7): window W_E = 2^(e+1) samples; output j is the mean of W_E
  consecutive input samples, y[j] = (x[o+j] + … + x[o+j+W_E−1]) / W_E (running sum), converted as above. The offset o,
  the output count and the screen span depend on the source's screen span w = end − start + 1 (the displayed
  sample range of the source) against its point count n:
  - w < 2: o = n − W_E, 1 output point;
  - w > n (fewer samples than screen points): d = ⌊w/n⌋; o = 0; n + 1 − W_E·d output points; screen start/end of
    the result moved inwards by ⌊W_E/2⌋·d;
  - otherwise: d = ⌊n/w⌋, h = ⌊(W_E/2) / d⌋; o = d·h − ⌊W_E/2⌋; n + 1 − 2·d·h output points; screen start/end moved
    inwards by h.
  The result's trigger index is the source's minus ⌊n/2⌋, ⌊W_E·d/2⌋ or d·h respectively (raised by the source's
  point interval until it is not negative). (For w ≥ 2 and d = 1, h = ⌊W_E/2⌋ and o = 0: the plain "n − W_E + 1 outputs, first output W_E/2
  samples after the first input" case.)

A firmware may additionally offer average/ERES as acquisition modes, computed on the PS the same way.

### 6.2 Sequence (segmented) mode

> Optional feature: not needed for a minimum viable firmware (`00` §5).

- Segment count N (1 … the maximum of `04` §8.2 step 6; also written while sequence is off): ACQ 0x018[9:0] ←
  N & 0x3FF, 0x05B[6:0] ← N >> 10 (RMW each); MAP 0x1BB ← N & 0xFFFF, MAP 0x1BC bit 0 ← N bit 16 (RMW).
- Count change while sequence is off: the count writes inside a stop/arm frame (e.g. `COUNt 10`: 0x019 ← 8,
  0x018, MAP 0x1BB, arm with 0x047 = 0x100A).
- Enable: ACQ 0x018 bit 10 ← 1; MAP 0x1BF ← 1 (sequence zone); re-apply the horizontal settings (§3.3), soft reset
  (§3.5) and re-arm (start delay 0). Enabling also switches the trigger sweep from Auto to **Normal**
  (`05` §7: A 0x00A bit 0 ← 0, the sweep-change soft reset, Normal LED); the sweep stays Normal after sequence off.
- **Completion**: when the N segments are captured (0x02F bit 12) the frame is done: stop request (0x019 ← 8), MAP
  0x1BA bit 0 ← 1 (RMW), the stored-record redraw of §3.7 (0x115 ← 1), program the history window (§6.3) with stop
  segment N − 1 (0x0F6) and clear the history pause, direction forward (0x0F8 ← 0x001). Do **not** re-arm: the
  acquisition stays stopped showing the held sequence; RUN starts the next sequence.
- Count change while sequence is on: history pause (0x0F8 ← 0x101), MAP 0x1BA bit 0 ← 0, the count writes
  above, 0x115 ← 0, arm, stop, clear blocks and soft reset (§3.5), arm; at completion as above with 0x0F6 ← N − 1
  (e.g. `COUNt 37`: … MAP 0x1BA ← 1, 0x0F6 ← 0x24, 0x0F8 ← 0x001, stopped).
- STOP while sequence is on: 0x0F8 ← 0x101 (pause), MAP 0x1BA bit 0 ← 0, Stop LED, then the §3.7 redraw with the
  history window start 0x0F5 ← N − 1 (show the last segment; 0x24 for N = 37), 0x115 ← 1.
  RUN while sequence is on: MAP 0x15A pulse, clear block and soft reset, 0x0F8 ← 0x101, 0x115 ← 0, arm (a stop and
  second soft reset/arm follow), then the completion above with 0x0F5 ← 0 and MAP 0x1BA bit 0 ← 1, 0x0F8 ← 0x001.
- Disable: ACQ 0x018 bit 10 ← 0; MAP 0x1BA bit 0 ← 0; MAP 0x1BF ← 0; history pause on (0x0F8 ← 0x101);
  0x0F6 ← 0; §3.3 (start delay bit 12 back on, e.g. 0x047 = 0x100A); re-arm.
- Segment length: 0x027 (+ 0x05B bit 7) is the segment value of `04` §8.2 step 6, written by every timebase setup,
  not by the sequence path. The FSM keeps filling segments after the arm; the start delay is 0 (§3.2 step 1).
- Worked example (CH1+CH2, 200 ns/div): `:ACQ:SEQ:COUN 10` → 0x018 ← 0x00A, MAP 0x1BB ← 0xA; `:ACQ:SEQ ON` → 0x018 ← 0x40A,
  MAP 0x1BF ← 1, bank/map pass, re-arm, MAP 0x1BA ← 1, 0x0F6 ← 9, 0x0F8 ← 0x001; OFF → 0x018 ← 0x00A, MAP 0x1BA ← 0,
  MAP 0x1BF ← 0, 0x0F8 ← 0x101. Also (1 ch): COUNt 37 → 0x018 = 0x25; ON → 0x018 = 0x425, MAP 0x1BF = 1,
  0x047 = 0x00A, MAP 0x1BA = 1, 0x0F6 = 0x24, 0x0F8 = 0x001; COUNt 1500 while on → 0x0F8 = 0x101, MAP 0x1BA = 0,
  0x018 = 0x5DC, 0x05B = 0x1, MAP 0x1BB = 0x5DC, MAP 0x1BA = 1, 0x0F6 = 0x5DB, 0x0F8 = 0x001; OFF → 0x018 = 0x1DC,
  MAP 0x1BA = 0, MAP 0x1BF = 0, 0x0F8 = 0x101, 0x047 = 0x100A, 0x0F6 = 0, then the arm.
- Progress: segment count (§4.2), sequence ready 0x02F bit 12, overflow 0x02F bit 15.
- Readout of segment `k`: segment-select enable 0x018 bit 12 = 1 (RMW); 0x04D = k & 0xFFFF and 0x05B bit 8 = k bit 16;
  read start 0x018 bit 13 pulse; wait 0x0A4 bit 0 (`02` §1.3); then §4.3; finally clear 0x018 bit 12. (0x018[9:0]
  is the segment *count* setting, 0x04D the read index.) Worked example (2 ch, N = 37, `:WAVeform:SEQuence 1,1` then
  `:WAVeform:DATA?`): 0x018 = 0x1425, 0x04D ← 0 (frame 1 = segment index 0), 0x018 = 0x3425/0x1425 pulse, one
  channel pass (0x0C1 = 0), 0x018 ← 0x425, MAP 0x148 ← 1, 0x0F8 ← 0x001. The readout of segment k ≠ 0 is UNVERIFIED.
- Map-side sequence display: MAP 0x1BA–0x1BF (`07`).

### 6.3 History

> Optional feature: not needed for a minimum viable firmware (`00` §5).

History frames are the segments of the record memory kept from normal acquisition. History pause: 0x0F8 bit 8
(set at init, i.e. history recording paused until enabled); playback reads a stored frame with the sequence readout
of §6.2. Frame timestamp: one 48-bit counter = 0x0E8[15:0] << 32 | 0x01D << 16 | 0x01C[15:0] (`02` §4.1), reset by
the 0x0EA pulse; tick = 1 µs (UNVERIFIED; the value is used as microseconds since the reset for the history list
and for the SCPI sequence time stamps). The timestamp
of segment k is read after the segment select and read-start steps of §6.2.

History window (playback by the PL), written in this order:
1. frame interval T (µs, the user's play speed): 0x0F4 ← T & 0xFFFF, 0x10F ← T >> 16;
2. first segment s: 0x0F5 ← s & 0xFFFF, 0x0F7 bit 0 ← s bit 16;
3. last segment e: 0x0F6 ← e & 0xFFFF, 0x0F7 bit 8 ← e bit 16;
4. direction 0x0F8 bit 0 (1 = forward, s ≤ e; as written for sequence capture).

Segment indices are 0-based (N segments → s = 0, e = N − 1). Boot values: 0x0F8 ← 0x100, 0x0F4 ← 1, 0x10F ← 0,
0x0F5, 0x0F7, 0x0F6 ← 0, 0x0F8 ← 0x101. Pause: 0x0F8 bit 8 ← 1, then wait for the map engine (ACQ 0x0FD bit 7 = 0,
§7) and the draw-stop handshake; resume: bit 8 ← 0. Segment being shown / played: 0x0F9 | (0x0FA bit 0) << 16
(while history or sequence is on; otherwise use segment count − 1). The playback sequence with real segments is
UNVERIFIED.

History commands (2 ch, run mode, Normal sweep):
- **ON**: stop request (0x019 ← 8); Stop LED and history LED (MAP 0x16A bit 0 ← 1, `08`); the §3.7 redraw of the
  held frame (stored bank pass, 0x115 ← 1, MAP 0x172/0x174 back to 0); no arm — history is a stopped state, the
  §3.3 "While stopped" rule applies to later setting changes.
- **FRAMe k**: no register writes when no further segments exist (the shown frame selects the segment for the
  readout of §6.2 and the window below; UNVERIFIED).
- **INTERval T**: 0x0F4 ← T in µs at once (1E-3 → 0x3E8; 0x10F ← T >> 16).
- **TIME?**: the timestamp read of the shown segment (0x018 segment select + bit 13 pulse: 0x1025, 0x3025, 0x1025,
  0x025); the reply format is `11`.
- **OFF**: clear blocks and soft reset (§3.5), LED word, the §3.7 redraw with 0x0F4 ← 1 (interval back to its boot
  value), then the arm: run mode resumes.

### 6.4 Roll

> Optional feature: not needed for a minimum viable firmware (`00` §5).

- Used for time/div enum ≥ 25 (50 ms/div) when roll is on (`04` §7 gives the timebase set). The PL fills the record
  continuously and streams the new samples to the PS through the DMA FIFO.
- **Start** (entering roll, or RUN in roll): sequence off (0x018 bit 10 ← 0), trigger enable 0x00A ← 0, the roll
  timebase set of `04` §7 in every bank, MAP 0x120 ← 1 (scan enable), MAP 0x124 ← 0x16 (clear-pixel count), MAP
  0x1B1 ← number of enabled channels and the rest of the roll map set (`07` §7.8); 0x00E bit 12 ← 0 with the type
  code re-evaluated for roll (acquisition sample mode 0, `05` §1/§7: code 15 in Auto, the type's own code in Normal; e.g. Auto
  0x000 → 0x00F, Normal 0x000) — 0x00E keeps these values while rolling and gets bit 12 back only when roll is left
  (`:ACQuire:MODE YT` → 0x1000); wave save
  0x01E ← 0; DMA compression 0x0D5 ← r_roll, then the 0x0D4 pulse 0, 1, 0; scan fast mode 0x024 ← 1 or 0; read mode 0x0D3
  bits 1:0 ← 0, MAP 0x03C ← 0; DMA buffer init 0x0C9 ← 0, 1; DMA buffer enable 0x0CA ← 1. No 0x115 write and no
  stored-record redraw belong to the roll start or to rolling.

  Symbols of this section: P_roll = roll point count of `04` §7 (0x005 · u), n_ch = number of enabled channels,
  r_roll = DMA compression rate written to 0x0D5, c_f = compression factor (2·r_roll, or 1 when r_roll = 0),
  L_roll = payload bytes per channel block, B = stride, A = available bytes.

  Worked values: CH1+CH2 at 100 ms/div → r_roll = 0x18 (compressed stream), 0x024 ← 1; CH1 only at 100 ms/div, 10M
  (P_roll = 1000) → r_roll = 0xC, 0x024 ← 1 (block LENGTH 0x21C0 = 12 groups); CH1 only at 100 ms/div, 10k
  (P_roll = 40) → 0x0D5 and 0x024 stay 0 (uncompressed stream); CH1+CH2 at 200 ms/div, 100k → 0x0D5 ← 4, 0x024
  stays 0; CH1–CH3 there → 0x0D5 ← 6 (so neither the 0x024 value nor r_roll is a function of the channel count
  alone). The full rule choosing r_roll and 0x024 is not known; c_f is multiplied by ⌊P_roll/4000⌋ when
  P_roll ≥ 4000 (UNVERIFIED). A replacement must pick r_roll so that one group (below)
  can ever become available: A is at most 4·0x7FF + 8·0x7FF = 24 564 bytes. r_roll = 0 (uncompressed) is usable
  only while n_ch·8·(2·P_roll + 8) bytes fit that; otherwise (e.g. P_roll = 1000: 16 064 bytes per channel) it uses
  compression with the parser below (r_roll = 12·n_ch matches both P_roll = 1000 values above).
- **Block geometry**: payload L_roll = 2·⌊P_roll / c_f⌋ bytes (16-bit words); with c_f = 1, L_roll ← 4·L_roll if
  P_roll is not a multiple of 4 (rule for this board; P_roll is a multiple of 4 after `04` §7 anyway).
  Stride B = L_roll + 8 when c_f > 1; when c_f = 1, B = L_roll + 8 if 8 | L_roll, else (L_roll & ~7) + 16. Group =
  n_ch·B·8 bytes (eight strides per channel).
- **Poll** (any period shorter than the time to fill the FIFO): available
  bytes A = 4·(ACQ 0x0CB & 0x7FF) + 8·(MAP 0x047 & 0x7FF). If A ≥ group, read len = ⌊A / group⌋·group bytes: one
  §4.4 transfer of len bytes (0x0CF/0x0D0 ← ⌈len/8⌉, AXI LENGTH = len); else read nothing. Worked example: 1 ch,
  10k, 100 ms/div (P_roll = 40, c_f = 1 → L_roll = 80, B = 88, group = 704), A = 0x480·8 = 9216 → LENGTH 0x23C0 =
  13 groups, 0x0CF = 0x478; 2 ch compressed (c_f = 48): LENGTH 0x2400.
- **Stream format, compression on** (0x0D5 ≠ 0): 64-bit little-endian words. A word whose value is < 4 is a channel
  marker: the following words belong to channel = value (0 = CH1) until the next marker; the current channel
  persists across DMA reads. Every other word is one data word of 4 × 16-bit samples, appended to that channel's
  ring buffer (e.g. 20 000 000 bytes per channel; when it wraps the buffer is full and the oldest data is
  overwritten). Which of the 4 samples are max/min (or time order) is UNVERIFIED.
- **Stream format, compression off** (0x0D5 = 0): blocks of stride B: an 8-byte header (byte 0 = channel index 0–3,
  other bytes unused) followed by L_roll sample bytes (16-bit words as §5); the channels' blocks alternate (slot k
  of every n_ch-block group). On the first read after a start skip 8-byte words until one whose byte 0 is < 4.
  (Layout UNVERIFIED against a PL.)
- Hand the collected data to the display (`07`) at any convenient rate.
- **Stop / exit**: 0x0CA ← 0 (DMA buffer off), 0x024 ← 0, 0x0D5 re-set for normal readout (§4.3); leaving roll
  re-applies the normal timebase (§3.3) with 0x010 bit 15 ← 1.
- Triggered roll ("roll stop on trigger") reports via 0x019 bits 0 and 9 both set.

### 6.5 Zoom

> Optional feature: not needed for a minimum viable firmware (`00` §5).

Zoom uses the same record. On: ACQ 0x089 ← 1, MAP 0x10A ← 1 and the split of `07` §7.4. After the main banks (§3.3),
for each model channel n = 0 … C − 1 configuration bank 4 + n is written (bank protocol of `02` §4.3) with the zoom
timebase set (`04` §11) — the main-timebase registers of `04` §8.2 recomputed for the zoom window — the zoom record
start (`04` §11), the vertical resize with the zoom gain/centre of channel n, and the wave-search set with (main,
zoom) time bases; then MAP sets 4 + n (`07` §7.8 zoom part) and the zoom DPO triple (`07` §7.9). A zoom delay change
rewrites only the record start (0x09A/0x09B/0x08C) and resize in banks 4 … 4 + C − 1. Off: 0x089 ← 0, MAP 0x10A ← 0.
Zoom on/off issue **no** picture-clear block (`07` §7.2). Zoom readout requests use channel numbers 0–3. Zoom banks keep the main
trigger position 0x006/0x007/0x025/0x028 (not rewritten in banks 4 … 7).

## 7. Timing summary

| Item | Value |
|---|---|
| Poll primitive | raw read + ≥ 1 µs sleep, ≤ N+1 reads, timeout = log and continue (`02` §1.3) |
| Arm ready poll (0x021 bit 0) | 300 000 |
| Frame done poll (0x019 bit 9) | 30 000 |
| Map ready poll (ACQ 0x0FD bit 7 = 0) | 5 002 |
| Bank commit pulse width | 10 µs |
| AXI DMA reset settle | 20 µs |
| DMA completion wait | `select()` 3 s |
| DMA block size | ≤ 7 000 000 bytes |
| Offset-DAC init waits | 2 ms before and after zeroing |
| ADC PLL sync pulse | 10 ms |
| Scope-ID poll | ≤ 1000 × 1 ms |
| Frame-ready check | one read of MAP 0x082 every 10…220 ms while armed (§3.6) |

## Open

- Trigger fine value φ (§4.3): φ_hw is known; the clock-period factor c_pre of φ_pre (decimating) and the
  constant φ_0 = 2 are not, and need real trigger data with non-zero 0x0AA/0x0A6 (the window is generous enough
  that an error of a few samples only moves the lead-in; fallback φ = 6).
- Stored-record redraw (§3.7, `04` §12): the source of the record's horizontal remainder ρ (the "macro/micro"
  fine position of the frame) is UNVERIFIED (`04` §12); so is the compressed screen-record
  read (§4.3, n ≥ 1e7 with Q = 1).
- The left-aligned word format (§5) is UNVERIFIED; a raw DMA dump from a unit would confirm
  it and show whether bits 3:0 are ever non-zero.
- Whether a stop request mid-frame abandons the frame or completes it is unknown (needs a unit).
- Peak-detect sample-pair order and the exact volts-per-word scaling (vertical calibration) need captured data.
- Whether the FSM stops by itself after one frame while channels are enabled (no stop bit is set at arm; A 0x00E
  bit 12 = 0 in Single, 1 in Auto/Normal) or keeps re-triggering until stopped needs a unit (the firmware issues
  the stop request itself after a Single frame, §3.6).
- Which settings changes set the "delayed acquisition" flag (start delay 250, §3.2) is not fully known.
- In the real-time stream setup (§4.3) the same argument goes to 0x0C1 and 0x0B1; whether it is a channel index or a
  mask there is not known.
- Roll (§6.4): the rule choosing the compression rate r_roll and scan fast mode 0x024 (four worked values in §6.4, r_roll = 12·n_ch fits
  the two P_roll = 1000 ones); the meaning of the 4 samples of a
  compressed data word; the stream layout against a real PL.
- Whether the PL uses 0x006/0x007 of banks 4 … 7 at all (they keep the main values, §6.5) is unknown.
- Frame timestamp (§6.3): the 1 µs tick is assumed; the PL counter rate needs a unit.
- History playback (§6.3) and segment-k readout (§6.2) with real segments are UNVERIFIED.
- Address range of the record memory in PS DDR4 (§1; `02` Open, `12` §4).
