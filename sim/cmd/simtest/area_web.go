package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/labeth/open-dso-core/webserver/wire"

	"github.com/labeth/open-sds800xhd/sim/harness"
)

// The web area serves the shared web page and API from this scope: the
// in-guest board test drives the core's web scope over the board (the
// conformance suite, caps, v2 frames, settings), and the app's run serves
// the page on the published port, where the host loads it, reads caps and
// frames, sets controls and checks that SCPI and the web agree.
//
// A second app run loads the phone remote (/remote) at a phone's viewport
// and sweeps its Menus, and checks the record download (area_web_remote.go).
func init() {
	register(area{
		name: "web", title: "web: page, caps, frames v2, settings, web and SCPI agree, phone remote, record.bin",
		runs: func(e *env) []harness.Spec {
			return []harness.Spec{
				{
					Name: e.prefix + "web-board", Secs: 240, SimdArgs: signals,
					Board: e.boardtest, BoardArgs: boardArgs("^TestWeb$"),
				},
				{
					Name: e.prefix + "web", Secs: 150, Ports: true, SimdArgs: signals,
					App: e.app, Hook: e.hook, GuestBoot: e.guestBoot,
					Host: func(ctx context.Context, dir string) { webHost(ctx, e, dir) },
				},
				{
					Name: e.prefix + "web-remote", Secs: 300, Ports: true, SimdArgs: signals,
					App: e.app, Hook: e.hook, GuestBoot: e.guestBoot, Host: webRemoteHost,
				},
			}
		},
		check: func(_ *env, runs []*harness.Run) []harness.Result {
			r := runs[0]
			res := harness.GuestResults(r, "web")
			res = append(res, harness.Missing(r, res, "web")...)
			if !strings.Contains(r.Console, "--- PASS: TestWeb") {
				res = append(res, harness.Fail("web", "go test", "TestWeb did not pass in the guest"))
			}
			res = append(res, hostResults(runs[1])...)
			return append(res, hostResultsOf(runs[2], "web", webRemoteFile)...)
		},
	})
}

// webHostFile is where the host's checks of the app run land, in the run's
// directory, so a -reuse check reads them again.
const webHostFile = "web-host.txt"

// webBase is the app's web port as the simulator publishes it.
const webBase = "http://127.0.0.1:8080"

// hostResults reads the host's checks of a run.
func hostResults(r *harness.Run) []harness.Result {
	b, err := os.ReadFile(r.File(webHostFile))
	if err != nil {
		return []harness.Result{harness.Fail("web", "host", "no host checks: %v", err)}
	}
	var out []harness.Result
	for l := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		f := strings.SplitN(l, " ", 3)
		if len(f) < 2 {
			continue
		}
		d := ""
		if len(f) == 3 {
			d = f[2]
		}
		out = append(out, harness.Result{Area: "web", Case: f[0], Pass: f[1] == passWord, Detail: d})
	}
	if len(out) == 0 {
		return []harness.Result{harness.Fail("web", "host", "the host checks reported nothing")}
	}
	return out
}

// hostLog writes the host's results as lines "CASE PASS|FAIL detail".
type hostLog struct{ w io.Writer }

// check writes one result.
func (h hostLog) check(c string, ok bool, format string, a ...any) bool {
	v := passWord
	if !ok {
		v = "FAIL"
	}
	_, _ = fmt.Fprintf(h.w, "%s %s %s\n", c, v, fmt.Sprintf(format, a...)) // a file the check reads; a lost line is a missing case
	return ok
}

// webHost runs the host's checks against the app while it runs: the page,
// caps, v2 frames of the injected signals, settings both ways between the
// web and SCPI, and the page in a browser.
func webHost(ctx context.Context, e *env, dir string) {
	f, err := os.Create(filepath.Join(dir, webHostFile)) //nolint:gosec // G304: the run's own directory
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	h := hostLog{f}
	cl := &http.Client{Timeout: 10 * time.Second}
	do := func(method, path, body string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, webBase+path, strings.NewReader(body))
		if err != nil {
			return nil, err
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		return cl.Do(req)
	}
	up := false
	for !up && ctx.Err() == nil {
		if r, err := do(http.MethodGet, "/api/status", ""); err == nil {
			_ = r.Body.Close()
			up = r.StatusCode == http.StatusOK
		}
		if !up {
			time.Sleep(time.Second)
		}
	}
	if !h.check("serving", up, "the app's web server answered on the published port") {
		return
	}
	get := func(path string) ([]byte, int) {
		r, err := do(http.MethodGet, path, "")
		if err != nil {
			return nil, 0
		}
		defer func() { _ = r.Body.Close() }()
		b, _ := io.ReadAll(r.Body)
		return b, r.StatusCode
	}
	post := func(path, body string) map[string]any {
		r, err := do(http.MethodPost, path, body)
		if err != nil {
			return map[string]any{"err": err.Error()}
		}
		defer func() { _ = r.Body.Close() }()
		var out map[string]any
		_ = json.NewDecoder(r.Body).Decode(&out) // a reply that is no JSON reads as empty
		return out
	}

	page, code := get("/")
	h.check("page", code == http.StatusOK && strings.Contains(string(page), "<html"), "GET / %d, %d bytes", code, len(page))
	_, code = get("/app_core.js")
	h.check("asset", code == http.StatusOK, "GET /app_core.js %d", code)

	var caps wire.Caps
	b, code := get("/api/caps")
	err = json.Unmarshal(b, &caps)
	h.check("caps", err == nil && len(caps.Channels) == 4 && caps.Codes.Bits == 12 && caps.Codes.CodesPerDiv == 7680 &&
		len(caps.Wire.Frame) == 1 && caps.Wire.Frame[0] == 2,
		"%d: %d channels, %d-bit %s codes, %g a division, frame wire %v (%v)", code, len(caps.Channels), caps.Codes.Bits,
		caps.Codes.Storage, caps.Codes.CodesPerDiv, caps.Wire.Frame, err)
	var feats []string
	for name, ft := range caps.Features {
		if ft.OK {
			feats = append(feats, name)
		}
	}
	h.check("caps-features", caps.Features["counter"].OK && caps.Features["segments"].OK && !caps.Features["stacker"].OK && caps.Features["stacker"].Why != "",
		"present %v; stacker: %s", feats, caps.Features["stacker"].Why)

	// C2 on through the web, then both injected signals in v2 frames
	post("/api/set", `{"control":"on","ch":1,"value":1}`)
	fr, ok := webFrame(ctx, get, func(fr wire.Frame) bool { return traceOf(fr, 0) != nil && traceOf(fr, 1) != nil })
	if h.check("frames", ok, "a v2 frame with C1 and C2 (%d traces)", len(fr.Traces)) {
		lo1, hi1 := swingOf(traceOf(fr, 0))
		lo2, hi2 := swingOf(traceOf(fr, 1))
		h.check("frame-c1", math.Abs(lo1+1) < 0.1 && math.Abs(hi1-1) < 0.1, "C1 decodes to %.3f … %.3f V (the ±1 V square)", lo1, hi1)
		h.check("frame-c2", math.Abs(lo2+0.5) < 0.1 && math.Abs(hi2-0.5) < 0.1, "C2 decodes to %.3f … %.3f V (the ±0.5 V sine)", lo2, hi2)
		if m := traceOf(fr, 0).Meas; h.check("frame-meas", m != nil, "C1 carries measurements") {
			h.check("frame-freq", math.Abs(m.Freq-5e6) < 5e4, "C1 measured %.6g Hz (5 MHz injected)", m.Freq)
		}
	}

	// the web sets, SCPI reads back; SCPI sets, the web reads back
	out := post("/api/set", `{"control":"vdiv2","value":0.5}`)
	h.check("set-web", out["ok"] == true, "C2 to 0.5 V/div from the page: %v", out)
	var reply string
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && ctx.Err() == nil; time.Sleep(200 * time.Millisecond) {
		if reply, err = scpiQuery(ctx, "C2:VDIV?"); err == nil && strings.Contains(reply, "5.00E-01") {
			break
		}
	}
	h.check("scpi-reads-web", strings.Contains(reply, "5.00E-01"), "C2:VDIV? after the page set it: %q (%v)", reply, err)
	_, err = scpiQuery(ctx, ":CHAN1:SCAL 0.5")
	var st map[string]any
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && ctx.Err() == nil; time.Sleep(200 * time.Millisecond) {
		b, _ = get("/api/status")
		_ = json.Unmarshal(b, &st) // a status that is no JSON reads as empty
		if st["vdiv1"] == 0.5 {
			break
		}
	}
	h.check("web-reads-scpi", st["vdiv1"] == 0.5, "the page's status after :CHAN1:SCAL 0.5: vdiv1 %v (%v)", st["vdiv1"], err)
	fr, ok = webFrame(ctx, get, func(fr wire.Frame) bool {
		t := traceOf(fr, 0)
		return t != nil && math.Abs(t.Scale.PerCodeV*7680-0.5) < 1e-6
	})
	if h.check("frame-follows", ok, "a frame at 0.5 V/div on C1") {
		lo, hi := swingOf(traceOf(fr, 0))
		h.check("frame-volts", math.Abs(lo+1) < 0.1 && math.Abs(hi-1) < 0.1, "C1 still %.3f … %.3f V at the tip", lo, hi)
	}
	_, _ = scpiQuery(ctx, ":CHAN1:SCAL 1")
	out = post("/api/set", `{"control":"vdiv","ch":3,"value":0.3}`)
	h.check("refused", out["ok"] == false && out["err"] != "", "0.3 V/div refused: %v", out["err"])
	b, code = get("/api/frame.bin?cols=100")
	h.check("v1-served-v2", code == http.StatusOK && strings.HasPrefix(string(b), string(wire.FrameMagic2[:])), "a v1 frame request: %d, magic %.4q", code, b)

	browserPage(ctx, e, h)
}

// webFrame polls v2 frames until one satisfies fn (a change is committed
// at the owner loop's next pass), for up to 20 s.
func webFrame(ctx context.Context, get func(string) ([]byte, int), fn func(wire.Frame) bool) (wire.Frame, bool) {
	var fr wire.Frame
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline) && ctx.Err() == nil; {
		b, code := get(fmt.Sprintf("/api/frame.bin?v=2&waitms=1500&since=%d", fr.Header.Seq))
		if code != http.StatusOK {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		g, err := wire.DecodeFrame2(b)
		if err != nil {
			continue
		}
		fr = g
		if !fr.Header.Unchanged && fn(fr) {
			return fr, true
		}
	}
	return fr, false
}

// traceOf is channel ch's trace, nil when the frame has none.
func traceOf(fr wire.Frame, ch int) *wire.FrameTrace {
	for i := range fr.Traces {
		if fr.Traces[i].Ch == ch {
			return &fr.Traces[i]
		}
	}
	return nil
}

// swingOf is a trace's lowest and highest volts.
func swingOf(t *wire.FrameTrace) (lo, hi float64) {
	lo, hi = math.Inf(1), math.Inf(-1)
	for i := range t.Codes.Len() {
		v := t.Volts(t.Codes, i)
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	return lo, hi
}

// scpiQuery sends one line over the raw SCPI socket and reads the reply of
// a query (a command has none).
func scpiQuery(ctx context.Context, line string) (string, error) {
	d := net.Dialer{Timeout: 3 * time.Second}
	c, err := d.DialContext(ctx, "tcp", "127.0.0.1:5025")
	if err != nil {
		return "", err
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, line+"\n"); err != nil {
		return "", err
	}
	if !strings.Contains(line, "?") {
		return "", nil
	}
	s, err := bufio.NewReader(c).ReadString('\n')
	return strings.TrimSpace(s), err
}

// pageRun is what the core's page driver reports of the page.
type pageRun struct {
	Chans []int `json:"chans"`
	Wire  int   `json:"wire"`
	Drawn map[string]struct {
		N   int     `json:"n"`
		Lo  float64 `json:"lo"`
		Hi  float64 `json:"hi"`
		Vpc float64 `json:"vpc"`
		Off float64 `json:"off"`
	} `json:"drawn"`
	Vdiv1  []any    `json:"vdiv1"`
	Errors []string `json:"errors"`
	Failed []string `json:"failed"`
}

// browserPage loads the shared page in a headless browser against the app
// (the core's page driver, webclient/pageload_browser.mjs): four channels
// built from caps, frames read in wire v2 and drawn at the injected
// signals' volts, a V/div step on C1 that reaches the scope, and no page
// error or failed API reply. Without node or Playwright the case says so
// (a failure with SIM_REQUIRE_BROWSER set).
func browserPage(ctx context.Context, e *env, h hostLog) {
	core := os.Getenv("DSO_CORE_DIR")
	if core == "" {
		core = filepath.Join(e.root, "..", "open-dso-core")
	}
	driver := filepath.Join(core, "webclient", "pageload_browser.mjs")
	if _, err := os.Stat(driver); err != nil { //nolint:gosec // G703: the core checkout's own driver, next to this repository or named by the harness's user
		h.check("browser", false, "no page driver at %s", driver)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "node", driver, webBase).CombinedOutput() //nolint:gosec // G204: the core's own driver against the simulated scope
	text := strings.TrimSpace(string(out))
	if strings.HasPrefix(text, "SKIP") {
		h.check("browser", os.Getenv("SIM_REQUIRE_BROWSER") == "", "the page was not loaded in a browser: %s", text)
		return
	}
	var run pageRun
	if jerr := json.Unmarshal([]byte(lastLine(text)), &run); jerr != nil {
		h.check("browser", false, "the driver's report: %v (%v): %.400s", jerr, err, text)
		return
	}
	h.check("browser-channels", len(run.Chans) == 4, "the page built channels %v from caps", run.Chans)
	h.check("browser-wire", run.Wire == 2, "the page read frame wire %d", run.Wire)
	for _, c := range []struct {
		k    string
		want float64
	}{{"c1", 1}, {"c2", 0.5}} {
		d, ok := run.Drawn[c.k]
		top, bottom := (d.Hi-128)*d.Vpc-d.Off, (d.Lo-128)*d.Vpc-d.Off
		h.check("browser-"+c.k, ok && d.N > 0 && math.Abs(top-c.want) < 0.1*c.want+0.05 && math.Abs(bottom+c.want) < 0.1*c.want+0.05,
			"%s drawn from %d values, %.3f … %.3f V (±%g V injected)", strings.ToUpper(c.k), d.N, bottom, top, c.want)
	}
	h.check("browser-control", len(run.Vdiv1) == 2 && run.Vdiv1[0] != run.Vdiv1[1], "C1's V/div step on the page: the scope went %v", run.Vdiv1)
	h.check("browser-errors", len(run.Errors) == 0 && len(run.Failed) == 0, "page errors %v, failed replies %v", run.Errors, run.Failed)
}

// lastLine is the last line of s.
func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}
