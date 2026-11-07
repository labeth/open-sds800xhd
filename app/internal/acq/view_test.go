package acq

import (
	"testing"

	"github.com/labeth/open-sds800xhd/app/internal/mmio"
	"github.com/labeth/open-sds800xhd/app/internal/mmio/mmiotest"
	"github.com/labeth/open-sds800xhd/app/internal/timebase"
)

func bankRegs(f func(t *mmio.Tx)) *mmio.Plane {
	p := mmio.NewPlane("acq", mmiotest.New())
	p.Locked(f)
	return p
}

func checkRegs(t *testing.T, p *mmio.Plane, want map[int]uint16) {
	t.Helper()
	for r, v := range want {
		if got := p.Shadow(r); got != v {
			t.Errorf("%#03x = %#x, want %#x", r, got, v)
		}
	}
}

// The examples of spec 04 §11.
func TestZoomBank(t *testing.T) {
	cases := []struct {
		name         string
		tMain, tZoom float64
		depth        int
		want         map[int]uint16
	}{
		{"10M 1ms/div zoom 200ns/div", 1e-3, 200e-9, 3, map[int]uint16{0x09A: 0xF68A, 0x08C: 0xED14, 0x09E: 6}},
		{"10k 1ms/div zoom 20us/div", 1e-3, 20e-6, 0, map[int]uint16{
			0x083: 0xA, 0x119: 0xA, 0x086: 0xA, 0x085: 0x28, 0x090: 0x13, 0x0CE: 0x1C,
			0x092: 0x52, 0x094: 0x1F4, 0x10E: 0x5C,
			0x082: 0xA, 0x0B8: 0x14, 0x03F: 0x4F, 0x0BA: 0x3A, 0x071: 0xA, 0x070: 0x28, 0x074: 0x13, 0x076: 0x1C,
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := timebase.Compute(timebase.Config{Enum: timebase.EnumOf(c.tMain, 2), Depth: c.depth, Mask: 1})
			z, ok := ZoomOf(h, h.TimeDiv, c.tZoom, 2)
			if !ok {
				t.Fatal("zoom window refused")
			}
			p := bankRegs(func(tx *mmio.Tx) { writeZoomBank(tx, h, z, timebase.PosInput{}, 0, 1, 0) })
			checkRegs(t, p, c.want)
		})
	}
}

func TestZoomTooFewSamples(t *testing.T) {
	h := timebase.Compute(timebase.Config{Enum: timebase.EnumOf(200e-9, 2), Depth: 3, Mask: 1})
	if _, ok := ZoomOf(h, h.TimeDiv, 50e-12, 2); ok {
		t.Error("a zoom window of fewer than 4 samples was accepted")
	}
}

// The XY example of spec 04 §10: 2 ch, 1 GSa/s, 200 ns/div.
func TestXYBank(t *testing.T) {
	h := timebase.Compute(timebase.Config{Enum: timebase.EnumOf(200e-9, 2), Depth: 3, Mask: 3})
	if h.S != 2000 {
		t.Fatalf("S = %v, want 2000", h.S)
	}
	p := bankRegs(func(tx *mmio.Tx) { writeXY(tx, h, 1) })
	checkRegs(t, p, map[int]uint16{0x092: 0x408, 0x094: 0x3E8, 0x10E: 0x41C, 0x198: 1, 0x098: 1, 0x0A0: 1})
	// The YT+XY variant puts the section count in 0x09F after 0x095 ← 1.
	p = bankRegs(func(tx *mmio.Tx) { writeXY(tx, h, 0x10) })
	checkRegs(t, p, map[int]uint16{0x092: 0x408, 0x094: 0x3E8, 0x198: 0x10, 0x095: 1, 0x09F: 1, 0x098: 0})
}

func TestXYPairs(t *testing.T) {
	for mask, want := range map[int]uint16{0: 0, 1: 1, 3: 1, 4: 4, 0xC: 4, 5: 5, 0xF: 5} {
		if got := XYPairs(mask); got != want {
			t.Errorf("XYPairs(%#x) = %d, want %d", mask, got, want)
		}
	}
}

// TestZoomResize: the zoom bank's resize carries the zoom gain and puts the
// zoom centre on the screen centre (spec 02 §4.2).
func TestZoomResize(t *testing.T) {
	tests := []struct {
		name         string
		gain, centre float64
		want         map[int]uint16
	}{
		{name: "unity", gain: 1, want: map[int]uint16{0x1E1: 0x4000, 0x1E2: 0, 0x1DF: 0, 0x1E0: 0, 0x1E3: 0x80, 0x1E4: 0xF7F}},
		{name: "unset gain is 1", gain: 0, want: map[int]uint16{0x1E1: 0x4000}},
		{name: "gain 2 about +1 div", gain: 2, centre: 1, want: map[int]uint16{0x1E1: 0x8000, 0x1DF: 0xFC40, 0x1E0: 0xFFFF}},
		{name: "gain 1.5 about -0.5 div", gain: 1.5, centre: -0.5, want: map[int]uint16{0x1E1: 0x6000, 0x1DF: 360, 0x1E0: 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := bankRegs(func(tx *mmio.Tx) { writeZoomResize(tx, tt.gain, tt.centre) })
			checkRegs(t, p, tt.want)
		})
	}
}
