// Package hdguard is the SDS800X HD's write-guard policy: what the core's
// write guard (open-dso-core/fsguard) refuses on this unit, and the guard
// both of its programs write through. The app module and the ota module
// both import it; it imports only the core and the standard library, so
// neither module requires the other's internals.
//
// The vendor U-Boot reads a USB stick at every boot before Linux runs; that
// stick route is both the unbrick path and the install path (spec 01 §2.2,
// §11.3, §12.4), and it lives in BOOT.bin on eMMC p1. The unit stays
// recoverable as long as nothing of ours touches what that route depends
// on, so the guard refuses:
//
//   - any file outside p12's root (/usr/bin/siglent/usr), after cleaning
//     the path and resolving symlinks: p1 (BOOT.bin, uEnv.txt), p5 (kernel,
//     device tree), p7/p10 (vendor system) and p9 (factory data:
//     calibration, serial, MAC, licences) are never written;
//   - inside p12, the two directories the vendor boot turns into eMMC
//     writes: updatefile/ (U-Boot flashes BOOT.bin, the kernel and raw
//     partition images found there) and usr/upgrade/ (update.sh runs as
//     root with / writable), as targets and as roots;
//   - any path through a symlink below p12's root;
//   - a U-Boot environment file anywhere (uboot.env: a valid one replaces
//     U-Boot's built-in boot command for good) and the stick's recovery
//     environment;
//   - block devices and raw eMMC nodes, eFuse, BBRAM and nvmem in /dev and
//     /sys, and /dev/mem windows over the CSU, eFuse and BBRAM controllers
//     and the SD/eMMC host controllers.
//
// The installer's /init mounts p12 on /mnt and roots the guard there
// (FS.SetRoot); tests root it in a temporary directory.
package hdguard

import "github.com/labeth/open-dso-core/fsguard"

// P12 is p12's root at run time: the only partition our code writes.
const P12 = "/usr/bin/siglent/usr"

// P12Device is p12's block device: the one eMMC partition anything of ours
// mounts for writing.
const P12Device = "/dev/mmcblk0p12"

// OpenDir is the open firmware's directory on p12, where the launch hook
// looks for the disable file (spec 01 §12.3).
const OpenDir = P12 + "/open"

// vendorPartition is why the vendor partitions are never written.
const vendorPartition = "a vendor partition (p8, p9 or p10)"

// Policy is the unit's rules.
var Policy = fsguard.Policy{
	Root: P12, RootName: "p12",
	// p12 lies inside /usr/bin/siglent and is the one exception.
	Vendor:     []fsguard.Area{{Dir: "/usr/bin/siglent", Why: vendorPartition}, {Dir: "/mnt/logo", Why: vendorPartition}},
	Enclosable: []string{"/mnt/logo"},
	System: []string{
		"/bin", "/boot", "/dev", "/etc", "/lib", "/lib64", "/mnt/logo", "/proc", "/root", "/sbin", "/sys", "/usr", "/var",
	},
	// spec 01 §2.2, §5 step 3, §11.1, §12.3
	RootForbidden: []fsguard.Area{
		{Dir: "updatefile", Why: "flashed by the vendor boot"},
		{Dir: "usr/upgrade", Why: "run by the vendor boot"},
	},
	// U-Boot's saved environment (spec 01 §2.2), the boot partition's files
	// and the stick's recovery environment (§11.3).
	Names:    []string{"uboot.env", "uboot-redund.env", "boot.bin", "uenv.txt", "huluwa_udiskenv.txt"},
	NamesWhy: "a boot or U-Boot environment file is never written",
	// Zynq eFuses, the BBRAM key, nvmem providers and the firmware
	// interface that programs them: writing any can make the SoC refuse the
	// vendor BOOT.bin for good.
	DeviceWords: []string{"efuse", "bbram", "nvmem", "/sys/firmware/", "/sys/bus/nvmem", "/sys/class/nvmem"},
	// The eMMC, its boot areas and partitions, MTD flash, and the kernel's
	// block-layer attributes.
	BlockPrefixes: []string{
		"/dev/mmcblk", "/dev/mtd", "/dev/sd", "/dev/loop", "/dev/ram", "/dev/disk/",
		"/sys/block/", "/sys/class/block/", "/sys/class/mmc_host/", "/sys/bus/mmc/",
	},
	// ZynqMP register blocks (UG1087): the CSU (secure lockdown, key
	// sources, PCAP), the eFuse and BBRAM controllers, and both SD/eMMC host
	// controllers, whose direct use would write the eMMC behind the kernel.
	Phys: []fsguard.Window{
		{Base: 0xFF16_0000, Size: 0x1_0000, What: "SD0/eMMC host controller"},
		{Base: 0xFF17_0000, Size: 0x1_0000, What: "SD1/eMMC host controller"},
		{Base: 0xFFCA_0000, Size: 0x1_0000, What: "CSU (secure boot, key sources, PCAP)"},
		{Base: 0xFFCC_0000, Size: 0x1_0000, What: "eFuse controller"},
		{Base: 0xFFCD_0000, Size: 0x1_0000, What: "BBRAM key controller"},
	},
	// Nothing of ours lives behind a symlink on p12 (the slot layout is plain
	// directories), so a link there is planted or damage: never written
	// through, even where it stays on p12 (SAFETY.txt rule 4).
	NoLinks: true,
	Device:  P12Device, DeviceFS: "ext4",
	UnitSources: []string{"/dev/mmcblk", "/dev/root", "/dev/mtd", "/dev/disk/"},
}

// FS is the process's write guard under Policy.
var FS = fsguard.New(Policy)
