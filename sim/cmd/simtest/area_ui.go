package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/labeth/open-dso-core/uimodel"

	"github.com/labeth/open-sds800xhd/sim/harness"
)

// The ui area runs the app with the core's on-device UI: the host presses
// the panel's keys, turns its knobs and touches the screen through the
// simulator's control socket, dumps fb0 after each step, and checks that
// the UI model moved as the CML's does (workspace, sheet, tab, run state),
// that the UI layer leaves the waveform plane transparent and draws the
// rest, and that what a key or knob set reached the board and reads the
// same over SCPI and the web.
func init() {
	register(appHostArea("ui", "on-device UI: keys, knobs, touch, fb0, settings reach the board", 420, uiSignals, uiHostFile,
		func(ctx context.Context, dir string) { uiHost(ctx, dir) }))
}

// modeWorkspace is the UI model's mode with no sheet or picker open.
const modeWorkspace = "workspace"

// uiHostFile is where the host's checks of the ui run land.
const uiHostFile = "ui-host.txt"

// uiSignals are the board areas' inputs with C3 and C4 driven too: a
// 2 MHz square of 1 V peak to peak on C3 and a 2 MHz sine of 1 V on C4,
// which the channels past two are measured on and the frequency response
// is taken between.
const uiSignals = signals + ";C3=square,2e6,1,0;C4=sine,2e6,1,0"

// hostResultsOf reads the host's checks of a run as area's results.
func hostResultsOf(r *harness.Run, area, file string) []harness.Result {
	b, err := os.ReadFile(r.File(file))
	if err != nil {
		return []harness.Result{harness.Fail(area, "host", "no host checks: %v", err)}
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
		out = append(out, harness.Result{Area: area, Case: f[0], Pass: f[1] == passWord, Detail: d})
	}
	if len(out) == 0 {
		return []harness.Result{harness.Fail(area, "host", "the host checks reported nothing")}
	}
	return out
}

// uiRig drives the running simulator and the app for as long as the run's
// context lasts (the host function's whole life).
type uiRig struct {
	ctx context.Context //nolint:containedctx // the rig lives exactly as long as the host function's run
	dir string
	h   hostLog
	cl  *http.Client
}

// ctl sends one control line to the simulator and returns its reply.
func (u *uiRig) ctl(line string) string {
	d := net.Dialer{Timeout: 3 * time.Second}
	c, err := d.DialContext(u.ctx, "unix", filepath.Join(u.dir, "sock", "ctl.sock"))
	if err != nil {
		return "err " + err.Error()
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(c, line+"\n"); err != nil {
		return "err " + err.Error()
	}
	s, _ := bufio.NewReader(c).ReadString('\n')
	return strings.TrimSpace(s)
}

// post sends a JSON body to the app and decodes its reply into v.
func (u *uiRig) post(path, body string, v any) error {
	req, err := http.NewRequestWithContext(u.ctx, http.MethodPost, webBase+path, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	r, err := u.cl.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = r.Body.Close() }()
	return json.NewDecoder(r.Body).Decode(v)
}

// setRow sets a sheet row through /api/sheets as a page does.
func (u *uiRig) setRow(row string, value any) uimodel.SetResult {
	b, _ := json.Marshal(map[string]any{"row": row, "value": value}) // a map of a string and a number always encodes
	var res uimodel.SetResult
	if err := u.post("/api/sheets/set", string(b), &res); err != nil {
		res.Error = err.Error()
	}
	return res
}

// sheetRow is row id of the web's sheets.
func (u *uiRig) sheetRow(id string) (uimodel.RowModel, []string) {
	var sheets uimodel.SheetsModel
	_ = u.get("/api/sheets", &sheets) // unreadable sheets have no row
	var tabs []string
	var out uimodel.RowModel
	for _, t := range sheets.Tabs {
		tabs = append(tabs, t.ID)
		for _, r := range t.Rows {
			if r.ID == id {
				out = r
			}
		}
	}
	return out, tabs
}

// scpiUntil polls a SCPI query until ok accepts its reply, for up to 20 s.
func (u *uiRig) scpiUntil(q string, ok func(string) bool) (string, bool) {
	var reply string
	got := u.until(func() bool {
		var err error
		reply, err = scpiQuery(u.ctx, q)
		return err == nil && ok(reply)
	})
	return reply, got
}

// get fetches a JSON document from the app into v.
func (u *uiRig) get(path string, v any) error {
	req, err := http.NewRequestWithContext(u.ctx, http.MethodGet, webBase+path, nil)
	if err != nil {
		return err
	}
	r, err := u.cl.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = r.Body.Close() }()
	if r.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %d", path, r.StatusCode)
	}
	return json.NewDecoder(r.Body).Decode(v)
}

// panel is the UI model's snapshot.
func (u *uiRig) panel() uimodel.Status {
	var st uimodel.Status
	_ = u.get("/api/panel/wait?seq=0&timeout_ms=1", &st) // an unreadable snapshot reads as empty
	return st
}

// status is the app's status document.
func (u *uiRig) status() map[string]any {
	var st map[string]any
	_ = u.get("/api/status", &st) // an unreadable status reads as empty
	return st
}

// until polls fn for up to 20 s.
func (u *uiRig) until(fn func() bool) bool {
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline) && u.ctx.Err() == nil; time.Sleep(250 * time.Millisecond) {
		if fn() {
			return true
		}
	}
	return false
}

// shot dumps fb0 and reads it back (the dump may land a moment after the
// reply).
func (u *uiRig) shot(name string) (*image.NRGBA, string) {
	rep := u.ctl("shotfb 0 " + name)
	var err error
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		var img image.Image
		if img, err = readPNG(filepath.Join(u.dir, "shots", name)); err == nil {
			n := image.NewNRGBA(img.Bounds())
			draw.Draw(n, n.Rect, img, img.Bounds().Min, draw.Src)
			return n, rep
		}
	}
	return nil, rep + ": " + err.Error()
}

// readPNG decodes a PNG file.
func readPNG(path string) (image.Image, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the run's own shot
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // read only
	return png.Decode(f)
}

// The panel's matrix positions (spec 08 §2.2) and touch places the host
// uses: row, bit and, for a knob, its steps.
const (
	keyMenu   = "key 0 7"
	keyCh1    = "key 0 3"
	keyCh3    = "key 0 5"
	keyMeas   = "key 1 5"
	keySingle = "key 7 3"
	keyRun    = "key 5 5"
	knobVcw   = "key 0 0 3" // vertical scale, 3 steps
	knobHcw   = "key 2 0 1" // horizontal scale, a step
)

// uiHost runs the host's side of the ui area.
func uiHost(ctx context.Context, dir string) {
	f, err := os.Create(filepath.Join(dir, uiHostFile)) //nolint:gosec // G304: the run's own directory
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	u := &uiRig{ctx: ctx, dir: dir, h: hostLog{f}, cl: &http.Client{Timeout: 10 * time.Second}}
	h := u.h
	if !h.check("serving", u.until(func() bool { return u.panel().Workspace != "" }), "the app's UI model answered on the published port") {
		return
	}
	st := u.panel()
	h.check("start", st.Workspace == "SCOPE" && st.Mode == modeWorkspace, "the UI starts in %s, %s", st.Workspace, st.Mode)

	// fb0: the UI drawn around the waveform plane, the plane transparent
	img, rep := u.shot("ui-scope.png")
	if h.check("fb0", img != nil && img.Rect.Dx() == 1024 && img.Rect.Dy() == 600, "fb0 dumped: %s", rep) {
		inside, outside := img.NRGBAAt(500, 300).A, img.NRGBAAt(1015, 5).A
		h.check("fb0-plane", inside == 0 && outside == 0xFF, "the plane's centre alpha %d, the UI's corner alpha %d", inside, outside)
	}

	// MENU opens the workspace's sheet; S/DIV turns its tab; MENU closes it
	u.ctl(keyMenu)
	h.check("menu-sheet", u.until(func() bool { s := u.panel(); return s.Mode == "sheet" && s.Sheet.Open }), "MENU opened %+v", u.panel().Sheet.Tab)
	tab := u.panel().Sheet.Tab
	sheetImg, _ := u.shot("ui-sheet.png")
	changed := differs(img, sheetImg)
	h.check("fb0-sheet", changed > 1000, "the sheet changed %d pixels of fb0", changed)
	u.ctl(knobHcw)
	h.check("sheet-tab", u.until(func() bool { return u.panel().Sheet.Tab != tab }), "S/DIV in the sheet: tab %s to %s", tab, u.panel().Sheet.Tab)
	u.ctl(keyMenu)
	h.check("menu-close", u.until(func() bool { return u.panel().Mode == modeWorkspace }), "MENU again: %s", u.panel().Mode)

	// the knobs reach the board: time/div and C1's V/div, read over SCPI and the web
	tdiv0 := u.status()["tdiv_set_s"]
	t0, _ := scpiQuery(ctx, "TDIV?")
	u.ctl(knobHcw)
	h.check("knob-tdiv", u.until(func() bool { return u.status()["tdiv_set_s"] != tdiv0 }), "S/DIV: tdiv %v to %v", tdiv0, u.status()["tdiv_set_s"])
	h.check("knob-tdiv-scpi", u.until(func() bool { t, _ := scpiQuery(ctx, "TDIV?"); return t != t0 && t != "" }), "TDIV? %q before", t0)
	u.ctl(keyCh1)
	v0 := u.status()["vdiv1"]
	c0, _ := scpiQuery(ctx, "C1:VDIV?")
	u.ctl(knobVcw)
	h.check("knob-vdiv", u.until(func() bool { return u.status()["vdiv1"] != v0 }), "V/DIV on CH1: %v to %v", v0, u.status()["vdiv1"])
	h.check("knob-vdiv-scpi", u.until(func() bool { c, _ := scpiQuery(ctx, "C1:VDIV?"); return c != c0 && c != "" }), "C1:VDIV? %q before", c0)

	// SINGLE takes one shot and stops the run (the injected signals trigger
	// it); RUN/STOP then runs again
	u.ctl(keySingle)
	h.check("single", u.until(func() bool { return !running(u.status()) }), "SINGLE: the shot stopped the run (running %v)", u.status()["running"])
	u.ctl(keyRun)
	h.check("run", u.until(func() bool { return running(u.status()) }), "RUN/STOP runs again: %v", u.status()["running"])

	// a touch on the softkey column presses its F-key: F2 on the rail is the
	// next workspace, F1 the SCOPE
	if u.panel().Mode != modeWorkspace { // CH1's key opened its sheet: MENU closes it
		u.ctl(keyMenu)
		u.until(func() bool { return u.panel().Mode == modeWorkspace })
	}
	rep = u.ctl("touch 1010 200")
	h.check("touch-f2", u.until(func() bool { return u.panel().Workspace == "BUS" }), "a touch on F2 (%s): %s, %s", rep, u.panel().Workspace, u.panel().Mode)
	rep = u.ctl("touch 1010 100")
	h.check("touch-f1", u.until(func() bool { return u.panel().Workspace == "SCOPE" }), "a touch on F1 (%s): %s", rep, u.panel().Workspace)

	// the web's sheets are the same rows
	var sheets uimodel.SheetsModel
	err = u.get("/api/sheets", &sheets)
	p := uimodel.CheckRows(sheets)
	h.check("sheets", err == nil && p == nil, "the page's rows on the web's sheets: %v (%v)", p, err)
	lamps := u.panel().LampsLit
	h.check("lamps", len(lamps) > 0, "lit %v", lamps)

	uiChannelsPastTwo(u)
	uiTwelveBitAnalysis(u)
	uiAnyChannel(u)
}

// uiChannelsPastTwo checks C3 and C4 on the UI: CH3's key switches C3 on
// and opens its tab, the shared vertical knob then turns C3's V/div, the
// web's sheets have both channels' tabs and set C4, and SCPI reads what the
// UI and the web set.
func uiChannelsPastTwo(u *uiRig) {
	h := u.h
	if u.panel().Mode != modeWorkspace {
		u.ctl(keyMenu)
		u.until(func() bool { return u.panel().Mode == modeWorkspace })
	}
	_, _ = scpiQuery(u.ctx, "C3:TRA OFF") // the key switches it on (a run may start with it on, as the last one left it)
	u.ctl(keyCh3)
	h.check("ch3-key", u.until(func() bool { s := u.panel(); return s.Sheet.Open && s.Sheet.Tab == "ch3" }), "CH3's key: sheet %+v", u.panel().Sheet.Tab)
	on, _ := u.sheetRow("ch3.on")
	h.check("ch3-on", on.Text == "On", "C3 switched on by its key: %q", on.Text)
	c0, _ := scpiQuery(u.ctx, "C3:VDIV?")
	u.ctl(knobVcw)
	c1, ok := u.scpiUntil("C3:VDIV?", func(r string) bool { return r != c0 && r != "" })
	h.check("knob-vdiv-c3", ok, "the vertical knob after CH3: C3:VDIV? %q to %q", c0, c1)
	_, tabs := u.sheetRow("ch4.vdiv")
	h.check("sheets-c3-c4", strings.Contains(strings.Join(tabs, " "), "ch3 ch4"), "the web's sheet tabs %v", tabs)
	res := u.setRow("ch4.on", 1)
	res2 := u.setRow("ch4.vdiv", 0.5)
	h.check("sheet-ch4", res.OK && res2.OK, "ch4.on %+v, ch4.vdiv %+v", res, res2)
	reply, ok := u.scpiUntil("C4:VDIV?", func(r string) bool { return strings.Contains(r, "5.00E-01") })
	h.check("sheet-ch4-scpi", ok, "C4:VDIV? after the web's sheet set it: %q", reply)
	_, _ = scpiQuery(u.ctx, "C4:VDIV 2")
	row, _ := u.sheetRow("ch4.vdiv")
	got := u.until(func() bool { row, _ = u.sheetRow("ch4.vdiv"); v, _ := row.Value.(float64); return v == 2 })
	h.check("scpi-ch4-sheet", got, "the web's ch4.vdiv after C4:VDIV 2: %v", row.Value)
}

// uiTwelveBitAnalysis checks the analysis on the board's 12-bit records of
// the injected signals on a channel past two: SCPI's measurement and the
// UI model's MEASURE values of C3 are the 2 MHz, 1 V square put in.
func uiTwelveBitAnalysis(u *uiRig) {
	h := u.h
	_, _ = scpiQuery(u.ctx, ":TIM:SCAL 1E-6")
	reply, ok := u.scpiUntil(":MEAS:SIMP:SOUR C3;:MEAS:SIMP:VAL? FREQ;:MEAS:SIMP:VAL? PKPK", func(r string) bool {
		f := strings.Split(r, ";")
		return len(f) == 2 && near(f[0], 2e6, 0.02) && near(f[1], 1, 0.1)
	})
	h.check("meas-scpi-c3", ok, "C3 over SCPI: %q (2 MHz, 1 V peak to peak injected)", reply)
	for _, row := range []string{"meas.freq", "meas.pkpk"} {
		if res := u.setRow(row, 4); !res.OK { // C3 alone
			h.check("meas-rows", false, "%s on C3: %+v", row, res)
			return
		}
	}
	if u.panel().Mode != modeWorkspace {
		u.ctl(keyMenu)
		u.until(func() bool { return u.panel().Mode == modeWorkspace })
	}
	u.ctl(keyMeas) // the MEAS tab: its rows carry the live values
	var freq, pkpk uimodel.RowModel
	ok = u.until(func() bool {
		freq, _ = u.sheetRow("meas.freq")
		pkpk, _ = u.sheetRow("meas.pkpk")
		return len(freq.Live) > 2 && len(pkpk.Live) > 2 && nearSI(freq.Live[2], 2e6, 0.02) && nearSI(pkpk.Live[2], 1, 0.1)
	})
	h.check("meas-ui-c3", ok, "the UI model's C3 values: freq %q, Vpp %q (2 MHz, 1 V injected)", freq.Live, pkpk.Live)
}

// near reports whether an NR3 reply is within frac of want.
func near(reply string, want, frac float64) bool {
	v, err := strconv.ParseFloat(strings.TrimSpace(reply), 64)
	return err == nil && math.Abs(v-want) <= frac*math.Abs(want)
}

// nearSI reports whether a value as the sheet writes it ("2MHz", "1.01V")
// is within frac of want.
func nearSI(text string, want, frac float64) bool {
	t := strings.TrimSpace(text)
	t = strings.TrimRight(t, "HzV%s")
	mult := 1.0
	if t != "" {
		switch t[len(t)-1] {
		case 'G':
			mult = 1e9
		case 'M':
			mult = 1e6
		case 'k':
			mult = 1e3
		case 'm':
			mult = 1e-3
		case 'u':
			mult = 1e-6
		case 'n':
			mult = 1e-9
		}
		if mult != 1 {
			t = t[:len(t)-1]
		}
	}
	v, err := strconv.ParseFloat(t, 64)
	return err == nil && math.Abs(v*mult-want) <= frac*math.Abs(want)
}

// running reports the status's run.
func running(st map[string]any) bool { return st["running"] == true }

// differs counts the pixels two pictures differ in (0 when one is missing).
func differs(a, b *image.NRGBA) int {
	if a == nil || b == nil {
		return 0
	}
	if a.Rect != b.Rect {
		return a.Rect.Dx() * a.Rect.Dy()
	}
	n := 0
	for i := 0; i+3 < len(a.Pix); i += 4 {
		if a.Pix[i] != b.Pix[i] || a.Pix[i+1] != b.Pix[i+1] || a.Pix[i+2] != b.Pix[i+2] || a.Pix[i+3] != b.Pix[i+3] {
			n++
		}
	}
	return n
}
