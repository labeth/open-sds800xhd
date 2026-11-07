# 14 — Kernel Driver Interface

This spec defines the board-specific and third-party kernel drivers of the SDS800X HD as a userspace ABI —
device node, open/read/write/poll/ioctl/mmap semantics, buffers — and the hardware behind each one (interrupt line,
acknowledge register, memory), so that a replacement firmware can either keep using the vendor modules or replace
them (e.g. with UIO or its own module). Module load order and the node list are in spec 01 §7; PL register
semantics beyond acknowledge/clear belong to spec 02/03.

---

## 1. Summary

All board-specific modules are platform drivers matched by DT `compatible`; each has a **dynamic** character major
and one device with **minor 0**, and devtmpfs creates the node (`CONFIG_DEVTMPFS_MOUNT=y`). Userspace must open by
path, never by a fixed major. Only one DT instance per driver is supported.

| Node | Module | DT compatible | IRQ (DT SPI / GIC INTID / PL line) | Trigger as requested | Ack in IRQ handler | Buffer |
|---|---|---|---|---|---|---|
| `/dev/siglent_dma` | siglent_dma | `siglent,dma-v1.0` | 94 / 126 / pl_ps_irq0[5] | DT level-high | read 32-bit 0x80030580 | 4 MiB from pool `wavedata` (0x1E400000) |
| `/dev/dmadata_irq` | siglent_dmadata_irq | `siglent,dmadata-irq-v1.0` | 89 / 121 / pl_ps_irq0[0] | DT level-high, shared | write 0xFFFFFFFF to 0x80010034 | — |
| `/dev/cpld_irq` | siglent_cpld_irq | `siglent,cpldirq-v1.0` | 105 / 137 / pl_ps_irq1[1] (tied to 0 in the PL, spec 12 §4.1) | rising edge | none | — |
| `/dev/siglent_kb` | siglentkb_sds | `siglent,keyboard-v1.0` | 90 / 122 / pl_ps_irq0[1] | DT level-high | read 32-bit 0x800405B8 | — |
| `/dev/siglent_vnc` | siglent_vnc | `siglent,vnc-v1.0` | 104 / 136 / pl_ps_irq1[0] | rising edge | none | — |
| `/dev/udmabuf0` | u_dma_buf (ikwzm) | `ikwzm,u-dma-buf` | — | — | — | 8 MiB from pool `rawdata` (0x1E800000) |
| (dmaengine) | xilinx_dma | `xlnx,axi-vdma-1.00.a` | 91, 96 (UI VDMA), 92, 93 (waveform VDMA) | DT level-high | Xilinx driver | — |

GIC INTID = SPI + 32. PL→PS lines: pl_ps_irq0[0..7] = SPI 89–96, pl_ps_irq1[0..7] = SPI 104–111.
"Ready" flags below are single booleans: events are not counted, so several interrupts between two waits collapse
into one.

## 2. `/dev/siglent_dma`

Hardware: interrupt SPI 94 from the PL; acknowledge = a 32-bit **read** of PL 0x80030580 (the value is discarded;
the read clears the request). DT `reg` maps 2 bytes at 0x80030580 (a full page is mapped).

**Before `insmod siglent_dma.ko` the request must be cleared by one 32-bit read of 0x80030580** (the boot does
this before loading the module, spec 01 §7). A request pending at load time oopses the kernel in interrupt context
and leaves the line asserted.

Buffer: the driver's buffer is the whole DT `memory-region` (pool `wavedata`, 0x1E400000, 4 MiB, CMA reusable),
allocated at probe; the physical address is expected to be 0x1E400000 (pool start;
**UNVERIFIED**, not exported). This is the same memory the waveform VDMA uses (spec 13 §5.2).

| Operation | Behaviour |
|---|---|
| open / release | always succeed, no per-file state |
| read(buf, n) | copies **n** bytes from the start of the DMA buffer to `buf`, **returns 0** (not n), also when `buf` is invalid (no `-EFAULT`). No bound check against 4 MiB: n must be ≤ 4 194 304. n > 0x7FFFFFFF triggers a kernel BUG. Does not wait and does not clear the ready flag |
| write | returns 0, no effect |
| poll | registers on the wait queue; returns `POLLIN|POLLRDNORM` (0x41) while ready = 1, else 0 |
| ioctl(fd, 1, any) | ready ← 0. Any other cmd: no effect. Always returns 0 |
| mmap | returns 0 without mapping anything (do not use; map the memory via `/dev/mem` instead) |
| IRQ | ack read; ready ← 1; wake all pollers |

Usage pattern: `poll()` for POLLIN → consume (via `read` or a `/dev/mem` mapping of 0x1E400000) → `ioctl(fd, 1)` to
re-arm. On this board the device serves as the **frame-ready signal of the read-data request** (spec 03 §3.6,
02 §10): after requesting a frame (MAP 0x148 ← 0), wait for readability, then `ioctl(fd, 1)`. The buffer is not
needed for this: the record moves by the AXI DMA (spec 03 §4.3). The PL raises SPI 94 when a pending read-data
request has a frame ready.

## 3. `/dev/dmadata_irq`

Hardware: interrupt SPI 89 = the S2MM interrupt of the PL's Xilinx AXI DMA at 0x80010000 (spec 02/03; the firmware
programs that DMA directly through `/dev/mem` to write sample data into udmabuf0). Acknowledge = write
0xFFFFFFFF to 0x80010034 (AXI DMA `S2MM_DMASR`; write-1-to-clear of IOC/Dly/Err interrupt bits). The driver
`ioremap`s that address itself (not from DT). IRQ requested with `IRQF_SHARED`.

| Operation | Behaviour |
|---|---|
| open | succeeds |
| read / write / ioctl / mmap | no handlers: read and write return `-EINVAL`, ioctl `-ENOTTY`, mmap `-ENODEV` |
| poll / select | returns `POLLPRI|POLLRDNORM` (0x42) if ready = 1 **and clears ready**; else 0 |
| IRQ | if ready = 0: ready ← 1, wake. Then (always) write 0xFFFFFFFF to 0x80010034 |

Wait with `select()` (exceptfds or readfds) / `poll(POLLPRI)`; one successful poll consumes the event. The flag stays
latched until a poll consumes it — nothing clears it at the start of a transfer — so a completion that arrived
after a timed-out wait satisfies the next wait at once: drain it (poll with timeout 0) before each transfer
(spec 03 §4.4).

## 4. `/dev/cpld_irq`

Hardware: interrupt SPI 105, requested rising-edge (overrides the DT level type); no acknowledge register. In the
PL image (boot-time and Linux-loaded) pl_ps_irq1[1] is **tied to constant 0** (spec 12 §4.1): the device never
signals on this product. Semantics identical to §3 (poll returns 0x42 once per event and clears
it; read/write `-EINVAL`, ioctl `-ENOTTY`, mmap `-ENODEV`). A replacement needs no handling for it.

## 5. `/dev/siglent_kb`

Hardware (DT `reg`): key status words at **0x80040580** (4 consecutive 32-bit words read: +0x0, +0x4, +0x8, +0xC;
with `bit-nums = 128` a further 4 words +0x10…+0x1C), step/encoder register **0x800405A4**, acknowledge register
**0x800405B8**. `bit-nums` = 64 on this board. Interrupt SPI 90, DT level-high.

IRQ handler, in order: read the 4 key words w0…w3; (128-key mode: 4 more); read the step register s; read the
acknowledge register (clears the request). If no reader is currently blocked in `read`, the event is **dropped**
(the IRQ is still acknowledged). Otherwise it builds the key byte array and stores a record.

Key byte array (64-key mode), k = 0…7: byte k = bits 15:8 of w[3 − k/2] if k is even, bits 7:0 of w[3 − k/2] if k is
odd (only the low 16 bits of each word are used). Keys are active-low (pressed = 0).

Record (4 bytes): find the first k with byte k ≠ 0xFF and, in it, the first bit j (j = 0 is bit 7, MSB) that is 0:

| Byte | Value |
|---|---|
| 0 | k (0–7), or 0xFF if all bytes are 0xFF (all keys released) |
| 1 | j (0–7), or 0xFF if all released |
| 2 | low byte of s (encoder/step value; spec 08) |
| 3 | 0 |

Only the first pressed key is reported. (128-key mode: 16 bytes, byte k from w[7 − k/2], same rule.) Because events
are captured only while a reader waits, a held key cannot be detected through this node; read the key words
directly for that (spec 01 §12.3).

| Operation | Behaviour |
|---|---|
| read(buf, n) | n must be 4, else returns −11 (`-EAGAIN`). Marks "reader waiting", then sleeps (interruptible) until an event; on a signal returns −11 and the "reader waiting" mark stays set, so the next key event is still captured and returned by the next read. Copies the 4-byte record, clears the event, returns 4 (−14 on fault) |
| poll | `POLLIN|POLLRDNORM` while an event is pending (only possible while a reader is waiting, so poll alone never becomes ready — use blocking `read` in a thread) |
| write | returns 0, no effect |
| open / release | succeed |

Key index → key name, LED control (MAP-plane LED word, spec 08 §6) and encoder decoding: spec 08.

## 6. `/dev/siglent_vnc`

Hardware: interrupt SPI 104, requested rising-edge; no register access in the driver. Raised by the PL once per
captured frame of the VNC video stream (UI VDMA S2MM into 0x1F800000, spec 13 §5.3).

| Operation | Behaviour |
|---|---|
| read(buf, n) | sleeps until ready (interruptible; on a signal returns −11); copies n bytes from an 8-byte all-zero field, ready ← 0, returns n (−14 on fault). The driver does not check n: the caller must keep n ≤ 8 (larger n copies adjacent kernel memory) |
| poll | returns `POLLPRI` (0x02) if ready **and clears ready**; else 0 |
| IRQ | ready ← 1, wake |

Use: wait with `poll(POLLPRI)`, then read the captured 1024×600 RGB565 frame from 0x1F800000 through `/dev/mem`
(spec 11 §12).

## 7. `/dev/udmabuf0` (u-dma-buf)

Third-party driver (ikwzm u-dma-buf, built with `USE_OF_RESERVED_MEM`, `IS_DMA_COHERENT`, quirk-mmap). DT node:
`device-name = "udmabuf0"`, `size = 0x800000`, `sync-mode = 1`, `memory-region` = pool `rawdata` (0x1E800000,
8 MiB). ABI (upstream u-dma-buf):

- `open("/dev/udmabuf0", O_RDWR [|O_SYNC])`; `mmap(…, size, PROT_READ|PROT_WRITE, MAP_SHARED, fd, 0)` maps the
  buffer. With `O_SYNC`, `sync-mode` 1 makes the mapping non-cached; without `O_SYNC` it is cached and the caller
  must use the sync attributes.
- sysfs `/sys/class/u-dma-buf/udmabuf0/` (driver version 4.4.1): `phys_addr` (expected 0x1E800000), `size`
  (8388608), `sync_mode`, `sync_offset`, `sync_size`, `sync_direction`, `sync_owner`, `sync_for_cpu`,
  `sync_for_device`, `dma_coherent`, `quirk_mmap_mode`, `debug_vma`, `device_name`, `driver_version`. Writing `1` to
  `sync_for_device` / `sync_for_cpu` performs the cache maintenance for the range `sync_offset`/`sync_size` in
  `sync_direction` (upstream semantics). `sync_for_device` after a transfer is not a CPU-side invalidate; a
  replacement uses an `O_SYNC` mapping or `sync_for_cpu` as specified in spec 03 §4.4.
- The DT node has no `dma-coherent` property, so the device is expected to be non-coherent (`dma_coherent` reads 0;
  **UNVERIFIED**, read the attribute on a unit); `IS_DMA_COHERENT` in the build options only makes the attribute
  available.
- The upstream 4.x ioctl interface may also be present (**UNVERIFIED**).

The PL AXI DMA (spec 02 §5.2, spec 03 §4.4) writes acquisition data to this buffer (used size: spec 03).

## 8. xilinx_dma

The upstream Xilinx AXI DMA/VDMA dmaengine driver. It binds to the two VDMA nodes (0x80000000, 0x80020000), resets
their channels at probe and registers dmaengine channels; it creates no device node and no userspace uses it. It must be loaded only while the PL is configured. Interrupts SPI 91 (menu MM2S), 96
(menu S2MM), 92 (wave MM2S), 93 (wave S2MM) are owned by this driver once loaded. A replacement that programs
the VDMAs directly (spec 13 §5) does not need it.

## 9. USB device side (USBTMC gadget)

`g_usbtmc.ko` (legacy gadget "Usbtmc Gadget", module parameters `idVendor`, `idProduct`, `bcdDevice` (ushort),
`qlen` (uint), `iManufacturer`, `iProduct`, `iSerialNumber`, `iSerialNum` (default `"1"`), `iPNPstring` (default
`MFG:linux;MDL:g_usbtmc;CLS:USBTMC;SN:1;`) (charp)) and `f_usbtmc.ko` (composite function
`usbtmc`, used with the built-in libcomposite/configfs). The boot does not load them; the instrument
firmware loads first `insmod …/f_usbtmc.ko` (no parameters), then `insmod …/g_usbtmc.ko idVendor=0xf4ec
idProduct=0xee38 iManufacturer="Siglent" iProduct="<model>" iSerialNum=<serial>`. `iSerialNum` and the
composite framework's `iSerialNumber` are two names for the same storage (the composite overwrite serial
string), so `iSerialNum=<serial>` sets the device descriptor's serial-number string; `iPNPstring` (`…SN:1;`) is a
separate parameter and stays at its default (spec 11 §5.1). Default `qlen` = 160. g_usbtmc creates `/dev/g_usbtmc`
and `/sys/class/usb_tmc_gadget/g_usbtmc/send_buffer_size`. The UDC is USB1 (DWC3 0xFE300000, peripheral,
high-speed). Character-device ABI of the gadget: spec 11.

Host side: the kernel's built-in `usbtmc` class driver (`/dev/usbtmc0…`) talks to external USBTMC instruments on
USB0 (spec 11/15).

## 10. Other drivers used

| Driver | Node / interface | Notes |
|---|---|---|
| `gt9xx.ko` (Goodix GTP) | `/dev/input/eventN`, name `goodix-ts` | I2C bus 0 address 0x14; spec 08 §7 |
| `uinput.ko` | `/dev/uinput` | vncserver injects pointer/key events |
| spidev (built-in) | `/dev/spidev0.0`, `/dev/spidev1.0`–`1.7` | DT children `compatible = "rohm,dh2228fv"`; `SPI_IOC_MESSAGE`, mode/speed ioctls; spec 01 §1, spec 06 |
| siglentfb (built-in, `siglent,lcd-fpga-1.00.a`) | `/dev/fb0` (0x1FC00000), `/dev/fb1` (0x1F800000) | plain memory framebuffers, 1024×600; the driver does not program the VDMA (scan-out is set up by spec 13 §5; **UNVERIFIED**: register writes by this driver) |
| zynqmp fpga_manager | `/sys/class/fpga_manager/fpga0/` | spec 13 §3.3 |
| tmp421 hwmon | `/sys/bus/i2c/devices/0-004c/hwmon/hwmon0/temp{1,2,3}_input` | spec 15 |
| cadence i2c | `/dev/i2c-0` | — |
| `/dev/mem` | unrestricted (`STRICT_DEVMEM` off) | PL registers, framebuffers, buffers |

Two further built-in board input drivers have no DT node and are inactive on this product.

## 11. Replacing the vendor modules

A replacement may drop any custom module and handle the hardware itself, provided it reproduces:

| Function | Requirement |
|---|---|
| siglent_dma | clear a pending request (read 0x80030580) before enabling SPI 94; wait for SPI 94; acknowledge by reading 0x80030580 before re-enabling the interrupt; buffer = 0x1E400000, 4 MiB (keep it reserved; it is shared with the waveform VDMA) |
| dmadata | wait for SPI 89; acknowledge by writing 0xFFFFFFFF to 0x80010034 (AXI DMA S2MM_DMASR) |
| cpld | nothing (SPI 105 is tied low in the PL image) |
| keyboard | on SPI 90 read 0x80040580…0x8004058C, 0x800405A4, then read 0x800405B8 to acknowledge |
| vnc | wait for a rising edge on SPI 104 |
| udmabuf | contiguous uncached (or synced) 8 MiB at 0x1E800000 visible to userspace |

The kernel has `CONFIG_UIO=y`; `uio_pdrv_genirq` is configured as a module but not shipped, so a UIO-based
replacement needs its own module build for vermagic `5.10.0+ SMP mod_unload aarch64` (the kernel is a modified Xilinx
5.10 tree whose source is not available) and DT changes — **UNVERIFIED** that an out-of-tree module built
against a mainline 5.10 tree loads (no module signing or modversions are configured).

Level-triggered lines (SPI 89, 90, 94) must be acknowledged in the hardware before the interrupt is unmasked again,
otherwise they re-fire continuously.

## Open

- Physical address actually returned for the siglent_dma buffer (expected 0x1E400000); the PL condition that raises
  SPI 94 beyond "a read-data request (MAP 0x148) has a frame" (spec 03 §3.6).
- Semantics of the 0x800405A4 step value (spec 08).
- `dma_coherent` of udmabuf0 on a unit (expected 0).
- Whether `siglentfb` writes any PL register (e.g. on `FBIOPAN_DISPLAY`); the depth `/dev/fb1` reports (fb0: spec 07 §2.1).
- Driver semantics above are not yet confirmed on a unit.
