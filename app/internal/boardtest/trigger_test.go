//go:build simboard

package boardtest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-sds800xhd/app/internal/trigcases"
)

// TestTrigger programs every trigger golden through the HAL, commits it
// with an arm and lets the harness check the trigger registers on the bus.
func TestTrigger(t *testing.T) {
	n := board.Profile().Channels
	var on []int
	for c := range n {
		if _, err := board.FrontEnd().Set(c, hal.Channel{On: true, VoltsDiv: 1, Probe: 1, Impedance: 1e6}); err != nil {
			t.Fatalf("C%d: %v", c+1, err)
		}
		on = append(on, c)
	}
	p, err := board.Planner().Plan(hal.Request{Channels: on, TimePerDivS: 200e-9})
	if err != nil {
		t.Fatal(err)
	}
	if err := board.Acquirer().Configure(p); err != nil {
		t.Fatal(err)
	}
	a := board.Acquirer()
	for _, gc := range trigcases.Cases {
		if n < 4 && strings.Contains(gc.Name, "c4") {
			continue
		}
		c := begin(t, "trigger/"+gc.Name)
		err := board.Trigger().Program(gc.Spec)
		if gc.Refuse != "" {
			if r, ok := hal.AsRefusal(err); ok && strings.Contains(r.Feature, gc.Refuse) {
				c.check(true, "refused: %s: %s", r.Feature, r.Reason)
			} else {
				c.check(false, "want a refusal of %q, got %v", gc.Refuse, err)
			}
			continue
		}
		if !c.noErr(err, "programmed") {
			continue
		}
		if err := a.Arm(); err != nil {
			c.check(false, "arm: %v", err)
			continue
		}
		_, err = a.Wait(context.Background(), 2*time.Second)
		if err != nil && !errors.Is(err, hal.ErrTimeout) {
			c.check(false, "wait: %v", err)
		}
		_ = a.Stop()
		c.expect(gc.Want)
	}

	for _, gc := range trigcases.Parity {
		if n < 4 {
			break
		}
		c := begin(t, "trigger/"+gc.Name)
		if !c.noErr(board.Trigger().Program(gc.Spec), "programmed as the reference run set it up") {
			continue
		}
		if err := a.Arm(); err == nil {
			_, _ = a.Wait(context.Background(), 2*time.Second)
		}
		_ = a.Stop()
	}

	c := begin(t, "trigger/force")
	spec := hal.TriggerSpec{Sweep: hal.Auto, Stages: []hal.TrigStage{{
		Node:   hal.NodeEdge,
		Source: hal.Source{Kind: hal.Analog, Index: 0}, Levels: hal.Levels{High: 0, Slope: hal.Rising},
	}}}
	if c.noErr(board.Trigger().Program(spec), "auto edge") && c.noErr(a.Arm(), "armed") {
		c.noErr(a.Force(), "forced")
		st, err := a.Wait(context.Background(), 2*time.Second)
		c.check(err == nil && st.Forced, "the record reports a forced trigger (%v, %+v)", err, st)
		_ = a.Stop()
		c.expect(map[string]uint16{trigcases.A(0x00C): 1, trigcases.A(0x02A): 1, trigcases.A(0x00A): 1, trigcases.A(0x00E): 0x1000})
	}

	// The video sync-tip default is the board's (spec 05 §5.6): at C1's
	// 1 V/div trunc(−150 mV · 60 / 1 V) = −9 px, A 0xE0 = 0x77A.
	c = begin(t, "trigger/video-default")
	td := board.Trigger()
	st, err := td.Default(hal.NodeVideo, hal.Source{Kind: hal.Analog, Index: 0})
	if c.noErr(err, "video default on C1") {
		c.check(near(st.Levels.High, -0.15, 1e-9), "sync-tip level %.4f V", st.Levels.High)
		if c.noErr(board.Trigger().Program(hal.TriggerSpec{Sweep: hal.NormalSweep, Stages: []hal.TrigStage{st}}), "programmed") &&
			c.noErr(a.Arm(), "armed") {
			_, _ = a.Wait(context.Background(), 2*time.Second)
			_ = a.Stop()
			c.expect(map[string]uint16{trigcases.A(0x0E0): 0x77a, trigcases.A(0x017): 0x13e0})
		}
	}
	_, err = td.Default(hal.NodePattern, hal.Source{Kind: hal.Analog, Index: 0})
	c.refused(err, "a pattern default")

	c = begin(t, "trigger/level-grid")
	g := board.Trigger().LevelGrid(hal.Source{Kind: hal.Analog, Index: 0})
	c.check(g.Step == 1.0/480 && g.Min == -4.1 && g.Max == 4.1, "one comparator step of 1/480 div over ±4.1 div: %+v", g)

	c = begin(t, "trigger/caps")
	caps := board.Trigger().Caps()
	c.check(len(caps.Nodes) == 10 && len(caps.Protocols) == 5 && !caps.Has(hal.NodeZone),
		"%d nodes, protocols %v, no zone", len(caps.Nodes), caps.Protocols)
}
