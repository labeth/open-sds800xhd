package web

import (
	"context"
	"testing"
	"time"

	"github.com/labeth/open-dso-core/engine"
	"github.com/labeth/open-dso-core/engine/fanout"
	"github.com/labeth/open-dso-core/hal/halfake"
	"github.com/labeth/open-dso-core/instrument"
	"github.com/labeth/open-dso-core/sample"
	"github.com/labeth/open-dso-core/scopeweb"
	"github.com/labeth/open-dso-core/uimodel/panel"
	"github.com/labeth/open-dso-core/webserver"
	"github.com/labeth/open-dso-core/webserver/webtest"

	"github.com/labeth/open-sds800xhd/app/internal/frontend"
	"github.com/labeth/open-sds800xhd/app/internal/scpi"
)

// format12 is this scope's record word: 12-bit codes left-aligned in 16 bits.
var format12 = sample.CodeFormat{Bits: 12, Storage: sample.U16, Shift: 4, Min: 0x0800, Max: 0xF7F0, Mid: 0x8000}

// fakeScope is the web scope over a four-channel 12-bit fake board run by
// the core's HAL loop, from this scope's default state, with the shared UI
// model over the same instrument state, until the test ends.
func fakeScope(t *testing.T) webserver.Scope {
	t.Helper()
	b := halfake.New(halfake.Config{Channels: 4, Format: format12})
	st := scpi.Defaults(4)
	st.TimeDivS = 1e-6
	st.Depth = 0
	ctl := instrument.New(st, nil)
	frames := fanout.NewFrames()
	ctl.SetFrames(frames)
	loop, cycle := engine.NewHALLoop(engine.HALConfig{Board: b, Instrument: ctl, WaitSlice: 5 * time.Millisecond, IdleWait: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, 2)
	go func() { loop.Run(ctx); done <- struct{}{} }()
	go func() { frames.Run(cycle, ctx.Done()); done <- struct{}{} }()
	t.Cleanup(func() { cancel(); <-done; <-done })
	if frames.WaitNext(0, 2*time.Second) == 0 {
		t.Fatal("no frame published")
	}
	us := panel.NewHALScope(ctl, b, "test")
	ctl.SetTimebases(us.Tdivs())
	pc := panel.New(us.Engine(), us.Analog(), us.Levels(), us.Hardware(), us.Tdivs(), st.TimeDivS, t.Logf)
	pc.SetFrameSource(frames.WithFrame)
	go us.Watch(frames, ctx.Done())
	return Scope(b, ctl, frames, "test", pc, nil, scopeweb.Parts{Bode: us.Bode()})
}

// The page's web scope over a fake of this scope's shape passes the core's
// conformance suite: caps, settings v2, v2-only frames, records, setups.
func TestConformance(t *testing.T) {
	webtest.Run(t, func() webserver.Scope { return fakeScope(t) })
}

// The probe factors offered are 1-2-5 steps from 0.1x to 10000x.
func TestProbes(t *testing.T) {
	p := frontend.Probes()
	if len(p) != 16 || p[0] != 0.1 || p[3] != 1 || p[6] != 10 || p[len(p)-1] != 10000 {
		t.Fatalf("%v", p)
	}
}
