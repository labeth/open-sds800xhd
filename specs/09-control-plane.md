# 09 — Control Plane

What the firmware must keep as instrument state, how a setting change reaches the hardware,
what is persisted, and how the unit identifies itself. Hardware register details are in the owning
specs (02–06, 07, 08); this spec fixes the structure, value ranges and the ordering rules.

---

## 1. Product identity and capabilities

### 1.1 Identity chain

| Item | Source | Value on this product |
|---|---|---|
| Product family | fixed | SDS800X HD; 4-channel and 2-channel models use the same constants of spec 04 |
| PL register set | — | spec 02 |
| Board variant | HWID straps (spec 13 §4.1): (HWID & 0x30) == 0x30 → newer clock-chip variant, else the original clock chip | per unit |
| Channel count | HWID bit 7 (sysfs GPIO 341; spec 02 §3, spec 13 §4.1): 1 → 2 channels, 0 → 4 channels. No PL register carries the channel count on this product (ACQ 0x059 is not accessed) | SDS8x2X HD = 2, SDS8x4X HD = 4 |
| ADC resolution | fixed for the type | hardware 12 bit, data path 16-bit words |
| Screen | fixed for the type | 1024 × 600, "7-inch" branch of the backlight formula (spec 07 §6) |
| Model string, serial, bandwidth, options | factory data on `firmdata0` (eMMC p9, read-only): `NSP_system_info.xml`, `NSP_sn_bandwidth.xml`, `NSP_trends_config_info.xml` (manufacturer/brand) | see §1.2 |
| Version string (product information id 1) | built once at start-up as `<boot>.<rootfs>.<kernel>.<app>`: `<boot>` = value after `boot_version=` in `/proc/cmdline` (U-Boot appends `uboot_version=4`), `<rootfs>` = integer after `rootfs_version=` in `/etc/version`, `<kernel>` = value after `siglent_version=` in `/proc/version` up to `@` (`0` if absent), `<app>` = application version | e.g. `4.8.14.<app>` (**UNVERIFIED** on a unit); exact rules and `*IDN?` layout in spec 11 §6.6 |
| Defaults without factory data | the fallback model of §1.2 and a placeholder serial, never reported as a real identity | — |

Model names: SDS802X HD / SDS804X HD (70 MHz), SDS812X HD / SDS814X HD (100 MHz), SDS822X HD /
SDS824X HD (200 MHz); last digit = channel count. All six share the same hardware path and firmware;
only the channel count (hardware strap) and the bandwidth (factory data) differ.

### 1.2 Bandwidth and options

The bandwidth class is a firmware limit applied through the analog front end's bandwidth-limit
settings (spec 06), selected from the factory data in `firmdata0`. There is no bandwidth-upgrade
option on this product (bandwidth comes from the factory data only); other installable options are
supported. The exact factory-file fields are **UNVERIFIED** (parse
`NSP_sn_bandwidth.xml` / `NSP_system_info.xml` from a unit's p9). License keys
and their checking are out of scope for this spec. Bandwidth type values (also the model-name slot
index of the identity record below): 9 = 70 MHz, 3 = 100 MHz, 5 = 200 MHz (spec 04 §3, §6).

**Identity record** `sys_cfg.cfg` / `sys_cfg_4ch.cfg` / `sys_cfg_2ch.cfg` on `firmdata0` (0xCF8 bytes, read at
start-up). Encoding: the decoded record r (n = 0xCF8 bytes) carries a checksum: u32 r[0..3] + Σ r[4..n−1] ≡ 0
(mod 2³²). Decode the file bytes f as follows (h = n/2): t = copy of f; for i = 0 … n/4−1 swap t[i] ↔ t[h−1−i]
and t[h+i] ↔ t[n−1−i]; r[0..h−1] = t[h..n−1]; r[h..n−1] = t[0..h−1]; invert every byte of r[h..n−1]; then invert
r[p] for every triangular p = 1, 3, 6, 10, … < n. A writer applies the inverse. Fields: manufacturer string @0x08,
prefix @0x28, brand @0x3C, series prefix @0x58 ("SDS"), vendor @0x5C, language mask u16 @0xA4 and default index u8
@0xA6, three model-name tables of ten 15-byte slots @0xA8, 0x140, 0x2D6 (slot index = bandwidth type), bit-name
table @0x1D6. Which field selects the slot on a real unit is **UNVERIFIED**.

**Resolution order** (model string → bandwidth → depth set `04` §3, minimum time/div `04` §2,
FIR selector `04` §6, `*IDN?`):
1. the model string from the identity record above (slot of the bandwidth type, if that field is known), else
2. the model string in `NSP_system_info.xml` / `NSP_sn_bandwidth.xml` (field names **UNVERIFIED**), else
3. fallback: `SDS80<C>X HD` with C = channel count from HWID bit 7 and bandwidth 70 MHz (the most conservative
   class: limited depth set, 2 ns/div minimum, FIR 5/3).

The bandwidth is derived from the model number: SDS80xX → 70 MHz, SDS81xX → 100 MHz, SDS82xX → 200 MHz.

### 1.3 Capabilities

| Capability | Present |
|---|---|
| Digital channels (MSO, external probe) | yes (spec 15) |
| Built-in AWG | no |
| Counter | yes |
| DVM / waveform histogram | no |
| Installable options | yes (bandwidth upgrade: no) |
| Quick-action key (code 47) | yes (spec 08 §2.2) |
| LED brightness adjustable | no |
| Key press type from FPGA (long press) | no (spec 08 §4) |
| Low-frequency buzzer variant | no |
| Knob period | 75 |

---

## 2. Instrument state model

The firmware must at least hold the following groups; every field is persisted in
the setup file (§4) unless marked volatile.

| Group | Fields (minimum) | Hardware owner spec |
|---|---|---|
| Channel n (2 or 4) | on/off, V/div, offset, coupling (DC/AC/GND), impedance, probe ratio, bandwidth limit (FULL / 20 MHz; `200M` is accepted by SCPI, effect **UNVERIFIED**, spec 06 §8.2, spec 11 §6.7), invert, label, unit (V/A), selected-channel pointer for the shared vertical knobs | 06 |
| Timebase | s/div, horizontal delay (trigger position), zoom on/off + zoom s/div + zoom delay, roll mode, XY mode | 04, 07 §7.4 |
| Acquisition | run state (run/stop/single), mode (normal/peak; average/ERES are not acquisition modes on this product — they are math operators with their own count / bits, spec 03 §6.1), memory depth, sequence (segments), interpolation (§3.3), acquisition-rate mode FAST/SLOW (§3.3) | 03, 04 |
| Trigger | type, source, slope/condition, level(s), coupling, holdoff, noise reject, mode (auto/normal/single), serial-trigger settings | 05 |
| Display | display type, colour grade, persistence, intensity, graticule brightness, grid style, transparency, backlight, LED brightness (§2.1) | 07 |
| Measure / cursors / math / ref / decode / search / mask test / history | as needed by the UI | 05, 07, 15 |
| Utility | sound on/off, language, power-on mode (§4.3), screen saver, network | 08, 11 |
| Volatile | trigger status, frame counters, LED word (derived), last key events | — |

### 2.1 Display group (ranges and defaults)

| Setting | Range | Default | Hardware effect |
|---|---|---|---|
| Display type | VECTor (0) / DOT (1) | VECTor | spec 07 §7.1 idx 0x125 |
| Colour grade | off/on | off | idx 0x0E7 |
| Persistence | OFF, INFinite, 1 s, 5 s, 10 s, 30 s (SCPI also lists 100/200/500 ms) | OFF | spec 07 §7.2 |
| Intensity | 0…100 % | 50 | idx 0x0FE = value + 16 |
| Graticule brightness | 0…100 % | 20 | grid colour idx 0x0D0/0x0D1 |
| Grid style | FULL (0) / LIGHt (1) / NONE (2) | FULL (**UNVERIFIED** default) | drawn by the UI |
| Transparency (menus) | 20…80 % | 60 | idx 0x0FA = trunc(p/100 × 255) |
| Backlight | 0…100 % | 50 | spec 07 §6 |
| LED brightness | 10…100 % | 60 | not adjustable on this model; no register (spec 08 §6.2) |

Out-of-range requests are clamped to the nearest limit and the UI shows a "limit reached" message.
The defaults of display type, colour grade, persistence, intensity, graticule brightness, transparency
and backlight correspond to these register values after a factory-setup start-up:
idx 0x125 ← 1, 0x0E7 ← 0, 0x0FE ← 0x42, 0x0D0/0x0D1 ← 0x3333/0x33, 0x0FA ← 0x99, 0x14D ← 0x44B.

### 2.2 Default setup (other groups)

*Default* (front-panel key, spec 08 §4.4, and power-on mode Default) applies the factory setup file
`firmdata0/factory_setting.xml` (per unit, contents **UNVERIFIED**: not in any available image), or
`user_default_setting.xml` if the user saved one. The built-in defaults (used when no setup file exists)
are:

| Group | Defaults |
|---|---|
| Channels | 1 V/div (index 17 at 1X), offset 0, DC, 1 MΩ, probe 1X, bandwidth Full, invert off, V/div coarse; CH1 on and selected, the other channels off (**UNVERIFIED** that the factory setup file does so) |
| Timebase | 200 ns/div (enum 9), delay 0, zoom off |
| Acquisition | Normal mode, depth enum 3 (10M, spec 04 §3), roll / XY / sequence off, running |
| Trigger | Auto, Edge, source C1, rising, DC coupling, level 0, holdoff off (value 80 ns), noise reject off |
| Display | §2.1 |
| Utility | sound on; screen saver Off (choices Off, 1, 5, 10, 30, 60 min); touch screen on; menu auto-hide setting 3 |

Probe-factor list: 0.1, 0.2, 0.5, 1, 2, 5, 10, … 10000 (index 3 = 1X).

---

## 3. Applying changes

### 3.1 General rules

1. Validate and clamp in the state model first; never write hardware with an out-of-range value.
2. Serialise all hardware writes on one thread. Register writes may skip unchanged values, but a write
   that acts as a strobe (pulses listed in specs 02/07) must always be issued.
3. A change that alters vertical scale/offset, timebase, memory depth, channel enable, or acquisition
   mode invalidates the displayed picture: after the new configuration is written, clear the displayed
   picture (map restart block, spec 07 §7.2) so old traces do not mix with new ones. The persistence
   sequence of spec 07 §7.2 is only for persistence-type changes.
4. Multi-register map parameter sets are written inside a configuration transaction
   (spec 07 §7.3: idx 0x190 enable → parameters → valid → disable).
5. Display-only settings (intensity, grid, transparency, colour grade, backlight, LEDs) can be written
   at any time; they do not require stopping acquisition.
6. After every key or touch event that changes state, update the LED word (spec 08 §6) from the new
   state in one rewrite.
7. State carried across changes:
   - a channel's vertical position is stored in pixels: a V/div change keeps the position on screen and scales
     the offset in volts (spec 06 §5);
   - trigger levels are stored per trigger type and per source channel in volts; a source change recalls the
     new source's own level; after a V/div, probe, offset or coupling change of the source the level is
     re-derived and clamped to ±4.1 div (spec 05 §3.2) and re-applied in its own stop/arm cycle with the
     A `0x2E` toggle;
   - a SCPI V/div or offset change of a channel makes it the selected channel (LEDs and priority ranks
     rewritten, spec 07 §7.1); the CH keys follow spec 08 §4.4.

### 3.2 Ordering for acquisition-affecting changes

| Step | Action |
|---|---|
| 1 | If running: stop the acquisition engine (spec 03) |
| 2 | Write analog front end (spec 06) / trigger (spec 05) / timebase-acquisition (spec 04) registers in the order those specs give |
| 3 | Map engine: configuration transaction with the new geometry (spec 04), then channel enables idx 0x106…0x109 and zoom split (spec 07 §7.4) |
| 4 | Clear the displayed picture (map restart block, spec 07 §7.2) |
| 5 | Re-arm if the run state is run/single (spec 03) |

Exceptions (a uniform stop/re-arm is also acceptable): a bandwidth-limit change needs no stop (relay bit and
VGA filter only); a coupling change stops and re-arms but need not redraw the stored record (spec 03 §3.7; **UNVERIFIED** in Normal mode, where the per-frame cycle hides it).

Frame-boundary rule: the PL latches map parameter sets at the end of the transaction (valid / enable
edge, which one is **UNVERIFIED**, spec 07 §7.3); writes need not wait for a display frame boundary. Whether a change written
mid-frame can tear one displayed frame is **UNVERIFIED** (harmless either way).

The per-setting write lists (which registers for which knob turn) are owned by specs 03–06; this spec does not repeat them.

### 3.3 Horizontal and acquisition settings: ranges and register effects

| Setting | Range / values | Register effect |
|---|---|---|
| Horizontal delay τ (`:TIMebase:DELay`, positive = trigger point moves left, spec 04 §9) | While running: −5 × t/div ≤ τ ≤ +10 000 × t/div. A request outside the range is set to the nearest limit; inside, it is kept as given (not quantised) | spec 04 §9 (live), §12 (held record) |
| Horizontal delay while stopped (record held) | with k = t_r / t_c (time/div of the held record ÷ current time/div; **UNVERIFIED**): lower limit min(−1500·k, −500) px, upper limit max(1 001 000·k, 1 000 000) px (px as below) | spec 04 §12 |
| Interpolation (`:ACQuire:INTerpolation`) | ON = sin(x)/x (default), OFF = linear | ACQ 0x081 bit 0 ← 1 (ON) / 0 (OFF), inside the bank windows with the coefficient window, spec 04 §8.3 |
| Acquisition-rate mode (`:ACQuire:AMODe`) | FAST (default), SLOW | ACQ 0x1EE ← 1 in SLOW, else the spec 04 §8.2 step 10 value; rewritten at once on a change (outside the bank windows) and by every timebase setup |
| Sequence on (`:ACQuire:SEQuence ON`) | Trigger sweep Auto → Normal (stays Normal after sequence off); Single/Force are programmed as Normal while it is on; after N segments the acquisition stops (no re-arm) | spec 03 §6.2, spec 05 §7 |
| History on (`:HISTORy ON`) | Acquisition stopped while on (held-frame display); settings changes then follow the stopped rule; off resumes run mode | spec 03 §3.3 "While stopped", §6.3 |
| Roll (`:ACQuire:MODE ROLL`) | Depth setting kept, but the rate column used is min(depth, 1M) | spec 04 §3, §7 |
| Any time/div, V/div, offset, depth or delay change while stopped | Only the held record is redrawn; the live timebase is programmed at the next RUN | spec 03 §3.3 "While stopped" |

Delay limits are evaluated in screen pixels of the 1000-column waveform area (100 px per division):
τ_px = round(τ / (t/div) · 100); a clamped value is written back as t/div · limit_px / 100. While running the
limits are +1 000 000 px (10 000 div) and −500 px (−5 div, the trigger at the right screen edge). The programming
guide's "[−5000 div, +5 div]" range text has the opposite sign of what the instrument accepts. Examples at
1 ms/div (running): `:TIM:DEL 2E-2` → 2.00E-02, `-2E-2` → −5.00E-03, `20` → 1.00E+01, `-20` → −5.00E-03; positive
values go to the trigger delay count ACQ 0x007/0x025, negative ones to the pre-trigger count 0x006/0x028
(spec 04 §9). `:ACQuire:INTerpolation OFF` / `ON` → ACQ 0x081 ← 0 / 1.

---

## 4. Persistence

### 4.1 Storage

| Mount | Device | Use |
|---|---|---|
| `/usr/bin/siglent/usr` | eMMC p12, ext4, read-write | user data: `usr/` (setups, option counters, user self-calibration `acq_self_cal_usr.txt` and `board_skew_data.txt`, spec 10 §1), `config/` (system configuration only), `mass_storage/local` (saved files) |
| `/usr/bin/siglent/firmdata0` | eMMC p9, ext4, read-only | factory data (identity, factory calibration) |
| `/usr/bin/siglent/config` | app partition, read-only | built-in defaults and resources |

Write with `fsync`/`sync` before reporting success; the unit may be powered off at any time.

### 4.2 Setup files

| File | Role |
|---|---|
| `factory_setting.xml` | factory default setup (what *Default* recalls) |
| `user_default_setting.xml` | user-defined default (when the user chose "custom default") |
| `save_setting.xml` | last-state setup, written by an auto-save timer and before shutdown-type events; recalled at power-on in "last" mode |
| `temp_setting.xml` | scratch copy |
| `user_setting.xml` / `user_setup.xml` | user save/recall slots ("autosave" directory naming also used) |
| `Setup.xml` (on external media), `*.xml` | user-exported setups |
| `eye_setting.xml`, `jitter_setting.xml`, `pa_setting.xml`, `bode_setting.xml` | option-specific state |
| `usr/config/NSP_config.xml`, `NSP_default.xml`, `NSP_user.xml`, `NSP_usr_system_info.xml` | system configuration (network, language, etc.) |
| `usr/usr/acq_self_cal_usr.txt` | user self-calibration (ASCII, spec 10 §4) |

Directories: `user_setting.xml`, `user_default_setting.xml`, `save_setting.xml` and the
option-specific `*_setting.xml` live in `/usr/bin/siglent/usr/usr/`; `factory_setting.xml` is read from
`/usr/bin/siglent/firmdata0/`; `temp_setting.xml` is written to `/tmp`. `/usr/bin/siglent/usr/config/`
holds the `NSP_*` system files, not the setups or the calibration. Every setup file carries `product_type:type`, `product_type:ch_num`,
`product_type:model` and `product_type:serial` (the product type, channel count, model and serial it was
saved on). A setup whose product type does not match is refused; for the power-on file, fall back to
`factory_setting.xml` and copy it over `save_setting.xml`. The firmware may use its own format for setups it
writes but must not apply a setup from another product type or channel count.

### 4.3 Power-on recall

Utility setting *Power On*: **Default** (apply `factory_setting.xml`, or the user default if set) or
**Last** (apply `save_setting.xml`). Order at power-on: identity (§1) → hardware bring-up (specs 13, 06)
→ self-calibration data load (spec 10) → recall setup → apply in the §3.2 order → start acquisition if
the recalled run state is run.

---

## Open

- Field-level schema of the setup XMLs and of `NSP_*` factory files (needs files from a unit).
- Auto-save interval of `save_setting.xml`.
- Bandwidth class fields in the factory data (keys are out of scope).
- Default grid style.
- Version-string components on a unit (spec 11 §6.6).
- Frame-boundary behaviour of map parameter changes.
