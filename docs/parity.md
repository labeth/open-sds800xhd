# Parity with the SDS1000CML+ 1.2 release

**Verified in simulation; no unit.** Every result below comes from the tier-1 simulator of the private
reference workspace (the unit's kernel and boot scripts under emulation against a model of the board), from
the open PL's co-simulation, or from host tests. Nothing here has run on an instrument.

The reference is the SDS1000CML+ 1.2 regression checklist (its bench pass before the 1.2 release), grouped as the
family's parity bar groups it: workspaces and views, web, triggers, protocols, records/streams/SOUND, front end,
system, and tests. Each row names the CML item, what covers it on the SDS800X HD, the result of one run of every
area on main, and a verdict:

- **PASS**: covered, and the run passed;
- **PARTIAL**: covered in part; the row says what is missing;
- **N/A**: the HD lacks the hardware; the row cites the reason the board reports in its caps (`Profile.Absent`);
- **GAP**: missing work, or a failure that is not a test problem; ranked at the end with a plan.

## Summary

| Verdict | Rows |
|---|---|
| PASS | 18 |
| PARTIAL | 12 |
| N/A | 1 |
| GAP | 9 |
| **Total** | **40** |

The HD meets the CML's 1.2 bar where the bar is about the hardware path: trigger programming and firing for every
type the board has, the front end against its model, records, roll, mask, calibration, SCPI, the install stick and
A/B updates, faults the unit can meet, the diagnostics census, and the RTL of the open PL. It falls short where the
CML's bar is a sweep over the whole product: every key, every sheet row, every bus, the operator workflows, the
phone remote, the BUS Visual view and SOUND. Those sweeps exist on the CML's bench and not yet in the HD's simulator lanes, and the
protocol sweep fails on about one bus in five.

Where the results come from:

| Lane | Command | Result |
|---|---|---|
| Simulator areas | `make sim-test` (or `./build/simtest -area A,B`) | 28 areas: 26 pass; `decode` and `decode-sweep` fail cases |
| Fault lane (L4) | `make l4-test` | 8 areas: 6 pass; `l4-fuzz` 17/20 seeds, `l4-storage` 17/18 |
| Open-PL co-simulation | `make cosim-test` | 7 areas: 6 pass; `scpi` 19/20 |
| Browser e2e over simlab | `make simlab-e2e` | simlab 11/11 steps; operator 35 pass, 12 fail, 58 skipped with a reason; fuzz 40 actions, 0 findings |
| Recorded sessions, native vs WebAssembly | `make sessions TARGET=pair` | 22 sessions × 2: 58/58 steps, 45/45 expectations; 364 fields match, 0 differ |
| Browser demo matrix | open-dso-core `make demo-test` (HD half) | 19/19 |
| Unit tests | `make test` | 26 packages ok |
| RTL | `make fpga-sim`, `make fpga-lint` | 10/10 testbenches; 35 modules lint-clean |
| Core, shared with the CML | open-dso-core `go test ./render -run TestGolden`, `./decode -run TestJSDecoderParity`, `SDS_AUTO_FUZZ=6000 … TestAutodetectFuzzCampaign` | 86 golden scenes; 1432/1432 Go↔JS vectors; 6000 mutations, 1 finding |

One full run of every simulator, fault and co-simulation area takes about eight hours of wall time on the shared
simulator lock (the decode sweep alone about four and a half).

## Workspaces and views

| CML 1.2 item | SDS800X HD coverage | Result | Verdict |
|---|---|---|---|
| Keys sweep: every key, knob and view alive (56) | `input` (in-guest: profile of 33 keys, 12 knobs, 19 lamps; injected events reach the reader), `ui` (keys, knobs and touch drive the UI model, SCPI and the web agree), `l4-input` (stuck, chattering, spinning input), `lab` (simlab panel), sessions `workspace-tour-keys`, `vdiv-knob`, `timebase-knob` | input 9/9, ui 40/40, l4-input 20/20, lab 10/10, sessions 6/6 + 2/2 + 3/3 | PARTIAL: no sweep presses every one of the 33 keys and turns every one of the 12 knobs in every workspace and checks it acted; simlab-e2e still sees its known panel findings (knob sign opposite the spec for V/div and s/div, every displayed channel's key lit, H-scale turns the sheet tab while a channel sheet is open) |
| Sheets sweep: every tab and row (152) | `ui` (`menu-sheet`, `sheet-tab`, channel sheets C3/C4, Bode, X-Y, SOUND and mask rows), sessions `sheets-keys` | ui 40/40 (its `sheets` check lists no rows), sessions 5/5 | GAP: no row-by-row sweep of every sheet |
| HUD actions (refs, mask, force, print, default, autoset) | `ui`, `display` (UI layer, held redraw, zoom, screen.png = fb0), `mask`, sessions `default-setup`, `scope-autoset`, `run-stop-single`, `trigger-mode` | ui 40/40, display 96/96, mask 10/10, sessions 22/22 | PARTIAL: the actions run on the simulated unit one by one, not as one HUD action sweep; the sessions run against the browser demo's board |
| Workspaces SCOPE, BUS, SPECTRUM, MEASURE, COMPARE and their views | sessions `workspace-tour-web`, `bus-views`, `spectrum-views`; demo matrix `workspace-tdiv`, `bus-views`, `spectrum-fft-sound`, `compare-master-mask`; `ui` X-Y and Bode views | sessions 5/5, 3/3, 3/3; demo 19/19; ui 40/40 | PASS |

## Web

| CML 1.2 item | SDS800X HD coverage | Result | Verdict |
|---|---|---|---|
| Web sweep, 0 page errors; browser and node suites | `web` (page, caps, frames v2, settings both ways, SCPI and the web agree, headless browser with 0 page errors), simlab-e2e `simlab` (bench page, panel, generator, CH keys and knobs read back over SCPI, the UI model, the web and fb0; UART/SPI/I²C presets decode) and `fuzz` (40 actions) | web 45/45, simlab 11/11, fuzz 1/1 (0 findings) | PASS |
| Operator workflows (the core's `webclient/e2e`, as the CML's web actions) | simlab-e2e `operator`: every workflow the simulator's sources can serve | 35 pass, 12 fail, 58 skipped with a reason (protocol sources start idle at the trigger point; zones not in the caps) | GAP: see the ranked list |
| Phone remote sweep | none: the page offers the remote, no lane loads it | — | GAP: no phone-remote sweep |
| Visual view streams (0.2 s, no losses) | none on the simulated unit | — | GAP: the BUS Visual and Stream views are not exercised over a stream |

## Triggers

| CML 1.2 item | SDS800X HD coverage | Result | Verdict |
|---|---|---|---|
| Edge: NORM rise/fall, SINGLE, placement ≤ 1 column, sweep modes, force, holdoff | `trigfire` (the device model's trigger fires on the signal and not on its near miss; trigger at the analytic instant, through the HAL, SCPI and the web), `trigger` (register goldens and reference parity, every type set from SCPI and the web), co-sim `trigger` (edge position to 1/16 sample) | trigfire 92/92, trigger 207/207, co-sim trigger 93/93 | PARTIAL: edge cases run at one timebase; the CML's 1 µs…50 ms/div ladder on both sources is not swept |
| Pulse width | `trigfire` pulse rows (7), co-sim `trigger` | pass | PASS |
| Zones | `mask` (the core's software mask on every frame; SCPI and the web agree) | mask 10/10 | GAP: the caps offer no zone trigger and give no reason |
| Slope | `trigfire` slope rows (3: fires inside the limit, waits outside) | pass | PASS |
| Video | `trigfire` video rows (2: NTSC any line fires on 4.7 µs syncs, not on 100 ns pulses) | pass | PARTIAL: PAL and line selection are not checked |
| Serial / frame-start triggers, AUTO arms the bus's trigger | `trigfire` serial rows (UART 3, I²C 4, SPI 2, CAN 2, LIN 2), co-sim `proto` (SPI and I²C protocol triggers on generated buses), demo `bus-auto-*` (frame-start trigger per bus) | pass; co-sim proto 12/12 | PARTIAL: the board's protocol node serves UART, I²C, SPI, CAN and LIN; the other buses decode in software on an edge trigger |
| Types the CML lacks (runt, window, pattern, setup/hold, dropout, interval, Nth, delay, qualified) | `trigfire`, `trigger`, co-sim `trigger` | pass | PASS |

## Protocols

| CML 1.2 item | SDS800X HD coverage | Result | Verdict |
|---|---|---|---|
| Bus sweep: every bus named by AUTO in BUS and decoded | `decode` (each of the 52 protocols' first variant through the realistic front end, AUTO, then the decode against the generator's truth), `decode-sweep` (all 92 variants) | decode 40/51, decode-sweep 71/91 (one skip each: a Morse unit longer than AUTO's slowest look) | GAP: see the ranked list; about half of the failures move between runs (record windows under the emulator), the rest repeat |
| AUTO fuzz (software) | open-dso-core `TestAutodetectFuzzCampaign`, 6000 mutations of 151 captures (shared code) | 1 finding (Modbus capture named SDQ under heavy noise); the CML's 1.2 run had 3 | PASS |

## Records, streams, SOUND

| CML 1.2 item | SDS800X HD coverage | Result | Verdict |
|---|---|---|---|
| SINGLE and STOP give full records | `readout` (windows, zoom, compression, pieces), `peak`, `segments`, co-sim `readout` (nth/min-max equal to software, decimation, segments), sessions `run-stop-single` | readout 33/33, peak 23/23, segments 19/19, co-sim readout 46/46, sessions 5/5 | PARTIAL: no SINGLE sweep over the timebases through the app checking every record is full |
| record.bin download | none | — | GAP: the record download is not checked |
| Stacking | none on the factory PL; co-sim `stacker` on the open PL; software super-resolution in the demo matrix | co-sim stacker 7/7, demo `superres` pass | N/A: "the vendor bitstream has no hardware stacker" |
| Stream starts | `roll` (roll through the Streamer), co-sim `stream` (gap-free stream; an injected stall drops whole blocks, counted) | roll 50/50, co-sim stream 10/10 | PASS |
| SOUND starts without losses | `ui` `sound-rows-c3-c4` (SOUND's channel rows offer every channel) | 1 check | GAP: SOUND is never started over the simulated stream |

## Front end

| CML 1.2 item | SDS800X HD coverage | Result | Verdict |
|---|---|---|---|
| V/div, coupling, probe sweep with refusals | `frontend` (ladder × coupling × BW × probe, refusals, reference SPI parity), `afe` (ladder, offset, couplings, a clipped top, BW-limit rise times and a 10x probe read back over SCPI against the front-end model), `chpair` | frontend 532/532, afe 27/27, chpair 10/10 | PASS |
| Calibration | `calibration` (factory DAC and self-cal files recalled), `selfcal` (self-cal end to end, file on p12) | 10/10, 20/20 | PASS |
| Hardware AUTO fuzz from random states | `l4-fuzz` (the core's random operator on the web UI, 20 seeds × 60 actions, 4 channels, 1024×600) | 17/20 seeds clean; 3 frame stalls | PARTIAL: no AUTO-in-BUS fuzz from random scope states; three fuzz seeds stall frames (see the ranked list) |

## System

| CML 1.2 item | SDS800X HD coverage | Result | Verdict |
|---|---|---|---|
| Restart rows: settings survive a restart (74/75) | `restart` (timebase and two channel scales set over SCPI survive a restart on the kept p12), `storage` (settings store on p12) | restart 10/10, storage 15/15 | PARTIAL: three settings, not every row |
| OTA A/B: update, rollback, agent update, activate | `ota` (install layout, health confirm, rollback of a dying update, disable switch, Acquire-key boot), `l4-update` (damaged images, power cuts during an update), `l4-install` (power cuts during the stick's copy, re-run), `l4-reboot`, `l4-guard` (the write guard) | ota 17/17, l4-update 25/25, l4-install 19/19, l4-reboot 6/6, l4-guard 11/11 | PASS |
| SCPI: IDN, TDIV, VDIV, ATTN, TRMD, PAVA, SAST, waveform | `scpi` (commons, status, tree and legacy forms, measurements, waveform transfer), `trigger` SCPI forms | scpi 20/20 | PASS |
| SCPI over the open PL | co-sim `scpi` | 19/20: `:WAV:DATA?` decodes −0.994…3.006 V for a ±1 V square, in two of two runs | GAP |
| Stall watch: no status call over 4 s | `l4-acq` (web status and `*IDN?` answer while each fault is on: 6–31 ms) | l4-acq 25/25 | PARTIAL: answers are timed during faults, not watched over a whole session (BUS search, AUTO) |
| Faults: storage | `l4-storage` (full p12, read-only p12) | 17/18 | PARTIAL: on a read-only p12 the host's check after the settings saver found the app not answering; the guest script reboots into the Acquire-key boot meanwhile (see the ranked list) |
| Faults: acquisition and input | `l4-acq` (DMA timeout and error, lost interrupt, clock lock loss, PL reload), `l4-input` | 25/25, 20/20 | PASS |
| Diagnostics stick (read-only census) | `census` | 12/12 | PASS |
| COMPARE master and mask | `mask` (create, pass, fail, stop on fail; SCPI and the web agree), demo `compare-master-mask` | 10/10, pass | PASS |
| Start-up and identity | `startup` (start-up registers and SPI against the reference), `platform`, `conformance` (the core's HAL conformance suite), co-sim `startup` | 15/15, 11/11, 22/22, 9/9 | PASS |

## Tests

| CML 1.2 item | SDS800X HD coverage | Result | Verdict |
|---|---|---|---|
| Unit tests | `make test` (app, ota, sim) | 26 packages ok | PASS |
| Golden display | open-dso-core `TestGolden1024x600`, `TestGolden4Ch12Bit` (this screen size and format), `display` (screen.png equals fb0) | 86 golden scenes pass; display 96/96 | PASS |
| Parity suites | sessions pair (native vs WebAssembly), demo matrix, Go↔JS decoder parity, `startup` and `trigger` reference parity | 364/364 fields; 19/19; 1432/1432 vectors; 15/15; 207/207 | PASS |
| Fuzz | `l4-fuzz`, the AUTO fuzz campaign | 17/20 seeds; 1 finding in 6000 | PARTIAL: see the fuzz stalls |
| RTL | `make fpga-sim`, `make fpga-lint`, `make cosim-test` | 10/10 testbenches, 35 modules clean, co-sim 196/197 | PARTIAL: the co-sim `:WAV:DATA?` failure |

## GAP rows, ranked by importance for parity

1. **Bus sweep failures** (`decode` 11, `decode-sweep` 20). Repeating in both areas and in an earlier run:
   `uart/baud=9600` (AUTO names SENT, or decodes the wrong bytes), `stepper` (AUTO names SDQ), `caliper`
   (record flat on the clock), `dcf77/second_ms=3` (no minute in the record), `quad/period_us=100` (all
   zero), `dmx512` (named UART, or no break in the record). The others (I²C 100 kHz, SDQ, SD card, USB
   low/full speed, t55xx, EM4305, MDIO, OOK, Qi, SENT 3 µs, USB PD, 1-Wire overdrive, Modbus 9600 8N2,
   DSI, EM4100) failed in one run and passed in another. Repro: `make sim-test AREA=decode CASES=uart`.
   Plan: per repeating bus, keep the record and compare AUTO's timebase and trigger choice with the CML's on
   the same synth bus; fix the record window (frame-start trigger, timebase) in the core's AUTO; then run
   each moving case three times and grade on the majority.
2. **Operator workflows on the simulated web UI** (12 of 47 run fail; a second run of eight of them
   repeated six). Repeating: `B2` and the other burst-source workflows (`B8`, `B10`–`B12`: autoset never
   finishes on the burst source), `M4` (the page keeps `#ttype` hidden although the caps offer pulse),
   `T25` (the mask test reads Off after it is enabled; the SCPI `mask` area passes), `T18` (super-resolution
   never stacks; the board has no stacker and the workflow is not skipped by the caps), `T5`/`T23` (the
   SPECTRUM peak table: a wrong fundamental, no 3rd harmonic). `B7` and `M2` passed on the second run.
   Repro: `WORKFLOWS=B2,M4,T25 make simlab-e2e SUITES=operator`. Plan: M4 and T25 are page/caps findings
   for the core's web client; T18 needs a caps skip or the software stacker on this board; the burst
   autoset and the spectrum readings need a run with the app's log at each step to separate emulator speed
   from the app.
3. **Sheets sweep.** Plan: port the CML's sheets sweep (every tab and row through the LCD path) as a `ui`
   host step over the UI model's sheet catalogue, with the HD's channel tabs.
4. **SOUND and the BUS Visual/Stream views over a stream.** Plan: an app area that starts SOUND and the
   Visual and Stream views on the simulated roll stream (a MIDI or tone source from the generator), and
   counts starts, messages and losses as the CML's `sndcycle` and `vis_hw` do.
5. **Co-sim `:WAV:DATA?` on the open PL** (two of two runs: part of a ±1 V square reads 2 V high). The
   factory-PL `scpi` area passes the same check. Repro: `make cosim-test AREA=scpi`. Plan: dump the
   descriptor and the words around the jump, and check the open-PL readout path against the DMA error-bit
   and `Request.Need` changes.
6. **Phone remote and record.bin.** Plan: load `/remote` in the `web` area's headless browser (sweep its
   rows, 0 page errors) and fetch `record.bin` after a SINGLE, checking its length and header against the
   record.
7. **Zone trigger.** The caps list no zone trigger and no reason. Plan: decide whether the factory PL has
   one (spec), then offer it or add a reason to `Profile.Absent`; the software mask covers COMPARE meanwhile.

PARTIAL rows with failures, for the same follow-up:

- **`l4-fuzz` frame stalls** (seeds 10, 12, 20: a timebase change, a C2 coupling change and a trigger
  level each left `frame.seq` unchanged for 5 s while running, at 2 frames/s under the emulator). Repro:
  `make l4-test AREA=l4-fuzz L4_FUZZ_SEEDS=10` (`DEBUG_AT=6`). Plan: replay each seed with diagnostics and
  separate the emulator's frame rate from a stuck acquisition; widen the invariant's window only for the
  former.
- **`l4-storage` read-only p12** (the host's `serving` check after the settings saver failed in 1 ms while
  the guest had moved on to its later boots). Repro: `make l4-test AREA=l4-storage`. Plan: have the host
  check before the guest's next boot (a guest marker), then decide from that run whether the app or the test
  is at fault.
