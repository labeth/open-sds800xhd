package mmio_test

import (
	"errors"
	"testing"

	"github.com/labeth/open-sds800xhd/app/internal/mmio"
	"github.com/labeth/open-sds800xhd/ota/fsguard"
)

// A writable window over the eFuse, BBRAM, CSU or SD/eMMC controllers is
// refused before /dev/mem is opened.
// TRLC-LINKS: REQ-HD-016
func TestMapRefusesSecureBlocks(t *testing.T) {
	for _, phys := range []uint64{0xFFCC_0000, 0xFFCD_0000, 0xFFCA_0000, 0xFF16_0000, 0xFF17_0000} {
		if _, err := mmio.Map(phys); !errors.Is(err, fsguard.ErrRefused) {
			t.Errorf("%#x: %v", phys, err)
		}
	}
}
