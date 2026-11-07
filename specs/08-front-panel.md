# 08 — Front Panel

The SDS800X HD front panel (buttons, six rotary encoders with push, key LEDs) is scanned and driven by
the PL. The CPU sees it only through the **map register file at 0x80040000** (word index idx, byte
address 0x80040000 + 4 × idx; register ownership spec 02):

- key and encoder state: four 16-bit words, one step counter, one interrupt (GIC SPI 90, DT 0x5A);
- LEDs: a 32-bit LED word written in two halves with a latch strobe.

The kernel module `siglentkb_sds.ko` turns the key interrupt into 4-byte records on `/dev/siglent_kb`.
The touchscreen is a Goodix GT9xx on I2C (§7); the beeper is a PS triple-timer-counter output (§8).
The SPI0 shift register `kb595` in the device tree is not used for LEDs on this model (§6.4).

---

## 1. Devices and registers

| Function | Interface |
|---|---|
| Key matrix | map idx 0x160…0x163 (0x80040580, 0x84, 0x88, 0x8C), read, bits 15:0 used, **active low** (idle 0xFFFF) |
| Encoder step | map idx 0x169 (0x800405A4), read, bits 7:0 |
| Key IRQ acknowledge | map idx 0x16E (0x800405B8), read clears |
| Long-push time | map idx 0x16B (0x800405AC); not written on this model (press types are not used, §4.3) |
| Knob period | map idx 0x16D (0x800405B4), write; 75 on this model |
| LED data | map idx 0x16A (bits 15:0) and idx 0x16F (bits 31:16) |
| LED latch | map idx 0x16C bit 0 |
| Key device | `/dev/siglent_kb` (char device, class `siglent_kb`) |
| Touch | `/dev/input/eventN` of the `goodix-ts` device; control `/dev/siglent_touch` |
| Beeper | Zynq UltraScale+ TTC0 at 0xFF110000 (mapped via `/dev/mem`) |

The unit of the knob period is **UNVERIFIED** (ms is plausible: 75 = encoder accumulation period).

---

## 2. Key matrix

### 2.1 Raw layout

The 64 key bits are presented as eight bytes B0…B7:

| Byte | Source |
|---|---|
| B0 | idx 0x163 bits 15:8 |
| B1 | idx 0x163 bits 7:0 |
| B2 | idx 0x162 bits 15:8 |
| B3 | idx 0x162 bits 7:0 |
| B4 | idx 0x161 bits 15:8 |
| B5 | idx 0x161 bits 7:0 |
| B6 | idx 0x160 bits 15:8 |
| B7 | idx 0x160 bits 7:0 |

Inside a byte, position p = 0…7 counts from the MSB (p = 0 is bit 7). A key is active when its bit is 0.

### 2.2 Key table

`code` is the key code used throughout this specification (also the target of the virtual-panel
table, §5). The "UI sign" column gives the sign the UI applies to the step byte for a rotation code:
the knob's quantity changes by sign × step.

| B | p | Register bit | code | Key | UI sign |
|---|---|---|---|---|---|
| 0 | 0 | 0x8004058C bit 15 | 67 | Vertical **scale** knob, rotate | + |
| 0 | 1 | 0x8004058C bit 14 | 68 | Vertical scale knob, rotate | − |
| 0 | 2 | 0x8004058C bit 13 | 69 | Vertical scale knob, push |  |
| 0 | 3 | 0x8004058C bit 12 | 9 | CH1 |  |
| 0 | 4 | 0x8004058C bit 11 | 10 | CH2 |  |
| 0 | 5 | 0x8004058C bit 10 | 11 | CH3 |  |
| 0 | 6 | 0x8004058C bit 9 | 12 | CH4 |  |
| 0 | 7 | 0x8004058C bit 8 | 37 | Menu on/off (not on the key-test screen; the UI takes no action on code 37 — physical key **UNVERIFIED**) |  |
| 1 | 0 | 0x8004058C bit 7 | 80 | Vertical **position** knob, rotate | + |
| 1 | 1 | 0x8004058C bit 6 | 79 | Vertical position knob, rotate | − |
| 1 | 2 | 0x8004058C bit 5 | 81 | Vertical position knob, push |  |
| 1 | 3 | 0x8004058C bit 4 | 38 | Digital |  |
| 1 | 4 | 0x8004058C bit 3 | 17 | Cursors |  |
| 1 | 5 | 0x8004058C bit 2 | 18 | Measure |  |
| 1 | 6 | 0x8004058C bit 1 | 41 | Navigate |  |
| 2 | 0 | 0x80040588 bit 15 | 91 | Horizontal **scale** knob, rotate | + |
| 2 | 1 | 0x80040588 bit 14 | 92 | Horizontal scale knob, rotate | − |
| 2 | 2 | 0x80040588 bit 13 | 93 | Horizontal scale knob, push |  |
| 2 | 3 | 0x80040588 bit 12 | 42 | Navigate ◀ |  |
| 2 | 4 | 0x80040588 bit 11 | 43 | Navigate stop (■) |  |
| 2 | 5 | 0x80040588 bit 10 | 44 | Navigate ▶ |  |
| 2 | 6 | 0x80040588 bit 9 | 35 | Math |  |
| 3 | 0 | 0x80040588 bit 7 | 94 | Horizontal **position** knob, rotate | − |
| 3 | 1 | 0x80040588 bit 6 | 95 | Horizontal position knob, rotate | + |
| 3 | 2 | 0x80040588 bit 5 | 96 | Horizontal position knob, push |  |
| 3 | 3 | 0x80040588 bit 4 | 36 | Ref |  |
| 3 | 4 | 0x80040588 bit 3 | 20 | Acquire |  |
| 3 | 5 | 0x80040588 bit 2 | 23 | Display/Persist |  |
| 3 | 6 | 0x80040588 bit 1 | 21 | Save/Recall |  |
| 4 | 0 | 0x80040584 bit 15 | 97 | Trigger **level** knob, rotate | + |
| 4 | 1 | 0x80040584 bit 14 | 98 | Trigger level knob, rotate | − |
| 4 | 2 | 0x80040584 bit 13 | 99 | Trigger level knob, push |  |
| 4 | 3 | 0x80040584 bit 12 | 47 | Quick (one-key quick action, user-configurable) |  |
| 4 | 4 | 0x80040584 bit 11 | 34 | Roll |  |
| 4 | 5 | 0x80040584 bit 10 | 22 | Clear Sweeps |  |
| 4 | 6 | 0x80040584 bit 9 | 27 | Decode |  |
| 5 | 0 | 0x80040584 bit 7 | 100 | **Adjust** (universal) knob, rotate | + |
| 5 | 1 | 0x80040584 bit 6 | 101 | Adjust knob, rotate | − |
| 5 | 2 | 0x80040584 bit 5 | 102 | Adjust knob, push |  |
| 5 | 3 | 0x80040584 bit 4 | 45 | Touch (touch screen on/off) |  |
| 5 | 4 | 0x80040584 bit 3 | 46 | Zoom |  |
| 5 | 5 | 0x80040584 bit 2 | 28 | Run/Stop |  |
| 5 | 6 | 0x80040584 bit 1 | 29 | Auto Setup |  |
| 6 | 3 | 0x80040580 bit 12 | 19 | Default |  |
| 6 | 4 | 0x80040580 bit 11 | 30 | Trigger Setup |  |
| 6 | 5 | 0x80040580 bit 10 | 31 | Trigger mode Auto |  |
| 6 | 6 | 0x80040580 bit 9 | 32 | Trigger mode Normal |  |
| 7 | 3 | 0x80040580 bit 4 | 33 | Single |  |
| 7 | 7 | 0x80040580 bit 0 | 25 | Print (not on the key-test screen — physical key **UNVERIFIED**) |  |

All other bits are unused. Bit p = 0 gives the "+" code on every knob except horizontal position
(p = 0 → 94, "−"). Which physical rotation (CW/CCW) each bit is, is **UNVERIFIED**. The UI sign is not
the same as the virtual-panel direction (§5) for codes 67/68 and 94/95.

Several codes share one (B, p) (the per-channel vertical codes, §2.3); the lookup returns the lowest
code that matches, so the hardware always yields 67/68/69 and 79/80/81.

### 2.3 Shared vertical knobs

There is one vertical-scale and one vertical-position knob for all channels. The code space
has per-channel codes: vertical scale CH1…CH4 = (67, 68, 69), (70, 71, 72), (73, 74, 75), (76, 77, 78),
each triple = (UI sign +, −, push); vertical position CH1…CH4 = (79, 80, 81), (82, 83, 84), (85, 86, 87),
(88, 89, 90), each triple = (UI sign −, +, push). The hardware always yields the CH1 codes; the application applies the event to
the currently selected channel.

### 2.4 Physical layout (key-test screen, for orientation)

Right-hand control area, rows top to bottom (x positions increase to the right):

```
[Cursors][Acquire][Save/Recall][Clear Sweeps][Run/Stop]
(Adjust) [Measure][Display]   [Quick]       [Decode][Auto Setup]
         [Navigate][◀][■][▶]  [Touch]       [Default]
[CH1]   (V-scale)        (H-scale)                [Setup]
[CH2]                                             [Auto]
[CH3]   [Math][Ref][Roll][Zoom]                   [Normal]
[CH4]                                             [Single]
[Digital] (V-pos)        (H-pos)        (Trig level)
```

---

## 3. `/dev/siglent_kb` protocol (kernel driver)

### 3.1 Interrupt handling

On GIC SPI 90 (level, high) the driver:

1. reads idx 0x160, 0x161, 0x162, 0x163 (in that order) and idx 0x169;
2. reads idx 0x16E (acknowledge);
3. **only if `read()` has been entered since the last record was produced** (the driver sets a flag on
   entry to `read()`, also when a record is already pending, and clears it when it produces a record):
   builds a record (§3.2), marks it pending and wakes the reader. Otherwise the event is discarded.

### 3.2 Record

`read()` requires `count == 4` (otherwise −EAGAIN). It blocks (interruptible) until a record is
pending, copies 4 bytes and clears the pending flag. An interrupted wait also returns −EAGAIN (not
−ERESTARTSYS):

| Byte | Content |
|---|---|
| 0 | k = index of the first byte B0…B7 (§2.1) that is not 0xFF; 0xFF if all are 0xFF |
| 1 | p = position (from MSB) of the first 0 bit in Bk; 0xFF if none |
| 2 | low byte of idx 0x169 (encoder step) at interrupt time |
| 3 | 0 |

Only the first active key is reported. `poll()` reports POLLIN | POLLRDNORM while a record is
pending. `write()` does nothing. The device-tree property `bit-nums` (64) selects this 4-word layout;
128 would read eight words and scan 16 bytes.

The firmware may instead read the registers directly with the same rules; it must read idx 0x16E
to release the interrupt.

---

## 4. Event decoding (user space)

### 4.1 Mapping

The application looks up (byte 0, byte 1) in §2.2. No match (including 0xFF, 0xFF) gives the
"no key" code 119 (0x77).

### 4.2 Event generation on this model

This model uses the plain path (no FPGA press-type): every record produces one key message
`(code, step = byte 2, flag = 0)` to the UI. In particular a key bit going active produces an event;
what the PL reports when a key is released or held (a second interrupt with all bits idle, auto-repeat)
is **UNVERIFIED**; every record is forwarded, and messages with code 119 are ignored by the UI.

Rotation events carry the step byte. Its meaning (number of detents accumulated during one knob
period, idx 0x16D) is **UNVERIFIED**; the UI turns it into a magnitude as in §4.4.

Implementation requirements:

- Treat codes 67…102 with p = 0 or 1 as rotations, p = 2 as push (§2.2).
- Treat a record with code 119 as "no key" (release).
- Programme idx 0x16D ← 75 at start-up before enabling the reader. idx 0x16B is left at
  its reset value.
- Ignore all key events while the panel is "locked".

### 4.3 Press types on other boards

idx 0x16B bit 12 can distinguish a long (bit 12 = 1) from a short press, with the press reported only on
release. This model does not use press types.

---

### 4.4 Key and knob actions

The instrument's response to each event. Every key action is ignored while the device is updating, during self-calibration or its
result screen, during save/recall and while a busy indicator is shown; while the screen saver is active the
first event only wakes the screen.

Keys:

| Key | Action |
|---|---|
| CH1…CH4 | If the eye-diagram or jitter function is on: set that function's source to the channel, nothing else. Otherwise, if this channel's menu is not shown: switch the channel on, make it the selected vertical source (target of the shared V-scale / V-position knobs, §2.3) and open its menu; if it is shown: toggle the channel on/off. The selection is a most-recently-selected stack: when the selected channel is switched off, the most recently selected channel that is still on becomes selected; with no analog channel on, the selection falls to the active Digital, Math or Ref function (in that order). A SCPI V/div or offset change of a channel also selects it (spec 07 §7.1) |
| Run/Stop | running or armed → stop; stopped → run. In Single mode after the single acquisition completed: return the trigger mode to the mode in use before Single and run |
| Auto / Normal / Single | set the trigger mode; if stopped, also start (Single arms one acquisition and stops after it); the mode already in force while running → no action |
| Default | confirmation dialog (Continue / Cancel); Continue recalls `user_default_setting.xml` if it exists, otherwise `firmdata0/factory_setting.xml` (spec 09 §4) |
| Auto Setup | confirmation dialog, then auto-setup (content **UNVERIFIED**) |
| Cursors, Measure, Decode | menu not shown: open it and switch the function on if off; menu shown: toggle the function |
| Math, Ref, Digital | as Cursors; if the function is on but not the selected vertical source, the first press selects it instead of toggling |
| Display | open the menu; menu shown: toggle persistence between Off and the last non-Off value |
| Acquire, Save/Recall, Utility, Trigger Setup, Navigate | open the menu |
| Roll, Zoom, Touch | toggle roll mode, zoom window, touch screen |
| Clear Sweeps | clear the display / statistics |

Run/Stop or Single while Single is armed and waiting: **UNVERIFIED** (stop is assumed).

Knobs. Let s = step byte, σ = the UI sign of §2.2/§2.3.

- **V-scale, H-scale**: move the 1-2-5 index by σ·s; positive = larger V/div or s/div. Clamp at the limits
  (spec 04 §2, spec 06 §12) and show "limit reached".
- **All other knobs** (V-position, H-position, trigger level, Adjust): n = A[s] with A = 1, 1, 4, 16, 45, 100,
  150, 200, 300, 350, 400, 400, … (400 for s = 11…19) and n = s for s ≥ 20; apply σ·n units:
  V-position 1 unit = 1/60 division (one pixel) of the selected source, positive = up; H-position 1 unit =
  1/100 division, positive = horizontal delay increases; trigger level 1 unit = 1/60 division, clamped so that
  level + channel position stays within ±246 units.
- V-scale in fine mode moves the fine V/div value (granularity **UNVERIFIED**; +1 at 1 V/div gives 1.02 V/div).
- The shared vertical knobs act on the selected vertical source (channel, Math, Ref or Digital); on a channel
  only while it is on.
- **Pushes**: V-scale → toggle coarse/fine of the selected channel; V-position → offset 0; H-scale → toggle the
  zoom window (same as the Zoom key); H-position → delay 0; trigger level → 50 % level for single-level trigger
  types, for dual-level types (slope, window, runt, qualified, setup/hold) toggle which level the knob moves;
  Adjust → intensity 50 % (or next cursor while cursors are on).
- **Adjust rotation**: the focused menu item if one has focus; else the history frame (history menu open);
  else the cursor position (cursors on); else waveform intensity ±1 % per unit.
- **Step 0**: whether the PL ever reports a rotation with step 0 is **UNVERIFIED**; the firmware should
  ignore rotation records with s = 0 (or treat them as s = 1).

## 5. Virtual panel codes (remote front panel)

The web front panel and the private SCPI command `$$SY_FP <id>,<dir>` (alias `SY_FP`) inject keys
through a fixed table `id → code`. For knobs `dir` = −1/+1 selects the rotation code and 0 the push
code; for buttons dir is ignored (web sends 1 or −1). Entries relevant to this model:

| id | code | Key | | id | code | Key |
|---|---|---|---|---|---|---|
| 0 | 37 | Menu on/off | | 28 | 21 | Save/Recall |
| 1…6 | 1…6 | soft keys F1…F6 (touch menu) | | 29 | 27 | Decode |
| 7 | 91/92/93 | H-scale (+1/−1/push) | | 30 | 38 | Digital |
| 10 | 94/95/96 | H-position (+1/−1/push) | | 31 | 35 | Math |
| 11 | 29 | Auto Setup | | 32 | 36 | Ref |
| 12 | 28 | Run/Stop | | 35…38 | 67…78 | V-scale CH1…CH4 (−1/+1/push) |
| 13 | 19 | Default | | 39…42 | 9…12 | CH1…CH4 |
| 14 | 39 | Wave Gen (no key on this model) | | 43…46 | 79…90 | V-position CH1…CH4 (−1/+1/push) |
| 15 | 100/101/102 | Adjust (+1/−1/push) | | 47 | 22 | Clear Sweeps |
| 16 | 97/98/99 | Trigger level (+1/−1/push) | | 48 | 26 | History (web) |
| 17 | 31 | Trigger Auto | | 49 | 34 | Roll |
| 18 | 30 | Trigger Setup | | 50 | 40 | Search (web) |
| 19 | 32 | Trigger Normal | | 51…54 | 41…44 | Navigate, ◀, ■, ▶ |
| 20 | 33 | Single | | 55, 56, 57 | 45, 46, 47 | Touch, Zoom, Quick (not used by the web page) |
| 22 | 17 | Cursors | | 23 | 23 | Display/Persist |
| 24 | 24 | Utility (web) | | 25 | 25 | Print |
| 26 | 18 | Measure | | 27 | 20 | Acquire |

The "dir" column is the virtual-panel direction, not the UI sign of §2.2 (for 67/68 and 94/95 they
differ). Utility (24), History (26) and Search (40) exist only as virtual-panel/UI codes; the hardware
keys at matrix (4,3), (5,3), (5,4) are Quick (47), Touch (45) and Zoom (46). Treat them as distinct
keys.

---

## 6. Key LEDs

### 6.1 LED word

| LED word bit | Key / indicator |
|---|---|
| 0 | Adjust-knob indicator: lit while the Adjust knob is assigned to something other than waveform intensity (focused menu item, cursors, history frame); off by default |
| 1 | CH1 |
| 2 | CH2 |
| 3 | CH3 |
| 4 | CH4 |
| 5 | Digital |
| 6 | Cursors |
| 7 | Measure |
| 8 | Math |
| 9 | Ref |
| 10 | Roll |
| 11 | Zoom (lit while the zoom window is on) |
| 12 | Decode |
| 13 | Touch (lit while the touch screen is enabled) |
| 14 | Trigger Auto |
| 15 | Trigger Normal |
| 16 | Single |
| 17 | Run/Stop: lit while acquiring/armed (green) |
| 18 | Run/Stop: stopped |

Bits 19…31 are unused. Bit value 1 = LED on; the electrical polarity is **UNVERIFIED**. Example words:
start-up 0x2 → 0x4 → 0x8 → 0x10 → 0x24010 → 0x24000 → **0x26000** (final: Auto + Run + Touch, no channel on);
Single from Auto 0x32000, completed 0x42000; Run/Stop stop 0x42000 / 0x42002 (CH1 selected), run 0x26000;
CH2 on and selected: low half (idx 0x16A) 0x6004; with the zoom window on (bit 11) and CH1 selected: Normal run
0x2A802, Math on: low half 0xA900 (without zoom these are 0x2A002 and 0xA100).

### 6.2 Write sequence

Every LED change rewrites the whole word W:

1. idx 0x16C ← 0
2. idx 0x16A ← W & 0xFFFF
3. idx 0x16C ← 1
4. idx 0x16C ← 0
5. idx 0x16F ← W >> 16
6. idx 0x16C ← 1

All-on writes W = 0xFFFFFFFF, all-off W = 0. LED colours are fixed and the LED brightness is not
adjustable on this model (there is no register for either).

### 6.3 LED policy

- **Vertical group** (bits 1–5, 8, 9: CH1–CH4, Digital, Math, Ref): exactly one LED, the one of the
  selected vertical source (§4.4); none if no source is on. It is not a "channel displayed" indicator: with
  CH1 and CH2 on and CH2 selected only bit 2 is lit; switching Math on moves the LED to Math.
- **Run state** (bits 14–18): stopped → bit 18 only (the trigger-mode LEDs are dark); running/armed → bit
  17 plus one mode bit (Auto 14, Normal 15, Single 16). Group words: Auto run 0x24000, Normal run 0x28000,
  Single armed 0x30000, stopped 0x40000.
- Bit 13 Touch while the touch screen is enabled (default on); bits 11 Zoom, 10 Roll, 6 Cursors,
  7 Measure, 12 Decode while that function is on; bit 0 per §6.1.
- Power-on word with the built-in defaults: 0x26000, plus the selected channel's bit when a channel is on
  (e.g. 0x26002). No LED blinks (**UNVERIFIED** while waiting for a trigger).

### 6.4 kb595

The DT node `spi0` / `kb595@0` (SPI0, 20 MHz) is present but unused; on this board the LED word in the map register file is used instead (LED changes write idx
0x16A/0x16F/0x16C and issue no SPI transfer). SPI0 is not connected on this board: it has no
MIO pins and its EMIO pins are not routed in the PL (spec 12 §4.2), so the node reaches no ball. The
firmware must not drive SPI0.

---

## 7. Touchscreen

| Item | Value |
|---|---|
| Controller | Goodix GT9xx, I2C address 0x14 (Linux i2c-0 = controller 0xFF030000) |
| Interrupt / reset GPIO | MIO/EMIO line 45 (irq), 51 (reset) of gpio controller (DT phandle 0xE) |
| Resolution | DT `resolution = <1024 600>` — reported coordinates are panel pixels, origin top-left, no scaling or swap needed by the application |
| Events | Linux multi-touch protocol B (input_mt slots), standard ABS_MT_POSITION_X/Y, BTN_TOUCH |
| Driver config | `/proc/gt9xx_config` (read/write the controller configuration block) |
| Touch on/off | `/dev/siglent_touch` (misc device created by `gt9xx.ko`): `ioctl(fd, 0x900, 0)` = suspend touch (no-op if already suspended), `ioctl(fd, 0x901, 0)` = resume; the argument is unused, any other command and both return 0 |
| Event node | the `goodix-ts` input device (found via `/proc/bus/input/devices`) |

Remote-view (VNC) pointer input is injected as ordinary multi-touch events on the `goodix-ts` event
node, and remote key input arrives on a uinput keyboard (spec 11), so the UI needs no separate
remote-input path.

The firmware can use the kernel's touch driver with any evdev reader. The maximum number of contacts is
**UNVERIFIED**.

---

## 8. Beeper

The beeper is driven by the PS triple-timer-counter **TTC0 at 0xFF110000** (Cadence TTC, counter 1,
waveform output to the buzzer). It is not the `pcspkr` input device (that path belongs to other
products).

### 8.1 Initialisation

| Step | Register (offset) | Write |
|---|---|---|
| 1 | Counter_Control_1 (0x0C) bit 0 | 1 (disable) |
| 2 | Clock_Control_1 (0x00) | 0 (no prescaler, internal clock) |
| 3 | Counter_Control_1 bit 5 | 0 (waveform output enabled) |
| 4 | Counter_Control_1 bit 3 | 1 (match mode) |
| 5 | Counter_Control_1 bit 1 | 1 (interval mode) |
| 6 | Interval_Counter_1 (0x24) | 27770 |
| 7 | Match_1_Counter_1 (0x30) | 13885 (interval / 2, 50 % duty) |
| 8 | Counter_Control_1 bit 0 | 0 (enable) |

Step 8 leaves the counter running; initialisation must end with bit 0 = 1 again (silent).

Tone frequency = TTC clock / 27770 (≈ 3.6 kHz at a 100 MHz LPD clock; clock **UNVERIFIED**).

### 8.2 Beep

Start = clear Counter_Control_1 bit 0; stop = set it. Durations (start, sleep, stop):

| Event | Beep | Duration |
|---|---|---|
| every button press (codes 1…66, and 118) | click | 1.7 ms |
| every touch press on the main window | click | 1.7 ms |
| knob rotations and knob pushes (codes 67…117), "limit reached" | none | — |
| pass/fail "fail" event | fail alarm | 50 ms |
| Math key held at boot (setup reset) only | long alarm, three times | 120 ms |
| (unused in normal operation) | short alarm | 40 ms |

All beeps except the fail alarm are suppressed when the Utility "Sound" switch is off. Sound defaults to
**on**; beeps stay off until the Utility setting is applied at start-up.

---

## Open

- Electrical polarity of LEDs; LED behaviour while waiting for a trigger.
- Physical presence of codes 25 (Print) and 37 (Menu).
- PL behaviour on key hold (auto-repeat), meaning and range of the encoder step byte (and whether step 0
  occurs), unit of idx 0x16D, CW/CCW per knob; fine V/div granularity; Run/Stop and Single while Single is armed.
- TTC input clock (tone frequency).
- Touch: contact count, report rate.
- Key positions of §2.2 other than (0,4), (4,3), (5,3), (5,4), (7,3) and the knob signs other than Adjust are
  not yet confirmed on a unit.
