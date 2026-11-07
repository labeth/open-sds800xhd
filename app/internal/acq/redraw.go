package acq

import (
	"errors"
	"math"
	"time"

	"github.com/labeth/open-sds800xhd/app/internal/frontend"
	"github.com/labeth/open-sds800xhd/app/internal/mmio"
	"github.com/labeth/open-sds800xhd/app/internal/timebase"
)

// ErrTooFewPoints is a redraw at a time/div where fewer than 4 of the
// record's samples span the screen (spec 04 §12): nothing is written.
var ErrTooFewPoints = errors.New("fewer than 4 record samples span the screen at this time/div")

// ErrRedrawXY is a redraw requested with the XY display on.
var ErrRedrawXY = errors.New("the held record of an XY display is not redrawn")

// Redraw is the stored-record redraw of spec 03 §3.7 while stopped (steps
// 1–6 and 8, no live pass, no arm): the PL map engine redraws the record
// held in the record memory at time/div tdivS and horizontal delay delayS,
// with the vertical settings ch of every channel against those the record
// was taken with. The banks then describe the record, not the live
// settings: the next Commit rewrites the whole live set.
func (e *Engine) Redraw(tdivS, delayS float64, ch [4]frontend.Channel) error {
	s := e.cur
	h := e.h
	if s.View.XY {
		return ErrRedrawXY
	}
	if s.Roll {
		return errors.New("a roll record is not redrawn")
	}
	in := timebase.StoredInput{TdivS: tdivS, DelayDeltaUs: (delayS - s.DelayS) * 1e6}
	if _, ok := h.StoredSet(in); !ok {
		return ErrTooFewPoints
	}
	a := e.acq
	// 1. Frame information.
	_ = a.HW(0x120)
	_ = a.HW(0x02F)
	// 2. Pass/fail output and mask hold.
	e.Map.PassFailHold(true)
	// A horizontal-delay change first writes the trigger position with the
	// record's timebase and the current delay (spec 03 §3.3 "While stopped").
	if delayS != s.DelayS {
		for n := range e.nch {
			pin := e.posInput(&s, n)
			pin.DelayUs = delayS * 1e6
			e.bank(n, func(t *mmio.Tx) { writePosition(t, h, pin) })
		}
	}
	// 3. Stored bank pass.
	var st timebase.Stored
	for n := range e.nch {
		in.SkewUs = e.posInput(&s, n).SkewUs
		st, _ = h.StoredSet(in)
		kappa, off := e.storedResize(n, ch[n])
		e.bank(n, func(t *mmio.Tx) { writeStored(t, h, st, kappa, off) })
	}
	// 4. Stored map sets and the DPO value.
	sm := timebase.StoredMapSet(h.S, st.Sv, st.Iv, h.TimeDiv, tdivS, s.DelayS*1e6, delayS*1e6)
	e.Map.StoredSets(sm, st.Iv, s.Display.Vectors, int(s.Trig.Slope))
	// 5. Draw stop.
	if !a.Poll(0x116, 1, true, 20000, time.Microsecond) {
		e.log("redraw: draw-stop not ready")
	}
	a.Set(0x115, 1)
	// 6. Trigger-interpolation read.
	v := a.Shadow(0x018)
	a.Set(0x018, v|0x2000, v)
	a.Poll(0x0A4, 1, true, 100000, time.Microsecond)
	_, _ = a.HW(0x0AA), a.HW(0x0A6)
	// 8. Release the hold; 0x115 stays 1 while stopped.
	e.Map.PassFailHold(false)
	e.redrawn = true
	return nil
}

// storedResize is the resize part of spec 04 §12 for channel n: κ = the
// record's V/div ÷ the current V/div, and the offset that moves the record's
// zero from where it was taken (y_r) to where it is now (y_c), in 12-bit
// screen codes (480 per division). Which per-channel record values κ, y_r
// and y_c are is UNVERIFIED.
func (e *Engine) storedResize(n int, now frontend.Channel) (kappa float64, off int32) {
	rec := e.cur.Ch[n]
	kappa = 1
	if now.VoltsDiv > 0 && rec.VoltsDiv > 0 {
		kappa = rec.VoltsDiv / now.VoltsDiv
	}
	yr := 0x800 + 8*e.FE.Derived(n).PosPx
	yc := 0x800 + 8*frontend.PositionPx(now)
	if now.Invert {
		return kappa, int32(math.Floor((0x800 - yc) - kappa*(yr-0x800)))
	}
	return kappa, int32(math.Floor((yc - 0x800) - kappa*(yr-0x800)))
}

// writeStored is the stored-record set of spec 04 §12 in one bank: the
// timebase part, the search part, roll off, the position part and the
// resize part.
func writeStored(t *mmio.Tx, h timebase.Set, st timebase.Stored, kappa float64, off int32) {
	// Timebase part: down-sampling and speed for the record's rate.
	t.Field(0x022, 14, 12, h.SpeedCode())
	t.Set32(0x05D, 0x05E, h.DownRatio(), 0xFFF)
	v := uint16(h.Decim) //nolint:gosec // G115: the decimation code is a register field
	if h.Peak && h.Decim != 0 {
		v |= 1 << 8
	}
	t.Set(0x004, v)
	t.Field(0x022, 9, 8, uint16(h.Switch)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set32(0x04B, 0x04C, h.DownDivisor(), 0xFFF)
	t.Set(0x04E, b2u(h.Decim != 0 || h.Switch != 0))
	t.Set(0x083, uint16(st.Iv)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	writeInterp(t, st.Iv)
	t.Set(0x005, 5)
	t.Set32(0x01A, 0x01B, uint32(h.D), 0xFFFF) //nolint:gosec // G115: the register pair takes the value's low 32 bits
	t.Set(0x096, 1)
	t.Set(0x098, uint16(st.Parts)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	l0 := (128 + st.P) / 4
	t.Set(0x092, uint16(l0))           //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x094, uint16(st.Iv*st.P/4)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x10E, uint16(h.Delta/4+l0)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	// Search part (§8.3 with I_v, without 0x03D), then stored search on.
	hv := h
	hv.I = st.Iv
	writeSearchSet(t, hv)
	t.Set(0x118, 1)
	t.Set(0x010, 0x8000)
	// Position part.
	t.Set(0x005, 5)
	t.Set32(0x09A, 0x09B, uint32(st.Start), 0xFFFF) //nolint:gosec // G115: the register pair takes the value's low 32 bits
	t.Set(0x08C, uint16(2*st.Start))                //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x09E, uint16(st.R))                      //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x08E, uint16(st.A1))                     //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x08A, uint16(st.A1+1))                   //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x0A7, uint16(st.A7))                     //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x0A8, uint16(st.A8))                     //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x0A9, uint16(st.A9))                     //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	// Resize part.
	t.Set32(0x1E1, 0x1E2, uint32(math.Floor(16384*kappa)), 0xFFFF)
	t.Set32(0x1DF, 0x1E0, uint32(off), 0xFFFF) //nolint:gosec // G115: the register pair takes the value's low 32 bits
	t.Set(0x1E4, 0xF7F)
	t.Set(0x1E3, 0x080)
}
