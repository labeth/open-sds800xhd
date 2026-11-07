// Package measurement runs the core's automatic measurements on this scope's
// traces.
//
// The core measures 8-bit codes, or Q8.8 words (8-bit codes with 8 fraction
// bits). The SDS800X HD's records hold 12-bit codes left-aligned in 16-bit
// words with mid-scale 0x8000, which is exactly a Q8.8 word: word/256 is the
// code in 8-bit units, centre 128. So the words go to the core unconverted,
// with the volts per 8-bit unit.
package measurement

import (
	"github.com/labeth/open-dso-core/measure"

	"github.com/labeth/open-dso-core/sample"
)

// Trace measures one 16-bit trace whose words are centred on 0x8000.
// sampleS is the interval between its words.
// TRLC-LINKS: REQ-HD-010
func Trace(t sample.Trace, sampleS float64) *measure.Result {
	if t.Format.Storage != sample.U16 || t.Format.Mid != 0x8000 {
		return nil
	}
	// v = (word − Zero)·PerCode − OffsetV = (word/256 − 128)·vpc − offV
	vpc := t.Scale.PerCode * 256
	offV := (t.Scale.Zero-0x8000)*t.Scale.PerCode + t.Scale.OffsetV
	return measure.ComputeQ8(t.Codes16, vpc, offV, sampleS)
}

// EdgeOffset is the distance in words from at to the nearest rising
// crossing of the trace's mid level (between its minimum and maximum), or 0
// if the trace has no rising crossing. It checks that the trigger sits on an
// edge.
func EdgeOffset(t sample.Trace, at float64) int {
	w := t.Codes16
	lo, hi := uint16(0xFFFF), uint16(0)
	for _, v := range w {
		lo, hi = min(lo, v), max(hi, v)
	}
	mid := (uint32(lo) + uint32(hi)) / 2
	best, found := 0, false
	for i := 1; i < len(w); i++ {
		if uint32(w[i-1]) < mid && uint32(w[i]) >= mid {
			d := i - int(at+0.5)
			if !found || abs(d) < abs(best) {
				best, found = d, true
			}
		}
	}
	return best
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
