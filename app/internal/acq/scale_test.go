package acq

import (
	"math"
	"testing"

	"github.com/labeth/open-sds800xhd/app/internal/frontend"
)

// The word scale names the screen's centre line and the offset it reads:
// a 0..3.3 V square at 500 mV/div with its 0 V 3.3 divisions below the
// centre reads its volts as before, the centre line's word is mid-scale
// and the offset is the 1.65 V the centre line reads below zero.
func TestScaleCentreLine(t *testing.T) {
	for _, inv := range []bool{false, true} {
		ch := frontend.Channel{On: true, VoltsDiv: 0.5, Invert: inv}
		d := frontend.Derived{PosPx: -3.3 * 60}
		sc := scaleOf(ch, d)
		if sc.Zero != 0x8000 {
			t.Fatalf("invert %v: centre word %v", inv, sc.Zero)
		}
		per := 0.5 / (480 * 16)
		zero0 := 0x8000 + d.PosPx*8*16
		if inv {
			per, zero0 = -per, 0x8000-d.PosPx*8*16
		}
		for _, w := range []float64{0x0800, 0x8000, 0xC000, 0xF7F0} {
			want := (w - zero0) * per
			if got := (w-sc.Zero)*sc.PerCode - sc.OffsetV; math.Abs(got-want) > 1e-9 {
				t.Fatalf("invert %v word %#x: %g V, want %g", inv, int(w), got, want)
			}
		}
		if !inv && math.Abs(-sc.OffsetV-1.65) > 1e-9 {
			t.Fatalf("the centre line reads %g V, want 1.65", -sc.OffsetV)
		}
	}
}
