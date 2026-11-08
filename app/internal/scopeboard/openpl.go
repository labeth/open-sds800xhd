package scopeboard

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"math/bits"
	"time"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-dso-core/sample"
	"github.com/labeth/open-sds800xhd/app/internal/acq"
	"github.com/labeth/open-sds800xhd/app/internal/frontend"
	"github.com/labeth/open-sds800xhd/app/internal/openpl"
	"github.com/labeth/open-sds800xhd/app/internal/trigger"
)

// Stage 2: the open PL (spec 20 §12). The board picks the backend from the
// image it finds at bring-up: compat ACQ 0x002 bits 15:8 = 0x4F, then
// ID_MAGIC, the ABI major and the register manifest. The front end, the
// panel, the display's framebuffer and the AXI DMA keep their stage-1
// drivers; acquisition, trigger, readout, stream, segments, counter and the
// stacker go to the open plane. Setters stage as in stage 1; Arm commits.
type openBackend struct {
	pl     *openpl.PL
	rec    openpl.Record
	trig   openpl.Trig
	st     openpl.Status
	winS   int     // the screen record, words
	rateHz float64 // record words a second (per channel)
	stream *openStream
	ctrEv  int
	ctrOn  bool
	segs   int
}

// openBringUp attaches the open plane when the PL is the open image.
func (b *Board) openBringUp() error {
	if !openpl.Detect(b.regs.ACQ) {
		return nil
	}
	pl, err := openpl.Open()
	if err != nil {
		return fmt.Errorf("open PL image refused (spec 20 §12): %w", err)
	}
	b.open = &openBackend{pl: pl}
	id := pl.ID
	b.log.Printf("open PL image %q ABI %d.%d build %08x manifest %016x caps %#x, sample clock %d kHz",
		id.Image, id.ABIMajor, id.ABIMinor, id.Build, id.Manifest, id.Caps, pl.ClockKHz())
	return nil
}

// OpenPL is the open plane when the board runs on the open image (nil on
// the vendor image): the in-guest tests read registers the HAL does not
// carry (trigger time and fraction, counters, stacker bins).
func (b *Board) OpenPL() *openpl.PL {
	if b.open == nil {
		return nil
	}
	return b.open.pl
}

// OpenStatus is the stored record's status as the open image reported it.
func (b *Board) OpenStatus() (openpl.Status, openpl.Record) {
	if b.open == nil {
		return openpl.Status{}, openpl.Record{}
	}
	return b.open.st, b.open.rec
}

// openProfile is the stage-2 profile: what the image's ID_CAPS says.
func (b *Board) openProfile(p hal.Profile) hal.Profile {
	o := b.open
	p.Stage = 2
	p.Content = sample.ContentSetOf(sample.RawFullRate, sample.RawDecimated, sample.PeakPairs)
	p.Triggers = openTrigCaps(o.pl)
	p.Display.HWRenderer = false
	absent := map[string]string{}
	maps.Copy(absent, p.Absent)
	delete(absent, "stacker")
	if !o.pl.Has(openpl.CapStacker) {
		absent["stacker"] = "this open image has no stacker"
	}
	delete(absent, "zones")
	if !p.Triggers.Has(hal.NodeZone) {
		absent["zones"] = "this open image has no zone engine (spec 20 §7.8)"
	}
	absent["images"] = "one open image (" + o.pl.ID.Image + "); switching images is the boot target's (vendor or open)"
	p.Absent = absent
	return p
}

// openTrigCaps: the interval engine serves every time-qualified node with
// every slope (spec 20 §7.4), so the vendor engine's broken cases go; video,
// zone, CAN, LIN and the symbol nodes come with their RTL (ID_CAPS).
func openTrigCaps(pl *openpl.PL) hal.TrigCaps {
	all := []hal.Slope{hal.Rising, hal.Falling, hal.Either}
	c := hal.TrigCaps{Nodes: map[hal.TrigNode]hal.NodeCaps{
		hal.NodeEdge:      {Slopes: all, QualOps: allQualOps, MinS: 0.5e-9, MaxS: maxTimeS},
		hal.NodePulse:     {Slopes: twoSlopes, QualOps: allQualOps, MinS: 0.5e-9, MaxS: maxTimeS},
		hal.NodeSlope:     {Slopes: twoSlopes, QualOps: allQualOps, MinS: 0.5e-9, MaxS: maxTimeS},
		hal.NodeRunt:      {Slopes: twoSlopes, QualOps: allQualOps, MinS: 0.5e-9, MaxS: maxTimeS},
		hal.NodeWindow:    {},
		hal.NodePattern:   {QualOps: allQualOps, MinS: 0.5e-9, MaxS: maxTimeS},
		hal.NodeSetupHold: {Slopes: twoSlopes, QualOps: allQualOps, MinS: 0.5e-9, MaxS: maxTimeS},
		hal.NodeTimeout:   {Slopes: twoSlopes, QualOps: []hal.QualOp{hal.Longer}, MinS: 0.5e-9, MaxS: maxTimeS},
	}, MaxStages: 2, Sources: []hal.SourceKind{hal.Analog}}
	if pl.Has(openpl.CapVideo) {
		c.Nodes[hal.NodeVideo] = hal.NodeCaps{}
	}
	for _, p := range []struct {
		bit  uint32
		name string
	}{{openpl.CapI2C, "i2c"}, {openpl.CapSPI, "spi"}, {openpl.CapUART, "uart"}} {
		if pl.Has(p.bit) {
			c.Protocols = append(c.Protocols, p.name)
		}
	}
	if len(c.Protocols) > 0 {
		c.Nodes[hal.NodeProtocol] = hal.NodeCaps{}
	}
	return c
}

// openChans is what the trigger conversion needs of every channel.
func (b *Board) openChans(s *acq.Settings) [4]trigger.Chan {
	var ch [4]trigger.Chan
	for c := range b.id.Channels {
		d := b.eng.FE.Derived(c)
		ch[c] = trigger.Chan{VoltsDiv: s.Ch[c].VoltsDiv, PosPx: d.PosPx, Class: d.Class, Invert: s.Ch[c].Invert}
	}
	return ch
}

// openRecord is the record of the committed plan: the screen record
// (timebase S samples at the table rate R, any integer decimation of the
// route's rate) with the trigger at the screen centre minus the delay.
func (b *Board) openRecord(s *acq.Settings) (openpl.Record, error) {
	h := b.eng.Timebase()
	mask := uint8(s.Mask(b.id.Channels)) //nolint:gosec // G115: four channel bits
	if mask == 0 {
		return openpl.Record{}, hal.Refuse("acquisition", "no channel is on")
	}
	route := openpl.RouteFor(mask)
	d := max(1, int(math.Round(openpl.RouteRate(route)/(h.R*1e6))))
	n := max(64, int(h.S))
	pre := int(math.Round(float64(n)/2 - h.R*1e6*s.DelayS))
	pre = max(0, min(n-1, pre))
	r := openpl.Record{Route: route, Mask: mask, Decim: d, Pre: pre, Post: n - 1 - pre, Segments: max(1, s.Segments)}
	if peakActive(h) && d >= 2 {
		r.Mode, r.Pre, r.Post = openpl.Peak, 2*pre, 2*(n-1-pre)+1
	}
	ch := b.openChans(s)
	for c := range b.id.Channels {
		if s.Ch[c].Invert {
			r.Invert |= 1 << c
		}
		if s.Ch[c].Coupling == frontend.GND {
			r.GndSub |= 1 << c
		}
		r.GndCode[c] = uint16(max(0, min(4095, openpl.LevelCode(0, ch[c], trigger.CoupDC))))
	}
	return r.Sized()
}

// openCommit applies the staged front end, then the record and trigger.
func (b *Board) openCommit() error {
	o := b.open
	b.eng.Commit(b.staged)
	rec, err := b.openRecord(&b.staged)
	if err != nil {
		return hal.Refuse("acquisition", err.Error())
	}
	recS := float64(rec.Words()) / (openpl.RouteRate(rec.Route) / float64(rec.Decim))
	t, err := openpl.FromSettings(b.staged.Trig, b.openChans(&b.staged), o.pl.ID.Caps, recS)
	if ref, ok := errors.AsType[*openpl.RefusalError](err); ok {
		return hal.Refuse(ref.What, ref.Why)
	}
	if err != nil {
		return err
	}
	o.rec, o.trig, o.segs = rec, t, rec.Segments
	o.rateHz = openpl.RouteRate(rec.Route) / float64(rec.Decim)
	o.winS = rec.Words()
	o.pl.Commit(rec, t)
	return nil
}

func (b *Board) openArm() error {
	if b.staged.Roll {
		return hal.Refuse("arm", "a roll plan is not armed: it streams through the Streamer")
	}
	if err := sequenceSweep(&b.staged); err != nil {
		return err
	}
	if b.open.stream != nil {
		return hal.Refuse("arm", "the stream runs")
	}
	if err := b.openCommit(); err != nil {
		b.errs.arms++
		return err
	}
	b.open.pl.Arm()
	b.state, b.stored, b.rerun, b.forced = hal.Armed, false, false, false
	if b.staged.Trig.Sweep == trigger.Force {
		// Free running: every acquisition completes forced.
		b.open.pl.Force()
		b.forced = true
	}
	return nil
}

func (b *Board) openWait(ctx context.Context, timeout time.Duration) (hal.Status, error) {
	if b.state != hal.Armed {
		return hal.Status{}, errors.New("not armed")
	}
	pl := b.open.pl
	deadline := time.Now().Add(timeout)
	for {
		st := pl.State()
		if st == openpl.Done {
			break
		}
		if st == openpl.Idle {
			b.state = hal.Idle
			return hal.Status{}, errors.New("the acquisition went idle without a record")
		}
		if timeout >= 0 && time.Now().After(deadline) {
			return hal.Status{Triggered: st == openpl.Post}, hal.ErrTimeout
		}
		select {
		case <-ctx.Done():
			return hal.Status{}, ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
	st := pl.ReadStatus()
	b.open.st = st
	b.seq++
	b.state, b.stored = hal.Ready, true
	b.status = hal.Status{
		Triggered: !st.Auto && !st.Forced, Forced: st.Forced || st.Auto, Done: true, Coherent: !st.Overflow,
		Record: hal.RecordID{Epoch: b.epoch, Seq: uint32(b.seq)}, //nolint:gosec // G115: the record sequence wraps at 32 bits
	}
	if b.open.segs > 1 {
		b.rerun = true
	}
	return b.status, nil
}

func (b *Board) openForce() error {
	if b.state != hal.Armed {
		return hal.Refuse("force trigger", "nothing is armed")
	}
	b.open.pl.Force()
	b.forced = true
	return nil
}

func (b *Board) openStop() error {
	if b.state == hal.Armed {
		b.stored = false
	}
	b.open.pl.Stop()
	b.state, b.rerun = hal.Idle, true
	return nil
}

// openRead reads windows of the stored record through the readout engine
// and the AXI DMA. A window is record words relative to the trigger word;
// fewer Points than words are met by the engine (every k-th word, or the
// bucket extremes for peak records and PeakPairs).
func (b *Board) openRead(ctx context.Context, req hal.ReadRequest, dst []*sample.Frame, yield func()) error {
	o := b.open
	if !b.stored || (b.state != hal.Ready && b.state != hal.Idle) {
		return hal.Refuse("readout", "no stored record: arm and wait for one first")
	}
	if req.Record != (hal.RecordID{}) && req.Record != b.status.Record {
		return hal.Refuse("readout", "that record was overwritten by a later acquisition")
	}
	if b.dma == nil {
		return errors.New("readout: no DMA path")
	}
	segs := req.Segments
	if segs == [2]int{} {
		segs = [2]int{0, 1}
	}
	if segs[0] < 0 || segs[1] <= segs[0] || segs[1] > o.segs {
		return hal.Refuse("segments", fmt.Sprintf("segments %d…%d: the record holds %d", segs[0], segs[1]-1, o.segs))
	}
	if len(req.Windows) == 0 || len(req.Windows) > maxWindows {
		return hal.Refuse("readout", fmt.Sprintf("1 to %d windows per read", maxWindows))
	}
	if want := len(req.Windows) * (segs[1] - segs[0]); len(dst) != want {
		return fmt.Errorf("readout: %d frames for %d windows × segments", len(dst), want)
	}
	prev := b.state
	b.state = hal.Reading
	defer func() { b.state = prev }()
	for s := segs[0]; s < segs[1]; s++ {
		pos := o.st.Pos
		if s > 0 || o.segs > 1 {
			pos = o.pl.ReadSegment(s).Pos
		}
		for i, w := range req.Windows {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := b.openWindow(w, s, pos, dst[(s-segs[0])*len(req.Windows)+i]); err != nil {
				b.errs.readouts++
				return err
			}
			if yield != nil {
				yield()
			}
		}
	}
	return nil
}

func (b *Board) openWindow(w hal.Window, seg, pos int, f *sample.Frame) error {
	o := b.open
	r := o.rec
	if len(w.Channels) != 1 {
		return hal.Refuse("readout window", "one channel per window: the readout engine reads one channel per pass")
	}
	ch := w.Channels[0]
	if ch < 0 || ch > 3 || r.Mask&(1<<ch) == 0 {
		return hal.Refuse("readout window", fmt.Sprintf("C%d was not acquired", ch+1))
	}
	peak := r.Mode == openpl.Peak
	start, span := int(w.StartSample), int(w.Span)
	if span <= 0 {
		start, span = -r.Pre, r.Words()
	}
	if peak {
		start, span = 2*(start/2), span+span%2
	}
	if start < -r.Pre || start+span-1 > r.Post {
		return hal.Refuse("readout window", fmt.Sprintf("words %d…%d from the trigger lie outside the record (%d before, %d after)",
			start, start+span-1, r.Pre, r.Post))
	}
	mode, k := openpl.ReadWords, 1
	switch {
	case w.Want == sample.RawFullRate && (r.Decim > 1 || peak):
		return hal.Refuse("readout window", "the record is decimated in the PL at this time/div")
	case w.Want == sample.PeakPairs && !peak && w.Points <= 0:
		return hal.Refuse("readout window", "the record holds plain samples; ask for points to reduce them to pairs")
	case w.Points > 0 && span > w.Points:
		k = (span + w.Points - 1) / w.Points
		mode = openpl.ReadNth
		if peak || w.Want == sample.PeakPairs {
			mode = openpl.ReadMinMax
		}
		if w.Want == sample.RawFullRate {
			return hal.Refuse("readout window", fmt.Sprintf("%d full-rate samples do not fit %d points", span, w.Points))
		}
	}
	rw := r.RingWords()
	spec := openpl.ReadSpec{Ch: ch, Seg: seg, First: ((pos+start)%rw + rw) % rw, Count: span, Mode: mode, Bucket: k}
	var tr sample.Trace
	if len(f.Traces) > 0 {
		tr = f.Traces[0]
	}
	words := grow(tr.Codes16, spec.OutWords())
	if err := o.pl.Read(b.dma, spec, rw, words, acq.MaxBlock); err != nil {
		return err
	}
	tw := o.st.TriggerWord(r.Route, r.Decim)
	if seg > 0 || o.segs > 1 {
		sg := o.pl.ReadSegment(seg)
		st := o.st
		st.Frac, st.FracOK = sg.Frac, !sg.Auto && !sg.Forced && o.trig.Interp
		tw = st.TriggerWord(r.Route, r.Decim)
	}
	captureS := 1 / openpl.RouteRate(r.Route)
	content := sample.RawFullRate
	if r.Decim > 1 || k > 1 {
		content = sample.RawDecimated
	}
	factor := k
	if mode == openpl.ReadMinMax || peak {
		content = sample.PeakPairs
		if mode == openpl.ReadMinMax {
			factor = max(
				// two codes per bucket
				k/2, 1)
		}
	}
	kind := sample.HardwareNode
	if o.st.Forced || o.st.Auto {
		kind = sample.Forced
	}
	*f = sample.Frame{
		Traces:    append(f.Traces[:0], sample.Trace{}),
		Content:   content,
		Flags:     sample.Ordered,
		CaptureS:  captureS,
		AcqDecim:  r.Decim,
		Depth:     r.Words(),
		WinPoints: o.winS / max(1, factor),
		TriggerAt: (float64(-start) + tw) / float64(max(1, factor)),
		TrigKind:  kind,
		TrigNode:  o.trig.Node,
		Seq:       b.seq,
		ContentID: uint64(b.epoch)<<32 | b.seq,
		Status:    b.status,
		Snapshot:  sample.Settings{TdivS: b.eng.Timebase().TimeDiv, DisplayedS: b.eng.Timebase().TimeDiv, Norm: true},
	}
	f.SampleS = captureS * float64(r.Decim) * float64(max(1, factor))
	if o.segs > 1 {
		f.Flags |= sample.Segmented
	}
	sc := b.eng.Scale(ch)
	f.Traces[0] = sample.Trace{
		Ch: ch, Format: format12, Codes16: words, Valid: len(words),
		Scale:       sample.Scale{PerCode: sc.PerCode, Zero: sc.Zero, OffsetV: sc.OffsetV},
		CodesPerDiv: codesPerDiv,
	}
	return nil
}

// openTimestamps are the segments' hardware trigger times (ticks) from the
// first segment's.
func (b *Board) openTimestamps(dst []float64) (int, error) {
	o := b.open
	if !b.stored || o.segs < 2 {
		return 0, hal.Refuse("segment timestamps", "no stored sequence")
	}
	n := min(o.segs, len(dst))
	var t0 uint64
	for k := range n {
		t := o.pl.ReadSegment(k).TimeTicks
		if k == 0 {
			t0 = t
		}
		dst[k] = float64(int64(t-t0)) * openpl.TickS //nolint:gosec // G115: tick differences fit
	}
	return n, nil
}

// ---------------------------------------------------------------- counter

// openCounter counts the trigger source's comparator A rising edges over
// 1 ms gates (250 000 sample-core clocks): the reciprocal count over the
// ticks between the gate's first and last edge resolves 0.5 ns over 1 ms,
// 5·10⁻⁷, whatever the frequency.
func (b *Board) openCounter() (sample.Counters, error) {
	o := b.open
	src := b.staged.Trig.Source
	if src < 0 || src > 3 {
		src = 0
	}
	ev := openpl.EvARise(src)
	if !o.ctrOn || o.ctrEv != ev {
		o.pl.CounterStart(ev, 250_000, false)
		o.ctrOn, o.ctrEv = true, ev
		return sample.Counters{}, nil
	}
	n, ticks, done := o.pl.CounterRead()
	if !done || n < 2 || ticks == 0 {
		return sample.Counters{Valid: false}, nil
	}
	return sample.Counters{FreqHz: float64(n-1) / (float64(ticks) * openpl.TickS), Valid: true}, nil
}

// ---------------------------------------------------------------- stacker

type openStacker Board

// Stack accumulates n records into the stacker's phase bins: one sequence
// acquisition of n segments (Normal sweep, the staged trigger), then a
// readout of each segment's window around its trigger into the stacker
// instead of the DMA; the stacker bins each by its segment's trigger
// fraction (spec 20 §8.2). The window is as long as fits 8192 bins at 16
// phases (512 words), centred on the trigger. The sequence stays the stored
// record, so its segments can be read as usual. OWNER.
func (s *openStacker) Stack(n int) error {
	b := (*Board)(s)
	o := b.open
	if n < 1 || n > 1024 {
		return hal.Refuse("stack", "1 … 1024 records (one sequence of segments)")
	}
	if b.staged.Trig.Sweep == trigger.Auto {
		return hal.Refuse("stack", "stacking needs triggered records: Normal or Single sweep")
	}
	saved := b.staged.Segments
	b.staged.Segments = n
	defer func() { b.staged.Segments = saved }()
	if err := b.openArm(); err != nil {
		return err
	}
	st, err := b.openWait(context.Background(), 60*time.Second)
	if err != nil || !st.Done {
		return fmt.Errorf("stack: the sequence did not complete: %w", err)
	}
	words, phases := b.stackWindow()
	if err := o.pl.StackStart(n, phases); err != nil {
		return err
	}
	r := o.rec
	rw := r.RingWords()
	for i := range n {
		pos := o.pl.ReadSegment(i).Pos
		o.pl.StartReadout(openpl.ReadSpec{Ch: bits.TrailingZeros8(r.Mask), Seg: i, First: ((pos-words/2)%rw + rw) % rw, Count: words})
		if !o.pl.WaitReadout(10 * time.Second) {
			return fmt.Errorf("stack record %d: readout did not finish", i)
		}
	}
	for range 1000 {
		if run, _, got := o.pl.StackStatus(); !run && got == n {
			return nil
		}
		time.Sleep(time.Millisecond)
	}
	_, _, got := o.pl.StackStatus()
	return fmt.Errorf("stack: %d of %d records reached the stacker", got, n)
}

// stackWindow is the stacked window's words and phases.
func (b *Board) stackWindow() (words, phases int) {
	r := b.open.rec
	phases = 16
	words = min(8192/phases, r.Words()-1)
	words = min(words, 2*min(r.Pre, r.Post))
	return max(2, words), phases
}

// StackBins reads the stacker's bins of the last Stack: sum and count per
// bin, the window's words and the phases (bin i·P + p is word i of the
// window at phase p). The HAL's Stacker has no read side yet.
func (b *Board) StackBins() (sums []uint32, counts []uint16, words, phases int) {
	if b.open == nil {
		return nil, nil, 0, 0
	}
	words, phases = b.stackWindow()
	n := words * phases
	sums, counts = make([]uint32, n), make([]uint16, n)
	for i := range n {
		sums[i], counts[i] = b.open.pl.Bin(i)
	}
	return sums, counts, words, phases
}
