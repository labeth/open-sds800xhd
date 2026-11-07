# Specifications

Implementable specifications of the Siglent SDS800X HD (SDS802X/804X/812X/814X/822X/824X HD) hardware and its
interfaces, written so a replacement firmware can be built from the text alone. Start with `00-overview.md`.

**Rules for these documents** (same as open-sds1000cml): concise, complete, implementable. State what *is*
and what the implementation *must do*, in our own words. No history, no "how we found this", no changelog.
Register addresses, value formats, ordering and timing are given explicitly; registers are named by plane and
index with a description of their function. Where a value is a fixed constant, give the constant. Anything not
yet confirmed on a real unit is marked **UNVERIFIED** inline. Interfaces a host or the firmware must see
verbatim (SCPI headers, waveform formats, device nodes, file-system paths, partition names) are given as they
are; everything else is described in our own words. Spec 16 names the simulator's tools and trace fields.

| # | Spec | Covers |
|---|------|--------|
| 00 | [`00-overview.md`](00-overview.md) | Device, block diagram, product variants, address map summary, glossary, minimum-viable-firmware reading path (§5) |
| 01 | [`01-system-architecture.md`](01-system-architecture.md) | Boot chain, eMMC partitions, init/launch, device nodes, kernel modules, launch hook, update/recovery, supervision |
| 02 | [`02-register-map.md`](02-register-map.md) | PL (FPGA) AXI register map: every register the firmware uses, address, fields, value formats |
| 03 | [`03-acquisition-engine.md`](03-acquisition-engine.md) | Arm/trigger/stop FSM, start-up order, frame loop, record readout and DMA to the PS, sample format and screen limits, acquisition modes |
| 04 | [`04-timebase-and-bands.md`](04-timebase-and-bands.md) | Sample rates, interleave, decimation/peak-detect, memory depths, timebase table, roll |
| 05 | [`05-triggering.md`](05-triggering.md) | Trigger engines, types, levels, holdoff, coupling, serial triggers |
| 06 | [`06-vertical-and-analog.md`](06-vertical-and-analog.md) | AFE: relays (HC595), VGAs, offset DACs, ADCs, clock chip (programming in 13), SPI protocols, V/div tables, bring-up order |
| 07 | [`07-display-and-rendering.md`](07-display-and-rendering.md) | LCD timing, framebuffers, VDMA layers, waveform layer and map engine, backlight |
| 08 | [`08-front-panel.md`](08-front-panel.md) | Key matrix, encoders, key LEDs (MAP-plane LED word), key codes, interrupts, touch, beeper |
| 09 | [`09-control-plane.md`](09-control-plane.md) | Instrument state model, apply ordering, persistence, options/model identity (incl. the identity-record encoding and the model resolution order) |
| 10 | [`10-calibration.md`](10-calibration.md) | Calibration data files/layout, self-cal procedure, how cal maps to runtime |
| 11 | [`11-host-interface.md`](11-host-interface.md) | SCPI over LAN (VXI-11, socket, telnet), USB-TMC, waveform transfer, web/VNC remote |
| 12 | [`12-fpga-board-interface.md`](12-fpga-board-interface.md) | PS/PL boundary, PL clocks, ADC data interfaces, every used PL pin where known |
| 13 | [`13-fpga-configuration.md`](13-fpga-configuration.md) | Bitstream format/loading, clock (PLL) init, PL bring-up sequence |
| 14 | [`14-kernel-driver-interface.md`](14-kernel-driver-interface.md) | Custom kernel drivers: device nodes, ioctls, mmap, IRQs, DMA buffers |
| 15 | [`15-auxiliary-functions.md`](15-auxiliary-functions.md) | AWG (external), logic analyser, DVM, frequency counter/totalizer, temperature, fan, options, board self-test (beeper: 08 §8) |
| 16 | [`16-test-environment.md`](16-test-environment.md) | The emulation test environment: vendor userspace under qemu-aarch64 against a Go device model; how to run it and validate a replacement firmware against the same model |
| 20 | [`20-open-pl.md`](20-open-pl.md) | The open PL design (stage 2): blocks, clocks, CDC, PS–PL interfaces, record memory, trigger engine, streams, stacking, the renderer decision, the register ABI, the HAL mapping, what depends on UNVERIFIED facts |
| A | [`appendix/scpi-commands.tsv`](appendix/scpi-commands.tsv) | Documented SCPI command list (current and legacy forms) with applicability and defaults (companion to 11) |
| A | [`appendix/scpi-errors.tsv`](appendix/scpi-errors.tsv) | SCPI error codes and texts (companion to 11 §6.2) |
