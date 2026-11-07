// Package timebase is the horizontal arithmetic of the SDS800X HD (spec 04):
// sample rate, screen samples, interpolation, record depth, the trigger
// position and the map engine's timebase sets. It is pure computation; the
// acq package writes the results.
package timebase

//go:generate sh -c "go run ./internal/genrates ../../../specs/04-timebase-and-bands.md > rates_gen.go"

import (
	"math"
	"math/bits"
)

// TimeDiv is the s/div of a time/div enum 0 … 38 (spec 04 §2): 200 ps to
// 1000 s in 2-5-10 steps.
func TimeDiv(e int) float64 {
	m := [3]float64{2, 5, 10}[e%3]
	return m * math.Pow10(e/3) * 1e-10
}

// MaxEnum is the largest time/div enum.
const MaxEnum = 38

// EnumOf is the time/div enum nearest to s seconds, not below minE.
func EnumOf(s float64, minE int) int {
	best, bd := minE, math.Inf(1)
	for e := minE; e <= MaxEnum; e++ {
		if d := math.Abs(math.Log(TimeDiv(e) / s)); d < bd-1e-9 {
			best, bd = e, d
		}
	}
	return best
}

// Route is the channel route of an enabled-channel mask (spec 04 §1): 0 for
// at most one channel, 1 for two, 2 for three or four.
func Route(mask int) int {
	switch n := bits.OnesCount(uint(mask & 0xF)); {
	case n <= 1:
		return 0
	case n == 2:
		return 1
	}
	return 2
}

// DepthPoints is the memory depth of a depth enum (spec 04 §3).
func DepthPoints(route, enum int, limited bool) float64 {
	if enum < 4 {
		return [4]float64{1e4, 1e5, 1e6, 1e7}[enum]
	}
	if limited {
		return [3]float64{50e6, 25e6, 25e6}[route]
	}
	return [3]float64{100e6, 50e6, 25e6}[route]
}

// tolEq compares within the decade tolerance of spec 04 §8.4.
func tolEq(a, b float64) bool {
	t := 1e-6
	if x := math.Abs(b); x > 0 {
		t = math.Pow10(int(math.Floor(math.Log10(x)))) * 1e-5
	}
	return math.Abs(a-b) <= t
}

// interpTable maps S/1000 to the interpolation factor I (spec 04 §5.4).
var interpTable = []struct{ x, i float32 }{
	{0.00125, 800},
	{0.0025, 400},
	{0.005, 400},
	{0.01, 200},
	{0.0125, 80},
	{0.02, 100},
	{0.025, 40},
	{0.04, 50},
	{0.05, 40},
	{0.08, 25},
	{0.1, 20},
	{0.125, 8},
	{0.16, 25},
	{0.2, 10},
	{0.25, 4},
	{0.4, 5},
	{0.5, 4},
	{0.8, 5},
	{1.25, 4},
	{1.6, 5},
	{2.5, 2},
	{12.5, 2},
}

// InterpFactor is the display interpolation factor I for s screen samples.
func InterpFactor(s float32) int {
	x := s / 1000
	for _, e := range interpTable {
		if tolEq(float64(x), float64(e.x)) {
			return int(e.i)
		}
	}
	return 1
}

// The interpolation coefficient window per I (spec 04 §5.4).
var (
	iwI     = []int{1, 2, 4, 5, 8, 10, 20, 25, 40, 50, 80, 100, 200, 400}
	iwStart = []int{0, 0, 2, 6, 11, 19, 29, 49, 74, 114, 164, 244, 344, 544}
	iwEnd   = []int{0, 1, 5, 10, 18, 28, 48, 73, 113, 163, 243, 343, 543, 943}
)

// InterpWindow is the coefficient window (start, end) for i.
func InterpWindow(i int) (int, int) {
	for k, v := range iwI {
		if v == i {
			return iwStart[k], iwEnd[k]
		}
	}
	return 0, 0
}

var decimList = []float64{
	1, 2, 4, 5, 10, 20, 25, 40, 50, 100, 125, 200, 250, 400, 500, 1000, 1250, 2000, 2500,
	4000, 5000, 1e4, 1.25e4, 2e4, 2.5e4, 4e4, 5e4, 1e5, 1.25e5, 2e5, 2.5e5, 4e5, 5e5, 1e6, 1.25e6, 2e6, 2.5e6,
	4e6, 5e6, 1e7, 1.25e7, 2e7, 2.5e7, 4e7, 5e7, 1e8, 1.25e8, 2e8, 2.5e8, 5e8,
}

// Config is the horizontal request.
type Config struct {
	Enum    int  // time/div enum
	Depth   int  // depth enum 0–4
	Mask    int  // enabled analog channels
	Limited bool // the limited depth set (bandwidth below 200 MHz, spec 04 §3)
	Peak    bool // peak detect
	Seq     bool // sequence (segmented) mode is on
	Roll    bool // roll mode is on (spec 04 §7)
}

// Set is the derived horizontal configuration (spec 04 §8.1).
type Set struct {
	Config
	Route   int
	R       float64 // table sample rate, MSa/s
	S       float32 // screen samples (10 divisions)
	I       int     // display interpolation factor
	M       int     // work mode m: 0 = 2 GSa/s, 1 = 1 GSa/s, 2 = slower
	Switch  int     // switch select
	Delta   int     // DDR interval Δ, samples
	U       int     // trigger interval unit Δ/20
	Decim   int     // decimation index, 0 = none
	D       int     // record depth, Δ units
	TimeDiv float64 // s/div
}

// Compute derives the horizontal set of c (spec 04 §8.1, §8.2 step 5).
func Compute(c Config) Set {
	h := Set{Config: c, Route: Route(c.Mask)}
	col := c.Depth
	if col == 4 && c.Limited {
		col = 5
	}
	if c.Roll { // the 1ch table at depth min(depth, 1M) for every route (spec 04 §7)
		col = min(c.Depth, 2)
		h.R = rateTable[0][min(c.Enum, MaxEnum)][col]
	} else {
		h.R = rateTable[h.Route][min(c.Enum, MaxEnum)][col]
	}
	h.TimeDiv = TimeDiv(c.Enum)
	S := float32(10 * float32(h.TimeDiv*1e6) * float32(h.R))
	if S <= 0 {
		S = 1000
	}
	h.S = min(S, 2e8)
	h.I = InterpFactor(h.S)
	switch {
	case tolEq(h.R, 2000):
		h.M = 0
	case tolEq(h.R, 1000):
		h.M = 1
	default:
		h.M = 2
	}
	if c.Depth == 4 {
		switch {
		case h.Route == 0 && tolEq(h.R, 1000):
			h.Switch = 3
		case h.Route == 0 && h.R < 1000:
			h.Switch = 2
		case h.Route == 1 && h.R < 1000:
			h.Switch = 1
		}
	}
	switch h.Switch {
	case 0:
		h.Delta = [3]int{160, 80, 40}[h.M]
	case 1:
		h.Delta = 80
	default:
		h.Delta = 160
	}
	h.U = h.Delta / 20
	if h.R < 500 {
		q := math.Round(500 / h.R)
		for k, v := range decimList {
			if v == q {
				h.Decim = k
				break
			}
		}
	}
	add := 320 / h.Delta
	k := float32(1.01)
	if !(float64(h.S) < 1e8 && h.R >= 500) {
		k = 1.001
		add += 20
	}
	v := int(k * h.S)
	v = (v + h.Delta - 1) / h.Delta * h.Delta
	D := v/h.Delta + add
	h.D = max(8, (D+7)/8*8)
	if c.Seq && h.M == 0 && tolEq(float64(h.S), 1000) { // refresh optimisation (spec 04 §8.1)
		h.D = 16
	}
	return h
}

// SpeedCode is ACQ 0x022 bits 14:12 (spec 04 §5.1).
func (h Set) SpeedCode() uint16 {
	switch {
	case tolEq(h.R, 2000):
		return 3
	case tolEq(h.R, 1000):
		return 2
	}
	return 1
}

func clamp28(v float64) uint32 { return uint32(math.Min(v, 0x0FFF_FFFF)) }

// DownRatio is the down-sampling ratio of 0x05D/0x05E (spec 04 §5.2).
func (h Set) DownRatio() uint32 {
	if h.R >= 500 {
		return 1
	}
	return clamp28(math.Round(500 / h.R))
}

// DownDivisor is the divisor of 0x04B/0x04C (spec 04 §5.2).
func (h Set) DownDivisor() uint32 { return clamp28(math.Round(2000 / h.R)) }

// Sections is the section length N of a screen record (spec 04 §8.2 step 3).
func Sections(x int) int {
	for _, n := range []int{5000, 4000, 6250} {
		if x != 0 && (n%x == 0 || x%n == 0) {
			return n
		}
	}
	return 5000
}

// SegmentValue is the segment count register value (spec 04 §8.2 step 6):
// the history and sequence capacity M as M + 1, or 1 when M is 1 or in roll.
func (h Set) SegmentValue() int {
	fac := [4][3]float64{{1, .5, .25}, {1, .5, .5}, {1, .5, 1}, {1, 1, .25}}[h.Switch][h.M]
	mem := 240e6 * fac
	u := float64(2*h.Delta*h.D + 100)
	q := math.Min(math.Floor(mem/u), 80000)
	m := q
	if mem < u*(q+1) {
		m = q - 1
	}
	m = math.Max(m, 1)
	if m == 1 || h.Roll {
		return 1
	}
	return int(m) + 1
}

// trigTimeouts is T[e] of spec 05 §7.
var trigTimeouts = []uint32{
	1, 1, 9, 18, 44, 88, 175, 438, 875, 1750, 4375, 8750, 17500, 43750, 87500,
	175000, 437500, 875000, 1750000, 4375000, 3500000, 7000000, 13125000,
	26250000, 52500000, 131250000, 250000000,
}

// Timeouts are the no-trigger and trigger timeouts NTR, TR (spec 05 §7).
func Timeouts(e int, trOver uint32) (ntr, tr uint32) {
	switch {
	case e >= 1 && e <= 21:
		return 2 * trigTimeouts[e], 2 * trOver
	case e >= 22 && e <= 25:
		return 2 * trigTimeouts[e], 2 * trigTimeouts[e]
	}
	return 500000000, 500000000
}

// TDCInsert is ACQ 0x039 bits 3:0 (spec 04 §8.2 step 8).
func TDCInsert(m, e int) uint16 {
	if m == 0 {
		if e >= 1 && e <= 6 {
			return [...]uint16{8, 7, 0, 5, 2, 2}[e-1]
		}
		return 4
	}
	if e >= 1 && e <= 7 {
		return []uint16{8, 7, 0, 5, 2, 3, 4}[e-1]
	}
	return 6
}

// FIRSelector is ACQ 0x1FF for a bandwidth (MHz) and rate (spec 04 §6).
func FIRSelector(bwMHz int, rateMSa float64) uint16 {
	switch bwMHz {
	case 70:
		if rateMSa == 2000 {
			return 5
		}
		return 3
	case 100:
		if rateMSa == 2000 {
			return 4
		}
		return 2
	case 200:
		if rateMSa == 2000 || rateMSa == 1000 {
			return 0
		}
		return 1
	}
	return 0
}

// MaxSegments is the sequence capacity M of h (spec 04 §8.2 step 6).
func (h Set) MaxSegments() int {
	v := h.SegmentValue()
	if v == 1 {
		return 1
	}
	return v - 1
}
