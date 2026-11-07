# 16 — Test environment (tier-1 emulation)

The test environment runs aarch64 SDS800X HD userspace — the original firmware installed on a unit or a
replacement firmware — unmodified on an x86-64 Linux host, against a software model of the board. It is the
reference for dynamic verification of specs 01–15: every access the software makes to hardware is recorded, and
the model answers the way the hardware described in those specs does. The simulator lives in a private reference
workspace (paths below are relative to it: source `sim/`, usage `sim/README.md`); this repository's `sim/` holds
the harness that drives it.

## 1. Architecture

| Layer | Implementation |
|---|---|
| CPU / user ABI | qemu-aarch64 10.1.2 linux-user, static, with a device-forwarding layer (`sim/qemu`) |
| Userspace | the unit's Buildroot rootfs (glibc 2.32, BusyBox, Qt 5.15) as container root; app partition read-only at `/usr/bin/siglent`; writable per-run p12 (`/usr/bin/siglent/usr`), p9 (`/usr/bin/siglent/firmdata0`) and `/tmp` |
| Kernel | not emulated. Syscalls run on the host kernel, except those listed in §2 which are forwarded |
| Board | `simd` (Go): device nodes, sysfs/procfs files, PS DDR, PL registers, SPI/I2C chips, IRQs (§3–§5) |
| Isolation | rootless podman; no network by default; optional loopback-only port publishing (§6); no host devices |

Every process of the tree runs under the same qemu: `execve` of an aarch64 ELF or a `#!` script re-enters
qemu, so boot scripts, `system()` calls and helper tools are emulated and logged too.

## 2. Forwarded operations

- `openat`, `stat`/`fstatat`/`faccessat`/`fchmodat`, `getdents64` (listing injection) for paths under `/dev/`,
  `/sys/`, `/proc/devices`, `/proc/modules`, `/proc/bus/`, `/proc/cmdline`, `/proc/mtd`, `/proc/gt9xx_config`,
  `/proc/cpuinfo`. Paths are normalised (`//` collapsed). `simd` answers *emulated*, *pass* (container file:
  `/dev/null`, `/dev/tty*`, `/dev/shm`, `/proc/self` …), *synthetic directory* (opened as an empty directory with
  injected entries) or ENOENT. Sysfs is entirely synthetic: unknown `/sys` paths are ENOENT.
- On emulated fds: `read`/`pread64`/`readv`, `write`/`pwrite64`/`writev`, `lseek`, `ioctl` (structures copied by
  the `_IOC` size; pointer-carrying ioctls access guest memory through the socket), `ppoll`, `pselect6`,
  `mmap`, `fstat`. `sendfile`, `splice`, `copy_file_range`, `tee` on emulated fds fail with EINVAL (callers fall
  back to read/write).
- `mmap` of an emulated fd yields either *shared memory* (a host memfd mapped `MAP_SHARED`: DDR, framebuffers,
  DMA buffers) or a *trapped* range (`PROT_NONE`; each load/store faults, is decoded — LDR/STR of all widths and
  addressing modes, LDP/STP, exclusive and acquire/release forms, SIMD/FP scalar and Q — and performed by
  `simd` as one access with the physical address, PC and caller chain).
- `init_module`, `finit_module`, `delete_module`: the image is identified by its `.modinfo` name, the probe's
  nodes are created (§3.2) plus `/sys/module/<name>/{initstate,refcnt}`; always succeeds.
- File status flags of emulated fds are virtual: `fcntl(F_SETFL)` and `FIONBIO` record O_NONBLOCK (reported by
  `F_GETFL`) but the RPC socket stays blocking; non-blocking behaviour of a device read is decided by the open()
  flags. `epoll_ctl` on an emulated fd fails with EPERM (as for a regular file): wait on IRQ nodes with
  `poll`/`select`. With these rules unmodified Go binaries (os.Open/ReadFile/WriteFile, runtime netpoll registration,
  async-preemption signals) need no workaround; regression test `sim/test/run.sh` (incl. concurrent MMIO
  from four threads; also runs the readout time-axis check `sim/test/taxis.sh`, §5.3, and the roll /
  mode-exit / Auto-read check `sim/test/roll.sh`, §5.1, §5.3).
- Process identity: each guest process sets its host `comm` to what the kernel would show (basename of the
  executed file; for a `#!` script the script), so `/proc/<pid>/{comm,stat,status}` read as on the device;
  `/proc/<pid>/cmdline` of other guest processes is rewritten to the guest argv (qemu itself fakes `/proc/self`).
  `/proc/<pid>/exe` of another process still points at qemu.
- `exit_group`, `kill`, `tgkill`, `tkill` are logged (`proc` events with caller chain) and then executed
  (SIGURG and signal 0 are not logged: Go's preemption signal).

Blocking follows the drivers: a `read` that blocks in the driver blocks in `simd`; `poll`/`select` on emulated
fds are evaluated every 2 ms. `select`/`pselect6` use the kernel's readiness sets: an fd in the read set is ready
on POLLIN, POLLRDNORM, POLLRDBAND, POLLHUP or POLLERR, in the write set on POLLOUT, POLLWRNORM, POLLWRBAND or
POLLERR, in the except set on POLLPRI (the PL interrupt device drivers report POLLPRI | POLLRDNORM); `poll`/`ppoll` return
the driver mask filtered by the requested events | POLLERR | POLLHUP.

Concurrency: any number of threads of one process may access trapped ranges and emulated fds at the same time,
as with real MMIO. Each request/reply exchange holds the lock of its daemon connection (one lock per socket,
shared by all fds that refer to it: every trapped mapping keeps a duplicate of its mapping fd, and guest
`dup`/`dup2` copies share the socket), so accesses through one fd are serialised and accesses through
different `open()`s run in parallel; each access is atomic in the model. Two *processes* must not use one
inherited emulated fd at the same time (the lock is per process); open the node in each process.

## 3. Board model

### 3.1 Physical address space (`/dev/mem`)

| Physical range | Model |
|---|---|
| 0x0000_0000–0x1FFF_FFFF | PS DDR: one sparse shared memory object. Reserved per DT: wavedata 0x1E40_0000 (4 MiB; siglent_dma buffer, waveform VDMA frame), rawdata 0x1E80_0000 (8 MiB; udmabuf0, AXI DMA destination), logo 0x1F00_0000, U-Boot fb 0x1F40_0000, fb1 0x1F80_0000, fb0 0x1FC0_0000 |
| 0x8000_0000 + 64 KiB | AXI VDMA "menu" (PG020: CR.reset self-clears; SR.halted = !CR.RS; VERSION reads 0x06300000) |
| 0x8001_0000 + 64 KiB | AXI DMA, S2MM channel used (§5) |
| 0x8002_0000 + 64 KiB | AXI VDMA "wave" (as menu) |
| 0x8003_0000 + 64 KiB | **acq** plane: register idx at `0x8003_0000 + 4·idx`, 16 significant bits |
| 0x8004_0000 + 64 KiB | **map** plane: `0x8004_0000 + 4·idx` (also LCD reset/enable, backlight, key matrix, VNC kick) |
| anything else | generic 32-bit register file (e.g. PS TTC0 0xFF11_0000, written for the buzzer) |

Default register behaviour: a read returns the last value written (0 after reset); 64-bit accesses are two
32-bit accesses, low word first; sub-word accesses read/modify the containing word. A bitstream load (§3.3)
resets the PL register file and sets the key matrix idle (0xFFFF). Status fields per §4. A JSON script can pin
values, sequences or OR/AND masks per address.

### 3.2 Device nodes

| Node | Created by | Model |
|---|---|---|
| `/dev/mem` | kernel | §3.1 |
| `/dev/fb0`, `/dev/fb1` | kernel (siglentfb) | 1024×600, 32 bpp (for fb1 an assumption of the model: the unit's fb1 memory holds RGB565, spec 01 §4) (R offset 16, G 8, B 0, A 24, each 8 bits), line 4096 B, smem 0x1FC0_0000 / 0x1F80_0000, `id` "siglentfb", truecolor; GET/PUT_VSCREENINFO, GET_FSCREENINFO, PAN, BLANK, WAITFORVSYNC accepted; mmap/read/write = DDR |
| `/dev/spidev0.0` | kernel spidev | DT `kb595`; not connected on this board (spec 12 §4.2); opened by the original application, never used (LEDs are the MAP-plane LED word, spec 08 §6) |
| `/dev/spidev1.0`…`1.7` | kernel spidev | pll, adc0, adc1, hc595, vga1–vga4; mode/bits/speed/LSB ioctls, `write`, `read`, `SPI_IOC_MESSAGE(n)`; every transfer logged with tx/rx. ADC12DL1500 reads (`[0x80\|A[14:8], A[7:0], 0]`) return the chip's register file (power-up FS_RANGE 0xA000; reg 0x06A = 0x03 once 0x06C went 0→1; 0x000 bit 7 resets it); everything else reads back 0. Frames to hc595, vga1–4 and the ADCs feed the analog model (§5.2) |
| `/dev/i2c-0` | kernel | GT911 @0x14 and TMP422 @0x4C are bound to drivers (I2C_SLAVE → EBUSY, I2C_SLAVE_FORCE works); TMP422 regs 0x00–0x02/0x10–0x12 = board temperature, 0xFE = 0x55, 0xFF = 0x22; GT911 product id "911" at 0x8140; @0x74 register file; others ENXIO. I2C_RDWR, I2C_SMBUS, read/write |
| `/dev/uinput` | uinput.ko | UI_DEV_CREATE creates `/dev/input/eventN`; events logged and delivered |
| `/dev/input/event0` | gt9xx.ko | "goodix-ts", phys `input/ts`, ABS_MT_* (0–1023 × 0–599), BTN_TOUCH, INPUT_PROP_DIRECT; `/proc/gt9xx_config` |
| `/dev/siglent_touch` | gt9xx.ko (misc device) | ioctl 0x900 = suspend touch (if not suspended), 0x901 = resume, any other command 0; all return 0; logged. Injected taps are dropped while suspended |
| `/dev/siglent_vnc` | siglent_vnc.ko | read blocks for the IRQ, returns `count` zero bytes; poll → POLLPRI once per IRQ |
| `/dev/siglent_kb` | siglentkb_sds.ko | read(4) blocks for a record; other sizes EAGAIN; poll → POLLIN\|POLLRDNORM (§3.4) |
| `/dev/siglent_dma` | siglent_dma.ko | read copies `count` bytes from 0x1E40_0000 and returns 0; poll → 0x41 while ready; ioctl 1 clears ready |
| `/dev/udmabuf0` | u_dma_buf.ko (4.4.1) | mmap/read/write of 0x1E80_0000, 8 MiB; sysfs `phys_addr` (`%pad`: `0x000000001e800000`), `size` (8388608), `sync_mode` 1, `sync_offset` (`0x0`), `sync_size`, `sync_direction` writable, `dma_coherent` 0, `driver_version` 4.4.1; writes to `sync_for_cpu`/`sync_for_device` are parsed as numbers (base 0; EINVAL otherwise) and logged decoded: bit 0 set → offset = bits 63:32, size = bits 31:4 (×16), direction = bits 3:2 |
| `/dev/dmadata_irq`, `/dev/cpld_irq` | modules | poll → POLLPRI\|POLLRDNORM once per IRQ (cleared by that poll) |
| `/dev/g_usbtmc` | g_usbtmc.ko | no USB host: blocking reads never complete (O_NONBLOCK: EAGAIN), writes logged; `/sys/class/usb_tmc_gadget/g_usbtmc/send_buffer_size` = 4096 (**UNVERIFIED**) |

Procfs: `/proc/bus/input/devices` (one block per input device: `N: Name="goodix-ts"`, `H: Handlers=eventN`),
`/proc/cpuinfo` (four Cortex-A53 entries, `Hardware\t: Xilinx ZUP`, `Revision\t: 0000`; the real unit reports two, spec 01 §1 — code must not size thread pools from this file), `/proc/cmdline`
(`earlycon console=ttyPS0,115200 clk_ignore_unused root=/dev/mmcblk0p7 rw rootwait` — differs from a unit, whose
effective line is that of spec 01 §2.3 with `uboot_version=4`; `/proc/version` is the host's, without
`siglent_version=`, so the kernel part of the `*IDN?` version differs from a unit, spec 11 §6.6),
`/proc/mtd` (header only),
`/proc/devices`, `/proc/modules`.

Sysfs: `class/fpga_manager/fpga0/{name,state,status,flags,firmware,key}`; `class/gpio/{export,unexport}` and
`gpioN/{value,direction,edge,active_low}` after export (GPIO 334–341 = hw-id bits 0–7, others read 0);
`class/hwmon/hwmon0` and `bus/i2c/devices/0-004c/hwmon/hwmon0` (`tmp422`, temp1–3 in m°C); ZynqMP AMS
`devices/platform/axi/ffa50000.ams/iio:device0/in_temp{0_ps,1_remote,2_pl}_temp_{raw,offset,scale}`
(T = (raw − 36058) · 7.771514892 m°C); `class/net/{lo,eth0}/{carrier,operstate,address,ifindex}` (eth0: no
carrier); `class/u-dma-buf/udmabuf0/*`; empty `class/block`, `block`, `bus/hid/devices`, `bus/mmc/devices`,
`kernel/debug`; `module/<name>` for loaded modules.

### 3.3 FPGA manager

A write of a name to `fpga0/firmware` loads `config/fpga/<name>` from the app partition (the boot script links it
into `/lib/firmware`), records size, SHA-256, sync-word offset and the IDCODE of the type-1 IDCODE write,
sets `state` = `operating` and resets the PL register file. Example: the unit's runtime file `eva_top_fpga.bin` (spec 13 §3.1), 5 568 668 bytes,
sync word at offset 80, IDCODE 0x04A43093.

### 3.4 Inputs and interrupts

IRQ lines `cpld_irq`, `dmadata_irq`, `siglent_dma`, `siglent_vnc`, `siglent_kb` are raised by the model (§5)
or from the control socket.

Key matrix: map idx 0x160–0x163 (0x8004_0580–0x8004_058C), 16 active-low bits each; idx 0x169 encoder step
byte. A key injection waits until a reader is blocked in `read()` (at most 3 s), then clears one matrix bit,
sets the step byte and fires the key interrupt; as in the driver, a record is produced only if a reader is
blocked in `read()` (otherwise the event is lost and logged as dropped). The record is derived from the matrix: bytes in the order reg3[15:8], reg3[7:0], reg2[15:8], …,
reg0[7:0]; row = index of the first byte ≠ 0xFF, bit = first 0 bit from the MSB; {0xFF, 0xFF} when no key is
down; then the step byte and 0. The release (matrix idle) follows after the hold time.

Touch: a tap is one report {ABS_MT_TRACKING_ID 0, ABS_MT_POSITION_X/Y, ABS_MT_TOUCH_MAJOR/WIDTH_MAJOR 20,
SYN_MT_REPORT, BTN_TOUCH 1, ABS_X/Y, SYN_REPORT} and, after the hold time, {BTN_TOUCH 0, SYN_MT_REPORT,
SYN_REPORT} (**UNVERIFIED** against the unit's touch driver).

### 3.5 Display path

The LCD image is what UI VDMA MM2S scans out (frame address 0x8000_005C, HSIZE 0x8000_0054, stride
0x8000_0058 bits 15:0, VSIZE 0x8000_0050): ARGB8888 (after `insmod_after_app.sh`, fb0 at 0x1FC0_0000; before,
0x1F40_0000). The waveform plane is waveform VDMA's frame (0x1E40_0000, 2000-byte lines, 480 lines). The PL compositor
is not modelled (the `shotcomp` capture blends fb0 over the plane at a given position with fb0's alpha byte as
straight alpha, out = W·(1 − a) + U·a, plane at (19, 50) by default; **UNVERIFIED**). The original firmware's
fb0 frames use straight (non-premultiplied) alpha: A = 0 and RGB 0 over the visible graticule, A = 255 elsewhere. `shotfb` captures fb0/fb1 as RGBA with the exact bytes
(alpha kept) and reports an alpha histogram; `shotraw` dumps any DDR range.
Waveform rendering is approximated (every pixel-level detail **UNVERIFIED**): after each completed frame (§5.1),
at most every 200 ms, while waveform VDMA MM2S runs, the plane is cleared and every channel with acq 0x0B1 bit c and map
0x106+c ≠ 0 is drawn unless map 0x0FF bit 0 = 1: screen sample count S = 10 · map 0x19B · max(1, map 0x1A7)
(spec 07 §7.8 inverted), trigger at column 500, row = (3968 − code)/8, per column min…max joined to the previous
column when map 0x125 bit 0 = 1, colour = the UI channel colour as RGB565. While UI VDMA S2MM
runs and 0x8004_06C0 = 1, every 100 ms the model writes the menu layer as RGB565 (**UNVERIFIED** format) to the
S2MM frame (0x8000_00AC, default fb1 0x1F80_0000, stride 0x8000_00A8) and raises `siglent_vnc`, which is what
`vncserver` consumes.

### 3.6 Factory data (p9 seed)

The recovery image has no p9 content. Each run's `firmdata0` is seeded (unless disabled) with synthetic data —
**not** the data of a real unit:
- `sys_cfg.cfg`, `sys_cfg_4ch.cfg`, `sys_cfg_2ch.cfg`: the 0xCF8-byte identity record the application reads at start.
  Encoding and fields: spec 09 §1.2. The seed fills every model-name slot with "SDS824X HD" (2-ch file:
  "SDS822X HD"); the generator is the test workspace's `mksyscfg.py`.
- `NSP_system_info.xml`: `device/system_information/serial_number/chip` = placeholder serial.

## 4. Status policy ("healthy idle board")

(acq 0x19 bits 3 and 9, 0x21 bit 0 and 0x3E bit 0 follow the frame state machine of §5.1 instead, unless simd
runs with `-acq-static`.)

Status fields software polls read as (register file | set) & ~clear:

| Plane idx | Set | Clear | Meaning (poller) |
|---|---|---|---|
| acq 0x19 | bits 0, 3, 9 | — | calibration value ready, stopped, record done/triggered |
| acq 0x1F | bits 8, 12, 13 | — | ADC PLL locked, xPLL locked |
| acq 0x21 | bit 0 | bit 8 | record memory ready; no DDR3 compare error |
| acq 0x26 | bit 0 | — | segment count ≥ 1: a completed record |
| acq 0x2F | bit 12 | — | sequence ready |
| acq 0x3E | bit 0 | — | valid parameters / record complete |
| acq 0x31, 0x6E, 0x116, 0x1B1, 0xA4 | bit 0 | — | expand done, counter gate done, draw stopped, data valid, cal-invalid flag (0x6E after the first counter restart: §5.5; 0x1B1 after the first mean start: §5.4) |
| acq 0x7C | bits 0, 1 | — | ADC data PLL ready |
| acq 0x7D | — | bits 4, 5 | no ADC I-channel error |
| acq 0xFD | — | bit 7 | map ready |
| acq 0x181 | bit 0 | — | device-DNA read done; 0x182–0x185 = configured 64-bit scope id (bits 15:0 … 63:48) |
| map 0x1A, 0xB8, 0x187 | bit 0 | — | distance data valid, search done, wave move done |
| map 0x82 | bit 0 | — | last-frame flag |
| map 0x11E | bit 4 | — | ping/echo clock locked |
| map 0x188 | — | bits 3:0, 8 | resize end, clear-preparing done |

**UNVERIFIED**: these are the values the pollers wait for, not measured behaviour (timing, transient and error
states are not modelled). FPGA version (acq 0x000–0x002) reads 0. acq 0x151 and 0x059 read 0 but are not
accessed by the original application on this product; the channel count comes from the hw-id GPIO byte (GPIO 334–341, §3.2;
simd `-hwid`).

## 5. Acquisition model

### 5.1 Frame state machine

An arm strobe (acq 0x003 written with bit 4 set) enters ARMED: acq 0x19 bits 3 and 9, 0x3E bit 0 and 0x21 bit 0 read
0. The frame is DONE (all four read 1) `-trig-ms` after the arm (default 20 ms: an auto trigger on every arm; `-1`
= never, only a stop request ends it) or at a stop request (acq 0x19 written with bit 3 set). acq 0x19 bit 0 stays
1. Acq 0x0FD bit 7 (map engine busy) reads 1 for `-map-busy-ms` (default 2 ms; 0 = never) after each frame
completion, 0 otherwise (the busy duration is **UNVERIFIED**). Pre-/post-trigger phases, holdoff and trigger
conditions are not modelled.

Read-data request (spec 03 §3.6, the PS "loop read" of the YT run mode): MAP 0x148 = 0 requests frame data
(also the power-up state), 1 stops the request. While the request is active and the acquisition runs (armed
since the last stop request), the model completes a frame every `-trig-ms` (continuous acquisition, A 0x00E
bit 12) and raises `/dev/siglent_dma` for each one (POLLIN | POLLRDNORM until `ioctl(fd, 1)` clears it); a
consumer that waits for one interrupt per request gets the next frame. `-read-irq=false` disables it.

### 5.2 Analog front end

Each channel's input is a configured signal (`-signals`, kind dc/sine/square/triangle, frequency, Vpp, mean;
default C1 1 kHz square 0 … +3 V, C2 1 MHz sine 1 Vpp, C3 10 kHz triangle 0.2 Vpp, C4 0 V; t = 0 at the trigger
point, rising edge / upward zero crossing). The sample code is computed from the state the software programmed,
spec 06 with its default tables run backwards:

- group X1/X10/X100 from the channel's relay control byte bits 2:1 (last 6-byte hc595 frame; before the first
  frame the initial image 0x7E), DC bit 3, CAL_N bit 4 (0 → input 0 V); DAC attenuator from the channel's nibble
  (0x8 A1X, 0x0 A0.5X, 0x1 A0.25X, 0x2 A0.1X; 0xF → A1X);
- VGA step s from the last word written to vga(c+1) (power-up 0x040A); ADC full-scale code fsr from ADC12D1000
  reg 0xB (C1/C3) / 0x3 (C2/C4) (power-up 0x4000) or ADC12DL1500 regs 0x033/0x032 (C1/C3) / 0x031/0x030 (power-up
  0xA000), part per hw-id bit 4;
- V/div = G[s]/1000 · (1, 10, 100) · F(fsr), F = 0.75 + 0.0125 · fsr/819 (ADC12D1000) or 0.625 + 0.0625 ·
  (fsr − 0x2000)/5734 (ADC12DL1500); halved when acq 0x19B has bit c set (or bits 3:0 all set);
- V_off = (0x8012 − DAC code)/S[att][group], DAC code from acq 0x146/0x148/0x149/0x147 (C1…C4);
- code = clamp(round(2048 + (V_in + V_off) / V/div · 480), 0, 4095); AC coupling removes the signal mean;
  GND substitution (acq 0x055 bit 4c) replaces the code with the channel's constant (acq 0x053/0x056/0x05A/0x05F).

A replacement that programs the front end per spec 06 therefore reads back codes whose volts per spec 06 §7.1
equal the configured signal (within one code and the DAC quantisation).

### 5.3 Record readout

The record readout path (spec 03 §4.4): software stops/resets AXI DMA S2MM
(CR 0x8001_0030 bit 2), writes DA (0x8001_0048) = 0x1E80_0000, CR |= 0x5001, LENGTH (0x8001_0058) = n, acq
0xCF/0xD0 = n/8, pulses acq 0xC8 0→1, then waits on `/dev/dmadata_irq` and copies from udmabuf0. The model:

- at the acq 0x0C2 rising edge (record read start) latches channel = acq 0x0C1 bits 2:0, the offset O = acq
  0x0C3 | (0x0C4 & 0x7F) << 16 (Δ-sample blocks) and the channel's time axis (below), and resets the block cursor
  (a readout without a 0x0C2 start, e.g. the roll stream, uses O = 0);
- on each 0xC8 rising edge with RS = 1 and LENGTH ≠ 0 writes LENGTH bytes at DA: one channel, 16-bit
  little-endian words; word i, counted from the 0x0C2 start across blocks, is record sample r = Δ·O + c·i; sets
  S2MM SR Idle | IOC_Irq (bits 1, 12; W1C bits 12–14; SR.halted = !CR.RS) and raises `dmadata_irq`;
- record sample r is the §5.2 code of the channel's signal at t = (r − r_trig)/R (signal t = 0 is the trigger
  edge), spec 03 §4.3 "Time axis": R = 2 GSa/s / DS_DIVISOR (acq 0x04B | 0x04C << 16; 500 MSa/s if 0);
  Δ = 4·(0x10E − 0x092) (spec 04 §8.2 step 4; if ≤ 0 the spec 04 §8.1 value from sw = 0x022 bits 9:8 and R);
  r_trig = u·(PRE − DELAY) − round(F) − 34 + 6 with u = Δ/20, PRE = 0x006 | 0x028 << 16, DELAY = 0x007 | 0x025
  << 16, F of spec 04 §9 (decimating = 0x004 bits 5:0 ≠ 0), 6 = the fine term while the model reads 0 at
  0x0A6/0x0AA/0x040/0x041; the fine registers 0x0A7–0x0A9 are not applied (error < u samples, 0 for the
  timebases tested); c = 2·0x0D5 if 0 < 0x0D5 < 0x1000, else 1;
- these timebase registers are taken per configuration bank (acq 0x110, spec 02 §4.3): for channel ch the most
  recent value written in bank ch, in bank 0 or outside a bank window (so per-channel skew values apply to their
  channel; the common values are normally written in bank 0 only);
- word = code << 4 (left-aligned, centre 0x8000, consistent with the original firmware's SCPI waveform data) or, with
  `-sample-align right`, the code in bits 11:0 (spec 03 §5 leaves the alignment **UNVERIFIED**); channel 4
  (digital) reads zeros;
- the readout length acq 0x0C5/0x0C6 is logged next to LENGTH (0x0C5 is in 4-sample units and only bounds the
  transfer, spec 03 §4.3).

Roll stream (spec 03 §6.4): a 0xC8 transfer after a DMA buffer init (acq 0x0C9 0→1) without a following record
read start (0x0C2) delivers the roll stream instead of record words: blocks of stride B cycling over the
channels set in acq 0x0B1 bits 3:0 (CH1 if none), geometry P = 4·0x092 (the roll Lr), c = 2·0x0D5 or 1,
L = 2·⌊P/c⌋ (×4 if c = 1 and P mod 4 ≠ 0), B and group as §6.4. Compression off: 8-byte header (byte 0 =
channel) + L bytes of words + padding; compression on: a 64-bit channel marker (value = channel) + L/8 words
of four 16-bit samples (never < 4). Sample j of a channel's stream is its input at t = j·c/R, continuing across
reads. The fill level registers are not modelled (ACQ 0x0CB reads 0, MAP 0x047 0x480 = 9216 bytes available on
every poll), so the stream runs as fast as the reader polls, not in real time. Logged as `dma` events with
`kind: roll` and the geometry.

Trigger-interpolation value: acq 0x0A6 and 0x0AA read 0 unless `-trig-fine A6,AA` sets them (raw register
values); the data time axis above is not shifted by them (the composition of the fine term from these
registers is not known, spec 03 Open), so they serve to test a replacement's read and composition path and to
measure the original firmware's response (its readout window, spec 03 §4.3, moves with t).

Segments (spec 03 §6.2/§6.3): a segment read start (acq 0x018 bit 13 rising with bit 12 set) latches the index
k = 0x04D | (0x05B bit 8) << 16; a record readout started (0x0C2) while 0x018 bit 12 is set delivers segment k, which
is the channel's signal k/(8·f) later (k/8 of a period; model choice, so that segments differ). The same start
latches the segment timestamp: 0x0E8[15:0] << 32 | 0x01D << 16 | 0x01C = µs from the last 0x0EA pulse (bit 0 rising)
to the (k + 1)-th `-trig-ms` tick after the last arm. History player: 0x0F9 | (0x0FA bit 0) << 16 = segment shown;
while playing (0x0F8 bit 8 = 0) s + ⌊t/T⌋ towards e and held at e (s = 0x0F5 | 0x0F7 bit 0 << 16, e = 0x0F6 |
0x0F7 bit 8 << 16, T = 0x0F4 | 0x10F << 16 µs, t since play started or the window was written; backwards when
0x0F8 bit 0 = 0 and e < s); paused: the value at the pause, or s once the window is rewritten.

Peak detect: when the channel's 0x004 has bit 8 set and bits 5:0 ≠ 0 (PEAK_EN at a decimated rate, spec 04 §5.2),
words 2j and 2j + 1 are the maximum and the minimum code of the signal over record samples [2j, 2j + 2) (16
instants). The word count is unchanged (spec 03 §4.3); the pair order is a model choice (spec 03 §5 leaves it
open).

No record memory, interleave or trigger jitter are modelled.

### 5.4 Mean engine

The self-calibration mean engine of spec 10 §5.2 (acq 0x1AE–0x1B6) computes from the §5.2 model: at a 0x1AE
rising edge the model takes, for every channel, the mean code m of its signal through the front end state at
that moment, averaged over 4096 instants of a window of 0x1AF ms (at least one signal period; AC coupling removes
the mean), and stores count = 0x1AF × 1000 and sum = round(m × count). 0x1B1 bit 0 (valid) reads 0 for 0x1AF ×
100 µs after the start, then 1; bits 12–15 (overflow) read 0 — a mean of clipped samples (0 or 4095) is a valid
result, and self-calibration relies on it. Reads of 0x1B2/0x1B3/0x1B4 (sum bits 15:0/31:16/47:32) and 0x1B5/0x1B6
(count) return the channel selected in 0x1B0 bits 1:0 at the time of the read. Before the first start 0x1B1 bit 0
reads 1 and the others read back. Engine sample rate and duration are model values (**UNVERIFIED**). Self-cal
runs need open inputs, i.e. `-signals C1=dc,0,0,0;C2=dc,0,0,0;C3=dc,0,0,0;C4=dc,0,0,0`; with an error-free model
every centre comes out at the 0 V DAC code 0x8012 and the full-scale ratios follow the §5.2 F law. With the original firmware,
`:SYSTem:SELFCal` (4 channels) completes in ≈ 236 s and writes `usr/usr/acq_self_cal_usr.txt`
(`sim/test/selfcal.sh`).

### 5.5 Frequency counter and totalizer

Spec 15 §4, from the signal of the counter source s = acq 0x00E bits 6:4 (0…3 = C1…C4): its frequency f if the
signal is periodic (kind ≠ dc, Vpp ≠ 0), the BNC is connected (CAL_N = 1) and no GND substitution is active; else f
= 0 (other sources: 0). The level and the comparator are not modelled. A 0x066 rising edge restarts both gates with
n = 0x067 and N = max(0x063, 1) as written at that moment:

| Register | Value |
|---|---|
| 0x06E bit 0 | 1 once (n − 1) ms have passed since the restart (auto gate done; 1 before the first restart) |
| 0x06E bit 4 | 1 once N/f s have passed (period gate done; never if f = 0) |
| 0x068 / 0x069 | ⌊f·(n − 1)/1000⌋ bits 15:0 / 31:16 after the auto gate, else 0 |
| 0x06A / 0x06B | round(N·10⁹/f) (1 ns units, saturated at 2³² − 1) after the period gate, else 0 |
| 0x11B–0x11E[7:0] | totalizer ⌊f_c·(t − t₀)⌋, 56 bits; f_c = frequency of the 0x111 bits 1:0 source, t₀ = the last 0x111 bit 12 rising edge (else the first 0x111 write); while 0x111 bit 8 is set, the value latched at its rising edge |

The counts use the frequency at read time (a signal change is not integrated). Gate bits 3–7 and shield bit 13 of
0x111 are ignored. The original firmware's FREQuency/PERiod/TOTalizer replies equal the configured signal
(`sim/test/counter.sh`).

### 5.6 Runtime stimulus

Control commands (scenario lines or `simctl`, logged): `temp C [CH]` sets TMP422 channel CH (1 local, 2 remote 1 =
ADC 0, 3 remote 2 = ADC 1; none = all, remotes C + 1 / C + 2 as at start) in the I2C registers (high byte °C,
low byte bits 7:4 = 1/16 °C) and the hwmon `temp<CH>_input` nodes (millidegrees), for quick-cal tests (spec 10 §6.1);
`signals SPEC` changes the §5.2 input signals (same syntax as `-signals`).

## 6. Running the original firmware

`sim/run/tier1.sh -n NAME -t SECS [-p] -- CMD`: `/sim/guest/m1-pll-pl.sh` (the unit's clock-chip tool `easyzup` and
`reload_pl.sh`), `/sim/guest/app-only.sh` (`easyzup`, `reload_pl.sh`, `insmod_before_app.sh`, rpcbind,
`insmod_after_app.sh` in the background, `main.app` in the foreground), `/sim/guest/boot-product.sh` (the unit's
`rcS`). A portmapper must run before the application: without it the VXI-11 service registration fails and the
application exits. `-p` publishes TCP 5024, 5025, 111 (as 1111), 9009 and 80 (as 8080) on host 127.0.0.1
(pasta; the host is not reachable from the container); VXI-11 core/abort ports are ephemeral and not
published. Runs are limited to 300 s and must hold `analysis/.sim.lock`. The UI is up 60–90 s after start. The
published ports belong to whichever run holds the lock: a host-side client must wait until its own run holds the
lock (e.g. until `runs/NAME/log.jsonl` exists) and should check `*IDN?` before sending; the `scpi` scenario command
is sent inside the lock and is safe.

Behaviour of the original firmware to expect in scenarios (not model artefacts): `:WAVeform:DATA?` while the
acquisition runs crashes the application about 10 s later (send `:TRIGger:STOP` first); vertical settings of a channel that is off are ignored (the default
user setup has C1 off: `:CHANnel1:SWITch ON` first) and settings apply asynchronously (query them ≥ 1 s later,
not in the same message); front-panel keys sent before the UI is up are consumed without effect.

Further behaviour of the original firmware seen in scenarios:
- In Auto the PS reads frames only while a measurement, math or other analysis needs data (the read-data request
  of §5.1, then a wait on `siglent_dma`); in Normal it re-arms every frame. The model's power-up value MAP 0x148 = 0
  (request active) is required.
- After `:ACQuire:MODE XY`, `:ACQuire:MODE YT` returns to the previous mode (`:ACQ:MODE?` = ROLL if roll was on
  before XY), not to YT.

## 7. Validating a replacement firmware

1. Build it for aarch64 Linux (glibc ≤ 2.32 compatible, or static; Go binaries need no special settings, §2).
2. Run it in **replacement mode**: `sim/run/tier1.sh -n NAME -t SECS [-p] -R PROG [-H HOOK] -- ARGS`. The host
   file PROG (plus `FWDIR` helper files) is placed read-only at `/sim/fw`; `/sim/guest/replace.sh` runs the unit's
   bring-up scripts up to HOOK, logs the mark `app start`, then execs PROG in place of main.app. Hooks: `none` (PROG does
   everything: PLL, bitstream, module loads), `pll` (the unit's clock-chip tool `easyzup`), `pl` (+ `reload_pl.sh`),
   `modules` (default; + `insmod_before_app.sh`, the portmapper and `insmod_after_app.sh` in the background —
   the environment the original application runs in). Rootfs, app partition (the unit's scripts, modules,
   bitstream), factory seed, device model, logging, control socket and scenarios are identical to a run of the
   original firmware.
   `FIRMWARE=DIR` instead replaces the whole app partition (the replacement then brings its own boot scripts).
3. Run the same scenario (keys, touches, SCPI lines) against both: `sim/run/compare.sh -n NAME [-S SCENARIO]
   [-H HOOK] [-p] PROG ARGS` runs `NAME-vendor` (the original firmware, app-only.sh) and `NAME-repl` and writes the trace diff.
4. Compare with `simdiff A@mark=app\ start B@mark=app\ start`: last value per register, registers written by one
   side only, per-block write order (LCS diff, duplicate consecutive writes collapsed), SPI frames per chip,
   device nodes / ioctls / sysfs writes / modules / bitstream, IRQ and DMA counts; plus `simlog -actions` per
   scenario step and the LCD PNGs. Ordering differences are acceptable only where the owning spec says the order
   is free. For a settings sweep, `simdiff -states "scpi " A B` splits both runs at every SCPI mark and compares
   per state the last value of every ACQ/MAP/AXI-DMA register written in it and the distinct SPI frames per chip.
   All sections ignore the frequency-counter task's registers (ACQ 0x063, 0x064, 0x066, 0x067: written by a
   periodic task at non-deterministic times) unless `-x none`; `-x` takes further registers (A<idx>, M<idx>).
5. Self-calibration: run both with open inputs (§5.4) and compare `runs/*/usr/usr/acq_self_cal_usr.txt` with
   `sim/test/calfile_cmp.py` (per key family, relative difference; the original firmware's file is reproducible
   byte for byte between runs). A run is limited to 300 s, so a replacement's self-cal must finish within ≈ 290 s
   of its start (the original firmware's: ≈ 236 s).
6. Anything the model does not check (timing, analog behaviour beyond §5.2, PL rendering) needs a real unit.

## Open

- LCD composition (mixer equation, placement of the waveform layer) is not modelled; the waveform-plane pixel
  format, the render model of §3.5 and the RGB565 VNC format are **UNVERIFIED**.
- Chip read-back beyond the ADC12DL1500 register file (§3.2): PLL lock/status over SPI is not modelled.
- Trigger behaviour (conditions, holdoff, auto timeout, pre-/post-trigger phases) and transient or error states are
  static or absent; the frame time is a fixed `-trig-ms`; the map-busy duration (§5.1) is a fixed `-map-busy-ms`.
- Analog model: default tables only (a calibrated unit differs), no bandwidth, noise, overload or probe; the
  sample-word alignment and the PL side of the readout units (spec 03 §4.3/§5) are **UNVERIFIED**. The time axis
  follows spec 03 §4.3 and the volts axis spec 06: the original firmware's `:WAV:DATA?` puts the edges of the test signals exactly
  where the trigger relation predicts (2 GSa/s, 100 MSa/s with and without delay, 5 MSa/s decimated;
  `sim/test/taxis.sh`). Not modelled: I > 1 interpolation (the relation is used unchanged), the fine registers,
  the compression pick rule; peak-detect pairs (§5.3) are a model choice (order, interval) not checked against the
  original firmware. The roll stream (§5.3) follows the framing of spec 03 §6.4 (the
  original firmware accepts it: `sim/test/roll.sh`) but its fill rate is not real time and the meaning of the four
  samples of a compressed word is **UNVERIFIED**.
- Segments / history: outside sequence mode the segment count (acq 0x026) reads 1 (history frames of normal
  acquisition are not counted); segment contents, timestamps and the history player (§5.3) are model choices not
  yet run against the original firmware.
- Mean engine and counter (§5.4, §5.5): no noise, no overflow, no comparator level or hysteresis; the engine's
  sample rate and the gate durations are model values.
- Factory record field semantics (language encoding, options, bandwidth type selection): synthetic seed only;
  the language bytes did not change the UI language (default user settings give the Chinese UI).
- The recovery image lacks p9; whether a real unit's p9 has other files the application requires (calibration,
  `NSP_sn_bandwidth.xml`) is unknown — the app recreates defaults.
- Touch coordinate mapping to UI controls was not confirmed (a tap produced the buzzer click only).
- `epoll` on emulated fds is refused (§2); a replacement that needs epoll on IRQ nodes cannot be run unmodified.
