package frontend

import (
	"math"

	"github.com/labeth/open-sds800xhd/app/internal/platform"
)

// The calibration model of spec 10 §2 and its no-file defaults (spec 10
// §4.3, spec 06 §6.3).

// Front-end gain groups g (spec 06 §6.1).
const (
	groupX100 = 0
	groupX10  = 1
	groupX1   = 2
)

// Offset-DAC attenuator settings d (spec 06 §3.3).
const (
	att1X    = 0
	att0p5X  = 1
	att0p25X = 2
	att0p1X  = 3
)

// Rate modes m of the calibration tables (spec 10).
const (
	rateSingle = 0
	rateUnite  = 2
)

// g0 is the default X1 VGA gain table, mV/div per step (spec 10 §4.3).
var g0 = [21]float32{
	10.66, 13.41, 16.88, 21.20, 26.67, 33.58, 42.26, 53.12, 66.81, 84.20, 105.99,
	1.078, 1.375, 1.726, 2.171, 2.725, 3.433, 4.326, 5.417, 6.842, 8.587,
}

// defaultSlope is the offset-DAC slope table, codes per volt (spec 10 §4.3).
var defaultSlope = [4][3]float32{
	{18.78, 194.8, 1948.4},
	{36.24, 375.5, 3755.6},
	{69.07, 716.0, 7161.6},
	{187.1, 1939.1, 19403.1},
}

// defaultCentre is the offset-DAC centre code without a self-cal file.
const defaultCentre = 0x8012

type vgaEntry struct {
	gain   [3]float32
	centre [3]int32
}

// Cal is one channel's calibration.
type Cal struct {
	slope [4][3]float32      // DAC[d].slope[g]
	fs    [][3]float64       // full-scale table FS[j] = {single, unite, reserved}
	vga   [3][3][21]vgaEntry // V[g][d'][s]
}

// fsGeometry is the full-scale table geometry of a converter (spec 06 §6.2).
type fsGeometry struct {
	n       int
	codeMin int
	codeMax int
	f0      float32
}

func fsFor(part platform.ADCPart) fsGeometry {
	if part == platform.ADC12DL1500 {
		return fsGeometry{n: 11, codeMin: 0x2000, codeMax: 0xFFFF, f0: 0.625}
	}
	return fsGeometry{n: 41, codeMin: 0, codeMax: 0x7FFF, f0: 0.75}
}

// fma32 is a fused float32 multiply-add, as the full-scale arithmetic
// requires (spec 06 §6.2).
func fma32(a, b, c float32) float32 { return float32(math.FMA(float64(a), float64(b), float64(c))) }

// DefaultCal is a channel's calibration when no calibration file exists.
func DefaultCal(part platform.ADCPart) Cal {
	var c Cal
	c.slope = defaultSlope
	p := fsFor(part)
	step := (1.25 - p.f0) / float32(p.n-1)
	c.fs = make([][3]float64, p.n)
	for j := range p.n {
		v := float64(fma32(float32(j), step, p.f0))
		c.fs[j] = [3]float64{v, v, v}
	}
	mult := [3]float32{100, 10, 1}
	for g := range 3 {
		for d := range 3 {
			for s := range 21 {
				gn := mult[g] * g0[s]
				c.vga[g][d][s] = vgaEntry{gain: [3]float32{gn, gn, gn}, centre: [3]int32{defaultCentre, defaultCentre, defaultCentre}}
			}
		}
	}
	return c
}
