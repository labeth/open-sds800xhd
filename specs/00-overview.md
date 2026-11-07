# 00 — Overview

This is the entry point to the SDS800X HD specification. It describes the instrument, its hardware, the
address map, what a replacement firmware is responsible for, and the vocabulary the other specs use. Read
it first, then `01-system-architecture.md` (boot, partitions, how a replacement firmware is launched) and
`02-register-map.md` (every FPGA register).

## 1. The instrument

The target is the Siglent **SDS800X HD** family of 12-bit digital storage oscilloscopes.

| Model | Analog channels | Bandwidth |
|---|---|---|
| SDS802X HD | 2 | 70 MHz |
| SDS804X HD | 4 | 70 MHz |
| SDS812X HD | 2 | 100 MHz |
| SDS814X HD | 4 | 100 MHz |
| SDS822X HD | 2 | 200 MHz |
| SDS824X HD | 4 | 200 MHz |

All models share one acquisition board design; model, channel count and bandwidth are
identity/configuration, not different hardware paths, except where `09-control-plane.md` says otherwise.

Fixed facts:

| Property | Value |
|---|---|
| ADC resolution | 12 bit (two ADC devices, `adc0`/`adc1`) |
| Max sample rate | 2 GSa/s (1 channel per ADC pair), 1 GSa/s (2 ch), 500 MSa/s (4 ch) — exact rules in `04` |
| Display | 1024 × 600 colour TFT, parallel RGB, capacitive touch (Goodix GT9xx) |
| Front panel | key matrix + encoders read by the FPGA (64 key bits); key LEDs driven by a 32-bit LED word in the MAP plane (`08`) |
| Host interfaces | LAN (VXI-11, raw socket SCPI, telnet SCPI, web server, VNC), USB device (USB-TMC), USB host |
| Optional | 16-channel logic analyzer probe (SLA1016), function generator / Bode (see `15`) |

## 2. Compute and acquisition hardware

The instrument is built around a single **Xilinx Zynq UltraScale+ MPSoC** (PL IDCODE `0x04a43093`; exact
part in `12`). The processing system (PS) runs Linux 5.10 (arm64, 2 × Cortex-A53) from eMMC; the
programmable logic (PL) holds the acquisition engine, the trigger engines, the waveform rendering and
display composition, and the front-panel scanner. There is no separate display controller.

The PS reaches the PL through the AXI HPM window at `0x8000_0000`:

| Range | Block |
|---|---|
| `0x8000_0000` | UI VDMA: UI (Qt) framebuffer → LCD; S2MM screen capture for remote view |
| `0x8002_0000` | waveform VDMA: waveform layer |
| `0x8001_0000` | Xilinx AXI DMA (S2MM only): sample-data readout into PS DRAM `0x1E80_0000` (`03` §4) |
| `0x8003_xxxx` | ACQ plane: acquisition, trigger, front-end helper registers (`0x8003_0580` = ACQ 0x160, the `siglent_dma` driver's interrupt acknowledge) |
| `0x8004_xxxx` | MAP plane: waveform mapping/rendering, display composition and miscellaneous registers: LCD reset/enable/backlight, VDMA kick, key matrix (`0x8004_0580`) |

The register layout of every block is in `02-register-map.md`. Fixed PS DRAM regions (CMA pools, marked reusable in the device tree; the framebuffers lie above the
kernel's RAM end at `0x1F80_0000`): (`0x1E40_0000` waveform layer, `0x1E80_0000` raw sample data, `0x1F00_0000` logo, `0x1F80_0000` and
`0x1FC0_0000` framebuffers) are fixed by the device tree; see `01` and `14`.

The analog front end and clocking are on the PS SPI1 bus (8 chip selects): clock PLL, two ADCs, the relay
shift register, and four variable-gain amplifiers (one per channel). See `06`.  Temperature is read from a TMP422 on I2C.

## 3. Firmware scope and responsibilities

A replacement firmware runs as Linux userspace on the vendor kernel (or a rebuilt one, see `01`/`14`) and
is responsible for:

1. **Bring-up.** Program the clock PLL, load the PL bitstream, configure the VDMAs and the LCD (`13`).
2. **Analog front end.** Relays, VGA gain, offset, coupling, bandwidth limit per channel; ADC setup and
   interleave alignment (`06`), applying calibration (`10`).
3. **Acquisition.** Set sample rate, memory depth and acquisition mode; arm, wait for trigger, stop; move
   sample data to the PS (`03`, `04`).
4. **Triggering.** Program the trigger engine for the selected type, source, level and holdoff (`05`).
5. **Display.** Drive the waveform renderer in the PL and draw the user interface into the UI framebuffer
   (`07`).
6. **Input.** Read keys, encoders and touch; drive key LEDs (`08`).
7. **Control plane.** Hold the instrument state and apply changes to the hardware in the required order
   (`09`).
8. **Host interface.** SCPI over VXI-11/socket/telnet/USB-TMC, waveform transfer, screen dump, web and
   VNC remote (`11`).
9. **Auxiliary functions** (`15`).

Out of scope: the vendor's encrypted update container format, licensing.

## 4. Glossary

- **PS / PL** — processing system (ARM cores, DDR controller, peripherals) / programmable logic (FPGA
  fabric) of the Zynq UltraScale+ MPSoC.
- **board variant** — the only hardware property that separates the models in these specs is the channel count:
  2-channel or 4-channel board, from hw-id bit 7 (`02` §3). Bandwidth and model name are identity data (`09`).
  A rule stated "for this product" applies to every SDS800X HD model.
- **acq plane / map plane** — the two PL register groups: acquisition (sampling, trigger, record memory)
  and mapping (waveform-to-pixel rendering, display composition). See `02`.
- **VDMA** — Xilinx AXI Video DMA; moves frames between PS DRAM and the PL video pipeline. Two instances, named
  the same way in every spec: the **UI VDMA** at `0x8000_0000` (MM2S: UI framebuffer → LCD; S2MM: screen capture)
  and the **waveform VDMA** at `0x8002_0000` (waveform frame store).
- **frame / last-frame flag** — one triggered record of all enabled channels (`03` §1); a complete frame is
  signalled by MAP 0x082 = 1 (`03` §3.6).
- **screen limits** — display codes 0x080 … 0xF7F, centre 0x800 (`03` §5).
- **Example** — an "Example:" value is the register value the hardware needs in the stated state. A replacement
  follows the normative text; examples are reference points to check its writes against.
- **write rule** — registers are written unconditionally (`02` §1.2).
- **VGA** — variable-gain amplifier in each channel's front end (not the video standard).
- **HC595** — 74HC595 shift register chain driving the front-end relays (SPI1 CS3, 48 bits; `06`).
- **udmabuf** — the u-dma-buf kernel driver exposing a reserved DRAM region to userspace for sample data.
- **SCPI** — Standard Commands for Programmable Instruments, the ASCII command language of `11`.
- **UNVERIFIED** — not yet confirmed on a real unit; see each spec's `## Open`.

## 5. Minimum viable firmware (reading path)

A first replacement that boots, sets up the front end, acquires edge-triggered frames, reads them out, displays
them, reads the panel and serves SCPI needs these sections, in this order:

1. **Boot / takeover**: `01` §12 (start from the vendor boot via the launch hook and take the hardware over), or
   for a full boot `01` §2, §5 and `13` §1–§5. Constraints: `01` §13.
2. **Identity**: `09` §1 (model, channel count, bandwidth) and `02` §3 (hw-id byte).
3. **Register access**: `02` §1 (mapping, write rule, polls), `02` §4.3 (configuration banks).
4. **Display bring-up** (only for a full boot): `13` §5, `07` §4.4; pixels: `07` §2, §8.
5. **Start-up order** (authoritative): `03` §2. It pulls in `06` §9 (ADC init, phase training), `06` §3–§6 and
   §11 (relays, VGA, offset DAC, V/div, channel setup), `10` §7 (calibration load; defaults `10` §4.3) and
   `07` §7.10 (map start-up).
6. **Timebase**: `04` §1–§6, §8, §9 (rate, depth, decimation, FIR, register set, position), `07` §7.8 (main map
   set), `07` §7.9 (DPO), applied in the order of `03` §3.3.
7. **Edge trigger**: `05` §1–§4, §5.1, §5.2, §6, §7 (sweep modes, timeouts), §8.
8. **Acquisition loop**: `03` §3.1–§3.6 (arm, stop, soft reset, frame loop on MAP 0x082).
9. **Readout**: `03` §4.1–§4.4 and §5; drivers `14` §3, §7.
10. **Display of waveforms**: `07` §3, §6, §7.1–§7.5 (the PL draws the waveform layer itself).
11. **Front panel**: `08` §1–§6 (keys, encoders, LEDs); touch `08` §7 if wanted.
12. **SCPI**: `11` §2 (raw socket), §6 (engine, errors, status, identity), §7 (waveform transfer), with
    `appendix/scpi-commands.tsv` and `appendix/scpi-errors.tsv`.

**Optional features** (each marked "Optional feature" at the top of its section): stored-record redraw (`03` §3.7,
`04` §12), sequence, history, roll and zoom (`03` §6.2–§6.5, `04` §7, §11), XY (`04` §10, `07` §7.7), persistence
modes other than Off (`07` §7.2; its clear blocks are required), capture / remote view (`07` §5, `11` §8, §12), trigger types other than edge (`05` §5.3–§5.9) and
serial triggers (`05` §10), user self-calibration (`10` §5–§6), the other host interfaces (`11` §3–§5, §9–§11),
auxiliary functions (`15`). `16` is the test environment, not part of the firmware.
