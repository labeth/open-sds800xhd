package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/labeth/open-dso-core/decode"
	"github.com/labeth/open-dso-core/decode/synth"
	"github.com/labeth/open-dso-core/framecalc"
	"github.com/labeth/open-dso-core/sample"
	"github.com/labeth/open-dso-core/webserver/wire"

	"github.com/labeth/open-sds800xhd/sim/harness"
)

// The decode areas run the protocol decode sweep against ground truth: each
// bus the core's generators write (decode/synth) goes onto the simulator's
// inputs through its front-end model, the app's AUTO in BUS names it and
// fills in the decoder's settings, and the record the app shows decodes with
// those settings (the decode the app's BUS view and strip run) to the bytes
// the generator wrote. "decode" runs every protocol once, its first variant;
// "decode-sweep" runs every variant (rates, modes and framings).

// decodeSimd is the simulator's front end for the decode areas: the
// realistic model (noise, bandwidth, gain and offset error, converter-core
// mismatch) under a fixed seed, every input idle at the start.
const decodeSimd = "-afe real,seed=1 -signals C1=dc,0,0,0;C2=dc,0,0,0;C3=dc,0,0,0;C4=dc,0,0,0"

// decodeCasesPerRun bounds one simulator run (300 s at most, the app's start
// included): AUTO in BUS takes 10 to 120 s in the simulator.
const decodeCasesPerRun = 4

// decodeHostFile holds a run's cases as JSON lines.
const decodeHostFile = "decode-host.jsonl"

// busCase is one bus of the sweep.
type busCase struct {
	ID    string            `json:"id"` // the result's case name
	Spec  synth.Spec        `json:"spec"`
	Place map[string]string `json:"place"` // role → channel ("C3")
	Vpp   float64           `json:"vpp"`
	Off   float64           `json:"offset"`
}

// busOutcome is what a case found.
type busOutcome struct {
	busCase
	Named    string `json:"named"`    // the protocol AUTO left in the BUS sheet
	Settings string `json:"settings"` // the BUS sheet's settings it filled in
	Decoded  string `json:"decoded"`  // the decode of the app's record, shortened
	Truth    string `json:"truth"`    // the generator's ground truth, shortened
	Record   string `json:"record"` // the record decoded: length, interval, channels
	Pass     bool   `json:"pass"`
	Reason   string `json:"reason"`
}

// recordText describes the record a case decoded.
func recordText(f *sample.Frame, p framecalc.DecodeParams) string {
	var on []string
	for _, t := range f.Traces {
		if len(t.Codes16)+len(t.Codes8) > 0 {
			on = append(on, fmt.Sprintf("C%d", t.Ch+1))
		}
	}
	return fmt.Sprintf("%d samples at %.3g s (%s) on %s, decoding C%d/C%d", f.Len(), f.SampleS, f.Content, strings.Join(on, ","), p.ChA+1, p.ChB+1)
}

func init() {
	register(decodeArea("decode", "protocols: every bus AUTO-named and decoded against the generator's truth", false))
	register(decodeArea("decode-sweep", "protocols × their variants (rates, modes, framings): AUTO and decode", true))
}

// decodeLevels are a bus's electrical levels: logic at 3.3 V, the bipolar
// avionics buses at their differential swing.
func decodeLevels(proto string) (vpp, off float64) {
	switch proto {
	case "arinc429":
		return 20, 0
	case "mil1553":
		return 14, 0
	}
	return 3.3, 1.65
}

// pairs and singles are the channels the sweep rotates a bus over: a bus
// may sit on any channel.
var (
	decodePairs   = [][2]string{{"C1", "C2"}, {"C3", "C4"}, {"C2", "C4"}, {"C1", "C3"}}
	decodeSingles = []string{"C1", "C2", "C3", "C4"}
)

// decodeCases lists the sweep's cases.
func decodeCases(e *env, sweep bool) []busCase {
	var out []busCase
	k := 0
	for _, p := range synth.Protocols() {
		vs := synth.Variants(p.Proto)
		if !sweep {
			vs = vs[:1]
		}
		for _, s := range vs {
			c := busCase{Spec: s, Place: map[string]string{}}
			c.Vpp, c.Off = decodeLevels(p.Proto)
			if len(p.Roles) == 1 {
				c.Place[p.Roles[0]] = decodeSingles[k%len(decodeSingles)]
			} else {
				pr := decodePairs[k%len(decodePairs)]
				c.Place[p.Roles[0]], c.Place[p.Roles[1]] = pr[0], pr[1]
			}
			c.ID = strings.ReplaceAll(s.String(), " ", "/")
			k++
			if e.cases != "" && !slices.ContainsFunc(strings.Split(e.cases, ","), func(w string) bool { return strings.Contains(c.ID, w) }) {
				continue
			}
			out = append(out, c)
		}
	}
	return out
}

func decodeArea(name, title string, sweep bool) area {
	return area{
		name: name, title: title,
		runs: func(e *env) []harness.Spec {
			cases := decodeCases(e, sweep)
			var specs []harness.Spec
			for i := 0; i < len(cases); i += decodeCasesPerRun {
				part := cases[i:min(len(cases), i+decodeCasesPerRun)]
				specs = append(specs, harness.Spec{
					Name: fmt.Sprintf("%s%s-%02d", e.prefix, name, i/decodeCasesPerRun), Secs: 290, Ports: true,
					SimdArgs: decodeSimd, App: e.app, Hook: e.hook, GuestBoot: e.guestBoot,
					Host: func(ctx context.Context, dir string) { decodeHost(ctx, dir, part) },
				})
			}
			return specs
		},
		check: func(e *env, runs []*harness.Run) []harness.Result {
			var outs []busOutcome
			var res []harness.Result
			for _, r := range runs {
				o, err := readOutcomes(r)
				if err != nil {
					res = append(res, harness.Fail(name, r.Name, "%v", err))
				}
				outs = append(outs, o...)
			}
			got := map[string]bool{}
			for _, o := range outs {
				got[o.ID] = true
				detail := fmt.Sprintf("named %s; %s", o.Named, o.Reason)
				res = append(res, harness.Result{Area: name, Case: o.ID, Pass: o.Pass, Detail: detail})
			}
			for _, c := range decodeCases(e, sweep) {
				if !got[c.ID] {
					res = append(res, harness.Fail(name, c.ID, "not run (the run ended first)"))
				}
			}
			writeDecodeTable(e, name, outs)
			return res
		},
	}
}

// readOutcomes reads a run's cases.
func readOutcomes(r *harness.Run) ([]busOutcome, error) {
	b, err := os.ReadFile(r.File(decodeHostFile))
	if err != nil {
		return nil, fmt.Errorf("no host results: %w", err)
	}
	var out []busOutcome
	for l := range bytes.SplitSeq(bytes.TrimSpace(b), []byte("\n")) {
		var o busOutcome
		if json.Unmarshal(l, &o) == nil {
			out = append(out, o)
		}
	}
	return out, nil
}

// writeDecodeTable writes the sweep's table to build/<area>-table.txt and
// the terminal: bus, AUTO verdict, decoded against truth, pass, reason.
func writeDecodeTable(e *env, area string, outs []busOutcome) {
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "BUS\tON\tAUTO\tDECODED\tTRUTH\tRESULT\tREASON") // a strings.Builder does not fail
	pass := 0
	for _, o := range outs {
		v := "FAIL"
		if o.Pass {
			v, pass = passWord, pass+1
		}
		var on []string
		for _, r := range slices.Sorted(maps.Keys(o.Place)) {
			on = append(on, r+"="+o.Place[r])
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", o.ID, strings.Join(on, ","), o.Named, o.Decoded, o.Truth, v, o.Reason)
	}
	_ = tw.Flush()
	fmt.Fprintf(&b, "\n%d/%d pass\n", pass, len(outs))
	_ = os.MkdirAll(filepath.Join(e.root, "build"), 0o755)                                          //nolint:gosec // G301: a build output directory of the repository
	_ = os.WriteFile(filepath.Join(e.root, "build", area+"-table.txt"), []byte(b.String()), 0o644) //nolint:gosec // G306: a build report of the repository
	fmt.Fprint(os.Stderr, "\n"+b.String())
}

// decodeHost runs cases against the app while it runs.
func decodeHost(ctx context.Context, dir string, cases []busCase) {
	f, err := os.Create(filepath.Join(dir, decodeHostFile)) //nolint:gosec // G304: the run's own directory
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	u := &uiRig{ctx: ctx, dir: dir, cl: &http.Client{Timeout: 30 * time.Second}}
	up := until(ctx, 120*time.Second, func() bool {
		var st map[string]any
		return u.get("/api/status", &st) == nil
	})
	enc := json.NewEncoder(f)
	if !up {
		for _, c := range cases {
			_ = enc.Encode(busOutcome{busCase: c, Reason: "the app's web server never answered"}) // a file the check reads
		}
		return
	}
	var ws map[string]any
	_ = u.post("/api/panel", `{"workspace":"bus"}`, &ws) // a refusal shows as the cases' failures
	for _, c := range cases {
		if ctx.Err() != nil {
			return
		}
		o := runBusCase(u, c)
		if ctx.Err() != nil {
			return // the run ended inside the case: it reports as not run
		}
		_ = enc.Encode(o) // a file the check reads; a lost line is a case not run
		_ = f.Sync()
	}
}

// runBusCase places one bus, lets AUTO in BUS find it and decodes the app's
// record with the settings AUTO filled in.
func runBusCase(u *uiRig, c busCase) busOutcome {
	o := busOutcome{busCase: c}
	bus, err := synth.Make(c.Spec, 0)
	if err != nil {
		o.Reason = err.Error()
		return o
	}
	o.Truth = truthText(bus.Truth)
	// every input idle, then the bus's lines on their channels, switched on
	if rep := u.ctl("signals C1=dc,0,0,0;C2=dc,0,0,0;C3=dc,0,0,0;C4=dc,0,0,0"); !strings.HasPrefix(rep, "ok") {
		o.Reason = "simulator: " + rep
		return o
	}
	args := []string{"proto", c.Spec.Proto, fmt.Sprintf("vpp=%g", c.Vpp), fmt.Sprintf("offset=%g", c.Off)}
	for r, ch := range c.Place {
		args = append(args, r+"="+ch)
	}
	for k, v := range c.Spec.Set {
		args = append(args, k+"="+v)
	}
	if rep := u.ctl(strings.Join(args, " ")); !strings.HasPrefix(rep, "ok") {
		o.Reason = "simulator: " + rep
		return o
	}
	for _, ch := range c.Place {
		_, _ = scpiQuery(u.ctx, ch+":TRA ON") // a channel left off shows as AUTO finding nothing
	}
	time.Sleep(500 * time.Millisecond)
	// an AUTO still running (a case before that timed out) would take the
	// press as its cancel: wait it out first
	until(u.ctx, 60*time.Second, func() bool { return !u.panel().AutosetBusy })
	before := u.panel().AutosetDone
	var rep map[string]any
	if err := u.post("/api/panel", `{"button":"auto","for":"bus"}`, &rep); err != nil {
		o.Reason = "AUTO: " + err.Error()
		return o
	}
	done := until(u.ctx, 150*time.Second, func() bool {
		st := u.panel()
		return !st.AutosetBusy && st.AutosetDone != before
	})
	if !done {
		o.Reason = "AUTO did not finish within 150 s"
	}
	p, named, settings := busSettings(u)
	o.Named, o.Settings = named, settings
	switch {
	case p.Proto == framecalc.ProtoOff || p.Proto == framecalc.ProtoAuto:
		o.Reason = strings.TrimSpace(o.Reason + " AUTO named no bus")
		return o
	case named != bus.Proto:
		o.Reason = fmt.Sprintf("AUTO named %s, the bus is %s", named, bus.Proto)
		if slices.Contains(bus.Truth.Alike, named) {
			o.Reason += " (the same line read coarser)"
		}
		return o
	}
	f, err := u.rawFrame()
	if err != nil {
		o.Reason = "record: " + err.Error()
		return o
	}
	r := framecalc.Decode(f, p)
	o.Record = recordText(f, p)
	o.Decoded = shorten(r.Text, 120)
	o.Pass, o.Reason = matchTruth(r, bus.Truth)
	if !o.Pass {
		o.Reason += "; record " + o.Record
	}
	return o
}

// busSettings reads the BUS sheet AUTO filled in as the decoder's
// parameters, the protocol's name and the settings as text.
func busSettings(u *uiRig) (framecalc.DecodeParams, string, string) {
	var sheets struct {
		Tabs []struct {
			ID   string
			Rows []struct {
				ID        string
				Text      string
				Value     float64
				Available bool
			}
		}
	}
	_ = u.get("/api/sheets", &sheets) // unreadable sheets read as no bus
	p := framecalc.DecodeParams{Format: "hex"}
	var set []string
	chOf := func(t string) int {
		var n int
		if _, err := fmt.Sscanf(t, "CH%d", &n); err == nil {
			return n - 1
		}
		return 0
	}
	for _, t := range sheets.Tabs {
		if t.ID != "bus" {
			continue
		}
		for _, r := range t.Rows {
			if !r.Available {
				continue
			}
			switch r.ID {
			case "bus.protocol":
				p.Proto = int(r.Value)
			case "bus.source", "bus.scl", "bus.clk":
				p.ChA = chOf(r.Text)
			case "bus.sda", "bus.data":
				p.ChB = chOf(r.Text)
			case "bus.spi_mode":
				m := int(r.Value)
				p.CPOL, p.CPHA = m >= 2, m%2 == 1
			case "bus.bit_order":
				p.LSB = strings.HasPrefix(r.Text, "LSB")
			case "bus.parity":
				if r.Text != "Auto" {
					p.Parity = strings.ToLower(r.Text)
				}
			case "bus.uart_line":
				p.Inverted = r.Text == "Inverted"
			case "bus.baud":
				p.Baud = baudOf(r.Text)
			default:
				continue
			}
			set = append(set, strings.TrimPrefix(r.ID, "bus.")+"="+r.Text)
		}
	}
	return p, framecalc.ResultName(p.Proto), strings.Join(set, " ")
}

// baudOf reads a baud row's text ("115k2", "9k6", "1M").
func baudOf(t string) int {
	t = strings.TrimSuffix(strings.TrimSuffix(t, "b/s"), "Bd")
	mul := 1.0
	for _, u := range []struct {
		s string
		m float64
	}{{"k", 1e3}, {"M", 1e6}} {
		if i := strings.Index(t, u.s); i >= 0 {
			t = t[:i] + "." + t[i+1:]
			mul = u.m
		}
	}
	var v float64
	if _, err := fmt.Sscanf(strings.TrimSuffix(t, "."), "%g", &v); err != nil {
		return 0
	}
	return int(v*mul + 0.5)
}

// rawFrame fetches the app's record as v2 raw samples, as a sample frame.
func (u *uiRig) rawFrame() (*sample.Frame, error) {
	req, err := http.NewRequestWithContext(u.ctx, http.MethodGet, webBase+"/api/frame.bin?v=2&raw=1&waitms=2000", nil)
	if err != nil {
		return nil, err
	}
	r, err := u.cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Body.Close() }()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	wf, err := wire.DecodeFrame2(b)
	if err != nil {
		return nil, err
	}
	return sampleFrame(wf)
}

// sampleFrame turns a v2 frame's raw traces into a sample frame indexed by
// channel.
func sampleFrame(wf wire.Frame) (*sample.Frame, error) {
	h := wf.Header
	f := &sample.Frame{Content: sample.RawFullRate, Flags: sample.Ordered, SampleS: h.SampleS, CaptureS: h.CaptureS, TriggerAt: h.TriggerAt, Seq: h.Seq}
	n := 0
	for _, t := range wf.Traces {
		n = max(n, t.Codes.Len())
	}
	if n == 0 || h.SampleS <= 0 {
		return nil, fmt.Errorf("no samples in the record (%s)", h.Content)
	}
	f.Traces = make([]sample.Trace, 4)
	for i := range f.Traces {
		f.Traces[i] = sample.Trace{Ch: i, Valid: n}
	}
	for _, t := range wf.Traces {
		if t.Ch < 0 || t.Ch >= 4 || t.Codes.Len() == 0 {
			continue
		}
		tr := &f.Traces[t.Ch]
		tr.Format = sample.CodeFormat{Bits: t.Fmt.Bits, Shift: t.Fmt.Shift, Frac: t.Fmt.Frac, Min: t.Fmt.Min, Max: t.Fmt.Max, Mid: t.Fmt.Mid}
		if t.Codes.U16 != nil {
			tr.Format.Storage, tr.Codes16 = sample.U16, t.Codes.U16
		} else {
			tr.Format.Storage, tr.Codes8 = sample.U8, t.Codes.U8
		}
	}
	return f, nil
}

// matchTruth compares a decode with the generator's truth: one whole unit
// of the bus's bytes (its repeating message) in order in the decoded bytes,
// and every token the truth names in the text.
func matchTruth(r decode.Result, t synth.Truth) (bool, string) {
	if !r.OK && len(r.Bytes) == 0 {
		return false, "no decode: " + r.Error
	}
	for _, w := range t.Contains {
		if !strings.Contains(r.Text, w) {
			return false, fmt.Sprintf("the decode lacks %q", w)
		}
	}
	if t.Bytes == nil {
		return true, "every truth token decoded"
	}
	unit := repeatUnit(t.Bytes)
	if contains(r.Bytes, unit) {
		return true, fmt.Sprintf("a whole message of %d values decoded", len(unit))
	}
	// the record AUTO framed holds less than a repetition of the bus: the
	// longest run it decoded that the bus sends (its edges cut values)
	if n := longestCyclic(r.Bytes, unit); n >= min(4, len(unit)) {
		return true, fmt.Sprintf("partial: the record holds a run of %d of the message's %d values, all right", n, len(unit))
	}
	return false, fmt.Sprintf("decoded %s, want %s", hexOf(r.Bytes, 16), hexOf(unit, 16))
}

// longestCyclic is the longest run of b that the endless repetition of unit
// holds.
func longestCyclic(b, unit []int) int {
	if len(unit) == 0 {
		return 0
	}
	best := 0
	for i := range b {
		for s := range unit {
			n := 0
			for i+n < len(b) && b[i+n] == unit[(s+n)%len(unit)] {
				n++
			}
			best = max(best, n)
		}
	}
	return best
}

// repeatUnit is the shortest prefix whose repetition makes b.
func repeatUnit(b []int) []int {
	for n := 1; n <= len(b); n++ {
		if len(b)%n != 0 {
			continue
		}
		ok := true
		for i := n; i < len(b) && ok; i++ {
			ok = b[i] == b[i-n]
		}
		if ok {
			return b[:n]
		}
	}
	return b
}

// contains reports sub as a contiguous run of b.
func contains(b, sub []int) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(b); i++ {
		if slices.Equal(b[i:i+len(sub)], sub) {
			return true
		}
	}
	return false
}

func hexOf(b []int, n int) string {
	var s []string
	for i, v := range b {
		if i == n {
			s = append(s, "…")
			break
		}
		s = append(s, fmt.Sprintf("%02X", v))
	}
	return strings.Join(s, " ")
}

// truthText is the truth as the table shows it.
func truthText(t synth.Truth) string {
	if t.Bytes != nil {
		return hexOf(repeatUnit(t.Bytes), 12)
	}
	return shorten(strings.Join(t.Contains, " | "), 60)
}

func shorten(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
