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
| PASS | 26 |
| PARTIAL | 10 |
| N/A | 2 |
| GAP | 2 |
| **Total** | **40** |

The HD meets the CML's 1.2 bar where the bar is about the hardware path: trigger programming and firing for every
type the board has, the front end against its model, records, roll, mask, calibration, SCPI, the install stick and
A/B updates, faults the unit can meet, the diagnostics census, and the RTL of the open PL. It falls short where the
CML's bar is a sweep over the whole product: every key, every bus, SOUND and the BUS Stream view. The sheets,
the operator workflows, the phone remote, record.bin and the BUS Visual view now have sweeps in the HD's simulator
lanes; SOUND and the Stream view are not built on the HD (the core's engine has no consumer of the board's
stream), and the protocol sweep fails on about one bus in five.

Where the results come from:

| Lane | Command | Result |
|---|---|---|
| Simulator areas | `make sim-test` (or `./build/simtest -area A,B`) | 30 areas (with `sheets` and `streamviews`): 28 pass; `decode` and `decode-sweep` fail cases. Rerun after these changes: sheets 36/36, streamviews 14/14, ui 67/67, web 64/64, trigfire 92/92, trigger 207/207, display 96/96, readout 33/33, mask 10/10, afe 27/27, roll 50/50 |
| Fault lane (L4) | `make l4-test` | 8 areas: `l4-storage` 18/18; `l4-fuzz` seeds 10, 12 and 20 replayed clean (3/3) with the emulator's 15 s stall window; the other areas from the earlier run |
| Open-PL co-simulation | `make cosim-test` | 7 areas pass; `scpi` 20/20 since the open image offers no vendor renderer (a held redraw polled a register the open PL never sets and stalled the owner) |
| Browser e2e over simlab | `make simlab-e2e` | simlab 11/11 steps; operator 43 pass, 2 fail, 60 skipped with a reason, then T25 (after the reference fix) and M10 pass on rerun; fuzz 40 actions: 1 finding under load 11 (a 6 s click timeout on super-resolution's ARM), 0 on rerun at load 3 |
| Recorded sessions, native vs WebAssembly | `make sessions TARGET=pair` | 22 sessions × 2: 58/58 steps, 45/45 expectations; 364 fields match, 0 differ |
| Browser demo matrix | open-dso-core `make demo-test` | HD 22/22, CML 22/22 |
| Unit tests | `make test` | 26 packages ok |
| RTL | `make fpga-sim`, `make fpga-lint` | 10/10 testbenches; 35 modules lint-clean |
| Core, shared with the CML | open-dso-core `go test ./render -run TestGolden`, `./decode -run TestJSDecoderParity`, `SDS_AUTO_FUZZ=6000 … TestAutodetectFuzzCampaign` | 86 golden scenes; 1432/1432 Go↔JS vectors; 6000 mutations, 1 finding |

One full run of every simulator, fault and co-simulation area takes about eight hours of wall time on the shared
simulator lock (the decode sweep alone about four and a half).

## Workspaces and views

| CML 1.2 item | SDS800X HD coverage | Result | Verdict |
|---|---|---|---|
| Keys sweep: every key, knob and view alive (56) | `input` (in-guest: profile of 33 keys, 12 knobs, 19 lamps; injected events reach the reader), `ui` (keys, knobs and touch drive the UI model, SCPI and the web agree), `l4-input` (stuck, chattering, spinning input), `lab` (simlab panel), sessions `workspace-tour-keys`, `vdiv-knob`, `timebase-knob` | input 9/9, ui 67/67, l4-input 20/20, lab 10/10, sessions 6/6 + 2/2 + 3/3 | PARTIAL: no sweep presses every one of the 33 keys and turns every one of the 12 knobs in every workspace and checks it acted; simlab-e2e still sees its known panel findings (knob sign opposite the spec for V/div and s/div, every displayed channel's key lit, H-scale turns the sheet tab while a channel sheet is open) |
| Sheets sweep: every tab and row (152) | `sheets` (every row of the UI model's sheet catalogue changed and read back through the touch layout and through `/api/sheets`, held 0.8 s, restored; four runs by tab group), `ui`, sessions `sheets-keys` | sheets 190 rows × 2 paths: 166 pass, 23 flagged with the scope's reason, 1 skipped (`sys.default`), 0 failed; ui 67/67 | PASS |
| HUD actions (refs, mask, force, print, default, autoset) | `ui`, `display` (UI layer, held redraw, zoom, screen.png = fb0), `mask`, sessions `default-setup`, `scope-autoset`, `run-stop-single`, `trigger-mode` | ui 67/67, display 96/96, mask 10/10, sessions 22/22 | PARTIAL: the actions run on the simulated unit one by one, not as one HUD action sweep; the sessions run against the browser demo's board |
| Workspaces SCOPE, BUS, SPECTRUM, MEASURE, COMPARE and their views | sessions `workspace-tour-web`, `bus-views`, `spectrum-views`; demo matrix `workspace-tdiv`, `bus-views`, `spectrum-fft-sound`, `compare-master-mask`; `ui` X-Y and Bode views | sessions 5/5, 3/3, 3/3; demo 22/22; ui 67/67 | PASS |

## Web

| CML 1.2 item | SDS800X HD coverage | Result | Verdict |
|---|---|---|---|
| Web sweep, 0 page errors; browser and node suites | `web` (page, caps, frames v2, settings both ways, SCPI and the web agree, headless browser with 0 page errors), simlab-e2e `simlab` (bench page, panel, generator, CH keys and knobs read back over SCPI, the UI model, the web and fb0; UART/SPI/I²C presets decode) and `fuzz` (40 actions) | web 64/64, simlab 11/11, fuzz 1/1 (0 findings on the quiet rerun) | PASS |
| Operator workflows (the core's `webclient/e2e`, as the CML's web actions) | simlab-e2e `operator`: every workflow the simulator's sources can serve | 45 pass (T25 and M10 on rerun), 60 skipped with a reason (protocol sources start idle at the trigger point; zones absent with the board's reason; the burst's spectrum at 2 ns/div holds the screen's 20 samples) | PASS |
| Phone remote sweep | `web` (`/remote` in the headless browser at a phone's size: every Menus tab, each row's sheet opened and closed, safe values changed from the phone and followed by the scope, the steppers) | 14 tabs, 89 rows, 6 changes and 2 steppers followed, 0 page errors | PASS |
| Visual view streams (0.2 s, no losses) | `streamviews` (a MIDI bus on C1: BUS Visual on the scope and on the page, captures and messages counted; the Stream view and SOUND started) | Visual from captures: 13 captures in 20 s all decoded, 117 messages, first 0.31 s; page 72 messages, first 0.15 s, 0 page errors | PARTIAL: Visual runs from captures; the Stream view has no stream on the HD (the core's engine has no consumer of `hal.Streamer`), and every surface says so |

## Triggers

| CML 1.2 item | SDS800X HD coverage | Result | Verdict |
|---|---|---|---|
| Edge: NORM rise/fall, SINGLE, placement ≤ 1 column, sweep modes, force, holdoff | `trigfire` (the device model's trigger fires on the signal and not on its near miss; trigger at the analytic instant, through the HAL, SCPI and the web), `trigger` (register goldens and reference parity, every type set from SCPI and the web), co-sim `trigger` (edge position to 1/16 sample) | trigfire 92/92, trigger 207/207, co-sim trigger 93/93 | PARTIAL: edge cases run at one timebase; the CML's 1 µs…50 ms/div ladder on both sources is not swept |
| Pulse width | `trigfire` pulse rows (7), co-sim `trigger` | pass | PASS |
| Zones | `mask` (the core's software mask on every frame; SCPI and the web agree) | mask 10/10 | N/A: "the vendor bitstream has no zone qualifier: the SDS800X HD offers no zone trigger (spec 05 §1, §10.7)"; the open image without its zone engine says "this open image has no zone engine (spec 20 §7.8)" |
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
| record.bin download | `web` (`/api/record.bin` while running, against the raw record and the caps) | 1 µs/div: 20210 bytes, 10000 samples × 2 channels, interval, timebase and volts per code as the caps say, −1…1 V and −0.5…0.5 V as injected; while stopped it refuses with its reason (it serves a capture made after the request) | PASS |
| Stacking | none on the factory PL; co-sim `stacker` on the open PL; software super-resolution in the demo matrix and simlab-e2e `T18` | co-sim stacker 7/7, demo `superres` pass, T18 pass | N/A: "the vendor bitstream has no hardware stacker" (the page's software stacker runs on the HD) |
| Stream starts | `roll` (roll through the Streamer), co-sim `stream` (gap-free stream; an injected stall drops whole blocks, counted) | roll 50/50, co-sim stream 10/10 | PASS |
| SOUND starts without losses | `streamviews` (SOUND's view reached, the caps, `/api/sound.bin` and the page's SOUND tab), `ui` `sound-rows-c3-c4` | the view is reached; caps, endpoint and page all say "this scope's engine does not run it yet" | GAP: SOUND is not built on the HD: the core's engine has no consumer of the board's stream (`hal.Streamer`); new engine work |

## Front end

| CML 1.2 item | SDS800X HD coverage | Result | Verdict |
|---|---|---|---|
| V/div, coupling, probe sweep with refusals | `frontend` (ladder × coupling × BW × probe, refusals, reference SPI parity), `afe` (ladder, offset, couplings, a clipped top, BW-limit rise times and a 10x probe read back over SCPI against the front-end model), `chpair` | frontend 532/532, afe 27/27, chpair 10/10 | PASS |
| Calibration | `calibration` (factory DAC and self-cal files recalled), `selfcal` (self-cal end to end, file on p12) | 10/10, 20/20 | PASS |
| Hardware AUTO fuzz from random states | `l4-fuzz` (the core's random operator on the web UI, 20 seeds × 60 actions, 4 channels, 1024×600) | seeds 10, 12, 20 replayed clean; the earlier stalls were the emulator's frame period at 50 ms/div (frames every 4–7 s, the app publishing throughout) | PARTIAL: no AUTO-in-BUS fuzz from random scope states |

## System

| CML 1.2 item | SDS800X HD coverage | Result | Verdict |
|---|---|---|---|
| Restart rows: settings survive a restart (74/75) | `restart` (timebase and two channel scales set over SCPI survive a restart on the kept p12), `storage` (settings store on p12) | restart 10/10, storage 15/15 | PARTIAL: three settings, not every row |
| OTA A/B: update, rollback, agent update, activate | `ota` (install layout, health confirm, rollback of a dying update, disable switch, Acquire-key boot), `l4-update` (damaged images, power cuts during an update), `l4-install` (power cuts during the stick's copy, re-run), `l4-reboot`, `l4-guard` (the write guard) | ota 17/17, l4-update 25/25, l4-install 19/19, l4-reboot 6/6, l4-guard 11/11 | PASS |
| SCPI: IDN, TDIV, VDIV, ATTN, TRMD, PAVA, SAST, waveform | `scpi` (commons, status, tree and legacy forms, measurements, waveform transfer), `trigger` SCPI forms | scpi 20/20 | PASS |
| SCPI over the open PL | co-sim `scpi` | 20/20 (`:WAV:DATA?` −1.000…1.000 V), twice | PASS |
| Stall watch: no status call over 4 s | `l4-acq` (web status and `*IDN?` answer while each fault is on: 6–31 ms) | l4-acq 25/25 | PARTIAL: answers are timed during faults, not watched over a whole session (BUS search, AUTO) |
| Faults: storage | `l4-storage` (full p12, read-only p12) | 18/18 | PASS |
| Faults: acquisition and input | `l4-acq` (DMA timeout and error, lost interrupt, clock lock loss, PL reload), `l4-input` | 25/25, 20/20 | PASS |
| Diagnostics stick (read-only census) | `census` | 12/12 | PASS |
| COMPARE master and mask | `mask` (create, pass, fail, stop on fail; SCPI and the web agree), demo `compare-master-mask` | 10/10, pass | PASS |
| Start-up and identity | `startup` (start-up registers and SPI against the reference), `platform`, `conformance` (the core's HAL conformance suite), co-sim `startup` | 15/15, 11/11, 22/22, 9/9 | PASS |

## Tests

| CML 1.2 item | SDS800X HD coverage | Result | Verdict |
|---|---|---|---|
| Unit tests | `make test` (app, ota, sim) | 26 packages ok | PASS |
| Golden display | open-dso-core `TestGolden1024x600`, `TestGolden4Ch12Bit` (this screen size and format), `display` (screen.png equals fb0) | 86 golden scenes pass; display 96/96 | PASS |
| Parity suites | sessions pair (native vs WebAssembly), demo matrix, Go↔JS decoder parity, `startup` and `trigger` reference parity | 364/364 fields; 22/22; 1432/1432 vectors; 15/15; 207/207 | PASS |
| Fuzz | `l4-fuzz`, the AUTO fuzz campaign | stalls classified (emulator frame period); replayed seeds 3/3; 1 finding in 6000 | PASS |
| RTL | `make fpga-sim`, `make fpga-lint`, `make cosim-test` | 10/10 testbenches, 35 modules clean, co-sim `scpi` 20/20 | PASS |

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
2. **SOUND and the BUS Stream view.** The HD's board has a streamer (`roll` passes through it), but the core's
   engine reads no stream from it: nothing leases SOUND's samples or feeds the decoded stream. Caps, the
   endpoints and the UI all say so (`streamviews`). Plan: an engine consumer of `hal.Streamer` for SOUND and
   the decoded stream, then `streamviews` counts starts, blocks and losses against its thresholds (half the
   rate over 20 s, no gaps, no losses).

Closed since the first run of this table: the sheets sweep (`sheets`), the phone remote and record.bin
(`web`), BUS Visual from captures (`streamviews`), the zone trigger (absent with the board's reason), the
co-sim `:WAV:DATA?` (the open image no longer offers the vendor renderer), and most operator workflows: the
page drew the HD's traces about 0 V instead of the screen's centre line (the board's scale now names the
centre line and the offset), autoset left a 150 MHz burst at 1 ns/div with ten samples (it now steps slower
until the frequency reads), the spectrum transformed half of a 2000-sample record (a record between two
powers of two is now resampled onto the one above, LCD and web alike), the web's references were read at 25
codes a division when the HD's 8-bit view has 30 (COMPARE read the master 0.33 V off; the export now restates
them), the Stack master row read "none" for a moment between the stack and its master, and M4/T25 follow the
page and the scope as they are.

PARTIAL rows with failures, for the same follow-up:

- **Operator `M10`**: with a 200 µs holdoff on the 2.5 kHz pulse train the frequency read 4 kHz in one full
  run and 2.5 kHz on the rerun. Plan: repeat it with the app's log to tell the glitch pulse in the window
  from a holdoff fault.
- **Fuzz finding** (simlab-e2e `fuzz`, seed 1, iteration 24): super-resolution's ARM did not take a click
  in 6 s under load 11, the stacker now running on the HD; the rerun at load 3 had no finding. Plan: repeat
  under load to tell the page's stacking work from the host's load.

