# 04 — Timebase, Sample Rate and Memory Depth

How the horizontal settings (time/div, memory depth, enabled channels, acquisition mode, bandwidth model) become a
sample rate, an ADC mode, a decimation setting and a record length, and which ACQ-plane registers carry them.
Register names and bit positions are defined in `02-register-map.md`; the apply sequence (configuration banks, arm)
is in `03-acquisition-engine.md`.

## 1. Model

- Two 12-bit ADC devices (`adc0`, `adc1`, `06`) give an aggregate 2 GSa/s. The **route** is chosen from the set of
  enabled analog channels only:

| Route | Enabled-channel mask (bit0 = CH1 … bit3 = CH4) | Max rate per channel |
|---|---|---|
| 0 ("1ch") | none or exactly one channel (0x0, 0x1, 0x2, 0x4, 0x8) | 2000 MSa/s |
| 1 ("2ch") | exactly two channels, any pair (0x3, 0x5, 0x6, 0x9, 0xA, 0xC) | 1000 MSa/s |
| 2 ("4ch") | three or four channels (0x7, 0xB, 0xD, 0xE, 0xF) | 500 MSa/s |

  On 2-channel models only CH1/CH2 exist, so routes 0 and 1 are the only ones reachable.
- The sample rate is a pure table lookup: `rate = table[route][depth_enum][tb_index]` (§4). There is no
  equivalent-time / random-interleaved sampling mode on this product; the shortest time bases are served by
  real-time sampling at the route's maximum rate plus display interpolation (that the PL image has no ETS mode is not
  yet confirmed on a real unit).
- The **record length** (store length, reported by `:ACQuire:POINts?`) is S of §8.1 (`10 × time/div × rate`, rounded to an integer) with the table rate of §4, clamped to [1000 if the product is ≤ 0, 2e8] (§5.4). It is **not** clipped to the depth setting: the
  tables keep it ≤ the depth everywhere except route 2 (4 ch), depth 10k, 5 µs/div, where the rate is 500 MSa/s and
  the record is 25 000 points. A replacement must reproduce this (rate and length from the table, no clipping). The
  FPGA stores a record per enabled channel in the record memory (PS DDR4 reached by the PL through the S_AXI_HP
ports, `12` §4; `03` §1, §5). The displayed span is always 10
  horizontal divisions. (While a record is held stopped, the length reported is that of the stored record; with the
  fixed-depth mode of the acquisition object it comes from the fixed-depth setting, `09`.)
- A fixed sample-rate mode exists in the control plane (`09`): when it is on, the rate comes from the fixed-rate
  setting instead of the tables (§5.3; UNVERIFIED on this product).

## 2. Timebase table

Time/div is an enum in a 1-2-5 sequence; enum 0 = 200 ps/div, enum 38 = 1000 s/div.

| Enum | Time/div | Enum | Time/div | Enum | Time/div |
|---|---|---|---|---|---|
| 0 | 200 ps | 13 | 5 µs | 26 | 100 ms |
| 1 | 500 ps | 14 | 10 µs | 27 | 200 ms |
| 2 | 1 ns | 15 | 20 µs | 28 | 500 ms |
| 3 | 2 ns | 16 | 50 µs | 29 | 1 s |
| 4 | 5 ns | 17 | 100 µs | 30 | 2 s |
| 5 | 10 ns | 18 | 200 µs | 31 | 5 s |
| 6 | 20 ns | 19 | 500 µs | 32 | 10 s |
| 7 | 50 ns | 20 | 1 ms | 33 | 20 s |
| 8 | 100 ns | 21 | 2 ms | 34 | 50 s |
| 9 | 200 ns | 22 | 5 ms | 35 | 100 s |
| 10 | 500 ns | 23 | 10 ms | 36 | 200 s |
| 11 | 1 µs | 24 | 20 ms | 37 | 500 s |
| 12 | 2 µs | 25 | 50 ms | 38 | 1000 s |

Rules the implementation must apply:

- **Minimum time/div**: enum 3 (2 ns/div) on 70 MHz and 100 MHz models, enum 2 (1 ns/div) on 200 MHz models.
  The shortest offered time/div is the first enum e ≥ that minimum with screen
  sample count S(e) ≥ 4 (§5.4) and I(e) · S(e) ≥ 1000 (I = display interpolation factor of §5.4; with XY on, I = 1,
  i.e. S ≥ 1000); with MSO channels open the MSO rate is checked too and the larger enum wins. Lower enums exist in
  the enum but are not offered. The rule is evaluated with the rate of the requested setting only. (Evaluating it
  also with the rate in use and taking the larger enum would let a large jump from a slow time base land above the
  true minimum, e.g. 1 ch 10k at 1 ms/div, R = 1 MSa/s, → `:TIMebase:SCALe 2E-7` would give 500 ns/div: at R = 1
  the first enum with S ≥ 4 and I·S ≥ 1000 is 10, S = 5, I = 400.)
- **Maximum**: enum 38 (1000 s/div).
- **Roll mode** becomes available at enum ≥ 25 (50 ms/div), see §7.
- Horizontal position (trigger delay) and zoom time base are separate settings; zoom uses its own time/div enum with
  the same table and the same rate as the main time base (the record is shared; UNVERIFIED beyond the zoom
  configuration banks of `02` §4.3).

## 3. Memory depth

The depth setting is an enum 0–4. Values in samples per channel:

| Enum | Label | Route 0 (1 ch) | Route 1 (2 ch) | Route 2 (4 ch) |
|---|---|---|---|---|
| 0 | 10k | 10 000 | 10 000 | 10 000 |
| 1 | 100k | 100 000 | 100 000 | 100 000 |
| 2 | 1M | 1 000 000 | 1 000 000 | 1 000 000 |
| 3 | 10M | 10 000 000 | 10 000 000 | 10 000 000 |
| 4 | max, **full set** | 100 000 000 | 50 000 000 | 25 000 000 |
| 4 | max, **limited set** | 50 000 000 | 25 000 000 | 25 000 000 |

- The **limited set** applies when the model bandwidth is not 200 MHz **and** the unit is not an "edu" model.
  I.e. SDS802X/804X/812X/814X HD (70/100 MHz) use the limited set; SDS822X/824X HD (200 MHz) and edu variants use
  the full set.
- The depth menu offered to the user per route is exactly the five labels above (10k, 100k, 1M, 10M, and 100M / 50M /
  25M as the max); any other depth enum maps to 0 samples (invalid).
- In roll mode the depth is not user-selectable: the rate column is that of min(depth, 1M) (example: with the 10M
  setting: 100 ms/div → 1 MSa/s, 200 ms/div → 0.5 MSa/s, S = 1e6 in both, §7).

## 4. Sample-rate tables

### 4.1 Lookup rule

```
tb_index = 0                       if tb_enum < 1
         = min(tb_enum - 1, 38)    otherwise
rate_MSa = TABLE[route][set][depth_enum][tb_index]
```

`set` (full / limited) matters only for route 0 and route 1 at depth enum 4. Rates are in MSa/s per channel. The
tables of §4.2–4.4 are printed **by time/div enum**, i.e. with the `tb_index` shift already applied (row e = table
entry e − 1; rows 0 and 1 are both entry 0). The lookup tables have 39 entries; entry 38 (= 0) is reached
only for tb_enum ≥ 39, which is never offered.
The implementation must reproduce these tables exactly; they are not derivable from a single formula (rates skip
200/250 MSa/s and the 10k column at 4ch has one entry above the depth, §1).

If the sample mode smode (§8.1) is 0 — only while roll is on (§7) — the 1ch table is used for every route.


#### 4.2 Route 1ch

| Enum | Time/div | d0 10k | d1 100k | d2 1M | d3 10M | d4 = 100M (full) | d4 = 50M (limited) |
|---|---|---|---|---|---|---|---|
| 0 | 200 ps | 2000 | 2000 | 2000 | 2000 | 2000 | 2000 |
| 1 | 500 ps | 2000 | 2000 | 2000 | 2000 | 2000 | 2000 |
| 2 | 1 ns | 2000 | 2000 | 2000 | 2000 | 2000 | 2000 |
| 3 | 2 ns | 2000 | 2000 | 2000 | 2000 | 2000 | 2000 |
| 4 | 5 ns | 2000 | 2000 | 2000 | 2000 | 2000 | 2000 |
| 5 | 10 ns | 2000 | 2000 | 2000 | 2000 | 2000 | 2000 |
| 6 | 20 ns | 2000 | 2000 | 2000 | 2000 | 2000 | 2000 |
| 7 | 50 ns | 2000 | 2000 | 2000 | 2000 | 2000 | 2000 |
| 8 | 100 ns | 2000 | 2000 | 2000 | 2000 | 2000 | 2000 |
| 9 | 200 ns | 2000 | 2000 | 2000 | 2000 | 2000 | 2000 |
| 10 | 500 ns | 2000 | 2000 | 2000 | 2000 | 2000 | 2000 |
| 11 | 1 µs | 1000 | 2000 | 2000 | 2000 | 2000 | 2000 |
| 12 | 2 µs | 500 | 2000 | 2000 | 2000 | 2000 | 2000 |
| 13 | 5 µs | 100 | 2000 | 2000 | 2000 | 2000 | 2000 |
| 14 | 10 µs | 100 | 1000 | 2000 | 2000 | 2000 | 2000 |
| 15 | 20 µs | 50 | 500 | 2000 | 2000 | 2000 | 2000 |
| 16 | 50 µs | 20 | 100 | 2000 | 2000 | 2000 | 2000 |
| 17 | 100 µs | 10 | 100 | 1000 | 2000 | 2000 | 2000 |
| 18 | 200 µs | 5 | 50 | 500 | 2000 | 2000 | 2000 |
| 19 | 500 µs | 2 | 20 | 100 | 2000 | 2000 | 2000 |
| 20 | 1 ms | 1 | 10 | 100 | 1000 | 2000 | 2000 |
| 21 | 2 ms | 0.5 | 5 | 50 | 500 | 2000 | 2000 |
| 22 | 5 ms | 0.2 | 2 | 20 | 100 | 2000 | 1000 |
| 23 | 10 ms | 0.1 | 1 | 10 | 100 | 1000 | 500 |
| 24 | 20 ms | 0.05 | 0.5 | 5 | 50 | 500 | 100 |
| 25 | 50 ms | 0.02 | 0.2 | 2 | 20 | 100 | 100 |
| 26 | 100 ms | 0.01 | 0.1 | 1 | 10 | 100 | 50 |
| 27 | 200 ms | 0.005 | 0.05 | 0.5 | 5 | 50 | 20 |
| 28 | 500 ms | 0.002 | 0.02 | 0.2 | 2 | 20 | 10 |
| 29 | 1 s | 0.001 | 0.01 | 0.1 | 1 | 10 | 5 |
| 30 | 2 s | 0.0005 | 0.005 | 0.05 | 0.5 | 5 | 2 |
| 31 | 5 s | 0.0002 | 0.002 | 0.02 | 0.2 | 2 | 1 |
| 32 | 10 s | 0.0001 | 0.001 | 0.01 | 0.1 | 1 | 0.5 |
| 33 | 20 s | 5e-05 | 0.0005 | 0.005 | 0.05 | 0.5 | 0.2 |
| 34 | 50 s | 2e-05 | 0.0002 | 0.002 | 0.02 | 0.2 | 0.1 |
| 35 | 100 s | 1e-05 | 0.0001 | 0.001 | 0.01 | 0.1 | 0.05 |
| 36 | 200 s | 5e-06 | 5e-05 | 0.0005 | 0.005 | 0.05 | 0.02 |
| 37 | 500 s | 2e-06 | 2e-05 | 0.0002 | 0.002 | 0.02 | 0.01 |
| 38 | 1000 s | 1e-06 | 1e-05 | 0.0001 | 0.001 | 0.01 | 0.005 |

#### 4.3 Route 2ch

| Enum | Time/div | d0 10k | d1 100k | d2 1M | d3 10M | d4 = 50M (full) | d4 = 25M (limited) |
|---|---|---|---|---|---|---|---|
| 0 | 200 ps | 1000 | 1000 | 1000 | 1000 | 1000 | 1000 |
| 1 | 500 ps | 1000 | 1000 | 1000 | 1000 | 1000 | 1000 |
| 2 | 1 ns | 1000 | 1000 | 1000 | 1000 | 1000 | 1000 |
| 3 | 2 ns | 1000 | 1000 | 1000 | 1000 | 1000 | 1000 |
| 4 | 5 ns | 1000 | 1000 | 1000 | 1000 | 1000 | 1000 |
| 5 | 10 ns | 1000 | 1000 | 1000 | 1000 | 1000 | 1000 |
| 6 | 20 ns | 1000 | 1000 | 1000 | 1000 | 1000 | 1000 |
| 7 | 50 ns | 1000 | 1000 | 1000 | 1000 | 1000 | 1000 |
| 8 | 100 ns | 1000 | 1000 | 1000 | 1000 | 1000 | 1000 |
| 9 | 200 ns | 1000 | 1000 | 1000 | 1000 | 1000 | 1000 |
| 10 | 500 ns | 1000 | 1000 | 1000 | 1000 | 1000 | 1000 |
| 11 | 1 µs | 1000 | 1000 | 1000 | 1000 | 1000 | 1000 |
| 12 | 2 µs | 500 | 1000 | 1000 | 1000 | 1000 | 1000 |
| 13 | 5 µs | 100 | 1000 | 1000 | 1000 | 1000 | 1000 |
| 14 | 10 µs | 100 | 1000 | 1000 | 1000 | 1000 | 1000 |
| 15 | 20 µs | 50 | 500 | 1000 | 1000 | 1000 | 1000 |
| 16 | 50 µs | 20 | 100 | 1000 | 1000 | 1000 | 1000 |
| 17 | 100 µs | 10 | 100 | 1000 | 1000 | 1000 | 1000 |
| 18 | 200 µs | 5 | 50 | 500 | 1000 | 1000 | 1000 |
| 19 | 500 µs | 2 | 20 | 100 | 1000 | 1000 | 1000 |
| 20 | 1 ms | 1 | 10 | 100 | 1000 | 1000 | 1000 |
| 21 | 2 ms | 0.5 | 5 | 50 | 500 | 1000 | 1000 |
| 22 | 5 ms | 0.2 | 2 | 20 | 100 | 1000 | 500 |
| 23 | 10 ms | 0.1 | 1 | 10 | 100 | 500 | 100 |
| 24 | 20 ms | 0.05 | 0.5 | 5 | 50 | 100 | 100 |
| 25 | 50 ms | 0.02 | 0.2 | 2 | 20 | 100 | 50 |
| 26 | 100 ms | 0.01 | 0.1 | 1 | 10 | 50 | 20 |
| 27 | 200 ms | 0.005 | 0.05 | 0.5 | 5 | 20 | 10 |
| 28 | 500 ms | 0.002 | 0.02 | 0.2 | 2 | 10 | 5 |
| 29 | 1 s | 0.001 | 0.01 | 0.1 | 1 | 5 | 2 |
| 30 | 2 s | 0.0005 | 0.005 | 0.05 | 0.5 | 2 | 1 |
| 31 | 5 s | 0.0002 | 0.002 | 0.02 | 0.2 | 1 | 0.5 |
| 32 | 10 s | 0.0001 | 0.001 | 0.01 | 0.1 | 0.5 | 0.2 |
| 33 | 20 s | 5e-05 | 0.0005 | 0.005 | 0.05 | 0.2 | 0.1 |
| 34 | 50 s | 2e-05 | 0.0002 | 0.002 | 0.02 | 0.1 | 0.05 |
| 35 | 100 s | 1e-05 | 0.0001 | 0.001 | 0.01 | 0.05 | 0.02 |
| 36 | 200 s | 5e-06 | 5e-05 | 0.0005 | 0.005 | 0.02 | 0.01 |
| 37 | 500 s | 2e-06 | 2e-05 | 0.0002 | 0.002 | 0.01 | 0.005 |
| 38 | 1000 s | 1e-06 | 1e-05 | 0.0001 | 0.001 | 0.005 | 0.002 |

#### 4.4 Route 4ch

| Enum | Time/div | d0 10k | d1 100k | d2 1M | d3 10M | d4 = 25M (both) |
|---|---|---|---|---|---|---|
| 0 | 200 ps | 500 | 500 | 500 | 500 | 500 |
| 1 | 500 ps | 500 | 500 | 500 | 500 | 500 |
| 2 | 1 ns | 500 | 500 | 500 | 500 | 500 |
| 3 | 2 ns | 500 | 500 | 500 | 500 | 500 |
| 4 | 5 ns | 500 | 500 | 500 | 500 | 500 |
| 5 | 10 ns | 500 | 500 | 500 | 500 | 500 |
| 6 | 20 ns | 500 | 500 | 500 | 500 | 500 |
| 7 | 50 ns | 500 | 500 | 500 | 500 | 500 |
| 8 | 100 ns | 500 | 500 | 500 | 500 | 500 |
| 9 | 200 ns | 500 | 500 | 500 | 500 | 500 |
| 10 | 500 ns | 500 | 500 | 500 | 500 | 500 |
| 11 | 1 µs | 500 | 500 | 500 | 500 | 500 |
| 12 | 2 µs | 500 | 500 | 500 | 500 | 500 |
| 13 | 5 µs | 500 | 500 | 500 | 500 | 500 |
| 14 | 10 µs | 100 | 500 | 500 | 500 | 500 |
| 15 | 20 µs | 50 | 500 | 500 | 500 | 500 |
| 16 | 50 µs | 20 | 100 | 500 | 500 | 500 |
| 17 | 100 µs | 10 | 100 | 500 | 500 | 500 |
| 18 | 200 µs | 5 | 50 | 500 | 500 | 500 |
| 19 | 500 µs | 2 | 20 | 100 | 500 | 500 |
| 20 | 1 ms | 1 | 10 | 100 | 500 | 500 |
| 21 | 2 ms | 0.5 | 5 | 50 | 500 | 500 |
| 22 | 5 ms | 0.2 | 2 | 20 | 100 | 500 |
| 23 | 10 ms | 0.1 | 1 | 10 | 100 | 100 |
| 24 | 20 ms | 0.05 | 0.5 | 5 | 50 | 100 |
| 25 | 50 ms | 0.02 | 0.2 | 2 | 20 | 50 |
| 26 | 100 ms | 0.01 | 0.1 | 1 | 10 | 20 |
| 27 | 200 ms | 0.005 | 0.05 | 0.5 | 5 | 10 |
| 28 | 500 ms | 0.002 | 0.02 | 0.2 | 2 | 5 |
| 29 | 1 s | 0.001 | 0.01 | 0.1 | 1 | 2 |
| 30 | 2 s | 0.0005 | 0.005 | 0.05 | 0.5 | 1 |
| 31 | 5 s | 0.0002 | 0.002 | 0.02 | 0.2 | 0.5 |
| 32 | 10 s | 0.0001 | 0.001 | 0.01 | 0.1 | 0.2 |
| 33 | 20 s | 5e-05 | 0.0005 | 0.005 | 0.05 | 0.1 |
| 34 | 50 s | 2e-05 | 0.0002 | 0.002 | 0.02 | 0.05 |
| 35 | 100 s | 1e-05 | 0.0001 | 0.001 | 0.01 | 0.02 |
| 36 | 200 s | 5e-06 | 5e-05 | 0.0005 | 0.005 | 0.01 |
| 37 | 500 s | 2e-06 | 2e-05 | 0.0002 | 0.002 | 0.005 |
| 38 | 1000 s | 1e-06 | 1e-05 | 0.0001 | 0.001 | 0.002 |

#### 4.5 Other tables (not used for analog channels on this product)

Digital (MSO) channels use their own table set (depth enums 0–4, ceiling 1000 MSa/s), consulted only by the
minimum-time/div rule of §2 while MSO channels are open. By depth enum, index 0..37 (MSa/s):

| Table | Values for tb_index 0..37 |
|---|---|
| x d0 | 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 500 50 50 50 10 10 5 2 1 0.5 0.2 0.1 0.05 0.02 0.01 0.005 0.002 0.001 0.0005 0.0002 0.0001 5e-05 2e-05 1e-05 5e-06 2e-06 1e-06 |
| x d1 | 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 500 50 50 50 10 10 5 2 1 0.5 0.2 0.1 0.05 0.02 0.01 0.005 0.002 0.001 0.0005 0.0002 0.0001 5e-05 2e-05 1e-05 |
| x d2 | 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 500 50 50 50 10 10 5 2 1 0.5 0.2 0.1 0.05 0.02 0.01 0.005 0.002 0.001 0.0005 0.0002 0.0001 |
| x d3 | 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 500 50 50 50 10 10 5 2 1 0.5 0.2 0.1 0.05 0.02 0.01 0.005 0.002 0.001 |
| x d4 | 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 1000 500 50 50 50 10 10 5 2 1 0.5 0.2 0.1 0.05 0.02 0.01 |

## 5. ADC mode, decimation and peak detect

All registers below are in the ACQ plane (`02` §6). They are written in the timebase-setup group (`03` §3.3).

### 5.1 ADC speed code — `ADC_CTRL` (0x022) bits 14:12

| Table rate R (work mode m of §8.1) | Code |
|---|---|
| 2000 MSa/s (m = 0) | 3 |
| 1000 MSa/s (m = 1; route 1 at full rate, and route 0 at 1000 MSa/s, e.g. 1 ch 10M 1 ms/div) | 2 |
| any other rate (m = 2) | 1 |

Bits 9:8 of the same register are the **switch select** (§5.3); bits 11:10 and 7:0 are preserved.

### 5.2 Down-sampling (decimation) in the FPGA

Let `rate` be the table rate (MSa/s) and `L = 500` (the down-sample limit of this board):

| Register | Value |
|---|---|
| `DS_RATIO` 0x05D (bits 15:0) / 0x05E (bits 27:16 of the ratio, 12 bits) | `rate ≥ L` → 1; else `round(L / rate)` |
| `DECIM_TYPE` 0x004 bits 5:0 | **decimation-ratio index**: `rate ≥ L` → 0; else the index of `round(500 / rate)` in the ratio list below (1 → 0, 2 → 1, 4 → 2, 5 → 3, 10 → 4, …, 500 → 14, …). Independent of the acquisition mode |
| `PEAK_EN` 0x004 bit 8 | 1 if the acquisition mode is peak detect **and** `rate` < L (500) **and** `rate` ≠ 400; else 0 (peak detect has no effect at undecimated rates). Read-modify-write of bit 8 only (the `DECIM_TYPE` write of this table keeps bit 8). Re-evaluated on every acquisition-mode change and on every rate change, after the bank pass (order: bank pass with the `DECIM_TYPE` RMW, then `PEAK_EN`). E.g. 1 ch, 10k, 1 ms/div (1 MSa/s, DECIM_TYPE 14) → 0x004 = 0x10E; switching to 2 GSa/s in peak mode: bank pass 0x004 ← 0x100, then 0x004 ← 0 |
| `DS_DIVISOR` 0x04B (bits 15:0) / 0x04C (bits 27:16, 12 bits) | `round(2000 / rate)` (the ADC-clock-to-sample ratio: 1 at 2G, 2 at 1G, 4 at 500M, 20 at 100M …) |
| `DS_EN` 0x04E | 0 when `DECIM_TYPE` = 0 **and** switch select = 0; else 1 |

`round()` is round-to-nearest of a float. All rates in §4 make both quotients integral. Both 28-bit fields
overflow at the slowest table rates (ratio for rates < 1.9e-6 MSa/s, divisor for rates < 7.5e-6 MSa/s); they are
clamped to 0x0FFF_FFFF (whether the FPGA uses these values in
that range, i.e. in roll mode, is UNVERIFIED). Peak detect is selected by `PEAK_EN` only.

Decimation-ratio list (index 0 … 49; index 0 also for rate > 500); `DECIM_TYPE` and the display compression use the
position of `round(500 / rate)` in it:
1, 2, 4, 5, 10, 20, 25, 40, 50, 100, 125, 200, 250, 400, 500, 1000, 1250, 2000, 2500, 4000, 5000, 1e4, 1.25e4, 2e4,
2.5e4, 4e4, 5e4, 1e5, 1.25e5, 2e5, 2.5e5, 4e5, 5e5, 1e6, 1.25e6, 2e6, 2.5e6, 4e6, 5e6, 1e7, 1.25e7, 2e7, 2.5e7, 4e7,
5e7, 1e8, 1.25e8, 2e8, 2.5e8, 5e8 (index 0 for rate > 500).

### 5.3 Switch select (0x022 bits 9:8)

Non-zero only when the depth enum is 4 (max) or the fixed-sample-rate mode (`:ACQuire:MMANagement FSRate`) is on:

| Route | Condition | Value |
|---|---|---|
| 0 | rate = 1000 MSa/s | 3 |
| 0 | rate < 1000 MSa/s | 2 |
| 1 | rate < 1000 MSa/s | 1 |
| any other case | | 0 |

Route is the route of the channel set being configured (not that of a record held for display). Meaning: selects which ADC lanes feed the record
memory when the record needs the full DDR bandwidth (UNVERIFIED); it also sets the DDR interval Δ of §8.

### 5.4 Display-side helpers

- Screen sample count = record length of §1 = `10 × time/div × rate`, clamped to [1000 (if ≤ 0), 2e8].
- Fewer than 4 samples on screen ⇒ the display switches to interpolated display (sin(x)/x or linear per the
  user setting, `07`).
- **Display interpolation factor I** (used by the minimum time/div of §2, the ACQ timebase set of §8 and the map
  timebase set of `07` §7.8): x = S / 1000 (float32, S = screen sample count). Compare x (exact match within float
  tolerance) against:

  | x | 0.00125 | 0.0025 | 0.005 | 0.01 | 0.0125 | 0.02 | 0.025 | 0.04 | 0.05 | 0.08 | 0.1 | 0.125 | 0.16 | 0.2 | 0.25 | 0.4 | 0.5 | 0.8 |
  |---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
  | I | 800 | 400 | 400 | 200 | 80 | 100 | 40 | 50 | 40 | 25 | 20 | 8 | 25 | 10 | 4 | 5 | 4 | 5 |

  For x = 1.25 / 1.6 / 2.5 / 12.5: I = 4 / 5 / 2 / 2. Any other x: I = 1. (A divider
  d = I · S / 1000 gives I · S / d = 1000 points per screen, 2000 at x = 0.005 and 0.01.) Examples: 1 ch
  (2 GSa/s) at 5 ns/div: S = 100, I = 20; at 2 ns/div: S = 40, I = 50; 4 ch (500 MSa/s) at 5 µs/div, 10k:
  S = 25 000, I = 1.
- Interpolation coefficient window, indexed by the position k of I in the list 1, 2, 4, 5, 8, 10, 20, 25, 40, 50,
  80, 100, 200, 400: start = 0, 0, 2, 6, 11, 19, 29, 49, 74, 114, 164, 244, 344, 544; end = 0, 1, 5, 10, 18, 28,
  48, 73, 113, 163, 243, 343, 543, 943. Other I: start = end = 0.

## 6. Bandwidth-limit FIR — `FIR_SEL` (0x1FF)

Written after every timebase change (after the configuration banks are committed). Value by model bandwidth and
the current rate:

| Model bandwidth | rate = 2000 MSa/s | rate = 1000 MSa/s | other rates |
|---|---|---|---|
| 70 MHz | 5 | 3 | 3 |
| 100 MHz | 4 | 2 | 2 |
| 200 MHz | 0 | 0 | 1 |

Any other bandwidth value → 0.

The lookup uses the model bandwidth (spec 09 §1.2) and the rate of the setting being applied. (The 200 MHz row is
**UNVERIFIED**: not yet confirmed on a 200 MHz unit; check `0x800307FC` on an SDS82xX HD.)

The coefficient sets live in the bitstream; only the selector is written.

## 7. Roll mode

> Optional feature: not needed for a minimum viable firmware (`00` §5).

- Available for time/div enum ≥ 25 (50 ms/div). Entering roll stops the triggered state machine and switches
  the readout to the roll path (`03` §6.4): the FPGA streams decimated samples continuously. The rate is the §4
  table rate at depth enum min(depth, 2) (1M, §3), from the 1ch table for every route (smode = 0, §4.1; UNVERIFIED on a unit).
- **Roll timebase set** (replaces §8.2/§9 while roll is on; same bank sequence): with R, S, Δ, u, D as in §8 (D by
  §8.2 step 5): P = ⌊S / 1000⌋ (S as integer); if P is not a multiple of 4, P = 4·P (14-bit); Lr = ⌈P / 4⌉.
  Write: 0x022 bits 14:12 ← 1; 0x010 bit 15 ← 0 (roll on); 0x1EE ← 10; 0x010 bits 11:0 ← P / u (scan number);
  down-sampling (§5.2); 0x005 ← P / u; 0x092 ← Lr; 0x094 ← Lr; roll full offset: h = (Δ·D − S) / 2,
  o = ⌊h / Δ⌋ (+1 if h − o·Δ > 32), 0x050 ← o & 0xFFFF, 0x051 ← o >> 16; 0x096, 0x098, 0x097, 0x099 ← 0;
  0x01A/0x01B ← D; 0x033 bit 0 ← 0; 0x00A ← 0 (auto trigger off); 0x0A0 ← 4; 0x0B6 ← 0; 0x083 ← 1; 0x082 ← 1;
  0x0C0, 0x0BA, 0x0BE, 0x0B8, 0x03F ← 0; 0x0B1 ← channel mask (re-written); 0x027 per §8.2 step 6; the record-start
  registers take the §9 smode-0 values (0x09A, 0x09B, 0x09E, 0x08C, 0x08E, 0x0A7 ← 0, 0x08A ← 1). The §9
  trigger position and the §8.3 search set are not written (this set replaces them).
  Example (1 ch, 10M setting, 100 ms/div → 1 MSa/s, S = 1e6, P = 1000, u = 2): 0x010 = 0x01F4, 0x005 = 0x1F4,
  0x092 = 0x094 = 0xFA, 0x050 = 0x1C, 0x1EE = 0xA, 0x027 = 1, DS 0x05D = 500.
  Example (1 ch, 10k, 100 ms/div, 10 kSa/s): S = 10 000, P = 40, u = 2: 0x010 = 0x0014, 0x005 = 0x14, 0x092 =
  0x094 = 0xA, D = 0x118, 0x050 = 15, 0x1EE = 0xA.

## 8. Timebase register set (ACQ plane)

Written by every timebase setup (time/div, depth, channel set, acquisition mode, sequence) inside the configuration
bank of each channel n = 0 … C − 1 (C = model channels, `02` §4.3), in the order below, followed in the same bank
window by the position registers of §9, the vertical resize registers (`02` §4.2) and the wave-search set (§8.3).
All values below are the same for every bank except the §9 registers, which depend on the channel's skew (§9). The
FIR selector (§6) is written after the last bank.

### 8.1 Quantities

| Symbol | Definition |
|---|---|
| R | table rate in MSa/s (§4; in fixed-rate mode the fixed rate) |
| S | screen sample count: S = round_half_away(10 · time/div[µs] · R[MSa/s]) computed in float64, ≤ 0 → 1000, > 2e8 → 2e8 (§5.4); all later uses take this integer (held as float32 where a formula multiplies it). `:ACQuire:POINts?` reports S |
| I | display interpolation factor of S (§5.4) |
| m | work mode: 0 if R = 2000, 1 if R = 1000, else 2 (comparisons with the tolerance of §8.4) |
| sw | switch select (§5.3) |
| Δ | DDR interval in samples: sw = 0 → 160 / 80 / 40 for m = 0 / 1 / 2; sw = 1 → 80; sw = 2 or 3 → 160 |
| u | trigger interval unit = Δ / 20 |
| seq | 1 while sequence (segmented) mode is on |
| opt | refresh optimisation: seq = 1 and m = 0 and S = 1000 |
| smode | acquisition sample mode: 0 while roll is on (§7), 1 in every other live and stored setup (the `smp` of `05` §7) |

Rounding: `round()` = round half away from zero of a float32; `⌊ ⌋` = truncation toward zero; all register values are
16 bit (low half of the register word) unless a high register is given.

### 8.2 Values, in write order

1. 0x022 bits 14:12 ← ADC speed code (§5.1); 0x010 bit 15 ← 1 (roll off, §7); down-sampling 0x05D/0x05E, 0x004,
   0x022 bits 9:8 ← sw, 0x04B/0x04C, 0x04E (§5.2).
2. 0x083 ← I. If I < 2: 0x0B6 ← 0, 0x086 ← 0, 0x085 ← 0, 0x090 ← 0, 0x0CE ← 0; 0x0A0 ← 1. Otherwise 0x0B6 ← 1,
   0x086 ← I, 0x085 ← 4·I, 0x090 ← start(I), 0x0CE ← end(I) (coefficient window of §5.4); 0x0A0 ← 4.
3. 0x005 ← 5. 0x096 ← 1 (0 while the ADC calibration mode of `10` is active). Sections: if S > 6250, the
   per-section count N is chosen with x = max(1, ⌊I·S / 1000⌋): N = the first c in (5000, 4000, 6250) with
   c mod x = 0 or x mod c = 0, and N = 5000 if none matches; then 0x098 ← ⌊S / N⌋. Otherwise N = S and 0x098 ← 1.
   Examples: x = 10 → 5000; x = 8 → 5000 (5000 mod 8 = 0); x = 16 → 4000; x = 7 → 5000 (none). With the table
   rates I·S/1000 is an integer whenever S > 6250 (S is a multiple of 1000, or x = 12.5 with I = 2), so the
   round() used by the zoom, stored-record and XY sets (§10–§12, `07` §7.8) gives the same N; the difference
   matters only for non-table (fixed-rate) settings.
4. L0 = ⌊(128 + N) / 4⌋, L1 = ⌊I·N / 4⌋: 0x10E ← Δ/4 + L0; 0x092 ← L0; 0x094 ← L1.
5. **Record depth D** (in Δ-sample units), written unless a held record is shown instead of live data (exact condition UNVERIFIED — the live pass of a `03` §3.7 redraw does write D):
   add = 320 / Δ; if S < 1e8 and R ≥ 500: k = 1.01, else k = 1.001 and add = add + 20; v = ⌊k·S⌋ rounded up to a
   multiple of Δ; D = v/Δ + add, rounded up to a multiple of 8 (minimum 8); if opt: D = 16.
   0x01A ← D & 0xFFFF, 0x01B ← D >> 16.
6. **Segment count** (history / sequence capacity) M: mem = 240 000 000 scaled by (sw, m):

   | sw | m = 0 | m = 1 | m = 2 |
   |---|---|---|---|
   | 0 | ×1 | ×½ | ×¼ |
   | 1 | ×1 | ×½ | ×½ |
   | 2 | ×1 | ×½ | ×1 |
   | 3 | ×1 | ×1 | ×¼ |

   U = 2Δ·D + 100; q = min(⌊mem / U⌋, 80 000); M = q − 1 if mem < U·(q + 1) else q; M = max(M, 1);
   value = 1 if M = 1 else M + 1. 0x027 ← value & 0xFFFF; 0x05B bit 7 ← bit 16 of value (read-modify-write, the
   other bits of 0x05B preserved). In roll mode value = 1. Written under the same condition as step 5.
7. Timeouts 0x00C/0x00D and 0x02A/0x02B (`05` §7).
8. 0x039 bits 3:0 (TDC insert) by time/div enum e: m = 0: e = 1…6 → 8, 7, 0, 5, 2, 2, other e → 4; m ≠ 0:
   e = 1…7 → 8, 7, 0, 5, 2, 3, 4, other e → 6.
9. 0x1C7 ← time/div enum.
10. 0x1EE ← 1 while the acquisition-rate mode is SLOW (`:ACQuire:AMODe SLOW`, `09`); otherwise 0x1A8 if m = 0 and
    S = 1000, else 0x400. A change of the rate mode rewrites 0x1EE at once with the same rule (with the current m and
    S), outside any bank window. Examples: `:ACQuire:AMODe SLOW` → 0x1EE = 1; `FAST` (1 ch, 2 ms/div) → 0x400.

### 8.3 Wave-search set (same bank window, after §9)

0x03D ← 1; 0x082 ← I; 0x0BE ← 0x1C / 0x0C / 0x04 for m = 0 / 1 / 2; 0x0B8 ← 2·I; 0x03F ← 8·I − 1 if I > 1 else 6;
0x0C0 ← 8; 0x0BA ← 6·I − 2 if I > 1 else 6; 0x071 ← I; 0x070 ← 4·I; 0x074 ← start(I), 0x076 ← end(I) (0 and 0 for
I = 1). The stored-record variant (redraw, §12) omits 0x03D and additionally writes 0x118 ← 1.
The display interpolation mode (`:ACQuire:INTerpolation`, sin(x)/x or linear, `07`) is ACQ 0x081 (value 1 at
the default sin(x)/x; the value for linear is UNVERIFIED); it is written inside the bank window by the
interpolation-mode setting and by the live pass after a stored-record redraw, together with the coefficient window
0x090/0x0CE and 0x086/0x085 of §8.2 step 2 (re-written with the same values).

### 8.4 Comparisons and examples

Equality tests on rates and counts (R = 2000, R ≥ 500, S = 1000, S < 1e8) use a tolerance of 0.01 for values in
[1000, 10 000), 0.1 for ≥ 10 000, 0.001 for [100, 1000) and so on by decade; with the table rates this is plain
equality.

| State | S | I | Δ | D (0x01A) | 0x092 | 0x094 | 0x10E | 0x098 | 0x027 | 0x039 |
|---|---|---|---|---|---|---|---|---|---|---|
| 1 ch, 2 GSa/s, 200 ns/div, 10k | 4000 | 1 | 160 | 0x20 | 0x408 | 0x3E8 | 0x430 | 1 | 0x5AAA | 4 |
| 2 ch, 1 GSa/s, 200 ns/div, 10k | 2000 | 1 | 80 | 0x20 | 0x214 | 0x1F4 | 0x228 | 1 | 0x59CC | 6 |
| 4 ch, 500 MSa/s, 1 µs/div, 10k | 5000 | 1 | 40 | 0x88 | 0x502 | 0x4E2 | 0x50C | 1 | 0x1558 | 6 |
| 4 ch, 500 MSa/s, 500 ns/div, 10k | 2500 | 2 | 40 | 0x48 | 0x291 | 0x4E2 | 0x29B | 1 | 0x27FE | 6 |

## 9. Horizontal position and trigger position (per bank, after §8.2)

Inputs: §8.1 plus D (§8.2 step 5), τ = horizontal delay in µs as `:TIMebase:DELay` (positive τ moves the trigger
point left, i.e. reduces the pre-trigger count), σ = the channel's horizontal skew in µs (below), B = 4,
A = 0x20 if opt else 0x40. Records are positioned with the delay range the control plane allows (`09` §3.3:
running −5 … +10 000 div; stopped limits scaled by t_r / t_c).

**Channel skew σ** (per bank n): −(user deskew of channel n, `:CHANnel<n>:SKEW`); − 12 ns in peak-detect mode at
R ≤ 100; + 2 ns while the trigger type is Dropout (API type enum 6 of `05`; not FPGA type code 6, which is Interval), so entering or leaving Dropout re-runs the position pass (`05` §5.10); + the factory skew of `10` when R < 2000. With all of these 0 the §9
values are identical in every bank. Skew has no register of its own: it acts only through these per-bank §9 values (example:
`:CHANnel1:SKEW 1.5E-9` at 1 GSa/s changes only bank 0's 0x08E/0x08A/0x0A7; −2 ns on CH2 moves bank 1's record
start 0x09A/0x08C/0x09E by one block). The ADC skew registers 0x09C/0x09D/0x020/0x023 are not written on this board.

**Record start.** X = (Δ·D − S)·I / 2. s = ⌊X − A·I − 32·I − I·σ·R⌋ (as unsigned 32 bit);
start = ⌊s / (Δ·I)⌋; rem = s − start·Δ·I; r = ⌊rem / (I·B)⌋; if r < 6 and s ≥ Δ·I: start = start − 1 and
r = r + Δ/B. a1 = A·I / B, plus I if rem ≠ r·I·B (before the adjustment). Write 0x005 ← 5; 0x09A ← start & 0xFFFF,
0x09B ← start >> 16; 0x08C ← 2·start; 0x09E ← r; 0x08E ← a1; 0x08A ← a1 + 1. (With smode = 0, i.e. in the roll set of §7: 0x09A/0x09B/0x09E/0x08C/0x08E ← 0 and 0x08A ← 1.)
The per-channel quantity p = ⌊(X − 32·I − I·σ·R) / (Δ·I)⌋ and its remainder are kept for the readout (`03` §4.3).

**Trigger position.** F = −32 if m = 0; −28 (−32 if sw ≠ 0) if m = 1; −26 (−28 if sw ≠ 0) if m = 2; while
decimating (§5.2 DECIM_TYPE ≠ 0) F = 3·R/500 − 31. c = 1.5 if m = 0 and the trigger coupling is HF or LF reject
(`05`), else 0. h = (Δ·D/2 − R·τ + F)·I − I·c·R/1000; v = round(h) (half away from zero); q = I·u.

- v ≥ 0: pre = ⌊v / q⌋, delay = 0, f = v − pre·q.
- v < 0: pre = 0, delay = ⌈−v / q⌉, f = (v mod q) + q if v mod q ≠ 0, else 0 (v mod q with the sign of v).

0x007 ← delay & 0xFFFF, 0x025 ← delay >> 16; 0x006 ← pre & 0xFFFF, 0x028 ← pre >> 16. Fine position, with
k = rem − r·I·B of the record start (before the adjustment):
- I = 1: 0x0A7 ← f, or f − k + 4 if k ≠ 0; 0x0A8 ← 0; 0x0A9 ← 0.
- I > 1: g = f, or f + 4·I − k if k ≠ 0; 0x0A7 ← 0; 0x0A8 ← ⌊g / 4⌋; 0x0A9 ← g mod 4 (sign of g).

Examples (τ = 0, σ = 0): 1 ch 2 GSa/s 200 ns/div: 0x09A = 2, 0x08C = 4, 0x09E = 0x24, 0x08E = 0x10, 0x08A = 0x11,
0x006 = 0x13C; 4 ch 500 MSa/s 500 ns/div (I = 2): 0x09A = 1, 0x08C = 2, 0x09E = 0xD, 0x08E = 0x22, 0x08A = 0x23,
0x006 = 0x2C3, 0x0A8 = 1.

## 10. XY record set

> Optional feature: not needed for a minimum viable firmware (`00` §5).

XY mode value x: 0 = off, 1 = XY on with the XY-only display, 0x10 = XY on with the YT+XY display
(`:ACQuire:MODE XY` gives 1; the YT+XY display is UNVERIFIED). On every
XY switch and every acquisition-mode change: 0x199 ← channel pairs active (1 = a channel of CH1/CH2 is on and none
of CH3/CH4, 4 = only CH3/CH4, 5 = both pairs, 0 = none; XY plots CH1 (X) against CH2 (Y) and CH3 against CH4 —
pair semantics from the encoding, UNVERIFIED on a unit), then 0x198 ← x and MAP 0x192 ← x (`07` §7.7), then the map
reset pulses and a re-arm.

Full order of an XY switch (`:ACQuire:MODE XY` / back to YT, e.g. 2 ch 1 GSa/s 200 ns/div): stop request;
ADC mode refresh (`03` §3.3 step 2: ADC mode words, 0x07D ← 0x102, 0x002); the timebase pass with the XY set below
(XY off: the normal set) and the map sets (`07` §7.7); the channel setups of every channel (`06` §9.2: VGA words,
relay frame, ADC channel words); 0x199, 0x198, MAP 0x192; two picture-clear blocks (`07` §7.2) and the soft reset
(`03` §3.5); the stored-record redraw (`03` §3.7); the arm with start delay 250 (XY on; `03` §3.2); then the XY
pixel mapping MAP 0x0C4 ← 0x1E0, 0x0C3 ← 0x1E0, 0x0C5 ← 0x138 (XY off: the YT mapping, `07` §7.7).

With XY on, after the §8–§9 writes of bank n (XY-only display), or in banks 4 … 4 + C − 1 holding only these writes
(YT+XY display), write: S_xy = 2·S. Sections: if S_xy > 6250 (the multi-section test of §8.2 step 3), P = the
first of 5000, 4000, 6250 that divides or is a multiple of round(S_xy / 1000) (5000 if none; note: no factor I and
round instead of ⌊I·S/1000⌋) and parts = ⌊S_xy / P⌋; otherwise P = ⌊S_xy⌋ and parts = 1. Write order inside the
bank window: 0x198 ← x (the XY mode value above, in every bank); 0x005 ← 5; parts → 0x098 (XY-only) or 0x09F with
0x095 ← 1 before it (YT+XY; 0x095 ← 0 while the ADC calibration mode is active); 0x10E ← Δ/4 + ⌊(128 + P)/4⌋;
0x092 ← ⌊(128 + P)/4⌋; 0x094 ← ⌊P/4⌋; 0x083 ← 1; 0x0B6 ← 0; 0x086, 0x085, 0x090, 0x0CE ← 0; 0x0A0 ← 1.
Example (2 ch, 1 GSa/s, 200 ns/div, S = 2000, P = 4000): 0x092 = 0x408, 0x094 = 0x3E8, 0x10E = 0x41C (written after
the normal values 0x214 / 0x1F4 / 0x228 in the same bank). The record then holds the X and Y channels' samples for S
points each (their layout in the record is UNVERIFIED). The MAP side is `07` §7.7–§7.8. The YT+XY variant is
UNVERIFIED.

## 11. Zoom timebase set (banks 4 + n)

> Optional feature: not needed for a minimum viable firmware (`00` §5).

Written in bank 4 + n for every model channel n when zoom is on or the zoom time/div changes (`03` §6.5), after the
main banks. Inputs: t_main, t_zoom (time/div), S of §5.4 for the main time base, k = t_main / t_zoom, S_z = S / k.
If S_z < 4 (the "too few points" test; the I·S ≥ 1000 condition of §2 belongs only to the minimum-time/div rule)
nothing is written. Otherwise n_z = round(min(S_z, S));
I_z = interpolation factor of §5.4 for S_z, except that S_z = 12 or 13 exactly gives I_z = 80 (x is taken as
0.0125); I_z ≥ 1. ppp = round(I_z·n_z / 1000) (÷ k when k < 1), clamped to 1 … 100 000 000 (0 → 1). Sections:
if n_z > 6249 and n_z ≠ 6250, P = the per-section count (§8.2 step 3 list) for ppp and parts = ⌊n_z / P⌋;
otherwise P = n_z and parts = 1. Write in
this order: down-sampling (§5.2) and 0x022 speed (§5.1) for the rate; 0x083 ← I_z; if I_z < 2: 0x0B6, 0x119, 0x086,
0x085, 0x090, 0x0CE ← 0 and 0x0A0 ← 1; else 0x0B6 ← 1, 0x119 ← I_z, 0x086 ← I_z, 0x085 ← 4·I_z, 0x090/0x0CE ←
start/end(I_z) (§5.4), 0x0A0 ← 4; 0x097 ← 1 (zoom part enable); 0x099 ← parts; 0x10E ← Δ/4 + 0x092;
0x092 ← ⌊(128 + P)/4⌋; 0x094 ← ⌊I_z·P/4⌋; 0x039 (TDC insert, §8.2 step 8) for the zoom time/div. Then the zoom
record start, the resize with the zoom gain/centre of channel n (`02` §4.2) and the wave-search set §8.3 with I_z.

Default zoom time/div (after zoom on, before any `:TIMebase:WINDow:SCALe`): 200 ns/div (n_z = 2000 at
1 GSa/s under a 1 ms/div main time base; host default `2.00E-07`, `11`).

**Zoom record start**: the §9 record-start rule with S = n_z and I = I_z (Δ, D, A, σ of the main time base), with
X = (Δ·D − n_z)·I_z/2 + R·(τ_z − τ)·I_z (τ_z = zoom delay `:TIMebase:WINDow:DELay`, µs, τ = main delay; positive moves
the window to later samples): 0x09A/0x09B ← start, 0x08C ← 2·start (16 bit), 0x09E ← r, 0x08E/0x08A as §9. The trigger
position 0x006/0x007/0x025/0x028 is not written in zoom banks (main values). Example (1 ch 10M, main 1 ms/div at
1 GSa/s, Δ = 80, τ = 0): zoom 200 ns/div (n_z = 2000) 0x09A = 0xF68A, 0x08C = 0xED14, 0x09E = 6; 20 µs/div
(n_z = 2e5) 0x09A = 0xF1B4, 0x08C = 0xE368, 0x09E = 0x10; 100 µs/div (n_z = 1e6) 0x09A = 0xDE2C; same with
τ_z = 1 ms: 0x09A/0x09B = 0x0F00/0x1 (start + 12 500 = +R·τ_z/Δ). The sign of the τ term is UNVERIFIED (only τ = 0
is confirmed).
Example (1 ch, 10k, main 1 ms/div at 1 MSa/s, S = 10 000; zoom 20 µs/div: k = 50, S_z = 200, I_z = 10):
0x083 = 0x119 = 0x086 = 0xA, 0x085 = 0x28, 0x090 = 0x13, 0x0CE = 0x1C, 0x092 = 0x52, 0x094 = 0x1F4, 0x10E = 0x5C,
search 0x082 = 0xA, 0x0B8 = 0x14, 0x03F = 0x4F, 0x0BA = 0x3A, 0x071 = 0xA, 0x070 = 0x28, 0x074 = 0x13, 0x076 = 0x1C.

## 12. Stored-record set (redraw of a held record, `03` §3.7)

> Optional feature: not needed for a minimum viable firmware (`00` §5).

Written in banks 0 … C − 1 by the stored bank pass of `03` §3.7 (and in banks 4 … 4 + C − 1 with the XY display
on). It describes the record already in memory, seen with the current time/div. Inputs: the record's rate R_r,
screen count S_r (§8.1 for the record's time/div at R_r), depth D_r (§8.2 step 5 with R_r, S_r), Δ_r and m_r
(§8.1 for R_r and the record's switch select), A of §9 (for R_r, S_r); the current time/div t_c and
S_v = 10·t_c[µs]·R_r (the record's samples that span the current screen). I(x) below is the zoom-style factor of
§11 (§5.4 with the 12/13 special case): I_r = I(S_r), I_v = I(S_v); with the XY display on (record ×2), I_v = 1.
fx = t_r / t_c, the record's time/div ÷ the current time/div (0.2 for a 200 ns/div record viewed at 1 µs/div; the
same fx as `07` §7.8). B = 4.

If S_v < 4 (the "too few points" test, as §11) nothing is written. Otherwise:

- n = round(S_r) and q = I_v·S_r / 1000 if S_r ≤ S_v, else n = round(S_v) and q = round(I_r·S_r / 1000);
  ppp = round(q / fx), clamped to 1 … 1e8.
- Sections: n > 6249 and n ≠ 6250 → P = per-section count (§8.2 step 3 list) for ppp, parts = ⌊n / P⌋;
  otherwise P = n, parts = 1.

**Timebase part**, in this order: down-sampling (§5.2) and 0x022 speed (§5.1) for R_r; 0x083 ← I_v; interpolation
set for I_v (§8.2 step 2: 0x0B6, 0x086, 0x085, 0x090, 0x0CE) and 0x0A0 ← 1 (I_v < 2) or 4; 0x005 ← 5;
0x01A/0x01B ← D_r; 0x096 ← 1 (0 in ADC calibration mode); 0x098 ← parts (XY display: 0x095/0x09F as §10);
0x092 ← ⌊(128 + P)/4⌋; 0x094 ← ⌊I_v·P/4⌋; 0x10E ← Δ_r/4 + 0x092. Not written in this pass: 0x027, 0x00C/0x00D,
0x02A/0x02B, 0x039, 0x1C7, 0x1EE and the trigger position 0x006/0x007/0x025/0x028. In run mode the live values
stay; while stopped, a horizontal-delay change writes the trigger position just before this pass with the §9 rule
evaluated for the record (R_r, D_r, S_r, I_r) and the current τ (`03` §3.3 "While stopped"; example: 1 ch 2 GSa/s
200 ns/div record, τ = 0.1 µs: 0x006 = 0x123, D_r = 0x20).

**Search part** (§8.3 with I = I_v, without 0x03D): 0x082, 0x0BE (for m_r), 0x0B8, 0x03F, 0x0C0, 0x0BA, 0x071,
0x070, 0x074, 0x076; then 0x118 ← 1. Then 0x010 ← 0x8000 (roll off).

**Position part** (per bank n, skew σ of channel n as §9 with I = I_v): X(S) = (Δ_r·D_r − S)/2;
ρ = I_v·μ_1 + ⌊(I_v / I_r)·μ_2⌋, with μ_1, μ_2 the integer and sub-sample fine position kept with the record
(its trigger-interpolation result; source UNVERIFIED, 0 when the frame's fine value is 0; typical values
ρ = 1 … 3 at Δ_r = 40 and ρ = 0 at Δ_r = 160); g = round(R_r·τ_Δ·I_v) (half away from zero; τ_Δ = current
horizontal delay − delay when the record was taken, µs; example: 2 GSa/s 200 ns/div record (Δ_r = 160, D_r = 0x20,
A = 0x40) viewed at 100 ns/div: τ_Δ = 0 → 0x09A = 9, 0x09E = 6; τ_Δ = +0.1 µs (g = 200) → 0x09A = 0xA, 0x09E = 0x10,
ρ = 0); d = ⌊R_r·σ·I_v⌋.
- s0 = ⌊(X(S_r) − A − 32)·I_v⌋ − ρ − d; s1 = ⌊(X(S_v) − A − 32)·I_v⌋ − ρ + g − d;
  s = max(s1, s0) if s0 ≥ 1, else max(s1, 0).
- start = ⌊s / (Δ_r·I_v)⌋, rem = s − start·Δ_r·I_v, r = ⌊rem / (I_v·B)⌋, k = rem − r·I_v·B. If rem < I_v·B and
  start > 1: start = start − 2, r = r + 2·Δ_r/B. (This differs from the live rule of §9.)
- a1 = ⌊I_v·A / B⌋, plus I_v if k ≠ 0.
- Fine: I_v = 1: 0x0A7 ← I_v·B − k if k ≠ 0, else 0; 0x0A8 ← 0; 0x0A9 ← 0. I_v > 1: 0x0A7 ← 0; with
  w = I_v·B − k if k ≠ 0, else 0: 0x0A8 ← ⌊w / B⌋, 0x0A9 ← w mod B.
- Write: 0x09A ← start & 0xFFFF, 0x09B ← start >> 16; 0x08C ← 2·start; 0x09E ← r; 0x08E ← a1; 0x08A ← a1 + 1;
  0x0A7, 0x0A8, 0x0A9.
- For a readout of the held record (`03` §4.3) the firmware keeps P_r = max(s0, s1) + I_v·A per channel:
  p = ⌊P_r / (Δ_r·I_v)⌋ and the remainder replace p and P − p·Δ·I of `03` §4.3.

**Resize part** (`02` §4.2, per channel n): κ = (V/div of the record ÷ current V/div of channel n); y_r, y_c = the
channel's vertical position in 12-bit screen codes (0x800 = centre, 480 codes per division) when the record was
taken and now. 0x1E1/0x1E2 ← ⌊16384·κ⌋; 0x1DF/0x1E0 ← ⌊(y_c − 0x800) − κ·(y_r − 0x800)⌋ (for an inverted channel
⌊(0x800 − y_c) − κ·(y_r − 0x800)⌋), 32-bit two's complement; 0x1E4 ← 0xF7F; 0x1E3 ← 0x080. (The identification of
κ, y_r and y_c is UNVERIFIED; it gives 0x1E1 = 0x8000 after 1 → 0.5 V/div and 0x1DF = 0xC0 after an offset
change of +0.4 div.) The live pass restores
0x4000 / 0.

Example (1 ch at 1 ms/div, 100k and 1M records, Δ_r = 40, I = 1, A = 0x40, S_v = S_r): the live pass writes
0x09E = 6, 0x08E = 0x10, 0x08A = 0x11 (rem = 24, k = 0) and the stored pass 0x09E = 5, 0x08E = 0x11, 0x08A = 0x12,
i.e. rem = 24 − ρ with ρ ∈ 1 … 3 (k ≠ 0). 1 ch 10k 200 ns/div held, viewed at 1 µs/div (S_r = 4000 ≤ S_v = 20 000): D = 0x20,
0x098 = 1, 0x092 = 0x408, 0x094 = 0x3E8, 0x10E = 0x430, 0x09A = 2, 0x08C = 4.

## Open

- What the PL does with the §8/§9 values (units of 0x092/0x094/0x10E, meaning of 0x0A7–0x0A9) is not yet
  confirmed on a real unit; the formulas give a consistent set of values, but whether other values would also
  work needs a unit.
- Whether the configuration banks latch every §8/§9 register per channel or only some
  needs a unit; the write rule a replacement follows is `02` §4.3.
- Route 2 (4 ch), depth 10k, 5 µs/div records 25 000 points (§1); whether the PL record memory really holds the
  extra points or the readout wraps needs captured data.
- The interpolation-factor computation used by the minimum-time/div rule (§2) is not yet confirmed on a real unit.
- How the PL uses the roll scan number and full offset (§7) is not yet confirmed on a real unit.
- Zoom set (§11) at the default zoom scale right after zoom on: one state (1 ch 10k, 1 ms/div) has
  0x083 = 0x190, 0x092 = 0x22, 0x094 = 0x1F4, 0x099 = 1, which does not fit §11 (0x092 implies P = 8, 0x094 P = 5);
  another (1 ch 2 µs/div at ≥ 100k) has 0x099 = 1, 0x092 = 0x408, 0x094 = 0x3E8, 0x10E = 0x430 = §11 with
  P = 4000. The first state is unexplained. XY sample layout in the record and the YT+XY variant (§10) are not yet confirmed.
- Minimum time/div per bandwidth (§2) is not yet confirmed on a unit (the SDS800X HD datasheet lists 1 ns/div for
  all models; UNVERIFIED for 70/100 MHz).
