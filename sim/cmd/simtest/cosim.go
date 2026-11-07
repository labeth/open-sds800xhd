package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/labeth/open-sds800xhd/sim/harness"
)

// The open-PL co-simulation lane (make cosim-test, simtest -cosim): the same
// guest programs against the simulator whose PL is the open design's RTL,
// Verilated (fpga/cosim, simd -pl open). The areas exercise stage 2 end to
// end: bring-up on the open image, every trigger type firing on the
// simulator's signals, readout windows and reductions, the gap-free stream
// with injected drops, the stacker against a software binning, and the
// app's SCPI over it. Every guest check reports expected and measured
// values; the report tabulates them.

var cosimAreas []area

func registerCosim(a area) { cosimAreas = append(cosimAreas, a) }

// Signal sets of the co-simulation runs.
const (
	cosimTrigSignals  = "-signals C1=sine,1.000123e6,2,0;C2=pulse,5e5,2,0,width=2e-7,rise=5e-9,glitch=2:6e-8:0.8;C3=uart,1e6,3.3,1.65,data=55A3;C4=square,1e6,2,0,burst=5:5"
	cosimProtoSignals = "-signals C1=spi.clk,1e6,3.3,1.65,mode=0,data=A55A;C2=spi.mosi,1e6,3.3,1.65,mode=0,data=A55A;C3=i2c.scl,4e5,3.3,1.65,addr=0x50,rw=w,data=3C;C4=i2c.sda,4e5,3.3,1.65,addr=0x50,rw=w,data=3C"
	cosimSine         = "-signals C1=sine,1.000123e6,2,0;C2=dc,0,0,0;C3=dc,0,0,0;C4=dc,0,0,0"
)

func init() {
	board := func(name, title, test string, secs int, simd string) {
		registerCosim(boardArea(name, title, test, secs, simd, nil, nil))
	}
	board("startup", "start-up on the open image: identity, manifest, clocks, caps", "CosimStartup", 120, cosimSine)
	registerCosim(boardArea("trigger", "every trigger type firing on injected signals; edge position to 1/16 sample",
		"CosimTrigger", 290, cosimTrigSignals, nil, checkTrigTicks))
	board("proto", "SPI and I2C protocol triggers on generated buses", "CosimProto", 200, cosimProtoSignals)
	board("readout", "readout windows, nth/minmax reductions vs software, decimation, segments, counter", "CosimReadout", 290, cosimSine)
	board("stream", "gap-free codes stream; injected HP1 stall drops whole blocks, counted", "CosimStream", 290, cosimSine)
	board("stacker", "stacker bins vs the software binning of the same segments", "CosimStacker", 290, cosimSine)
	for _, a := range areas {
		if a.name == "scpi" {
			registerCosim(a)
		}
	}
}

// cosimArgs are the simulator flags of a co-simulation run.
func cosimArgs(e *env, s harness.Spec) string {
	log := filepath.Join(harness.Workspace(), "analysis", "testenv", "runs", s.Name, "cosim.jsonl")
	return strings.TrimSpace(s.SimdArgs + " -pl open -cosim " + e.cosim + " -cosim-log " + log)
}

// fSine is C1's frequency (the guest's constant of the same name).
const fSine = 1.000123e6

var reTick = regexp.MustCompile(`^TRIGTICK (\S+) (\d+) (\d+)$`)

// checkTrigTicks places the edge triggers against the analytic signal: the
// co-sim's log gives each accepted trigger's absolute tick; C1 is a 1 MHz
// sine (fSine) through 0 V rising at whole periods, falling half way. The crossing the image reports is the trigger sample − 1 + frac/16.
func checkTrigTicks(_ *env, r *harness.Run) []harness.Result {
	var out []harness.Result
	abs := map[string]uint64{}
	raw, err := os.ReadFile(filepath.Join(r.Dir, "cosim.jsonl"))
	if err != nil {
		return []harness.Result{harness.Fail("trigger", "analytic", "co-sim log: %v", err)}
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		var ev struct {
			Ev      string `json:"ev"`
			T, Tick uint64
		}
		if json.Unmarshal([]byte(line), &ev) == nil && ev.Ev == "trig" {
			abs[strconv.FormatUint(ev.T, 10)] = ev.Tick
		}
	}
	for line := range strings.SplitSeq(r.Console, "\n") {
		m := reTick.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		var frac float64
		_, _ = fmt.Sscan(m[3], &frac)
		tick, ok := abs[m[2]]
		if !ok {
			out = append(out, harness.Fail("trigger", m[1]+"-analytic", "no co-sim trigger event at t %s", m[2]))
			continue
		}
		cross := float64(tick) - 1 + frac/16
		per := 2e9 / fSine
		phase := 0.0
		if strings.HasSuffix(m[1], "falling") {
			phase = per / 2
		}
		d := cross - phase
		d -= per * math.Floor(d/per+0.5)
		v := fmt.Sprintf("crossing vs the analytic sine: expected 0 measured %+.3f ticks (tick %d, frac %v/16)", d, tick, frac)
		if d > -2 && d < 2 {
			out = append(out, harness.Pass("trigger", m[1]+"-analytic", v))
		} else {
			out = append(out, harness.Fail("trigger", m[1]+"-analytic", "%s", v))
		}
	}
	if len(out) == 0 {
		out = append(out, harness.Fail("trigger", "analytic", "no TRIGTICK lines"))
	}
	return out
}

var reRow = regexp.MustCompile(`^(.*): expected (.*) measured (.*)$`)

// renderCosim is the per-area table of expected and measured values.
func renderCosim(results []harness.Result, names []string) string {
	var b strings.Builder
	for _, s := range harness.Summarise(results, names) {
		v := verdict(s.Fail == 0 && s.Pass > 0)
		fmt.Fprintf(&b, "\n== %s: %s (%d pass, %d fail)\n", s.Area, v, s.Pass, s.Fail)
		tw := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "CASE\tCHECK\tEXPECTED\tMEASURED\tVERDICT")
		for _, r := range results {
			if r.Area != s.Area {
				continue
			}
			v := verdict(r.Pass)
			what, want, got := r.Detail, "", ""
			if m := reRow.FindStringSubmatch(r.Detail); m != nil {
				what, want, got = m[1], m[2], m[3]
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Case, what, want, got, v)
		}
		_ = tw.Flush()
	}
	return b.String()
}

// passWord is a passed check's word, in the guest's RESULT lines and the reports.
const passWord = "PASS"

// verdict is a check's word in the tables.
func verdict(pass bool) string {
	if pass {
		return passWord
	}
	return "FAIL"
}
