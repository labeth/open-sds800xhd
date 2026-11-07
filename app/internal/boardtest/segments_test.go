//go:build simboard

package boardtest

import (
	"context"
	"slices"
	"testing"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-dso-core/sample"
)

// TestSegments captures a sequence (spec 03 §6.2), reads segments and their
// timestamps.
func TestSegments(t *testing.T) {
	seg, ok := board.Segments()
	c := begin(t, "segments/capacity")
	if !c.check(ok, "the board has segmented memory") {
		return
	}
	c.check(seg.Max(10_000) >= 10, "%d segments fit at 10k", seg.Max(10_000))
	_, err := board.Planner().Plan(hal.Request{Channels: []int{0}, TimePerDivS: 200e-9, Depth: 10_000, Segments: 1_000_000})
	c.refused(err, "a million segments")

	// A sequence under the AUTO sweep is refused at the commit, whichever
	// was staged first (sequence runs under Normal or Single, spec 03
	// §6.2; the board does not substitute the sweep).
	c = begin(t, "segments/auto-sweep")
	if _, err := setup(t, []int{0, 1}, hal.Request{TimePerDivS: 200e-9, Depth: 10_000, Segments: 10}); c.noErr(err, "stage a 10-segment sequence") {
		auto := hal.TriggerSpec{Sweep: hal.Auto, Stages: []hal.TrigStage{{
			Node:   hal.NodeEdge,
			Source: hal.Source{Kind: hal.Analog, Index: 0}, Levels: hal.Levels{Slope: hal.Rising},
		}}}
		c.noErr(board.Trigger().Program(auto), "stage the AUTO sweep")
		c.refused(board.Acquirer().Arm(), "arm a sequence in AUTO")
		c.check(board.Acquirer().State() != hal.Armed, "nothing armed (%v)", board.Acquirer().State())
		p, _ := board.Planner().Plan(hal.Request{Channels: []int{0}, TimePerDivS: 200e-9, Depth: 10_000})
		c.noErr(board.Acquirer().Configure(p), "single records in AUTO")
		if c.noErr(board.Acquirer().Arm(), "arm single records in AUTO") {
			_ = board.Acquirer().Stop()
		}
	}

	// 2 ch 200 ns/div, 10 segments: the example of spec 03 §6.2.
	c = begin(t, "segments/sequence")
	if _, err := setup(t, []int{0, 1}, hal.Request{TimePerDivS: 200e-9, Depth: 10_000, Segments: 10}); !c.noErr(err, "stage a 10-segment sequence") {
		return
	}
	st, ok := acquire(c)
	if !ok {
		return
	}
	c.expect(map[string]uint16{"acq:0x018": 0x40A, "map:0x1bb": 0xA, "map:0x1bf": 1, "map:0x1ba": 1, "acq:0x0f6": 9, "acq:0x0f8": 1})
	fs := frames(10)
	err = board.Readout().Read(context.Background(), hal.ReadRequest{
		Record: st.Record, Segments: [2]int{0, 10},
		Windows: []hal.Window{{Channels: []int{0}}},
	}, fs, nil)
	if !c.noErr(err, "read the 10 segments of C1") {
		return
	}
	differ := 0
	for k := 1; k < 10; k++ {
		if !slices.Equal(fs[k].Traces[0].Codes16[:fs[k].Traces[0].Valid], fs[0].Traces[0].Codes16[:fs[0].Traces[0].Valid]) {
			differ++
		}
	}
	c.check(fs[3].Flags&sample.Segmented != 0 && differ > 0, "segments are flagged and distinct records (%d of 9 differ from the first)", differ)
	ts := make([]float64, 10)
	n, err := seg.Timestamps(ts)
	if c.noErr(err, "timestamps") {
		inc := n == 10
		for k := 1; k < n; k++ {
			inc = inc && ts[k] > ts[k-1]
		}
		c.check(inc && ts[0] == 0, "%d timestamps, increasing from 0: %v", n, ts[:n])
	}

	c = begin(t, "segments/off")
	if _, err := setup(t, []int{0}, hal.Request{TimePerDivS: 200e-9, Depth: 10_000}); c.noErr(err, "back to single records") {
		if _, ok := acquire(c); ok {
			c.expect(map[string]uint16{"acq:0x018": 0x00A, "map:0x1bf": 0, "map:0x1ba": 0, "acq:0x0f6": 0})
		}
	}
	_ = board.Acquirer().Stop()
}
