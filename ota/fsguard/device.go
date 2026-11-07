package fsguard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// deviceWords are parts of device and attribute paths that reach one-time
// programmable or secure-boot storage (Zynq eFuses, BBRAM key, nvmem
// providers) or the firmware interface that programs them. Writing any of
// them can make the SoC refuse the vendor BOOT.bin for good.
var deviceWords = []string{"efuse", "bbram", "nvmem", "/sys/firmware/", "/sys/bus/nvmem", "/sys/class/nvmem"}

// blockNames are the block-device nodes of the unit (the eMMC, its boot
// areas and partitions, MTD flash) and the kernel's block-layer attributes.
var blockNames = []string{
	"/dev/mmcblk", "/dev/mtd", "/dev/sd", "/dev/loop", "/dev/ram", "/dev/disk/",
	"/sys/block/", "/sys/class/block/", "/sys/class/mmc_host/", "/sys/bus/mmc/",
}

// CheckDevice returns nil when the device node or sysfs attribute at path
// may be opened for writing: our hardware control (GPIO export, spidev,
// fpga_manager, the takeover's attributes) passes; block devices, eFuse,
// BBRAM and nvmem do not, by name and, for an existing node, by type.
// TRLC-LINKS: REQ-HD-016
func CheckDevice(path string) error {
	clean, err := abs(path)
	if err != nil {
		return err
	}
	rp, err := resolve(clean)
	if err != nil {
		return err
	}
	for _, p := range []string{clean, rp} {
		lp := strings.ToLower(p) + "/"
		for _, w := range deviceWords {
			if strings.Contains(lp, w) {
				return refuse("write", path, "eFuse, BBRAM and nvmem storage are never written")
			}
		}
		for _, b := range blockNames {
			if strings.HasPrefix(lp, b) {
				return refuse("write", path, "a block device (raw eMMC or flash) is never written")
			}
		}
		if within(p, "/dev") {
			if err := checkName(p); err != nil {
				return err
			}
		}
	}
	if st, err := os.Stat(rp); err == nil && st.Mode()&os.ModeDevice != 0 && st.Mode()&os.ModeCharDevice == 0 {
		return refuse("write", path, "a block device (raw eMMC or flash) is never written")
	}
	return nil
}

// Physical register blocks of the ZynqMP no writable /dev/mem window may
// cover (UG1087): the CSU (secure lockdown, key sources, PCAP), the eFuse
// and BBRAM controllers, and both SD/eMMC host controllers, whose direct
// use would write the eMMC behind the kernel.
var physForbidden = []struct {
	base, size uint64
	what       string
}{
	{0xFF16_0000, 0x1_0000, "SD0/eMMC host controller"},
	{0xFF17_0000, 0x1_0000, "SD1/eMMC host controller"},
	{0xFFCA_0000, 0x1_0000, "CSU (secure boot, key sources, PCAP)"},
	{0xFFCC_0000, 0x1_0000, "eFuse controller"},
	{0xFFCD_0000, 0x1_0000, "BBRAM key controller"},
}

// CheckPhys returns nil when a writable /dev/mem window of size bytes at
// phys covers none of the forbidden register blocks. Read-only windows (the
// census's) need no check.
// TRLC-LINKS: REQ-HD-016
func CheckPhys(phys, size uint64) error {
	for _, f := range physForbidden {
		if phys < f.base+f.size && f.base < phys+size {
			return refuse("map", fmt.Sprintf("/dev/mem %#x+%#x", phys, size), "covers the "+f.what)
		}
	}
	return nil
}

// isBlockSource reports whether a /proc/mounts source is the unit's eMMC
// (or the kernel's alias for the root partition).
// TRLC-LINKS: REQ-HD-016
func isBlockSource(src string) bool {
	return strings.HasPrefix(src, "/dev/mmcblk") || src == "/dev/root" || strings.HasPrefix(src, "/dev/mtd") ||
		strings.HasPrefix(filepath.Clean(src), "/dev/disk/")
}
