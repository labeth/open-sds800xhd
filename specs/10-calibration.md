# 10 — Calibration

This document specifies the analog calibration of the SDS800X HD: where the data lives, the file
formats, the in-memory tables consumed by the V/div and offset mapping of `06-vertical-and-analog.md`,
the user self-calibration procedure, the defaults used when data is missing, and how calibration is
applied at runtime. Trigger-timing (TDC/interpolator) calibration belongs to `03`/`05`; MSO threshold
and skew files are listed here for completeness only.

Notation: channel c = 0..n−1 (n = 4 or 2); front-end group g ∈ {X100 = 0, X10 = 1, X1 = 2} (06 §6.1);
DAC attenuator d ∈ {A1X = 0, A0.5X = 1, A0.25X = 2, A0.1X = 3} (06 §3.3);
VGA step s = 0..20 (06 §4.3); rate mode m: 0 = "single" (channel at the maximum sample rate, 2 GSa/s),
2 = "unite" (below the maximum rate), 1 = reserved (never measured).

## 1. Storage

| Location | Device | Mount | Content |
|---|---|---|---|
| `/usr/bin/siglent/firmdata0/` | eMMC p9 (ext4, "firmdata0") | read-only; remount rw only while saving factory data | factory calibration |
| `/usr/bin/siglent/usr/usr/` | eMMC p12 (ext4, user data) | read-write | user self-cal, skew |

Files (all plain ASCII). Full paths are `<dir>/<file>` with the directories above, e.g.
`/usr/bin/siglent/firmdata0/acq_self_cal_factory_dac.txt` and `/usr/bin/siglent/usr/usr/acq_self_cal_usr.txt`:

| File | Dir | Written by | Read |
|---|---|---|---|
| `acq_self_cal_factory_dac.txt` | firmdata0 | factory DAC calibration (§3) | every boot |
| `acq_self_cal_usr.txt` | usr/usr | user self-cal (§5) | every boot |
| `acq_self_cal_factory_filter.txt` | firmdata0 | factory filter-gain save | never (ignored on this product) |
| `acq_self_cal_factory_mso_level.txt` | firmdata0 | MSO threshold cal | boot (spec 15) |
| `acq_self_cal_factory_mso_delay.txt` | firmdata0 | MSO channel skew | on demand (spec 15) |
| `acq_self_cal_factory_clk_ext.txt`, `acq_self_cal_factory_ext_delay.txt` | firmdata0 | external clock / ext-trigger delay | boot (formats **UNVERIFIED**) |
| `board_skew_data.txt` | usr/usr | channel skew cal (§6.2) | boot |

No calibration data is stored in SPI flash, MTD, EEPROM or the raw "factorydata" eMMC area on this
product. The recovery image ships no calibration files; a unit without files runs on the defaults (§4.3).

Write every file to a temporary name and rename it, and `sync` before reporting success, so that a power
failure never leaves a partial file. Remount firmdata0 read-only afterwards.

Parsing rule: files are read positionally — the reader skips to the next `"`, then
reads values separated by `,` and `;` up to the closing `"`; key names are written but not checked on
read. The firmware must write the exact layout below (line order is the index) and may additionally
check the keys.

## 2. In-memory calibration model

Per channel the firmware holds:

1. **DAC table** `DAC[c][d] = { slope[g] (3 × f32), centre[g] (3 × f32) }` — 96 bytes per channel.
   `slope` = offset-DAC codes per volt at the BNC. Only `slope` is used at runtime (the centre code comes
   from the VGA table).
2. **Full-scale table** `FS[c][j] = { single, unite, reserved }` (f64), j = 0..N−1 (N = 41 for
   ADC12D1000, 11 for ADC12DL1500): relative V/div at ADC full-scale code `FS_min + j·FS_step`
   (06 §6.2), normalised to 1.0 at the default code.
3. **VGA table** `V[c][g][d'][s] = { gain[m] (3 × f32), centre[m] (3 × s32) }`, d' = 0..2 (A1X, A0.5X,
   A0.25X). `gain` = mV/div at the BNC with the nominal ADC full scale;
   `centre` = offset-DAC code that puts 0 V at sample code 2048.

## 3. Factory DAC file `acq_self_cal_factory_dac.txt`

Produced at the factory from 5-point measurements of the offset-DAC transfer with an external meter.

Layout: for impedance I in (`1M`, `50R`), for DAC range D in (`DAC1X`, `DAC0.5X`, `DAC0.25X`,
`DAC0.1X`), for channel k = 1..n, for group G in (`X100`, `X10`, `X1`), one line:

```
<D>,<I>,<G>,CH<k>="code0,val0;code1,val1;code2,val2;code3,val3;code4,val4;"
```

`code` = DAC code (integer), `val` = measured value (integer; input-referred millivolts, **UNVERIFIED**).

Recall (boot): read at most 10000 bytes; use only the `1M` lines. For each (D, k, G) fit a least-squares
line `code = a·(val/1000) + b` over the 5 points and store `slope = a` in `DAC[k−1][D].slope[G]`. The
intercept is discarded. If the file is missing, keep the defaults (§4.3).

Save input (factory command, spec 11): a 2884-byte binary block: 4-byte header (ignored), then for
impedance 1M then 50R: for D = 0..3: `u16 code[4 ch][3 G][5]` followed by `s32 val[4 ch][3 G][5]`
(little-endian). Write the text file from it as above (channels beyond n are not written).

## 4. User self-cal file `acq_self_cal_usr.txt`

### 4.1 Layout

```
VER:4
```

then, per channel k = 1..n in turn, the channel's two full-scale lines immediately followed by its 189 VGA
lines (CH1 FS, FS, CH1 VGA × 189, CH2 FS, FS, CH2 VGA × 189, …; recall is positional, so this order is
mandatory). The two full-scale lines of channel k:

```
CH<k>_UNITE_ADC_FULLSCALE_M="r0,r1,...,r{N-1}"
CH<k>_PART_ADC_FULLSCALE_M="r0,r1,...,r{N-1}"
```

`r_j` = (float32) value × 10⁶, truncated. The first line (`UNITE`) carries column 0 =
`FS[k−1][j].single` (measured with the channel alone, used at 2 GSa/s); the second line (`PART`) carries
column 1 = `FS[k−1][j].unite` (all channels on). The key names are the opposite of the content; keep the
names and the line order for compatibility (recall is positional: first line → `.single`, second →
`.unite`).

The 189 VGA lines of channel k, order G outer (`_100X`, `_10X`, `_1X`), D middle (`DX1_`, `DXP5_`,
`DXP25_`), VGA name inner:

```
FULL_<D>C<k><G>_V<name>="g0,c0,g1,c1,g2,c2"
```

e.g. `FULL_DX1_C1_1X_V16="1078,32786,1078,32786,1078,32786"`. `name` = step s for s ≤ 10, s + 5 for
s ≥ 11 (i.e. the VGA bits 4:0 value: 0..10, 16..25). `g_m` = gain[m] × 1000 truncated (µV/div),
`c_m` = centre[m] (integer DAC code). All numbers are unsigned decimal without zero padding (the reader
parses unsigned integers; negative values are not representable). Self-cal fills only m = 0 (step 5, single)
and m = 2 (step 4, unite) of §5.3; m = 1 is never measured and stays at the 0 of the zeroed self-cal buffer,
so the lines read `g0,c0,0,0,g2,c2`. The firmware MUST write `0,0` in column 1 (m = 1 is not used at
runtime, §7) and MUST ignore column 1 on recall.

### 4.2 Recall

- File missing → keep defaults. First line not `VER:4` → keep defaults ("version mismatch").
- Otherwise parse in order: first FS line → `.single`, second → `.unite` (each ÷ 10⁶), gains ÷ 1000,
  centres as integers; load each channel's block into the tables of §2.

### 4.3 Defaults (no files)

- `DAC[c][d].slope[g]` (defaults, codes/V):

  | d | X100 | X10 | X1 |
  |---|---|---|---|
  | A1X | 18.78 | 194.8 | 1948.4 |
  | A0.5X | 36.24 | 375.5 | 3755.6 |
  | A0.25X | 69.07 | 716.0 | 7161.6 |
  | A0.1X | 187.1 | 1939.1 | 19403.1 |

  When the factory file is missing, the self-cal procedure uses these slopes instead (for the volts/div
  arithmetic of §5.4 only): slope[d][X100] = K_d, slope[d][X10] = 10·K_d, slope[d][X1] = 100·K_d
  with K = {17.3, 34.6, 69.2, 173}; centre 32150.
- `FS[c][j]` all three columns = FSr_min + j·FSr_step: ADC12D1000 0.75 + 0.0125·j (N = 41); ADC12DL1500
  0.625 + 0.0625·j (N = 11).
- `V[c][g][d'][s].gain[m]` = M_g × G0[s] with M = {100, 10, 1} for {X100, X10, X1} and G0 = 10.66 13.41
  16.88 21.20 26.67 33.58 42.26 53.12 66.81 84.20 105.99 1.078 1.375 1.726 2.171 2.725 3.433 4.326 5.417
  6.842 8.587 (s = 0..20); `centre[m]` = 0x8012 (32786).

## 5. User self-calibration

Run with all inputs disconnected (no relay isolates the BNC during self-cal: the cal relay of 06 §3.2 is not used, and GND coupling is not forced). Duration (informative, 4 channels, with the
waits and windows below): ≈ 236 s — step 3 all-channel part ≈ 30 s, single-channel part ≈ 56 s, step 4 ≈ 88 s,
step 5 ≈ 62 s. These figures assume §5.3–§5.4 is followed exactly (windows, waits, three-measurement quick
search); bisecting in every search takes about twice as long.

### 5.1 Constants

Measurements use 16-bit-scaled codes (12-bit code << 4):
`CENTER` = 32768; `KDIV` = 7680 codes/div; `DELTA` = 3·KDIV = 23040 (3 divisions); `TOL` = 3072;
mean-agreement tolerance 512. "Wait w" below means a delay of 20 ms × w.

### 5.2 Mean engine (PL)

Average of one channel (acq regs, spec 02), one complete measurement per channel and probe: reg 0x1AF = N
(window); reg 0x1AE = 0 then 1 (start — every channel's measurement has its own start strobe, also when all
channels are measured at the same DAC probe); poll reg 0x1B1 bit 0 (valid) every 1 ms, up to N + 1 polls; then
reg 0x1B0 = channel select; overflow flags reg 0x1B1 bits 12..15 for C1..C4 (an overflow is a failure, §5.5); sum = reg 0x1B2 | 0x1B3 << 16 | 0x1B4 << 32;
count = reg 0x1B5 | 0x1B6 << 16; mean = (round(sum / count) & 0xFFF) << 4. Repeat up to 4 times until two
consecutive means differ by ≤ 512; use the last.

### 5.3 Sequence

1. Enter: set the self-cal flag (suppresses the GND special-casing of 06 §8.1), preview off, recall the
   factory default setup, time/div enum 16 (50 µs/div, spec 04 §2), memory-depth enum 2 (1M, spec 04 §3), enable
   all channels (route 2 on a 4-channel model, 500 MSa/s by spec 04 §4.4), zero the VGA tables in the self-cal
   buffer. (That self-cal runs at 50 µs/div is UNVERIFIED.)
2. Every channel: enabled, 50 mV/div, VGA step 10 (LG −20 dB), BW full.
3. **ADC full scale.** All channels on, mean window 20, VGA step 10, ADC full scale = default code (the power-up
   code of 06 §9: 0x4000 on ADC12D1000, 0xA000 on ADC12DL1500 — not a table entry `FS_min + j·FS_step`).
   Full centre search (§5.4 steps 1–3) on all channels → reference V/div `g_ref[c]`. For j = 0..N−1: full scale
   code `FS_min + j·FS_step` on all channels, quick centre search (§5.4 step 4, ratio r = r_j) →
   `FS[c][j].unite = g_j / g_ref`. Then mean window **5** and, for each channel alone (others off; VGA step 10,
   default code): full centre search → reference, then each j as before (quick search, r = r_j) →
   `FS[c][j].single`. Restore the default code on all channels. r_j = FSr_min + j·FSr_step (§4.3: 0.75 +
   0.0125·j on ADC12D1000, 0.625 + 0.0625·j on ADC12DL1500), the nominal full-scale ratio of entry j.
4. **VGA tables, unite.** All channels on, default full scale, mean window 20. For d' in (A1X, A0.5X,
   A0.25X): set the DAC attenuator of all channels; for s = 0..20: VGA step s on all channels; centre
   search (full, §5.4 steps 1–3) on all channels → `V[c][X1][d'][s] = {gain[2], centre[2]}`; keep each
   channel's P1/P2 DAC codes of this search per (c, d', s) for step 5. Derive the X10 and X100 entries for the same
   (d', s, m): `gain[X10] = gain[X1] · slope[d][X1] / slope[d][X10]`,
   `gain[X100] = gain[X1] · slope[d][X1] / slope[d][X100]`; centre codes are copied unchanged.
5. **VGA tables, single.** Mean window 5. For each channel alone: default full scale; for each d', s:
   set attenuator and VGA step, quick centre search with the kept step-4 probe codes of the same (c, d', s)
   (§5.4 step 5) → `{gain[0], centre[0]}`.
6. Load the per-channel blocks into the runtime tables (§2), write `acq_self_cal_usr.txt` (§4), restore the user setup,
   clear the self-cal flag, preview on.

Progress reporting (for the UI, spec 09): 0–0.149 step 3, 0.314–0.805 step 4, 0.805–0.996 step 5, 0.99
before saving.

### 5.4 Centre search (per channel, all active channels in parallel)

1. Bisect the offset-DAC code in [lo, hi] = [0x4800, 0xA800]: each step sets code = (lo + hi) >> 1 (so
   the first probe is 0x7800), waits 3, measures the mean m, e = trunc(m − (CENTER + DELTA)). Stop when
   hi − lo ≤ 1 or |e| ≤ TOL → point P1 = (dac1, m1) = (code, m). Otherwise e > TOL → lo = code;
   e < −TOL → hi = code. (Higher DAC code → lower sample code.)
2. Repeat with target CENTER − DELTA, with lo = dac1 and hi = 0xA800 → P2 = (dac2, m2).
3. Interpolate the DAC code for m = CENTER on the line P1–P2, set it, wait 3, measure m.
   Results: `centre = round(dac − (m − CENTER)·(dac1 − dac2)/(m1 − m2))`;
   `volts/div = −((dac2 − dac1)/(m2 − m1)) · KDIV / slope[d][g]`; stored gain = volts/div × 1000 (mV/div).
   Keep per channel P1, P2 and the interpolated code c0 (the code set before the final measurement). If m1 is
   within 10⁻⁴ of CENTER, keep m1 = CENTER + DELTA instead; if m2 is, keep m2 = CENTER − DELTA.
4. **Quick centre search** (step 3, ratio r): exactly three measurements per channel, no bisection; all
   arithmetic in float32, probe codes truncated toward zero:
   - c₊ = trunc(c0 + r·(dac1 − c0)·DELTA / (m1 − CENTER)); set, wait 3, measure m₊ (the +3 div probe).
   - c₋ = trunc(c0 − r·(dac2 − c0)·DELTA / (m2 − CENTER)); set, wait 3, measure m₋ (the −3 div probe).
   - x = trunc(((m₋·c₊ − m₊·c₋) + CENTER·(c₋ − c₊)) / (m₋ − m₊) + 0.5); set, wait 3, measure m.
   Results as in step 3 with (dac1, m1, dac2, m2) = (c₊, m₊, c₋, m₋):
   `centre = trunc(x − k·(m − CENTER) + 0.5)` with k = (c₊ − c₋)/(m₊ − m₋) (k = 0 if m₊ = m₋ within 10⁻⁴);
   `volts/div = −((c₋ − c₊)/(m₋ − m₊))·KDIV / slope[d][g]`. P1, P2 and c0 of the last *full* search are not
   updated by a quick search (every j scales the same reference by r_j). With all channels on, the channels
   are probed in parallel (all c₊ set, one wait, every channel measured; then all c₋; then all x).
5. **Quick centre search, step 5 variant:** as step 4 but c₊ = the P1 code and c₋ = the P2 code kept by
   step 4 for the same (c, d', s), without scaling.

### 5.5 Pass/fail

No per-step failure criterion is defined by the hardware. The firmware should reject a result if the bisection ends at a bracket limit (DAC 0x4800/0xA800) or the
mean engine reports overflow, keep the previous table, and report the channel as failed.

## 6. Other calibrations

### 6.1 ADC internal (temperature-triggered) calibration

Trigger and gate: the check runs from a periodic timer that exists only while the Utility **quick-cal**
switch is ON (the setting is supported on this product). Switching quick-cal on starts the timer, switching
it off stops it; the switch is applied at setup recall/start-up too. Timer period: 1.5 s
(**UNVERIFIED**). With the switch OFF the check never runs. Default: quick-cal OFF (**UNVERIFIED**).
The firmware MUST run the check periodically while quick-cal is on (period 1.5 s; any period up to 60 s is
adequate) and never while it is off. The check:

1. Read the ADC temperatures through hwmon of the TMP422 (I2C 0x4C, spec 15 §5) as integer °C
   (millidegrees / 1000, truncated; 0 if the file cannot be read): ADC 0 = `temp2_input` (remote diode 1);
   ADC 1 = `temp3_input` (remote diode 2), on 4-channel models only (2-channel models use 0 for ADC 1).
2. On the first check (and on the first check after a calibration) the readings become the reference
   T_ref0 / T_ref1; continue with step 3 using the same readings.
3. If |T_ref0 − T0| ≥ 3 or |T_ref1 − T1| ≥ 3 (integer °C, i.e. a change of more than 2 °C): run the ADC
   internal calibration of both ADCs (06 §9.5) and mark the reference stale, so that the next check takes
   a new reference (step 2).

Run the calibration only when acquisition is stopped or between frames. No analog re-calibration is
performed.

### 6.2 Channel skew

Uses the cal relay (06 §3.2 bit 4 = 0) and the internal fast-edge source (spec 15) on all channels,
edge trigger, measures the inter-channel delay, adds it to the channel's horizontal skew σ (spec 04 §9) and stores
it in `board_skew_data.txt` (one float per channel, seconds; format **UNVERIFIED**). Applied at boot. There is no separate delay register: the delay is realised only by the
per-bank record start / fine position of spec 04 §9 (the ADC skew registers 0x09C/0x09D/0x020/0x023 are not used
for it on this board).

### 6.3 Offset-DAC zero search (diagnostic)

Per channel, bisect the DAC over 0..0xFFFF until the mean equals CENTER (wait 3 per step). The result is
left in the DAC and not saved.

### 6.4 ADC interleave

No interleave gain/offset/phase correction is stored. Interleaving at 2 GSa/s is done inside the
ADC12D1000/ADC12DL1500 (DES). The PL data-capture phase is trained once after ADC init (06 §9.4); an ADC mode
change re-applies the stored phase on ADC12DL1500 boards when the DES state changes, and does nothing to the
phase on ADC12D1000 boards (06 §9.4 step 7).

## 7. Runtime application

Boot order: spec 03 §2 step 13a (after register init and the first stop, before the first channel setup of step 14,
06 §11):

1. Load MSO threshold calibration (spec 15).
2. Recall the factory DAC file → DAC slopes (else defaults).
3. Recall external-clock and external-delay data (else their defaults).
4. Recall the user self-cal file → FS and VGA tables (else defaults).
5. Recall `board_skew_data.txt` → channel delays.
6. Apply the channel setup for every channel (06 §11.1).

Lookups per channel setup (06 §6, §5):

| Quantity | Table | Index |
|---|---|---|
| VGA step search | `V[c][g][d'][s].gain[m]` | g from V/div, d' from DAC attenuator rule, m = 0 at 2 GSa/s else 2 |
| ADC full-scale code | `FS[c][j].single` (2 GSa/s) or `.unite` | residual of the step search |
| Offset-DAC centre | `V[c][g][d'][s].centre[m]` | as above |
| Offset-DAC slope | `DAC[c][d].slope[g]` | d from DAC attenuator rule |
| Digital ×2 | none | V/div class 7 (06 §6.4) |

## Open

- Unit of the factory `val` field (mV assumed): needs a factory file from a real unit.
- Formats of `board_skew_data.txt`, `acq_self_cal_factory_clk_ext.txt`, `acq_self_cal_factory_ext_delay.txt`.
- Meaning of rate mode 1.
- The quick-cal timer period (1.5 s assumed) and the default of the quick-cal switch (§6.1):
  time the hwmon reads with quick-cal enabled.
- Whether the BNC must be physically disconnected (no internal isolation found): confirm on a unit.
- The real mean-engine latency per window (and hence the real self-cal duration of §5) needs a unit; the probe
  order and windows of §5.3–§5.4 are confirmed only against the simulator's mean-engine model.
