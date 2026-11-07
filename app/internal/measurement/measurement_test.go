package measurement

import (
	"math"
	"testing"

	"github.com/labeth/open-dso-core/sample"
)

// A square wave between two 12-bit codes that are not multiples of 16 (the
// 8-bit grid of the core's Q8.8 histogram) measures to within one 12-bit
// code: the core's settled-level search refines below its 256 bins.
func TestTwelveBitLevels(t *testing.T) {
	const vdiv = 1.0
	per := vdiv / (480 * 16)
	tr := sample.Trace{Format: sample.CodeFormat{Storage: sample.U16, Mid: 0x8000}, Scale: sample.Scale{PerCode: per, Zero: 0x8000}}
	hi, lo := uint16((0x800+243)<<4), uint16((0x800-117)<<4)
	for i := range 4000 {
		w := lo
		if i%200 < 100 {
			w = hi
		}
		tr.Codes16 = append(tr.Codes16, w)
	}
	r := Trace(tr, 1e-9)
	code := vdiv / 480
	for name, c := range map[string][2]float64{
		"top": {r.Vtop, 243 * code}, "base": {r.Vbase, -117 * code},
		"max": {r.Vmax, 243 * code}, "min": {r.Vmin, -117 * code},
	} {
		if math.Abs(c[0]-c[1]) > code {
			t.Errorf("%s = %.5f V, want %.5f V ± one code (%.5f)", name, c[0], c[1], code)
		}
	}
	if math.Abs(r.Freq-5e6) > 1 {
		t.Errorf("freq %g", r.Freq)
	}
}
