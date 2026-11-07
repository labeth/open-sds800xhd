//go:build simboard

package boardtest

import (
	"context"
	"math"
	"testing"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-dso-core/sample"
)

// pairVolts returns the lowest maximum and the highest minimum of a
// peak-pair trace in volts, and how many pairs have max ≥ min.
func pairVolts(tr *sample.Trace) (loMax, hiMin float64, ordered int) {
	loMax, hiMin = math.Inf(1), math.Inf(-1)
	for i := range len(tr.Max16) {
		mx, mn := tr.Scale.Volts(float64(tr.Max16[i])), tr.Scale.Volts(float64(tr.Min16[i]))
		loMax, hiMin = math.Min(loMax, mx), math.Max(hiMin, mn)
		if tr.Max16[i] >= tr.Min16[i] {
			ordered++
		}
	}
	return loMax, hiMin, ordered
}

// TestPeak acquires in peak detect (spec 03 §6.1, spec 04 §5.2) and reads
// max/min pairs. The pair order (maximum first) is the simulator model's
// choice (spec 16 §5.3); spec 03 §5 leaves the PL's order UNVERIFIED, so
// the order cases check the board against the model, not against a unit.
func TestPeak(t *testing.T) {
	r := board.Readout()

	// 1 ch 10k 1 ms/div: 1 MSa/s, DECIM_TYPE 14, PEAK_EN (spec 04 §5.2
	// example: 0x004 = 0x10E). C1 is a 5 MHz square: each record sample
	// spans five periods, so every pair holds both levels.
	c := begin(t, "peak/pairs")
	p, err := setup(t, []int{0}, hal.Request{TimePerDivS: 1e-3, Depth: 10_000, Mode: hal.PeakDetect})
	if !c.noErr(err, "stage 1 ch 1 ms/div 10k in peak detect") {
		return
	}
	c.check(p.Content.Has(sample.PeakPairs), "the plan delivers peak pairs (%v)", p.Content)
	st, ok := acquire(c)
	if !ok {
		return
	}
	c.expect(map[string]uint16{"acq:0x004": 0x10E})
	fs := frames(1)
	err = r.Read(context.Background(), hal.ReadRequest{Record: st.Record, Windows: []hal.Window{{Channels: []int{0}}}}, fs, nil)
	if !c.noErr(err, "read the screen record") {
		return
	}
	f := fs[0]
	tr := &f.Traces[0]
	c.check(f.Content == sample.PeakPairs && f.Flags.Has(sample.Envelope) && tr.Valid == 10000 && len(tr.Max16) == 5000 && len(tr.Min16) == 5000,
		"%v: %d codes, %d pairs for the 10 000-sample screen", f.Content, tr.Valid, len(tr.Max16))
	interleaved := true
	for i := range len(tr.Max16) {
		interleaved = interleaved && tr.Codes16[2*i] == tr.Min16[i] && tr.Codes16[2*i+1] == tr.Max16[i]
	}
	c.check(interleaved && !f.Flags.Has(sample.Ordered), "the codes interleave each pair as (min, max), unordered: the PL does not record which came first")
	c.check(near(f.SampleS, 1e-6, 1e-12) && f.TriggerAt > 0 && f.TriggerAt < float64(2*len(tr.Max16)),
		"codes %.3g s apart (pairs twice that), trigger at code %.1f", f.SampleS, f.TriggerAt)
	loMax, hiMin, ordered := pairVolts(tr)
	c.check(near(loMax, 1, 0.05) && near(hiMin, -1, 0.05),
		"every pair spans the square: maxima ≥ %.3f V, minima ≤ %.3f V", loMax, hiMin)

	c = begin(t, "peak/pair-order-model")
	c.check(ordered == len(tr.Max16), "%d of %d pairs read maximum first: the model's order (spec 16 §5.3), UNVERIFIED on a unit (spec 03 §5)",
		ordered, len(tr.Max16))

	c = begin(t, "peak/points")
	fs = frames(1)
	err = r.Read(context.Background(), hal.ReadRequest{Record: st.Record, Windows: []hal.Window{{Channels: []int{0}, Want: sample.PeakPairs, Points: 100}}}, fs, nil)
	if c.noErr(err, "100 points of the screen record") {
		f, tr := fs[0], &fs[0].Traces[0]
		loMax, hiMin, _ := pairVolts(tr)
		c.check(len(tr.Max16) == 100 && tr.Valid == 200 && near(f.SampleS, 50e-6, 1e-12) && near(loMax, 1, 0.05) && near(hiMin, -1, 0.05),
			"%d buckets (codes %.3g s apart) keep the envelope %.3f / %.3f V", len(tr.Max16), f.SampleS, loMax, hiMin)
	}

	c = begin(t, "peak/refusals")
	c.refused(r.Read(context.Background(), hal.ReadRequest{Windows: []hal.Window{{Channels: []int{0}, Want: sample.RawDecimated}}}, frames(1), nil), "plain samples of a peak record")
	c.refused(r.Read(context.Background(), hal.ReadRequest{Windows: []hal.Window{{Channels: []int{0}, Want: sample.RawFullRate}}}, frames(1), nil), "full-rate samples of a peak record")

	// 1 µs/div is 2 GSa/s: PEAK_EN is 0 and the plan delivers plain
	// samples (spec 04 §5.2: switching to 2 GSa/s in peak mode, 0x004 ← 0).
	c = begin(t, "peak/full-rate")
	p, err = setup(t, []int{0}, hal.Request{TimePerDivS: 1e-6, Depth: 10_000, Mode: hal.PeakDetect})
	if c.noErr(err, "stage 1 µs/div in peak detect") {
		c.check(p.Content.Has(sample.RawFullRate) && p.Note != "", "plain samples: %q", p.Note)
		if st, ok := acquire(c); ok {
			c.expect(map[string]uint16{"acq:0x004": 0})
			fs = frames(1)
			if err := r.Read(context.Background(), hal.ReadRequest{Record: st.Record, Windows: []hal.Window{{Channels: []int{0}}}}, fs, nil); c.noErr(err, "read") {
				c.check(fs[0].Content == sample.RawFullRate, "%v", fs[0].Content)
			}
		}
	}

	c = begin(t, "peak/off")
	if _, err := setup(t, []int{0}, hal.Request{TimePerDivS: 1e-3, Depth: 10_000}); c.noErr(err, "normal acquisition at 1 ms/div") {
		if _, ok := acquire(c); ok {
			c.expect(map[string]uint16{"acq:0x004": 0x00E})
		}
	}
	_ = board.Acquirer().Stop()
}
