package acq

import (
	"errors"
	"time"
)

// stop is the stop request (spec 03 §3.4).
func (e *Engine) stop() {
	e.acq.Field(0x019, 3, 3, 1)
	e.armed = false
}

// softReset is the record-memory reset of spec 03 §3.5 between a map clear
// block and a map soft-reset pulse.
func (e *Engine) softReset() {
	e.Map.ClearBlock()
	e.Map.SoftReset(1)
	e.acq.Set(0x003, 1, 0)
	e.Map.SoftReset(0)
}

// startDelay programs the acquisition start delay (spec 03 §3.2 step 1)
// with the write rule of spec 02 §1.2.
func (e *Engine) startDelay() {
	d := 10
	if e.calJustRan {
		d = 110
	}
	now := time.Now()
	if d != e.delayD && (d > e.delayD || now.Sub(e.delayAt) > time.Duration(e.delayD)*time.Millisecond) {
		// Bit 12 is off in sequence mode (spec 03 §6.2: start delay 0).
		en := uint16(0x1000)
		if e.cur.Segments > 1 {
			en = 0
		}
		e.acq.Set(0x047, en|e.acq.Shadow(0x047)&0xFFF)
		e.acq.Set(0x047, en|uint16(d))
		e.delayD = d
	}
	e.delayAt = now
}

// arm is the arm sequence of spec 03 §3.2. withDelay is set for arms a
// settings change, RUN or the start-up requests: they run a pending ADC
// calibration and program the start delay; the per-frame re-arm does not.
func (e *Engine) arm(withDelay bool) bool {
	a := e.acq
	a.Set(0x115, 0)
	e.calJustRan = false
	if withDelay {
		if e.calPending {
			e.FE.ADC.InternalCal()
			e.calPending, e.calJustRan = false, true
		}
		e.startDelay()
		if e.h.Decim == 0 { // run-start DPO recall (spec 07 §7.9)
			e.Map.DPORecall(e.cur.Enum-e.minEnum, e.h.Route != 0)
		}
	}
	if !a.Poll(0x021, 1, true, 300000, time.Microsecond) {
		e.log("arm: acquisition not ready, not arming")
		return false
	}
	a.Set(0x003, 0x10)
	a.Set(0x019, 0)
	_ = a.HW(0x06F)
	a.Set(0x003, 0)
	if e.cur.Mask(e.nch) == 0 {
		a.Field(0x019, 3, 3, 1)
	}
	e.armed = true
	return true
}

// Arm arms the acquisition (spec 03 §3.2). withDelay is set for the arm
// after a settings change, RUN or the start-up: it runs a pending ADC
// calibration and programs the start delay. The per-frame re-arm (withDelay
// false) ends with the 0x02E toggle of the frame cycle (spec 03 §3.6).
func (e *Engine) Arm(withDelay bool) error {
	if !e.arm(withDelay) {
		return errNotReady
	}
	if !withDelay {
		e.acq.Set(0x02E, 1)
	}
	return nil
}

// errNotReady is an arm the PL did not accept (0x021 bit 0 stayed 0).
var errNotReady = errors.New("acquisition not ready, not armed")

// Armed reports whether an acquisition is armed.
func (e *Engine) Armed() bool { return e.armed }

// FrameReady reads the last-frame flag MAP 0x082 (spec 03 §3.6); one read.
func (e *Engine) FrameReady() bool { return e.mp.HW(0x082)&1 != 0 }

// Halt stops the acquisition after a frame and waits until the record is
// held and the map engine idle (spec 03 §3.6). halted is false when the stop
// did not complete (the frame is skipped and the owner re-arms); valid says
// whether the record has valid parameters (0x03E bit 0).
func (e *Engine) Halt() (halted, valid bool) {
	e.acq.Field(0x019, 3, 3, 1)
	e.armed = false
	if !e.acq.Poll(0x019, 1<<9, true, 30000, time.Microsecond) {
		return false, false
	}
	e.acq.Set(0x02E, 0)
	e.Map.WaitReady()
	_ = e.acq.HW(0x1C9)
	return true, e.acq.HW(0x03E)&1 != 0
}

// Release ends the hold of a read record before the next arm (spec 03
// §3.6: 0x115 ← 0).
func (e *Engine) Release() { e.acq.Set(0x115, 0) }

// Stop is the user's stop request.
func (e *Engine) Stop() { e.stop() }
