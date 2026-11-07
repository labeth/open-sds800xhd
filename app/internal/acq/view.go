package acq

import (
	"math"

	"github.com/labeth/open-sds800xhd/app/internal/mmio"
	"github.com/labeth/open-sds800xhd/app/internal/timebase"
)

// View is what the map engine draws beside the acquisition (spec 07 §7):
// which acquired channels are shown and in which priority, the zoom window
// and the XY display.
type View struct {
	Hidden uint8 // acquired channels not drawn, one bit per channel
	// Selected is the selection order, newest first, as channel + 1 (0 ends
	// the list); empty: the order the channels were switched on.
	Selected   [4]int8
	Zoom       bool
	ZoomTdivS  float64 // zoom window time/div
	ZoomDelayS float64 // zoom window position: its centre's time after the main centre
	// ZoomGain is each channel's vertical gain in the zoom window (0: 1)
	// and ZoomCentreDiv the main-screen position, divisions above the
	// centre, that the zoom window centres on.
	ZoomGain      [4]float64
	ZoomCentreDiv [4]float64
	XY            bool // XY display: C1 against C2, C3 against C4
	YTXY          bool // with XY: the YT display beside it (MAP 0x192 = 0x10), else XY only
}

// xyMode is the XY mode value of spec 04 §10: 0 off, 1 XY only, 0x10 YT+XY.
func (v View) xyMode() uint16 {
	switch {
	case !v.XY:
		return 0
	case v.YTXY:
		return 0x10
	}
	return 1
}

// xyOnly reports whether the XY-only display is on.
func (v View) xyOnly() bool { return v.XY && !v.YTXY }

// drawList is the channels the map engine renders: on and not hidden.
func (e *Engine) drawList(s *Settings) []bool {
	on := e.onList(s)
	for c := range on {
		if s.View.Hidden&(1<<c) != 0 {
			on[c] = false
		}
	}
	return on
}

// order is the priority order of the channels: the view's selection when it
// gives one, else the switch-on order.
func (e *Engine) order(s *Settings) []int {
	var r []int
	for _, c := range s.View.Selected {
		if c > 0 {
			r = append(r, int(c)-1)
		}
	}
	if len(r) == 0 {
		return e.recency
	}
	for _, c := range e.recency {
		found := false
		for _, x := range r {
			found = found || x == c
		}
		if !found {
			r = append(r, c)
		}
	}
	return r
}

// XYPairs is ACQ 0x199 for a channel mask (spec 04 §10): 1 when C1 or C2
// is on and neither C3 nor C4, 4 for C3/C4 only, 5 for both pairs.
func XYPairs(mask int) uint16 {
	var v uint16
	if mask&3 != 0 {
		v |= 1
	}
	if mask&0xC != 0 {
		v |= 4
	}
	return v
}

// writeXY is the XY record set of spec 04 §10 with XY mode x: after the
// §8–§9 writes of a bank for the XY-only display, alone in banks 4 + n for
// the YT+XY display (its section count in 0x09F after 0x095 ← 1; static
// only).
func writeXY(t *mmio.Tx, h timebase.Set, x uint16) {
	sxy := 2 * h.S
	var P, parts int
	if sxy > 6250 {
		P = timebase.Sections(int(math.Floor(float64(sxy)/1000 + 0.5)))
		parts = int(sxy) / P
	} else {
		P, parts = int(sxy), 1
	}
	t.Set(0x198, x)
	t.Set(0x005, 5)
	if x == 0x10 {
		t.Set(0x095, 1)
		t.Set(0x09F, uint16(parts))
	} else {
		t.Set(0x098, uint16(parts))
	}
	t.Set(0x10E, uint16(h.Delta/4+(128+P)/4)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x092, uint16((128+P)/4))           //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x094, uint16(P/4))                 //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x083, 1)
	t.Set(0x0B6, 0)
	for _, r := range []int{0x086, 0x085, 0x090, 0x0CE} {
		t.Set(r, 0)
	}
	t.Set(0x0A0, 1)
}

// finishXY is the rest of an XY switch (spec 04 §10, spec 07 §7.7): the
// channel pairs, the XY mode in both planes, two picture clears and the
// soft reset, the pixel mapping. The YT+XY display keeps the YT pixel
// mapping (which mapping it uses is not known: the variant is static only).
func (e *Engine) finishXY(s *Settings) {
	x := s.View.xyMode()
	e.acq.Set(0x199, XYPairs(s.Mask(e.nch)))
	e.acq.Set(0x198, x)
	e.Map.XYMode(x)
	e.Map.ClearBlock()
	e.softReset()
	e.Map.XYMapping(s.View.xyOnly())
}

// Zoom is the zoom window's horizontal set (spec 04 §11) for a main set h.
type Zoom struct {
	Sz    float32 // zoom screen samples S/k
	N     int     // samples covered, n_z
	I     int     // interpolation I_z
	PPP   int
	P     int // per-section count
	Parts int
	Enum  int // the zoom time/div's enum
	ok    bool
}

// ZoomOf computes the zoom set for main set h, main time/div tMain and
// zoom time/div tZoom; ok is false for fewer than 4 samples.
func ZoomOf(h timebase.Set, tMain, tZoom float64, minEnum int) (Zoom, bool) {
	k := tMain / tZoom
	z := Zoom{Sz: float32(float64(h.S) / k), Enum: timebase.EnumOf(tZoom, minEnum)}
	if z.Sz < 4 {
		return z, false
	}
	z.N = int(math.Floor(float64(min(z.Sz, h.S)) + 0.5))
	if z.Sz == 12 || z.Sz == 13 {
		z.I = 80
	} else {
		z.I = max(1, timebase.InterpFactor(z.Sz))
	}
	ppp := float64(z.I*z.N) / 1000
	if k < 1 {
		ppp /= k
	}
	z.PPP = int(math.Floor(ppp + 0.5))
	z.PPP = max(1, min(z.PPP, 100_000_000))
	if z.N > 6249 && z.N != 6250 {
		z.P = timebase.Sections(z.PPP)
		z.Parts = z.N / z.P
	} else {
		z.P, z.Parts = z.N, 1
	}
	z.ok = true
	return z, true
}

// writeZoomBank is the zoom set of spec 04 §11 in bank 4 + n.
func writeZoomBank(t *mmio.Tx, h timebase.Set, z Zoom, in timebase.PosInput, zoomDelayUs, gain, centreDiv float64) {
	// Down-sampling and speed for the rate (as the main set's step 1).
	t.Field(0x022, 14, 12, h.SpeedCode())
	t.Set32(0x05D, 0x05E, h.DownRatio(), 0xFFF)
	v := uint16(h.Decim) //nolint:gosec // G115: the decimation code is a register field
	if h.Peak && h.Decim != 0 {
		v |= 1 << 8
	}
	t.Set(0x004, v)
	t.Field(0x022, 9, 8, uint16(h.Switch)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set32(0x04B, 0x04C, h.DownDivisor(), 0xFFF)
	t.Set(0x083, uint16(z.I)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	if z.I < 2 {
		for _, r := range []int{0x0B6, 0x119, 0x086, 0x085, 0x090, 0x0CE} {
			t.Set(r, 0)
		}
		t.Set(0x0A0, 1)
	} else {
		st, en := timebase.InterpWindow(z.I)
		t.Set(0x0B6, 1)
		t.Set(0x119, uint16(z.I))   //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		t.Set(0x086, uint16(z.I))   //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		t.Set(0x085, uint16(4*z.I)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		t.Set(0x090, uint16(st))    //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		t.Set(0x0CE, uint16(en))    //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		t.Set(0x0A0, 4)
	}
	t.Set(0x097, 1)
	t.Set(0x099, uint16(z.Parts)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	l0 := (128 + z.P) / 4
	t.Set(0x10E, uint16(h.Delta/4+l0)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x092, uint16(l0))           //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x094, uint16(z.I*z.P/4))    //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Field(0x039, 3, 0, timebase.TDCInsert(h.M, z.Enum))

	// Zoom record start: the §9 rule with S = n_z, I = I_z and the zoom
	// window's offset from the main window.
	st := zoomStart(h, z, in, zoomDelayUs)
	t.Set(0x005, 5)
	t.Set32(0x09A, 0x09B, uint32(st.start), 0xFFFF) //nolint:gosec // G115: the register pair takes the value's low 32 bits
	t.Set(0x08C, uint16(2*st.start))                //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x09E, uint16(st.r))                      //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x08E, uint16(st.a1))                     //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x08A, uint16(st.a1+1))                   //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits

	writeZoomResize(t, gain, centreDiv)
	hz := h
	hz.I = z.I
	writeSearch(t, hz)
}

// writeZoomResize is the resize of a zoom bank (spec 02 §4.2): scale
// round(16384·g) for the zoom gain g, and the offset that puts the zoom
// centre (centreDiv divisions above the main screen's centre, 480 codes per
// division) on the screen centre, by the mapping of the stored-record resize
// (spec 04 §12): row code = 0x800 + g·(code − 0x800) + offset. Whether the
// zoom offset and limits are static only is UNVERIFIED; the limits stay
// the screen limits.
func writeZoomResize(t *mmio.Tx, gain, centreDiv float64) {
	if gain <= 0 {
		gain = 1
	}
	off := int32(math.Floor(-gain * centreDiv * 480))
	t.Set32(0x1DF, 0x1E0, uint32(off), 0xFFFF) //nolint:gosec // G115: the register pair takes the value's low 32 bits
	t.Set32(0x1E1, 0x1E2, uint32(math.Floor(16384*gain+0.5)), 0xFFFF)
	t.Set(0x1E3, 0x080)
	t.Set(0x1E4, 0xF7F)
}

type recordStart struct{ start, r, a1 int }

// zoomStart is the record start of spec 04 §9 for the zoom window:
// X = (Δ·D − n_z)·I_z/2 + R·(τ_z − τ)·I_z.
func zoomStart(h timebase.Set, z Zoom, in timebase.PosInput, zoomDelayUs float64) recordStart {
	const a, b = 0x40, 4
	I := float64(z.I)
	dI := h.Delta * z.I
	X := (float64(h.Delta*h.D)-float64(z.N))*I/2 + h.R*(zoomDelayUs-in.DelayUs)*I
	s := uint32(int64(math.Floor(X - a*I - 32*I - I*in.SkewUs*h.R))) //nolint:gosec // G115: the record start is a 32-bit register value; spec 04 §9 wraps it the same way
	start := int(s) / dI
	rem := int(s) - start*dI
	r := rem / (z.I * b)
	k := rem - r*z.I*b
	a1 := a * z.I / b
	if k != 0 {
		a1 += z.I
	}
	if r < 6 && int(s) >= dI {
		start--
		r += h.Delta / b
	}
	return recordStart{start, r, a1}
}

// zoomPass writes the zoom banks and the map zoom sets (spec 04 §11, spec
// 07 §7.8), after the main pass.
func (e *Engine) zoomPass(s *Settings, h timebase.Set) {
	z, ok := ZoomOf(h, h.TimeDiv, s.View.ZoomTdivS, e.minEnum)
	if !ok {
		return
	}
	for n := range e.nch {
		in := e.posInput(s, n)
		e.bank(4+n, func(t *mmio.Tx) {
			writeZoomBank(t, h, z, in, (s.DelayS+s.View.ZoomDelayS)*1e6, s.View.ZoomGain[n], s.View.ZoomCentreDiv[n])
		})
	}
	e.Map.ZoomSets(h.S, z.Sz, z.I, int(s.Trig.Slope))
}

// applyView brings the zoom window to s after the main pass (passRan says
// whether it ran, and so rewrote the main map sets).
func (e *Engine) applyView(s, old *Settings, h timebase.Set, passRan bool) {
	v, ov := s.View, old.View
	if v.Zoom != ov.Zoom {
		e.Map.SetZoom(v.Zoom)
		if !passRan {
			e.Map.TimebaseSets(h) // the main sets' code window follows the zoom
		}
	}
	if v.Zoom && (passRan || v.Zoom != ov.Zoom || v.ZoomTdivS != ov.ZoomTdivS || v.ZoomDelayS != ov.ZoomDelayS ||
		v.ZoomGain != ov.ZoomGain || v.ZoomCentreDiv != ov.ZoomCentreDiv) {
		e.zoomPass(s, h)
	}
	if v.xyMode() != ov.xyMode() {
		e.finishXY(s)
	}
}
