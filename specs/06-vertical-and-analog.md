# 06 — Vertical and Analog Front End

This document specifies the per-channel analog path of the SDS800X HD: input relays,
offset DAC, variable-gain amplifier (VGA), ADCs and the clock chip, the SPI protocols that reach them, the
V/div and offset mapping, and the bring-up order. Calibration tables consumed here are laid out in
`10-calibration.md`. PL register indices are owned by `02-register-map.md`; they are cited here as
"acq reg N", which is register ACQ N of spec 02 (32-bit word index N in the acquisition register plane
at 0x80030000, address 0x80030000 + 4·N). Clock-chip register
sequences are in `13-fpga-configuration.md`; the PL side of the ADC data interface is in `12`.

Everything applies to both channel variants: 4-channel models (SDS804X/814X/824X HD) and 2-channel models
(SDS802X/812X/822X HD; only C1/C2 exist, the same per-channel rules apply).

## 1. Signal chain and control ownership

Per channel (C1..C4):

```
BNC (1 MΩ ∥ ~17 pF) ─ AC/DC relay ─ attenuator A (÷10) ─ attenuator B (÷10) ─ input/cal relay ─┐
                                                                                                 ├─ LMH6518 VGA ─ ADC ─ PL
                  offset DAC (DAC80501, 16 bit) ─ DAC attenuator (4 steps) ───────────────────────┘
```

The physical order of the relays and the injection point of the offset is **UNVERIFIED**; the control
behaviour below is exact.

| Control | Transport | Device | Section |
|---|---|---|---|
| Coupling, attenuators, BW relay, impedance relay, cal relay, DAC attenuators | PS SPI1 CS3 (`/dev/spidev1.3`) | 48-bit serial-in shift-register chain (DT `hc595`, 74HC595-type; part count **UNVERIFIED**) | §3 |
| Gain (preamp + ladder), analog BW filter | PS SPI1 CS4..CS7 (`/dev/spidev1.4..1.7`) | LMH6518 ×4 | §4 |
| Vertical offset | PL register (acq reg 0x146..0x149) | DAC80501 ×4, serialised by the PL | §5 |
| Fine gain (full-scale range) | PS SPI1 CS1/CS2 (`/dev/spidev1.1`, `1.2`) | ADC ×2 | §9 |
| Digital ×2 (500 µV/div), GND substitution | PL registers | FPGA | §6.4, §8.3 |
| Sample clocks | PS SPI1 CS0 (`/dev/spidev1.0`), boot only | clock chip | §10 |

There is no 50 Ω input path on this product (datasheet: "no internal 50 Ω"); the firmware keeps the 1 MΩ
setting. There is no hardware input-overload detector on this board (§13).

## 2. SPI transport

All analog chips are on PS SPI1 (Cadence SPI at 0xFF050000), 8 GPIO chip selects, exposed by the standard
Linux `spidev` driver (device-tree children compatible `rohm,dh2228fv`). Use `/dev/spidev1.N`; no
product-specific SPI driver is involved.

| Node | DT name | Device |
|---|---|---|
| `/dev/spidev1.0` | pll@0 | clock chip (boot only, §10) |
| `/dev/spidev1.1` | adc0@1 | ADC 0 |
| `/dev/spidev1.2` | adc1@2 | ADC 1 |
| `/dev/spidev1.3` | hc595@3 | relay shift-register chain |
| `/dev/spidev1.4`..`1.7` | vga1..vga4 | LMH6518 for C1..C4 |

Rules:

- SPI mode 0 (`SPI_IOC_WR_MODE` = 0, request 0x40016B01, argument is a pointer to a u8). Bits per word
  and speed are left at the spidev defaults: 8 bits, `spi-max-frequency` = 20 MHz. MSB first.
- A **write** is one `write(fd, buf, n)`: one chip-select frame of n bytes.
- A **read** (ADC12DL1500 only) is one `SPI_IOC_MESSAGE(1)` (0x40206B00) full-duplex transfer of n bytes
  with `speed_hz` = 1 000 000, `delay_usecs` = 1, `bits_per_word` = 8, `cs_change` = 0.
- The nodes may be kept open; mode 0 must be set before the first transfer.
- All front-end SPI accesses are serialised (one thread or one lock); the PS SPI controller is shared by all
  eight devices.

## 3. Relay shift register (HC595 chain, `/dev/spidev1.3`)

### 3.1 Frame

The chain holds 48 bits. Keep a 48-bit image `R` and send it whole on every change, as one 6-byte write:

```
tx[0] = R[47:40]   (DAC-attenuator nibbles C3/C4)
tx[1] = R[39:32]   (DAC-attenuator nibbles C1/C2)
tx[2] = R[31:24]   (C4 control byte)
tx[3] = R[23:16]   (C3 control byte)
tx[4] = R[15:8]    (C2 control byte)
tx[5] = R[7:0]     (C1 control byte)
```

### 3.2 Per-channel control byte (`R[8·c+7 : 8·c]`, c = 0 for C1 … 3 for C4)

| Bit | Name | Value | Meaning |
|---|---|---|---|
| 0 | — | 0 | unused, keep 0 |
| 1 | ATT_B | 1 = bypass | ÷10 stage B (table below) |
| 2 | ATT_A | 1 = bypass | ÷10 stage A (table below) |
| 3 | DC | 1 = DC, 0 = AC | coupling capacitor bypass |
| 4 | CAL_N | 1 = normal, 0 = cal/internal | active-low input-select ("cal") relay |
| 5 | BW_FULL | see §8.2 | 1 only when bandwidth limit is off and the route (spec 04 §1) is 0 |
| 6 | Z_1M | 1 = 1 MΩ | impedance select; always 1 on this product |
| 7 | — | 0 | unused, keep 0 |

Attenuator bits by V/div range (V/div at the BNC, i.e. after removing the probe factor, §12):

| V/div | ATT_A (bit 2) | ATT_B (bit 1) | front-end group | overall |
|---|---|---|---|---|
| ≤ 100 mV | 1 | 1 | X1 | ÷1 |
| > 100 mV … ≤ 1 V | 1 | 0 | X10 | ÷10 |
| > 1 V | 0 | 0 | X100 | ÷100 |

(Which physical stage each bit drives is **UNVERIFIED**; only the combinations above are used.)

### 3.3 DAC-attenuator nibbles (`R[32+4·c+3 : 32+4·c]`)

One 4-bit field per channel selects the scaling of that channel's offset-DAC injection:

| DAC attenuator setting | nibble | DAC gain (from factory slopes, approx.) | used for V/div |
|---|---|---|---|
| A1X | 0x8 | 1 | 10 mV < V/div ≤ 1 V |
| A0.5X | 0x0 | ≈ 1/1.93 | 5 mV < V/div ≤ 10 mV, and V/div > 1 V |
| A0.25X | 0x1 | ≈ 1/3.68 | V/div ≤ 5 mV |
| A0.1X | 0x2 | ≈ 1/10 | not selected by the V/div rule (factory cal only) |

Selection rule, with V = V/div in volts: V > 1 → A0.5X; 0.01 < V ≤ 1 → A1X; 0.005 < V ≤ 0.01 → A0.5X;
V ≤ 0.005 → A0.25X.

### 3.4 Initial image and output enable

- Initial image: every control byte = 0x7E (bits 1–6 set: ÷1, DC, normal, BW_FULL, 1 MΩ); all four
  DAC-attenuator nibbles = 0xF (R[47:32] = 0xFFFF). 0xF is not a valid attenuator setting; each nibble
  keeps 0xF until that channel's setup writes it (§11.1 step 4), so frames sent before that (e.g. the
  BW_FULL updates of the first channel setups) carry 0xF nibbles.
- After the first image is prepared, drive sysfs GPIO 343 as output high (`/sys/class/gpio/gpio343`:
  export, direction `out`, value `1`). This is the 595 output-enable/reset line (**UNVERIFIED** which).
- The image is sent the first time any field changes (the channel setup in §11 always sends it).

## 4. VGA (TI LMH6518, one per channel)

### 4.1 Frame

3-byte write: `[0x00, D[15:8], D[7:0]]` (command byte bit 7 = 0 = write, address 0). Nothing is read back;
keep a 16-bit shadow per channel and always write the whole word.

### 4.2 Data word

| Bits | Field | Values |
|---|---|---|
| 15:11 | — | 0 |
| 10 | AUX_HIZ | 1 = auxiliary output high-Z (always 1) |
| 9 | — | 0 |
| 8:6 | FILTER | 000 full (≈900 MHz), 001 20 MHz, 010 100 MHz, 011 200 MHz, 100 350 MHz, 101 650, 110 750 |
| 5 | — | 0 |
| 4 | PREAMP | 0 = low gain (LG), 1 = high gain (HG, +20 dB) |
| 3:0 | LADDER | 0..10 = 0 … −20 dB in 2 dB steps (11..15 not allowed) |

Gain: LG 18.8 dB − 2·LADDER, HG 38.8 dB − 2·LADDER.

- Power-up value written to all four VGAs: **0x040A** (Aux Hi-Z, full BW, LG, −20 dB).
- A gain change replaces bits 4:0 only; a filter change replaces bits 8:6 only.
- Filter used on this product: 20 MHz (001) when the channel's 20 MHz limit is on **or** the channel is in
  GND coupling (outside self-cal); otherwise 350 MHz (100). (Other filter codes are not used on this product.)

### 4.3 VGA step index

The firmware numbers the 21 usable gain settings by a step index s:

| s | PREAMP | LADDER | VGA bits 4:0 | name in cal file |
|---|---|---|---|---|
| 0..10 | LG | s | s | V0..V10 |
| 11..20 | HG | s − 11 | 0x10 + (s − 11) | V16..V25 |

Step → bits: `PREAMP = (s > 10)`, `LADDER = s mod 11`. Bits → step: `(bits & 0x1F)`, minus 5 if bit 4 set.

## 5. Offset DAC

- Each channel has a DAC80501 (16-bit, code 0..65535) whose serial interface is driven by the PL. The PS
  writes the code to an acq register: C1 → reg 0x146, C2 → reg 0x148, C3 → reg 0x149, C4 → reg 0x147.
- The code may be written unconditionally or only when it changes (write rule of spec 02 §1.2); after a write that **changed** the code wait **5 ms** before relying on the new offset.
- Offset-DAC controller init (once, after the PL is configured), in this order: acq reg 0x141 = 4; wait
  2 ms; write 0 to acq regs 0x146, 0x148, 0x149, 0x147 (C1..C4 offset); write 0 to acq reg 0x144 (EXT
  trigger level), 0x143 (reserve 0), 0x142 (expand volt), 0x145 (reserve 1); wait 2 ms; acq reg 0x141 = 8.
  (Meaning of 4/8: controller reset/run, **UNVERIFIED**.)

Code computation (per channel, recomputed on every V/div, offset, coupling or sample-rate change):

```
V_off   = (vpos / 60) × Vdiv                  # volts at the BNC; vpos in screen pixels (60 px = 1 div)
                                              # vpos is negated first when the channel is inverted
code    = round_toward_zero( C − S × V_off )  # clamp to 0..65535 (§7.2 keeps it in range)
```

- `S` = DAC slope (codes per volt at the BNC) for the channel, the DAC attenuator setting (§3.3) and the
  front-end group (§3.2): `10-calibration.md` §3 (factory file), default table §6.3 below.
- `C` = DAC centre (zero) code for (channel, group, DAC attenuator, VGA step, sample-rate mode):
  `10-calibration.md` §4 (user self-cal file), default 0x8012 (32786).
- Sample-rate mode for table lookups: index 0 when the channel runs at the maximum sample rate (2 GSa/s),
  index 2 otherwise.
- Sign: a positive `V_off` moves the trace up; a higher DAC code moves it down.
- The stored vertical quantity is `vpos` (pixels), not volts: a V/div change keeps `vpos`, so the offset in volts
  scales with the V/div (CH1 `OFFSet 0.2` at 0.5 V/div = 24 px; then `SCALe 0.005` → V_off = 24 × 0.005 / 60 = 2 mV,
  DAC 0x8003, not the 0x7A79 that 0.2 V would give). `:CHANnel<n>:OFFSet?` then reports the scaled value
  (**UNVERIFIED** for the query). An offset command sets `vpos = V_off · 60 / Vdiv` after clamping by §7.2
  (whether `vpos` is kept fractional is **UNVERIFIED**; the DAC code uses V_off directly).
- Invert also sets the PL invert mask ACQ 0x00F (spec 02 §7); the trigger level is re-applied after an invert,
  offset or V/div change of the trigger source (spec 05 §3.2).
- The offset range presented to the user is limited by §7.2; the firmware must clamp before computing
  the code.

## 6. V/div → hardware mapping

### 6.1 Inputs

`Vdiv` = requested V/div at the BNC in mV (displayed V/div ÷ probe factor), any value in 0.5 … 10000
(fine adjustment allowed). The V/div class used by relays and limits:

| class | 7 | 8 | 9 | 10 | 11 | 12 | 13 | 14 | 15 | 16 | 17 | 18 | 19 | 20 |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| upper bound (mV) | 0.995 | 1.01 | 2 | 5 | 10 | 20 | 50 | 100 | 200 | 500 | 1000 | 2000 | 5000 | 10000 |

Class = first class whose upper bound ≥ Vdiv (≥ 10000 → 20). Relays: class ≤ 14 → X1, 15..17 → X10,
≥ 18 → X100 (§3.2). Front-end group for tables: g = X100 (index 0) if Vdiv > 1000, X10 (1) if
100 < Vdiv ≤ 1000, X1 (2) otherwise.

### 6.2 Algorithm

1. `t = Vdiv` (mV); if `Vdiv < 0.995` then `t = 2·Vdiv` and enable digital ×2 (§6.4).
2. Multiply `t` by the ADC/VGA compensation coefficient for the channel (1.0; nothing in this
   specification changes it).
3. Gain table `G[s]` = mV/div achieved at VGA step s with nominal ADC full scale, for the channel, group
   g, DAC attenuator (§3.3: A1X → sub-table 0, A0.5X → 1, A0.25X → 2) and sample-rate mode
   (`10-calibration.md` §4; defaults §6.3).
4. Step search:
   - if g = X1: scan s = 11..20; at the first s with `t < G[s]`, choose s or s−1, whichever `G` is closer
     to t (ties → s).
   - if nothing chosen: scan s = 0..10; at the first s with `t < G[s]` choose s (if s = 0) or the closer of
     s and s−1.
   - if still nothing (t beyond the table): s = 10 and ADC full-scale code 0 (not reachable with
     default tables up to 10 V/div).
5. Residual `r = t / G[s]` (dimensionless, ≈ 0.89…1.12).
6. ADC full scale: the full-scale table `F[j]`, j = 0..N−1, gives the relative V/div at ADC full-scale code
   `code_j = FS_min + j·FS_step` (`10-calibration.md` §4.2, defaults below). Find the first j with
   `r < F[j]` (compared with a tolerance: `r < F[j] − ε`, ε = 10⁻⁶ for F[j] < 1, 10⁻⁵ for 1 ≤ F[j] < 10).
   If that j is 0, or there is no such j (r ≥ F[N−1]), the result is code **0** (ADC12D1000: the minimum
   full scale; ADC12DL1500: raised to 0x2000 by §9.3). Otherwise
   `fsr = FS_min + FS_step × ((j−1) + (r − F[j−1]) / (F[j] − F[j−1]))` in float32 (the final multiply-add
   is fused, one rounding), then **truncated toward zero** to an integer (ADC12D1000: then clamped to
   0..0x7FFF). Neither out-of-range case occurs with the default tables; both can with a self-cal table.
   Use the "single channel" column of `F` at 2 GSa/s and the "all channels" column otherwise.
7. Write the VGA (bits 4:0 from s), the ADC full-scale register for the channel (§9), the relays (§3),
   the DAC attenuator nibble and the offset DAC (§5).

| ADC fitted | N | FS_min | FS_max | FS_step | F default |
|---|---|---|---|---|---|
| ADC12D1000 | 41 | 0x0000 | 0x7FFF | 819 | 0.75 + 0.0125·j |
| ADC12DL1500 | 11 | 0x2000 | 0xFFFF | 5734 | 0.625 + 0.0625·j |

(FS_step = ⌊(FS_max − FS_min)/(N − 1)⌋; F step = (1.25 − F[0])/(N − 1) in float32; the default
`F[j]` = F[0] + j·step is computed as a fused float32 multiply-add.)

### 6.3 Default tables (used when no calibration file is present)

Gain table, mV/div for group X1 (multiply by 10 for X10 and 100 for X100); identical for all channels,
DAC sub-tables and modes:

| s | 0 | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9 | 10 |
|---|---|---|---|---|---|---|---|---|---|---|---|
| G (LG) | 10.66 | 13.41 | 16.88 | 21.20 | 26.67 | 33.58 | 42.26 | 53.12 | 66.81 | 84.20 | 105.99 |

| s | 11 | 12 | 13 | 14 | 15 | 16 | 17 | 18 | 19 | 20 |
|---|---|---|---|---|---|---|---|---|---|---|
| G (HG) | 1.078 | 1.375 | 1.726 | 2.171 | 2.725 | 3.433 | 4.326 | 5.417 | 6.842 | 8.587 |

DAC slope S (codes per volt at the BNC) used when no factory DAC file exists:

| DAC attenuator | X100 | X10 | X1 |
|---|---|---|---|
| A1X | 18.78 | 194.8 | 1948.4 |
| A0.5X | 36.24 | 375.5 | 3755.6 |
| A0.25X | 69.07 | 716.0 | 7161.6 |
| A0.1X | 187.1 | 1939.1 | 19403.1 |

DAC centre C = 0x8012 for every entry.

Resulting mapping for the standard steps with default tables (C1..C4 identical):

| V/div | class | relays (A,B) | VGA step | preamp / ladder | VGA bits 4:0 | residual r | FSR code (ADC12D1000) | FSR code (ADC12DL1500) | dig ×2 | DAC att | S (codes/V) |
|---|---|---|---|---|---|---|---|---|---|---|---|
| 500 µV | 7 | 1,1 | 11 | HG / 0 dB | 0x10 | 0.9276 | 11639 | 35957 | yes | A0.25X | 7161.6 |
| 1 mV | 8 | 1,1 | 11 | HG / 0 dB | 0x10 | 0.9276 | 11639 | 35957 | no | A0.25X | 7161.6 |
| 2 mV | 9 | 1,1 | 14 | HG / −6 dB | 0x13 | 0.9212 | 11219 | 35369 | no | A0.25X | 7161.6 |
| 5 mV | 10 | 1,1 | 18 | HG / −14 dB | 0x17 | 0.9230 | 11336 | 35533 | no | A0.25X | 7161.6 |
| 10 mV | 11 | 1,1 | 0 | LG / 0 dB | 0x00 | 0.9381 | 12323 | 36915 | no | A0.5X | 3755.6 |
| 20 mV | 12 | 1,1 | 3 | LG / −6 dB | 0x03 | 0.9434 | 12671 | 37402 | no | A1X | 1948.4 |
| 50 mV | 13 | 1,1 | 7 | LG / −14 dB | 0x07 | 0.9413 | 12531 | 37207 | no | A1X | 1948.4 |
| 100 mV | 14 | 1,1 | 10 | LG / −20 dB | 0x0A | 0.9435 | 12677 | 37411 | no | A1X | 1948.4 |
| 200 mV | 15 | 1,0 | 3 | LG / −6 dB | 0x03 | 0.9434 | 12671 | 37402 | no | A1X | 194.8 |
| 500 mV | 16 | 1,0 | 7 | LG / −14 dB | 0x07 | 0.9413 | 12531 | 37207 | no | A1X | 194.8 |
| 1 V | 17 | 1,0 | 10 | LG / −20 dB | 0x0A | 0.9435 | 12677 | 37411 | no | A1X | 194.8 |
| 2 V | 18 | 0,0 | 3 | LG / −6 dB | 0x03 | 0.9434 | 12671 | 37402 | no | A0.5X | 36.24 |
| 5 V | 19 | 0,0 | 7 | LG / −14 dB | 0x07 | 0.9413 | 12531 | 37207 | no | A0.5X | 36.24 |
| 10 V | 20 | 0,0 | 10 | LG / −20 dB | 0x0A | 0.9435 | 12677 | 37411 | no | A0.5X | 36.24 |

Calibrated units produce different steps/codes; always run the algorithm, never hard-code this table.

### 6.4 Digital ×2 (500 µV/div class)

For class 7 the analog path is set for 1 mV/div and the PL doubles the samples: acq reg 0x19B bit n = 1
enables ×2 on ADC data lane n (RMW of the single bit). The enable value is 1 for class 7, 0 for every other
class; it is also 0 while the channel is in GND coupling. Lanes
written for channel index c (0 = CH1):

| ADC rate | enabled-channel mask (0x0B1) | lanes |
|---|---|---|
| 2 GSa/s | any | 0, 1, 2, 3 |
| 1 GSa/s | 0x1 or 0x2 | 0, 1 |
| 1 GSa/s | 0x4 or 0x8 | 2, 3 |
| 1 GSa/s | 0x3 | 2c, 2c + 1 (CH1 → 0, 1; CH2 → 2, 3) |
| 1 GSa/s | 0x5, 0x6, 0x9, 0xA | (c & 2), (c & 2) + 1 (CH1/CH2 → 0, 1; CH3/CH4 → 2, 3) |
| 1 GSa/s | 0xC | 2·(c & 1), 2·(c & 1) + 1 (CH3 → 0, 1; CH4 → 2, 3) |
| 1 GSa/s | other | c |
| ≤ 500 MSa/s | any | c |

("ADC rate" is the ADC work mode of the route, spec 04 §5.1: 2 GSa/s = route 0 at full rate.) The 1 GSa/s rows for masks other than 0x3, 0x5 and 0xC
are **UNVERIFIED**. The GND substitution of §8.3 does **not** use this lane numbering.

Each lane bit is written read-modify-write when its channel is set up (§11.1). Bits of lanes that no
enabled channel owns may be left set (e.g. mask 0x5 with CH1 at 500 µV → 0x19B = 0xF; then enabling CH4
(mask 0xD, "other" row) leaves lane 1 set → 0x7): they affect only lanes no channel reads. The firmware may
instead clear them (write the whole 4-bit word from the current channel set).

## 7. Code ↔ volts and offset limits

### 7.1 Sample codes

- ADC samples are 12-bit unsigned, 0..4095, centre (0 V after offset) = **2048**.
- The screen spans 8 divisions = 3840 codes: **480 codes/div**, screen codes 128..3968 (8 codes per
  vertical pixel, 60 px/div, 480 px). Codes beyond the screen are valid (overrange to 0/4095).
- Volts at the BNC for code `k`: `V = (k − 2048) / 480 × Vdiv_BNC − V_off`; at the probe tip multiply by the
  probe factor. (Software paths that use 16-bit scaled data use `k16 = k12 << 4`, centre 32768.)

### 7.2 Offset range (± volts at the BNC, 1 MΩ)

| V/div class | 7..10 (≤ 5 mV) | 11 (≤ 10 mV) | 12..14 (≤ 100 mV) | 15..17 (≤ 1 V) | 18..20 (> 1 V) |
|---|---|---|---|---|---|
| limit | ±1.6 V | ±4 V | ±8 V | ±80 V | ±400 V |

At the default slopes these limits use about ±11 500…±15 600 DAC codes around the centre.

## 8. Coupling, bandwidth limit, impedance

### 8.1 Coupling

| Coupling | DC (bit 3) | ATT bits | CAL_N (bit 4) | VGA filter | PL |
|---|---|---|---|---|---|
| DC | 1 | per V/div | 1 | per BWL | normal |
| AC | 0 | per V/div | 1 | per BWL | normal |
| GND | 0 | 0,0 (÷100) | 0 | 20 MHz | sample substitution (§8.3) |

Order on a coupling change: (1) apply GND state to all channels in GND (CAL_N = 0, VGA 20 MHz, PL
substitution) and remove it from the others; (2) set the DC bit and, for GND, clear the attenuator bits
(for DC/AC re-apply the V/div attenuator bits); (3) for DC/AC set CAL_N = 1; send the relay image.
During self-cal (`10-calibration.md` §5) GND does not force the attenuator bits, CAL_N or the 20 MHz filter.

### 8.2 Bandwidth limit (20 MHz)

- On: VGA FILTER = 001 (20 MHz); relay bit 5 = 0.
- Off: VGA FILTER = 100 (350 MHz); relay bit 5 = 1 if the route (spec 04 §1: 0 = zero or one analog channel
  enabled) is 0, else 0, independent of the rate. At boot, before the first channel-set evaluation, the route
  is taken as 1, so the first channel setups send bit 5 = 0; whenever the route changes, recompute bit 5 for every
  channel (one relay frame per channel whose bit changed, CH1 first, or one frame with all changes).
- Stored-record redraw (spec 03 §3.7 step 3): when the held record's channel set differs from the live one, the
  record's set is applied temporarily — ACQ 0x0B1 ← record mask, MAP 0x106+n per channel
  (spec 07 §7.1) and relay bit 5 recomputed for the record's route (one relay frame per changed channel);
  no VGA, offset-DAC, full-scale or ADC-mode write — and the live pass (03 §3.7 step 7) re-applies the live set
  the same way. Example: CH1 on, then CH2 on: 0x0B1 0x3 → 0x1 → 0x3, MAP 0x107 1 → 0 → 1, bit 5 set on all four
  channels for the redraw and cleared again. A firmware that does not redraw stored records omits this.
- The datasheet lists a 200 MHz hardware limit as well; the only analog filters used are 20 MHz and
  350 MHz plus relay bit 5 (presumed fixed ~200 MHz path, **UNVERIFIED**). The model bandwidth
  (70/100/200 MHz) is not set in the analog path.

Order on a BWL change: relay image first, then the VGA word. While a channel is in GND coupling (outside
self-cal) a BWL change updates the stored VGA word but sends nothing; leaving GND re-applies the stored
BWL setting and sends the word.

### 8.3 GND substitution in the PL

For every *enabled* channel in GND coupling the PL replaces the samples by a constant so the trace
sits at the offset position (a disabled channel in GND contributes neither a mask bit nor a constant): constant = clamp(2048 + 8·vpos, 0, 4095) (2048 − 8·vpos when inverted), vpos in pixels.
Registers: acq reg 0x55 = enable mask (0x1111 at 2 GSa/s, otherwise OR of 0x0001/0x0010/0x0100/0x1000
for C1/C2/C3/C4 in GND); constants in acq regs 0x53 (C1), 0x56 (C2), 0x5A (C3), 0x5F (C4); at
2 GSa/s all four constants get the value of the GND channel. Below 2 GSa/s the slot is the **channel index** (CH1 →
mask 0x0001 / 0x053, CH2 → 0x0010 / 0x056, CH3 → 0x0100 / 0x05A, CH4 → 0x1000 / 0x05F) at 1 GSa/s and at ≤ 500 MSa/s
alike — it does not follow the ×2 lane table of §6.4. Every coupling change, every offset/position or invert change of
a channel in GND, and every change of the enabled-channel set or of the ADC rate recomputes 0x055 and all four constants and
writes them in the order 0x055, 0x053, 0x056, 0x05A, 0x05F (constant 0 for slots whose channel is not in GND;
unchanged values may be skipped, write rule of spec 02 §1.2).

### 8.4 Impedance

Relay bit 6 = 1 (1 MΩ) always. (Bit 6 cleared would select a 50 Ω path, which is not populated on this
product, **UNVERIFIED**.)

## 9. ADCs

### 9.1 Part selection

Two ADCs, each digitising two channels. The fitted part is read from the board-ID GPIOs: HWID bit i =
sysfs GPIO 334 + i (i = 0..7); BOM version = HWID[7:4].

| HWID bit 4 (= BOM bit 0) | part | acq reg 0x1FB |
|---|---|---|
| 0 | TI ADC12D1000 (12-bit, 2 × 1.0 GSPS or 1 × 2.0 GSPS DES) | 0 |
| 1 | TI ADC12DL1500 (12-bit, 2 × 1.5 GSPS or 1 × 3.0 GSPS DES, LVDS) | 1 |

ADC 0 = `/dev/spidev1.1`, ADC 1 = `/dev/spidev1.2`. Channel mapping as programmed
(full-scale register per channel, §9.2/§9.3): C1 = ADC0 Q/INB input, C2 = ADC0 I/INA, C3 = ADC1 Q/INB,
C4 = ADC1 I/INA. That these are also the physical BNC → ADC input connections is **UNVERIFIED**.
Write acq reg 0x1FB before any ADC or PL-init access. The clock-chip variant test (HWID & 0x30) == 0x30 is used
only at boot for the clock chip (spec 13 §4.1), not for the ADC part.

On 2-channel models (HWID bit 7 = 1) only ADC 0 exists for the software: never access `/dev/spidev1.2` (no init,
test pattern, mode, full-scale or calibration); the VGA init still writes all four VGAs.

### 9.2 ADC12D1000

- Frame: 3-byte write `[0x40 | (A[3:0] << 1), D[15:8], D[7:0]]`. Write-only: keep a 16-register shadow per
  chip and do read-modify-write on the shadow.
- Registers used: 0x0 configuration (bit 15 CAL, 13 OVS, 12 TPM test pattern, 11 PDI, 10 PDQ, 7 DES,
  6 DEQ, 4 2SC), 0x3 I-channel full scale, 0xB Q-channel full scale (bits 14:0; 0x0000 ≈ 600 mVpp,
  0x4000 ≈ 800 mVpp default, 0x7FFF ≈ 1000 mVpp), 0x2/0xA I/Q offset (bit 12 sign, 11:0 magnitude; not used).
- Init: reg 0x0 = 0x2000, then reg 0x0 = 0x2040 (non-DES, both inputs). Keep CAL at 0 until §9.5.
- Modes (reg 0x0): non-DES 0x2040; DES on the I input 0x2080; DES on the Q input 0x20C0. DES is used only when
  exactly one analog channel is enabled **and** the ADC runs at 2 GSa/s (route 0 at full rate, spec 04 §5.1); one
  channel at a lower ADC rate (e.g. 1 GSa/s at 1 ms/div, 10 Mpts) uses non-DES. Both chips get the same value:

  | enabled analog channels | ADC rate | reg 0x0 (both ADCs) |
  |---|---|---|
  | CH1 only, or CH3 only | 2 GSa/s | 0x20C0 (DES, Q input) |
  | CH2 only, or CH4 only | 2 GSa/s | 0x2080 (DES, I input) |
  | one channel | < 2 GSa/s | 0x2040 (non-DES) |
  | any two or more, or none | any | 0x2040 (non-DES) |

  **ADC-mode refresh rule** (the single statement of it; spec 03 §3.3 step 2 and §11.1 refer here): the mode
  word (both chips), one read of ACQ 0x01F (lock bits 8 and 12, spec 02 §3), the 0x07D pulse below and the
  channel setups of every enabled channel (§11.1) are rewritten on every change of the enabled-channel set and on
  every change of the sample rate (a time/div or memory-depth change that changes the rate), even when the mode
  word itself is unchanged; a time/div or depth change that keeps the rate writes nothing to the ADCs. On a channel-set change the
  channel setups run once, after the mode word. (Whether any channel set needs different DES inputs on the two
  chips, one 0x2080 and the other 0x20C0, is UNVERIFIED; the table above gives both chips the same word.) After the mode word (both chips):
  ACQ 0x07D ← 0x102, then 0x002 (spec 02 §7); then rewrite the full-scale registers of every enabled channel
  (below); then set the calibration-pending flag so that the internal calibration of §9.5 (reg 0x0 = mode,
  then mode | 0x8000, both chips) runs before the next arm — not immediately. No phase action follows on
  ADC12D1000 boards (§9.4 step 7); the phase training of §9.4 is repeated only if the clock or the capture
  interface was reset. ACQ 0x1FA is not written at runtime (only the bit-8 sync pulse of §9.4).
- Full scale: when the channel runs at the maximum sample rate (2 GSa/s) write both registers with the
  channel's code, 0xB first, then 0x3; otherwise write only the channel's own register: C1/C3 → 0xB (Q
  input), C2/C4 → 0x3 (I input). Then wait 100 µs. The code is the truncated value of §6.2 clamped to
  0..0x7FFF.
- Output bus: 1:1 non-demux (DI[11:0] and DQ[11:0] per chip); the PL image captures exactly these 48
  lanes (spec 12 §6.2), so the ADC must not be put into 1:2 demux mode. No software write selects it (demux is a
  pin-strap function of the part; board strapping **UNVERIFIED**).
- Test pattern (training): set TPM (bit 12); clear it afterwards.
- Power down: reg 0x0 = 0x0C00; power on: 0x0000 (then re-init).

### 9.3 ADC12DL1500

- Frame: write `[A[14:8], A[7:0], D]`; read `[0x80 | A[14:8], A[7:0], 0x00]` → data = third received byte.
- Init (repeat the whole block three times, unconditionally):
  0x000=0xB0 (soft reset), 0x200=0x00, 0x061=0x00, 0x029=0x60, 0x02A=0x00, 0x030=0x00, 0x031=0xA0,
  0x032=0x00, 0x033=0xA0 (FS_RANGE_A/B = 0xA000), 0x038=0x00, 0x03B=0x00, 0x048=0x00, 0x060=0x01,
  0x062=0x01 (foreground cal), 0x068=0x61, 0x06B=0x00, 0x070=0x00, 0x09D=0x00, 0x160=0x00, 0x161=0x00,
  0x204=0x08, 0x206=0x0F, 0x201=0x02, 0x202=0x50, 0x204=0x08, 0x205=0x01, 0x061=0x01, 0x200=0x01,
  0x203=0x01, 0x06C=0x00, 0x06C=0x01 (start cal); then poll reg 0x06A until bits 1:0 ≠ 0, at most 500
  reads, 1 ms apart; on timeout log and continue (the block is repeated three times regardless). This init
  condition (≠ 0) differs from the calibration-done condition of §9.5 (= 0b11).
- Mode change, chip by chip, ADC0 (CH1/CH2) then ADC1 (CH3/CH4): 0x200 = 0x00, 0x061 = 0x00; then
  - DES (the §9.2 condition: exactly one analog channel enabled and full ADC rate): reg 0x201 bit 2 = 1, and
    reg 0x060 bits 1:0 = 2 (INB) if the chip's first channel (CH1 for ADC0, CH3 for ADC1) is enabled,
    else 1 (INA). The mux is chosen per chip from the channel mask, so with CH1 alone ADC0 uses INB and
    ADC1 INA; with CH3 alone ADC0 uses INA and ADC1 INB;
  - non-DES (two or more channels): reg 0x201 bit 2 = 0; reg 0x060 is **not** written (keeps its last value);

  then 0x061 = 0x01, 0x200 = 0x01. Regs 0x201 and 0x060 are read-modify-write (SPI read first) and
  written only when the value changes. Then ACQ 0x07D ← 0x102, 0x002 as in §9.2, the full-scale
  registers of the enabled channels, and the phase re-apply of §9.4 step 7 if the DES state changed.
  Both chips always get the same DES state.
- Full scale: input A ← reg 0x031 = code>>8, 0x030 = code&0xFF; input B ← 0x033/0x032; minimum code 0x2000.
- Test pattern on: 0x203 = 0x00, then 0x205 = 0x11; off (normal data): 0x203 = 0x00, then 0x205 = 0x00.
  0x203 stays 0 after the training (the init block's 0x203 = 0x01 / 0x205 = 0x01 are not restored);
  leave it at 0 (whether the data interface needs 0x203 = 1 again is **UNVERIFIED**).

### 9.4 Data-capture phase training (PL side; run once after ADC init)

Register meanings are in spec 02 (acq regs 0x1F, 0x4F, 0x7A..0x7D, 0x1FA). Only the interface-0 controls are
used: 0x1F bit 0, 0x7A = 0 → 1, 0x7C bit 0. One phase in 0x7B serves both ADCs; the interface-1 forms
(0x1F bit 1, 0x7A = 0x10, 0x7C bit 1) are not used on this product.

1. Put each fitted ADC into test-pattern mode (§9.2/§9.3). If this fails, skip to step 6 with phase = 4.
2. PLL sync: reg 0x1FA bit 8 = 1 (RMW), wait 10 ms; bit 8 = 0, wait 10 ms.
3. For p = 0..7:
   1. 0x4F = 1 (ISERDES reset; held across the retries below).
   2. Up to 3 tries: {0x1F = 1; 0x1F = 0; wait 10 ms; poll 0x7C bit 0 = 1, at most 1000 reads, 1 ms apart}.
      If all 3 tries fail: 0x4F = 0 and abort the training.
   3. 0x7B[2:0] = p; wait 10 ms.
   4. 0x7A = 0; 0x7A = 1.
   5. Poll 0x1F bit 8 = 1 (at most 5000 × 1 µs, spec 02 §1.3). On timeout: 0x4F = 0 and abort the training
      (no retry).
   6. 0x4F = 0; wait 10 ms.
   7. Clear errors: 0x7D = 0, 1, 0, 2; wait 10 ms.
   8. good[p] = (0x7D bit 4 = 0) and, on 4-channel models only, (0x7D bit 5 = 0).
4. If fewer than 3 phases are good the training failed. Otherwise:
   - if good[0] and good[7] are not both set: best = ⌊(first good p + last good p) / 2⌋;
   - else (the window wraps): a = first p in 0..6 with good[p] and not good[p+1] (−1 if none);
     b = (first p found scanning p = 7, 6, …, 1 downwards with good[p] and not good[p−1]) − 8 (−1 if none);
     s = a + b;
     best = s / 2 truncated toward zero, plus 8 if s < −1. When all 8 phases are good this gives 7.
5. Return the ADCs to normal data (§9.2/§9.3).
6. Apply best, or the product default **4** if the training failed or was skipped: 0x1F = 1; 0x1F = 0;
   0x4F = 1; wait 10 ms; 0x7B[2:0] = phase; wait 10 ms; 0x7A = 0; 0x7A = 1; poll 0x1F bit 8 (5000 × 1 µs;
   on timeout log and continue); 0x4F = 0. Keep the applied phase.
7. After an ADC mode change: on ADC12DL1500 boards repeat step 6 with the stored phase (no retraining)
   whenever the DES state (reg 0x201 bit 2) changed; an input-mux-only change (reg 0x060, e.g. none → CH1)
   does not re-apply the phase. On ADC12D1000 boards no phase action at all (no ACQ 0x07B/0x07A/0x01F/0x04F
   writes at runtime).

### 9.5 ADC internal calibration

- ADC12D1000: write reg 0x0 with CAL = 0, wait 1 µs, write CAL = 1, wait 1 µs; then wait 52 ms.
- ADC12DL1500: reg 0x06C = 0 then 1; then wait 52 ms; poll reg 0x06A until bits 1:0 = 0b11 on every
  fitted ADC, at most 500 passes 1 ms apart; on timeout log and continue (no retry).
- The calibration runs on every fitted ADC, whether or not it serves an enabled channel (4-channel model with
  only CH1 on: both ADCs are calibrated); ADC1 does not exist on 2-channel models.
  The CAL bit stays set in the ADC12D1000 shadow (e.g. 0x20C0 → 0xA0C0 is the last word written).
- Keep a **calibration-pending flag**. Set it when the ADC mode word (§9.2) changes, when an *enabled*
  channel's V/div or offset changes, when the channel's rate class changes between 2 GSa/s (calibration-table mode
  index 0, "single") and below 2 GSa/s (mode index 2, "unite"; spec 10 §7), and when its invert state changes. Before an arm requested by a settings change, by RUN or at boot (spec 03 §3.2), while
  stopped: if the flag is set, run the calibration, program the start delay (spec 03 §3.2 uses the flag),
  then clear the flag. Do not calibrate on the plain re-arm after each frame (spec 03 §3.6).
- Also run it (stop first and re-arm afterwards if acquiring) on setup recall, on the SCPI ADC-calibration
  command, and — only while the Utility quick-cal switch is on — when an ADC's die temperature (TMP422 remote channel) has moved by 3 °C or more (integer °C)
  since the reference reading (`10-calibration.md` §6.1). Never while an acquisition is running.

## 10. Clock chip (boot)

Programmed once at boot by the boot tool before the bitstream is loaded (`13-fpga-configuration.md`);
the application never touches `/dev/spidev1.0`.

- Board HWID[5:4] ≠ 0b11: TI LMK04000-family jitter cleaner (LMK04033 by register format/VCO range,
  **UNVERIFIED** marking). 25 MHz reference, PLL2 N = 40, VCO 2000 MHz, distribution 1000 MHz:
  two LVPECL outputs at 1000 MHz (ADC sample clocks, **UNVERIFIED** assignment), two LVDS outputs at
  100 MHz (PL reference), one output disabled. No lock check by software.
- Board HWID[5:4] = 0b11 (newer clock-chip variant): unidentified paged clock chip; register list in spec 13.
- ADC rate: 1 GHz clock → 1 GSa/s per input non-DES, 2 GSa/s DES (ADC12D1000). Lower rates are PL
  decimation (spec 04).

## 11. Bring-up order

The authoritative start-up order (PL, ADCs, VGAs, relays, phase training, first channel setup, first arm) is
spec 03 §2; this section only names the analog steps in it: ADC type 0x1FB (§9.1), offset-DAC controller init
(§5), VGA init 0x040A to all four VGAs (§4), ADC init ADC 0 then ADC 1 (§9.2/§9.3), ISERDES pulse 0x4F = 1, 0,
relay image prepared (§3.4, no SPI frame yet) and GPIO 343 high, GPIO 373 high (spec 03 §2 step 10a), phase
training (§9.4), then per channel the
channel setup (§11.1), the ADC mode (§9.2) and, before the first arm, the internal calibration (§9.5).
Calibration data (`10-calibration.md` §7) must be loaded before the first channel setup.

### 11.1 Channel setup (any V/div / offset / coupling / BWL / sample-rate change)

For an enabled channel every setup sends the relay image, the VGA word and the ADC full-scale register even
when their values are unchanged (the SPI writes are not cached). Which events re-run the setups of all enabled channels
together with the ADC mode word is the refresh rule of §9.2. After an offset change, a sample-rate change
and a channel-set change (not after a plain V/div change) follow with a VGA "filter pass":
write the VGA word of all four VGAs, enabled or not, in VGA order 1–4 (once is sufficient).

1. Class and relay attenuator bits from V/div (§6.1); send relay image (unless GND).
2. Digital ×2 lane bits (§6.4).
3. VGA step and ADC full-scale code (§6.2); write VGA bits 4:0.
4. DAC attenuator nibble (§3.3), send relay image; offset DAC code (§5), wait 5 ms if changed.
5. ADC full-scale register for the channel's ADC input (§9).
6. If the analog path changed, request ADC internal calibration (§9.5) before the next acquisition.

For a **disabled** channel: apply the relay bits (attenuator from V/div, coupling, BWL, impedance, GND) and the
VGA filter bits immediately; only store V/div and offset — VGA gain bits, offset DAC, ADC full-scale and digital
×2 are written when the channel is enabled. (Because the stored values are already current, enabling a channel
with unchanged values does not by itself request the calibration; the ADC mode change on enabling does.)

Start-up recall (spec 03 §2): every channel may first be set up as enabled (ACQ 0x0B1 = 0xF; VGA gain,
offset DAC 0x8012, full scale on all four), then the channels that are off in the setup switched off one by
one (0xE, 0xC, 0x8, 0), each switch-off rewriting the ADC mode word; or the rule above may be applied
directly. The end state is the same; only the start-up SPI traffic differs.

## 12. Probe attenuation

All of the above uses V/div and offset **at the BNC**. With probe factor P (1X, 10X, 100X, custom):
`Vdiv_BNC = Vdiv_display / P`, `V_off_BNC = V_off_display / P`; displayed values and limits scale by P.
The V/div limits are 0.5 mV … 10 V/div at the BNC. The analog layer only ever receives BNC-referred
values.

## 13. Overload

There is no input-overload detection for C1..C4 on this board: the board has no over-voltage detector for
the inputs, and the device tree has no over-voltage interrupt node. `/dev/overload_irq` (if present) belongs to the optional AWG output protection (spec 15), not to the
scope inputs. Maximum input is 400 Vpk; protection is passive.

## Open

- Physical order of relays and the meaning of relay bits 1/2 (which ÷10 stage) and bit 5 (200 MHz path?):
  measure the relay states with a meter on a unit, or confirm by the BW of a 200 MHz model with BWL off.
- Route-mode semantics behind relay bit 5 (spec 04 owner): trace the hc595 word while changing channel
  count/sample rate.
- Which shipped hardware revisions use ADC12DL1500 / the paged clock chip (HWID bits 4/5); C1..C4 → ADC
  input mapping (inject a signal on C1 and read lanes).
- Clock-chip identity on the newer clock-chip variant (HWID[5:4] = 0b11) and its output frequencies.
- GPIO 343 function (OE vs. reset) and whether a relay image must be sent before it is raised.
- Meaning of acq reg 0x141 values 4/8 and the DAC80501 configuration the PL performs.
- DAC80501 reference/gain (sets the absolute slope); verify slopes against the factory file of a real unit.
- Needs a unit: physical analog behaviour, whether the ADC12DL1500 data interface needs
  0x203 = 1 after training (§9.3).
