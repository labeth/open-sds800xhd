# 07 — Display and Rendering

The SDS800X HD drives a 1024 × 600 parallel-RGB TFT from the PL. The PS never generates video timing:
the PL scans two frame stores in DDR through two Xilinx AXI VDMA cores, mixes them, and drives the panel.

- **UI layer**: an ordinary Linux framebuffer (`/dev/fb0`, ARGB8888) that the application draws with any
  2-D library. Menus, read-outs, grid labels, cursors, tags.
- **Waveform layer**: a 1000 × 480 image rendered by the PL itself (the "map" engine) from the acquired
  samples, with intensity grading, persistence, vector/dot drawing, channel priority and grid. Software
  only configures it through the map register file.
- **Capture**: the PL writes the mixed picture back to DDR as RGB565 for remote view (VNC / screenshots).

Register indices in this spec are word indices into the **map register file at 0x80040000**
(byte address = 0x80040000 + 4 × idx); their owner-spec listing is spec 02. All map registers are
32-bit words of which only bits 15:0 are used unless stated. Writes are posted stores. A firmware may keep
a shadow copy and skip writes of an unchanged value; the hardware does not need repeated writes.

---

## 1. Panel and timing

| Property | Value |
|---|---|
| Active area | 1024 × 600, landscape, 7-inch class (the 7-inch backlight encoding of §6 applies) |
| Interface | parallel RGB from the PL (connector J15 per service manual) |
| Timing | generated inside the PL bitstream; no timing register is written by software. Pixel clock, porches and refresh rate **UNVERIFIED** (measure on J15) |
| Physical size for the UI toolkit | 222 × 125 mm (only affects font DPI) |
| Reset | map idx 0x015 (0x80040054): write 1, then 0 (no delay needed between the writes) |
| Enable / backlight on | map idx 0x016 (0x80040058): 1 = on, 0 = off |
| Brightness | map idx 0x14D (0x80040534), §6 |

There is no DSI/I2C panel controller to configure.

---

## 2. Memory map of the display buffers

| Buffer | Physical | Size | Format | Producer → consumer |
|---|---|---|---|---|
| Boot logo | 0x1F400000 | 1024 × 600 × 4 | ARGB8888 | U-Boot → UI VDMA MM2S until the switch (§4.4) |
| UI framebuffer (`/dev/fb0`, DT `siglentfb`) | 0x1FC00000 | 0x258400 (= (1024·600 + 256) · 4) | ARGB8888, line 4096 B | application → UI VDMA MM2S |
| Capture buffer (`/dev/fb1`, DT `siglentfb1`) | 0x1F800000 | 1024 × 600 × 2 used | RGB565, line 2048 B | UI VDMA S2MM (PL mixer output) → VNC / screenshot |
| Waveform frame store (DT `wavedata`, 4 MiB) | 0x1E400000 | 2000 B × 480 lines | 16 bit/pixel, 1000 px/line | PL map engine ⇄ waveform VDMA S2MM/MM2S |

The DT declares RAM 0x00000000–0x1F7FFFFF only, so 0x1F800000–0x1FFFFFFF (capture buffer and UI
framebuffer) is invisible to Linux. 0x1E400000 (waveform, 4 MiB), 0x1E800000 (raw acquisition data,
8 MiB, spec 03) and 0x1F000000 (logo/update pictures, 8 MiB, which includes the boot logo at
0x1F400000) are `reusable` shared-dma-pool regions (CMA: Linux may lend them to movable pages until the
owning driver allocates them). A replacement must either declare them as `no-map` reservations or
allocate them through the owning driver before the PL or U-Boot data there is needed.

### 2.1 UI framebuffer format

`siglentfb` is a memory-only framebuffer driver (no registers): it maps `fbmem-phys` write-combined
and reports:

| Field | Value |
|---|---|
| `id` | `siglent` |
| visual | TRUECOLOR, packed pixels |
| xres × yres = virtual | 1024 × 600 (DT `resolution`, `virtual-resolution`) |
| bits_per_pixel | 32 |
| red / green / blue / transp | offset 16 / 8 / 0 / 24, length 8 each |
| line_length | 4096 |

So each pixel is a little-endian 32-bit word `0xAARRGGBB`. The transparency field is present unless the
DT node carries `no_transp` (it does not). How the PL uses the alpha byte when mixing over the waveform
layer is **UNVERIFIED** (§3).

`/dev/fb1` is the same driver on 0x1F800000 and also reports 32 bpp, but the PL writes RGB565 there
(§5); readers must use the VDMA geometry, not the fb1 var info.

### 2.2 Waveform frame store

1000 pixels × 480 lines, 2 bytes per pixel, line pitch 2000 B. This is exactly the graticule area
(§3.1). The PL writes it through waveform VDMA S2MM and reads it back through waveform VDMA MM2S on the same address;
DDR is the persistence store of the map engine. The meaning of the 16-bit pixel (RGB565 colour vs.
density/channel code) is **UNVERIFIED** (dump 0x1E400000 on a running unit). Software never writes
this buffer.

---

## 3. Layer composition

### 3.1 Screen geometry

| Item | Value |
|---|---|
| Graticule (waveform window) | 1000 × 480 px; interior top-left at screen (19, 50), with a 1-px frame at x = 18 / 1019 and y = 49 / 530 |
| Divisions | 10 horizontal × 8 vertical; 100 px per horizontal division, 60 px per vertical division |
| Vertical code mapping | 12-bit display code, 0x800 = screen centre; the screen limits of spec 03 §5 (codes 0x080 … 0xF7F) span the 480 rows (0xF00 codes, 8 codes per pixel, 480 codes per division); 20 blank lines reserved |
| Horizontal | 1000 columns (0…999) |
| Zoom split | main window 115 rows on top, 20-row gap, zoom window below (§7.4) |
| XY window origin | top rim 20, left rim 150 inside the waveform area |

A UI that narrows the waveform window to ≈ 800 px while a side menu is open (interior x = 19…821)
resizes the map horizontally (idx 0x0C4 ← 804, spec 04); closing the menu restores 1000 px.

The placement of the 1000 × 480 waveform layer on the 1024 × 600 panel is fixed in the PL. That the PL
puts it at the same (19, 50) as the UI graticule is **UNVERIFIED** (capture the mixed output, §5, with a
known trace on a unit).

### 3.2 Mixing

The PL mixes, per output pixel: UI layer (UI VDMA MM2S, 32-bit stream) over waveform layer (waveform VDMA MM2S,
16-bit stream). Controls in the map register file:

| idx | Field | Meaning |
|---|---|---|
| 0x0FA | bits 7:0 | menu transparency 0…255. The UI exposes 20…80 % and writes `trunc(p / 100 × 255)` (example: `:DISPlay:TRANsparence 80` → 0xCC) |
| 0x0F9 | bits 7:0 | base colour (default not observed) |
| 0x0DB | 16 bit | overlay enable — written 1 at map init |
| 0x0DC | 16 bit | print overlay enable (screenshot path) |
| 0x0FF | bit 0 | waveform blank: 1 = waveform layer suppressed, 0 = shown. Write 1 during start-up and auto-zero and whenever the analog display is off (e.g. FFT-exclusive view, XY without PL support, sample log, software roll), 0 otherwise (0 once the UI is ready) |

fb0 holds straight (non-premultiplied) alpha. A UI layer known to compose correctly uses A = 0 with RGB 0 over the
visible graticule interior (where the waveform shows), A = 255 for opaque UI, and partial alpha only for grid dots
(RGB 227,227,227 with A = 45, 82 or 113). Menu transparency (0x0FA) is applied in the PL: changing it leaves fb0 unchanged. With
the waveform plane at (19, 50) its centre column lands under the UI trigger marker (x = 519).
Exact blend equation (how the PL combines the fb0 alpha, 0x0FA and the waveform layer) is **UNVERIFIED**. A
replacement firmware that wants a predictable result should draw the UI with alpha = 0xFF where
opaque and 0x00 where the waveform must show through, and set 0x0FA to the default
(60 % → 153).

---

## 4. VDMA programming

Both VDMAs are Xilinx AXI VDMA v6.3, one frame store each (`num-fstores` = 1), genlock, flush on
frame sync. Register offsets are PG020's. The writes below are the complete programming; apart from the
framebuffer switch of §4.4 the VDMAs need no writes at run time.

### 4.1 UI VDMA at 0x80000000 — UI layer MM2S (32-bit stream)

| Step | Address | Value | Meaning |
|---|---|---|---|
| 1 | 0x80000000 MM2S_VDMACR | 0x00000004 | soft reset |
| 2 | 0x80000000 MM2S_VDMACR | 0x0001008B | RS, circular park, genlock enable, internal genlock source, IRQFrameCount = 1 |
| 3 | 0x8000005C START_ADDRESS1 | 0x1F400000 | boot logo (later 0x1FC00000, §4.4) |
| 4 | 0x80000058 FRMDLY_STRIDE | 0x01001000 | frame delay 1, stride 4096 B |
| 5 | 0x80000054 HSIZE | 0x00001000 | 4096 B per line |
| 6 | 0x80000050 VSIZE | 0x00000258 | 600 lines — this write starts the channel |

### 4.2 UI VDMA at 0x80000030 — capture S2MM (16-bit stream)

| Step | Address | Value | Meaning |
|---|---|---|---|
| 1 | 0x80000030 S2MM_VDMACR | 0x00000004 | soft reset |
| 2 | 0x80000030 S2MM_VDMACR | 0x00018003 | RS, circular park, bit 15 (Repeat_En), IRQFrameCount = 1 |
| 3 | 0x800000AC START_ADDRESS1 | 0x1F800000 | |
| 4 | 0x800000A8 FRMDLY_STRIDE | 0x00000800 | stride 2048 B |
| 5 | 0x800000A4 HSIZE | 0x00000800 | 2048 B per line (1024 × RGB565) |
| 6 | 0x800000A0 VSIZE | 0x00000258 | 600 lines, starts the channel |
| 7 | map idx 0x158 (0x80040560) | 0, then 1 | start the PL → PS video stream |
| 8 | map idx 0x1B0 (0x800406C0) | 1 | enable (capture path) — semantics **UNVERIFIED** |

### 4.3 waveform VDMA at 0x80020000 — waveform frame store (16-bit streams)

| Step | Address | Value | Meaning |
|---|---|---|---|
| 1 | 0x80020030 S2MM_VDMACR | 0x00000004 | reset |
| 2 | 0x80020030 S2MM_VDMACR | 0x00018003 | as §4.2 |
| 3 | 0x800200AC START_ADDRESS1 | 0x1E400000 | |
| 4 | 0x800200A8 FRMDLY_STRIDE | 0x000007D0 | stride 2000 B |
| 5 | 0x800200A4 HSIZE | 0x000007D0 | 2000 B |
| 6 | 0x80020000 MM2S_VDMACR | 0x00000004 | reset |
| 7 | 0x80020000 MM2S_VDMACR | 0x0001008B | as §4.1 |
| 8 | 0x8002005C START_ADDRESS1 | 0x1E400000 | |
| 9 | 0x80020058 FRMDLY_STRIDE | 0x010007D0 | frame delay 1, stride 2000 B |
| 10 | 0x80020054 HSIZE | 0x000007D0 | |
| 11 | 0x800200A0 S2MM VSIZE | 0x000001E0 | 480, starts S2MM |
| 12 | 0x80020050 MM2S VSIZE | 0x000001E0 | 480, starts MM2S |

### 4.4 Order at boot and the framebuffer switch

0. U-Boot has already shown the boot logo from 0x1F400000 with the same UI VDMA MM2S geometry, LCD reset
   pulse, enable and brightness 1999 (state left behind: spec 13 §5.6).
1. Before the PL is (re)loaded: map idx 0x016 ← 0 (backlight off), wait 0.5 s.
2. Load the bitstream (spec 13).
3. §4.1, §4.3, §4.2 in that order.
4. map idx 0x015 ← 1, ← 0 (LCD reset); map idx 0x14D ← 0x700; wait 0.1 s; map idx 0x016 ← 1.
5. The application draws into 0x1FC00000. To show it, retarget the UI layer: 0x8000005C ← 0x1FC00000,
   then 0x80000050 ← 0x258 (rewriting VSIZE latches the new address at the next frame). A firmware may do
   this as soon as its framebuffer holds a picture.

VDMA interrupts (GIC SPI 0x5B/0x60 for UI VDMA, 0x5C/0x5D for waveform VDMA) are not used.

---

## 5. Capture path (remote view, screenshots)

> Optional feature: not needed for a minimum viable firmware (`00` §5).

- The mixer output is written continuously (§4.2) to 0x1F800000 as RGB565, 1024 × 600, 2048 B lines.
- `siglent_vnc.ko` exposes GIC SPI 0x68 as `/dev/siglent_vnc`; `poll()` returns POLLPRI per captured
  frame (spec 14). A VNC server maps 0x1F800000 read-only and treats each event as a full-screen
  update.
- Remote view needs no access to the map register file (spec 11 §12, spec 14 §6).
- The screenshot (`:PRINt?`, spec 11 §8) is taken after a 1 s settle delay, optionally colour-inverted;
  a firmware may read the capture buffer or compose the image from its own layers.
- A replacement firmware can skip the PL capture entirely and serve remote view from its own UI
  framebuffer plus a software render of the waveform; the capture buffer is only needed to show
  exactly what the panel shows.

---

## 6. Backlight

| Item | Value |
|---|---|
| Enable | map idx 0x016: 1 on, 0 off |
| Level register | map idx 0x14D (`adj_cnt_max`), 16 bit, larger = brighter (**UNVERIFIED** polarity) |
| Encoding (7-inch screen, this unit) | `reg = 18 · b + 199`, b = UI backlight 0…100 → 199…1999 |
| Encoding used by the other screen size (not this unit) | `reg = 15 · b + 469` |
| Boot value | 0x700 (= 1792, b ≈ 88) |
| UI default | b = 50 → 1099. At runtime write the level (idx 0x14D) only; idx 0x016 stays 1 after the boot sequence of §4.4 |
| Screen saver | time Off (default), 1, 5, 10, 30 or 60 min of no input (spec 09 §2.2). On timeout: b ← 0 (idx 0x14D = 199; enable idx 0x016 stays 1, the panel is dimmed, not switched off) and a full-screen saver widget is shown. On the next input while the saver is shown: b ← 100 (1999) for 0.5 s, then the user level; the timer restarts if the saver time is not 0 |

---

## 7. Waveform rendering (map engine)

The PL renders each enabled channel's acquired record into the waveform frame store. Software
configures geometry (spec 04 for horizontal compression, spec 03 for frame hand-off), appearance
(this section) and reads back a few results (peak lines, search hits, pass/fail).

### 7.1 Appearance registers

| idx | Bits | Name | Encoding |
|---|---|---|---|
| 0x125 | 0 | display type | 1 = vectors (connect samples), 0 = dots. SCPI `:DISPlay:TYPE VECTor` = 0 → write 1; `DOT` = 1 → write 0 |
| 0x0FE | 7:0 | intensity | `UI intensity (0…100 %) + 16`; UI default 50 → 66. Bits 15:8 preserved |
| 0x0E7 | 0 | colour grade | 1 = colour-graded (temperature) display, 0 = channel colour |
| 0x0D0, 0x0D1 | 15:0 / 13:0 | grid colour | 24-bit `0x00GGGGGG` grey: 0x0D0 ← bits 15:0, 0x0D1 ← bits 29:16. g = `trunc(G / 100 × 255)`, G = UI graticule brightness 0…100 %, default 20 % → g = 51 → 0x0D0 = 0x3333, 0x0D1 = 0x0033 |
| 0x0D2, 0x0D3 | same split | "magic trace" | written 2 at start-up; meaning **UNVERIFIED** |
| 0x0D4, 0x0D5 | same split | "magic grid" | written 1 at start-up; meaning **UNVERIFIED** (grid style FULL/LIGHT/NONE is drawn by the UI, see §7.6) |
| 0x105 | ch (0…3) | trace visible | 1 = shown |
| 0x106…0x109 | 15:0 | CH1…CH4 enable | 1 = channel rendered |
| 0x130 | 3:0 | background priority | always 0 |
| 0x131…0x134 | 3:0 | CH1…CH4 priority | rank 0 … C − 1: rank 0 = the selected channel (the channel most recently switched on or selected, spec 08 §4.4 selection stack), the other on channels follow in order of recency, off channels after them. Rewrite all ranks (idx 0x130 ← 0 first) after every channel switch-on/off (after the channel enable 0x106+n) and whenever the selected channel changes. A SCPI V/div or offset change of a channel (possibly also coupling, **UNVERIFIED**) makes that channel the selected one: ranks and the channel-LED word (spec 08 §6) are rewritten (e.g. CH1, CH2 on, CH2 selected: `:CHAN1:SCAL 0.5` → 0x131 = 0, 0x132 = 1, LED word 0x6002). Unchanged ranks need not be rewritten. Example: boot with CH1 on → (0, 3, 2, 1); then CH2, CH3, CH4 switched on → (3, 2, 1, 0). That rank 0 is drawn on top is **UNVERIFIED** (intended: 0 = highest) |
| 0x181 | 0 | double screen | 1 = split display |
| 0x0BD | 15:0 | mask enable | pass/fail mask overlay |

Trace colours are fixed in the PL on this model (the per-channel colour registers idx 0x097+ch /
0x09C+ch exist but are not needed; leave them at their reset values). The channel colours are CH1 yellow,
CH2 magenta/pink, CH3 cyan, CH4 green; the UI uses the same colours for its own tags. The exact PL palette is
**UNVERIFIED**.

### 7.2 Persistence

| UI persistence | SCPI value | idx 0x140 bits 1:0 | idx 0x144 bits 7:0 | idx 0x080 |
|---|---|---|---|---|
| Off | 0 | 0 | unchanged | 3 |
| Infinite | 1 | 3 | 3 | 0 |
| 1 s | 2 | 1 | 1 | 0 |
| 5 s | 3 | 1 | 5 | 0 |
| 10 s | 4 | 1 | 10 | 0 |
| 30 s | 5 | 1 | 30 | 0 |
| 100/200/500 ms | 6…8 | unchanged | 0 | 0 | 

Bits 15:2 of 0x140 and 15:8 of 0x144 are preserved (read-modify-write on the software shadow; spec 02
§1.2 — the hardware is read only for a register not yet written since start-up). The sub-second rows are not
offered on this model (**UNVERIFIED**).

**Set persistence type**: stop the acquisition and wait for stop-ready and map-ready (§7.5, spec 03 §4.1);
write idx 0x080 per the table, then idx 0x140 (bits 1:0), then idx 0x144 (bits 7:0; not written for Off).
If the new type is Off, follow with a persistence clear. Then restart the acquisition. (While stopped, the
same without stop/restart, followed by a picture clear.)

**Persistence clear**: idx 0x080 ← 3; v = shadow of idx 0x140; idx 0x140 ← 2; idx 0x140 ← v. Issued when
persistence is switched Off, and also together with the picture clear after acquisition-setting changes; a
clear while persistence is Off is harmless. Example (INF → OFF): 0x080 ← 3, 0x140 ← 0, 0x140 ← 2, 0x140 ← 0
(with a shadow copy the second 0x080 ← 3 is skipped as unchanged).

**Clear the displayed picture** (`:DISPlay:CLEar`, and after display-type, channel, XY, sequence and other
acquisition-setting changes; zoom on/off issue **no** clear block): the map restart block idx 0x0AF ← 0, ← 1; idx 0x103 ← 1, ← 0 (map soft
reset, §7.5); idx 0x170 bit 8 pulse (set, then clear; the other bits preserved: 0x101, 0x001 once bit 0 is 1, see
below); idx 0x15A ← 0, ← 1. Issue the block
two to four times in a row, followed by one more idx 0x103 1 → 0 pulse. The block itself does not touch
idx 0x080 or 0x140 (the persistence clear above is a separate sequence). idx 0x0AF is the search-clear strobe and
idx 0x170 the pass/fail-mask load register (bits 8, 4 and 0, spec 02 §9): bits 0 and 4 are pulsed and left at 1 by
the pass/fail mask load (spec 15), which first runs after start-up, so the clear blocks of the start-up (§7.10)
write 0x100, 0x000 and later ones 0x101, 0x001. Their PL effect is **UNVERIFIED** — a replacement writes the bit-8
pulse with its current shadow of bits 0 and 4.

### 7.3 Map configuration transactions

Multi-register map parameter sets (the timebase set of §7.8, the zoom-window sets, XY) are bracketed
by writes to idx 0x190 (fields: bit 0 enable, bits 7:4 set number n, bit 6 zoom valid, bit 12 valid,
bit 15 gain/offset valid). Each step is a 16-bit read-modify-write whose mask also clears other
fields; the PL must see exactly the word sequence below, so reproduce the words, not individual bit flips.
p = value read before step 1 (normally the previous set's n << 4).

| Step | Normal set | Zoom set |
|---|---|---|
| 1 | W = (p & 0x00FE) \| 0x0001 | W = (p & 0x00FE) \| 0x0001 |
| 2 | W = W & 0x00FF | W = W & 0x00BE (bit 0 drops again) |
| 3 | W = (W & 0x000F) \| n << 4 | W = (W & 0x000F) \| n << 4 |
| 4 | write the parameters | write the parameters |
| 5 | W = (W & 0x00FF) \| 0x1000 | W = (W & 0x00BF) \| 0x0040 |
| 6 | W = W & 0x00FE | W = W & 0x00BE |

So a normal set n from p = (n−1) << 4 writes (n−1)<<4 | 1, n<<4 | 1, parameters, 0x1000 | n<<4 | 1,
n<<4 (enable and valid fall in the same write; e.g. 0x31, 0x41, …, 0x1041, 0x40).

Gain/offset transaction (issued after each main set n while redrawing a stored record):
steps 1–3, write nothing, then W = W | 0x8000, W = W & 0x7FFF, then step 6 (words for n = 0: 0x1,
0x8001, 0x1, 0x0). On this product the transaction carries no parameters (the vertical mapping is done in
the ACQ plane, spec 02 §4.2 resize registers); a firmware may omit it.

Bit 6 is also bit 2 of n, so zoom steps 5/6 overwrite it. A write whose word equals the previous one may be
skipped. Set numbers: n = 0 … C − 1 = main timebase set of channel n (§7.8, written for every
channel of the model, C = 4 or 2); 4 + n = zoom-window set of channel n (normal-transaction words)
and, in XY mode, the XY set (§7.7; not yet confirmed on a unit). Which edge latches the set in the PL (valid 0 → 1 or enable
1 → 0) is **UNVERIFIED**. The zoom-set column (bit 6) is not needed: zoom parameters go through normal
transactions on sets 4 … 4 + C − 1, so a firmware uses the normal column only.

### 7.4 Main / zoom split

Software state: total rows R = 480, gap G = 20.

| Zoom | Writes, in order |
|---|---|
| on | idx 0x10A ← 1; idx 0x090 ← 1; idx 0x091 ← R − G (460); idx 0x092 ← (R − G) / 4 (115); idx 0x093 ← G (20); idx 0x08F ← 1; idx 0x08F ← 0 |
| off | idx 0x10A ← 0; idx 0x090 ← 1; idx 0x091 ← R (480); idx 0x092 ← R (480); idx 0x093 ← 0; idx 0x08F ← 1; idx 0x08F ← 0 |

When the split geometry itself changes (new R or G) the same row writes are issued, followed by the
up-area pixel idx 0x0C7 = `top_offset + 40 + G/2 + (R − G)/4` (zoom on) or 0x3FF (zoom off), then the
idx 0x08F pulse. `top_offset` is a layout value (**UNVERIFIED**). Horizontal resize of the
zoom window writes idx 0x0C3…0x0CB (source/destination pixel counts, offsets and start pixel for
main and zoom; owner spec 04).

### 7.5 Engine handshakes

| Operation | Sequence | Timeout |
|---|---|---|
| Map soft reset | idx 0x103 ← v (raw). If v = 0 (release): poll idx 0x188 bit 8 until 0, then idx 0x188 bits 3:0 until 0 | 50 002 reads × 1 µs each |
| Wait wave move | poll idx 0x187 bits 3:0 until ≠ 0, then poll idx 0x188 bits 3:0 until 0 (resize end); a firmware that does not need the resize to finish may skip both | 5 002 × 1 µs, then 50 002 × 1 µs |
| Wait map ready | poll ACQ register 0xFD bit 7 until 0 (spec 02, acquisition register file) | 5 002 × 1 µs |
| Frame counter reset | idx 0x15A ← 0, ← 1 | — |
| Display update | idx 0x08F ← 1, ← 0 after row-count changes | — |

A timeout is not fatal: log it and continue.

### 7.6 What the UI draws itself

Grid lines (full / light / none), rulers, channel and trigger tags, cursors, measurement and menu
text, and the 10 × 8 division ticks are drawn by the application into the UI framebuffer. Grid
brightness additionally sets idx 0x0D0/0x0D1 (§7.1), so the PL also knows the grid colour (it is used
for its own grid/overlay, e.g. in screenshots — **UNVERIFIED** which).

### 7.7 XY mode

> Optional feature: not needed for a minimum viable firmware (`00` §5).

idx 0x192 = XY mode: 0 = off; 1 = XY on, XY-only display; 0x10 = XY on with the YT+XY display variant
(`:ACQuire:MODE XY` gives 1, observed; which UI choice gives 0x10 is UNVERIFIED). ACQ 0x198 gets the same value at the same time, after ACQ 0x199 (channel pairs, spec 04 §10).

- XY-only display (1): every map set n = 0 … C − 1 of §7.8 is written with the XY variant: S is replaced by
  S_xy = 2·S (two samples per point, X channel and Y channel), I = 1, and idx 0x199 (XY length) ← ⌊P / 2⌋ with P of
  §7.8 computed for S_xy (= S with one section; P = N = 5000 in the multi branch). Example (2 ch, 1 GSa/s): 200 ns/div
  (S = 2000, S_xy = 4000, one section): set 0 0x19B = 0x190, 0x19D = 4, 0x19F = 3, 0x1A1 = 0x10, 0x199 = 0x7D0,
  0x153 = 0x320, then 0x192 ← 1; 1 µs/div (S = 10 000, S_xy = 20 000, ppp 20, N = 5000, seg 4): 0x19B = 0x1F4,
  0x19A = 0xFA, 0x19D = 0xA, 0x1A7 = 4, 0x153 = 0x3E8, 0x199 = 0x9C4 (2500).
- XY pixel mapping (after the arm of the XY switch, spec 04 §10): idx 0x0C4 ← 0x1E0, 0x0C3 ← 0x1E0, 0x0C5 ← 0x138
  (480 source / 480 destination columns at offset 312); XY off restores the YT mapping (0x0C4 ← 1000, or 804 with a
  side menu open, §3.1; 0x0C3 ← 1000; 0x0C5 ← 0xA0). Observed with the side menu open.
- YT+XY variant (0x10, not yet confirmed on a unit): sets 0 … C − 1 normal, then sets 4 … 4 + C − 1 with the XY variant.
- idx 0x193–0x197 (X min code, X/Y gain/offset) and 0x198/0x19C (X label start/end) are not used on this
  product: leave them at their reset values.

The ACQ side (record set) is spec 04 §10.

### 7.8 Map timebase set

Written for every channel of the model, n = 0 … C − 1 (C = 4 or 2, independent of which channels are
enabled), after the ACQ configuration-bank pass of spec 03 §3.3, each inside its own normal transaction of
§7.3 with set number n: transaction start (§7.3 steps 1–3, set n), the registers below in this order, then
idx 0x1BD ← 0x168 if the zoom window is on else 0, idx 0x1BE ← 0x1DF, then §7.3 steps 5–6. (XY mode: §7.7.)

Inputs: table rate R (MSa/s, spec 04 §4), time/div t (s), H = 1000 (waveform columns).

- S = screen sample count = float32(10 · t · R · 10⁶), clamped as in spec 04 §5.4.
- I = display interpolation factor (spec 04 §5.4).
- round(x) = ⌊x + 0.5⌋ for x ≥ 0.
- multi = (S > 6250).
- Not multi: P = round(I · S); seg = 1; ppp = round((P & 0xFFFF) / 1000).
  Multi: ppp = round(I · S / 1000); N = the first c in (5000, 4000, 6250) with c mod ppp = 0 or ppp mod c = 0
  (5000 if none); P = I · N; seg = ⌊S / N⌋. (ACQ side: spec 04 §8.2 step 3 uses ⌊I·S/1000⌋; with the table rates
  both give the same N.)
- Then ppp = 1 if ppp = 0; 2 if ppp = 3; 100 000 000 if ppp > 100 000 000.
- p = P & 0xFFFF.

| idx | name | value |
|---|---|---|
| 0x060 | norm transfer count | H / 4 = 250 |
| 0x19B | norm BRAM read count | ⌊p / 10⌋ |
| 0x19A | norm address adjust | 2 if seg = 1, else ⌊p / 20⌋ |
| 0x19D | norm points per pixel | 10 if ppp mod 10 = 0, else ppp |
| 0x19F bits 3:0 | read times | ppp ≥ 10 → 1; ppp = 8 → 3; else ⌊10 / ppp⌋ if ppp divides 10, else ⌊10 / ppp⌋ + 1 |
| 0x1A1 | mask base address | ppp = 1 → 0x20; 2 → 0x18; 4 or 8 → 0x10; 5 → 8; 125 → 0x2A; else 0 |
| 0x1A3 | ten-point count | 1 if ppp < 11 or ppp ∈ {25, 125}, else ⌊ppp / 10⌋ |
| 0x120 | scan enable | 0 |
| 0x1A7 | norm BRAM segment count | seg |
| 0x1AB bits 11:0 | lx BRAM count | 1 if ppp < p, else ⌊ppp / p⌋ |
| 0x1AE | x label start | 0 |
| 0x1AF | x label end | H = 1000 |
| 0x199 | XY length (XY mode only, §7.7) | as §7.7 |
| 0x153 | norm output length | ⌊p / 5⌋ |
| 0x110 | preview data | 0 |
| 0x1B5, 0x1B6, 0x1B7 | optimized refresh | below |

Optimized refresh: S′ = ⌊S + 0.5⌋, I′ = interpolation factor for S′ (minimum 1), u = I′ · S′. If u ∈ {1000,
2000, 5000}, the zoom window is off, no digital channel is open and the acquisition is running: idx 0x1B5 ← 1,
0x1B6 ← u / 1000, 0x1B7 ← u. Otherwise 0x1B5, 0x1B6, 0x1B7 ← 0. (Further conditions are **UNVERIFIED**: in every
observed state, including ones that meet the rule above such as 5 ns/div 1 ch (u = 2000), 0x1B5–0x1B7 stayed 0.
A firmware writes 0.)

Example (2 GSa/s, 500 ns/div, S = 10 000, I = 1): 0x19D ← 10, 0x060 ← 250, 0x19F ← 1, 0x19B ← 500, 0x19A ← 250,
0x1A1 ← 0, 0x1A3 ← 1, 0x1A7 ← 2, 0x1AB ← 1, 0x1AE ← 0, 0x1AF ← 1000, 0x153 ← 1000.

Zoom window (only while zoom is on; ACQ side: spec 04 §11): for n = 0 … C − 1 a normal transaction with set number 4 + n, after the
main sets. S_z = S · t_zoom / t_main; write nothing if S_z < 4. I_z = interpolation factor for S_z; P_z, seg_z,
ppp_z as above with S_z and I_z (in the multi branch seg_z = ⌊min(S, S_z) / N⌋). Write idx 0x061 ← 250, then
0x19B, 0x19A, 0x19D, 0x19F, 0x1A1, 0x1A3, 0x1A7, 0x1AB, 0x1AE, 0x1AF with the zoom values, the optimized
refresh, idx 0x154 ← ⌊P_z / 5⌋, idx 0x110 ← 0, idx 0x1BD ← 0x168, idx 0x1BE ← 0x1DF (same codes as the main
sets with zoom on).

idx 0x1BD / 0x1BE (max/min code window): no zoom → 0 / 0x1DF; zoom on, run-mode sets (main and zoom) →
0x168 / 0x1DF; zoom window of a stopped (held) record → 0 / 0x167.

**Stored-record set** (the map side of the stored-record redraw, spec 03 §3.7; not needed for live display).
MAP order inside the redraw frame: 0x174 ← 0x20, 0x172 ← 1 before the stored ACQ banks (03 §3.7 step 2); after
them, per n = 0 … C − 1, a normal transaction with set number n holding the registers below, followed by the
empty gain/offset transaction n of §7.3 (03 §3.7 step 4); then the DPO value of §7.9; after the live pass
(03 §3.7 step 7, which rewrites the live sets of this section) 0x172 ← 0, 0x174 ← 0 (03 §3.7 step 8); then the
arm with its DPO recall (§7.9). Inputs are those of spec 04 §12: record rate R_r, record screen count S_r,
S_v = 10 · t_c · R_r · 10⁶ (t_c = current time/div, s; capped at 2·10¹³, i.e. not in practice),
I_v = interpolation factor of S_v (1 with the XY display), and fx = t_r / t_c (t_r = time/div the record was
taken at).

- Nothing is written if S_v < 4 (the too-few-points test, spec 04 §11/§12).
- If S_r ≤ S_v: n_s = S_r and ppp = trunc(I_v · S_r / (1000 · fx)); else n_s = S_v and ppp =
  trunc(I_v · S_v / 1000) (both are the points per pixel of the current screen); if ppp > I_v · S_r then
  ppp = ⌊I_v · S_r⌋. If ppp = 8 and quick preview is off, the window start/end below are doubled. Then the ppp
  corrections of the live set (0 → 1, 3 → 2, > 10⁸ → 10⁸).
- Sections as spec 04 §12 (n_s > 6249 and n_s ≠ 6250: P = per-section count for ppp, seg = ⌊n_s / P⌋; else
  P = n_s, seg = 1); p = (I_v · P) & 0xFFFF.
- Registers, in this order: 0x19D, 0x060 (250), 0x19F as the live set from ppp; 0x19B ← max(1, ⌊p / 10⌋);
  0x19A ← ⌊p / 20⌋ (no seg = 1 case); 0x1A1, 0x1A3 from ppp; 0x1A7 ← seg; 0x1AB from ppp and p;
  0x1AE ← window start, 0x1AF ← window end; 0x1B5–0x1B7 ← 0; 0x153 ← ⌊p / 5⌋; 0x110 = quick-preview flag,
  0x111 = quick-preview start address, 0x125 = stored display type (0 when quick preview is on and I = 1);
  XY display: 0x199 ← ⌊P / 2⌋; then 0x1BD/0x1BE as below.
- Window (waveform columns, H = 1000): start = 500 − 500 · fx + δ, end = 500 + 500 · fx + δ, with δ = x_c −
  fx · x_r (horizontal position now and when the record was taken, in waveform columns; 0 when neither moved),
  each clamped to 0 … 1000; end = start + 1 when they are equal (start ≠ 0). Examples: 0x190/0x258 (200 ns/div
  record at 1 µs/div), 0x1F4/0x1F5. (**UNVERIFIED**: in decimated sampling the start may also need a pre-trigger
  term read from the PL, and a fixed-memory mode may scale the half-width.)

Worked example (1 ch, depth ≥ 100k so 2 GSa/s at both time/divs; 200 ns/div record viewed at 1 µs/div: S_r = 4000, S_v = 20 000, fx = 0.2,
ppp = 20, P = 4000): live set 0 (0x19B = 0x1F4, 0x19A = 0xFA, 0x1A7 = 4, 0x153 = 0x3E8); 0x174 ← 0x20,
0x172 ← 1; stored ACQ banks; stored set 0 0x19B = 0x190, 0x19A = 0xC8, 0x1A7 = 1, 0x1AE = 0x190, 0x1AF = 0x258,
0x153 = 0x320 (0x19D, 0x19F, 0x1A1, 0x1A3 unchanged, not rewritten), words 0x1001, 0x0, gain/offset 0x1, 0x8001, 0x1,
0x0, sets 1–3 empty with their gain/offset pulses up to 0x8031; 0x13E ← 5 (§7.9 computed with p = 20); live
set 0 again (0x1AE = 0, 0x1AF = 0x3E8 …); 0x172 ← 0, 0x174 ← 0; arm (0x13A ← 0x1A, 0x13E ← 0 recalled).

**Roll map set** (replaces the main set n while roll is on; spec 03 §6.4, spec 04 §7). Inputs: S of this section for
the roll rate, ppp = ⌊S / 1000⌋ & 0xFFFF, u1 = ⌊ppp / 10⌋, u2 = u1 if ppp mod 4 = 0, else 4·u1. Write: 0x10A ← 0
(zoom off), 0x120 ← 1 (scan enable), 0x124 ← 0x16, 0x1A3 ← u1, 0x18F ← 0, 0x19B ← u2, 0x19A ← ⌊u2 / 2⌋ + 1,
0x1A1 ← 0, 0x19F ← 1, 0x19D ← 10, 0x1AB ← 1, 0x1A7 ← 1, 0x1AE ← 0, 0x1AF ← 0x3FF, 0x184 bit 0 ← 1 (one frame),
0x1B1 ← number of enabled channels, the optimized refresh, 0x153 ← ⌊ppp / 5⌋ (the output length uses ppp in roll),
0x110 ← 0. Example (1 ch, S = 1e6, ppp = 1000): 0x120 = 1, 0x124 = 0x16, 0x18F = 0, 0x19B = 0x64, 0x19A = 0x33,
0x1A7 = 1, 0x1AF = 0x3FF, 0x1B1 = 1, 0x153 = 0xC8 (0x1A3 = 0x64 and the rest unchanged from the 1M live set).

Roll stop: 0x120 ← 0, then per set only the codes 0x1BD/0x1BE. A change of the number of channels issues, for
n < C, a transaction n containing only idx 0x1BE ← 0xF70 (a fixed code inside the screen, not the screen limit
of spec 03 §5).

Writing all of these registers in every set is safe; a firmware with a shadow copy may skip unchanged values
(same rule as the ACQ banks, spec 02 §4.3).

### 7.9 DPO parameters

At map init, after every timebase change and after the stored map sets of a stored-record redraw (§7.8, spec 03
§3.7 step 4; p = the stored set's ppp): idx 0x189 ← 1 (DPO valid), then (x, ud, a) → idx 0x1A9, 0x13A,
0x13E (main) and 0x1AA, 0x14C, 0x13F (zoom), computed as below — the main triple with p and I of the main set, the
zoom triple with ppp_z and I_z of the zoom set (written at zoom on and on every zoom-scale change; observed zoom
200 ns/div under 1 ms/div, ppp_z = 2: 0x1AA = 1, 0x14C = 0, 0x13F = 0x10; zoom 20 µs/div, ppp_z = 200: 0x13F = 0). At the start of each run-mode acquisition (pre-arm)
(x, ud, a) are instead taken from a table, but only when the acquisition samples at the undecimated
ADC rate (sampling mode 1; at decimated rates, e.g. 1 ms/div, nothing is recalled and the computed values stay;
further conditions **UNVERIFIED**, met in every observed run state). Index =
min(tb_enum − 1, 20): the DPO table starts at enum 1 = 500 ps/div regardless of the model's minimum time/div
(spec 04 §2). The table is chosen by the route mode (spec 04 §1):

- table B (route 0, one channel at full rate): (1,0,0) (1,0,0) (1,1,0) (1,2,0) (1,4,0) (1,6,0) (1,3,0) (1,6,0) (1,9,0) (1,15,0)
  (1,26,0) (1,10,0) (2,4,0) (4,2,0) (13,0,0) (17,0,0) (15,0,0) (12,0,0) (15,0,0) (31,0,0) (31,0,0);
- table A (route 1 or 2, two or more channels; also the value at map init): (1,0,1), (1,0,0) × 8, (1,2,0) (1,4,0) (1,7,0) (2,2,0) (2,1,0) (4,0,0) (6,0,0)
  (7,0,0) (3,0,0) (7,0,0) (15,0,0) (15,0,0).

Observed pre-arm recalls: 1 ch 5 ns/div (enum 4) → B[3] = (1,2,0); 1 ch 1 µs/div → B[10] = (1,26,0); 1 ch 500 ns/div
→ B[9] = (1,15,0); 4 ch 500 ns/div → A[9] = (1,2,0). After the frame the computed values are rewritten (e.g.
0x13A ← 0, 0x13E ← 0xC), so in Normal/Auto the pair alternates per frame. The 2- and 3-channel cases of table A are
not yet confirmed on a unit.

Computed form. Inputs: c = DPO configuration value (1 in every observed state; a firmware uses 1), p = points
per pixel ppp of §7.8, i = interpolation factor I & 0xFF, slope = trigger slope enum, flag = mode flag (false in
every observed state), acquisition-status flag (0 in every observed state). Write nothing if c, p or i is 0.
If roll mode or the acquisition-status flag = 1, and no history playback: c = 1. If flag is false: i = 1.
n = p · c.

- If p < c: k = max(1, round(√⌊p² / 160⌋)); t = ⌊⌊n / 160⌋ / k⌋; y = 1 if t < i else ⌊t / i⌋.
- Else: y = max(1, round(√⌊c² / 160⌋)); k = 1 if ⌊n / 160⌋ < y else ⌊⌊n / 160⌋ / y⌋.
- q = ⌊128 · i / n⌋; a = q − 1. If q ≥ 4 and the acquisition-status flag is 0 (or history is playing):
  a = a >> 1 when a > 16. If q < 4 (or status ≠ 0): a = 0 when q = 0. Finally a = min(a, 16).
- ud = y − 1, or y when slope = 2 and y − 1 is odd.
- x = k >> 5 (k > 512), k >> 4 (k > 256), k >> 3 (k > 128), k >> 2 (k ≥ 5), k >> 1 (k = 3, 4), 1 (k < 3).

Examples: boot, c = p = i = 1 → 0x1A9 ← 1, 0x13A ← 0, 0x13E ← 0x10; c = 1, p = 10 → 0x13E ← 0x0B. The PL
semantics of x / ud / a (decay and accumulation per persistence frame) are **UNVERIFIED**.

### 7.10 Start-up map sequence

Start-up (spec 03 §2 step 12 "MAP-plane init") writes the map plane in this order (values for the
4-channel model and the factory setup; groups marked *setup* take their values from the recalled setup).
Writes that repeat an unchanged value are omitted. A firmware reproduces the groups in this order; within a
group the order is as listed.

| # | Group | Writes |
|---|---|---|
| 1 | DPO (§7.9) | 0x189 ← 1; 0x1A9 ← 1, 0x13A ← 0, 0x13E ← 0x10 |
| 2 | pass/fail outputs off (spec 15) | 0x174 ← 0x20, 0x172 ← 1 |
| 3 | channels off | 0x106, 0x107, 0x108, 0x109 ← 0 |
| 4 | restart block (§7.2) | 0x0AF ← 0, 1; 0x103 ← 1, 0; 0x170 ← 0x100, 0 (bit-8 pulse; bit 0 not yet set, §7.2); 0x15A ← 0, 1 |
| 5 | timebase sets n = 0 … C − 1 (§7.8, §7.3) | boot geometry (1 µs/div class: 0x19D ← 10, 0x060 ← 250, 0x19F ← 1, 0x19B ← 500, 0x19A ← 250, 0x1A1 ← 0, 0x1A3 ← 1, 0x1A7 ← 2, 0x1AB ← 1, 0x1AE ← 0, 0x1AF ← 1000, 0x153 ← 1000, 0x110 ← 0, 0x1B5–0x1B7 ← 0), then 0x125 ← 1 (display type, vectors), 0x111 ← 0, 0x1BD ← 0, 0x1BE ← 0x1DF |
| 6 | trigger-level preview | 0x1E7 ← code − h, 0x1E9 ← h, 0x1E8 ← code + h (rule: spec 05 §3.2 "map-side copy", appendix MAP rows; boot values 0x3C2 / 0x3E / 0x43E) |
| 7 | persistence/DPO control | 0x080 ← 3, 0x140 ← persistence type (§7.2; 0 = off), 0x184 ← 0 (one-frame mode off) |
| 8 | knob period (spec 08 §4.2) | 0x16D ← 75 |
| 9 | magic grid / trace | 0x0D4 ← 1, 0x0D5 ← 0, 0x0D2 ← 2, 0x0D3 ← 0 |
| 10 | waveform layer hidden (§3.2) | 0x0FF ← 1 (set 0 once the first acquisition is displayed) |
| 11 | pass/fail red, overlay, search counters | 0x179 ← 0, 0x0DB ← 1, 0x1FA ← 0xA5, 0x1FB ← 0xB0, 0x115 ← 1 |
| 12 | zoom split off (§7.4) | 0x10A ← 0, 0x090 ← 1, 0x091 ← 480, 0x092 ← 480, 0x093 ← 0, 0x08F ← 1, 0 |
| 13 | LEDs (spec 08 §6) | 0x16C, 0x16A, 0x16F |
| 14 | XY off, sequence off | 0x192 ← 0; 0x1BA ← 0, 0x1BF ← 0, 0x1BB ← 2, 0x1BC ← 0 |
| 15 | channel recall (spec 03 §2 step 14) | 0x105 (trace-visible mask, ends 0xF), ranks 0x130 ← 0 then 0x131–0x134 (§7.1), channel enables per recalled channel |
| 16 | serial trigger defaults | M-block registers of spec 05 §2.2 / §10 |
| 17 | appearance, *setup* (§7.1) | 0x0E7 (colour mode), 0x0FE (intensity), 0x0D0/0x0D1 (grid), 0x14D, 0x0FA (transparency); factory values 0, 0x42, 0x3333, 0x33, 0x44B, 0x99 |
| 18 | waveform search off | 0x1E6 ← 0, 0x1EB ← 0, 0x1EA ← 0 |
| 19 | horizontal pixel mapping (§7.4), once the UI is up | 0x0C3 ← 1000, 0x0C4 ← 1000, 0x0C5 ← 0xA0, 0x0C6 ← 0x9E, 0x0C7 ← 0x3FF, then 0x08F pulse |
| 20 | search positions | 0x1FD ← 0x4B, 0x1FF ← 0xD2 |

Groups 1–12 run within ~0.6 s after the ACQ register init, 13–18 during the start-up recall, 19–20 when the UI
has laid out (the delay is not critical). The per-frame cycle (restart block, 0x174/0x172, 0x190 transactions)
then repeats as in spec 03 §3.6. Meaning of 0x1FA/0x1FB/0x115/0x184/0x0D2–0x0D5 values beyond their names is
**UNVERIFIED**; reproduce them as listed.

---

## 8. Minimum path to pixels for a replacement firmware

1. Keep RAM ending at 0x1F800000, and reserve 0x1E400000–0x1F7FFFFF for the PL (declare it `no-map`, or allocate
   the CMA pools through their drivers before use; §2, spec 01 §4). Do not give DRAM above the declared memory
   node (0x00000000–0x1F7FFFFF) to Linux (record memory, spec 01 §13).
2. Load the PL bitstream (spec 13).
3. Program UI VDMA MM2S (§4.1) with your ARGB8888 framebuffer at 0x1FC00000 (or 0x1F400000), waveform VDMA
   (§4.3); UI VDMA S2MM (§4.2) only if you want capture.
4. LCD reset pulse, brightness (§6), enable.
5. Draw opaque UI pixels (alpha 0xFF). With no acquisition running the waveform layer is whatever the
   PL left in 0x1E400000; clear the picture (§7.2 restart block) and disable all channels (idx 0x106…0x109 ← 0) to
   get an empty waveform area.
6. For traces: configure the map engine per §7.8 (timebase sets, after the ACQ timebase of spec 04 §8), §7.9 (DPO),
   §7.1 (appearance), write idx 0x0FF ← 0
   (waveform layer shown, §3.2) and arm acquisitions (spec 03); the PL draws.

A firmware that renders waveforms in software can instead keep all map channels disabled and draw
traces into its own framebuffer.

---

## Open

- Panel timing (pixel clock, porches, refresh) — measure on J15.
- Mixer equation: role of the fb0 alpha byte, idx 0x0FA transparency and idx 0x0F9 base colour. (The PL palette,
  pixel format and placement need a unit.)
- Waveform frame-store pixel format at 0x1E400000 (dump on a unit).
- Exact on-panel position of the waveform layer inside the PL mixer (assumed equal to the UI graticule, (19, 50)).
- Channel trace palette inside the PL; meaning of "magic trace/grid" (idx 0x0D2–0x0D5).
- Whether priority rank 0 (idx 0x131–0x134) is drawn on top.
- idx 0x1B0.
- Backlight register polarity and the light output vs. value curve.
- Latch edge of the idx 0x190 configuration transaction; PL effect of idx 0x0AF / 0x170 in the clear block.
- §7.8: the YT+XY set variant and the zoom-set transaction variant are not yet confirmed on a unit (XY-only sets observed at
  200 ns and 1 µs/div); the roll map set is observed at ppp = 1000 only (the ppp mod 4 ≠ 0 branch is not yet
  confirmed); the XY pixel mapping 0x0C3–0x0C5 is observed with a side menu open only; the further conditions of
  the optimized-refresh rule; stored-record window: the decimated-sampling start term, the fixed-memory
  factor and the unit of the horizontal positions x_c/x_r (one observed state with δ = 0). §7.9: DPO semantics (decay/accumulation per frame).
- The zoom/gain-offset transaction variants of §7.3 and all PL-side effects are not yet confirmed on a unit.
