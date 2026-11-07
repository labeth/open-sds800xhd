//go:build simboard

package boardtest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-dso-core/sample"
)

// only stages the channels so that ch alone is on with setting c; the
// others are switched off at 1 V/div. It returns what the board applied.
func only(ch int, c hal.Channel) (hal.Channel, error) {
	fe := board.FrontEnd()
	n := board.Profile().Channels
	var got hal.Channel
	for i := range n {
		s := hal.Channel{VoltsDiv: 1, Probe: 1, Coupling: hal.DC}
		if i == ch {
			s = c
			s.On = true
		}
		a, err := fe.Set(i, s)
		if err != nil {
			return a, err
		}
		if i == ch {
			got = a
		}
	}
	return got, nil
}

// acquireOne commits the staged front end with a plan for channel ch at
// tdivS and an auto edge trigger on it, arms, waits for one record and
// reads its main window.
func acquireOne(ch int, tdivS float64) (*sample.Frame, error) {
	p, err := board.Planner().Plan(hal.Request{Channels: []int{ch}, TimePerDivS: tdivS})
	if err != nil {
		return nil, fmt.Errorf("plan: %w", err)
	}
	if err := board.Acquirer().Configure(p); err != nil {
		return nil, fmt.Errorf("configure: %w", err)
	}
	err = board.Trigger().Program(hal.TriggerSpec{Sweep: hal.Auto, Stages: []hal.TrigStage{{
		Node: hal.NodeEdge, Source: hal.Source{Kind: hal.Analog, Index: ch}, Levels: hal.Levels{Slope: hal.Rising},
	}}})
	if err != nil {
		return nil, fmt.Errorf("trigger: %w", err)
	}
	a := board.Acquirer()
	if a.State() == hal.Armed {
		_ = a.Stop()
	}
	for range 3 {
		if err := a.Arm(); err != nil {
			return nil, fmt.Errorf("arm: %w", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		st, err := a.Wait(ctx, 3*time.Second)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("wait: %w", err)
		}
		if !st.Done {
			continue
		}
		f := &sample.Frame{}
		err = board.Readout().Read(context.Background(), hal.ReadRequest{Windows: []hal.Window{{Role: hal.Main, Channels: []int{ch}}}},
			[]*sample.Frame{f}, nil)
		if err != nil {
			return nil, fmt.Errorf("read: %w", err)
		}
		return f, nil
	}
	return nil, errors.New("no complete record in three arms")
}

// levels returns the means of the lowest and the highest quarter of a
// trace's samples, in volts: the two levels of a square wave.
func levels(t *sample.Trace) (lo, hi float64) {
	v := make([]float64, t.Valid)
	for i := range v {
		v[i] = t.Volts(i)
	}
	slices.Sort(v)
	q := max(1, len(v)/4)
	for _, x := range v[:q] {
		lo += x
	}
	for _, x := range v[len(v)-q:] {
		hi += x
	}
	return lo / float64(q), hi / float64(q)
}
