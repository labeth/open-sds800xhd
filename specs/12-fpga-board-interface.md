# 12 — FPGA Board Interface

What a PL (FPGA) image and the PS boot configuration must provide to run on the SDS800X HD acquisition
board: the device, configuration options, the PS-side configuration the PL depends on (clocks, resets, AXI
ports, interrupts, EMIO), the PS pin map, the ADC data interface and every PL ball with a known board
connection. The register map an image presents is in `02-register-map.md`; how the PL is loaded is
`13-fpga-configuration.md`; the ADC/clock-chip programming is `06-vertical-and-analog.md`.

Ball names are SFVC784 package balls. For differential pairs the P ball is listed first. Every ball
assignment, I/O standard, drive strength and pull in this specification is **UNVERIFIED** until it is
measured on a unit, whether or not the row repeats the mark; an image drives no ball whose function is
unverified (spec 20 §14).

---

## 1. Device

| property | value |
|---|---|
| Part | **XCZU2CG-2SFVC784** (Zynq UltraScale+ MPSoC CG: 2 × Cortex-A53, 2 × Cortex-R5F, no GPU). ZU2 rather than ZU3 and speed grade -2 are **UNVERIFIED** (read CSU IDCODE `0xFFCA0040`: `0x04711093` = ZU2, `0x04710093` = ZU3, identical for CG and EG; read the package marking) |
| PL IDCODE (configuration port) | `0x04A43093`. The xczu3eg PL IDCODE is `0x04A42093`; the two differ in the same bit (12) as the ZU2/ZU3 CSU IDCODEs, which identifies `0x04A43093` as the ZU2 |
| Configuration frames | 14,964 frames × 93 words = 1,391,652 FDRI words (14,952 frames + 2 pad frames per row per block type; 3 rows); identical for XCZU2 and XCZU3 |
| Speed grade | -2 or faster: the A53 clock is 1316.7 MHz (§3.2), above the -1 limit |

Device capacities (AMD DS891 Tables 1/3; CG and EG equal):

| resource | XCZU2CG/EG | XCZU3CG/EG |
|---|---|---|
| CLB LUTs (logic) | 47,232 | 70,560 |
| CLB flip-flops | 94,464 | 141,120 |
| Block RAM (RAMB36 equivalents) | 150 | 216 |
| DSP48E2 | 240 | 360 |
| HD I/O in SFVC784 (HP: 156 on both) | 96 | 96 |

A replacement image must be built for the XCZU2 (PL IDCODE `0x04A43093`) and stay within the XCZU2 column
of the table. A bitstream built for the XCZU3CG/EG (`0x04A42093`) has the same frame layout but is rejected
by the IDCODE check on a ZU2; if the CSU IDCODE reads `0x04710093`, build for the XCZU3CG instead.

The DDR is DDR4, 32-bit bus (two ×16 devices, one rank), 600 MHz clock (1200 MT/s), configured by the FSBL
(platform, spec 13). The PL does not access DDR except through the PS AXI slave ports (§4).

### 1.1 Bank supplies

| bank | type | VCCO | use |
|---|---|---|---|
| 24 | HD | **3.3 V** (LVCMOS33) | Ethernet PHY (MII, §4.2), fabric outputs (§7.2) |
| 25 | HD | **3.3 V** (LVCMOS33) | low-speed I/O |
| 26 | HD | **1.5 V** (LVCMOS15) | ADC SDO inputs, low-speed I/O (§7.2) |
| 44 | HD | **3.3 V** (LVCMOS33) | LCD RGB666 + pixel clock, beeper (§7.2) |
| 64 | HP | **UNVERIFIED** (1.8 V expected) | ADC lanes, group A |
| 65 | HP | **UNVERIFIED** (1.8 V expected) | ADC lanes, groups A and B |
| 66 | HP | **1.8 V** (LVCMOS18) | ADC lanes group B, 1.8 V control I/O |
| 500/501/502/503 | PS MIO / config | **UNVERIFIED** | PS peripherals (§5) |
| 504 | PS DDR | 1.2 V (DDR4; board rail DVCC1V2_ZYNQ) | DDR4 |

Board rails that feed the device (service manual): DVCC0V85_ZYNQ, DVCC1V2_ZYNQ, DVCC1V2_ZYNQ_PLL,
DVCC1V5_ZYNQ, DVCC1V8_ZYNQ, DVCC1V8_ZYNQ_AUX, DVCC2V5_ZYNQ, DVCC3V3_ZYNQ. Rail-to-bank assignment beyond the
table is **UNVERIFIED**.

---

## 2. Configuration

The PL is configured by Linux `fpga_manager` (PCAP) from the runtime bitstream file after boot (spec 13). BOOT.bin
also carries a bitstream which the FSBL loads at power-on. This section is authoritative for the relation of the
two copies (spec 01 §5 and spec 13 §1/§3.1 refer here): one image can serve both roles, but the runtime copy must
still be (re)loaded after the clock chip is programmed (spec 13 §1), because the boot-time copy is configured
before the ADC clocks are valid. An image must:

* be a full (non-partial), unencrypted, uncompressed `.bin` in 32-bit big-endian word order (the order
  `fpga_manager` with flags 0 expects; BOOT.bin stores the same words byte-swapped);
* contain the bus-width words `0x000000BB 0x11220044` and the sync word `0xAA995566`;
* use these configuration settings:

| register | value | meaning |
|---|---|---|
| CTL0 | bit 6 DEC = 0 | no decryption; GTS_USR_B (bit 0) = 1 |
| TIMER, WBSTAR | 0 | no watchdog, no warm boot |
| IDCODE | `0x04A43093` | |

Any Vivado-generated full `.bin` for the XCZU2 with these options is acceptable.

### 2.1 Frame address order (for tools that read or patch frames)

FAR = block_type << 24 | row << 18 | column << 8 | minor. FDRI data is written once, starting at FAR 0, in the
order: block type 0 (CLB/IO/CLK) rows 0..2, each row columns 0..103 with their minor frames, then 2 pad
frames; then block type 1 (BRAM content) rows 0..2, 6 columns × 256 minors, then 2 pad frames per row.

### 2.2 State after configuration

The PS boots and loads the PL before the clock chip is guaranteed to deliver a stable ADC clock (the clock chip
is programmed before the runtime reload, spec 13 §1; the ADCs are programmed later by the application).
An image must hold all logic clocked from ADC data clocks in reset until its PLL/MMCM reports lock, and must
expose the lock and ISERDES-reset controls of §6.4 so software can resynchronise the capture at any time.

---

## 3. PS clocking the PL depends on

### 3.1 Reference and PLLs

PS_REF_CLK = **33.333 MHz**. The FSBL sets:

| PLL | FBDIV | DIV2 | output |
|---|---|---|---|
| IOPLL | 90 | ÷2 | 1500 MHz |
| RPLL | 60 | ÷2 | 1000 MHz |
| APLL | 79 | ÷2 | 1316.7 MHz |
| DPLL | 72 | ÷2 | 1200 MHz |
| VPLL | 63 | ÷2 | 1050 MHz |

Cross-domain dividers: IOPLL_TO_FPD ÷3, RPLL_TO_FPD ÷2, APLL_TO_LPD ÷3, DPLL_TO_LPD ÷3, VPLL_TO_LPD ÷2.

### 3.2 PL fabric clocks (FCLK)

| clock | register (CRL_APB) | value | source / divisors | frequency | known consumers |
|---|---|---|---|---|---|
| pl_clk0 (DT `fclk0`, clk 71) | PL0_REF_CTRL `0xFF5E00C0` | `0x01010F00` | IOPLL ÷15 ÷1 | **100 MHz** | AXI-Lite of both VDMAs (`s_axi_lite_aclk`) |
| pl_clk1 (`fclk1`, clk 72) | PL1_REF_CTRL `0xFF5E00C4` | `0x01010500` | IOPLL ÷5 ÷1 | **300 MHz** | VDMA AXI memory-mapped and MM2S stream clocks |
| pl_clk2 (`fclk2`, clk 73) | PL2_REF_CTRL `0xFF5E00C8` | `0x01010A02` | RPLL ÷10 ÷1 | **100 MHz** | none known |
| pl_clk3 (`fclk3`, clk 74) | PL3_REF_CTRL `0xFF5E00CC` | `0x01010A02` | RPLL ÷10 ÷1 | **100 MHz** | none known |

Linux keeps all four enabled through the DT `fclk0..3` nodes (status okay); `clk_ignore_unused` is only in the DT
`chosen/bootargs`, which U-Boot replaces, so it is not on the effective command line (spec 01 §2.3). An image needs
only pl_clk0 and pl_clk1. The VDMA S2MM stream clock is a fixed 100 MHz clock inside the PL (DT `misc_clk_0`).
The M_AXI_HPM0_LPD interface clock (`maxihpm0_lpd_aclk`) is driven from a PL clock; which one the original image
uses is **UNVERIFIED** (an open image drives it from pl_clk0).

Other PS clocks (for reference): A53 1316.7 MHz (APLL ÷1); R5 500 MHz; DDR 600 MHz (DPLL ÷2); GEM0 reference
125 MHz (IOPLL ÷12); GEM TSU 250 MHz; USB0/1 bus 250 MHz, USB3 dual ref 20 MHz; SDIO0 200 MHz (RPLL ÷5);
SPI0/SPI1 reference 200 MHz (RPLL ÷5); UART0 100 MHz; I2C1 100 MHz; LPD_LSBUS 100 MHz (RPLL ÷10), TOPSW_MAIN
600 MHz (DPLL ÷2); timestamp 33.333 MHz (PS_REF).

### 3.3 Resets

* **pl_resetn0 = EMIO GPIO 95** (GPIO bank 5 bit 31; Linux gpiochip offset 173). The FSBL drives it as an output,
  1 → 0 → 1 with a ~1 µs delay between steps, after loading the boot bitstream. It writes DIRM_5, OEN_5 and DATA_5
  as whole words (0x80000000), so EMIO 64..94 are left as inputs. An image must use it (or a
  register-controlled reset) as the global PL reset, active low. Whether anything toggles it after the
  `fpga_manager` reload is **UNVERIFIED**; an image must therefore come out of configuration in a usable state
  with pl_resetn0 already high.
* PS-PL isolation is removed and the PL power domain requested up by the FSBL (PMU_GLOBAL REQ_PWRUP bit 23,
  REQ_ISO bit 2). A replacement FSBL must do the same before configuring the PL.

---

## 4. AXI ports between PS and PL

| port | role | data width | QoS (RD/WR) | use |
|---|---|---|---|---|
| M_AXI_HPM0_LPD | PS master → PL | **32-bit** data (LPD AFI_FS DW_SS2 = 0); 35 address bits and 16-bit IDs used | — | all PL registers: `0x8000_0000` window (`0x8000_0000`–`0x9FFF_FFFF`) |
| M_AXI_HPM0_FPD / HPM1_FPD | PS master → PL | reset width (FPD AFI_FS not written) | — | **unused** |
| S_AXI_HP0_FPD | PL master → DDR | **128-bit**, read and write | 5 / 15 | consumer in the original image **UNVERIFIED** (candidates: acquisition record memory, record readout AXI DMA to `rawdata`, VDMAs) |
| S_AXI_HP1_FPD | PL master → DDR | **64-bit**, **write only** | 3 / 3 | consumer **UNVERIFIED** |
| S_AXI_HP2_FPD | PL master → DDR | **64-bit**, read and write | 10 / 10 | consumer **UNVERIFIED** |
| S_AXI_HP3_FPD | PL master → DDR | **32-bit**, read and write | 10 / 10 | consumer **UNVERIFIED** |
| S_AXI_HPC0/HPC1_FPD, S_AXI_LPD, S_AXI_ACP, S_AXI_ACE | | QoS 0 written | 0 / 0 | **unused** |

DisplayPort live video/audio and the PL↔PS DMA handshake signals are unused.

The PL has no DDR interface of its own (§1.1: no bank carries a memory interface), so the acquisition **record
memory** (spec 03 §1) is PS DDR4 reached through these HP ports, besides the AXI DMA and VDMA traffic. Which port
carries which master, and the DDR address range of the record memory, are **UNVERIFIED** (most likely DRAM
above `0x2000_0000`, outside the Linux memory node; spec 01 §1/§13). A replacement kernel must not give that
DRAM to Linux until the range is known.

The FSBL programs the HP widths (AFIFM RDCTRL/WRCTRL `FABRIC_WIDTH`: `0xFD380000`/`0xFD380014` = 0,
`0xFD390000`/`14` = 1, `0xFD3A0000`/`14` = 1, `0xFD3B0000`/`14` = 2) and QoS (`…08` RD, `…1C` WR). An image must
use exactly these widths on the ports it uses, or the FSBL/psu_init must be regenerated.

PL slaves in the HPM0_LPD window (address decode is part of the image; details spec 02):

| base | size | block |
|---|---|---|
| `0x8000_0000` | 64 KiB | UI VDMA (UI/menu layer; MM2S 32-bit stream, S2MM 16-bit) |
| `0x8001_0000` | 64 KiB | Xilinx AXI DMA (S2MM only; record readout to `0x1E80_0000`; the `dmadata_irq` interrupt is acknowledged at offset 0x34; spec 02 §5.2, spec 14) |
| `0x8002_0000` | 64 KiB | waveform VDMA (waveform layer; MM2S and S2MM 16-bit) |
| `0x8003_0000` | 64 KiB | acquisition (ACQ) registers (word-indexed, spec 02; includes the `siglent_dma` acknowledge word `0x80030580`) |
| `0x8004_0000` | — | LCD, backlight, key matrix, VNC registers (spec 02, 07, 08) |

VDMA address width 32, one frame store, genlock, flush-on-fsync.

### 4.1 Interrupts PL → PS

PL interrupts are level-high (DT flags 4) on `pl_ps_irq0[7:0]` = GIC SPI 89..96 and `pl_ps_irq1[7:0]` = GIC SPI
104..111.

| PL line | GIC SPI | DT consumer |
|---|---|---|
| pl_ps_irq0[0] | 89 | `siglent_dmadata_irq` (acquisition data ready) |
| pl_ps_irq0[1] | 90 | `siglentkb` (key matrix) |
| pl_ps_irq0[2] | 91 | UI VDMA MM2S |
| pl_ps_irq0[3] | 92 | waveform VDMA MM2S |
| pl_ps_irq0[4] | 93 | waveform VDMA S2MM |
| pl_ps_irq0[5] | 94 | `siglent_dma` |
| pl_ps_irq0[6] | 95 | none in DT (function **UNVERIFIED**; `/dev/overload_irq` candidate) |
| pl_ps_irq0[7] | 96 | UI VDMA S2MM |
| pl_ps_irq1[0] | 104 | `siglent_vnc` (frame copied) |
| pl_ps_irq1[1] | 105 | `siglent_cpld_irq` (rising edge per driver, spec 14). `/dev/cpld_irq` never signals on this product |
| pl_ps_irq1[7:2] | 106..111 | unused |

An image may tie irq1[1] to 0.

### 4.2 EMIO

The board uses these EMIO signals; every other EMIO input of the PS is tied off and every other EMIO output is
unused:

| EMIO signal | PS peripheral | PL connection |
|---|---|---|
| GPIO EMIO 0 output (gpio 78) | SPI1 CS1, active low | ADC 0 serial-port chip select (DT `adc0@1`) → ball **A6** (bank 66, LVCMOS18 12 mA) and select input of the SDO mux (§7.2) |
| GPIO EMIO 1 output (gpio 79) | SPI1 CS2, active low | ADC 1 chip select (`adc1@2`) → ball **A7** (bank 66, LVCMOS18 12 mA) and the SDO mux |
| GPIO EMIO 95 output | — | pl_resetn0 (§3.3), into PL logic |
| GEM0 GMII + MDIO (19 signals on 17 balls, MII width) | GEM0 `0xFF0B0000` | Ethernet PHY on bank 24, ball map below. DT `phy-mode = "gmii"`, PHY address 0x1E. GEM0 RX and reference clock come from EMIO (IOU_SLCR GEM_CLK_CTRL `0xFF180308` [1:0] = 3) |
| TTC0 wave-out 0 (counter 1) | TTC0 `0xFF110000` | ball **AF11** (bank 44, LVCMOS33 12 mA, pull-up): the beeper square wave (spec 15 §7) |

No EMIO GPIO input is connected. **SPI0 is not connected**: its EMIO pins are unused and SPI0 has no MIO pins
(§5), so the DT node `spi@ff040000` / `kb595@0` reaches no ball and software must not depend on it. GEM0 TSU and
FIFO interfaces, UART, I2C, CAN, SDIO and SPI1 EMIO are unused.

GEM0 ball map (bank 24, LVCMOS33, pull-up on every pin; outputs 12 mA; **UNVERIFIED** on a unit):

| GEM0 EMIO signal | dir (PL view) | ball | GEM0 EMIO signal | dir | ball |
|---|---|---|---|---|---|
| GMII_TXD0 | O | AH13 (L3N) | GMII_RXD0 | I | Y13 (L10N) |
| GMII_TXD1 | O | AE13 (L4P) | GMII_RXD1 | I | W12 (L11P) |
| GMII_TXD2 | O | AF13 (L4N) | GMII_RXD2 | I | W11 (L11N) |
| GMII_TXD3 | O | AB13 (L7N_HDGC) | GMII_RX_DV | I | AA12 (L12N) |
| GMII_TX_EN | O | AB14 (L8N_HDGC) | GMII_RX_ER | I | Y14 (L10P) |
| GMII_TX_CLK | I | AA13 (L7P_HDGC) | GMII_COL | I | W14 (L9P) |
| GMII_RX_CLK | I | AB15 (L8P_HDGC) | GMII_CRS | I | W13 (L9N) |
| MDIO_MDC | O | AC14 (L6P_HDGC) | | | |
| MDIO | I/O | AC13 (L6N_HDGC): I → MDIO_IN, O ← MDIO_OUT, driven while the PS enables the MDIO output (MDIO_TRI_B low) | | | |

GMII_TXD[7:4], RXD[7:4], TX_ER and GTX_CLK are not connected: the PHY interface is 4-bit MII with the transmit
clock supplied by the PHY, i.e. 10/100 Mb/s only (U-Boot forces the PHY to 100 Mb/s, spec 01).

Linux registers all 174 PS GPIO lines: offsets 0–77 = MIO 0–77, 78–173 = EMIO 0–95. The DT properties
`emio-gpio-width = 32` and `gpio-mask-low = 0x5600` are ignored by the unit's kernel. EMIO 2..94 have no DT user
and are unused.

Global Linux GPIO numbers are allocated from the top of a 512-number space. The 4-line `zynqmp-gpio-modepin`
controller registers first and gets 508–511. The PS GPIO controller registers next and gets **base 334**
(334–507), because it waits for the firmware clock and power-domain providers. So:

| sysfs GPIO | PS GPIO line | use |
|---|---|---|
| 334 + i (i = 0…7) | MIO i | board-ID straps (HWID bit i, spec 13 §4.1) |
| 342 | MIO 8 | external-calibration switch output (spec 06/10) |
| 343 | MIO 9 | relay shift-register output enable / reset, output, set 1 after the first relay image (spec 06 §3.4); MIO 9 is the only pin with a pull-down (§5), i.e. the enable defaults off |
| 373 | MIO 39 | output, set 1 once before the ADC phase training (function **UNVERIFIED**; spec 01 §7) |
| 380 | MIO 46 | input, rising-edge probe interrupt, exported after the phase training (spec 01 §7) |
| 334 + 78 + k | EMIO k | — |

A replacement kernel must reproduce this numbering, or the replacement software must look the lines up by
controller offset. **UNVERIFIED** (derived from driver probe order, not observed): confirm with
`/sys/kernel/debug/gpio` on a unit. If the order were reversed, the base would be 338, and 334–337 would be the
mode pins.

---

## 5. PS pin map (MIO)

MIO_PIN_n (`0xFF180000 + 4n`) values written by the FSBL:

| MIO | function |
|---|---|
| 0–12 | GPIO; 0–7 = board-ID straps (sysfs 334–341), 8 = external-calibration switch output (sysfs 342), 9 = relay-chain enable output (sysfs 343), §4.2 |
| 13–20 | SD0 (eMMC) DATA0–7 |
| 21 | SD0 CMD |
| 22 | SD0 CLK |
| 23 | SD0 bus power |
| 24–31 | GPIO; 27–31 are SPI1 chip selects CS7..CS3 (DT order: CS3 = MIO31 `hc595`, CS4 = MIO30 `vga1`, CS5 = MIO29 `vga2`, CS6 = MIO28 `vga3`, CS7 = MIO27 `vga4`) |
| 32 | SPI1 SCLK |
| 33 | GPIO |
| 34 | GPIO = SPI1 CS0 (`pll@0`), active low |
| 35 | SPI1 SS0 (native; not used by Linux, which uses GPIO chip selects) |
| 36 | SPI1 MISO |
| 37 | SPI1 MOSI |
| 38–41 | GPIO |
| 42 | UART0 RXD (console 115200 8N1) |
| 43 | UART0 TXD |
| 44–51 | GPIO; 47 and 48 are pulsed 1→0→1 by the FSBL (USB ULPI PHY resets **UNVERIFIED** which is which) |
| 52–63 | USB0 ULPI (host port) |
| 64–75 | USB1 ULPI (device port, USB-TMC) |
| 76 | I2C1 SCL (400 kHz; touch GT9xx @0x14, TMP422 @0x4C, device @0x74) |
| 77 | I2C1 SDA |

Tri-state: MIO_MST_TRI0 = 0, TRI1 = `0x00B00400` (MIO 42, 52, 53, 55 input), TRI2 = `0x0000000B` (MIO 64, 65, 67
input). Bank control: all pins maximum drive, CMOS (no Schmitt), pull enabled on every pin,
pull-up on every pin except MIO 9, which has a pull-down; fast slew. SD0 is eMMC (IOU_SLCR CTRL_REG_SD bit 0 = 1), 8-bit.

The SPI1 chip-select GPIO numbers are indices of the PS GPIO controller: 0–77 = MIO, 78–173 = EMIO 0–95. The DT gives
`num-cs = <7>` but lists 8 `cs-gpios`; the SPI core uses the larger count, so CS7 (`vga4`) works. A replacement
DT should set `num-cs = <8>`.

---

## 6. ADC data interface

### 6.1 ADCs

Two **TI ADC12D1000** (12-bit, dual-channel 1.0 GSPS, or one channel at 2.0 GSPS in DES interleave mode), each
on its own 1.9 V analog rail (VCC1V9_ADC1, VCC1V9_ADC2). Serial ports on PS SPI1 CS1/CS2 (EMIO GPIO 78/79).
Register programming, modes (DES, demux, calibration) and the clock chip are in spec 06. Boards with HWID
bit 4 = 1 fit the **TI ADC12DL1500** instead (12-bit, 2 × 1.5 GSPS, LVDS); software selects the type with acq
register `0x800307EC` (word 0x1FB, 0 = ADC12D1000, 1 = ADC12DL1500; spec 06). Whether the ADC12DL1500 board uses
the balls and lane rate of §6.2 (it may run at 1.5 GSPS, i.e. 750 MHz DCLK) is **UNVERIFIED**.

### 6.2 Lanes

The board carries **48 LVDS data pairs**: per ADC the 1:1 non-demux buses DI[11:0] and DQ[11:0] (24 pairs).
At 1 GSPS per channel each pair runs at 1 Gb/s DDR against the ADC data clock DCLK = 500 MHz (fs/2).

Every lane must be received as a differential input (LVDS, 100 Ω differential termination: internal
`DIFF_TERM` or on the board, **UNVERIFIED** which), through the bitslice of its P ball:

* IDELAYE3, `DELAY_FORMAT = TIME`; a fixed delay is sufficient (group A 24 taps, group B 0, **UNVERIFIED**),
  because software aligns the capture only through the ADC clock phase (word 0x7B, applied by 0x7A), §6.4;
* ISERDESE3, `DATA_WIDTH = 4` (1:4 DDR), CLK = the 500 MHz data clock, CLKDIV = 250 MHz; each lane yields
  4 bits per 4 ns. Per ADC: 24 lanes × 4 bits = 8 samples of 12 bits (4 per channel, or 8 consecutive DES
  samples) per 250 MHz clock.

A replacement image may use variable delays; no per-lane delay register is part of the register interface. The
lanes form two groups (ball assignment **UNVERIFIED** on a unit):

**Group A (24 pairs):**

| bank | P / N balls |
|---|---|
| 64 byte 0 | AC9/AD9, AE9/AE8, AB8/AC8, AD7/AE7, AB7/AC7, AB6/AC6 |
| 64 byte 1 | AG9/AH9, AF8/AG8, AH8/AH7, AG6/AG5, AF7/AF6 |
| 64 byte 2 | AC4/AC3, AB4/AB3, AD2/AD1, AB2/AC2, AB1/AC1 |
| 64 byte 3 | AG4/AH4, AG3/AH3, AE3/AF3, AE2/AF2, AH2/AH1, AF1/AG1 |
| 65 byte 0 | W8/Y8, U9/V9 |

**Group B (24 pairs):**

| bank | P / N balls |
|---|---|
| 65 byte 0 | U8/V8, R8/T8, R7/T7, R6/T6 |
| 65 byte 1 | L1/K1, J1/H1, K2/J2, H4/H3, K4/K3 |
| 65 byte 2 | N7/N6, P7/P6, N9/N8, M8/L8 |
| 65 byte 3 | J5/J4, J6/H6, J7/H7, K8/K7, K9/J9, H9/H8 |
| 66 byte 0 | G1/F1, E1/D1, F2/E2, G3/F3, E4/E3 |

Which group belongs to ADC 0 or ADC 1, and the order of DI/DQ bits within a group, are **UNVERIFIED** (settle by
driving ADC test patterns on a unit and reading the capture).

ADC data clocks (DCLK), **UNVERIFIED**:

| ball pair | bank | use |
|---|---|---|
| AD5 / AD4 (L13P/N_T2L_N0_GC_QBC_64) | 64 | DCLK of the group-A ADC |
| L7 / L6 (L13P/N_T2L_N0_GC_QBC_65) | 65 | DCLK of the group-B ADC |
| AE5 (L12P_GC_64), L3 (L12P_GC_65), M6 (L14P_GC_65) | 64/65 | clock-capable inputs, function unknown; leave unused |

Each ADC interface has its own clock PLL/MMCM with its own reset and lock bits (word 0x1F, §6.4). Whether the
out-of-range outputs ORI/ORQ reach the PL is **UNVERIFIED**.

### 6.3 ADC-side outputs and 1.8 V control (bank 66)

| ball | electrical | use |
|---|---|---|
| B4 / A4 (L10P/N_T1U_N6_QBC_66) | differential output, held low | DCLK_RST± of one ADC (**UNVERIFIED**) |
| A9 / A8 (L23P/N_T3U_N8_66) | differential output, held low | DCLK_RST± of the other ADC (**UNVERIFIED**) |
| C3 / C2 (L12P/N_T1U_N10_GC_66) | differential clock output | target on the board **UNVERIFIED** |
| A6 (L21N_66) | LVCMOS18 12 mA output = EMIO GPIO 0 | ADC 0 SPI chip select (§4.2) |
| A7 (L21P_66) | LVCMOS18 12 mA output = EMIO GPIO 1 | ADC 1 SPI chip select (§4.2) |
| D7 (L13P_GC_QBC_66) | LVCMOS18 clock-capable input | **UNVERIFIED** |

An image must hold B4/A4 and A9/A8 low (the software has no control that would pulse them) and must route
EMIO GPIO 0/1 to A6/A7.

### 6.4 Capture control the software expects

The application drives the capture through word registers at `0x8003_0000 + 4·index` (full field list in
spec 02). Those that act on the ADC interface:

| index (address) | access | function |
|---|---|---|
| 0x1F (`0x8003007C`) | W: bit 0 / bit 1 = ADC-clock PLL reset for interface 0 / 1. R: bit 8 = ADC-clock PLL locked; bits 12, 13 = second PLL locked; the interface is ready when bits 8 and 12 are set | |
| 0x4F (`0x8003013C`) | W | ISERDES / input-delay reset (value written as given) |
| 0x7A (`0x800301E8`) | W | ADC phase-adjust enable strobe: write 0 then 1 (interface 0) or 0 then 0x10 (interface 1). On this product only the interface-0 form (0, 1) is used; it applies the phase of both ADCs |
| 0x7B (`0x800301EC`) | RMW bits [2:0] | ADC clock phase step |
| 0x7C (`0x800301F0`) | R bit 0 / bit 1 | PLL ready, interface 0 / 1 |
| 0x7D (`0x800301F4`) | W / R | W: ADC error clear strobes, 0 then 1 (interface 0), 0 then 2 (interface 1); bit 8 is also written (0x100, 0x102; meaning spec 02). R: ADC capture error flags (bits per spec 06) |
| 0x1EE (`0x800307B8`) | W | DDR4 delay setting |
| 0x1FA (`0x800307E8`) | RMW bit 8 | PLL sync |
| 0x1FB (`0x800307EC`) | W | ADC type select |

Software polls the lock bits (0x1F) for at most 5000 iterations of 1 µs and the PLL-ready bit (0x7C) for at most
1000 iterations of 1 ms. ACQ 0x080 is not part of the ADC interface (it is the trigger qualifier register,
spec 05 §2.1); there is no software lane or word alignment step. An image must implement these with the same
semantics (spec 02 is authoritative for addresses and bit layouts).

Start-up order (interface-0 controls only, HWID 0): write 0x1FB ← ADC type; pulse 0x4F 1 → 0; pulse 0x1FA bit 8
(PLL sync: 1, wait 10 ms, 0, wait 10 ms); then the **phase scan**, for k = 0..7 (exact procedure and phase choice:
spec 06 §9.4):

1. 0x4F ← 1 (hold ISERDES/delay logic in reset);
2. up to 3 tries: 0x1F ← 1, then 0 (pulse ADC-clock PLL reset); wait 10 ms; poll 0x7C bit 0 every 1 ms
   (≤ 1000 reads); abort the training after 3 failed tries;
3. 0x7B[2:0] ← k; wait 10 ms; 0x7A ← 0 then 1 (apply phase);
4. poll 0x1F bit 8 (≤ 5000 × 1 µs; abort the training on timeout);
5. 0x4F ← 0 (release); wait 10 ms;
6. 0x7D ← 0, 1, 0, 2 (clear capture errors); wait 10 ms; read 0x7D bits 4/5 (error flags for phase k).

The software then selects and applies the phase, continues with the ADC mode setup (0x07D ← 0x102 → 0x002) and
writes 0x1EE with every timebase setup and every rate-mode change (value rule: spec 02 §4.2). An image must
therefore keep its capture logic restartable by 0x4F at any time, re-lock after every 0x1F pulse, and report
per-interface capture errors in 0x7D that reflect the phase written in 0x7B.

---

## 7. Other PL balls

### 7.1 Electrical summary of every connected HD-bank ball

All HD outputs use slow slew. "PU" = internal pull-up enabled. Ball assignments, directions and standards are
**UNVERIFIED** on a unit; functions are in §7.2.

| bank | ball (pin name) | dir | standard / drive |
|---|---|---|---|
| 24 | AE15 (L1P), AG13 (L3P), AD15 (L5P_HDGC) | O | LVCMOS33 4 mA |
| 24 | AE14 (L1N), AH14 (L2N), AG14 (L2P) | O | LVCMOS33 4 mA, PU |
| 24 | AH13 (L3N), AF13 (L4N), AE13 (L4P), AC14 (L6P_HDGC) | O | LVCMOS33 12 mA, PU |
| 24 | AD14 (L5N_HDGC) | O | LVCMOS33 12 mA |
| 24 | AC13 (L6N_HDGC) | I/O | LVCMOS33 12 mA, PU |
| 24 | AB13 (L7N_HDGC), AB14 (L8N_HDGC) | O | LVCMOS33 12 mA, PU |
| 24 | AA13 (L7P_HDGC), AB15 (L8P_HDGC), W14, W13 (L9P/N), Y14, Y13 (L10P/N), W12, W11 (L11P/N), Y12, AA12 (L12P/N) | I | LVCMOS33, PU |
| 25 | D10 (L7N_HDGC), D11 (L8N_HDGC), B10 (L9N), B11, A10 (L10P/N), A12, A11 (L11P/N), D12, C12 (L12P/N), J11 (L1P) | O | LVCMOS33 12 mA |
| 25 | K12 (L2N), H11 (L3P) | O | LVCMOS33 4 mA |
| 25 | G10 (L3N), F10 (L5N_HDGC), F12, F11 (L6P/N_HDGC) | I/O | LVCMOS33 12 mA |
| 25 | J10 (L1N), J12 (L4P) | I | LVCMOS33 |
| 25 | K13 (L2P), C11 (L9P) | O | LVCMOS33 likely (bank VCCO) |
| 26 | B13, A13 (L3P/N) | I | LVCMOS15, two single-ended inputs |
| 26 | F13 (L7N_HDGC), F15 (L8P_HDGC) | I/O | LVCMOS15 12 mA |
| 26 | E15 (L8N_HDGC) | O | LVCMOS15 12 mA |
| 44 | AG10 (L1P), AG11 (L2N), AH12 (L3P) | O | LVCMOS33 12 mA |
| 44 | AF11 (L2P) | O | LVCMOS33 12 mA, PU |
| 44 | AH10 (L1N), AE10, AF10 (L4P/N), AE12 (L5P_HDGC), AC12, AD12 (L6P/N_HDGC), AD11, AD10 (L7P/N_HDGC), AB11, AC11 (L8P/N_HDGC), AA11, AA10 (L9P/N), W10, Y10 (L10P/N), Y9 (L11P), AB10, AB9 (L12P/N) | O | LVCMOS33 4 mA |
| 44 | AH11 (L3N) | I | LVCMOS33 |
| 44 | AF12 (L5N_HDGC), AA8 (L11N) | O | LVCMOS33 likely (bank VCCO) |

Balls not listed have no known board connection; a replacement image must leave them unused (high impedance)
unless their board connection is established.

### 7.2 Ball functions

Known functions (**UNVERIFIED** on a unit):

* **Bank 24, Ethernet PHY**: the 17 GEM0 balls of §4.2.
* **AF11 (bank 44)**: TTC0 wave-out 0, the beeper (§4.2).
* **SPI1 SDO read-back mux**: **A13 (L3N_26) = ADC 0 SDO** and **B13 (L3P_26) = ADC 1 SDO** (LVCMOS15 inputs);
  ball **J11** (L1P_25, LVCMOS33 12 mA output) returns the selected ADC's SDO at 3.3 V towards PS SPI1 MISO
  (MIO 36; board net **UNVERIFIED**). An image must drive J11 = A13 while EMIO GPIO 0 (ADC 0 CS) is low; J11 = B13
  while GPIO 0 is high and GPIO 1 (ADC 1 CS) is low; J11 = 0 while both are high. The other SPI1 devices (clock
  chip, HC595, VGAs) are not involved.
* **LCD (J15, parallel RGB666, 1024×600)**: 18 data outputs on bank 44 in three groups of six (one colour per
  group is likely; bit order and colour assignment **UNVERIFIED**): {AH10, AE10, AF10, AE12, AF12, AC12},
  {AD11, AD10, AB11, AC11, AA11, AA10}, {W10, Y10, Y9, AA8, AB10, AB9}. **AD12 (L6N_HDGC_44) = pixel clock
  output**. DE/HSYNC/VSYNC candidates (**UNVERIFIED** which, and whether these): AG10, AG11, AH12 in bank 44, or
  AG13 and AD15 in bank 24. AH11 is an input of unknown function. LCD reset/enable/backlight are registers at
  `0x80040054`, `0x80040058`, `0x80040534` (spec 07); the VDMA streams of §4 feed the pixel pipeline.

Connected, function **UNVERIFIED**:

* **D11 (L8N_HDGC_25)**: clock output.
* **E15 (L8N_HDGC_26)**: output.
* **6-bit bidirectional bus**: G10, F10, F12, F11 (bank 25, 3.3 V) and F13, F15 (bank 26, 1.5 V); J12 (input) and
  H11 (output) belong to the same interface. Front-panel or CPLD interface candidate.
* **Parallel control outputs**: B10, B11, A10, A12, A11, C12, D12, D10, C11: relay/attenuator or shift-register
  chain candidate.
* K13, K12 (outputs), J10 (input); bank 24 AE15, AE14, AG14, AH14, AD14 (outputs).

Their functions are to be settled on a unit (logic analyser, board trace).

---

## 8. What a replacement image must implement (summary)

1. Target XCZU2CG SFVC784, PL IDCODE `0x04A43093`, within the XCZU2 resource limits of §1, configuration
   options of §2.
2. Use pl_clk0 (100 MHz) for the AXI-Lite register side, pl_clk1 (300 MHz) for the video DMA side, pl_resetn0
   (EMIO GPIO 95) as reset; decode registers in the HPM0_LPD window at the addresses of spec 02. pl_clk2/3 are
   not needed.
3. Route EMIO: GPIO 0/1 to A6/A7 and to the SDO mux on J11 (§7.2), GEM0 MII + MDIO to the bank-24 balls,
   TTC0 wave-out 0 to AF11 (§4.2). SPI0 needs no connection.
4. Receive the 48 ADC lanes of §6.2 with IDELAYE3 + ISERDESE3 ×4, clocked from the ADC data clocks through
   MMCM/PLL; provide the lock/reset/phase controls of §6.4; hold the DCLK_RST outputs of §6.3 low.
5. Drive the interrupt lines of §4.1 with the semantics of spec 14; use the HP ports with the widths of §4.
6. Drive the LCD (bank 44) and the bank-25/26 peripherals as far as §7.2 and specs 07/08 define them.

---

## 9. Open toolchain coverage (for an open PL implementation)

The public UltraScale+ database (prjuray-db, part xczu3eg-sfvc784, same die) covers CLB logic, routing, BRAM and
the single-ended HD I/O of §7; it lacks LVDS inputs with termination (§6.2), MMCM/PLL settings, DSP blocks and the
PS8 interface, so an image that uses those is built with Vivado.

---

## Open

* XCZU2 vs XCZU3 (§1) and speed/temperature grade: read CSU IDCODE `0xFFCA0040` and the package marking.
* VCCO of HP banks 64 and 65, and of PS MIO banks 500–502.
* PS GPIO global base (334, derived from driver probe order, §4.2): confirm with `/sys/kernel/debug/gpio`.
* LVDS termination (internal DIFF_TERM vs board resistors) and the I/O standard of the lanes, the DCLK inputs and
  of B4/C3/A9.
* Assignment of lane groups A/B to ADC 0/1 and DI/DQ bit order: ADC test patterns on a unit. DCLK balls
  (AD5/AD4, L7/L6) and the clock PLL settings: scope on a unit.
* Every ball assignment of §4.2, §6 and §7: confirm on a unit.
* Target of the clock outputs C3/C2 and D11; function of the bank-25/26 bus and control outputs, of the bank-24
  outputs and of AH11/E15; LCD colour/bit order and which balls carry DE/HSYNC/VSYNC: probe the board.
* Consumers of S_AXI_HP0..3 in the original image and the clock driving M_AXI_HPM0_LPD.
* Lane map and lane rate on ADC12DL1500 boards; identity of the board variant that reaches an Anlogic
  acquisition FPGA over SPI (product board 0xB).
* DDR size: the controller address map implies 1 GiB, Linux uses 504 MiB (spec 01/13). Address range of the
  acquisition record memory in PS DDR (§4): probe the DDR above 504 MiB on a unit.
