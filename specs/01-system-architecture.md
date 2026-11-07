# 01 — System Architecture

This spec defines the platform, the boot chain from BootROM to the launch hook, the eMMC layout, the
boot state, the running processes, every device node and kernel module the stack uses, the read-only policy,
network/time/login services, the update and recovery paths, and how a replacement firmware installs itself and
takes over. Ordering rules marked **must** are load-bearing: the clock chip (spec 13 §4), the PL bitstream
(spec 13 §3) and the display VDMAs (spec 13 §5) only work when brought up in the order of §5.

---

## 1. Platform

| Item | Value |
|---|---|
| SoC | Xilinx Zynq UltraScale+ MPSoC, 2× Cortex-A53 used by Linux (DT `cpu@0`, `cpu@1`), Mali-400 disabled. Part XCZU2CG-2SFVC784 per spec 12 §1 (ZU2 rather than ZU3 is **UNVERIFIED**; spec 12 §1 gives the settling CSU IDCODE read); bitstream configuration-port IDCODE 0x04A43093 (spec 13 §2) |
| DRAM | Linux memory node 0x0000_0000–0x1F7F_FFFF (504 MiB). 0x1F80_0000–0x1FFF_FFFF holds the two framebuffers outside Linux memory (§4). Physical DRAM: expected 1 GiB (DDR4, 32-bit; spec 12); Linux uses only the first 504 MiB (**UNVERIFIED**). The PL has no memory interface of its own: the acquisition record memory is in this PS DDR4, reached by the PL through the S_AXI_HP ports (spec 12 §1, §4); its address range is **UNVERIFIED** (most likely DRAM above 0x2000_0000, outside the Linux memory node; §13) |
| Boot medium | eMMC on SD0 (0xFF160000), 8-bit bus (§3) |
| OS | Linux 5.10.0 (Xilinx tree, SMP, PREEMPT_NONE, HZ 250), Buildroot 2021.02.6 rootfs, BusyBox 1.33.1 init |
| Console | UART0 0xFF000000 = `ttyPS0`, 115200 8N1 |
| Network | GEM0 0xFF0B0000 via EMIO, PHY at MDIO address 0x1E. DT `phy-mode = "gmii"`, but the PL wires only the 4-bit MII signal set: 10/100 Mb/s only (spec 12 §4.2; U-Boot forces 100 Mb/s, §2.2) |
| USB | USB0 (0xFE200000) host, high-speed (front USB-A); USB1 (0xFE300000) peripheral (USBTMC device port) |
| I2C | Linux `i2c-0` = PS I2C1 0xFF030000: Goodix GT9xx touch 0x14, TMP422 0x4C, `i2cdev` 0x74 |
| SPI | `spidev1.0`…`spidev1.7` = PS SPI1 0xFF050000 with GPIO chip selects: 0 clock chip, 1 ADC0, 2 ADC1, 3 relay HC595, 4–7 VGA1–4; 20 MHz max. `spidev0.0` = PS SPI0 0xFF040000 CS0 (DT name "kb595") exists but SPI0 is **not connected** on this board (no MIO pins, EMIO pins unused in the PL; spec 12 §4.2): nothing needs to use it; the key LEDs are PL registers (spec 08 §6) |
| RTC | none (PS RTC node disabled); time from NTP or manual setting (§9) |
| Watchdog | none active (§10) |
| Product identity | The hw-id byte from sysfs GPIOs 334–341 (sysfs numbers of the shipped kernel and DT; the MIO 0–7 mapping is **UNVERIFIED**, spec 12 §4.2) gives: clock-chip board variant ((byte & 0x30) == 0x30, spec 13 §4.1), channel count (bit 7 = 1 → 2-channel model, 0 → 4-channel), PCB version (bits 3:0) and BOM version (bits 7:4) (spec 02 §3) |

## 2. Boot chain

| Stage | Source | Runs at | Role |
|---|---|---|---|
| BootROM | on-chip | — | reads BOOT.bin from eMMC p1 (FAT), loads PMU firmware and FSBL |
| PMU firmware | BOOT.bin partition 0 (first 0x1FAE0 bytes) | PMU | Xilinx 2021.1; services EEMI calls incl. PL configuration from Linux |
| FSBL | BOOT.bin partition 0 (next 0x171F0 bytes) | OCM 0xFFFC0000, A53-0 EL3 | DDR/PS init, loads the PL bitstream (partition 1), BL31, U-Boot |
| PL bitstream | BOOT.bin partition 1 | PL | boot-time bitstream; drives LCD/VDMA so U-Boot can show the logo. Replaced by Linux (spec 13 §3) |
| BL31 (ATF) | BOOT.bin partition 2 | OCM 0xFFFEA000, EL3, TrustZone | Xilinx ATF v2.4 (2021.1), PSCI |
| U-Boot | BOOT.bin partition 3 | DDR 0x0800_0000, EL2 | U-Boot 2021.01; logo, update/recovery, loads Linux (§2.2) |
| Linux | eMMC p5 (FAT) `Image`, `system.dtb` | DDR 0x0300_0000 / DTB 0x02A0_0000 | `booti 0x3000000 - 0x2A00000` (no initrd) |

### 2.1 BOOT.bin format

Standard Xilinx ZynqMP boot image, unencrypted and unauthenticated, with four partitions: PMU firmware and FSBL
(one image, executed on A53-0 at 0xFFFC0000), the boot-time PL bitstream, BL31 (0xFFFEA000, EL3) and U-Boot
(0x08000000, EL2).

A replacement BOOT.bin is not required: a replacement firmware keeps the vendor boot chain and kernel (§12).

### 2.2 U-Boot behaviour

U-Boot has no interactive window (boot delay 0). On every eMMC boot it does, in order:

1. **USB recovery probe**: for USB devices 0–3, FAT partitions 1 and 2, it looks for `huluwa_udiskEnv.txt`; on the
   first one found it imports the file as a text environment (`env import -t`) and runs its `uenvcmd` (§11.3,
   §12.4). A `uenvcmd` that ends in `booti` never returns; one that returns lets the boot continue with step 2.
2. Drives U-Boot GPIO 9 low (function **UNVERIFIED**) and sets the Ethernet PHY (MDIO address 0x1E) BMCR to 0xB100
   (reset, 100 Mb/s, auto-negotiation enable, full duplex).
3. If `uEnv.txt` exists on p1, imports it, appends ` uboot_version=4` to `bootargs` and runs its `uenvcmd`.
4. Only if that returns: loads `system.dtb` and `Image` from the boot partition and boots them.

Saved environment: before the boot command runs, U-Boot loads a saved environment from FAT on the eMMC (on this
layout p1), file `uboot.env` (U-Boot binary environment: CRC32 + data; size and redundancy **UNVERIFIED**). A unit
as shipped has no such file and uses the built-in environment described here. A valid `uboot.env` would replace
the built-in environment (including the boot command and boot delay) on every boot. `saveenv` writes that file:
a replacement or installer must not run `saveenv` and must not create `uboot.env` unless it intends to change
the boot flow permanently; deleting `uboot.env` from p1 restores the built-in defaults.

U-Boot reads `logo.bmp` and `update_logo.bmp` from p8 (FAT), decodes them into the logo buffer (logo at
0x1F400000, update picture at 0x1F000000; 1024×600), programs the UI VDMA, turns the LCD on and beeps once for
100 ms (sequence: spec 13 §5.6). The environment commands `show_logo` and `show_update` switch the displayed
picture by writing the UI VDMA MM2S start address (0x8000005C ← 0x1F400000 or 0x1F000000) and then VSIZE
(0x80000050 ← 600); a recovery or installer environment may `run` them.

The p1 `uEnv.txt` as shipped makes every boot an update check. If p12 `/updatefile/bsp_update` exists, the update
picture is shown. Each of the following files present in p12 `/updatefile/` is then written to its destination:

| File | Destination |
|---|---|
| `BOOT.bin`, `uEnv.txt` | p1 (FAT) |
| `Image`, `system.dtb` | p5 (FAT) |
| `logo.bmp`, `update_logo.bmp` | p8 (FAT) |
| `rootfs.ext4.gz` | p7, raw (gzip-decompressed) |
| `siglent.ext4.gz` | p10, raw |
| `factorydata.ext4.gz` | p11, raw |

Then the kernel `Image` and `system.dtb` are loaded from p5 and booted without initrd, with
`bootargs` = `console=ttyPS0,115200 root=/dev/mmcblk0p7 rw earlycon rootfstype=ext4 rootwait cpuidle.off=1`.
Linux empties `/updatefile/` on every boot (§5), so a file placed there is flashed exactly once. A replacement
must never place files in p12 `/updatefile/` unless it intends that flash.

### 2.3 Kernel command line

Effective `/proc/cmdline` on a normal boot (U-Boot replaces the DT `chosen/bootargs`):
`console=ttyPS0,115200 root=/dev/mmcblk0p7 rw earlycon rootfstype=ext4 rootwait cpuidle.off=1 uboot_version=4`.
On a USB-recovery boot the last token is `uboot_version=reboot`. The DT's `clk_ignore_unused` (in
`chosen/bootargs`) is therefore not in effect (**UNVERIFIED**).

## 3. eMMC layout

MBR with an extended partition. Each logical partition N (N ≥ 5) is preceded by an 8 KiB gap that holds its
EBR, and the partitions are whole MiB long, so `start(pN) mod 1 MiB = (N − 3) × 8 KiB` (p5 0x4000, p7 0x8000,
p9 0xC000, p10 0xE000, p11 0x10000; the EBR sits in the gap, e.g. at start − 512). Positions are byte offsets on
`mmcblk0`. The ext4 file systems on p7 and p10 have the `metadata_csum` feature (ro_compat 0x46B); p9, p11 and p12
are assumed to have it as well (**UNVERIFIED**, check with `dumpe2fs -h`). Consequence: U-Boot can read them (`ext4load`) but refuses to write them (§12.4).
Anyone re-creating an ext4 partition for this kernel (Linux 5.10) must use only features it supports: in
particular no `orphan_file` (default in e2fsprogs ≥ 1.47; a 5.10 kernel refuses to mount such a file system
read-write) — e.g. `mkfs.ext4 -O metadata_csum,^orphan_file,^metadata_csum_seed`. The block size of p12 as shipped
is **UNVERIFIED**.

| Part | FS | Mounted at | Mode | Contents | Start / size |
|---|---|---|---|---|---|
| p1 | FAT | not mounted | — | `BOOT.bin`, `uEnv.txt` | **UNVERIFIED** |
| p2, p3 | — | unused by any script | — | — | **UNVERIFIED** |
| p4 | (extended container) | — | — | — | **UNVERIFIED** |
| p5 | FAT | not mounted | — | `Image`, `system.dtb` | starts at 1 MiB-multiple + 0x4000 (**UNVERIFIED**) |
| p6 | — | unused | — | — | **UNVERIFIED** |
| p7 | ext4 (1 KiB blocks, image 200 MiB) | `/` (kernel `root=`) | rw at boot, ro once the boot has run (§5) | Buildroot rootfs | 0x0C808000; ≥ 200 MiB |
| p8 | FAT | `/mnt/logo` | rw | `logo.bmp`, `update_logo.bmp` | between p7 and p9 |
| p9 | ext4 | `/usr/bin/siglent/firmdata0` | ro (remounted rw only while calibration is written, §8) | per-unit data: calibration, `NSP_system_info.xml` (MAC, serial), options/licences | 0x3F20C000, 50 MiB |
| p10 | ext4 (4 KiB blocks) | `/usr/bin/siglent` | ro | vendor application: `main.app`, `vncserver`, `drivers/` (kernel modules, §7), `config/` (PL image, boot scripts, web) | 0x4240E000, 500 MiB |
| p11 | ext4 | not mounted by scripts | — | `factorydata` (written only by the update path) | 0x61810000; size **UNVERIFIED** |
| p12 | ext4 | `/usr/bin/siglent/usr` | rw | user data: settings (`config/`), `updatefile/`, `usr/upgrade/`, `mass_storage/local` (user files, SMB share), `samba/`, `avahi/`, optional `extra.sh` | **UNVERIFIED** |

Irreplaceable per-unit content: p9 (calibration, serial, MAC, licences) and p11. Back both up (raw `dd` of
`mmcblk0p9`, `mmcblk0p11`) before modifying anything. The USB recovery package does not contain them.

## 4. Physical memory map (DRAM)

| Range | Size | Owner |
|---|---|---|
| 0x0000_0000–0x1F7F_FFFF | 504 MiB | Linux (`memory` node) |
| 0x0200_0000 / 0x02A0_0000 / 0x0300_0000 | — | U-Boot staging: initrd / DTB / kernel load addresses (boot time only) |
| 0x0800_0000 | — | U-Boot (boot time only) |
| 0x1E40_0000–0x1E7F_FFFF | 4 MiB | reserved CMA pool `wavedata` (reusable): waveform-layer VDMA frame and `siglent_dma` buffer (spec 14 §2) |
| 0x1E80_0000–0x1EFF_FFFF | 8 MiB | reserved CMA pool `rawdata` (reusable): `/dev/udmabuf0` (spec 14 §7) |
| 0x1F00_0000–0x1F7F_FFFF | 8 MiB | reserved CMA pool `logo` (reusable): U-Boot update picture 0x1F000000, logo 0x1F400000 |
| 0x1F80_0000–0x1FBF_FFFF | 4 MiB | `siglentfb1` fb memory (outside Linux): VNC capture frame, 1024×600 RGB565 written by the UI VDMA S2MM (the depth the fb1 driver reports is UNVERIFIED) |
| 0x1FC0_0000–0x1FFF_FFFF | 4 MiB | `siglentfb` fb memory (outside Linux): `/dev/fb0`, the UI frame scanned out by the UI VDMA MM2S |
| 0xFFFC_0000, 0xFFFE_A000 | OCM | FSBL (boot only), BL31 (resident) |

PL AXI register space (HPM0, 0x8000_0000–) is listed in spec 02; the blocks this spec touches are in spec 13 §5
and spec 14.

## 5. Init sequence

BusyBox init runs the usual early mounts (`/proc`, devpts, tmpfs on `/dev/shm`, `/tmp`, `/run`, sysfs), then
`/etc/init.d/rcS`. `rcS` and everything it runs, including the S-scripts, is an inittab `sysinit` action: init
starts the serial getty (`ttyPS0`) only after `rcS` has returned. Shutdown runs the S-scripts with `stop` in reverse
order and unmounts read-only.

The boot does the following, in this order. Only the outcome matters to a replacement; the launch hook (§12) runs
after all of it.

1. Mounts p10 ro at `/usr/bin/siglent`, p9 ro at `/usr/bin/siglent/firmdata0`, p12 rw at `/usr/bin/siglent/usr`,
   p8 (vfat) at `/mnt/logo`.
2. Empties p12 `updatefile/` (U-Boot has consumed it on this boot, §2.2).
3. **Upgrade hook**: if the directory `/usr/bin/siglent/usr/usr/upgrade/` exists, makes its `update.sh`
   executable, runs it, deletes the directory and forces a reboot (§12.3). `/` is still rw here; the clock chip
   and the PL are not yet initialised.
4. On the first boot after a rootfs flash only: remounts `/` rw for some symlink edits and back to ro.
5. Programs the clock chip (spec 13 §4). **Must** precede step 6.
6. Reloads the PL from Linux and configures the display VDMAs and the LCD (spec 13 §3, §5), using the script
   `config/fpga/reload_pl.sh`, which also loads `xilinx_dma.ko` (§7).
7. Remounts `/` ro (after a symlink edit for the NTP configuration on p12); mounts debugfs on `/sys/kernel/debug`.
8. Starts the Ethernet configuration in the background (§9); brings up `lo`.
9. Loads the kernel modules of §7 with `drivers/insmod_before_app.sh`.
10. Starts `main.app` and `vncserver` in the background, then the web server (lighttpd + php-cgi FastCGI, port 80,
    document root `config/www/`).
11. Starts `drivers/insmod_after_app.sh` in the background: after `sleep 10` it writes UI VDMA MM2S start ←
    0x1FC00000 (`/dev/fb0` memory) and VSIZE ← 600 (spec 13 §5). The takeover must stop it (§12.2 step 1).
12. Runs every `/etc/init.d/S??*` in lexical order with `start`, except `S49ntp`, `S50dropbear`, `S50telnet`
    and `S91smb`.

| Script | Runs | Effect |
|---|---|---|
| S01syslogd, S02klogd | yes | BusyBox syslogd/klogd (RAM log) |
| S02sysctl | yes | applies `/etc/sysctl.d`, `/etc/sysctl.conf` if present |
| S20urandom | yes | seeds RNG; saving the seed fails silently (ro root) |
| S30rpcbind | yes | rpcbind (needed by VXI-11, spec 11) |
| S40network | yes | `ifup -a` (`/etc/network/interfaces`: only `lo`) |
| S49ntp | no | NTP; started on demand (§9) |
| S50dropbear | no | SSH daemon present but never started |
| S50lighttpd | yes | a second lighttpd on port 80; fails to bind (the web server of step 10 already listens) and exits; harmless |
| S50telnet | no | BusyBox telnetd present but not started at boot |
| S70vsftpd | yes | vsftpd, anonymous login enabled |
| S90product-custom | yes | runs `/usr/bin/siglent/product_start.sh` if present (not present as shipped) |
| S91smb | no | Samba; started on demand |
| S99extra | yes | if `/usr/bin/siglent/usr/extra.sh` (p12) exists as a regular file, executes it directly (it must be executable; first line `#!/bin/sh`) and waits for it to exit — the launch hook (§12) |

Trap: on the first boot after a rootfs flash, `/` is already read-only when step 6 runs, so the Linux PL reload
fails for that boot and the boot-time (FSBL) bitstream stays loaded (a functionally equivalent build, configured
before the clock chip, spec 12 §2; ADC capture in that state is **UNVERIFIED**). On every later boot `/` is still
rw at step 6, so the trap affects exactly one boot. A replacement must not depend on writing to `/` in that
window.

## 6. Process model

Processes running when the launch hook starts, and what the takeover does with them (§12.2):

| Process | Started by | Role | Takeover |
|---|---|---|---|
| `main.app` | §5 step 10, root, cwd `/usr/bin/siglent` | the vendor instrument application (UI on `/dev/fb0`, acquisition, front end, SCPI, USBTMC gadget, network services) | stop |
| `vncserver` | §5 step 10 | RFB server on port 5900; holds `/dev/siglent_vnc`, `/dev/uinput` (spec 11, spec 14 §6) | stop |
| `insmod_after_app.sh` (+ child `sleep 10`) | §5 step 11, exits after ~10 s | delayed display switch | stop first |
| `lighttpd` + `php-cgi` | §5 step 10 | vendor web UI | stop if the replacement serves port 80 |
| Ethernet configuration script | §5 step 8, exits | §9 | — |
| `udhcpc`, `ntpd`, `smbd`/`nmbd`, `avahi-daemon`, `telnetd` | the vendor application, at run time | network services | stop as required |
| syslogd, klogd, rpcbind, vsftpd | S-scripts | services | keep `rpcbind` if VXI-11 is served |
| getty | inittab, after `rcS` returns | serial login on `ttyPS0` | keep |

Nothing supervises `main.app`: no respawn entry, no watchdog. Once stopped it stays stopped until reboot.

## 7. Kernel modules and device nodes

Out-of-tree modules live in `/usr/bin/siglent/drivers/` (vermagic `5.10.0+ SMP mod_unload aarch64`). Load order
(**must** be kept if these modules are reused; all after the PL is configured, because probe touches PL
registers or PL interrupts):

| # | Module | Loaded by | Creates | Spec |
|---|---|---|---|---|
| 1 | `xilinx_dma.ko` | `reload_pl.sh`, before the bitstream load | dmaengine channels for the two AXI VDMAs (no char device) | 14 §8 |
| — | (32-bit read of 0x80030580) | `insmod_before_app.sh` | clears a pending siglent_dma interrupt; **must** precede `insmod siglent_dma.ko` (a request pending at load time oopses the kernel) | 14 §2 |
| 2 | `uinput.ko` | `insmod_before_app.sh` | `/dev/uinput` | — |
| 3 | `gt9xx.ko` | ″ | Goodix touch → `/dev/input/eventN` ("goodix-ts"), `/dev/siglent_touch` (misc) | 08 §7 |
| 4 | `siglent_vnc.ko` | ″ | `/dev/siglent_vnc` | 14 §6 |
| 5 | `siglentkb_sds.ko` | ″ | `/dev/siglent_kb` | 14 §5 |
| 6 | `siglent_dma.ko` | ″ | `/dev/siglent_dma` | 14 §2 |
| 7 | `u_dma_buf.ko` | ″ | `/dev/udmabuf0`, `/sys/class/u-dma-buf/udmabuf0/` | 14 §7 |
| 8 | `siglent_dmadata_irq.ko` | ″ | `/dev/dmadata_irq` | 14 §3 |
| 9 | `siglent_cpld_irq.ko` | ″ | `/dev/cpld_irq` | 14 §4 |
| 10 | `f_usbtmc.ko`, then `g_usbtmc.ko idVendor=0xf4ec idProduct=0xee38 …` | the instrument firmware (not the boot) | `/dev/g_usbtmc` (USBTMC gadget on USB1) | 11, 14 §9 |

Built-in drivers that create nodes the stack uses: `/dev/fb0` (`siglentfb`, 0x1FC00000) and `/dev/fb1`
(`siglentfb1`, 0x1F800000) — numbering by DT order, **UNVERIFIED**; `/dev/spidev0.0`, `/dev/spidev1.0`–`1.7`;
`/dev/i2c-0`; `/dev/mem` (unrestricted: no `STRICT_DEVMEM`); `/sys/class/fpga_manager/fpga0/{flags,firmware,state}`;
`/sys/class/gpio`; TMP422 hwmon `/sys/bus/i2c/devices/0-004c/hwmon/hwmon0/temp{1,2,3}_input`; `/dev/usbtmc0…`
(host-side USBTMC for external instruments); `/dev/ttyPS0`.

Device nodes the firmware uses:

| Node | Purpose |
|---|---|
| `/dev/spidev1.0` | clock chip (spec 13 §4); programmed by the boot |
| `/dev/spidev1.1`–`1.7` | ADCs, relays, VGAs (spec 06) |
| `/dev/spidev0.0` | not needed (SPI0 not connected, §1) |
| `/dev/mem` | PL registers: 64 KiB windows at 0x80030000 (acquisition plane), 0x80040000 (display/map plane) and 0x80010000 (AXI DMA) (spec 02); framebuffers; PS TTC0 at 0xFF110000 (beeper, spec 15) |
| `/sys/class/u-dma-buf/udmabuf0/` | cache maintenance of the acquisition buffer (spec 14 §7) |
| `/dev/fb0` | UI framebuffer |
| `/dev/siglent_dma`, `/dev/dmadata_irq`, `/dev/cpld_irq`, `/dev/udmabuf0` | acquisition handshakes and buffers (spec 14, spec 03) |
| `/dev/siglent_kb` | front-panel keys/encoders (spec 08) |
| `/dev/siglent_vnc` | per-frame screen-capture interrupt (spec 14 §6) |
| `/dev/input/eventN` (goodix-ts), `/dev/uinput` | touch; injected input |
| `/dev/siglent_touch` | touch suspend/resume (misc device of `gt9xx.ko`: ioctl 0x900 suspend, 0x901 resume; spec 08 §7) |
| `/dev/i2c-0` | TMP422 / device 0x74 (spec 15) |
| `/dev/g_usbtmc` | USBTMC device endpoint (spec 11) |
| sysfs GPIO 334–341 | inputs: hw-id straps (MIO 0–7 expected, **UNVERIFIED**, spec 12 §4.2): clock-chip variant (spec 13 §4.1), channel count, PCB/BOM version (§1) |
| sysfs GPIO 342 | output: external-calibration switch (MIO 8 expected, spec 12 §4.2; spec 06/10) |
| sysfs GPIO 343 | output, set 1 after the first relay image is prepared: relay shift-register output enable / reset (MIO 9 expected; spec 06 §3.4, spec 12 §4.2). U-Boot's GPIO 9 low at boot (§2.2) is presumably the same line (**UNVERIFIED**) |
| sysfs GPIO 373 | output, set 1 once at start-up right before the ADC phase training (spec 03 §2 step 10a): unexport, export, direction `out`, value 1. Probe/S-bus power (function **UNVERIFIED**); MIO 39 by the numbering of spec 12 §4.2 |
| sysfs GPIO 380 | input, edge `rising`: probe interrupt (MIO 46 by spec 12 §4.2), exported after the phase training. A firmware that supports probe events opens `gpio380/value`, waits with `poll(POLLPRI)` and runs probe detection/identification (specs 06 §12, 15) once at start-up and after every rising edge |

The GPIO numbers above are the Linux sysfs numbers of the shipped kernel and DT. A replacement that keeps that
kernel and DT must use these sysfs numbers (not MIO numbers); one with its own kernel must reproduce the
numbering or look the lines up by controller offset (spec 12 §4.2).

Two further built-in board input drivers have no DT node and are inactive.

## 8. File systems and read-only policy

| Mount | Device | Options | Policy |
|---|---|---|---|
| `/` | mmcblk0p7 | ext4, rw from cmdline; remounted ro during the boot (§5) | read-only after boot |
| `/usr/bin/siglent` | mmcblk0p10 | ext4 ro | read-only |
| `/usr/bin/siglent/firmdata0` | mmcblk0p9 | ext4 ro | remount rw (`mount -o rw,remount`) only while writing calibration/identity, then ro |
| `/usr/bin/siglent/usr` | mmcblk0p12 | ext4 rw | user data; the only persistent writable area |
| `/mnt/logo` | mmcblk0p8 | vfat rw | logo pictures |
| `/tmp`, `/run`, `/dev/shm` | tmpfs | — | volatile |
| `/sys/kernel/debug` | debugfs | — | mounted by the boot (§5) |
| USB sticks | — | mounted by the firmware on demand | — |

The instrument has no shutdown handshake visible to software (power-off behaviour **UNVERIFIED**); the ro policy
protects p7, p9, p10 against power loss. A replacement firmware must keep its own writes on p12 and `sync` after
each write.

## 9. Network, time, login

- Ethernet (`config_eth0.sh`, background at boot): if `firmdata0/NSP_system_info.xml` has `<Mac>…</Mac>`:
  `ifconfig eth0 down; ifconfig eth0 hw ether <mac>; ifconfig eth0 up`. If `usr/config/NSP_usr_system_info.xml` exists:
  `<DHCP>ON</DHCP>` → `udhcpc -i eth0 -n -q`; otherwise `ifconfig eth0 <Ip> netmask <Subnet>` or, with no `<Ip>`,
  `10.11.13.220/24`. Without the file: `10.11.13.220/24`. After boot, DHCP renewal, link-local fallback
  (169.254.x.x), mDNS and SMB are the running firmware's job.
- Time: no RTC; system time starts at the epoch-default each boot until NTP (`ntpd`, servers 0–3.pool.ntp.org, config
  on p12 via the `/etc/ntp.conf` symlink; `/etc/init.d/S49ntp start`) or a manual `date -s` from the UI/SCPI. Time zone file `/etc/timezone` = `Etc/UTC`.
- Login surfaces: serial getty on `ttyPS0` (root account, shell `/bin/sh`, home `/usr/bin/siglent`); BusyBox
  `telnetd` installed (not started at boot); `dropbear` installed, never started; anonymous FTP
  (vsftpd) running; Samba (`ShareUser`) when enabled in the UI. No password material is documented here.

## 10. Supervision

- Hardware watchdogs: both Cadence WDTs (0xFD4D0000, 0xFF150000) are `status = "disabled"` in the DT; no user opens
  `/dev/watchdog`. Nothing resets a hung system.
- Kernel: `panic_timeout = 0` (no automatic reboot on panic), oops does not panic.
- No software thermal or fan supervision runs on this product (spec 15 §5–6); nothing reacts to over-temperature.
A replacement firmware may enable a watchdog only by changing the DT (not recommended without a unit to test).

## 11. Update and recovery

### 11.1 Application update (.ADS), on-device flow

The vendor update container (`.ADS`, encrypted; format not covered here) is applied in two boot-time stages that
both work through p12:

1. A one-shot script in `/usr/bin/siglent/usr/usr/upgrade/update.sh` runs on the next boot (§5 step 3) with `/` rw
   and nothing else running; it installs application-partition content and places BSP images (`BOOT.bin`,
   `uEnv.txt`, `Image`, `system.dtb`, `logo.bmp`, `update_logo.bmp`, `rootfs.ext4.gz`, `siglent.ext4.gz`,
   `factorydata.ext4.gz`, marker `bsp_update`) into `/usr/bin/siglent/usr/updatefile/`.
2. On the boot after that, U-Boot flashes every image found in `/updatefile/` (§2.2); Linux then empties it.

A replacement must not create `usr/usr/upgrade/` or files in `updatefile/` on p12 unless it intends these effects.

### 11.2 Version identities

`/etc/version` (`rootfs_version=8`), `/proc/version` (`siglent_version=14`), `/proc/cmdline` (`uboot_version=4`).

### 11.3 USB recovery

A FAT-formatted USB stick in a front USB host port containing `huluwa_udiskEnv.txt` is detected by U-Boot on every
boot (§2.2 step 1). The recovery package's environment does, in order: `show_update`; write `BOOT.bin`, `uEnv.txt`
→ p1; `Image`, `system.dtb` → p5; `logo.bmp`, `update_logo.bmp` → p8 (`fatwrite`); `rootfs.ext4.gz` → raw 0xC808000;
`firmdata0.ext4.gz` → raw 0x3F20C000; `siglent.ext4.gz` → raw 0x4240E000 (`gzwrite`, 1 MiB chunks); `show_logo`;
boot with `bootargs … uboot_version=reboot`. Each step runs only if the file is present on the stick (FAT
partition 1 or 2 of USB device 0–3). The stick must be removed afterwards, otherwise the recovery repeats on every
boot. A stick that contains `firmdata0.ext4.gz` overwrites p9 (calibration, serial, MAC, licences): never use a
stick carrying another unit's `firmdata0.ext4.gz`. Any `uenvcmd` on the stick runs with full U-Boot authority (it
can read every partition, write FAT and raw blocks, and boot any kernel/initramfs, but cannot write ext4, §12.4):
this is both the unbrick path and the install path (§12.4).

## 12. Launching a replacement firmware

### 12.1 What the vendor boot leaves running when S99extra runs

S99extra is the last S-script; it starts roughly 1–2 s after `main.app` and `vncserver` were started (only the
S-scripts run in between). At that moment the clock chip is programmed, the PL image is loaded, both VDMAs and the
LCD are configured (UI VDMA still showing the U-Boot logo at 0x1F400000), all modules of §7 rows 1–9 are loaded,
`/` is ro and p12 rw. The vendor application may or may not have accessed the front end, the ADCs or the
acquisition engine yet. It loads the USBTMC gadget modules only after tens of seconds, so they are normally
absent. The delayed display switch (§5 step 11) writes the UI VDMA about 10 s after `main.app` started. These
times are **UNVERIFIED** on hardware.

SIGTERM terminates `main.app` within well under a second without any cleanup, possibly in the middle of an SPI,
PL-register or beeper sequence; the replacement therefore re-initialises everything it uses. After `main.app` and
`vncserver` have exited, every loaded module stays loaded (including the gadget modules, if they were loaded),
and `lighttpd` + `php-cgi`, `syslogd`, `rpcbind`, `vsftpd` keep running.

A replacement **must keep**: the PL configuration (reloading the PL erases all PL state and requires redoing spec 13
§5), the clock chip programming (never re-run it while the PL is configured unless the ADC/PL clocking is
re-initialised afterwards, spec 13 §4), the loaded vendor modules if it uses the vendor ABI (spec 14), the mounts.

### 12.2 Takeover procedure

`extra.sh` (p12 root, i.e. `/usr/bin/siglent/usr/extra.sh`, mode 0755, first line `#!/bin/sh`):

1. Fallback check (§12.3): if the disable condition holds, `exit 0` — the vendor firmware keeps running.
2. Start the replacement in the background with its standard streams detached, e.g.
   `/usr/bin/siglent/usr/open/<binary> </dev/null >/dev/null 2>&1 &`, then `exit 0`. `&` is sufficient: the child is
   re-parented to init when `extra.sh` exits (`setsid` optional). `extra.sh` must not wait for it (§13).

The replacement then performs, in this order:

1. **Stop the display-switch script before it fires**: send SIGKILL to the shell whose command line contains
   `insmod_after_app.sh` **first**, then to its child `sleep 10` (killing only the `sleep` would make the shell run
   its two `devmem` writes at once). If the script has already finished, nothing to do (step 6 rewrites the VDMA
   anyway).
2. **Stop the vendor processes**: SIGTERM to every process whose `/proc/<pid>/cmdline` argv[0] is
   `/usr/bin/siglent/main.app` or `/usr/bin/siglent/vncserver` (vncserver holds `/dev/siglent_vnc`, `/dev/uinput`
   and `/dev/mem` mappings of 0x1F800000 and 0x80040000). These are not children of the replacement, so `wait()` is
   not available: poll every 100 ms; a process is gone when `/proc/<pid>` is absent or its state (third field of
   `/proc/<pid>/stat`) is `Z`. After 5 s send SIGKILL to any survivor and poll for another 5 s. If one is still
   present, abort the takeover without touching the hardware (log the failure on p12).
3. **Remove main.app's leftovers**: SIGKILL any process whose command line contains `/usr/bin/siglent/drivers/`
   (a `sh -c insmod …` main.app may have spawned), and, as the replacement requires, the services main.app starts
   at runtime (`udhcpc`, `ntpd`, `smbd`, `nmbd`, `avahi-daemon`, `telnetd`) and the boot services (`lighttpd`,
   `php-cgi`, `vsftpd`). Keep `rpcbind` if VXI-11 is served (spec 11).
4. **Silence the beeper**: set bit 0 of TTC0 Counter_Control_1 (0xFF11000C, read-modify-write; spec 08 §8).
5. **Halt acquisition and the sample DMA**: ACQ 0x019 (0x80030064) ← 0x00000008 (stop request, spec 03 §3.4; the
   other bits are rewritten by step 8); AXI DMA S2MM_DMACR (0x80010030) ← 0x4 (reset, self-clearing), wait ≥ 20 µs
   (spec 03 §4.4), then S2MM_DMASR (0x80010034) ← 0xFFFFFFFF (spec 02 §5.2).
6. **Take the display**: UI VDMA 0x8000005C ← the frame the replacement draws to (e.g. 0x1FC00000 = `/dev/fb0`),
   then 0x80000050 ← 0x258 (spec 13 §5.1). The LCD and backlight are already on.
7. **USBTMC gadget**: if `/sys/module/g_usbtmc` exists, either use `/dev/g_usbtmc` as left, or `rmmod g_usbtmc
   f_usbtmc` and reload with the replacement's parameters; if absent (normal case), load `f_usbtmc.ko` then
   `g_usbtmc.ko` itself (spec 11, spec 14 §9).
8. **Re-initialise** the acquisition engine (spec 03 §2) and the AFE/ADCs (spec 06) from scratch; never rely on
   state the vendor application left.

Logging: log to a file on p12 (e.g. `/usr/bin/siglent/usr/open/log/<name>.log`, `fsync` after each line or
batch), not to `/dev/kmsg`: the kernel rate-limits `/dev/kmsg` writes by default (`printk.devkmsg=ratelimit`,
10 lines per 5 s) and drops the rest. Use kmsg only for a few milestones.

### 12.3 Hooks (all on p12, no change to p7/p10)

| Hook | When | Use |
|---|---|---|
| `/usr/bin/siglent/usr/extra.sh` | every boot, last S-script, ~1–2 s after main.app started | **launch** (§12.2): executable, returns promptly |
| `/usr/bin/siglent/usr/usr/upgrade/update.sh` | next boot only (the test is on the directory `…/usr/upgrade/`), before PLL/PL init: `/` rw, p9 and p10 mounted ro, p12 rw, p8 rw, cwd `/usr/bin/siglent/`, rcS environment (`/etc/profile` not yet sourced); the script is `chmod +x`'d by the caller; afterwards the directory is deleted, `sleep 2`, `reboot -f` | one-shot self-update of an installed replacement (it can also remount p7/p10 rw). It must already be on p12, so it is not a first-install path |
| USB stick `huluwa_udiskEnv.txt` | every boot while the stick is inserted, before Linux | first install (§12.4) and unbrick (§11.3) |

Disable switch: the replacement must provide one before anything else. Simplest: `extra.sh` exits 0 if
`/usr/bin/siglent/usr/open/disable` exists. A key held at power-up can be detected by reading the key status words
0x80040580–0x8004058C directly (active low, mapping spec 08; reading them has no side effect); `/dev/siglent_kb`
cannot be used for this because it only reports key changes while a reader is blocked. Removing `extra.sh` returns
the unit to the vendor firmware on the next boot.

### 12.4 First install from a USB stick (installer initramfs)

U-Boot cannot write p12: its `ext4write` refuses file systems with `metadata_csum` (§3). The install therefore
boots the vendor kernel with a small installer initramfs from the stick; Linux writes p12. The whole method is
**UNVERIFIED** on a unit.

Stick: MBR, partition 1 FAT32, containing exactly:

- `huluwa_udiskEnv.txt` (below),
- `oinst.img`: U-Boot legacy ramdisk image of a gzip-compressed newc cpio archive:
  `mkimage -A arm64 -O linux -T ramdisk -C none -n oinst -d oinst.cpio.gz oinst.img`. Keep it ≤ 64 MiB.

`huluwa_udiskEnv.txt` (one `name=value` per line; `env import -t` stores the values unexpanded):

```
oi_mark=/open/installed-1
oi_have=ext4load mmc 0:0xc 0x200000 ${oi_mark}
oi_load=fatload mmc 0:5 0x3000000 Image && fatload mmc 0:5 0x2A00000 system.dtb && fatload usb ${usbdev} 0x6000000 oinst.img
oi_run=run show_update; setenv initrd_high 0x10000000; setenv bootargs console=ttyPS0,115200 earlycon cpuidle.off=1 rdinit=/init; booti 0x3000000 0x6000000 0x2A00000; run show_logo
uenvcmd=if run oi_have; then echo open: already installed; else if run oi_load; then run oi_run; fi; fi
```

Behaviour: if the marker file exists on p12 the stick is inert and U-Boot continues the normal boot (§2.2 steps
2–4; the p1 `uEnv.txt` import replaces `uenvcmd` and `bootargs`), so the stick may stay inserted. Otherwise U-Boot
shows the update picture and boots the vendor kernel (from p5) with the installer as initramfs; `initrd_high`
keeps the relocated initrd inside the 504 MiB Linux memory. If any load fails, U-Boot falls through to the normal
boot. A new version uses a new marker name.

Installer initramfs: `/init` is a static aarch64 executable (e.g. Go with `CGO_ENABLED=0 GOARCH=arm64`, or a
`#!/bin/sh` script plus a static busybox), and the payload under `/payload/` mirrors the target paths on p12
(`extra.sh`, `open/…`; p12's root is `/usr/bin/siglent/usr/` at run time). `/init` must:

1. Mount devtmpfs on `/dev` (the kernel does not mount it inside an initramfs), proc on `/proc`, sysfs on `/sys`;
   send its output to `/dev/console` (ttyPS0).
2. Wait for the block device `/dev/mmcblk0p12` (poll every 100 ms, up to 15 s).
3. `mount -t ext4 /dev/mmcblk0p12 /mnt` (read-write; the kernel replays the journal if p12 was not unmounted).
4. Create missing directories (`open/`, `open/log/`, …) with mode 0755, owner root:root. Copy every payload
   file: write `<path>.new`, `fsync`, set the mode (from the archive entry; 0755 for `extra.sh` and executables,
   owner root:root), rename to `<path>`, then `fsync` the parent directory (the rename is durable only after the
   directory is synced). `extra.sh` last. Then delete every uninstall marker `open/removed-*` (and fsync
   `open/`), so that an uninstall stick works again after this install.
5. Write the marker file named by `oi_mark` (non-empty content, e.g. the version string), `fsync` it and its
   directory, `sync`, `umount /mnt`.
6. `reboot(LINUX_REBOOT_CMD_RESTART)` (busybox `reboot -f`). The next boot finds the marker and boots normally,
   and S99extra starts the replacement.

On any error `/init` must not write the marker and must not reboot (that would repeat the install on every boot):
unmount, `sync`, report on the console and halt. "Halt" means: PID 1 stays alive in an endless sleep loop (it must
never exit — that panics the kernel — and should not power off, so the console message stays readable); the user
removes the stick and power-cycles into the vendor firmware.

Uninstall stick: the same with a different `oi_mark` (e.g. `/open/removed-1`) and an `/init` that deletes
`extra.sh` and every `open/installed-*` marker (fsync the directories), then writes its own marker. It leaves the
rest of `open/` (binaries, logs) in place — without `extra.sh` nothing runs it — and a later install overwrites
it. Marker life cycle: install deletes `open/removed-*`, uninstall deletes `open/installed-*`, so install and
uninstall sticks can be alternated, and each stick is inert while its own marker exists.

Other install routes: `ext4write mmc 0:0xc …` works only if p12 lacks `metadata_csum` (check with
`dumpe2fs -h /dev/mmcblk0p12`); a serial-console root shell (credentials not documented here) can copy the files
from a stick mounted under Linux.

### 12.5 Running without the vendor app at all (UNVERIFIED alternative)

Changing the boot scripts on p10 (remount rw) so that the vendor application is never started would avoid the
takeover, but changes the vendor partition and the next vendor update overwrites it. A replacement that does this
must reproduce §5 steps 1–9 (mounts, clock chip, PL reload, network, module loads) in the same order.

## 13. Constraints and traps

- Any access to PL addresses (0x8000_0000…) while the PL is unconfigured or being reconfigured stalls the AXI
  interconnect and hangs the CPU (standard ZynqMP behaviour). Order **must** be: clock chip → PL load → PL register
  access.
- `/dev/mem` is unrestricted; a wrong address write can corrupt Linux memory or the fb/VDMA buffers.
- **Do not extend the Linux memory node beyond 0x1F7F_FFFF.** The PL writes the acquisition record memory into
  PS DDR4 through the S_AXI_HP ports at an address range that is not yet known (most likely DRAM above
  0x2000_0000, which the shipped DT keeps out of Linux; spec 02 Open, spec 12 §4). A kernel that hands that memory
  to Linux would be overwritten by the PL. Keep the shipped memory node until the range is settled.
- The wavedata/rawdata/logo pools are `reusable` CMA: Linux may hand that memory to other allocations until the
  owning driver allocates it; only use those ranges through their drivers (or after their drivers loaded).
- `rcS` is an inittab `sysinit` action and runs S99extra synchronously; init starts the serial getty only after
  `rcS` has returned. A blocking `extra.sh` therefore stalls the boot **and** prevents the serial login; `extra.sh`
  must put its long-running work in the background and exit.
- Only p12 is writable without remounting; keep all persistent state there.

## Open

- Confirmation of the SoC part on a unit (spec 12 §1); total DRAM and eMMC sizes (read `/proc/meminfo`,
  `/proc/partitions`, `fdisk -l /dev/mmcblk0`).
- Partition starts/sizes of p1–p6, p8, p11, p12 (needs the MBR/EBRs from a unit).
- Whether U-Boot `gpio clear 9` is the relay-chain enable line (sysfs GPIO 343, §7); `modeboot` and `partid` values.
- Address range of the acquisition record memory in PS DDR4 (§1, §13): read `/proc/iomem` and the DDR beyond
  504 MiB on a unit (spec 12 Open).
- Which `/dev/fbN` maps to which `siglentfb` node (spec 07 §2.1); the depth `/dev/fb1` reports.
- Whether the first-boot `/lib/firmware` trap (§5) occurs on hardware.
- Size/format of a saved U-Boot environment `uboot.env` (§2.2).
- The USB installer (§12.4) end to end on a unit: U-Boot legacy-ramdisk `booti` with `initrd_high`, initramfs
  unpack, `/dev/mmcblk0p12` appearance time, PSCI reboot. Whether p12 has `metadata_csum` (`dumpe2fs -h`).
- The §12.1 timing on hardware (in particular whether the vendor application has touched the front end when
  S99extra runs) and whether stopping it leaves stale VXI-11 registrations in rpcbind.
- Serial-console root credentials (not documented).
- Except §5, the §7 order and §12.1, this spec is not yet confirmed on a unit.
