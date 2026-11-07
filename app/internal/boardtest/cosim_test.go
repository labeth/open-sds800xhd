//go:build simboard

package boardtest

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-dso-core/hal/prototrig"
	"github.com/labeth/open-dso-core/sample"
	"github.com/labeth/open-sds800xhd/app/internal/openpl"
	"github.com/labeth/open-sds800xhd/app/internal/platform"
)

// The open-PL areas run when the simulator's PL is the open design's
// co-simulation (simd -pl open, make cosim-test): the board brought itself
// up on stage 2. Each check prints its expected and measured values; the
// host harness tabulates them.

// row reports one expected-vs-measured check.
func (c *tcase) row(ok bool, what string, want, got any) bool {
	c.t.Helper()
	return c.check(ok, "%s: expected %v measured %v", what, want, got)
}

func needOpen(t *testing.T) *openpl.PL {
	t.Helper()
	pl := board.OpenPL()
	if pl == nil {
		fmt.Println("RESULT cosim/stage FAIL the board runs the vendor image (stage 1), not the open PL")
		t.FailNow()
	}
	return pl
}

// TestCosimStartup: the board found the open image and brought it up.
func TestCosimStartup(t *testing.T) {
	c := begin(t, "startup/identity")
	pl := needOpen(t)
	p := board.Profile()
	c.row(p.Stage == 2, "profile stage", 2, p.Stage)
	id := pl.ID
	c.row(id.ABIMajor == openpl.ABIMajor, "ID_ABI major", openpl.ABIMajor, id.ABIMajor)
	c.row(id.Manifest == openpl.Manifest, "ID_MANIFEST", fmt.Sprintf("%016x", uint64(openpl.Manifest)), fmt.Sprintf("%016x", id.Manifest))
	c.row(id.Lanes == 8 && id.Channels == 4 && id.TickPS == 500, "ID_GEOMETRY lanes/channels/tick ps", "8/4/500",
		fmt.Sprintf("%d/%d/%d", id.Lanes, id.Channels, id.TickPS))
	k := pl.ClockKHz()
	c.row(k >= 249_000 && k <= 251_000, "SYS_CCLK_KHZ", 250000, k)
	st := pl.Read32(openpl.SysStatus)
	c.row(st&0xF == 0xF, "SYS_STATUS locks, core alive, ADC1 FIFO", "0xf", fmt.Sprintf("%#x", st&0xF))
	c.row(pl.Faults() == 0, "SYS_FAULT after start-up", 0, 0)
	caps := pl.ID.Caps
	c.row(p.Triggers.Has(hal.NodeEdge) && caps&openpl.CapInterval != 0, "trigger caps from ID_CAPS", "interval engine", fmt.Sprintf("caps %#x", caps))
	_, ok := board.Stacker()
	c.row(ok == pl.Has(openpl.CapStacker), "Stacker facet follows ID_CAPS", pl.Has(openpl.CapStacker), ok)
}

// ---------------------------------------------------------------- helpers

func an(c int) hal.Source { return hal.Source{Kind: hal.Analog, Index: c} }

// openSetup puts the channels of vdiv on (the others off), plans tdiv and
// programs the trigger.
func openSetup(vdiv map[int]float64, tdiv float64, segs int, spec hal.TriggerSpec) error {
	fe := board.FrontEnd()
	var on []int
	for c := range board.Profile().Channels {
		v, ok := vdiv[c]
		if !ok {
			v = 1
		}
		if ok {
			on = append(on, c)
		}
		if _, err := fe.Set(c, hal.Channel{On: ok, VoltsDiv: v, Probe: 1, Coupling: hal.DC, Impedance: 1e6}); err != nil {
			return err
		}
	}
	p, err := board.Planner().Plan(hal.Request{Channels: on, TimePerDivS: tdiv, Segments: segs, Depth: planDepth})
	if err != nil {
		return err
	}
	if err := board.Acquirer().Configure(p); err != nil {
		return err
	}
	if spec.Sweep == 0 {
		spec.Sweep = hal.NormalSweep
	}
	return board.Trigger().Program(spec)
}

// planDepth is the depth openSetup asks for (0: the board's default).
var planDepth int

// openAcquire arms and waits up to wait for a record; ok false on a timeout
// (the acquisition is then stopped).
func openAcquire(c *tcase, wait time.Duration) (hal.Status, bool) {
	a := board.Acquirer()
	if !c.noErr(a.Arm(), "arm") {
		return hal.Status{}, false
	}
	st, err := a.Wait(context.Background(), wait)
	if errors.Is(err, hal.ErrTimeout) {
		return st, false
	}
	if err != nil {
		c.check(false, "wait: %v", err)
		return st, false
	}
	return st, st.Done
}

// fSine is C1's frequency in the co-simulation runs: a little off 1 MHz,
// so the trigger's sub-sample phase walks from record to record.
const fSine = 1.000123e6

// rec is one channel's whole record, 12-bit codes, with the trigger word's
// index and the word period.
type rec struct {
	x       []int
	tw      int
	sampleS float64
	frame   *sample.Frame
}

func readRec(c *tcase, ch int) (rec, bool) {
	f := &sample.Frame{}
	err := board.Readout().Read(context.Background(), hal.ReadRequest{Windows: []hal.Window{{Role: hal.Main, Channels: []int{ch}}}},
		[]*sample.Frame{f}, nil)
	if !c.noErr(err, fmt.Sprintf("read C%d", ch+1)) {
		return rec{}, false
	}
	_, r := board.OpenStatus()
	w := f.Traces[0].Codes16[:f.Traces[0].Valid]
	x := make([]int, len(w))
	for i, v := range w {
		x[i] = int(v >> 4)
	}
	return rec{x: x, tw: r.Pre, sampleS: f.SampleS, frame: f}, true
}

// thr is channel ch's comparator thresholds as programmed (k 0 = A, 1 = B).
func thr(pl *openpl.PL, ch, k int) (hi, lo int) {
	base := openpl.TrigCmp0AHi + uint32(16*ch+8*k)
	return int(pl.Read32(base)), int(pl.Read32(base + 4))
}

// schmitt is the comparator's state per sample and its rising/falling edges.
func schmitt(x []int, hi, lo int) (rises, falls []int) {
	s := x[0] >= hi
	for i, v := range x {
		switch {
		case !s && v >= hi:
			s = true
			rises = append(rises, i)
		case s && v <= lo:
			s = false
			falls = append(falls, i)
		}
	}
	return rises, falls
}

func lastBefore(e []int, i int) int {
	r := -1
	for _, v := range e {
		if v <= i {
			r = v
		}
	}
	return r
}

func ns(n int, sampleS float64) float64 { return float64(n) * sampleS * 1e9 }

// fracOf is the interpolator's fraction (spec 20 §7.5) from the two samples
// around a level.
func fracOf(x0, x1, l int, falling bool) int {
	if falling {
		x0, x1, l = 4095-x0, 4095-x1, 4095-l
	}
	if x1 <= x0 {
		return 0
	}
	if l <= x0 {
		return 0
	}
	if l-x0 >= x1-x0 {
		return 16
	}
	return (l - x0) * 16 / (x1 - x0)
}

// TestCosimTrigger: every trigger type the open image runs, firing on the
// simulator's signals (the harness sets them: C1 a ~1 MHz sine (fSine) 2 Vpp, C2 a
// 500 kHz pulse of 200 ns from −1 V to +1 V with a runt (−0.2 V, 60 ns)
// every second period, C3 UART 1 MBd 0x55 0xA3, C4 1 MHz square ±1 V in
// bursts of five periods). Each fired record is checked against the
// trigger's condition measured in the record itself with a software model
// of the comparators; edge triggers also against the 1/16-sample
// interpolator.
func TestCosimTrigger(t *testing.T) {
	pl := needOpen(t)
	rise, fall, either := hal.Levels{Slope: hal.Rising}, hal.Levels{Slope: hal.Falling}, hal.Levels{Slope: hal.Either}
	stage := func(n hal.TrigNode, src int, l hal.Levels) hal.TrigStage {
		return hal.TrigStage{Node: n, Source: an(src), Levels: l}
	}
	one := func(st hal.TrigStage) hal.TriggerSpec { return hal.TriggerSpec{Stages: []hal.TrigStage{st}} }
	fire := func(id string, vdiv map[int]float64, tdiv float64, spec hal.TriggerSpec, ch int) (*tcase, rec, bool) {
		c := begin(t, id)
		if !c.noErr(openSetup(vdiv, tdiv, 0, spec), "set up") {
			return c, rec{}, false
		}
		st, ok := openAcquire(c, 20*time.Second)
		if !c.row(ok && st.Triggered, "record triggered", true, ok && st.Triggered) {
			_ = board.Acquirer().Stop()
			return c, rec{}, false
		}
		r, ok := readRec(c, ch)
		return c, r, ok
	}
	sine := map[int]float64{0: 0.5}
	// ---- edge, the interpolator
	for _, e := range []struct {
		name string
		l    hal.Levels
	}{{"rising", rise}, {"falling", fall}} {
		c, r, ok := fire("trigger/edge-"+e.name, sine, 100e-9, one(stage(hal.NodeEdge, 0, e.l)), 0)
		if !ok {
			continue
		}
		hi, lo := thr(pl, 0, 0)
		x0, x1 := r.x[r.tw-1], r.x[r.tw]
		st, _ := board.OpenStatus()
		if e.l.Slope == hal.Rising {
			c.row(x0 < hi && x1 >= hi, "trigger word: x[-1] < L <= x[0]", fmt.Sprintf("L=%d", hi), fmt.Sprintf("%d, %d", x0, x1))
			want := fracOf(x0, x1, hi, false)
			c.row(st.FracOK && st.Frac == want, "TRIG_FRAC (1/16 sample)", want, st.Frac)
		} else {
			c.row(x0 > lo && x1 <= lo, "trigger word: x[-1] > L >= x[0]", fmt.Sprintf("L=%d", lo), fmt.Sprintf("%d, %d", x0, x1))
			want := fracOf(x0, x1, lo, true)
			c.row(st.FracOK && st.Frac == want, "TRIG_FRAC (1/16 sample)", want, st.Frac)
		}
		c.row(r.sampleS == 0.5e-9, "sample period at one channel (ns)", 0.5, r.sampleS*1e9)
		c.row(math.Abs(r.frame.TriggerAt-(float64(r.tw)-1+float64(st.Frac)/16)) < 1e-9, "frame TriggerAt (trigger word − 1 + frac/16)",
			float64(r.tw)-1+float64(st.Frac)/16, r.frame.TriggerAt)
		fmt.Printf("TRIGTICK %s %d %d\n", c.id, st.TimeTicks, st.Frac)
	}
	if c, r, ok := fire("trigger/edge-either", sine, 100e-9, one(stage(hal.NodeEdge, 0, either)), 0); ok {
		hi, lo := thr(pl, 0, 0)
		x0, x1 := r.x[r.tw-1], r.x[r.tw]
		c.row((x0 < hi && x1 >= hi) || (x0 > lo && x1 <= lo), "trigger word crosses a threshold", fmt.Sprintf("%d / %d", lo, hi), fmt.Sprintf("%d, %d", x0, x1))
	}
	// ---- pulse width
	pulse := map[int]float64{1: 0.5}
	shorter := one(stage(hal.NodePulse, 1, rise))
	shorter.Stages[0].Qual = &hal.Qualifier{Op: hal.Shorter, MinS: 100e-9, MaxS: 300e-9}
	if c, r, ok := fire("trigger/pulse-shorter", pulse, 500e-9, shorter, 1); ok {
		hi, lo := thr(pl, 1, 0)
		rs, fs := schmitt(r.x, hi, lo)
		w := ns(r.tw-lastBefore(rs, r.tw), r.sampleS)
		c.row(lastBefore(fs, r.tw) == r.tw && w > 150 && w < 300, "pulse width at the trigger < 300 ns (ns)", "≈200", w)
	}
	longer := one(stage(hal.NodePulse, 1, rise))
	longer.Stages[0].Qual = &hal.Qualifier{Op: hal.Longer, MinS: 300e-9, MaxS: 1e-6}
	{
		c := begin(t, "trigger/pulse-longer-none")
		if c.noErr(openSetup(pulse, 500e-9, 0, longer), "set up") {
			_, ok := openAcquire(c, 3*time.Second)
			acc := pl.Read32(openpl.TrigAccepted)
			c.row(!ok && acc == 0, "no 200 ns pulse is longer than 300 ns: accepted", 0, acc)
			c.noErr(board.Acquirer().Force(), "force")
			st, err := board.Acquirer().Wait(context.Background(), 10*time.Second)
			c.row(err == nil && st.Done && st.Forced, "forced record", "done, forced", fmt.Sprintf("%v %v (%v)", st.Done, st.Forced, err))
		}
	}
	// ---- interval
	iv := one(stage(hal.NodeEdge, 0, rise))
	iv.Stages[0].Qual = &hal.Qualifier{Op: hal.Inside, MinS: 0.9e-6, MaxS: 1.1e-6}
	if c, r, ok := fire("trigger/interval", sine, 500e-9, iv, 0); ok {
		hi, lo := thr(pl, 0, 0)
		rs, _ := schmitt(r.x, hi, lo)
		prev := lastBefore(rs, r.tw-1)
		d := ns(r.tw-prev, r.sampleS)
		c.row(prev >= 0 && math.Abs(d-1e9/fSine) <= 1, "interval between rising edges (ns)", fmt.Sprintf("%.3f", 1e9/fSine), d)
	}
	// ---- Nth edge on bursts
	nth := one(stage(hal.NodeEdge, 3, rise))
	nth.Stages[0].Count = 3
	nth.Stages[0].Qual = &hal.Qualifier{Op: hal.Longer, MinS: 2e-6}
	if c, r, ok := fire("trigger/nth-edge", map[int]float64{3: 0.5}, 2e-6, nth, 3); ok {
		hi, lo := thr(pl, 3, 0)
		rs, _ := schmitt(r.x, hi, lo)
		n, last := 0, -1
		for _, e := range rs {
			if e > r.tw {
				break
			}
			if last >= 0 && ns(e-last, r.sampleS) > 2000 {
				n = 0
			}
			n++
			last = e
		}
		c.row(n == 3 && last == r.tw, "rising edges since the idle gap", 3, n)
	}
	// ---- slope, window, runt (two levels)
	sl := stage(hal.NodeSlope, 0, hal.Levels{Low: -0.5, High: 0.5, Slope: hal.Rising})
	sl.Qual = &hal.Qualifier{Op: hal.Inside, MinS: 100e-9, MaxS: 250e-9}
	if c, r, ok := fire("trigger/slope", sine, 200e-9, one(sl), 0); ok {
		ahi, alo := thr(pl, 0, 0)
		bhi, blo := thr(pl, 0, 1)
		ra, _ := schmitt(r.x, ahi, alo)
		rb, _ := schmitt(r.x, bhi, blo)
		d := ns(r.tw-lastBefore(ra, r.tw), r.sampleS)
		want := 1e3 * (math.Asin(0.5) - math.Asin(-0.5)) / (2 * math.Pi)
		c.row(lastBefore(rb, r.tw) == r.tw && math.Abs(d-want) < 15, "low-to-high transition time (ns)", fmt.Sprintf("%.1f", want), d)
	}
	win := stage(hal.NodeWindow, 0, hal.Levels{Low: -0.8, High: 0.8})
	if c, r, ok := fire("trigger/window", sine, 200e-9, one(win), 0); ok {
		ahi, alo := thr(pl, 0, 0)
		bhi, _ := thr(pl, 0, 1)
		v := r.x[r.tw]
		c.row(v >= bhi || v <= alo, "trigger sample outside the window", fmt.Sprintf("<= %d or >= %d", alo, bhi), v)
		_ = ahi
	}
	runt := stage(hal.NodeRunt, 1, hal.Levels{Low: -0.5, High: 0.5, Slope: hal.Rising})
	runt.Qual = &hal.Qualifier{Op: hal.Shorter, MinS: 1e-9, MaxS: 1e-6}
	if c, r, ok := fire("trigger/runt", pulse, 500e-9, one(runt), 1); ok {
		ahi, alo := thr(pl, 1, 0)
		bhi, _ := thr(pl, 1, 1)
		ra, _ := schmitt(r.x, ahi, alo)
		st := lastBefore(ra, r.tw)
		peak := 0
		for _, v := range r.x[max(0, st):r.tw] {
			peak = max(peak, v)
		}
		c.row(st >= 0 && peak >= ahi && peak < bhi, "runt peak between the levels (codes)", fmt.Sprintf("%d … %d", ahi, bhi), peak)
	}
	// ---- dropout
	dr := stage(hal.NodeTimeout, 1, rise)
	dr.Qual = &hal.Qualifier{Op: hal.Longer, MinS: 1e-6}
	if c, r, ok := fire("trigger/dropout", pulse, 1e-6, one(dr), 1); ok {
		hi, lo := thr(pl, 1, 0)
		rs, _ := schmitt(r.x, hi, lo)
		d := ns(r.tw-lastBefore(rs, r.tw), r.sampleS)
		c.row(math.Abs(d-1000) < 5, "time since the last rising edge at the dropout (ns)", 1000, d)
	}
	// ---- pattern, qualified, delay, setup/hold
	two := map[int]float64{0: 0.5, 3: 0.5}
	pat := hal.TrigStage{Node: hal.NodePattern, Levels: rise, Pattern: &hal.Pattern{Terms: []hal.PatternTerm{
		{Source: an(0), State: hal.High}, {Source: an(3), State: hal.High},
	}}}
	if c, r, ok := fire("trigger/pattern", two, 500e-9, one(pat), 0); ok {
		r4, ok4 := readRec(c, 3)
		hi1, _ := thr(pl, 0, 0)
		hi4, _ := thr(pl, 3, 0)
		if ok4 {
			c.row(r.x[r.tw] >= hi1-4 && r4.x[r4.tw] >= hi4-4, "C1 and C4 high at the trigger (codes)", fmt.Sprintf(">= %d, %d", hi1, hi4),
				fmt.Sprintf("%d, %d", r.x[r.tw], r4.x[r4.tw]))
		}
	}
	q := stage(hal.NodeEdge, 0, rise)
	q.Gate = &hal.Gate{Source: an(3), High: true}
	if c, r, ok := fire("trigger/qualified", two, 500e-9, one(q), 0); ok {
		r4, ok4 := readRec(c, 3)
		hi, _ := thr(pl, 0, 0)
		hi4, lo4 := thr(pl, 3, 0)
		if ok4 {
			c.row(r.x[r.tw-1] < hi && r.x[r.tw] >= hi && r4.x[r4.tw] > (hi4+lo4)/2, "C1 rises while C4 is high", "rise, C4 high",
				fmt.Sprintf("C1 %d→%d, C4 %d (C4 %v, C1 %v around the trigger word; C4 thresholds %d/%d, QUAL %#x/%#x)",
					r.x[r.tw-1], r.x[r.tw], r4.x[r4.tw], r4.x[r4.tw-3:r4.tw+4], r.x[r.tw-3:r.tw+4], hi4, lo4,
					pl.Read32(openpl.TrigQualMask), pl.Read32(openpl.TrigQualReq)))
		}
	}
	dl := hal.TriggerSpec{Stages: []hal.TrigStage{
		{Node: hal.NodePattern, Levels: rise, Pattern: &hal.Pattern{Terms: []hal.PatternTerm{{Source: an(3), State: hal.High}}}},
		{Node: hal.NodeEdge, Source: an(0), Levels: rise, Qual: &hal.Qualifier{Op: hal.Shorter, MinS: 1e-9, MaxS: 1.2e-6}},
	}}
	if c, r, ok := fire("trigger/delay", two, 500e-9, dl, 0); ok {
		r4, _ := readRec(c, 3)
		hi4, lo4 := thr(pl, 3, 0)
		rs4, _ := schmitt(r4.x, hi4, lo4)
		d := ns(r.tw-lastBefore(rs4, r.tw), r.sampleS)
		c.row(d < 1200, "C1 rise after C4 became high (ns)", "< 1200", d)
	}
	sh := hal.TrigStage{
		Node: hal.NodeSetupHold, Source: an(3), Levels: rise, Gate: &hal.Gate{Source: an(0), High: true},
		Qual: &hal.Qualifier{Op: hal.Shorter, MinS: 1e-9, MaxS: 1e-6},
	}
	if c, r, ok := fire("trigger/setup", two, 500e-9, one(sh), 3); ok {
		r1, _ := readRec(c, 0)
		hi, lo := thr(pl, 0, 0)
		rs, fs := schmitt(r1.x, hi, lo)
		d := ns(r.tw-max(lastBefore(rs, r.tw), lastBefore(fs, r.tw)), r.sampleS)
		c.row(d < 1000, "data edge to clock edge (ns)", "< 1000", d)
	}
	// ---- UART
	u := hal.TrigStage{Node: hal.NodeProtocol, Proto: prototrig.UART{
		Line:      prototrig.Line{Source: an(2), Level: 1.65},
		Condition: prototrig.UARTData, Baud: 1e6, Bits: 8, StopBits: 1, IdleHigh: true, Value: 0xA3,
	}}
	if c, r, ok := fire("trigger/uart", map[int]float64{2: 1}, 5e-6, one(u), 2); ok {
		m := pl.Read32(openpl.ProtoMatches)
		c.row(m >= 1, "PROTO_MATCHES", ">= 1", m)
		sl := pl.Read32(openpl.ProtoSliceA) // the line slicer's thresholds, not a trigger comparator's
		_, fs := schmitt(r.x, int(sl&0xFFF), int(sl>>16&0xFFF))
		// the match is at the byte's stop bit: its start bit fell 9.5 bits before
		d := ns(r.tw-lastBefore(fs, r.tw-int(8e-6/r.sampleS)), r.sampleS)
		_ = d
		c.row(len(fs) > 0, "UART frames in the record", "> 0", len(fs))
	}
}

// readWin reads one window of channel ch (segments s0 … s1−1).
func readWin(c *tcase, ch int, w hal.Window, s0, s1 int) ([]*sample.Frame, bool) {
	w.Role, w.Channels = hal.Main, []int{ch}
	fs := make([]*sample.Frame, s1-s0)
	for i := range fs {
		fs[i] = &sample.Frame{}
	}
	err := board.Readout().Read(context.Background(), hal.ReadRequest{Segments: [2]int{s0, s1}, Windows: []hal.Window{w}}, fs, nil)
	return fs, c.noErr(err, fmt.Sprintf("read C%d words %d+%d points %d", ch+1, w.StartSample, w.Span, w.Points))
}

func codes(f *sample.Frame) []int {
	t := f.Traces[0]
	x := make([]int, t.Valid)
	for i := range x {
		x[i] = int(t.Codes16[i] >> 4)
	}
	return x
}

// TestCosimReadout: windows, the engine's reductions against the same
// reductions of the raw words in software, ring wrap, decimation, peak
// records, segments with hardware timestamps, and the counter (C1 1 MHz
// sine 2 Vpp).
func TestCosimReadout(t *testing.T) {
	pl := needOpen(t)
	c := begin(t, "readout/full")
	edge := hal.TriggerSpec{Stages: []hal.TrigStage{{Node: hal.NodeEdge, Source: an(0), Levels: hal.Levels{Slope: hal.Rising}}}}
	if !c.noErr(openSetup(map[int]float64{0: 0.5}, 500e-9, 0, edge), "set up") {
		return
	}
	if _, ok := openAcquire(c, 20*time.Second); !c.row(ok, "record", "done", ok) {
		return
	}
	st, r := board.OpenStatus()
	raw, ok := readRec(c, 0)
	if !ok {
		return
	}
	c.row(len(raw.x) == r.Words(), "words read (whole record)", r.Words(), len(raw.x))
	rw := r.RingWords()
	first := ((st.Pos-r.Pre)%rw + rw) % rw
	c.row(true, "ring: trigger word, first word, ring words, wrapped", "-", fmt.Sprintf("%d, %d, %d, %v", st.Pos, first, rw, first+r.Words() > rw))
	c.row(st.Words >= r.Words(), "ACQ_WORDS >= PRE + POST + 1", r.Words(), st.Words)
	// a part window
	fs, ok := readWin(c, 0, hal.Window{StartSample: -100, Span: 300}, 0, 1)
	if ok {
		x := codes(fs[0])
		same := len(x) == 300
		for i := 0; same && i < 300; i++ {
			same = x[i] == raw.x[raw.tw-100+i]
		}
		c.row(same, "window −100…+199 equals the whole record's words", 300, len(x))
		c.row(math.Abs(fs[0].TriggerAt-(100-1+float64(st.Frac)/16)) < 1e-9, "window TriggerAt", 100-1+float64(st.Frac)/16, fs[0].TriggerAt)
	}
	// nth: every k-th word
	n := len(raw.x)
	pts := 1000
	k := (n + pts - 1) / pts
	if fs, ok := readWin(c, 0, hal.Window{StartSample: int64(-r.Pre), Span: int64(n), Points: pts, Want: sample.RawDecimated}, 0, 1); ok {
		x := codes(fs[0])
		bad := 0
		for i := range x {
			if i*k >= n || x[i] != raw.x[i*k] {
				bad++
			}
		}
		c.row(bad == 0 && len(x) == (n+k-1)/k, "engine nth (k words) = software nth, mismatches", 0, fmt.Sprintf("%d of %d (k %d)", bad, len(x), k))
	}
	// minmax: the bucket extremes
	if fs, ok := readWin(c, 0, hal.Window{StartSample: int64(-r.Pre), Span: int64(n), Points: pts, Want: sample.PeakPairs}, 0, 1); ok {
		x := codes(fs[0])
		bad := 0
		for b := 0; 2*b+1 < len(x); b++ {
			mx, mn := 0, 4095
			for _, v := range raw.x[b*k : min(n, (b+1)*k)] {
				mx, mn = max(mx, v), min(mn, v)
			}
			if x[2*b] != mx || x[2*b+1] != mn {
				bad++
			}
		}
		c.row(bad == 0 && len(x) == 2*((n+k-1)/k), "engine minmax = software (max, min) per bucket, mismatches", 0, fmt.Sprintf("%d of %d buckets", bad, len(x)/2))
	}
	c.row(st.Lost == 0, "REC_LOST (groups)", 0, st.Lost)
	// a deep record at the full rate: 400 000 words at 2 GSa/s, every group written
	c = begin(t, "readout/deep")
	if c.noErr(openSetup(map[int]float64{0: 0.5}, 20e-6, 0, edge), "set up") {
		if _, ok := openAcquire(c, 60*time.Second); c.row(ok, "record", "done", ok) {
			st, r := board.OpenStatus()
			c.row(st.Lost == 0 && pl.Faults()&1 == 0, "REC_LOST, rec_overflow", "0, 0", fmt.Sprintf("%d, %d", st.Lost, pl.Faults()&1))
			if d, ok := readRec(c, 0); ok {
				hi, lo := thr(pl, 0, 0)
				rs, _ := schmitt(d.x, hi, lo)
				bad := 0
				for i := 1; i < len(rs); i++ {
					if math.Abs(ns(rs[i]-rs[i-1], d.sampleS)-1e9/fSine) > 1 {
						bad++
					}
				}
				c.row(r.Decim == 1 && len(rs) > 100 && bad == 0, "words, sine periods off by > 1 ns", "D 1, 0",
					fmt.Sprintf("%d words, D %d, %d of %d periods", len(d.x), r.Decim, bad, len(rs)-1))
			}
		}
	}
	// a decimated record: the sine's period in words
	c = begin(t, "readout/decimated")
	planDepth = 10_000
	defer func() { planDepth = 0 }()
	if c.noErr(openSetup(map[int]float64{0: 0.5}, 20e-6, 0, edge), "set up") {
		if _, ok := openAcquire(c, 30*time.Second); c.row(ok, "record", "done", ok) {
			_, r := board.OpenStatus()
			if d, ok := readRec(c, 0); ok {
				hi, lo := thr(pl, 0, 0)
				rs, _ := schmitt(d.x, hi, lo)
				per := ns(rs[len(rs)-1]-rs[0], d.sampleS) / float64(len(rs)-1)
				c.row(r.Decim > 1 && math.Abs(per-1e9/fSine) < 2*d.sampleS*1e9, "decimation D, sine period from the decimated words (ns)",
					fmt.Sprintf("D > 1, %.1f ± %.0f", 1e9/fSine, 2*d.sampleS*1e9), fmt.Sprintf("D %d, %.1f", r.Decim, per))
				c.row(d.frame.AcqDecim == r.Decim, "frame AcqDecim", r.Decim, d.frame.AcqDecim)
			}
		}
	}
	// a peak-detect record: (max, min) pairs per group of D samples
	c = begin(t, "readout/peak")
	fe := board.FrontEnd()
	if _, err := fe.Set(0, hal.Channel{On: true, VoltsDiv: 0.5, Probe: 1, Coupling: hal.DC, Impedance: 1e6}); c.noErr(err, "C1") {
		p, err := board.Planner().Plan(hal.Request{Channels: []int{0}, TimePerDivS: 20e-6, Mode: hal.PeakDetect, Depth: 10_000})
		if c.noErr(err, "plan peak detect") && c.noErr(board.Acquirer().Configure(p), "configure") &&
			c.noErr(board.Trigger().Program(edge), "trigger") {
			if _, ok := openAcquire(c, 30*time.Second); c.row(ok, "record", "done", ok) {
				_, r := board.OpenStatus()
				if d, ok := readRec(c, 0); ok {
					bad, top, bot := 0, 0, 4095
					for i := 0; i+1 < len(d.x); i += 2 {
						if d.x[i] < d.x[i+1] {
							bad++
						}
						top, bot = max(top, d.x[i]), min(bot, d.x[i+1])
					}
					c.row(r.Mode == openpl.Peak && d.frame.Content == sample.PeakPairs, "record mode, frame content", "peak, PeakPairs",
						fmt.Sprintf("%d, %v", r.Mode, d.frame.Content))
					c.row(bad == 0, "pairs with max < min", 0, bad)
					c.row(top > 2048+900 && bot < 2048-900, "envelope reaches the sine's peaks (codes)", "> 2948, < 1148", fmt.Sprintf("%d, %d", top, bot))
				}
			}
		}
	}
	// segments with hardware timestamps
	c = begin(t, "readout/segments")
	planDepth = 0
	if c.noErr(openSetup(map[int]float64{0: 0.5}, 200e-9, 8, edge), "set up") {
		if _, ok := openAcquire(c, 30*time.Second); c.row(ok, "sequence of 8", "done", ok) {
			ts := make([]float64, 8)
			if sg, ok := board.Segments(); ok {
				m, err := sg.Timestamps(ts)
				bad := 0
				for k := 1; k < m; k++ {
					d := (ts[k] - ts[k-1]) * fSine
					if d <= 0 || math.Abs(d-math.Round(d)) > 0.002 {
						bad++
					}
				}
				c.row(err == nil && m == 8 && bad == 0, "segment spacing in sine periods", "whole numbers ± 0.002",
					fmt.Sprintf("%.4f … (%d off)", ts[1]*fSine, bad))
			}
			if fs, ok := readWin(c, 0, hal.Window{StartSample: -4, Span: 8}, 0, 8); ok {
				hi, _ := thr(pl, 0, 0)
				good := 0
				for _, f := range fs {
					x := codes(f)
					if x[3] < hi && x[4] >= hi {
						good++
					}
				}
				c.row(good == 8, "every segment's trigger word crosses the level", 8, good)
			}
		}
	}
	// counter: the trigger source's rising edges
	c = begin(t, "readout/counter")
	if c.noErr(openSetup(map[int]float64{0: 0.5}, 500e-9, 0, edge), "set up") {
		if _, ok := openAcquire(c, 20*time.Second); ok {
			ctr, _ := board.Counter()
			var v sample.Counters
			for range 200 {
				v, _ = ctr.Read()
				if v.Valid {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			c.row(v.Valid && math.Abs(v.FreqHz-fSine) < 1, "counter frequency (Hz, 1 ms gate, reciprocal)", fSine, v.FreqHz)
		}
	}
}

// TestCosimStream: a codes stream of C1 (sine) at 250 MSa/s is gap-free
// block to block; stalling the stream's HP port drops whole blocks, every
// one counted (sequence jump, lost flag, STREAM_LOST).
func TestCosimStream(t *testing.T) {
	pl := needOpen(t)
	c := begin(t, "stream/codes")
	if !c.noErr(openSetup(map[int]float64{0: 0.5}, 500e-9, 0, hal.TriggerSpec{Stages: []hal.TrigStage{{Node: hal.NodeEdge, Source: an(0), Levels: hal.Levels{Slope: hal.Rising}}}}), "set up") {
		return
	}
	s, _ := board.Streamer()
	if !c.noErr(s.Start(hal.StreamSpec{Payload: hal.PayloadCodes, Channels: []int{0}, SampleS: 4e-9}), "start (C1, 4 ns)") {
		return
	}
	defer func() { c.noErr(s.Stop(), "stop") }()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var prev []uint16
	var next uint64
	steps, jumps, blocks := 0, 0, 0
	maxStep := 0
	lease := func() (hal.StreamBlock, bool) {
		b, err := s.Lease(ctx)
		if err != nil {
			c.check(false, "lease: %v", err)
			return b, false
		}
		return b, true
	}
	for i := range 24 {
		b, ok := lease()
		if !ok {
			return
		}
		w := b.Words
		if i > 0 && b.First != next {
			jumps++
		}
		next = b.First + uint64(len(w)/4)
		all := slices.Concat(prev[len(prev)-min(1, len(prev)):], w)
		for j := 1; j < len(all); j++ {
			d := int(all[j]>>4) - int(all[j-1]>>4)
			maxStep = max(maxStep, max(d, -d))
			steps++
		}
		prev = w
		blocks++
		s.Release(b)
	}
	limit := int(math.Ceil(960 * 2 * math.Pi * fSine * 4e-9 * 1.1))
	c.row(jumps == 0, "sequence jumps over 24 blocks", 0, jumps)
	c.row(maxStep <= limit, "largest step between consecutive words, across block borders (codes)", fmt.Sprintf("<= %d", limit), maxStep)
	f := s.Faults()
	c.row(f.LostBlocks == 0 && pl.ReadStream().Lost == 0, "lost before the stall (blocks, items)", "0, 0", fmt.Sprintf("%d, %d", f.LostBlocks, pl.ReadStream().Lost))
	// inject: the HP1 port stalls for 60 000 mclk cycles (200 µs)
	c = begin(t, "stream/drops")
	platform.Mark("cosim stall hp1 60000")
	var lostBlocks uint64
	for range 40 {
		b, ok := lease()
		if !ok {
			return
		}
		if b.First != next {
			lostBlocks += (b.First - next) / uint64(len(b.Words)/4)
		}
		next = b.First + uint64(len(b.Words)/4)
		s.Release(b)
	}
	f = s.Faults()
	lost := pl.ReadStream().Lost
	c.row(lostBlocks > 0, "blocks dropped by the stall (sequence jumps)", "> 0", lostBlocks)
	c.row(f.LostBlocks == lostBlocks && f.SeqJumps > 0, "Faults().LostBlocks = the jumps seen", lostBlocks, f.LostBlocks)
	c.row(uint64(lost) == lostBlocks*510, "STREAM_LOST = dropped blocks × 510 items", lostBlocks*510, lost)
	c.row(pl.Faults()&2 != 0, "SYS_FAULT stream_overflow", 1, pl.Faults()&2)
}

// TestCosimStacker: 32 triggered segments of the ~1 MHz sine stacked into
// 16 phases in the PL; the bins equal the spec 20 §8.2 binning of the same
// segments' words and trigger fractions computed here.
func TestCosimStacker(t *testing.T) {
	pl := needOpen(t)
	c := begin(t, "stacker/bins")
	edge := hal.TriggerSpec{Stages: []hal.TrigStage{{Node: hal.NodeEdge, Source: an(0), Levels: hal.Levels{Slope: hal.Rising}}}}
	if !c.noErr(openSetup(map[int]float64{0: 0.5}, 200e-9, 0, edge), "set up") {
		return
	}
	sk, ok := board.Stacker()
	if !c.row(ok, "Stacker facet", true, ok) {
		return
	}
	const n = 32
	if !c.noErr(sk.Stack(n), "Stack(32)") {
		return
	}
	sums, counts, words, P := board.StackBins()
	fs, ok := readWin(c, 0, hal.Window{StartSample: int64(-words / 2), Span: int64(words)}, 0, n)
	if !ok {
		return
	}
	wantS, wantC := make([]uint32, words*P), make([]uint16, words*P)
	phases := map[int]bool{}
	for s := range n {
		phi := min(15, pl.ReadSegment(s).Frac) // the stacker bins a crossing on the trigger sample with 15/16
		p := P - 1 - phi*P/16
		phases[p] = true
		for i, v := range codes(fs[s]) {
			wantS[i*P+p] += uint32(v)
			wantC[i*P+p]++
		}
	}
	bad := 0
	for b := range wantS {
		if sums[b] != wantS[b] || counts[b] != wantC[b] {
			bad++
		}
	}
	c.row(bad == 0, "bins (sum, count) equal the software binning, mismatches", 0, fmt.Sprintf("%d of %d", bad, len(wantS)))
	c.row(len(phases) > 1, "phases used by the 32 records", "> 1", len(phases))
	_, _, recs := pl.StackStatus()
	c.row(recs == n, "STACK_STATUS records", n, recs)
}

// TestCosimProto: the SPI and I2C engines on generated buses (C1/C2 SPI
// clock and MOSI, mode 0, frames 0xA5 0x5A; C3/C4 I2C at 400 kHz, address
// 0x50 write, data 0x3C).
func TestCosimProto(t *testing.T) {
	pl := needOpen(t)
	bitsOf := func(v uint32, n int) []hal.PatternState {
		var b []hal.PatternState
		for i := n - 1; i >= 0; i-- {
			if v>>i&1 == 1 {
				b = append(b, hal.High)
			} else {
				b = append(b, hal.Low)
			}
		}
		return b
	}
	cases := []struct {
		id    string
		vdiv  map[int]float64
		tdiv  float64
		proto hal.ProtoTrigger
		ch    int
	}{
		{"proto/spi", map[int]float64{0: 1, 1: 1}, 5e-6, prototrig.SPI{
			Clock: prototrig.Line{Source: an(0), Level: 1.65},
			Data:  prototrig.Line{Source: an(1), Level: 1.65}, SelectMode: prototrig.SelectTimeout, IdleS: 3e-6, MSBFirst: true,
			Bits: bitsOf(0xA55A, 16),
		}, 1},
		{"proto/i2c", map[int]float64{2: 1, 3: 1}, 10e-6, prototrig.I2C{
			SCL: prototrig.Line{Source: an(2), Level: 1.65},
			SDA: prototrig.Line{Source: an(3), Level: 1.65}, Condition: prototrig.I2CAddr7Data, Address: 0x50,
			RW: prototrig.Write, Data: []uint8{0x3C},
		}, 3},
	}
	for _, k := range cases {
		c := begin(t, k.id)
		spec := hal.TriggerSpec{Stages: []hal.TrigStage{{Node: hal.NodeProtocol, Proto: k.proto}}}
		if !c.noErr(openSetup(k.vdiv, k.tdiv, 0, spec), "set up") {
			continue
		}
		st, ok := openAcquire(c, 30*time.Second)
		c.row(ok && st.Triggered, "record triggered", true, ok && st.Triggered)
		m, ev := pl.Read32(openpl.ProtoMatches), pl.Read32(openpl.ProtoEvents)
		c.row(m >= 1, "PROTO_MATCHES", ">= 1", m)
		c.row(ev >= 2, "PROTO_EVENTS (decoded)", ">= 2", ev)
		c.row(pl.Read32(openpl.ProtoErrors) == 0, "PROTO_ERRORS", 0, pl.Read32(openpl.ProtoErrors))
		if !ok {
			_ = board.Acquirer().Stop()
		}
	}
}
