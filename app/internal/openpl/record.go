package openpl

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"time"
)

// Reduction modes of the decimator (ACQ_MODE, spec 20 §5.3).
const (
	Pick = 0
	Box  = 1
	Peak = 2
)

// Record is one acquisition's configuration (the ACQ block, spec 20 §6).
type Record struct {
	Route    int   // 0: one channel at 2 GSa/s, 1: two at 1 GSa/s, 2: four at 500 MSa/s
	Mask     uint8 // channels recorded
	Decim    int   // D, 1 … 2^28 − 1
	Mode     int   // Pick, Box, Peak
	Pre      int   // words before the trigger word
	Post     int   // words after it
	Segments int
	Groups   int // 32-word groups per channel ring
	Invert   uint8
	GndSub   uint8
	GndCode  [4]uint16
}

// RouteFor is the route of a channel mask (spec 20 §5.2): one channel at
// 2 GSa/s, two at 1 GSa/s, three or four at 500 MSa/s.
func RouteFor(mask uint8) int {
	switch n := bits.OnesCount8(mask & 15); {
	case n <= 1:
		return 0
	case n == 2:
		return 1
	}
	return 2
}

// TicksPerLane is the sample period of a route in ticks.
func TicksPerLane(route int) int { return 1 << route }

// RouteRate is a route's sample rate per channel, Sa/s.
func RouteRate(route int) float64 { return 2e9 / float64(TicksPerLane(route)) }

// Words is the record's length per segment.
func (r Record) Words() int { return r.Pre + r.Post + 1 }

// RingWords is a channel ring's length.
func (r Record) RingWords() int { return 32 * r.Groups }

// Sized returns r with Groups covering the record (with a group to spare,
// so the post-trigger end never reaches the pre-trigger start), or an error
// when the rings do not fit the record region.
func (r Record) Sized() (Record, error) {
	if r.Segments < 1 {
		r.Segments = 1
	}
	r.Groups = (r.Words()+31)/32 + 1
	nch := max(1, bits.OnesCount8(r.Mask&15))
	if need := r.Groups * 48 * nch * r.Segments; need > RecBytes {
		return r, fmt.Errorf("%d words × %d channels × %d segments need %d MiB of record memory; %d MiB are reserved",
			r.Words(), nch, r.Segments, need>>20, RecBytes>>20)
	}
	if r.Decim < 1 || r.Decim >= 1<<28 {
		return r, fmt.Errorf("decimation %d: 1 … 2^28 − 1", r.Decim)
	}
	return r, nil
}

// Commit writes the record and trigger configuration and the COMMIT strobe
// (spec 20 §3.1: configuration is taken while the acquisition is stopped).
func (p *PL) Commit(r Record, t Trig) {
	p.writeRecord(r)
	p.writeTrig(t)
	p.r.Write32(ACQCtrl, 1<<4)
}

func (p *PL) writeRecord(r Record) {
	w := p.r.Write32
	w(ACQRoute, uint32(r.Route&3)|uint32(r.Mask&15)<<4)
	w(ACQDecim, uint32(r.Decim)) //nolint:gosec // G115: checked by Sized
	w(ACQMode, uint32(r.Mode&3))
	w(ACQInvert, uint32(r.Invert&15))
	w(ACQGndsub, uint32(r.GndSub&15))
	w(ACQGndcode01, uint32(r.GndCode[0]&0xFFF)|uint32(r.GndCode[1]&0xFFF)<<16)
	w(ACQGndcode23, uint32(r.GndCode[2]&0xFFF)|uint32(r.GndCode[3]&0xFFF)<<16)
	w(ACQPre, uint32(r.Pre))                   //nolint:gosec // G115: checked by Sized
	w(ACQPost, uint32(r.Post))                 //nolint:gosec // G115: checked by Sized
	w(ACQSegments, uint32(max(1, r.Segments))) //nolint:gosec // G115: segments ≤ 1024
	w(ACQRecBase, RecBase)
	w(ACQRecGroups, uint32(r.Groups)) //nolint:gosec // G115: checked by Sized
}

// Arm starts an acquisition (ARM implies COMMIT) with the acq_done
// interrupt cause enabled.
func (p *PL) Arm() {
	p.r.Write32(SysIRQEn, p.r.Read32(SysIRQEn)|1)
	p.r.Write32(ACQCtrl, 1)
}

// Stop is the STOP strobe: a pre-trigger is abandoned, a post-trigger
// finishes.
func (p *PL) Stop() { p.r.Write32(ACQCtrl, 1<<1) }

// Force is the FORCE strobe: one trigger as soon as the pre-trigger is full.
func (p *PL) Force() { p.r.Write32(ACQCtrl, 1<<2) }

// Clear returns the acquisition to IDLE.
func (p *PL) Clear() { p.r.Write32(ACQCtrl, 1<<3) }

// The acquisition states (ACQ_STATUS.state).
const (
	Idle  = 0
	Pre   = 1
	Armed = 2
	Post  = 3
	Done  = 4
)

// Status is ACQ_STATUS and the record's trigger.
type Status struct {
	State     int
	Triggered bool
	Auto      bool
	Forced    bool
	Overflow  bool
	Stopping  bool
	Segment   int
	TimeTicks uint64 // the trigger's time in ticks since the commit
	Pos       int    // ring word holding the trigger
	Phase     int    // ticks from that word's first sample to the trigger sample
	Frac      int    // sixteenths of a sample, 0 … 16: the crossing lies Frac/16 after the sample before the trigger sample
	FracOK    bool
	Words     int // words written to the last segment
	Lost      uint32
}

// State reads ACQ_STATUS alone.
func (p *PL) State() int { return int(p.r.Read32(ACQStatus) & 7) }

// ReadStatus reads the status and the trigger registers.
func (p *PL) ReadStatus() Status {
	s := p.r.Read32(ACQStatus)
	st := Status{
		State: int(s & 7), Triggered: s>>4&1 == 1, Auto: s>>5&1 == 1, Forced: s>>6&1 == 1,
		Overflow: s>>7&1 == 1, Stopping: s>>8&1 == 1, Segment: int(s >> 16),
	}
	st.TimeTicks = uint64(p.r.Read32(ACQTrigTimeHi)&0xFFFF)<<32 | uint64(p.r.Read32(ACQTrigTimeLo))
	st.Pos = int(p.r.Read32(ACQTrigPos))
	st.Phase = int(p.r.Read32(ACQTrigPhase))
	f := p.r.Read32(ACQTrigFrac)
	st.Frac, st.FracOK = int(f&31), f>>8&1 == 1
	st.Words = int(p.r.Read32(ACQWords))
	st.Lost = p.r.Read32(ACQRecLost)
	return st
}

// TriggerWord is the trigger's position in record words from the trigger
// word's start: (Phase/tpl − 1 + Frac/16)/D when the fraction is valid (the
// crossing lies between the trigger sample and the one before it), else
// Phase/tpl/D (the trigger sample itself).
func (s Status) TriggerWord(route, decim int) float64 {
	samples := float64(s.Phase) / float64(TicksPerLane(route))
	if s.FracOK {
		samples += float64(s.Frac)/16 - 1
	}
	return samples / float64(max(1, decim))
}

// Segment is one segment's entry of the per-segment table.
type Segment struct {
	TimeTicks uint64
	Pos       int
	Frac      int
	Auto      bool
	Forced    bool
}

// ReadSegment reads segment k of the table.
func (p *PL) ReadSegment(k int) Segment {
	p.r.Write32(ACQSegSel, uint32(k&0x3FF))
	f := p.r.Read32(ACQSegFlags)
	return Segment{
		TimeTicks: uint64(p.r.Read32(ACQSegTimeHi)&0xFFFF)<<32 | uint64(p.r.Read32(ACQSegTimeLo)),
		Pos:       int(p.r.Read32(ACQSegPos)), Frac: int(f & 31), Auto: f>>8&1 == 1, Forced: f>>9&1 == 1,
	}
}

// The readout reductions (READOUT_MODE, spec 20 §6.4).
const (
	ReadWords  = 0
	ReadNth    = 1
	ReadMinMax = 2
	ReadSum    = 3
)

// Transferred is the AXI DMA path (acq.DMA): one block of n bytes, started
// by start, copied into dst.
type Transferred interface {
	Transfer(dst []byte, n int, start func()) error
}

// ReadSpec is one readout window: count ring words of channel ch in
// segment seg from ring word first, reduced by mode over buckets of k.
type ReadSpec struct {
	Ch, Seg, First, Count, Mode, Bucket int
}

// OutWords is how many 16-bit words a readout delivers.
func (r ReadSpec) OutWords() int {
	k := max(1, r.Bucket)
	b := (r.Count + k - 1) / k
	switch r.Mode {
	case ReadNth:
		return b
	case ReadMinMax, ReadSum:
		return 2 * b
	}
	return r.Count
}

// ErrReadout is a readout the engine refused or did not finish.
var ErrReadout = errors.New("readout")

// Read runs one readout window through the DMA into dst (OutWords words).
// Windows longer than one DMA block are split at bucket boundaries.
func (p *PL) Read(d Transferred, r ReadSpec, ringWords int, dst []uint16, maxBlock int) error {
	k := max(1, r.Bucket)
	per := 2
	if r.Mode == ReadMinMax || r.Mode == ReadSum {
		per = 4
	}
	if r.Mode == ReadWords {
		k = 1
	}
	chunk := max(k, (maxBlock/per/k)*k) // input words per block, whole buckets
	buf := make([]byte, 0)
	out := 0
	for done := 0; done < r.Count; done += chunk {
		n := min(chunk, r.Count-done)
		part := ReadSpec{Ch: r.Ch, Seg: r.Seg, First: (r.First + done) % ringWords, Count: n, Mode: r.Mode, Bucket: r.Bucket}
		bytes := 2 * part.OutWords()
		if cap(buf) < bytes {
			buf = make([]byte, bytes)
		}
		buf = buf[:bytes]
		err := d.Transfer(buf, bytes, func() {
			p.r.Write32(ReadoutSrc, uint32(part.Ch&3)|uint32(part.Seg&0x3FF)<<16)
			p.r.Write32(ReadoutStart, uint32(part.First))                                //nolint:gosec // G115: a ring word
			p.r.Write32(ReadoutCount, uint32(part.Count))                                //nolint:gosec // G115: at most one block
			p.r.Write32(ReadoutMode, uint32(part.Mode&3)|uint32(max(1, part.Bucket))<<8) //nolint:gosec // G115: bucket < 2^24
			p.r.Write32(ReadoutCtrl, 1)
		})
		if err != nil {
			return fmt.Errorf("%w: %w", ErrReadout, err)
		}
		if s := p.r.Read32(ReadoutStatus); s&2 != 0 {
			return fmt.Errorf("%w: the engine refused the window (ring word %d of %d)", ErrReadout, part.First, ringWords)
		}
		for i := 0; i+1 < len(buf) && out < len(dst); i += 2 {
			dst[out] = binary.LittleEndian.Uint16(buf[i:])
			out++
		}
	}
	return nil
}

// WaitReadout waits for the readout engine to go idle (a readout into the
// stacker has no DMA completion).
func (p *PL) WaitReadout(timeout time.Duration) bool {
	end := time.Now().Add(timeout)
	for p.r.Read32(ReadoutStatus)&1 != 0 {
		if time.Now().After(end) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
	return true
}

// StartReadout starts a readout without a DMA (into the stacker).
func (p *PL) StartReadout(r ReadSpec) {
	p.r.Write32(ReadoutSrc, uint32(r.Ch&3)|uint32(r.Seg&0x3FF)<<16)
	p.r.Write32(ReadoutStart, uint32(r.First))                              //nolint:gosec // G115: a ring word
	p.r.Write32(ReadoutCount, uint32(r.Count))                              //nolint:gosec // G115: a window
	p.r.Write32(ReadoutMode, uint32(ReadWords)|uint32(max(1, r.Bucket))<<8) //nolint:gosec // G115: bucket < 2^24
	p.r.Write32(ReadoutCtrl, 1)
}
