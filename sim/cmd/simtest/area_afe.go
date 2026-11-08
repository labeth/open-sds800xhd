package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/labeth/open-sds800xhd/sim/harness"
)

// The afe area reads the front end back through SCPI measurements against
// the simulator's front-end model: the V/div ladder, the offset, the three
// couplings, the 20 MHz limit and a probe's attenuation. The model is the
// realistic one (a unit's gain and offset error, noise, bandwidth): each
// reading is checked against what the model's transfer predicts for the
// settings the app programmed (ctl "afe"), so the check holds the app to
// programming the hardware it means, and the model's departures from the
// default tables show as the reading's error.

const afeHostFile = "afe-host.jsonl"

// afeChan is the model's view of a channel (simd ctl "afe").
type afeChan struct {
	VdivSet  float64 `json:"vdiv_set"`
	VdivTrue float64 `json:"vdiv_true"`
	VoffSet  float64 `json:"voff_set"`
	VoffTrue float64 `json:"voff_true"`
	BWHz     float64 `json:"bw_hz"`
	RiseS    float64 `json:"rise_s"`
	NoiseV   float64 `json:"noise_v"`
	GainErr  float64 `json:"gain_err"`
	DC       bool    `json:"dc"`
	GndSub   bool    `json:"gnd_sub"`
}

// reading is what the app reads for an input of v volts at the BNC: the
// unit's transfer, read back with the tables the app programmed by.
func (c afeChan) reading(v float64) float64 {
	g := c.VdivSet / c.VdivTrue
	return v*g + c.VoffTrue*g - c.VoffSet
}

// afeOutcome is one reading.
type afeOutcome struct {
	Case     string  `json:"case"`
	Item     string  `json:"item"`
	Measured float64 `json:"measured"`
	Model    float64 `json:"model"`
	Ideal    float64 `json:"ideal"` // what a perfect front end reads
	Tol      float64 `json:"tol"`
	Pass     bool    `json:"pass"`
	Note     string  `json:"note"`
}

func init() {
	register(area{
		name: "afe", title: "front end vs the model: V/div ladder, offset, coupling, BW limit, probe",
		runs: func(e *env) []harness.Spec {
			return []harness.Spec{{
				Name: e.prefix + "afe", Secs: 290, Ports: true,
				SimdArgs: "-afe real,seed=1 -signals C1=dc,0,0,0;C2=dc,0,0,0;C3=dc,0,0,0;C4=dc,0,0,0",
				App:      e.app, Hook: e.hook, GuestBoot: e.guestBoot,
				Host: func(ctx context.Context, dir string) { afeHost(ctx, dir) },
			}}
		},
		check: func(e *env, runs []*harness.Run) []harness.Result {
			b, err := os.ReadFile(runs[0].File(afeHostFile))
			if err != nil {
				return []harness.Result{harness.Fail("afe", "host", "no host results: %v", err)}
			}
			var outs []afeOutcome
			var res []harness.Result
			for l := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
				var o afeOutcome
				if json.Unmarshal([]byte(l), &o) != nil {
					continue
				}
				outs = append(outs, o)
				res = append(res, harness.Result{Area: "afe", Case: o.Case + "/" + o.Item, Pass: o.Pass,
					Detail: fmt.Sprintf("measured %.4g, model %.4g ± %.2g (ideal %.4g) %s", o.Measured, o.Model, o.Tol, o.Ideal, o.Note)})
			}
			if len(res) == 0 {
				res = append(res, harness.Fail("afe", "host", "the host checks reported nothing"))
			}
			writeAFETable(e, outs)
			return res
		},
	})
}

// writeAFETable writes the front-end table to build/afe-table.txt and the
// terminal.
func writeAFETable(e *env, outs []afeOutcome) {
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "CASE\tREADING\tMEASURED\tMODEL\tTOL\tIDEAL\tRESULT\tNOTE") // a strings.Builder does not fail
	pass := 0
	for _, o := range outs {
		v := "FAIL"
		if o.Pass {
			v, pass = passWord, pass+1
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%.4g\t%.4g\t%.2g\t%.4g\t%s\t%s\n", o.Case, o.Item, o.Measured, o.Model, o.Tol, o.Ideal, v, o.Note)
	}
	_ = tw.Flush()
	fmt.Fprintf(&b, "\n%d/%d pass\n", pass, len(outs))
	_ = os.MkdirAll(filepath.Join(e.root, "build"), 0o755)                                          //nolint:gosec // G301: a build output directory of the repository
	_ = os.WriteFile(filepath.Join(e.root, "build", "afe-table.txt"), []byte(b.String()), 0o644) //nolint:gosec // G306: a build report of the repository
	fmt.Fprint(os.Stderr, "\n"+b.String())
}

// afeRig is the host's side of the area's run.
type afeRig struct {
	*uiRig
	out *json.Encoder
}

// model reads the model's view of channel ch (0-based).
func (a *afeRig) model(ch int) (afeChan, error) {
	var m struct {
		Channels []afeChan `json:"channels"`
	}
	rep := a.ctl("afe")
	if err := json.Unmarshal([]byte(rep), &m); err != nil || len(m.Channels) <= ch {
		return afeChan{}, fmt.Errorf("afe: %q", rep)
	}
	return m.Channels[ch], nil
}

// measure reads one simple measurement of C1 as a number, once two readings
// in a row agree within tol (the frames after a change have arrived).
func (a *afeRig) measure(item string, tol float64) (float64, error) {
	var last float64
	var err error
	for i := range 40 {
		var rep string
		rep, err = scpiQuery(a.ctx, ":MEAS:SIMP:VAL? "+item)
		if err == nil {
			var v float64
			v, err = strconv.ParseFloat(strings.TrimSpace(rep), 64)
			if err == nil {
				if i > 0 && math.Abs(v-last) <= tol {
					return v, nil
				}
				last = v
			}
		}
		time.Sleep(400 * time.Millisecond)
	}
	if err != nil {
		return 0, err
	}
	return last, fmt.Errorf("%s did not settle (last %g)", item, last)
}

// setup sets C1's input and the scope, then waits for the app to settle.
func (a *afeRig) setup(signal string, scpi ...string) error {
	if rep := a.ctl("signals C1=" + signal); !strings.HasPrefix(rep, "ok") {
		return fmt.Errorf("simulator: %s", rep)
	}
	for _, l := range scpi {
		if _, err := scpiQuery(a.ctx, l); err != nil {
			return fmt.Errorf("%s: %w", l, err)
		}
	}
	time.Sleep(1500 * time.Millisecond)
	return nil
}

// check records one reading against the model's prediction.
func (a *afeRig) check(c, item string, model, ideal, tol float64, note string) {
	o := afeOutcome{Case: c, Item: item, Model: model, Ideal: ideal, Tol: tol, Note: note}
	v, err := a.measure(item, tol/2)
	o.Measured = v
	switch {
	case err != nil:
		o.Note = strings.TrimSpace(note + " " + err.Error())
	default:
		o.Pass = math.Abs(v-model) <= tol
	}
	_ = a.out.Encode(o) // a file the check reads; a lost line is a missing case
}

func afeHost(ctx context.Context, dir string) {
	f, err := os.Create(filepath.Join(dir, afeHostFile)) //nolint:gosec // G304: the run's own directory
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	a := &afeRig{uiRig: &uiRig{ctx: ctx, dir: dir, cl: &http.Client{Timeout: 10 * time.Second}}, out: json.NewEncoder(f)}
	fail := func(c string, err error) {
		_ = a.out.Encode(afeOutcome{Case: c, Item: "setup", Note: err.Error()}) // a file the check reads
	}
	if !until(ctx, 120*time.Second, func() bool { var st map[string]any; return a.get("/api/status", &st) == nil }) {
		fail("app", fmt.Errorf("the app's web server never answered"))
		return
	}
	if _, err := scpiQuery(ctx, ":MEAS:SIMP:SOUR C1"); err != nil {
		fail("app", err)
		return
	}
	afeLadder(a, fail)
	afeOffset(a, fail)
	afeCoupling(a, fail)
	afeBandwidth(a, fail)
	afeProbe(a, fail)
}

// afeLadder: a ±2-division square at each V/div of the ladder, read as its
// top, base and amplitude.
func afeLadder(a *afeRig, fail func(string, error)) {
	for _, vd := range []float64{0.005, 0.02, 0.1, 0.5, 2} {
		c := fmt.Sprintf("ladder/%gV", vd)
		amp := 4 * vd
		if err := a.setup(fmt.Sprintf("square,1e3,%g,0", amp), ":CHAN1:COUP DC", ":CHAN1:BWL FULL", ":CHAN1:PROB VAL,1",
			fmt.Sprintf(":CHAN1:SCAL %g", vd), ":CHAN1:OFFS 0", ":TIM:SCAL 2E-4"); err != nil {
			fail(c, err)
			continue
		}
		m, err := a.model(0)
		if err != nil {
			fail(c, err)
			continue
		}
		tol := 0.006*amp + 4*m.NoiseV + vd/48
		a.check(c, "TOP", m.reading(amp/2), amp/2, tol, fmt.Sprintf("gain error %+.2f%%", 100*m.GainErr))
		a.check(c, "BASE", m.reading(-amp/2), -amp/2, tol, "")
		a.check(c, "AMPL", m.reading(amp/2)-m.reading(-amp/2), amp, tol, "")
	}
}

// afeOffset: a DC input read with the trace moved by the offset; the
// reading stays the input's (the unit's offset error aside).
func afeOffset(a *afeRig, fail func(string, error)) {
	for _, off := range []float64{-0.3, 0.2} {
		c := fmt.Sprintf("offset/%+gV", off)
		if err := a.setup("dc,0,0,0.1", ":CHAN1:COUP DC", ":CHAN1:SCAL 0.1", fmt.Sprintf(":CHAN1:OFFS %g", off), ":TIM:SCAL 2E-4"); err != nil {
			fail(c, err)
			continue
		}
		m, err := a.model(0)
		if err != nil {
			fail(c, err)
			continue
		}
		note := fmt.Sprintf("DAC offset %.3f V programmed, %.3f V the unit's", m.VoffSet, m.VoffTrue)
		a.check(c, "MEAN", m.reading(0.1), 0.1, 0.006*0.1+4*m.NoiseV+0.1/48+0.002, note)
	}
}

// afeCoupling: a quarter-duty square off its mid level, read as its top and
// base: DC keeps the levels, AC blocks the true mean (0.2 V: the levels move
// to +0.3 and -0.1 V, not to the swing's mid level), GND reads no input.
func afeCoupling(a *afeRig, fail func(string, error)) {
	const sig = "square,1e3,0.4,0.3,duty=0.25" // 0.1 … 0.5 V, mean 0.2 V
	for _, cpl := range []string{"DC", "AC", "GND"} {
		c := "coupling/" + cpl
		if err := a.setup(sig, ":CHAN1:SCAL 0.2", ":CHAN1:OFFS 0", ":CHAN1:COUP "+cpl, ":TIM:SCAL 2E-4"); err != nil {
			fail(c, err)
			continue
		}
		m, err := a.model(0)
		if err != nil {
			fail(c, err)
			continue
		}
		note := fmt.Sprintf("model coupling dc=%v gnd=%v", m.DC, m.GndSub)
		tol := 0.006*0.4 + 4*m.NoiseV + 0.2/48 + 0.002
		switch cpl {
		case "GND":
			a.check(c, "MEAN", 0, 0, tol, note) // the PL's substitution: the trace at its position, no input
		default:
			shift := map[string]float64{"DC": 0, "AC": 0.2}[cpl]
			a.check(c, "TOP", m.reading(0.5-shift), 0.5-shift, tol, note)
			a.check(c, "BASE", m.reading(0.1-shift), 0.1-shift, tol, "")
		}
	}
	_, _ = scpiQuery(a.ctx, ":CHAN1:COUP DC") // a later case sets what it needs
	afeClip(a, fail)
}

// afeClip: a square whose top lies past the converter's range reads at the
// rail (code 4095), its base as it is.
func afeClip(a *afeRig, fail func(string, error)) {
	c := "clip/0.1V"
	if err := a.setup("square,1e3,0.4,0.3,duty=0.25", ":CHAN1:SCAL 0.1", ":CHAN1:OFFS 0", ":CHAN1:COUP DC", ":TIM:SCAL 2E-4"); err != nil {
		fail(c, err)
		return
	}
	m, err := a.model(0)
	if err != nil {
		fail(c, err)
		return
	}
	rail := (4095.0-2048)/480*m.VdivSet - m.VoffSet
	tol := 0.1/48 + 4*m.NoiseV + 0.002
	a.check(c, "TOP", rail, 0.5, tol, fmt.Sprintf("0.5 V in, the rail at %.3f V", rail))
	a.check(c, "BASE", m.reading(0.1), 0.1, tol, "")
}

// afeBandwidth: a fast square's rise time with the 20 MHz limit on and off,
// against the model's chain.
func afeBandwidth(a *afeRig, fail func(string, error)) {
	for _, bwl := range []string{"20M", "FULL"} {
		c := "bandwidth/" + bwl
		if err := a.setup("square,1e6,0.8,0", ":CHAN1:COUP DC", ":CHAN1:SCAL 0.2", ":CHAN1:OFFS 0", ":CHAN1:BWL "+bwl, ":TIM:SCAL 2E-8"); err != nil {
			fail(c, err)
			continue
		}
		m, err := a.model(0)
		if err != nil {
			fail(c, err)
			continue
		}
		// the measurement's own resolution: a sample at 2 GSa/s
		tol := 0.15*m.RiseS + 0.5e-9
		a.check(c, "RISE", m.RiseS, 0, tol, fmt.Sprintf("model bandwidth %.3g Hz", m.BWHz))
	}
	_, _ = scpiQuery(a.ctx, ":CHAN1:BWL FULL") // a later case sets what it needs
}

// afeProbe: a 20 Vpp tip voltage through a 10x probe, the scope told so.
func afeProbe(a *afeRig, fail func(string, error)) {
	c := "probe/10x"
	if err := a.setup("square,1e3,20,0,probe=10", ":CHAN1:COUP DC", ":CHAN1:PROB VAL,10", ":CHAN1:SCAL 5", ":CHAN1:OFFS 0", ":TIM:SCAL 2E-4"); err != nil {
		fail(c, err)
		return
	}
	m, err := a.model(0)
	if err != nil {
		fail(c, err)
		return
	}
	// the model reads at the BNC (2 Vpp); the scope scales by the probe
	want := 10 * (m.reading(1) - m.reading(-1))
	a.check(c, "AMPL", want, 20, 0.006*20+40*m.NoiseV+5.0/48, "")
	_, _ = scpiQuery(a.ctx, ":CHAN1:PROB VAL,1") // back to 1x
}
