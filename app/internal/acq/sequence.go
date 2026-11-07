package acq

// Sequence (segmented) mode, spec 03 §6.2.

// sequenceCount writes the segment count N (also while sequence is off).
func (e *Engine) sequenceCount(n int) {
	a := e.acq
	a.Field(0x018, 9, 0, uint16(n)&0x3FF)    //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	a.Field(0x05B, 6, 0, uint16(n>>10)&0x7F) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
}

// sequenceSwitch turns sequence mode on or off with count n; the
// horizontal pass, soft reset and re-arm that follow are the caller's.
func (e *Engine) sequenceSwitch(on bool, n int) {
	a := e.acq
	if on {
		a.Field(0x018, 10, 10, 1)
		e.Map.SequenceZone(true, n)
		return
	}
	a.Field(0x018, 10, 10, 0)
	e.Map.SequenceHeld(false)
	e.Map.SequenceZone(false, n)
	a.Set(0x0F8, 0x101)
	a.Set(0x0F6, 0)
}

// SequenceDone reports whether all N segments are captured (0x02F bit 12)
// and the segments captured so far (spec 03 §4.2).
func (e *Engine) SequenceDone() (done bool, captured int) {
	a := e.acq
	return a.HW(0x02F)&(1<<12) != 0, int(a.HW(0x026)) | int(a.HW(0x05B)>>9&1)<<16
}

// SequenceComplete is the completion of a captured sequence (spec 03 §6.2):
// the held sequence is shown and the history window covers its N segments.
// The acquisition stays stopped.
func (e *Engine) SequenceComplete(n int) {
	a := e.acq
	e.Map.SequenceHeld(true)
	a.Set(0x0F6, uint16(n-1))                 //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	a.Field(0x0F7, 8, 8, uint16((n-1)>>16)&1) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	a.Set(0x0F8, 0x001)
}

// SegmentStamp selects segment k of the held sequence and reads its
// timestamp in µs (spec 03 §6.2, §6.3).
func (e *Engine) SegmentStamp(k int) uint64 {
	a := e.acq
	cnt := a.Shadow(0x018) & 0x7FF
	a.Set(0x018, 0x1000|cnt)
	a.Set(0x04D, uint16(k))               //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	a.Field(0x05B, 8, 8, uint16(k>>16)&1) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	_, stamp := e.selectSegment(cnt)
	a.Set(0x018, cnt)
	return stamp
}
