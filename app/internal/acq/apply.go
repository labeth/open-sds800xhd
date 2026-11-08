package acq

import (
	"github.com/labeth/open-sds800xhd/app/internal/timebase"
	"github.com/labeth/open-sds800xhd/app/internal/trigger"
)

// Commit brings the hardware to s in the order of spec 03 §3.3: stop, the
// channel enables and setups (with the ADC mode refresh when the channel set
// or the rate changes), the timebase pass, the trigger. The arm that follows
// (Arm with the start delay) is the caller's. This firmware does not redraw
// stored records, so a stopped screen keeps the last frame. It reports
// whether anything changed.
func (e *Engine) Commit(s Settings) bool {
	old := e.cur
	if s == old && !e.redrawn {
		return false
	}
	a := e.acq
	e.stop()
	e.RollStop()
	h := e.horizontal(&s)
	mask, oldMask := s.Mask(e.nch), old.Mask(e.nch)
	rateChanged := h.R != e.h.R
	e.cur = s

	if mask != oldMask {
		a.Set(0x0B1, uint16(mask)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		for c := range e.nch {
			if s.Ch[c].On && !old.Ch[c].On {
				e.selected(c)
			}
		}
	}
	if mask != oldMask || s.View.Hidden != old.View.Hidden || s.View.Selected != old.View.Selected {
		e.Map.Channels(e.drawList(&s), e.order(&s))
	}
	xyChanged := s.View.xyMode() != old.View.xyMode()
	e.Map.SetXY(s.View.xyOnly(), s.View.XY && s.View.YTXY)
	e.FE.SetRoute(h.Route, s.Ch[:e.nch])
	if mask != oldMask || rateChanged || xyChanged {
		e.adcMode(mask, h)
	}
	for c := range e.nch {
		if mask != oldMask || rateChanged || xyChanged || s.Ch[c] != old.Ch[c] {
			e.FE.SetupChannel(c, s.Ch[c], mask, h.R)
		}
	}
	e.writeInvert(&s, h)
	e.FE.ApplyGND(s.Ch[:e.nch], h.R)

	seqOn, wasOn := e.commitSequence(&s, &old)

	passRan := h != e.h || s.DelayS != old.DelayS || s.Ch != old.Ch || s.Trig.Coupling != old.Trig.Coupling ||
		s.Trig.Type != old.Trig.Type || xyChanged || e.redrawn
	if passRan {
		e.timebasePass(&s, h)
		e.redrawn = false
	}
	e.applyView(&s, &old, h, passRan)

	if s.Trig != old.Trig || s.Ch != old.Ch || s.Roll != old.Roll {
		if s.Trig.Sweep != old.Trig.Sweep {
			e.softReset() // a sweep-mode change resets once (spec 03 §3.5)
		}
		e.programTrigger(&s)
		if old.Trig.Sweep == trigger.Force && s.Trig.Sweep != trigger.Force && !passRan {
			// The Force row left both timeouts at 1 (spec 05 §7); the
			// timebase's own come back with the sweep that needs them.
			ntr, tr := timebase.Timeouts(h.Enum, autoOvertime)
			a.Set32(0x00C, 0x00D, ntr, 0xFFFF)
			a.Set32(0x02A, 0x02B, tr, 0xFFFF)
		}
	}
	if seqOn && (!wasOn || s.Segments != old.Segments) {
		e.softReset()
	}
	if s.Peak != old.Peak {
		// An acquisition-mode change runs the software reset twice, each
		// soft reset between a picture clear and a map soft reset (spec 03
		// §3.5); PEAK_EN itself went out with the bank pass (spec 04 §5.2).
		e.softReset()
		e.softReset()
	}
	if s.Display != old.Display {
		e.commitDisplay(&s, &old)
	}
	return true
}

// commitSequence writes the sequence (segmented) count and switch when they
// change, and reports whether sequence is on now and was on before.
func (e *Engine) commitSequence(s, old *Settings) (seqOn, wasOn bool) {
	seqOn, wasOn = s.Segments > 1, old.Segments > 1
	// The count setting is kept when sequence goes off (spec 03 §6.2).
	if seqOn && s.Segments != old.Segments {
		e.sequenceCount(s.Segments)
	}
	if seqOn != wasOn || seqOn && s.Segments != old.Segments {
		e.sequenceSwitch(seqOn, int(e.acq.Shadow(0x018)&0x3FF)|int(e.acq.Shadow(0x05B)&0x7F)<<10)
	}
	return seqOn, wasOn
}

// commitDisplay writes a changed appearance, display type and persistence.
func (e *Engine) commitDisplay(s, old *Settings) {
	e.Map.Appearance(s.Display)
	if s.Display.Vectors != old.Display.Vectors {
		e.Map.DisplayType(s.Display.Vectors)
	}
	if s.Display.Persistence != old.Display.Persistence {
		e.Map.SetPersistence(s.Display.Persistence)
	}
}

// selected moves channel c to the front of the selection order.
func (e *Engine) selected(c int) {
	r := []int{c}
	for _, x := range e.recency {
		if x != c {
			r = append(r, x)
		}
	}
	e.recency = r
}
