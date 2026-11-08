//go:build simboard

package boardtest

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labeth/open-dso-core/engine"
	"github.com/labeth/open-dso-core/engine/fanout"
	"github.com/labeth/open-dso-core/instrument"
	"github.com/labeth/open-dso-core/scopeweb"
	"github.com/labeth/open-dso-core/webserver"
	"github.com/labeth/open-dso-core/webserver/webtest"
	"github.com/labeth/open-dso-core/webserver/wire"

	"github.com/labeth/open-sds800xhd/app/internal/scpi"
	"github.com/labeth/open-sds800xhd/app/internal/web"
)

// webRig is the board run by the core's HAL loop with the shared web scope
// over it, as the app composes them.
type webRig struct {
	ctl *instrument.Model
	h   http.Handler
	sc  webserver.Scope
}

// do serves one request.
func (r *webRig) do(method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body)))
	return rec
}

// json serves one request and decodes its reply.
func (r *webRig) json(method, path, body string) map[string]any {
	var out map[string]any
	_ = json.Unmarshal(r.do(method, path, body).Body.Bytes(), &out) // a reply that is no JSON reads as empty
	return out
}

// frame is the newest frame after since in wire v2.
func (r *webRig) frame(since uint64) (wire.Frame, error) {
	return wire.DecodeFrame2(r.do(http.MethodGet, "/api/frame.bin?v=2&waitms=1500&since="+jsonText(since), "").Body.Bytes())
}

func jsonText(v any) string { b, _ := json.Marshal(v); return string(b) }

// settled waits for a frame fn accepts (a change is committed at the owner
// loop's next pass).
func (r *webRig) settled(fn func(wire.Frame) bool) (wire.Frame, bool) {
	var f wire.Frame
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		g, err := r.frame(f.Header.Seq)
		if err != nil {
			continue
		}
		f = g
		if !f.Header.Unchanged && fn(f) {
			return f, true
		}
	}
	return f, false
}

// trace is channel ch's trace of f, nil when f has none.
func trace(f wire.Frame, ch int) *wire.FrameTrace {
	for i := range f.Traces {
		if f.Traces[i].Ch == ch {
			return &f.Traces[i]
		}
	}
	return nil
}

// swing is a trace's lowest and highest volts.
func swing(tr *wire.FrameTrace) (lo, hi float64) {
	lo, hi = math.Inf(1), math.Inf(-1)
	for i := range tr.Codes.Len() {
		v := tr.Volts(tr.Codes, i)
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	return lo, hi
}

// TestWeb serves the shared web API over this board in the simulator: the
// core's conformance suite, the capabilities from the board's HAL, v2
// frames that decode to the injected signals (C1 a 2 Vpp square, C2 a
// 1 Vpp sine), and settings that reach the board through the instrument
// state, with refusals that say why.
func TestWeb(t *testing.T) {
	n := board.Profile().Channels
	st := scpi.Defaults(n)
	ctl := instrument.New(st, nil)
	frames := fanout.NewFrames()
	ctl.SetFrames(frames)
	loop, cycle := engine.NewHALLoop(engine.HALConfig{Board: board, Instrument: ctl, Logf: logger.Printf})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, 2)
	go func() { loop.Run(ctx); done <- struct{}{} }()
	go func() { frames.Run(cycle, ctx.Done()); done <- struct{}{} }()
	defer func() { cancel(); <-done; <-done }()
	sc := web.Scope(board, ctl, frames, "boardtest", nil, nil, scopeweb.Parts{})
	r := &webRig{ctl: ctl, h: webserver.New(sc).Handler(), sc: sc}

	c := begin(t, "web/first-frame")
	if !c.check(frames.WaitNext(0, 30*time.Second) != 0, "the loop published a frame") {
		return
	}

	c = begin(t, "web/caps")
	var caps wire.Caps
	err := json.Unmarshal(r.do(http.MethodGet, "/api/caps", "").Body.Bytes(), &caps)
	c.check(err == nil && len(caps.Channels) == n && caps.Codes.Bits == 12 && caps.Codes.Storage == "u16" &&
		caps.Codes.Shift == 4 && caps.Codes.CodesPerDiv == 7680, "%d channels, %d-bit %s codes shift %d, %g a division (%v)",
		len(caps.Channels), caps.Codes.Bits, caps.Codes.Storage, caps.Codes.Shift, caps.Codes.CodesPerDiv, err)
	c.check(len(caps.Wire.Frame) == 1 && caps.Wire.Frame[0] == 2, "frame wire %v", caps.Wire.Frame)
	c.check(caps.Features["stacker"].Why != "" && caps.Features["counter"].OK && caps.Features["segments"].OK && caps.Features["streamer"].OK,
		"stacker absent (%q), counter, segments and streamer present", caps.Features["stacker"].Why)
	c.check(len(caps.Vertical.Vdivs) > 10 && len(caps.Timebase.Tdivs) > 20 && caps.Screen.W == 1024 && caps.Screen.H == 600,
		"%d V/div steps, %d time/div steps, screen %dx%d", len(caps.Vertical.Vdivs), len(caps.Timebase.Tdivs), caps.Screen.W, caps.Screen.H)

	c = begin(t, "web/frames")
	r.json(http.MethodPost, "/api/set", `{"control":"on","ch":1,"value":1}`)
	f, ok := r.settled(func(f wire.Frame) bool { return trace(f, 0) != nil && trace(f, 1) != nil })
	if c.check(ok, "C1 and C2 in a v2 frame (%d traces)", len(f.Traces)) {
		lo1, hi1 := swing(trace(f, 0))
		lo2, hi2 := swing(trace(f, 1))
		c.check(near(lo1, -1, 0.1) && near(hi1, 1, 0.1), "C1 %.3f … %.3f V, want the ±1 V square", lo1, hi1)
		c.check(near(lo2, -0.5, 0.1) && near(hi2, 0.5, 0.1), "C2 %.3f … %.3f V, want the ±0.5 V sine", lo2, hi2)
		m := trace(f, 0).Meas
		c.check(m != nil && near(m.Vpp, 2, 0.2), "C1 measured Vpp %v", m)
	}

	c = begin(t, "web/settings")
	set := func(body string) map[string]any { return r.json(http.MethodPost, "/api/set", body) }
	out := set(`{"control":"vdiv2","value":0.5}`)
	c.check(out["ok"] == true && out["applied"] == 0.5, "C2 to 0.5 V/div by its v1 name: %v", out)
	var zero0 float64
	if t1 := trace(f, 0); t1 != nil {
		zero0 = t1.Scale.Zero
	}
	out = set(`{"control":"offset","ch":0,"value":0.5}`)
	offApplied, _ := out["applied"].(float64)
	c.check(out["ok"] == true && near(offApplied, 0.5, 0.01), "C1 offset 0.5 V by the v2 body, on the offset grid: %v", out)
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline) && board.FrontEnd().Get(1).VoltsDiv != 0.5; {
		time.Sleep(50 * time.Millisecond)
	}
	c.check(board.FrontEnd().Get(1).VoltsDiv == 0.5, "the board's C2 is at %g V/div", board.FrontEnd().Get(1).VoltsDiv)
	f, ok = r.settled(func(f wire.Frame) bool {
		t1 := trace(f, 0)
		return t1 != nil && t1.Scale.Zero != zero0
	})
	if c.check(ok, "a frame at the new offset") {
		lo1, hi1 := swing(trace(f, 0))
		c.check(near(lo1, -1, 0.1) && near(hi1, 1, 0.1), "C1 still decodes to %.3f … %.3f V at the tip", lo1, hi1)
	}
	status := r.json(http.MethodGet, "/api/status", "")
	c.check(status["vdiv2"] == 0.5 && status["off1_v"] == offApplied, "status vdiv2 %v off1_v %v", status["vdiv2"], status["off1_v"])
	set(`{"control":"offset","ch":0,"value":0}`)

	c = begin(t, "web/refusals")
	for _, body := range []string{
		`{"control":"vdiv","ch":1,"value":0.3}`,
		`{"control":"vdiv","ch":9,"value":1}`,
		`{"control":"trigtype","value":1}`,
	} {
		out := set(body)
		c.check(out["ok"] == false && out["err"] != "", "%s refused: %v", body, out["err"])
	}
	rec := r.do(http.MethodGet, "/api/frame.bin?cols=100", "")
	c.check(rec.Code == http.StatusOK && strings.HasPrefix(rec.Body.String(), string(wire.FrameMagic2[:])), "a v1 frame request is served in v2: %d", rec.Code)

	c = begin(t, "web/conformance")
	c.check(t.Run("conformance", func(t *testing.T) {
		webtest.Run(t, func() webserver.Scope { return sc })
	}), "the core's web conformance suite")
}
