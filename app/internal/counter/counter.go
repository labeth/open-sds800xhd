// Package counter is the PL's frequency counter and totalizer (spec 15 §4,
// spec 05 §9). Both count the trigger comparator of the counter source
// (ACQ 0x00E[6:4], which follows the trigger source and is the trigger's
// to write).
//
// The counter is polled: Read never waits for a gate. It advances the
// measurement cycle of spec 15 §4 one step when a gate has completed and
// returns the latest frequency, so the owner can call it every frame.
package counter

import (
	"github.com/labeth/open-sds800xhd/app/internal/mmio"
)

// AutoGate is the auto-frequency gate count written to ACQ 0x067; the
// frequency computation uses the same value (spec 15 §4).
const AutoGate = 900

// Registers (spec 15 §4).
const (
	regPeriodN   = 0x063
	regPeriodN2  = 0x064
	regRestart   = 0x066
	regAutoGate  = 0x067
	regAutoLo    = 0x068
	regAutoHi    = 0x069
	regPeriodLo  = 0x06A
	regPeriodHi  = 0x06B
	regGateDone  = 0x06E
	regTotalizer = 0x111
	regTotal0    = 0x11B
)

// Gate-done bits of ACQ 0x06E.
const (
	autoDone   = 1 << 0
	periodDone = 1 << 4
)

// Totalizer control words (spec 15 §4): the default count setting, and the
// lock (bit 8), clear (bit 12) and shield (bit 13) bits.
const (
	totDefault = 0x0004
	totLock    = 1 << 8
	totClear   = 1 << 12
	totShield  = 1 << 13
)

// phase is where the measurement cycle is.
type phase int

const (
	idle phase = iota
	auto
	period
)

// Counter drives the counter registers of the ACQ plane. One goroutine (the
// owner) calls every method.
type Counter struct {
	acq   *mmio.Plane
	ph    phase
	n     int // period gate N of the running period measurement
	freq  float64
	valid bool
	total uint64
}

// New returns the counter on the ACQ plane; nothing is written until Start.
func New(acq *mmio.Plane) *Counter { return &Counter{acq: acq} }

// Start runs the counter's first writes (spec 05 §9: auto gate, restart,
// period gate 1) and the totalizer's start sequence (clear, default
// setting, two clears).
func (c *Counter) Start() {
	a := c.acq
	a.Set(regAutoGate, AutoGate)
	a.Set(regRestart, 0, 1)
	a.Set(regPeriodN, 1)
	a.Set(regPeriodN2, 0)
	c.ph = auto
	c.clearPulse(totDefault)
	a.Set(regTotalizer, totDefault)
	c.clearPulse(totDefault)
	c.clearPulse(totDefault)
}

func (c *Counter) clearPulse(w uint16) { c.acq.Set(regTotalizer, w|totClear, w) }

// PeriodGate is the period count N for an expected frequency (spec 15 §4).
func PeriodGate(f0 float64) int {
	switch {
	case f0 < 10:
		return 1
	case f0 < 100:
		return 5
	case f0 < 1e3:
		return 10
	case f0 < 1e4:
		return 100
	case f0 < 1e5:
		return 1000
	case f0 < 1e6:
		return 10000
	}
	return 1
}

// AutoFreq is the auto-mode frequency in Hz from the 32-bit count (spec 15
// §4: unsigned integer arithmetic, n the auto gate written).
func AutoFreq(count uint32, n int) float64 {
	return float64(uint64(count) * 1000 / uint64(n-1)) //nolint:gosec // G115: the gate n is at least 2 (spec 15 §4)
}

// PeriodFreq is the period-mode frequency in Hz: N periods counted in
// nanoseconds.
func PeriodFreq(n int, ns uint32) float64 {
	if ns == 0 {
		return 0
	}
	return float64(n) * 1e9 / float64(ns)
}

// Poll advances the measurement cycle: when the running gate is done it
// takes its result, and starts the next gate. It reports the latest
// frequency and whether one has been measured since Start.
func (c *Counter) Poll() (float64, bool) {
	a := c.acq
	switch c.ph {
	case auto:
		if a.HW(regGateDone)&autoDone == 0 {
			break
		}
		f := AutoFreq(uint32(a.HW(regAutoLo))|uint32(a.HW(regAutoHi))<<16, AutoGate)
		if f >= 1e6 || f == 0 {
			c.freq, c.valid = f, true
			a.Set(regRestart, 0, 1)
			break
		}
		c.n = PeriodGate(f)
		a.Set(regPeriodN, uint16(c.n)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		a.Set(regPeriodN2, 0)
		a.Set(regRestart, 0, 1)
		c.ph = period
	case period:
		if a.HW(regGateDone)&periodDone == 0 {
			break
		}
		f := PeriodFreq(c.n, uint32(a.HW(regPeriodLo))|uint32(a.HW(regPeriodHi))<<16)
		c.freq, c.valid = f, true
		a.Set(regRestart, 0, 1)
		c.ph = auto
	case idle:
		// not counting: nothing to read
	}
	return c.freq, c.valid
}

// Total latches the totalizer (lock pulse) and reads its 56-bit count, high
// word first (spec 15 §4).
func (c *Counter) Total() uint64 {
	a := c.acq
	a.Set(regTotalizer, totDefault|totLock, totDefault)
	r3, r2, r1, r0 := a.HW(regTotal0+3), a.HW(regTotal0+2), a.HW(regTotal0+1), a.HW(regTotal0)
	c.total = uint64(r3&0xFF)<<48 | uint64(r2)<<32 | uint64(r1)<<16 | uint64(r0)
	return c.total
}

// ResetTotal clears the totalizer with the count inhibited: shield on,
// clear, lock, shield off (spec 15 §4).
func (c *Counter) ResetTotal() {
	a := c.acq
	s := uint16(totDefault | totShield)
	a.Set(regTotalizer, s, s|totClear, s, s|totLock, s, totDefault)
}
