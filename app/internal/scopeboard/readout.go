package scopeboard

import (
	"context"
	"fmt"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-dso-core/sample"
	"github.com/labeth/open-sds800xhd/app/internal/acq"
	"github.com/labeth/open-sds800xhd/app/internal/timebase"
)

type readoutFacet Board

// Read limits: the PL reads one channel per pass (ACQ 0x0C1), so every
// window holds one channel (HAL v1 §4.2).
const (
	maxWindows = 16
	maxPoints  = 200_000_000 // the longest screen record (spec 04 §8.1)
)

// Read reads windows of the stored record (one or several segments of a
// sequence) with the readout of spec 03 §4.3. Each window is one channel;
// its span lies inside the screen record. A window asking for every sample
// (Want RawFullRate) of 2·M samples or more is a piece read; any other
// window of that size is compressed by the PL (c_f = 2r). Points below what
// the PL delivers are met by picking every k-th sample (RawDecimated); a
// window that wants full-rate samples it cannot get is refused. A read whose
// ctx ends stops before its next window.
func (r *readoutFacet) Read(ctx context.Context, req hal.ReadRequest, dst []*sample.Frame, yield func()) error {
	b := (*Board)(r)
	if err := ctx.Err(); err != nil {
		return err
	}
	b.reading = true
	defer func() { b.reading = false }()
	if b.open != nil && b.eng != nil {
		return b.openRead(ctx, req, dst, yield)
	}
	if b.eng == nil || !b.stored || (b.state != hal.Ready && b.state != hal.Idle) {
		return hal.Refuse("readout", "no stored record: arm and wait for one first")
	}
	if req.Record != (hal.RecordID{}) && req.Record != b.status.Record {
		return hal.Refuse("readout", "that record was overwritten by a later acquisition")
	}
	segs := req.Segments
	if segs == [2]int{} {
		segs = [2]int{0, 1}
	}
	n := b.eng.Settings().Segments
	if segs[0] < 0 || segs[1] <= segs[0] || segs[1] > max(1, n) {
		return hal.Refuse("segments", fmt.Sprintf("segments %d…%d: the record holds %d", segs[0], segs[1]-1, max(1, n)))
	}
	if len(req.Windows) == 0 || len(req.Windows) > maxWindows {
		return hal.Refuse("readout", fmt.Sprintf("1 to %d windows per read", maxWindows))
	}
	if want := len(req.Windows) * (segs[1] - segs[0]); len(dst) != want {
		return fmt.Errorf("readout: %d frames for %d windows × segments", len(dst), want)
	}
	plans := make([]windowPlan, len(req.Windows))
	for i, w := range req.Windows {
		p, err := b.planWindow(w)
		if err != nil {
			return err
		}
		plans[i] = p
	}
	prev := b.state
	b.state = hal.Reading
	defer func() { b.state = prev }()
	for s := segs[0]; s < segs[1]; s++ {
		sel, err := b.eng.BeginRead(s)
		if err != nil {
			return err
		}
		for i, p := range plans {
			f := dst[(s-segs[0])*len(plans)+i]
			err := ctx.Err()
			if err == nil {
				err = b.readWindow(sel, p, f, yield)
			}
			if err != nil {
				b.eng.EndRead(sel)
				b.errs.readouts++
				return err
			}
			f.Status = b.status
			if n > 1 {
				f.Flags |= sample.Segmented
			}
		}
		b.eng.EndRead(sel)
	}
	return nil
}

// windowPlan is a window checked against the stored record.
type windowPlan struct {
	w     hal.Window
	ch    int
	k, n  int  // screen samples
	piece bool // uncompressed piece read
}

func (b *Board) planWindow(w hal.Window) (windowPlan, error) {
	h := b.eng.Timebase()
	s := b.eng.Settings()
	if len(w.Channels) != 1 {
		return windowPlan{}, hal.Refuse("readout window", "one channel per window: the PL reads one channel per pass")
	}
	ch := w.Channels[0]
	if ch < 0 || ch >= b.id.Channels || !s.Ch[ch].On {
		return windowPlan{}, hal.Refuse("readout window", fmt.Sprintf("C%d was not acquired", ch+1))
	}
	peak := peakActive(h)
	switch w.Want { //nolint:exhaustive // default: every other sample form is refused
	case 0:
	case sample.RawFullRate, sample.RawDecimated:
		if peak {
			return windowPlan{}, hal.Refuse("readout window", fmt.Sprintf("the record holds peak-detect max/min pairs, not %v samples", w.Want))
		}
	case sample.PeakPairs:
		if !peak {
			return windowPlan{}, hal.Refuse("readout window", "the record holds plain samples: peak detect is off or has no effect at this rate (spec 04 §5.2)")
		}
	default:
		return windowPlan{}, hal.Refuse("readout window", fmt.Sprintf("this board delivers raw samples or peak pairs, not %v", w.Want))
	}
	S := int(h.S)
	trig := b.triggerSample()
	k, n := 0, S
	if w.Span > 0 {
		k = int(trig + float64(w.StartSample))
		n = int(w.Span)
	}
	if k < 0 || n <= 0 || k+n > S {
		return windowPlan{}, hal.Refuse("readout window", fmt.Sprintf(
			"samples %d…%d from the trigger lie outside the screen record (%d samples)", w.StartSample, w.StartSample+w.Span, S))
	}
	p := windowPlan{w: w, ch: ch, k: k, n: n}
	// Full-rate reads of a long record are piece reads (spec 03 §4.3: the
	// record holds 2·M samples or more).
	if S >= 2*timebase.PieceSize && (w.Want == sample.RawFullRate || w.Points >= n && n >= 2*timebase.PieceSize) {
		p.piece = true
	}
	if peak && n >= 2*timebase.PieceSize {
		// The PL's compressed read keeps every c_f-th word (c_f even), so
		// it would keep one word of every pair: long peak windows are read
		// in pieces and reduced here.
		p.piece = true
	}
	if h.Decim != 0 && w.Want == sample.RawFullRate {
		return windowPlan{}, hal.Refuse("readout window", "the acquisition is decimated in the PL at this time/div")
	}
	if !p.piece && n >= 2*timebase.PieceSize && w.Want == sample.RawFullRate {
		return windowPlan{}, hal.Refuse("readout window", "full-rate windows this long are read in pieces")
	}
	if w.Want == sample.RawFullRate && w.Points > 0 && w.Points < n {
		return windowPlan{}, hal.Refuse("readout window", fmt.Sprintf("%d full-rate samples do not fit %d points", n, w.Points))
	}
	return p, nil
}

func (b *Board) readWindow(sel acq.Selection, p windowPlan, f *sample.Frame, yield func()) error {
	h := b.eng.Timebase()
	var buf []uint16
	var tr *sample.Trace
	traces := f.Traces[:0]
	if cap(traces) > 0 {
		tr = &traces[:1][0]
		buf = tr.Codes16
	}
	win, err := b.eng.ReadWindow(sel, p.ch, p.k, p.n, p.piece, buf, yield)
	if err != nil {
		return err
	}
	if peakActive(h) {
		b.peakFrame(p, win, f, tr)
		return nil
	}
	words := win.Words
	pick := 1
	if p.w.Points > 0 && len(words) > p.w.Points {
		pick = (len(words) + p.w.Points - 1) / p.w.Points
		m := 0
		for i := 0; i < len(words); i += pick {
			words[m] = words[i]
			m++
		}
		words = words[:m]
	}
	factor := win.Factor * pick
	content := sample.RawFullRate
	if factor > 1 || h.Decim != 0 {
		content = sample.RawDecimated
	}
	*f = sample.Frame{
		Traces:    append(traces, sample.Trace{}),
		Content:   content,
		Flags:     sample.Ordered,
		CaptureS:  1 / (h.R * 1e6),
		AcqDecim:  1,
		Depth:     h.D * h.Delta,
		WinPoints: int(h.S) / factor,
		TriggerAt: win.TriggerAt / float64(pick),
		TrigKind:  b.trigKind(),
		Seq:       b.seq,
		ContentID: uint64(b.epoch)<<32 | b.seq,
		Status:    b.status,
		Snapshot:  sample.Settings{TdivS: h.TimeDiv, DisplayedS: h.TimeDiv, Norm: true},
	}
	f.SampleS = f.CaptureS * float64(factor)
	sc := b.eng.Scale(p.ch)
	f.Traces[0] = sample.Trace{
		Ch: p.ch, Format: format12, Codes16: words, Valid: len(words),
		Scale:       sample.Scale{PerCode: sc.PerCode, Zero: sc.Zero},
		CodesPerDiv: codesPerDiv,
	}
	return nil
}

// peakFrame fills f with the max/min pairs of a peak-detect window. The
// pair layout is the one spec 16 §5.3 gives the simulator: words 2j and
// 2j + 1 of the record are the maximum and the minimum over record samples
// 2j and 2j + 1. Spec 03 §5 leaves the PL's pair order UNVERIFIED; the
// stated order is used as is, not repaired. The transfer starts at an even
// record sample (its offset is in Δ units), so pairs start at even transfer
// indices. Points below the pair count are met by buckets: the maximum of
// the maxima and the minimum of the minima.
// Peak windows are never PL-compressed (planWindow), so the transfer holds
// every record word.
func (b *Board) peakFrame(p windowPlan, win acq.Window, f *sample.Frame, tr *sample.Trace) {
	h := b.eng.Timebase()
	// Whole pairs from the even index at or before the window's first
	// sample, as many as the window holds samples: a window starting on an
	// odd sample starts one sample early (TriggerAt says where).
	start := win.At &^ 1
	end := min(len(win.Buf), start+len(win.Words)&^1)
	var mx, mn []uint16
	if tr != nil {
		mx, mn = tr.Max16, tr.Min16
	}
	mx, mn, bucket := reducePairs(win.Buf[start:end], p.w.Points, mx, mn)
	n := len(mx)
	// A pair spans 2·bucket record samples; SampleS, TriggerAt and
	// WinPoints count codes, half a pair each (sample.PeakPairs).
	factor := bucket
	trig := (win.TriggerAt + float64(win.At-start)) / float64(factor)
	*f = sample.Frame{
		Traces:    append(f.Traces[:0], sample.Trace{}),
		Content:   sample.PeakPairs,
		CaptureS:  1 / (h.R * 1e6),
		AcqDecim:  1,
		Depth:     h.D * h.Delta,
		WinPoints: int(h.S) / factor,
		TriggerAt: trig,
		TrigKind:  b.trigKind(),
		Seq:       b.seq,
		ContentID: uint64(b.epoch)<<32 | b.seq,
		Status:    b.status,
		Snapshot:  sample.Settings{TdivS: h.TimeDiv, DisplayedS: h.TimeDiv, Norm: true},
	}
	f.SampleS = f.CaptureS * float64(factor)
	sc := b.eng.Scale(p.ch)
	// The core's PeakPairs content is each bucket's two extremes interleaved
	// in the codes, (min, max) and unflagged: the PL does not record which
	// came first. The pairs are already copied out of the transfer buffer
	// into mx and mn, so the buffer takes the interleaved codes. Max16 and
	// Min16 stay as the frame's display envelope.
	codes := win.Buf[:2*n]
	for i := range n {
		codes[2*i], codes[2*i+1] = mn[i], mx[i]
	}
	f.Flags.Set(sample.Envelope, true)
	f.Traces[0] = sample.Trace{
		Ch: p.ch, Format: format12, Max16: mx, Min16: mn, Valid: 2 * n,
		Codes16:     codes,
		Scale:       sample.Scale{PerCode: sc.PerCode, Zero: sc.Zero},
		CodesPerDiv: codesPerDiv,
	}
}

// reducePairs splits words into (maximum, minimum) pairs and, when there
// are more pairs than points (> 0), reduces each bucket of pairs to the
// maximum of its maxima and the minimum of its minima. mx and mn lend their
// storage.
func reducePairs(words []uint16, points int, mx, mn []uint16) (maxs, mins []uint16, bucket int) {
	pairs := len(words) / 2
	bucket = 1
	if points > 0 && pairs > points {
		bucket = (pairs + points - 1) / points
	}
	n := (pairs + bucket - 1) / bucket
	mx, mn = grow(mx, n), grow(mn, n)
	for j := range n {
		lo := 2 * j * bucket
		hi := min(2*pairs, lo+2*bucket)
		a, z := words[lo], words[lo+1]
		for i := lo + 2; i < hi; i += 2 {
			a, z = max(a, words[i]), min(z, words[i+1])
		}
		mx[j], mn[j] = a, z
	}
	return mx, mn, bucket
}

// grow returns s with length n, reusing its storage when it is long enough.
func grow(s []uint16, n int) []uint16 {
	if cap(s) < n {
		return make([]uint16, n)
	}
	return s[:n]
}

// Export reads one channel's whole screen record at the full rate through
// the piece path (spec 03 §4.3, long records). LONG.
func (r *readoutFacet) Export(ctx context.Context, rec hal.RecordID, ch int, dst *sample.Trace) error {
	b := (*Board)(r)
	if err := ctx.Err(); err != nil {
		return err
	}
	f := &sample.Frame{Traces: []sample.Trace{*dst}}
	err := r.Read(ctx, hal.ReadRequest{Record: rec, Windows: []hal.Window{{
		Role: hal.Main, Channels: []int{ch}, Want: sample.RawFullRate,
		StartSample: int64(-b.triggerSample()), Span: int64(b.eng.Timebase().S),
	}}}, []*sample.Frame{f}, nil)
	if err != nil {
		return err
	}
	*dst = f.Traces[0]
	return nil
}

func (r *readoutFacet) Limits() hal.ReadLimits {
	return hal.ReadLimits{MaxPoints: maxPoints, MaxWindows: maxWindows, MaxSegments: 80_000, ChannelsPerWindow: 1}
}

// triggerSample is the screen sample of the trigger, S/2 − 2 − R·τ (spec
// 03 §4.3, time axis).
func (b *Board) triggerSample() float64 {
	h := b.eng.Timebase()
	return float64(h.S)/2 - 2 - h.R*b.eng.Settings().DelayS*1e6
}

// trigKind is how the held record's trigger was found: a forced record is
// marked as such (the stage-1 PL has no flag that tells an auto timeout from
// a trigger, so only Force is known).
func (b *Board) trigKind() sample.TrigKind {
	if b.status.Forced {
		return sample.Forced
	}
	return sample.HardwareNode
}
