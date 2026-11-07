package acq

import (
	"errors"
	"math/bits"

	"github.com/labeth/open-sds800xhd/app/internal/mmio"
	"github.com/labeth/open-sds800xhd/app/internal/timebase"
)

// writeRoll is the roll timebase set of spec 04 §7, in place of §8.2, §9
// and §8.3 in each bank window.
func writeRoll(t *mmio.Tx, h timebase.Set, mask int) {
	r := h.RollSet()
	t.Field(0x022, 14, 12, 1)
	t.Field(0x010, 15, 15, 0)
	t.Set(0x1EE, 10)
	t.Field(0x010, 11, 0, uint16(r.Scan)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set32(0x05D, 0x05E, h.DownRatio(), 0xFFF)
	t.Set(0x004, uint16(h.Decim))          //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Field(0x022, 9, 8, uint16(h.Switch)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set32(0x04B, 0x04C, h.DownDivisor(), 0xFFF)
	t.Set(0x04E, b2u(h.Decim != 0 || h.Switch != 0))
	t.Set(0x005, uint16(r.Scan))                    //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x092, uint16(r.Lr))                      //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x094, uint16(r.Lr))                      //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set32(0x050, 0x051, uint32(r.Offset), 0xFFFF) //nolint:gosec // G115: the register pair takes the value's low 32 bits
	for _, n := range []int{0x096, 0x098, 0x097, 0x099} {
		t.Set(n, 0)
	}
	t.Set32(0x01A, 0x01B, uint32(h.D), 0xFFFF) //nolint:gosec // G115: the register pair takes the value's low 32 bits
	t.Field(0x033, 0, 0, 0)
	t.Set(0x00A, 0)
	t.Set(0x0A0, 4)
	t.Set(0x0B6, 0)
	t.Set(0x083, 1)
	t.Set(0x082, 1)
	for _, n := range []int{0x0C0, 0x0BA, 0x0BE, 0x0B8, 0x03F} {
		t.Set(n, 0)
	}
	t.Set(0x0B1, uint16(mask)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	sv := h.SegmentValue()
	t.Set(0x027, uint16(sv))               //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Field(0x05B, 7, 7, uint16(sv>>16)&1) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	for _, n := range []int{0x09A, 0x09B, 0x09E, 0x08C, 0x08E, 0x0A7} {
		t.Set(n, 0)
	}
	t.Set(0x08A, 1)
}

// RollStart starts the roll stream (spec 03 §6.4, start) after a commit of
// settings with Roll on, and returns its geometry.
func (e *Engine) RollStart() (RollGeometry, error) {
	if !e.cur.Roll {
		return RollGeometry{}, errors.New("roll is not committed")
	}
	if e.dma == nil {
		return RollGeometry{}, errors.New("readout DMA unavailable")
	}
	a := e.acq
	mask := e.cur.Mask(e.nch)
	g := NewRollGeometry(e.h, bits.OnesCount(uint(mask)))
	a.Field(0x018, 10, 10, 0)
	a.Set(0x00A, 0)
	a.Set(0x01E, 0)
	a.Set(0x0D5, uint16(g.Rate)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	a.Set(0x0D4, 0, 1, 0)
	a.Set(0x024, b2u(g.Rate != 0))
	a.Field(0x0D3, 1, 0, 0)
	e.mp.Set(0x03C, 0)
	a.Set(0x0C9, 0, 1)
	a.Set(0x0CA, 1)
	a.Set(0x0C1, 0)
	a.Set(0x0B1, uint16(mask)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	e.rolling = true
	return g, nil
}

// RollStop stops the stream (spec 03 §6.4, stop).
func (e *Engine) RollStop() {
	if !e.rolling {
		return
	}
	a := e.acq
	a.Set(0x0CA, 0)
	a.Set(0x024, 0)
	a.Set(0x0D5, 0)
	e.Map.RollStop()
	e.rolling = false
}

// Rolling reports whether the roll stream runs.
func (e *Engine) Rolling() bool { return e.rolling }

// RollPoll is one poll of the stream FIFO (spec 03 §6.4, poll): when at
// least one group is available it moves whole groups into the DMA window
// and returns them (valid until the next transfer); nil when none is ready.
func (e *Engine) RollPoll(g RollGeometry) ([]byte, error) {
	avail := 4*int(e.acq.HW(0x0CB)&0x7FF) + 8*int(e.mp.HW(0x047)&0x7FF)
	if avail < g.Group {
		return nil, nil
	}
	n := min(avail/g.Group, maxBlock/g.Group) * g.Group
	if err := e.dma.block(nil, n); err != nil {
		return nil, err
	}
	return e.dma.buf[:n], nil
}
