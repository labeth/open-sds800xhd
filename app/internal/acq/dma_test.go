package acq

import (
	"strings"
	"testing"
	"time"

	"github.com/labeth/open-sds800xhd/app/internal/mmio"
	"github.com/labeth/open-sds800xhd/app/internal/mmio/mmiotest"
)

// A transfer whose status carries an error bit is a failed read with the
// bits named; a clean completion is none.
func TestDMAStatusErr(t *testing.T) {
	if err := dmaStatusErr(64, 0x1002); err != nil { // idle, completion interrupt
		t.Errorf("clean completion: %v", err)
	}
	for _, c := range []struct {
		sr   uint32
		want string
	}{
		{dmaSlvErr | dmaErrIrq, "slave error"},
		{dmaDecErr | dmaErrIrq | 0x1, "decode error"},
		{dmaIntErr, "internal error"},
	} {
		err := dmaStatusErr(64, c.sr)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("status %#x: %v, want an error naming %q", c.sr, err, c.want)
		}
	}
}

// A PL whose image identity changed, or whose start-up sentinel reads its
// power-up 0, is reloaded; the check reads the PL at most every
// reloadEvery, and a sentinel that never read back is not used.
func TestReloaded(t *testing.T) {
	bus := mmiotest.New()
	a := mmio.NewPlane("acq", bus)
	e := &Engine{acq: a, ver: [3]uint16{0, 0, 0x1234}, sentinel: true}
	bus.Write32(4*0x002, 0x1234)
	bus.Write32(4*0x1EF, 1)
	if r, why := e.Reloaded(); r {
		t.Fatalf("unchanged PL reloaded: %s", why)
	}
	bus.Write32(4*0x1EF, 0)
	if r, _ := e.Reloaded(); r {
		t.Error("checked again within reloadEvery")
	}
	e.checkAt = time.Time{}
	if r, why := e.Reloaded(); !r || !strings.Contains(why, "0x1EF") {
		t.Errorf("sentinel at 0: reloaded %v (%s)", r, why)
	}
	e.sentinel, e.checkAt = false, time.Time{}
	if r, why := e.Reloaded(); r {
		t.Errorf("an unused sentinel: reloaded (%s)", why)
	}
	bus.Write32(4*0x002, 0x4F01)
	e.checkAt = time.Time{}
	if r, why := e.Reloaded(); !r || !strings.Contains(why, "identity") {
		t.Errorf("identity changed: reloaded %v (%s)", r, why)
	}
}
