package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/labeth/open-dso-core/webserver/wire"
)

// The web area's second app run: the phone remote swept in a browser at a
// phone's viewport (sim/e2e/remote_sweep.mjs), and the record download
// (/api/record.bin, the BUS Visual view's whole sampled record) checked
// against the record the scope captures and the caps that describe it.

// webRemoteFile is where the host's checks of the remote run land.
const webRemoteFile = "web-remote-host.txt"

// remoteRun is what the remote driver reports.
type remoteRun struct {
	Device   string `json:"device"`
	Viewport struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"viewport"`
	Tabs []struct {
		ID         string   `json:"id"`
		Rows       int      `json:"rows"`
		Drawn      int      `json:"drawn"`
		Missing    []string `json:"missing"`
		Opened     int      `json:"opened"`
		FailedOpen []string `json:"failedOpen"`
	} `json:"tabs"`
	Rows    int `json:"rows"`
	Opened  int `json:"opened"`
	Changed []struct {
		Row  string `json:"row"`
		From string `json:"from"`
		To   string `json:"to"`
		Back bool   `json:"back"`
	} `json:"changed"`
	Refused  []map[string]any `json:"refused"`
	Stuck    []string         `json:"stuck"`
	Followed []struct {
		Row   string `json:"row"`
		From  any    `json:"from"`
		To    any    `json:"to"`
		Moved bool   `json:"moved"`
		Back  bool   `json:"back"`
	} `json:"followed"`
	SideScroll bool     `json:"sideScroll"`
	Errors     []string `json:"errors"`
	Failed     []string `json:"failed"`
}

// webRemoteHost runs the remote sweep and the record checks.
func webRemoteHost(ctx context.Context, dir string) {
	f, err := os.Create(filepath.Join(dir, webRemoteFile)) //nolint:gosec // G304: the run's own directory
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	u := &uiRig{ctx: ctx, dir: dir, h: hostLog{f}, cl: &http.Client{Timeout: 20 * time.Second}}
	h := u.h
	if !h.check("remote-serving", until(ctx, 120*time.Second, func() bool { return u.panel().Workspace != "" }), "the app's UI model answered") {
		return
	}
	webRecord(u)
	webRemote(u)
}

// webRemote sweeps the phone remote's Menus.
func webRemote(u *uiRig) {
	h := u.h
	out, ok := runPageDriver(u.ctx, "remote_sweep.mjs", 200*time.Second, webBase, filepath.Join(u.dir, "shots"))
	if !ok {
		h.check("remote", os.Getenv("SIM_REQUIRE_BROWSER") == "", "the remote was not run in a browser: %s", out)
		return
	}
	var r remoteRun
	if err := json.Unmarshal([]byte(lastLine(out)), &r); err != nil {
		h.check("remote", false, "the driver's report: %v: %.400s", err, out)
		return
	}
	h.check("remote-phone", r.Viewport.Width > 0 && r.Viewport.Width <= 480 && !r.SideScroll,
		"%s at %dx%d, sideways scroll %v", r.Device, r.Viewport.Width, r.Viewport.Height, r.SideScroll)
	var tabs, missing, failedOpen []string
	for _, t := range r.Tabs {
		tabs = append(tabs, fmt.Sprintf("%s %d/%d", t.ID, t.Drawn, t.Rows))
		for _, m := range t.Missing {
			missing = append(missing, t.ID+":"+m)
		}
		for _, m := range t.FailedOpen {
			failedOpen = append(failedOpen, t.ID+":"+m)
		}
	}
	h.check("remote-tabs", len(r.Tabs) >= 12 && len(missing) == 0,
		"%d Menus tabs, rows drawn/held: %s; missing %v", len(r.Tabs), strings.Join(tabs, ", "), missing)
	h.check("remote-rows-open", r.Opened > 0 && len(failedOpen) == 0, "%d rows opened their sheet and closed; failed %v", r.Opened, failedOpen)
	var changed []string
	back := true
	for _, c := range r.Changed {
		changed = append(changed, fmt.Sprintf("%s %s->%s", c.Row, c.From, c.To))
		back = back && c.Back
	}
	h.check("remote-changes", len(r.Changed) >= 6 && back && len(r.Refused) == 0,
		"%d safe values changed from the phone, the scope's sheets followed and went back (%v): %s; refused %v",
		len(r.Changed), back, strings.Join(changed, "; "), r.Refused)
	for _, fl := range r.Followed {
		h.check("remote-"+fl.Row, fl.Moved && fl.Back, "the phone's stepper: the scope's status %v -> %v (moved %v, back %v)", fl.From, fl.To, fl.Moved, fl.Back)
	}
	h.check("remote-errors", len(r.Errors) == 0 && len(r.Failed) == 0 && len(r.Stuck) == 0,
		"page errors %q, failed replies %q, taps that found nothing to tap %q", r.Errors, r.Failed, r.Stuck)
}

// webRecord fetches /api/record.bin while the scope runs and checks it
// against the record it captures (the raw v2 frame's length and sample
// interval), the status (timebase, V/div) and the caps (the codes' format
// gives the 8-bit view's volts per code), and the injected signals' volts;
// after a SINGLE the stopped scope captures no record after the request,
// and the download says so.
func webRecord(u *uiRig) {
	h := u.h
	var caps wire.Caps
	if err := u.get("/api/caps", &caps); err != nil {
		h.check("record", false, "caps: %v", err)
		return
	}
	_, _ = scpiQuery(u.ctx, ":TIM:SCAL 1E-6;*OPC?")
	_, _ = scpiQuery(u.ctx, "C2:TRA ON;*OPC?")
	var fr wire.Frame
	got := until(u.ctx, 30*time.Second, func() bool {
		_, raw, err := u.rawFrame()
		if err != nil {
			return false
		}
		g, err := wire.DecodeFrame2(raw)
		fr = g
		return err == nil && math.Abs(g.Header.TdivS-1e-6) < 1e-9 && traceOf(g, 0) != nil && traceOf(g, 1) != nil
	})
	if !h.check("record-frame", got, "a raw record at 1 us/div with C1 and C2: %d samples at %.3g s", rawLen(fr), fr.Header.SampleS) {
		return
	}
	st := u.status()
	// the page asks for 2 ns or coarser (the server's floor); a record
	// sampled finer qualifies
	want := math.Max(fr.Header.SampleS, 2e-9)
	body, code := svGet(u, fmt.Sprintf("/api/record.bin?sample_s=%g&wait_ms=8000", want))
	hdr, codes, err := decodeRecordBin(body)
	if !h.check("record-served", code == http.StatusOK && err == nil, "GET /api/record.bin: %d, %d bytes (%v)", code, len(body), err) {
		return
	}
	hdrLen := int(binary.LittleEndian.Uint32(body[:4]))
	h.check("record-length", hdr.N >= 1024 && len(body) == 4+hdrLen+hdr.N*len(hdr.Chans) && len(codes) == len(hdr.Chans),
		"%d bytes: a %d-byte header and %d samples of %d channels %v, one byte each", len(body), hdrLen, hdr.N, len(hdr.Chans), hdr.Chans)
	h.check("record-depth", hdr.N == rawLen(fr) && math.Abs(hdr.SampleS-fr.Header.SampleS) <= 1e-6*fr.Header.SampleS,
		"%d samples every %.4g s; the raw record %d every %.4g s", hdr.N, hdr.SampleS, rawLen(fr), fr.Header.SampleS)
	tdiv, _ := st["tdiv_set_s"].(float64)
	h.check("record-timebase", math.Abs(hdr.TdivS-tdiv) <= 1e-9*math.Max(tdiv, 1e-12) && hdr.TdivS > 0, "the header's %.4g s/div, the status's %.4g", hdr.TdivS, tdiv)
	h.check("record-channels", slices.Contains(hdr.Chans, 0) && slices.Contains(hdr.Chans, 1), "channels %v (C1 and C2 on)", hdr.Chans)
	// the 8-bit view of the caps' codes: one step is unit8 words
	cc := caps.Codes
	unit8 := math.Ldexp(1, int(cc.Bits)+int(cc.Shift)+int(cc.Frac)-8)
	for i, ch := range hdr.Chans {
		if ch > 1 || i >= len(hdr.Vpc) {
			continue
		}
		vdiv, _ := st[fmt.Sprintf("vdiv%d", ch+1)].(float64)
		vpc := vdiv * unit8 / cc.CodesPerDiv
		h.check(fmt.Sprintf("record-scale-c%d", ch+1), math.Abs(hdr.Vpc[i]-vpc) <= 1e-6*vpc,
			"C%d: %.6g V a code; %g V/div at %g codes a division of %d-bit %s codes on the 8-bit view (unit %g): %.6g",
			ch+1, hdr.Vpc[i], vdiv, cc.CodesPerDiv, cc.Bits, cc.Storage, unit8, vpc)
		lo, hi := 255, 0
		for _, c := range codes[i] {
			lo, hi = min(lo, int(c)), max(hi, int(c))
		}
		volts := func(c int) float64 { return float64(c-128)*hdr.Vpc[i] - hdr.Off[i] }
		amp := []float64{1, 0.5}[ch]
		h.check(fmt.Sprintf("record-volts-c%d", ch+1), math.Abs(volts(hi)-amp) < 0.15*amp+0.05 && math.Abs(volts(lo)+amp) < 0.15*amp+0.05,
			"C%d's codes %d..%d read %.3f .. %.3f V (±%g V injected)", ch+1, lo, hi, volts(lo), volts(hi), amp)
	}

	// SINGLE: the scope stops on one capture; a record asked for then is
	// refused with why (the download takes a capture made after the request)
	var rep map[string]any
	_ = u.post("/api/panel", `{"button":"single"}`, &rep) // the run state checked next tells
	stopped := u.until(func() bool { return !running(u.status()) })
	body, code = svGet(u, fmt.Sprintf("/api/record.bin?sample_s=%g&wait_ms=3000", want))
	var ref wire.RecordReply
	_ = json.Unmarshal(body, &ref) // a reply that is no JSON shows in the check
	h.check("record-single", stopped && code == http.StatusConflict && strings.Contains(ref.Err, "stopped"),
		"after SINGLE (stopped %v): %d %q", stopped, code, shorten(ref.Err, 120))
	_ = u.post("/api/panel", `{"button":"run"}`, &rep) // later checks want it running
	u.until(func() bool { return running(u.status()) })
}

// rawLen is a v2 frame's record length.
func rawLen(fr wire.Frame) int {
	n := 0
	for _, t := range fr.Traces {
		n = max(n, t.Codes.Len())
	}
	return n
}

// decodeRecordBin splits /api/record.bin's reply: the little-endian header
// length, the JSON header, each channel's codes in turn.
func decodeRecordBin(b []byte) (wire.RecordReply, [][]byte, error) {
	var h wire.RecordReply
	if len(b) < 4 {
		return h, nil, fmt.Errorf("%d bytes", len(b))
	}
	n := int(binary.LittleEndian.Uint32(b[:4]))
	if 4+n > len(b) {
		return h, nil, fmt.Errorf("a %d-byte header in %d bytes", n, len(b))
	}
	if err := json.Unmarshal(b[4:4+n], &h); err != nil {
		return h, nil, err
	}
	if len(h.Chans) == 0 { // the two-channel layout: C1 then C2
		h.Chans, h.Vpc, h.Off = []int{0, 1}, []float64{h.Vpc1, h.Vpc2}, []float64{h.Off1V, h.Off2V}
	}
	var codes [][]byte
	rest := b[4+n:]
	for range h.Chans {
		if len(rest) < h.N {
			return h, codes, fmt.Errorf("codes of %d samples cut at %d", h.N, len(rest))
		}
		codes = append(codes, rest[:h.N])
		rest = rest[h.N:]
	}
	return h, codes, nil
}
