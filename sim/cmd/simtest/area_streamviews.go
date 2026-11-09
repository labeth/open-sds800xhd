package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/labeth/open-dso-core/decode/synth"
	"github.com/labeth/open-dso-core/framecalc"
	"github.com/labeth/open-dso-core/sample"
	"github.com/labeth/open-dso-core/streamwire"
	"github.com/labeth/open-dso-core/uimodel"
	"github.com/labeth/open-dso-core/webserver/wire"
)

// The streamviews area starts the views a gap-free stream feeds, as the
// CML's bench did (SOUND, and BUS's Stream and Visual views), on the app
// with a MIDI bus on C1 and a 440 Hz tone on C2, and counts what each
// delivers: starts, messages or blocks, the delay to the first, and losses.
//
// A view the scope cannot feed from a stream says so: its caps feature, its
// endpoint and the UI model's refusal name the same reason, and the case
// reports it as absent. BUS's Visual view also reads each capture's decode
// (the gap-free stream only adds what lies between captures), so its
// messages are counted from the records the app serves while the view is
// shown, and from the web page's own Visual view in a browser.
//
// Thresholds, set for the emulator (the app runs 10 to 20 times slower than
// on the unit, and a capture takes about a second there):
//   - a start answers within 15 s (the unit's stream starts in under 2 s;
//     an image load and the emulated ARM make it several times longer);
//   - the Visual view's first message within 15 s of entering it, and at
//     least 4 decoded captures in 20 s (the unit's captures come at 20 to
//     60 a second; one every 5 s still shows the view is fed);
//   - every capture's decode a run of the MIDI bus's own bytes, but for up
//     to eight misframed at its start (a record that begins inside the
//     bus's back-to-back characters takes a few to fall in step, on the unit
//     too) and two at its end (a character the record cuts);
//   - a stream's samples or lines at least half its rate over 20 s and no
//     loss (the stream's buffer covers seconds; a qemu stall of a few
//     hundred milliseconds must not lose data).
func init() {
	register(appHostArea("streamviews", "streams: SOUND, BUS Stream and Visual started, counted (messages, delay, losses)", 300,
		streamViewSignals, streamViewsFile, func(ctx context.Context, dir string) { streamViewsHost(ctx, dir) }))
}

// streamViewSignals are the inputs: the generator's MIDI bus on C1 (note
// on and off, a controller, clock and a program change, repeating), a
// 440 Hz tone of 1 V peak to peak on C2.
const streamViewSignals = "-signals C1=bus.midi.tx,0,3.3,1.65;C2=sine,440,1,0;C3=dc,0,0,0;C4=dc,0,0,0"

// streamViewsFile is where the host's checks of the run land.
const streamViewsFile = "streamviews-host.txt"

// Thresholds (see the area's comment for why).
const (
	svStartS       = 15
	svFirstS       = 15
	svCountS       = 20
	svMinCaptures  = 4
	svEdgeWrong    = 2
	svLeadWrong    = 8
	svMinRateShare = 0.5
)

// streamViewsHost drives the run.
func streamViewsHost(ctx context.Context, dir string) {
	f, err := os.Create(filepath.Join(dir, streamViewsFile)) //nolint:gosec // G304: the run's own directory
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	u := &uiRig{ctx: ctx, dir: dir, h: hostLog{f}, cl: &http.Client{Timeout: 10 * time.Second}}
	h := u.h
	if !h.check("serving", until(ctx, 120*time.Second, func() bool { return u.panel().Workspace != "" }), "the app's UI model answered") {
		return
	}
	var caps wire.Caps
	err = u.get("/api/caps", &caps)
	feat := func(n string) wire.Feature { return caps.Features[n] }
	h.check("caps-streamer", err == nil && feat("streamer").OK, "the board's gap-free stream: %+v (%v); sound %+v; client decode %+v",
		feat("streamer"), err, feat("sound"), feat("client_decode"))

	svBus(u)
	svVisual(u)
	svStream(u)
	svWebPage(u)
	svSound(u, feat("sound"))
	svSlowStream(u)
}

// svSlowBus is a bus every stream here resolves: DALI's 1200 Bd biphase
// (a half bit of 417 us) on C1.
const svSlowBus = "proto dali vpp=3.3 offset=1.65 bus=C1"

// svSlowStream puts a slow bus on C1 and starts BUS's Stream view on it:
// a scope whose stream carries it counts the transcript's lines (the
// factory PL's roll stream samples every 24 us).
func svSlowStream(u *uiRig) {
	h := u.h
	if rep := u.ctl("signals C1=dc,0,0,0;C2=dc,0,0,0;C3=dc,0,0,0;C4=dc,0,0,0"); !strings.HasPrefix(rep, "ok") {
		h.check("slow-bus", false, "simulator: %s", rep)
		return
	}
	if rep := u.ctl(svSlowBus); !strings.HasPrefix(rep, "ok") {
		h.check("slow-bus", false, "simulator: %s", rep)
		return
	}
	var ws map[string]any
	_ = u.post("/api/panel", `{"workspace":"bus"}`, &ws) // a refusal shows in the checks below
	scpi := func(line string) { _, _ = scpiQuery(u.ctx, line+";*OPC?") }
	scpi(":TIM:SCAL 5E-3")
	scpi(":TRIG:MODE AUTO;:TRIG:EDGE:SOUR C1;SLOP FALL;LEV 1.65")
	res := u.setRow("bus.protocol", framecalc.ProtoDALI)
	res2 := u.setRow("bus.source", 0)
	until(u.ctx, 10*time.Second, func() bool {
		p, _, _ := busSettings(u)
		return p.Proto == framecalc.ProtoDALI && !u.panel().Display.DecDetecting
	})
	p, named, set := busSettings(u)
	if !h.check("slow-bus", res.OK && res2.OK && p.Proto == framecalc.ProtoDALI && p.ChA == 0,
		"BUS decodes %s on C%d (%s): protocol %+v, source %+v", named, p.ChA+1, set, res, res2) {
		return
	}
	svStreamCase(u, "slow-stream")
}

// svBus sets the MIDI decoder on C1 and a timebase that holds a whole
// repetition of the bus (about 7 ms) in every capture, triggered on its
// start bits.
func svBus(u *uiRig) {
	h := u.h
	var ws map[string]any
	_ = u.post("/api/panel", `{"workspace":"bus"}`, &ws) // a refusal shows in the checks below
	scpi := func(line string) { _, _ = scpiQuery(u.ctx, line+";*OPC?") }
	scpi(":TIM:SCAL 1E-3")
	scpi(":TRIG:MODE AUTO;:TRIG:EDGE:SOUR C1;SLOP FALL;LEV 1.65")
	scpi("C2:TRA ON")
	// entering BUS with decode off runs Auto's detection: MIDI is set once
	// it has ended, and stays
	until(u.ctx, 90*time.Second, func() bool { d := u.panel().Display; return !d.DecDetecting && !d.AutosetBusy })
	res := u.setRow("bus.protocol", framecalc.ProtoMIDI)
	res2 := u.setRow("bus.source", 0)
	until(u.ctx, 10*time.Second, func() bool {
		p, _, _ := busSettings(u)
		return p.Proto == framecalc.ProtoMIDI && !u.panel().Display.DecDetecting
	})
	p, named, set := busSettings(u)
	h.check("bus-midi", res.OK && res2.OK && p.Proto == framecalc.ProtoMIDI && p.ChA == 0,
		"BUS decodes %s on C%d (%s): protocol %+v, source %+v", named, p.ChA+1, set, res, res2)
}

// svAgain presses the workspace's key again until its view is want, at
// most n times; the views it passed.
func svAgain(u *uiRig, ws, want string, n int) (bool, []string) {
	var seen []string
	for range n {
		v := u.panel().View
		seen = append(seen, v)
		if v == want {
			return true, seen
		}
		var rep map[string]any
		_ = u.post("/api/panel", fmt.Sprintf(`{"workspace":%q,"again":true}`, ws), &rep) // the view checked next tells
		until(u.ctx, 5*time.Second, func() bool { return u.panel().View != v })
	}
	v := u.panel().View
	return v == want, append(seen, v)
}

// svVisual enters BUS's Visual view on the UI model and counts the MIDI
// messages of the captures the app serves while it shows.
func svVisual(u *uiRig) {
	h := u.h
	packets, _ := u.shot("sv-bus.png")
	t0 := time.Now()
	ok, seen := svAgain(u, "bus", "Visual", 6)
	if !h.check("visual-start", ok && time.Since(t0) < svStartS*time.Second, "BUS again to Visual in %v: views %v", time.Since(t0).Round(time.Millisecond), seen) {
		return
	}
	// the display loop draws the view: under the emulator's load that can
	// take seconds, and a dump may land late, so look again for a while
	n, rep := 0, ""
	for end := time.Now().Add(10 * time.Second); n <= 1000 && time.Now().Before(end); {
		time.Sleep(time.Second)
		var vis *image.NRGBA
		vis, rep = u.shot("sv-visual.png")
		n = differs(packets, vis)
	}
	h.check("visual-drawn", n > 1000, "the Visual view changed %d pixels of fb0 from the packets (%s)", n, rep)

	p, _, _ := busSettings(u)
	truth, err := svMIDITruth()
	if err != nil {
		h.check("visual-messages", false, "the generator's truth: %v", err)
		return
	}
	entered := time.Now()
	var st svCount
	var seq uint64
	for end := entered.Add(svCountS * time.Second); time.Now().Before(end) && u.ctx.Err() == nil; {
		f, raw, err := u.rawFrameSince(seq)
		if err != nil || f.Seq == seq {
			continue
		}
		seq = f.Seq
		st.captures++
		r := framecalc.Decode(f, p)
		if len(r.Bytes) == 0 {
			st.notes = append(st.notes, fmt.Sprintf("#%d: no decode (%s; %s)", f.Seq, r.Error, recordText(f, p)))
			_ = os.WriteFile(filepath.Join(u.dir, fmt.Sprintf("sv-undecoded-%d.bin", f.Seq)), raw, 0o644) //nolint:gosec // G306: a run's output
			continue
		}
		st.decoded++
		st.bytes += len(r.Bytes)
		// a record that starts inside the bus's back-to-back characters
		// may frame a data bit as a start bit for a few characters before
		// an idle gap puts the decode in step (any UART decoder does); one
		// cut by the record's end decodes as something else
		at, n := longestRun(r.Bytes, truth)
		if w := max(0, at-svLeadWrong) + max(0, len(r.Bytes)-at-n-svEdgeWrong); w > 0 {
			st.wrong += w
			st.notes = append(st.notes, fmt.Sprintf("#%d: %s", f.Seq, hexOf(r.Bytes, 40)))
			_ = os.WriteFile(filepath.Join(u.dir, fmt.Sprintf("sv-wrong-%d.bin", f.Seq)), raw, 0o644) //nolint:gosec // G306: a run's output
		}
		if m := midiMessages(r.Bytes); m > 0 {
			if st.messages == 0 {
				st.first = time.Since(entered)
			}
			st.messages += m
		}
	}
	h.check("visual-captures", st.captures >= svMinCaptures && st.decoded == st.captures,
		"%d captures in %d s while Visual shows, %d decoded as MIDI (at least %d, all) %v", st.captures, svCountS, st.decoded, svMinCaptures, st.notes)
	h.check("visual-messages", st.messages > 0 && st.first < svFirstS*time.Second && st.wrong == 0,
		"%d MIDI messages (%d bytes) from the captures, the first %v after entering the view; %d bytes not the bus's beyond %d misframed at a capture's start and %d cut at its end %v",
		st.messages, st.bytes, st.first.Round(time.Millisecond), st.wrong, svLeadWrong, svEdgeWrong, st.notes)
}

// longestRun is where in b the longest run the endless repetition of unit
// holds starts, and its length.
func longestRun(b, unit []int) (at, n int) {
	for i := range b {
		for s := range unit {
			k := 0
			for i+k < len(b) && b[i+k] == unit[(s+k)%len(unit)] {
				k++
			}
			if k > n {
				at, n = i, k
			}
		}
	}
	return at, n
}

// svCount is what the captures of a view delivered.
type svCount struct {
	captures, decoded, bytes, messages, wrong int
	first                                     time.Duration
	notes                                     []string // the captures that did not decode, or decoded wrong
}

// svMIDITruth is one repetition of the generator's MIDI bus.
func svMIDITruth() ([]int, error) {
	b, err := synth.Make(synth.Spec{Proto: "midi", Set: map[string]string{}}, 0)
	if err != nil {
		return nil, err
	}
	return repeatUnit(b.Truth.Bytes), nil
}

// midiMessages counts the whole MIDI messages in b: a status byte and its
// data bytes, running status carrying on with more data, real-time bytes
// on their own; a message cut by the capture's start or end does not count.
func midiMessages(b []int) int {
	n, need, have, status := 0, 0, 0, 0
	for _, v := range b {
		switch {
		case v >= 0xF8: // real time: one byte, anywhere
			n++
		case v >= 0x80:
			status, have = v, 0
			need = midiDataBytes(v)
			if need == 0 {
				n++
			}
		case status != 0 && need > 0:
			if have++; have == need {
				n++
				have = 0 // running status: the next data bytes are another message
			}
		}
	}
	return n
}

// midiDataBytes is how many data bytes follow a status byte.
func midiDataBytes(s int) int {
	switch s & 0xF0 {
	case 0xC0, 0xD0:
		return 1
	case 0xF0:
		switch s {
		case 0xF1, 0xF3:
			return 1
		case 0xF2:
			return 2
		}
		return 0
	}
	return 2
}

// rawFrameSince fetches the app's next record after seq as v2 raw samples.
func (u *uiRig) rawFrameSince(seq uint64) (*sample.Frame, []byte, error) {
	req, err := http.NewRequestWithContext(u.ctx, http.MethodGet, fmt.Sprintf("%s/api/frame.bin?v=2&raw=1&waitms=2000&since=%d", webBase, seq), nil)
	if err != nil {
		return nil, nil, err
	}
	r, err := u.cl.Do(req)
	if err != nil {
		time.Sleep(200 * time.Millisecond)
		return nil, nil, err
	}
	defer func() { _ = r.Body.Close() }()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, nil, err
	}
	wf, err := wire.DecodeFrame2(b)
	if err != nil {
		return nil, nil, err
	}
	if wf.Header.Unchanged {
		return &sample.Frame{Seq: seq}, b, nil
	}
	f, err := sampleFrame(wf)
	return f, b, err
}

// svStream starts BUS's Stream view, from the UI model and as the web page
// asks for it, and counts the transcript's lines; a scope with no decoded
// stream refuses with its reason, and one whose stream is too slow for the
// bus (the factory PL's roll stream and MIDI's 32 us bit) refuses the bus
// with the rates.
func svStream(u *uiRig) { svStreamCase(u, "stream") }

// svStreamCase is svStream, its checks named after tag.
func svStreamCase(u *uiRig, tag string) {
	h := u.h
	t0 := time.Now()
	ok, seen := svAgain(u, "bus", labelStreamView, 6)
	h.check(tag+"-view", ok, "BUS again to Stream in %v: views %v", time.Since(t0).Round(time.Millisecond), seen)
	row, _ := u.sheetRow("bus.stream")
	var page uimodel.SetResult
	err := u.post("/api/stream/page", `{"on":true}`, &page)
	lines, code := svGet(u, "/api/stream/lines?from=0&max=1")
	if why := svRateRefusal(u, row.Available && err == nil, page); why != "" {
		// a decoded stream too slow for this bus: the start says so with
		// the rates, and the transcript stays empty
		var p streamwire.LinesPage
		gerr := u.get("/api/stream/lines?from=0&max=4", &p)
		h.check(tag+"-start", gerr == nil && len(p.Lines) == 0, "refused for this bus %v after entering the view: %q; /api/stream/lines %d lines (%v)",
			time.Since(t0).Round(time.Second), why, len(p.Lines), gerr)
		svLeaveStream(u)
		return
	}
	if !row.Available || !page.OK {
		// no decoded stream here: every surface says why, the same why, and
		// none says the bus is missing (MIDI is decoded)
		why := page.Error
		h.check(tag+"-start", err == nil && !page.OK && code == http.StatusNotFound && why != "" &&
			!strings.Contains(why, "needs a bus") && !strings.Contains(why, "set Protocol") && row.Why != "" && !strings.Contains(row.Why, "decode on"),
			"absent: the page's start refused %q (%v); /api/stream/lines %d %q; the Stream row %q: needs %q", why, err, code, shorten(string(lines), 80), row.Text, row.Why)
		svLeaveStream(u)
		return
	}
	// a stream: count its lines for svCountS
	start := time.Now()
	var first time.Duration
	next, total, gaps := -1, 0, 0
	for end := start.Add(svCountS * time.Second); time.Now().Before(end) && u.ctx.Err() == nil; time.Sleep(250 * time.Millisecond) {
		var p streamwire.LinesPage
		if u.get(fmt.Sprintf("/api/stream/lines?from=%d&max=4000", max(next, 0)), &p) != nil {
			continue
		}
		if next >= 0 && p.Base > next {
			gaps += p.Base - next // lines the ring dropped before they were read
		}
		if next >= 0 && len(p.Lines) > 0 && total == 0 {
			first = time.Since(start)
		}
		if next >= 0 {
			total += len(p.Lines)
		}
		next = p.Next
	}
	h.check(tag+"-lines", total > 0 && first < svFirstS*time.Second && gaps == 0,
		"%d transcript lines in %d s, the first %v after the start, %d lost", total, svCountS, first.Round(time.Millisecond), gaps)
	svLeaveStream(u)
}

// svRefusalS bounds the wait for a stream's verdict on a bus too fast for
// it.
const svRefusalS = 45

// svRateNote is the words of a stream's refusal of a bus it is too slow
// for.
const svRateNote = "the board streams one every"

// svRateRefusal is a decoded stream's refusal of the bus: the page's start
// refused with the rates, or the stream the view started failed (the UI
// model's pane on the LCD and the app's log say why: the rates); "" when
// the stream runs or is absent.
func svRateRefusal(u *uiRig, has bool, page uimodel.SetResult) string {
	if !has {
		return ""
	}
	if !page.OK {
		if strings.Contains(page.Error, svRateNote) {
			return page.Error
		}
		return ""
	}
	// the verdict comes once the UI model has read the bus off the screen
	// (its decode of a frame) and the stream's first block is in: on the
	// emulator that can take longer than a start of a stream that runs
	// a FAIL left by an earlier stream counts only once this start has run
	// (START) or nothing else came in the whole wait
	started, last := false, ""
	for end := time.Now().Add(svRefusalS * time.Second); time.Now().Before(end) && u.ctx.Err() == nil; time.Sleep(250 * time.Millisecond) {
		var p streamwire.LinesPage
		if u.get("/api/stream/lines?from=0&max=1", &p) != nil {
			continue
		}
		last = p.State
		switch p.State {
		case streamwire.Starting.String():
			started = true
		case streamwire.Failed.String():
			if started {
				// the start failed: the UI model's pane on the LCD and the
				// app's log say why; the page reads the state
				return "the stream's start failed (FAIL), its transcript empty"
			}
		case streamwire.Streaming.String(), streamwire.Triggered.String(), streamwire.Stopped.String():
			return ""
		}
	}
	if last == streamwire.Failed.String() {
		return "the stream failed (FAIL) and did not start again, its transcript empty"
	}
	return ""
}

// labelStreamView is BUS's Stream view as the UI model names it.
const labelStreamView = "Stream"

// svLeaveStream ends the page's stream and puts BUS back on its packets.
func svLeaveStream(u *uiRig) {
	var rep map[string]any
	_ = u.post("/api/stream/page", `{"on":false,"leave":true}`, &rep) // nothing held is fine
	svAgain(u, "bus", "Packets", 4)
}

// svGet fetches a path's body and status.
func svGet(u *uiRig, path string) ([]byte, int) {
	req, err := http.NewRequestWithContext(u.ctx, http.MethodGet, webBase+path, nil)
	if err != nil {
		return nil, 0
	}
	r, err := u.cl.Do(req)
	if err != nil {
		return nil, 0
	}
	defer func() { _ = r.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	return b, r.StatusCode
}

// svPage is what the core's page reports of its Visual view and SOUND tab
// (sim/e2e/streamviews.mjs).
type svPage struct {
	Visual struct {
		Shown    bool     `json:"shown"`
		Msgs     int      `json:"msgs"`
		FirstMS  int      `json:"first_ms"`
		Streamed bool     `json:"streamed"`
		Toasts   []string `json:"toasts"`
	} `json:"visual"`
	Sound struct {
		Shown       bool   `json:"shown"`
		Unavailable string `json:"unavailable"`
		Blocks      int    `json:"blocks"`
	} `json:"sound"`
	Errors []string `json:"errors"`
	Failed []string `json:"failed"`
}

// svWebPage runs the page's BUS Visual view and SOUND tab in a browser.
func svWebPage(u *uiRig) {
	h := u.h
	out, ok := runPageDriver(u.ctx, "streamviews.mjs", 150*time.Second, webBase, strconv.Itoa(svCountS))
	if !ok {
		h.check("web-visual", os.Getenv("SIM_REQUIRE_BROWSER") == "", "the page was not run in a browser: %s", out)
		return
	}
	var p svPage
	if err := json.Unmarshal([]byte(lastLine(out)), &p); err != nil {
		h.check("web-visual", false, "the driver's report: %v: %.400s", err, out)
		return
	}
	h.check("web-visual", p.Visual.Shown && p.Visual.Msgs > 0 && p.Visual.FirstMS < svFirstS*1000,
		"the page's Visual view: %d MIDI messages in %d s, the first after %d ms; following a stream %v; toasts %q",
		p.Visual.Msgs, svCountS, p.Visual.FirstMS, p.Visual.Streamed, p.Visual.Toasts)
	h.check("web-sound", p.Sound.Shown && (p.Sound.Unavailable != "" || p.Sound.Blocks > 0),
		"the page's SOUND tab: unavailable %q, %d blocks", p.Sound.Unavailable, p.Sound.Blocks)
	h.check("web-errors", len(p.Errors) == 0 && len(p.Failed) == 0, "page errors %q, failed replies %q", p.Errors, p.Failed)
}

// runPageDriver runs one of this repository's browser drivers (sim/e2e)
// with args; ok false with the reason when no browser ran it.
func runPageDriver(ctx context.Context, name string, limit time.Duration, args ...string) (string, bool) {
	root, err := os.Getwd()
	if err != nil {
		return err.Error(), false
	}
	driver := filepath.Join(root, "sim", "e2e", name)
	if _, err := os.Stat(driver); err != nil {
		return "no page driver at " + driver, false
	}
	cctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	b, err := exec.CommandContext(cctx, "node", append([]string{driver}, args...)...).CombinedOutput() //nolint:gosec // G204: this repository's own driver against the simulated scope
	text := strings.TrimSpace(string(b))
	if strings.HasPrefix(text, "SKIP") {
		return text, false
	}
	if err != nil && !strings.HasPrefix(lastLine(text), "{") {
		return fmt.Sprintf("%v: %.400s", err, text), true
	}
	return text, true
}

// svSound enters SPECTRUM's SOUND view and reads the audio stream as a
// page does; a scope with no audio stream says why on every surface.
func svSound(u *uiRig, f wire.Feature) {
	h := u.h
	var ws map[string]any
	_ = u.post("/api/panel", `{"workspace":"spectrum"}`, &ws) // the view checked next tells
	t0 := time.Now()
	ok, seen := svAgain(u, "spectrum", "Sound", 7)
	h.check("sound-view", ok && time.Since(t0) < svStartS*time.Second, "SPECTRUM again to Sound in %v: views %v", time.Since(t0).Round(time.Millisecond), seen)
	time.Sleep(time.Second)
	u.shot("sv-sound.png")
	body, code := svGet(u, "/api/sound.bin?wait=600")
	if !f.OK {
		h.check("sound-start", code == http.StatusNotFound && f.Why != "",
			"absent: caps say %q; /api/sound.bin %d %q", f.Why, code, shorten(strings.TrimSpace(string(body)), 80))
		return
	}
	// the stream: blocks from the newest on, for svCountS
	start := time.Now()
	var first time.Duration
	var next uint64
	blocks, samples, gaps, losses0, losses := 0, 0, 0, -1, 0
	rate := 0.0
	for end := start.Add(svCountS * time.Second); time.Now().Before(end) && u.ctx.Err() == nil; {
		q := "/api/sound.bin?wait=600&min=2048"
		if next > 0 {
			q += fmt.Sprintf("&from=%d", next)
		}
		b, c := svGet(u, q)
		hdr, err := svSoundHeader(b)
		if c != http.StatusOK || err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if losses0 < 0 {
			losses0 = hdr.Losses
		}
		losses, rate = hdr.Losses-losses0, hdr.Rate
		if hdr.N > 0 {
			if blocks == 0 {
				first = time.Since(start)
			}
			if next > 0 && hdr.First > next {
				gaps++
			}
			blocks++
			samples += hdr.N
			gaps += len(hdr.Gaps)
		}
		next = hdr.Next
	}
	want := int(svMinRateShare * rate * svCountS)
	h.check("sound-blocks", blocks > 0 && first < svStartS*time.Second && samples >= want,
		"%d blocks, %d samples in %d s at %.0f Sa/s (at least %d), the first after %v", blocks, samples, svCountS, rate, want, first.Round(time.Millisecond))
	h.check("sound-losses", gaps == 0 && losses == 0, "%d gaps in the blocks read, %d losses the stream counted", gaps, losses)
}

// svSoundHeader reads a /api/sound.bin reply's header: magic, the JSON's
// length, the JSON.
func svSoundHeader(b []byte) (wire.SoundBinHeader, error) {
	var h wire.SoundBinHeader
	if len(b) < 8 || !bytes.Equal(b[:3], wire.SoundBinMagic[:]) {
		return h, fmt.Errorf("no sound.bin reply (%d bytes)", len(b))
	}
	n := int(binary.LittleEndian.Uint32(b[4:8]))
	if 8+n > len(b) {
		return h, fmt.Errorf("a header of %d bytes in %d", n, len(b))
	}
	return h, json.Unmarshal(b[8:8+n], &h)
}
