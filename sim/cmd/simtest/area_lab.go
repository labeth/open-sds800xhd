package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/labeth/open-sds800xhd/sim/harness"
	"github.com/labeth/open-sds800xhd/sim/lab"
)

// The lab area is a smoke test of simlab, the virtual bench: simlab
// attaches to the app's run and the host drives its page API as the page
// does: the bench state with the key LEDs, the screen, a generator change
// the app measures, a key the app lights and reports over SCPI, a knob turn
// that moves the selected channel's V/div, a touch, and a protocol on two
// channels.
func init() { //nolint:dupl // each host-driven area's spec is its own
	register(area{
		name: "lab", title: "simlab bench: generator, keys, knobs, touch, screen through its API",
		runs: func(e *env) []harness.Spec {
			return []harness.Spec{{
				Name: e.prefix + "lab", Secs: 180, Ports: true, SimdArgs: signals,
				App: e.app, Hook: e.hook, GuestBoot: e.guestBoot,
				Host: func(ctx context.Context, dir string) { labHost(ctx, dir) },
			}}
		},
		check: func(_ *env, runs []*harness.Run) []harness.Result {
			return hostResultsOf(runs[0], "lab", labHostFile)
		},
	})
}

// labHostFile is where the host's checks of the lab run land.
const labHostFile = "lab-host.txt"

// labClient drives simlab's API.
type labClient struct {
	ctx  context.Context //nolint:containedctx // the client lives exactly as long as the host function's run
	base string
}

func (l labClient) post(path, body string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(l.ctx, http.MethodPost, l.base+path, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Body.Close() }()
	var out map[string]any
	if err := json.NewDecoder(r.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out["ok"] != true {
		return out, fmt.Errorf("%s: %v", path, out["err"])
	}
	return out, nil
}

func (l labClient) get(path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(l.ctx, http.MethodGet, l.base+path, nil)
	if err != nil {
		return nil, err
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Body.Close() }()
	b, err := io.ReadAll(r.Body)
	if r.StatusCode != http.StatusOK {
		return b, fmt.Errorf("%s: %s %s", path, r.Status, b)
	}
	return b, err
}

func (l labClient) state() (lab.State, error) {
	var st lab.State
	b, err := l.get("/api/state")
	if err == nil {
		err = json.Unmarshal(b, &st)
	}
	return st, err
}

// until polls f every 300 ms until it reports done or d passes.
func until(ctx context.Context, d time.Duration, f func() bool) bool {
	end := time.Now().Add(d)
	for {
		if f() {
			return true
		}
		if time.Now().After(end) || ctx.Err() != nil {
			return false
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// measFreq is C1's frequency as the app measures it over SCPI.
func measFreq(ctx context.Context) (float64, string) {
	if _, err := scpiQuery(ctx, ":MEASure:SIMPle:SOURce C1"); err != nil {
		return 0, err.Error()
	}
	rep, err := scpiQuery(ctx, ":MEASure:SIMPle:VALue? FREQ")
	if err != nil {
		return 0, err.Error()
	}
	f, _ := strconv.ParseFloat(strings.TrimSpace(rep), 64)
	return f, rep
}

// vdivOf is channel ch's V/div over SCPI.
func vdivOf(ctx context.Context, ch string) float64 {
	rep, err := scpiQuery(ctx, ch+":VDIV?")
	if err != nil {
		return 0
	}
	f := strings.Fields(strings.TrimSuffix(rep, "V"))
	v, _ := strconv.ParseFloat(f[len(f)-1], 64)
	return v
}

func labHost(ctx context.Context, dir string) {
	f, err := os.Create(filepath.Join(dir, labHostFile)) //nolint:gosec // G304: the run's own directory
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	h := hostLog{f}
	run := lab.NewRunner(nil)
	go run.Attach(ctx, dir)
	tmp, err := os.MkdirTemp("", "simlab")
	if err != nil {
		h.check("setup", false, "%v", err)
		return
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	srv := lab.NewServer(run, tmp)
	go srv.Serve(ctx)
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	l := labClient{ctx: ctx, base: hs.URL}

	up := until(ctx, 90*time.Second, func() bool {
		r, err := http.Get(webBase + "/api/status") //nolint:noctx // bounded by the client's default transport and the loop
		if err != nil {
			return false
		}
		_ = r.Body.Close()
		return r.StatusCode == http.StatusOK
	})
	if !h.check("serving", up, "the app's web server answered on the published port") {
		return
	}

	var st lab.State
	ok := until(ctx, 10*time.Second, func() bool { st, err = l.state(); return err == nil && st.LEDWord != 0 })
	h.check("state", ok && st.Run.State == "attached", "run %s %s, LED word %#x lamps %v (%v)", st.Run.Name, st.Run.State, st.LEDWord, st.Lamps, err)
	h.check("signals", st.Signals["C1"] == "square,5e6,2,0", "the bench reads the model's inputs: %v", st.Signals)

	b, err := l.get("/api/screen.png")
	if err == nil {
		img, derr := png.Decode(bytes.NewReader(b))
		err = derr
		if err == nil && (img.Bounds().Dx() != 1024 || img.Bounds().Dy() != 600) {
			err = fmt.Errorf("%v", img.Bounds())
		}
	}
	h.check("screen", err == nil, "the device model's screen, 1024x600 PNG of %d bytes (%v)", len(b), err)

	// The generator: C1 from 5 MHz to 2 MHz, which the app measures.
	f0, r0 := 0.0, ""
	until(ctx, 20*time.Second, func() bool { f0, r0 = measFreq(ctx); return math.Abs(f0-5e6) < 0.05e6 })
	_, err = l.post("/api/signal", `{"ch":"C1","spec":"square,2e6,2,0"}`)
	f1, r1 := 0.0, ""
	ok = err == nil && until(ctx, 20*time.Second, func() bool { f1, r1 = measFreq(ctx); return math.Abs(f1-2e6) < 0.05e6 })
	h.check("generator", ok, "C1 measured %q before, %q after the bench set a 2 MHz square (%v)", r0, r1, err)

	// A key: CH2 switches C2 on, selects it and lights its lamp.
	_, err = l.post("/api/key", `{"key":"ch2"}`)
	ok = err == nil && until(ctx, 10*time.Second, func() bool {
		st, _ = l.state()
		return slices.Contains(st.Lamps, "ch2")
	})
	tra, _ := scpiQuery(ctx, "C2:TRA?")
	h.check("key", ok && strings.Contains(tra, "ON"), "CH2 pressed: lamps %v, C2:TRA? %q (%v)", st.Lamps, tra, err)

	// A knob: the vertical scale moves the selected channel's V/div one
	// step, and back.
	v0 := vdivOf(ctx, "C2")
	_, err = l.post("/api/knob", `{"knob":"vscale","steps":1}`)
	v1 := v0
	ok = err == nil && until(ctx, 10*time.Second, func() bool { v1 = vdivOf(ctx, "C2"); return v1 > 0 && v1 != v0 })
	h.check("knob", ok, "V-scale +1 with C2 selected: %g -> %g V/div (%v)", v0, v1, err)
	_, err = l.post("/api/knob", `{"knob":"vscale","steps":-1}`)
	v2 := v1
	ok = err == nil && until(ctx, 10*time.Second, func() bool { v2 = vdivOf(ctx, "C2"); return v2 == v0 })
	h.check("knob-back", ok, "V-scale -1: %g -> %g V/div (%v)", v1, v2, err)

	// A touch reaches the touch controller.
	_, err = l.post("/api/touch", `{"x":512,"y":300}`)
	h.check("touch", err == nil, "touch at (512, 300): %v", err)

	// A protocol on two channels.
	_, err = l.post("/api/bus", `{"kind":"i2c","lines":{"scl":"C3","sda":"C4"},"rate":1e5,"vpp":3.3,"offset":1.65,"opts":{"addr":"0x50","data":"00a5"}}`)
	st, _ = l.state()
	h.check("bus", err == nil && strings.HasPrefix(st.Signals["C3"], "i2c.scl,") && strings.HasPrefix(st.Signals["C4"], "i2c.sda,"),
		"I2C on C3/C4: %q, %q (%v)", st.Signals["C3"], st.Signals["C4"], err)
}
