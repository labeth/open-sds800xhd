package timebase

import "math"

// The stored-record set (spec 04 §12) and its map side (spec 07 §7.8): the
// values that make the PL map engine redraw a record already in the record
// memory under the current time/div and position, without acquiring.

// ZoomInterp is the interpolation factor I(x) of spec 04 §11 and §12: the
// §5.4 factor, except that 12 or 13 samples exactly give 80; at least 1.
func ZoomInterp(s float32) int {
	if s == 12 || s == 13 {
		return 80
	}
	return max(1, InterpFactor(s))
}

// StoredInput is what a redraw of the record held under h needs beyond h.
type StoredInput struct {
	TdivS        float64 // current time/div t_c
	DelayDeltaUs float64 // τ_Δ: current horizontal delay − delay when the record was taken, µs
	SkewUs       float64 // channel skew σ of spec 04 §9, µs
	// Rho is ρ = I_v·μ_1 + ⌊(I_v/I_r)·μ_2⌋, the record's kept fine position.
	// Its source is UNVERIFIED (spec 04 §12); 0 when the frame's fine value
	// is 0.
	Rho int
	XY  bool // the XY display is on: I_v = 1
}

// Stored is the ACQ stored-record set of one bank (spec 04 §12).
type Stored struct {
	Sv     float32 // record samples spanning the current screen
	Iv, Ir int     // interpolation of the view and of the record
	N      int     // samples drawn, n
	PPP    int
	P      int // per-section count
	Parts  int
	// Position part.
	Start, R, A1 int
	A7, A8, A9   int
	S0, S1       int
}

// StoredSet computes the stored-record set of the record held under h for
// in; ok is false when fewer than 4 record samples span the current screen
// (nothing is written, spec 04 §12).
func (h Set) StoredSet(in StoredInput) (st Stored, ok bool) {
	const a, b = 0x40, 4
	st.Sv = float32(10 * float32(in.TdivS*1e6) * float32(h.R))
	if st.Sv < 4 {
		return st, false
	}
	st.Ir = ZoomInterp(h.S)
	st.Iv = ZoomInterp(st.Sv)
	if in.XY {
		st.Iv = 1
	}
	fx := h.TimeDiv / in.TdivS
	var q float64
	if h.S <= st.Sv {
		st.N = int(math.Floor(float64(h.S) + 0.5))
		q = float64(st.Iv) * float64(h.S) / 1000
	} else {
		st.N = int(math.Floor(float64(st.Sv) + 0.5))
		q = math.Floor(float64(st.Ir)*float64(h.S)/1000 + 0.5)
	}
	st.PPP = max(1, min(int(math.Floor(q/fx+0.5)), 100_000_000))
	if st.N > 6249 && st.N != 6250 {
		st.P = Sections(st.PPP)
		st.Parts = st.N / st.P
	} else {
		st.P, st.Parts = st.N, 1
	}

	Iv := float64(st.Iv)
	X := func(S float32) float64 { return (float64(h.Delta*h.D) - float64(S)) / 2 }
	g := int(roundHalfAway(h.R * in.DelayDeltaUs * Iv))
	d := int(math.Floor(h.R * in.SkewUs * Iv))
	st.S0 = int(math.Floor((X(h.S)-a-32)*Iv)) - in.Rho - d
	st.S1 = int(math.Floor((X(st.Sv)-a-32)*Iv)) - in.Rho + g - d
	s := max(st.S1, 0)
	if st.S0 >= 1 {
		s = max(st.S1, st.S0)
	}
	dI := h.Delta * st.Iv
	start := s / dI
	rem := s - start*dI
	r := rem / (st.Iv * b)
	k := rem - r*st.Iv*b
	if rem < st.Iv*b && start > 1 {
		start -= 2
		r += 2 * h.Delta / b
	}
	st.Start, st.R = start, r
	st.A1 = st.Iv * a / b
	if k != 0 {
		st.A1 += st.Iv
	}
	w := 0
	if k != 0 {
		w = st.Iv*b - k
	}
	if st.Iv == 1 {
		st.A7 = w
	} else {
		st.A8, st.A9 = w/b, w%b
	}
	return st, true
}

// StoredMap is the map engine's stored-record set (spec 07 §7.8).
type StoredMap struct {
	PPP, P, Seg int
	Start, End  int // waveform columns of the record's window
}

// StoredMapSet computes the map side of a redraw: record screen samples sr
// at time/div tr, the current screen's sv record samples at interpolation
// iv and time/div tc, and the horizontal delays (µs) when the record was
// taken and now.
func StoredMapSet(sr, sv float32, iv int, tr, tc, delayRUs, delayCUs float64) StoredMap {
	var m StoredMap
	fx := tr / tc
	ivf := float64(iv)
	var ns float64
	var ppp int
	if sr <= sv {
		ns = float64(sr)
		ppp = int(ivf * float64(sr) / (1000 * fx))
	} else {
		ns = float64(sv)
		ppp = int(ivf * float64(sv) / 1000)
	}
	if lim := ivf * float64(sr); float64(ppp) > lim {
		ppp = int(math.Floor(lim))
	}
	// The window: the record's span in waveform columns, moved by δ = x_c −
	// fx·x_r with x = −τ·100 columns per division / time/div.
	delta := -delayCUs*1e-6*100/tc + fx*delayRUs*1e-6*100/tr
	start := 500 - 500*fx + delta
	end := 500 + 500*fx + delta
	if ppp == 8 { // quick preview is off on this firmware
		start, end = 2*start, 2*end
	}
	m.Start = max(0, min(int(start), 1000))
	m.End = max(0, min(int(end), 1000))
	if m.Start == m.End && m.Start != 0 {
		m.End = m.Start + 1
	}
	switch {
	case ppp == 0:
		ppp = 1
	case ppp == 3:
		ppp = 2
	case ppp > 100_000_000:
		ppp = 100_000_000
	}
	n := int(ns)
	if n > 6249 && n != 6250 {
		m.P = Sections(ppp)
		m.Seg = n / m.P
	} else {
		m.P, m.Seg = n, 1
	}
	m.PPP = ppp
	m.P = (iv * m.P) & 0xFFFF
	return m
}
