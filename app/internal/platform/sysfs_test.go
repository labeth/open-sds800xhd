package platform

import (
	"errors"
	"testing"

	"github.com/labeth/open-sds800xhd/ota/fsguard"
)

// Attribute writes to raw eMMC, eFuse and nvmem paths are refused before
// anything is opened.
// TRLC-LINKS: REQ-HD-016
func TestWriteAttrRefusesStorage(t *testing.T) {
	for _, p := range []string{
		"/dev/mmcblk0p1", "/dev/mmcblk0", "/sys/block/mmcblk0/force_ro",
		"/sys/bus/nvmem/devices/zynqmp-nvmem0/nvmem", "/sys/firmware/zynqmp/pggs0",
	} {
		if err := WriteAttr(p, "0"); !errors.Is(err, fsguard.ErrRefused) {
			t.Errorf("%s: %v", p, err)
		}
	}
}
