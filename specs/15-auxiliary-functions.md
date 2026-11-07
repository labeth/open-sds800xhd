# 15 — Auxiliary Functions

This spec covers the functions outside the acquisition/trigger/display core: the function generator,
the logic analyser (MSO), the DVM and frequency counter, temperature sensing, fan, beeper, board
self-test and the option (feature) mechanism.

Product parameters that apply throughout:

- hardware ADC bits 12, data word width 16;
- display 1024 × 600;
- 10 horizontal × 8 vertical divisions.

PL register numbers are ACQ-plane indices (spec 02 §1.1: byte address 0x8003_0000 + 4 × idx).

---

## 1. Function generator (AWG / WGEN)

**There is no on-board waveform generator.** No DAC, register or pin for a generator output exists on
this product. A firmware must not offer an internal AWG.

The SCPI WGEN set exists only to drive an **external** Siglent generator:

- **SAG1021I:** a USB generator module on a front USB-A host port. It is opened as a USBTMC *host*
  device `/dev/usbtmc<N>` through the Linux `usbtmc` host driver.
- **SDG over LAN:** for the Bode-plot application, an SDG generator reached through a VXI-11 client
  (spec 11 §4.3).

The commands sent to the generator are SDG-style text: `C1:OUTP ON|OFF` and
`C1:BSWV WVTP,<type>,FRQ,<f>,AMP,<vpp>,OFST,<v>,PHSE,<deg>,DUTY,<%>,SYM,<%>`, plus `ARWV`, `STL?` and
`SYNC`. The guide documents these as `WGEN` / `<channel>:BSWV` …; they require the FG option (§8) and
the external hardware. Their behaviour is that of the SDG programming guide.

### 1.1 Built-in arbitrary wave tables (`config/arb/*.bin`)

Wave tables for download to the external generator are files named `<index>_<name>_ram.bin`, where the
index is the SDG built-in wave index:

- 0 sine, 1 noise, 2 stairup, 3 stairdn, 4 stairud, 5 ppulse, 6 npulse, 7 trapezia, 8 upramp, 9 dnramp;
- 10 exp_fall, 11 exp_rise, 12 logfall, 13 logrise, 14 sqrt, 15 root3, 16 x^2, 17 x^3, 18 sinc,
  19 gussian;
- 20 dlorentz, 21 haversine, 22 lorentz, 23 gauspuls, 24 gmonopuls, 25 tripuls, 26 cardiac, 27 quake,
  28 chirp, 29 twotone, 30 snr;
- 34 hamming, 35 hanning, 36 kaiser, 37 blackman, 38 gausswin, 39 triang, 40 blackmanharris,
  41 barthannwin;
- 42 tan, 43 cot, 44 sec, 45 csc, 46 asin, 47 acos, 48 atan, 49 acot.

Format of each file:

| Property | Value |
|---|---|
| Size | 32 768 bytes = 16 384 samples |
| Sample | signed 16-bit little-endian, full scale −32768 … +32767 |
| Content | one period, sample 0 at phase 0 (a sine peaks at sample 4096, crosses zero at 8192 and reaches its minimum at 12288) |
| Header | none |

## 2. Logic analyser (MSO, D0–D15)

Digital channels exist when the MSO option (option code `16LA`) is installed and an **SLA1016**
logic probe is connected.

- **Connection:** the SLA1016 is a USB device. The firmware finds it as a USBTMC host device
  (`/dev/usbtmc0..3`, USB product string `SLA1016`). It sends it commands and reads back captured
  digital records in USBTMC messages, using `USBTMC488_IOCTL_WAIT_SRQ` to wait for completion. No PL
  ISERDES path is used for the LA data on this product; the ISERDES reset register (ACQ 0x04F)
  belongs to the ADC data path.
- **Probe settings:** the firmware owns the probe-side settings and sends them to the probe:
  - D0–D7 and D8–D15 threshold mode (TTL, CMOS, LVCMOS33, LVCMOS25, custom) and custom level;
  - deskew, active channels, height and position;
  - digital trigger type, slope, pattern, limit range, holdoff;
  - threshold calibration values of the D0–D7 and D8–D15 groups.
- **Probe LEDs:** the probe has red and green LEDs that the firmware drives from a timer.
- **Probe protocol:** the SLA1016 USB command protocol is not specified here (**UNVERIFIED**). It is
  outside the scope's own hardware.
- **Probe absent:** when the probe is unplugged, digital reads fail and the digital channels show no
  data.

Waveform transfer of digital channels is described in spec 11 §7.3 (1 bit per point).

## 3. DVM

This product has no DVM and no waveform histogram. `:DVM?` answers `OFF`, while sub-commands such as
`:DVM:ENABle?` give −113 (Undefined header). A firmware may answer `:DVM?` the same way or omit the
root. **UNVERIFIED**:
whether the UI shows a DVM menu.

## 4. Frequency counter / totalizer

The counter is supported on this product. It is computed by the acquisition FPGA from the trigger
comparator of the selected source. Registers, all 16-bit words in the ACQ plane:

| Index | Access | Function |
|---|---|---|
| 0x0E | RW | bits 6:4 = frequency-counter source select |
| 0x63, 0x64 | W | low-frequency gate: 0x63 = number of periods N, 0x64 = 0 (N chosen from the expected frequency, see below) |
| 0x66 | W | auto-frequency measurement clear/restart: write 0 then 1 |
| 0x68 / 0x69 | R | auto-frequency count, low/high 16 bits |
| 0x6A / 0x6B | R | low-frequency period count, low/high 16 bits |
| 0x6E | R | gate status: bit 0 = auto-frequency gate (0x67) done, bit 4 = period gate (0x63) done; the bit tested depends on the measurement mode (auto / period), not on the source (poll with about 1 µs between reads and a bounded number of polls) |
| 0x111 | RW | totalizer control: bits 1:0 count source, bit 2 count edge, bit 3 gate type, bits 5:4 gate source, bit 6 gate pulse mode, bit 7 gate enable, bit 8 lock (pulse: write the current word with bit 8 set, then with bit 8 clear — e.g. 0x0104 → 0x0004 — to latch the count before reading 0x11B–0x11E), bit 12 clear (pulse likewise: e.g. 0x1004 → 0x0004), bit 13 shield (count inhibit) |
| 0x11B–0x11E | R | totalizer count, 56 bits: value = (r11E[7:0] << 48) \| (r11D << 32) \| (r11C << 16) \| r11B |

**Frequency computation.**

- **Measurement cycle:** restart (0x66 0 → 1), wait for 0x6E bit 0, read the auto value; if
  it is ≥ 1 MHz report it, else program the period gate N for it (table below), restart, wait for 0x6E bit 4 and
  report the low-frequency value (if that is ≥ 1 MHz, fall back to auto mode).
- **Auto mode:** f = ⌊(r68 | r69 << 16) × 1000 / (n − 1)⌋ in Hz, unsigned 64-bit integer arithmetic, with n = 900 =
  the auto gate count written to 0x67 (spec 05 §9); a firmware MUST use the n it wrote to 0x67. (The unit of the
  count, and so the absolute scale, is UNVERIFIED.)
- **Low-frequency mode:** f = N × 1.0e9 / (r6A | r6B << 16) (double arithmetic). The period counter
  therefore counts in 1 ns units. N is the gate written to 0x63, and it is chosen from the expected
  frequency f₀ as follows:

  | f₀ | N |
  |---|---|
  | < 10 Hz | 1 |
  | < 100 Hz | 5 |
  | < 1 kHz | 10 |
  | < 10 kHz | 100 |
  | < 100 kHz | 1000 |
  | < 1 MHz | 10000 |
  | ≥ 1 MHz | 1 |

  The register write order is 0x63 ← N, then 0x64 ← 0.

**Totalizer sequence.** Counter on: clear pulse (0x111 bit 12), then 0x111 = 0x0004 with the default
settings (bit 2 = 1; which edge this value selects is UNVERIFIED) and two more clear pulses. Every mode change
issues one clear pulse. While the mode is totalizer, a periodic task (about every 340 ms) issues a lock pulse
(0x104 → 0x004) and reads 0x11E, 0x11D, 0x11C, 0x11B in that order. Totalizer reset: shield on (bit 13), clear
pulse, lock pulse, shield off (words 0x2004, 0x3004, 0x2004, 0x2104, 0x2004, 0x0004). A firmware MUST
latch (lock pulse) before reading the count and SHOULD clear with the shield set.

**Counter/totalizer level.** The counter level goes through the trigger-level registers of the source
(spec 05). When the counter source is the trigger source, the counter level and the trigger's level 1 are one
value: `:COUNter:LEVel v` sets the trigger level 1 of the current type (LEVEL1 A 0xE0/0xE1, the source's level-1
pair, the search level, then the level re-apply cycle of spec 05 §5.10; example: 0.5 V on C1 at 1 V/div, rising →
0x8B2), later V/div changes re-clamp it like a trigger level, and a trigger-level command also updates the
counter level. With another counter source only the per-channel pair below is written. There are two level slots, k = 0 and k = 1, per source s = 0…3. The input value v (the hysteresis code)
is first clamped to 15 · 2^(12−8) = 240 codes (the same clamp as spec 05 §3.3). Then:

| k | Level code (from level, slope, v) written to | v written to |
|---|---|---|
| 0 | 0xD7, 0xDB, 0xF1, 0x101 (source 0–3) | 0xD6, 0xDA, 0xF0, 0x100 |
| 1 | 0xD9, 0xDD, 0xF3, 0x103 | 0xD8, 0xDC, 0xF2, 0x102 |

The level code is the same level conversion that the trigger uses (spec 05).

Supported modes/SCPI: `:COUNter[:ENABle]`, `:COUNter:MODE`, `:COUNter:SOURce`, `:COUNter:CURRent?`,
`:COUNter:STATistics:*` (appendix), and legacy `CYMT?`.

Replies without a signal: `:COUNter:CURRent?` → `0.00E+00` in frequency and totalizer mode (NR3, the count
as a number), `INF` in period mode. `CYMT?` → `CYMT <f>Hz` (CHDR header of spec 11 §6.3.2, unit `Hz`): for
f ≥ 10 Hz `<f>` is NR3 with the `:FORMat:DATA` digits (25 kHz signal: `CYMT 2.50E+04Hz`); for f < 10 Hz,
including no signal, the literal `10` (`CYMT 10Hz`);
`:TRIGger:FREQuency?` → `0.00E+00`. `:SYSTem:SELFCal?` answers `DONE` also while a self-calibration started by
`:SYSTem:SELFCal` is running (spec 10 §5); a firmware should answer `DOING` while it runs.

## 5. Temperature sensing

- **TMP422** (Texas Instruments, local + 2 remote diodes) at I²C address **0x4C** on the PS I²C
  controller at 0xFF030000 (Linux `i2c-0`, 400 kHz). It is bound by the kernel `tmp421` hwmon driver
  (DT compatible `ti,tmp422`) and readable as `/sys/bus/i2c/devices/0-004c/hwmon/hwmon0/temp{1,2,3}_input`
  (millidegrees C). Remote diodes 1/2 sense the ADC 0 / ADC 1 dies (physical wiring **UNVERIFIED**).
- **ZynqMP AMS** (PS and PL system monitor) at 0xFFA50000 is enabled in the device tree (IIO
  `xlnx,zynqmp-ams`); it reads the PS/PL die temperatures and supply voltages.
- **Use:** no temperature drives a fan (§6). The ADC quick-cal hook (spec 10 §6.1) reads
  `/sys/bus/i2c/devices/0-004c/hwmon/hwmon0/temp2_input` (remote diode 1, used as ADC 0) and, on 4-channel
  models only, `temp3_input` (remote diode 2, ADC 1), as integer °C (millidegrees / 1000, truncated; 0 if
  the file cannot be read). The first reading is the reference; when either temperature differs from its
  reference by **3 °C or more**, the hook runs the ADC internal calibration and takes a new reference at
  the next check. No other temperature is used for control. A replacement firmware may use the AMS
  freely.

## 6. Fan

The fan (if fitted) is not under PS software control. A firmware needs no fan code. **UNVERIFIED**
whether the fan is hard-wired or driven by PL logic.

## 7. Beeper

The beeper is specified in spec 08 §8 (TTC0 at 0xFF110000, counter 1: initialisation, interval 27770 /
match 13885, start/stop by Counter_Control_1 bit 0, durations 40 / 120 / 50 / 1.7 ms, sound-switch policy).
Additions owned here:

- **Routing:** TTC0 wave-out 0 is an EMIO signal routed through the PL to ball **AF11** (bank 44,
  LVCMOS33 12 mA, pull-up; spec 12 §4.2). The board net from AF11 to the buzzer is **UNVERIFIED**.
- **Switch:** the user switch is `:SYSTem:BUZZer {ON|OFF}` (legacy `BUZZ`).
- **Not used:** no Linux beeper input device (`pcspkr`) is used on this product. The kernel does not own
  the TTC (its DT node is `disabled`); the firmware maps 4 KiB of `/dev/mem` at 0xFF110000.
- **U-Boot:** U-Boot drives the same TTC0 for its boot beep (spec 13), with interval 16666. The
  firmware must reprogram TTC0 completely at start-up.

## 8. Options (capability list)

Options are licence-gated features; each option type is either supported by the product or not. Option
licences live in `option.txt` / `*.lic` files on the user partition, with a backup copy.
The UI shows option state in the system information, and `*OPT?` lists the installed options (one field
per supported option type in the type order below, `0` when not installed; spec 11 §6.4). This spec
lists only which options exist and what they gate. Licence formats and keys are out of scope.

Option types in order (index: name as used by `*OPT?`): 0 `FG`, 1 `WIFI`, 2 `16LA`, 3 `FlexRay`,
4 `CANFD`, 5 `I2S`, 6 `1553B`, 7 `PA`, 8 `SENT`, 9 `Manch`, 10 `EJ`, 11 `USB20`, 12 `ARINC`, 13 `CT-USB2`,
14 `CT-100BASE-T`, 15 `CT-1000BASE-T`, 16 `CT-100BASE-T1`, 17 `CT-1000BASE-T1`, 18 `CT-MIPI D-PHY`, 19 `CT-DDR`,
20 `CT-2.5/5/10GBASE-T`, 21 `CANXL`. Which of them the SDS800X HD supports is **UNVERIFIED** (three, from
`*OPT?` = `0,0,0`). The bandwidth upgrades are not in this list.

| Option code | Gates |
|---|---|
| `2BW01`, `4BW01` | bandwidth upgrade to 100 MHz (2-ch / 4-ch model) |
| `2BW02`, `4BW02` | bandwidth upgrade to 200 MHz (2-ch / 4-ch model) |
| `16LA` | MSO digital channels with SLA1016 (§2) |
| FG | external function-generator control (§1) |
| PWA | power analysis application |
| decode options (`CFD`, `MANC`, `U20`, `A429`, `FlexRay`, `1553B`, `SENT`…) | serial protocols beyond the standard IIC/SPI/UART/CAN/LIN; the programming guide lists none of these for SDS800X HD (**UNVERIFIED** which are offered on this model) |
| `WIFI`, compliance-test codes (`CT…`) | other products |

The bandwidth options change the analog front-end filter configuration (spec 06) and the reported
model. The `BW03`, `BW05` and `BW10` codes belong to other products.

## 9. Board self-test and factory test

- **UI self-tests:** the Utility → Self Test menu offers a screen test, a key/encoder test and an LED
  test. These need only the display and front panel (specs 07/08).
- **Factory board test:** the board supports the hardware checks below (not part of the documented
  command set). All register indices are ACQ-plane indices.

  | Check | How it runs |
  |---|---|
  | PLL lock | read IO line 0x31 of the PL I/O status |
  | DDR (record memory: PS DDR4 reached by the PL through S_AXI_HP, spec 12 §4) calibration | DDR reset, then poll register 0x21 for up to 37 856 polls |
  | DDR (record memory) pattern test | after 1 s, read the compare-error flag |
  | ADC code test | write register 0x1E6 = 0 then 1, wait 50 ms, read registers 0x1E8–0x1EB (one per channel) |
  | AC-line test | write register 0x1E5 = 0, wait 10 ms, write 1, poll register 0x1E7 (up to 30 000 polls) |
  | EXT-trigger input test | same sequence as the AC-line test |

  Further checks: a board-information byte (register 0x02 bits 15:8), LVDS and clock checks, and a
  self-test bus enable/data pair (0x189 = self-test mode, 0x18A = sense).
- **`*TST?`:** always returns 0.

A firmware may omit the factory tests.

---

## Open

- Whether the UI offers a DVM menu (`:DVM?` is answered, the product has no DVM).
- The meaning of n in the auto-mode formula (gate periods or gate time).
- The TTC clock rate (spec 08) and the board net from AF11 to the buzzer.
- Which option types the SDS800X HD supports.
- Whether a fan exists and how it is powered.
- TMP422 remote-diode wiring (assumed: remote 1 = ADC 0, remote 2 = ADC 1).
- The SLA1016 USB command protocol, which is needed only if the replacement supports the MSO option.
- Which decode options are actually sold for SDS800X HD.
