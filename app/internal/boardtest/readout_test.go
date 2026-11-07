//go:build simboard

package boardtest

import (
	"context"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-dso-core/sample"
)

// setup stages the front end (the channels in on at 1 V/div, the rest off),
// the plan and an edge trigger on C1, and returns the plan.
func setup(t *testing.T, on []int, req hal.Request) (hal.Plan, error) {
	t.Helper()
	fe := board.FrontEnd()
	for c := range board.Profile().Channels {
		if _, err := fe.Set(c, hal.Channel{On: slices.Contains(on, c), VoltsDiv: 1, Probe: 1, Coupling: hal.DC, Impedance: 1e6}); err != nil {
			return hal.Plan{}, err
		}
	}
	req.Channels = on
	p, err := board.Planner().Plan(req)
	if err != nil {
		return p, err
	}
	if err := board.Acquirer().Configure(p); err != nil {
		return p, err
	}
	err = board.Trigger().Program(hal.TriggerSpec{Sweep: hal.NormalSweep, Stages: []hal.TrigStage{{
		Node: hal.NodeEdge, Source: hal.Source{Kind: hal.Analog, Index: on[0]},
		Levels: hal.Levels{High: 0, Slope: hal.Rising},
	}}})
	return p, err
}

// acquire arms and waits for one record.
func acquire(c *tcase) (hal.Status, bool) {
	a := board.Acquirer()
	if !c.noErr(a.Arm(), "arm") {
		return hal.Status{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for {
		st, err := a.Wait(ctx, 5*time.Second)
		if err != nil {
			c.check(false, "wait: %v", err)
			return st, false
		}
		if st.Done {
			return st, true
		}
		if ctx.Err() != nil {
			c.check(false, "no complete record in 20 s")
			return st, false
		}
		if err := a.Arm(); err != nil {
			c.check(false, "re-arm: %v", err)
			return st, false
		}
	}
}

// volts returns min and max of a trace in volts.
func volts(tr *sample.Trace) (lo, hi float64) {
	lo, hi = math.Inf(1), math.Inf(-1)
	for i := range tr.Valid {
		v := tr.Volts(i)
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	return lo, hi
}

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func frames(n int) []*sample.Frame {
	fs := make([]*sample.Frame, n)
	for i := range fs {
		fs[i] = &sample.Frame{}
	}
	return fs
}

// TestReadout reads windows of stored records (spec 03 §4.3): the main
// screen record, zoom windows of the same record, compressed and piece
// reads of long records, picked points, refusals.
func TestReadout(t *testing.T) {
	r := board.Readout()
	c := begin(t, "readout/no-record")
	_ = board.Acquirer().Stop()
	c.refused(r.Read(context.Background(), hal.ReadRequest{Windows: []hal.Window{{Channels: []int{0}}}}, frames(1), nil), "read before any acquisition")

	// 1 ch… 2 ch 1 GSa/s 200 ns/div 10k: Δ 80, S 2000, O 1, L 678 (spec 03 §4.3 examples).
	c = begin(t, "readout/main")
	if _, err := setup(t, []int{0, 1}, hal.Request{TimePerDivS: 200e-9, Depth: 10_000}); !c.noErr(err, "stage 2 ch 200 ns/div 10k") {
		return
	}
	st, ok := acquire(c)
	if !ok {
		return
	}
	fs := frames(3)
	err := r.Read(context.Background(), hal.ReadRequest{Record: st.Record, Windows: []hal.Window{
		{Role: hal.Main, Channels: []int{0}},
		{Role: hal.Main, Channels: []int{1}},
		{Role: hal.Zoom, Channels: []int{0}, StartSample: -100, Span: 400},
	}}, fs, nil)
	if !c.noErr(err, "main C1, main C2 and a zoom window of C1 in one read") {
		return
	}
	m1, m2, z := fs[0], fs[1], fs[2]
	c.check(m1.Traces[0].Valid == 2000 && m2.Traces[0].Valid == 2000, "main windows hold the 2000-sample screen record (%d, %d)", m1.Traces[0].Valid, m2.Traces[0].Valid)
	c.check(m1.Content == sample.RawFullRate && near(m1.SampleS, 1e-9, 1e-15), "full rate, 1 ns apart")
	lo, hi := volts(&m1.Traces[0])
	c.check(near(lo, -1, 0.05) && near(hi, 1, 0.05), "C1 square 2 Vpp reads %.3f … %.3f V", lo, hi)
	lo, hi = volts(&m2.Traces[0])
	c.check(near(lo, -0.5, 0.05) && near(hi, 0.5, 0.05), "C2 sine 1 Vpp reads %.3f … %.3f V", lo, hi)
	ti := int(math.Round(m1.TriggerAt))
	w := m1.Traces[0].Codes16
	c.check(ti > 0 && ti < len(w)-1 && w[ti-1] < 0x8000 && w[ti+1] >= 0x8000, "a rising edge of C1 at the trigger index %.1f", m1.TriggerAt)
	k := ti - 100
	zw := z.Traces[0].Codes16[:z.Traces[0].Valid]
	c.check(z.Traces[0].Valid == 400 && k >= 0 && slices.Equal(zw, w[k:k+400]), "the zoom window equals the main record's samples %d…%d", k, k+400)
	c.check(m1.Status.Record == st.Record, "frames carry the record %v", st.Record)

	c = begin(t, "readout/window")
	if c.noErr(r.Read(context.Background(), hal.ReadRequest{Windows: []hal.Window{{Channels: []int{0}}}}, frames(1), nil), "main C1 alone") {
		c.expect(map[string]uint16{"acq:0x0c3": 1, "acq:0x0c4": 0, "acq:0x0c5": 678, "acq:0x0c6": 0, "acq:0x0d5": 0, "acq:0x0c1": 0})
	}

	c = begin(t, "readout/points")
	fs = frames(1)
	err = r.Read(context.Background(), hal.ReadRequest{Windows: []hal.Window{{Channels: []int{1}, Points: 500}}}, fs, nil)
	if c.noErr(err, "500 points of a 2000-sample record") {
		c.check(fs[0].Content == sample.RawDecimated && fs[0].Traces[0].Valid == 500 && near(fs[0].SampleS, 4e-9, 1e-15),
			"picked every 4th sample: %d points %.3g s apart", fs[0].Traces[0].Valid, fs[0].SampleS)
	}

	c = begin(t, "readout/re-read")
	_ = board.Acquirer().Stop()
	fs = frames(1)
	err = r.Read(context.Background(), hal.ReadRequest{Record: st.Record, Windows: []hal.Window{{Channels: []int{0}}}}, fs, nil)
	if c.noErr(err, "re-read of the stored record after STOP") {
		c.check(slices.Equal(fs[0].Traces[0].Codes16[:2000], w[:2000]), "same samples")
	}

	c = begin(t, "readout/refusals")
	c.refused(r.Read(context.Background(), hal.ReadRequest{Windows: []hal.Window{{Channels: []int{0, 1}}}}, frames(1), nil), "two channels in one window")
	c.refused(r.Read(context.Background(), hal.ReadRequest{Windows: []hal.Window{{Channels: []int{2}}}}, frames(1), nil), "a channel that was off")
	c.refused(r.Read(context.Background(), hal.ReadRequest{Windows: []hal.Window{{Channels: []int{0}, Want: sample.PeakPairs}}}, frames(1), nil), "peak pairs")
	c.refused(r.Read(context.Background(), hal.ReadRequest{Windows: []hal.Window{{Channels: []int{0}, StartSample: 900, Span: 400}}}, frames(1), nil), "a window past the screen record")
	c.refused(r.Read(context.Background(), hal.ReadRequest{Windows: []hal.Window{{Channels: []int{0}, Want: sample.RawFullRate, Points: 100}}}, frames(1), nil), "full rate in fewer points")
	_, err = board.Planner().Plan(hal.Request{Channels: []int{0}, TimePerDivS: 1e-6, Mode: hal.Average})
	c.refused(err, "averaging as an acquisition mode")

	// 1 ch 10M 1 ms/div: S = 1e7 at 1 GSa/s, Δ 80. The screen read is
	// compressed (r 1, c_f 2); a full-rate window is a piece read.
	c = begin(t, "readout/compressed")
	if _, err := setup(t, []int{0}, hal.Request{TimePerDivS: 1e-3, Depth: 10_000_000}); !c.noErr(err, "stage 1 ch 1 ms/div 10M") {
		return
	}
	if st, ok = acquire(c); !ok {
		return
	}
	fs = frames(1)
	err = r.Read(context.Background(), hal.ReadRequest{Windows: []hal.Window{{Channels: []int{0}}}}, fs, nil)
	if c.noErr(err, "screen record of 1e7 samples") {
		f := fs[0]
		lo, hi = volts(&f.Traces[0])
		c.check(f.Content == sample.RawDecimated && f.Traces[0].Valid == 5_000_000 && near(f.SampleS, 2e-9, 1e-15),
			"compressed by 2: %d samples %.3g s apart", f.Traces[0].Valid, f.SampleS)
		c.check(near(lo, -1, 0.05) && near(hi, 1, 0.05), "reads %.3f … %.3f V", lo, hi)
	}
	c.expect(map[string]uint16{"acq:0x0d5": 1})

	c = begin(t, "readout/piece")
	fs = frames(1)
	err = r.Read(context.Background(), hal.ReadRequest{Windows: []hal.Window{{
		Channels: []int{0}, Want: sample.RawFullRate,
		StartSample: 6_000_000 - 4_999_998, Span: 1000,
	}}}, fs, nil)
	if c.noErr(err, "1000 full-rate samples 6e6 into the screen record") {
		f := fs[0]
		lo, hi = volts(&f.Traces[0])
		c.check(f.Content == sample.RawFullRate && f.Traces[0].Valid == 1000 && near(f.SampleS, 1e-9, 1e-15), "%d full-rate samples", f.Traces[0].Valid)
		c.check(near(lo, -1, 0.05) && near(hi, 1, 0.05), "reads %.3f … %.3f V", lo, hi)
	}
	// O for STARt 6e6 is 0x1_2768, L that of the whole record (spec 03 §4.3).
	c.expect(map[string]uint16{"acq:0x0c3": 0x2768, "acq:0x0c4": 1, "acq:0x0c5": 0x2670, "acq:0x0c6": 0x26})
	_ = board.Acquirer().Stop()
}
