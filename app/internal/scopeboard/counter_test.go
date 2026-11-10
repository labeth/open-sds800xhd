package scopeboard

import (
	"testing"

	"github.com/labeth/open-sds800xhd/app/internal/board"
	"github.com/labeth/open-sds800xhd/app/internal/mmio"
	"github.com/labeth/open-sds800xhd/app/internal/mmio/mmiotest"
)

// The counter source is the committed mux, not a source staged while
// stopped. A changed mux starts a fresh gate instead of relabeling old data.
func TestCounterUsesActualSource(t *testing.T) {
	bus := mmiotest.New()
	bus.Status[0x06E*4] = func() uint32 { return 1 }
	bus.Status[0x068*4] = func() uint32 { return 4495000 & 0xffff }
	bus.Status[0x069*4] = func() uint32 { return 4495000 >> 16 }
	b := New(nil)
	b.regs = &board.Board{ACQ: mmio.NewPlane("acq", bus)}
	ctr := (*counterFacet)(b)
	if r, err := ctr.Read(); err != nil || r.Valid {
		t.Fatalf("first gate: %+v %v", r, err)
	}
	if r, err := ctr.Read(); err != nil || !r.Valid || r.FreqHz != 5e6 {
		t.Fatalf("frequency: %+v %v", r, err)
	}
	b.staged.Trig.Source = 1
	if r, _ := ctr.Read(); !r.Valid || ctr.Source().Index != 0 {
		t.Fatalf("staged source relabeled the hardware counter: %+v %+v", r, ctr.Source())
	}
	b.regs.ACQ.Field(0x00E, 6, 4, 1)
	if r, _ := ctr.Read(); r.Valid || ctr.Source().Index != 1 {
		t.Fatalf("old reading survived the mux change: %+v %+v", r, ctr.Source())
	}
	if r, err := ctr.Read(); err != nil || !r.Valid || r.FreqHz != 5e6 {
		t.Fatalf("new source gate: %+v %v", r, err)
	}
}
