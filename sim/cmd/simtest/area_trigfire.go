package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/labeth/open-dso-core/webserver/wire"

	"github.com/labeth/open-sds800xhd/sim/harness"
)

// The trigfire area checks the trigger end to end: the simulator's model of
// the trigger nodes fires (or not) on the injected signals with what the
// firmware programmed, and the record must hold the trigger at the analytic
// instant. The in-guest board tests run every trigger type and its forms
// through the HAL (TestTriggerFire, TestTriggerFireSerial); the app run
// drives the edge trigger, the sweep modes and holdoff through SCPI and the
// web, which carry the edge trigger. Every case is one row of the table
// written to build/trigfire-table.txt: type, form, expected, measured,
// verdict.
func init() {
	register(area{
		name: "trigfire", title: "trigger firing end to end: every type fires on its signal, not on its near miss",
		runs: func(e *env) []harness.Spec {
			return []harness.Spec{
				{
					Name: e.prefix + "trigfire", Secs: 200, SimdArgs: signals,
					Board: e.boardtest, BoardArgs: boardArgs("^TestTriggerFire(Serial)?$"),
				},
				{
					Name: e.prefix + "trigfire-app", Secs: 240, Ports: true, SimdArgs: signals,
					App: e.app, Hook: e.hook, GuestBoot: e.guestBoot,
					Host: func(ctx context.Context, dir string) { trigfireHost(ctx, dir) },
				},
			}
		},
		check: func(e *env, runs []*harness.Run) []harness.Result {
			var res []harness.Result
			var rows [][]string
			r := runs[0]
			for _, test := range []string{"TriggerFire", "TriggerFireSerial"} {
				if !strings.Contains(r.Console, "--- PASS: Test"+test) {
					res = append(res, harness.Fail("trigfire", test, "Test%s did not pass in the guest", test))
				}
			}
			g := harness.GuestResults(r, "trigfire")
			res = append(res, g...)
			if len(g) == 0 {
				res = append(res, harness.Fail("trigfire", "board", "the board tests reported nothing"))
			}
			for _, l := range r.Lines("L3ROW ") {
				rows = append(rows, append([]string{"HAL"}, strings.Split(l, "|")...))
			}
			h, hostRows := trigHostResults(runs[1])
			res = append(res, h...)
			rows = append(rows, hostRows...)
			writeTable(e, rows)
			return res
		},
	})
}

const (
	trigHostFile = "trigfire-host.txt"
	statTrigd    = "Trig'd"
	typEdge      = "edge"
)

// trigHostResults reads the host's rows: "ROW via|type|form|expected|measured|PASS".
func trigHostResults(r *harness.Run) ([]harness.Result, [][]string) {
	b, err := os.ReadFile(r.File(trigHostFile))
	if err != nil {
		return []harness.Result{harness.Fail("trigfire", "host", "no host checks: %v", err)}, nil
	}
	var res []harness.Result
	var rows [][]string
	for l := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		f := strings.Split(strings.TrimPrefix(l, "ROW "), "|")
		if len(f) != 6 {
			continue
		}
		rows = append(rows, f)
		res = append(res, harness.Result{
			Area: "trigfire", Case: f[0] + " " + f[1] + " " + f[2], Pass: f[5] == passWord,
			Detail: "want " + f[3] + "; got " + f[4],
		})
	}
	if len(rows) == 0 {
		res = append(res, harness.Fail("trigfire", "host", "the host checks reported nothing"))
	}
	return res, rows
}

// writeTable writes the table of every case.
func writeTable(e *env, rows [][]string) {
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "VIA\tTYPE\tFORM\tEXPECTED\tMEASURED\tVERDICT")
	for _, r := range rows {
		_, _ = fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	_ = tw.Flush()
	_ = os.MkdirAll(filepath.Join(e.root, "build"), 0o755)                                            //nolint:gosec // G301: a build output directory of the repository
	_ = os.WriteFile(filepath.Join(e.root, "build", "trigfire-table.txt"), []byte(b.String()), 0o644) //nolint:gosec // G306: a build report of the repository
	fmt.Fprint(os.Stderr, b.String())
}

// trigCase is one SCPI/web case on C1.
type trigCase struct {
	typ, form string
	sigs      string
	set       string  // SCPI commands
	level     float64 // the crossing expected at the trigger
	dir       int     // +1 rising, −1 falling
	fires     bool
	stat      string // :TRIG:STAT? reply wanted ("" any)
}

// trigfireHost runs the SCPI and web cases against the app while it runs.
func trigfireHost(ctx context.Context, dir string) {
	f, err := os.Create(filepath.Join(dir, trigHostFile)) //nolint:gosec // G304: the run's own directory
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	rowf := func(via, typ, form, want, got string, ok bool) {
		v := failWord
		if ok {
			v = passWord
		}
		_, _ = fmt.Fprintf(f, "ROW %s|%s|%s|%s|%s|%s\n", via, typ, form, want, got, v)
	}
	cl := &http.Client{Timeout: 10 * time.Second}
	get := func(path string) ([]byte, int) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, webBase+path, nil)
		if err != nil {
			return nil, 0
		}
		r, err := cl.Do(req)
		if err != nil {
			return nil, 0
		}
		defer func() { _ = r.Body.Close() }()
		b, _ := io.ReadAll(r.Body)
		return b, r.StatusCode
	}
	for up := false; !up && ctx.Err() == nil; {
		_, code := get("/api/status")
		up = code == http.StatusOK
		if !up {
			time.Sleep(time.Second)
		}
	}
	if ctx.Err() != nil {
		return
	}
	time.Sleep(3 * time.Second)
	ctl := func(cmd string) string {
		var d net.Dialer
		c, err := d.DialContext(ctx, "unix", filepath.Join(dir, "sock", "ctl.sock"))
		if err != nil {
			return err.Error()
		}
		defer func() { _ = c.Close() }()
		_, _ = io.WriteString(c, cmd+"\n")
		s, _ := bufio.NewReader(c).ReadString('\n')
		return strings.TrimSpace(s)
	}
	base := ":CHAN1:SWIT ON;:CHAN1:SCAL 1;:CHAN1:OFFS 0;:CHAN2:SWIT OFF;:CHAN3:SWIT OFF;:CHAN4:SWIT OFF;:TIM:SCAL 2E-7;" +
		":TRIG:EDGE:SOUR C1;:TRIG:EDGE:COUP DC;:TRIG:EDGE:HOLD OFF;"
	sine := "C1=sine,1e6,2,0"
	cases := []trigCase{
		{typEdge, "normal, rising 0.5 V", sine, ":TRIG:EDGE:SLOP RIS;:TRIG:EDGE:LEV 0.5;:TRIG:MODE NORM", 0.5, 1, true, statTrigd},
		{typEdge, "normal, falling -0.5 V", sine, ":TRIG:EDGE:SLOP FALL;:TRIG:EDGE:LEV -0.5;:TRIG:MODE NORM", -0.5, -1, true, statTrigd},
		{
			typEdge, "normal, AC coupling, 0.25 V above a 2 V mean", "C1=sine,1e6,1,2",
			":TRIG:EDGE:SLOP RIS;:TRIG:EDGE:COUP AC;:TRIG:EDGE:LEV 0.25;:TRIG:MODE NORM", 2.25, 1, true, statTrigd,
		},
		{typEdge, "normal, level above the signal", sine, ":TRIG:EDGE:SLOP RIS;:TRIG:EDGE:LEV 1.5;:TRIG:MODE NORM", 0, 0, false, ""},
		{typEdge, "auto, level above the signal", sine, ":TRIG:EDGE:SLOP RIS;:TRIG:EDGE:LEV 1.5;:TRIG:MODE AUTO", 0, 0, false, "Auto"},
		{typEdge, "auto, rising 0.5 V", sine, ":TRIG:EDGE:SLOP RIS;:TRIG:EDGE:LEV 0.5;:TRIG:MODE AUTO", 0.5, 1, true, statTrigd},
	}
	for _, c := range cases {
		if ctx.Err() != nil {
			return
		}
		ctl("signals " + c.sigs)
		_, err := scpiQuery(ctx, base+c.set)
		time.Sleep(2500 * time.Millisecond)
		stat, _ := scpiQuery(ctx, ":TRIG:STAT?")
		want := posWantText(c.level, c.dir)
		if !c.fires {
			want = "no trigger"
		}
		if c.stat != "" {
			rowf("SCPI", c.typ, c.form+": status", c.stat, stat, stat == c.stat)
		}
		if !c.fires && strings.Contains(c.form, "normal") {
			ok := stat != statTrigd && stat != "Auto"
			rowf("SCPI", c.typ, c.form+": status", "Arm or Ready", stat, ok)
			b, _ := get("/api/frame.bin?v=2&waitms=10")
			fr, _ := wire.DecodeFrame2(b)
			b, _ = get(fmt.Sprintf("/api/frame.bin?v=2&waitms=2000&since=%d", fr.Header.Seq))
			nx, err := wire.DecodeFrame2(b)
			still := err == nil && (nx.Header.Unchanged || nx.Header.Seq == fr.Header.Seq)
			rowf("web", c.typ, c.form, "no new frame in 2 s", fmt.Sprintf("seq %d → %d (unchanged %v)", fr.Header.Seq, nx.Header.Seq, nx.Header.Unchanged), still)
			continue
		}
		if !c.fires { // auto without the condition: frames, but none holds a crossing at the trigger
			locked := lockedOver(ctx, 3)
			rowf("SCPI", c.typ, c.form, "untriggered: no crossing held at time 0", fmt.Sprintf("locked over 3 reads: %v", locked), !locked)
			_, _, trigd := webTrigPos(ctx, get, 0.5, 1)
			rowf("web", c.typ, c.form, "trigd false", fmt.Sprintf("trigd %v", trigd), !trigd)
			continue
		}
		// SCPI waveform: time 0 is the trigger (spec 11 §7.4)
		got, ok := scpiTrigPos(ctx, c.level, c.dir)
		rowf("SCPI", c.typ, c.form, want, got, ok && err == nil)
		wg, wok, trigd := webTrigPos(ctx, get, c.level, c.dir)
		rowf("web", c.typ, c.form, want+", trigd", fmt.Sprintf("%s, trigd %v", wg, trigd), wok && trigd)
	}

	// Single: one frame, then Stop
	ctl("signals " + sine)
	_, _ = scpiQuery(ctx, base+":TRIG:EDGE:SLOP RIS;:TRIG:EDGE:LEV 0.5;:TRIG:MODE SING")
	time.Sleep(2500 * time.Millisecond)
	stat, _ := scpiQuery(ctx, ":TRIG:STAT?")
	got, ok := scpiTrigPos(ctx, 0.5, 1)
	rowf("SCPI", "sweep", "single, rising 0.5 V: status", "Stop", stat, stat == "Stop")
	rowf("SCPI", "sweep", "single, rising 0.5 V", posWantText(0.5, 1), got, ok)

	// After a single shot taken from AUTO, :TRIG:MODE NORM must give NORMal
	_, _ = scpiQuery(ctx, ":TRIG:MODE NORM")
	time.Sleep(time.Second)
	afterShot, _ := scpiQuery(ctx, ":TRIG:MODE?")
	rowf("SCPI", "sweep", "NORM after a single shot taken from AUTO", "NORMal", afterShot, strings.HasPrefix(afterShot, "NORM"))

	// Force from Normal without the condition (AUTO first, so the mode the shot replaced is gone)
	_, _ = scpiQuery(ctx, ":TRIG:MODE AUTO")
	time.Sleep(time.Second)
	_, _ = scpiQuery(ctx, base+":TRIG:EDGE:SLOP RIS;:TRIG:EDGE:LEV 1.5;:TRIG:MODE NORM")
	time.Sleep(2 * time.Second)
	modeBefore, _ := scpiQuery(ctx, ":TRIG:MODE?")
	b, _ := get("/api/frame.bin?v=2&waitms=10")
	before, _ := wire.DecodeFrame2(b)
	_, _ = scpiQuery(ctx, ":TRIG:MODE FTRIG")
	b, _ = get(fmt.Sprintf("/api/frame.bin?v=2&waitms=3000&since=%d", before.Header.Seq))
	after, err := wire.DecodeFrame2(b)
	forced := err == nil && !after.Header.Unchanged && after.Header.Seq != before.Header.Seq
	mode, _ := scpiQuery(ctx, ":TRIG:MODE?")
	rowf("web", "sweep", "force from normal, no condition", "one new frame, marked forced",
		fmt.Sprintf("seq %d → %d, kind %q", before.Header.Seq, after.Header.Seq, after.Header.TrigKind), forced && after.Header.TrigKind == "forced")
	rowf("SCPI", "sweep", "force from normal: mode", "NORMal before and after FTRIG", modeBefore+" → "+mode,
		strings.HasPrefix(modeBefore, "NORM") && strings.HasPrefix(mode, "NORM"))

	// Holdoff 0.5 s: frames at least 0.5 s apart
	_, _ = scpiQuery(ctx, base+":TRIG:EDGE:SLOP RIS;:TRIG:EDGE:LEV 0.5;:TRIG:EDGE:HLDT 0.5;:TRIG:EDGE:HOLD TIME;:TRIG:MODE NORM")
	time.Sleep(2 * time.Second)
	var seqs []uint64
	b, _ = get("/api/frame.bin?v=2&waitms=10")
	fr, _ := wire.DecodeFrame2(b)
	start := time.Now()
	for time.Since(start) < 4*time.Second && ctx.Err() == nil {
		b, _ = get(fmt.Sprintf("/api/frame.bin?v=2&waitms=1500&since=%d", fr.Header.Seq))
		if g, err := wire.DecodeFrame2(b); err == nil && !g.Header.Unchanged {
			fr = g
			seqs = append(seqs, g.Header.Seq)
		}
	}
	rate := float64(len(seqs)) / time.Since(start).Seconds()
	rowf("web", "holdoff", "0.5 s, rising 0.5 V", "≤ 2 frames/s", fmt.Sprintf("%.2f frames/s (%d in 4 s)", rate, len(seqs)), rate <= 2.2 && len(seqs) > 0)
	_, _ = scpiQuery(ctx, ":TRIG:EDGE:HOLD OFF;:TRIG:MODE AUTO")
}

func posWantText(level float64, dir int) string {
	return fmt.Sprintf("%s crossing of %g V at the trigger", map[int]string{1: "rising", -1: "falling"}[dir], level)
}

// lockedOver reports whether n successive waveform reads all hold a rising
// crossing of 0.5 V at time 0: an auto sweep without the condition must not.
func lockedOver(ctx context.Context, n int) bool {
	for range n {
		if _, ok := scpiTrigPos(ctx, 0.5, 1); !ok {
			return false
		}
		time.Sleep(400 * time.Millisecond)
	}
	return true
}

// scpiTrigPos reads C1's waveform and reports where the crossing of level
// in direction dir nearest time 0 lies, in samples before the trigger.
func scpiTrigPos(ctx context.Context, level float64, dir int) (string, bool) {
	tdivS, err := scpiQuery(ctx, ":TIM:SCAL?")
	if err != nil {
		return err.Error(), false
	}
	tdiv, err := strconv.ParseFloat(strings.TrimSpace(tdivS), 64)
	if err != nil {
		return "tdiv " + tdivS, false
	}
	desc, err := scpiBlock(ctx, ":WAV:SOUR C1;:WAV:WIDT WORD;:WAV:STAR 0;:WAV:POIN 0;:WAV:PRE?")
	if err != nil || len(desc) != 346 {
		return fmt.Sprintf("descriptor: %v (%d bytes)", err, len(desc)), false
	}
	le := binary.LittleEndian
	f32 := func(o int) float64 { return float64(math.Float32frombits(le.Uint32(desc[o:]))) }
	gain, off, cpd := f32(156), f32(160), f32(164)
	dt := f32(176)
	ho := math.Float64frombits(le.Uint64(desc[180:]))
	words, err := scpiBlock(ctx, ":WAV:DATA?")
	if err != nil || len(words) < 64 {
		return fmt.Sprintf("data: %v", err), false
	}
	v := make([]float64, len(words)/2)
	for i := range v {
		v[i] = float64(int16(le.Uint16(words[2*i:])))*gain/cpd - off //nolint:gosec // G115: a word sample is a signed 16-bit code
	}
	// time(i) = −5·tdiv + HORIZ_OFFSET + i·interval; the trigger is time 0
	at := (5*tdiv - ho) / dt
	x, ok := nearestCross(v, level, dir, at, 100)
	if !ok {
		return fmt.Sprintf("no crossing near time 0 (index %.1f of %d)", at, len(v)), false
	}
	e := at - x
	return fmt.Sprintf("time 0 %+.2f samples after the crossing (%.3g ns)", e, e*dt*1e9), e >= -0.25 && e <= 1.25
}

// webTrigPos reads a new v2 frame and reports where the crossing nearest
// its trigger lies.
func webTrigPos(ctx context.Context, get func(string) ([]byte, int), level float64, dir int) (string, bool, bool) {
	fr, ok := webFrame(ctx, get, func(fr wire.Frame) bool { return traceOf(fr, 0) != nil })
	if !ok {
		return "no frame", false, false
	}
	t := traceOf(fr, 0)
	v := make([]float64, t.Codes.Len())
	for i := range v {
		v[i] = t.Volts(t.Codes, i)
	}
	at := fr.Header.TriggerAt
	x, have := nearestCross(v, level, dir, at, 100)
	if !have || dir == 0 {
		return fmt.Sprintf("no crossing near the trigger (index %.1f of %d)", at, len(v)), false, fr.Header.Trigd
	}
	e := at - x
	return fmt.Sprintf("trigger %+.2f samples after the crossing", e), e >= -0.25 && e <= 1.25, fr.Header.Trigd
}

func nearestCross(v []float64, level float64, dir int, near float64, span int) (float64, bool) {
	best, ok := 0.0, false
	for i := max(0, int(near)-span); i < min(len(v)-1, int(near)+span); i++ {
		a, b := v[i]-level, v[i+1]-level
		up, down := a < 0 && b >= 0, a >= 0 && b < 0
		if dir > 0 && !up || dir < 0 && !down || dir == 0 && !up && !down {
			continue
		}
		x := float64(i) + a/(a-b)
		if !ok || math.Abs(x-near) < math.Abs(best-near) {
			best, ok = x, true
		}
	}
	return best, ok
}

// scpiBlock sends a query whose reply is a definite block and returns the
// block's bytes.
func scpiBlock(ctx context.Context, line string) ([]byte, error) {
	d := net.Dialer{Timeout: 3 * time.Second}
	c, err := d.DialContext(ctx, "tcp", "127.0.0.1:5025")
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	if _, err := io.WriteString(c, line+"\n"); err != nil {
		return nil, err
	}
	r := bufio.NewReader(c)
	if _, err := r.ReadString('#'); err != nil {
		return nil, err
	}
	nd, err := r.ReadByte()
	if err != nil || nd < '1' || nd > '9' {
		return nil, errors.New("no block header")
	}
	digits := make([]byte, nd-'0')
	if _, err := io.ReadFull(r, digits); err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(string(digits))
	if err != nil {
		return nil, err
	}
	b := make([]byte, n)
	_, err = io.ReadFull(r, b)
	return b, err
}
