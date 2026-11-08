package frontend

import (
	"fmt"
	"math"
)

// The V/div range at the BNC (spec 06 §6.1, §12) and the probe factors
// offered.
const (
	MinVoltsDiv = 0.5e-3
	MaxVoltsDiv = 10
	MinProbe    = 0.1
	MaxProbe    = 10000.0
)

// bncLadder is the 1-2-5 ladder at the BNC, 500 µV … 10 V per division.
var bncLadder = []float64{0.5e-3, 1e-3, 2e-3, 5e-3, 10e-3, 20e-3, 50e-3, 0.1, 0.2, 0.5, 1, 2, 5, 10}

// Ladder is the V/div ladder at the probe tip for a probe factor. The
// 500 µV/div step at the BNC is the 1 mV/div analog range doubled by the PL
// (spec 06 §6.4): the records already carry the doubled codes, so the step
// is not a display zoom the core would have to apply.
func Ladder(probe float64) []float64 {
	out := make([]float64, len(bncLadder))
	for i, v := range bncLadder {
		out[i] = v * probe
	}
	return out
}

// Digital2x reports whether a V/div at the BNC is the PL-doubled class.
func Digital2x(bncVoltsDiv float64) bool { return bncVoltsDiv*1000 < 0.995 }

// CheckProbe returns why a probe factor cannot be used, or "".
func CheckProbe(probe float64) string {
	if probe < MinProbe || probe > MaxProbe || math.IsNaN(probe) {
		return fmt.Sprintf("probe factor %gx: this scope takes %gx … %gx", probe, MinProbe, MaxProbe)
	}
	return ""
}

// CheckVoltsDiv returns why a V/div at the tip cannot be set with a probe,
// or "". Values between the ladder steps are allowed (fine adjustment, spec
// 06 §6.1).
func CheckVoltsDiv(v, probe float64) string {
	const eps = 1e-9
	bnc := sig12(v / probe)
	switch {
	case !(v > 0):
		return si(v) + "/div: not a scale"
	case bnc < MinVoltsDiv*(1-eps):
		return fmt.Sprintf("%s/div at %gx: below the %s/div tip floor", si(v), probe, si(MinVoltsDiv*probe))
	case bnc > MaxVoltsDiv*(1+eps):
		return fmt.Sprintf("%s/div at %gx: above the %s/div tip ceiling", si(v), probe, si(MaxVoltsDiv*probe))
	}
	return ""
}

// SnapVoltsDiv cleans a V/div at the tip: a value within 0.1 % of a ladder
// step becomes the step, so a computed 0.1·10 is exactly 1; others stay
// (fine adjustment). Callers check the range first.
func SnapVoltsDiv(v, probe float64) float64 {
	for _, s := range Ladder(probe) {
		if math.Abs(v-s) <= 1e-3*s {
			return s
		}
	}
	return v
}

// si formats volts with a unit prefix: 2 mV, 500 µV, 10 V, 100 kV.
func si(v float64) string {
	a := math.Abs(v)
	switch {
	case a >= 1e3:
		return fmt.Sprintf("%g kV", v/1e3)
	case a >= 1 || a == 0:
		return fmt.Sprintf("%g V", v)
	case a >= 1e-3:
		return fmt.Sprintf("%g mV", math.Round(v*1e6)/1e3)
	}
	return fmt.Sprintf("%g µV", math.Round(v*1e9)/1e3)
}

// Probes are the probe factors the product offers: 1-2-5 steps across the
// front end's range (any factor in the range is taken over SCPI).
func Probes() []float64 {
	var out []float64
	for d := MinProbe; d <= MaxProbe; d *= 10 {
		for _, m := range []float64{1, 2, 5} {
			if v := d * m; v <= MaxProbe*(1+1e-9) {
				out = append(out, sig3(v))
			}
		}
	}
	return out
}

// BWLimits are the bandwidth limits by name: one 20 MHz limit (spec 06).
func BWLimits() []string { return []string{"full", "20M"} }

// sig3 rounds v to three significant digits: the decades' products carry
// binary noise (0.1·10 is not 1 in float).
func sig3(v float64) float64 {
	e := 1.0
	for v*e < 100 {
		e *= 10
	}
	for v*e >= 1000 {
		e /= 10
	}
	return float64(int64(v*e+0.5)) / e
}
