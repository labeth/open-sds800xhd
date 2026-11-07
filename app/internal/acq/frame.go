package acq

import (
	"time"

	"github.com/labeth/open-sds800xhd/app/internal/frontend"
	"github.com/labeth/open-sds800xhd/app/internal/timebase"
)

// Content is what a frame's samples are (the content kinds of the core's
// HAL).
type Content uint8

// The content kinds this scope delivers.
const (
	RawFullRate  Content = iota + 1 // every sample at the acquisition rate
	RawDecimated                    // every Nth sample: PL decimation or readout compression
)

// CodeFormat says how a word maps to the converter: this scope's records
// hold 12-bit codes left-aligned in 16-bit words, mid-scale (screen centre)
// 0x8000 (spec 03 §5).
type CodeFormat struct {
	Bits, Shift uint8
	Min, Max    uint32
	Mid         uint32
}

// Format12 is the record format of the SDS800X HD.
var Format12 = CodeFormat{Bits: 12, Shift: 4, Min: 0, Max: 0xFFF0, Mid: 0x8000}

// Scale converts a word to volts at the probe tip: v = (word − Zero)·PerCode.
type Scale struct {
	PerCode float64
	Zero    float64
}

// Trace is one channel of a frame.
type Trace struct {
	Ch      int
	Words   []uint16 // screen record, from screen sample 0
	Volts   Scale    // snapshotted with the frame
	Channel frontend.Channel
}

// Frame is one read-out frame.
type Frame struct {
	Seq       uint64
	At        time.Time
	Content   Content
	Format    CodeFormat
	SampleS   float64 // interval between delivered words
	CaptureS  float64 // acquisition sample interval
	TriggerAt float64 // index of the trigger in Words
	FineValue int     // φ of spec 03 §4.3
	Traces    []Trace
}

// scaleOf is the word scale of channel ch with derived setup d.
func scaleOf(ch frontend.Channel, d frontend.Derived) Scale {
	// 480 codes per division, 16 word units per code; the trace's 0 V sits
	// PosPx rows (8 codes each) above the centre.
	per := ch.VoltsDiv / (480 * 16)
	zero := 0x8000 + d.PosPx*8*16
	if ch.Invert {
		return Scale{PerCode: -per, Zero: 0x8000 - d.PosPx*8*16}
	}
	return Scale{PerCode: per, Zero: zero}
}

// fineValue is φ of spec 03 §4.3 from the trigger-interpolation registers.
// While decimating its pre-trigger term is unknown and the fallback 6 is
// used.
func fineValue(h timebase.Set, a6, aa uint16) int {
	if h.Decim != 0 {
		return 6
	}
	ut := [3]int{8, 4, 2}[h.M]
	switch h.Switch {
	case 1:
		ut = 4
	case 2, 3:
		ut = 8
	}
	y := 4 - int(aa&0x1F) + 4*int(aa>>6)
	phiHW := float64(int(a6&7)*h.I*ut) + float64(h.I*y)/float64(h.I)
	return int(phiHW) + 2
}
