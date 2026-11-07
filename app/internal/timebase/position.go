package timebase

import "math"

// PosInput is the per-bank input of the position computation (spec 04 §9).
type PosInput struct {
	DelayUs  float64 // horizontal delay τ, µs
	SkewUs   float64 // channel skew σ, µs (negated channel + board skew)
	CoupHFLF bool    // trigger coupling HF or LF reject
}

// Position is the record start and trigger position (spec 04 §9) and the
// readout inputs it fixes (spec 03 §4.3).
type Position struct {
	Start, R, A1  int
	Pre, Delay    int
	A7, A8, A9    int
	P             float64 // first screen sample in interpolated-sample units
	PIdx          int     // ⌊P/(Δ·I)⌋
	TrigRemainder int     // f
}

func roundHalfAway(x float64) float64 {
	if x < 0 {
		return -math.Floor(-x + 0.5)
	}
	return math.Floor(x + 0.5)
}

// Position computes the position registers of h for in (spec 04 §9).
func (h Set) Position(in PosInput) Position {
	var o Position
	I := float64(h.I)
	const a, b = 0x40, 4
	dI := h.Delta * h.I
	X := (float64(h.Delta*h.D) - float64(h.S)) * I / 2
	s := uint32(int64(math.Floor(X - a*I - 32*I - I*in.SkewUs*h.R))) //nolint:gosec // G115: the record start is a 32-bit register value; spec 04 §9 wraps it the same way
	start := int(s) / dI
	rem := int(s) - start*dI
	r := rem / (h.I * b)
	k := rem - r*h.I*b
	a1 := a * h.I / b
	if k != 0 {
		a1 += h.I
	}
	if r < 6 && int(s) >= dI {
		start--
		r += h.Delta / b
	}
	o.Start, o.R, o.A1 = start, r, a1
	o.P = X - 32*I - I*in.SkewUs*h.R
	o.PIdx = int(math.Floor(o.P / float64(dI)))
	F := h.LatencyF()
	c := 0.0
	if h.M == 0 && in.CoupHFLF {
		c = 1.5
	}
	v := int(roundHalfAway((float64(h.Delta*h.D)/2-h.R*in.DelayUs+F)*I - I*c*h.R/1000))
	q := h.I * h.U
	var f int
	if v >= 0 {
		o.Pre = v / q
		f = v - o.Pre*q
	} else {
		o.Delay = (-v + q - 1) / q
		if mod := v % q; mod != 0 {
			f = mod + q
		}
	}
	o.TrigRemainder = f
	if h.I == 1 {
		o.A7 = f
		if k != 0 {
			o.A7 = f - k + 4
		}
	} else {
		g := f
		if k != 0 {
			g = f + 4*h.I - k
		}
		o.A8, o.A9 = g/4, g%4
	}
	return o
}

// LatencyF is the latency compensation term F of spec 04 §9.
func (h Set) LatencyF() float64 {
	switch {
	case h.Decim != 0:
		return 3*h.R/500 - 31
	case h.M == 0:
		return -32
	case h.M == 1:
		if h.Switch != 0 {
			return -32
		}
		return -28
	}
	if h.Switch != 0 {
		return -28
	}
	return -26
}

// MapSet is the map engine's timebase values for a screen sample count
// (spec 07 §7.8).
type MapSet struct {
	P, Seg, PPP int
}

// MapSetFor computes the map set for s screen samples at interpolation i.
func MapSetFor(s float32, i int) MapSet {
	var ms MapSet
	var p int
	if s > 6250 {
		ms.PPP = int(math.Floor(float64(float32(i)*s)/1000 + 0.5))
		n := Sections(ms.PPP)
		p = i * n
		ms.Seg = int(s) / n
	} else {
		p = int(math.Floor(float64(float32(i)*s) + 0.5))
		ms.Seg = 1
		ms.PPP = int(math.Floor(float64(p&0xFFFF)/1000 + 0.5))
	}
	switch {
	case ms.PPP == 0:
		ms.PPP = 1
	case ms.PPP == 3:
		ms.PPP = 2
	case ms.PPP > 100000000:
		ms.PPP = 100000000
	}
	ms.P = p & 0xFFFF
	return ms
}

// ReadTimes is MAP 0x19F bits 3:0 for a points-per-pixel value.
func ReadTimes(ppp int) int {
	switch {
	case ppp >= 10:
		return 1
	case ppp == 8:
		return 3
	case 10%ppp == 0:
		return 10 / ppp
	}
	return 10/ppp + 1
}

// MaskBase is MAP 0x1A1 for a points-per-pixel value.
func MaskBase(ppp int) int {
	switch ppp {
	case 1:
		return 0x20
	case 2:
		return 0x18
	case 4, 8:
		return 0x10
	case 5:
		return 8
	case 125:
		return 0x2A
	}
	return 0
}

// DPO computes the DPO triple (x, ud, a) of spec 07 §7.9 for DPO config c,
// points per pixel p, interpolation i and trigger slope.
func DPO(c, p, i, slope int, flag, status bool) (x, ud, a int, ok bool) {
	if c == 0 || p == 0 || i == 0 {
		return 0, 0, 0, false
	}
	if !flag {
		i = 1
	}
	n := p * c
	var k, y int
	if p < c {
		k = max(1, int(roundHalfAway(math.Sqrt(float64(p*p/160)))))
		t := n / 160 / k
		y = 1
		if t >= i {
			y = t / i
		}
	} else {
		y = max(1, int(roundHalfAway(math.Sqrt(float64(c*c/160)))))
		k = 1
		if n/160 >= y {
			k = n / 160 / y
		}
	}
	q := 128 * i / n
	a = q - 1
	if q >= 4 && !status {
		if a > 16 {
			a >>= 1
		}
	} else if q == 0 {
		a = 0
	}
	a = min(a, 16)
	ud = y - 1
	if slope == 2 && (y-1)%2 == 1 {
		ud = y
	}
	switch {
	case k > 512:
		x = k >> 5
	case k > 256:
		x = k >> 4
	case k > 128:
		x = k >> 3
	case k >= 5:
		x = k >> 2
	case k >= 3:
		x = k >> 1
	default:
		x = 1
	}
	return x, ud, a, true
}

// DPORecall is the run-start DPO triple of spec 07 §7.9 for the time/div
// enum offset from the minimum (table B; table A with two or more channels).
func DPORecall(enumFromMin int, multi bool) [3]int {
	e := max(0, min(enumFromMin, 20))
	if multi {
		return dpoTableA[e]
	}
	return dpoTableB[e]
}

var dpoTableB = [21][3]int{
	{1, 0, 0},
	{1, 0, 0},
	{1, 1, 0},
	{1, 2, 0},
	{1, 4, 0},
	{1, 6, 0},
	{1, 3, 0},
	{1, 6, 0},
	{1, 9, 0},
	{1, 15, 0},
	{1, 26, 0},
	{1, 10, 0},
	{2, 4, 0},
	{4, 2, 0},
	{13, 0, 0},
	{17, 0, 0},
	{15, 0, 0},
	{12, 0, 0},
	{15, 0, 0},
	{31, 0, 0},
	{31, 0, 0},
}

var dpoTableA = [21][3]int{
	{1, 0, 1},
	{1, 0, 0},
	{1, 0, 0},
	{1, 0, 0},
	{1, 0, 0},
	{1, 0, 0},
	{1, 0, 0},
	{1, 0, 0},
	{1, 0, 0},
	{1, 2, 0},
	{1, 4, 0},
	{1, 7, 0},
	{2, 2, 0},
	{2, 1, 0},
	{4, 0, 0},
	{6, 0, 0},
	{7, 0, 0},
	{3, 0, 0},
	{7, 0, 0},
	{15, 0, 0},
	{15, 0, 0},
}
