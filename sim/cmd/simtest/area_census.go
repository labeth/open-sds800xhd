package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/labeth/open-sds800xhd/sim/harness"
)

// The census area: the census binary on its own on the board variants the
// device model offers, which must agree with the straps and must not write
// a PL register or read one with a read side effect. The census belongs to
// the supervisor side (hal.CensusBoard), so the in-app board test has none;
// the supervisor board runs the same package.
func init() {
	variants := []struct {
		hwid       string
		channels   int
		adc, clock string
	}{
		{"0x80", 2, "ADC12D1000", "original"},
		{"0x30", 4, "ADC12DL1500", "new"},
		{"0xB0", 2, "ADC12DL1500", "new"},
	}
	register(area{
		name: "census", title: "diag stick: read-only census",
		runs: func(e *env) []harness.Spec {
			var specs []harness.Spec
			for _, v := range variants {
				specs = append(specs, harness.Spec{
					Name: e.prefix + "census-" + v.hwid, Secs: 60, SimdArgs: "-hwid " + v.hwid,
					Guest: filepath.Join(e.root, "sim", "guest", "census.sh"),
					Files: map[string]string{"census": filepath.Join(e.root, "build", "census")},
				})
			}
			return specs
		},
		check: func(_ *env, runs []*harness.Run) []harness.Result {
			var res []harness.Result
			for i, v := range variants {
				res = append(res, checkCensusRun(runs[i], v.hwid, v.channels, v.adc, v.clock)...)
			}
			return res
		},
	})
}

type censusReport struct {
	Identity struct {
		HWID      int    `json:"hwid"`
		Channels  int    `json:"channels"`
		ADCPart   string `json:"adc_part"`
		ClockChip string `json:"clock_chip"`
		Model     string `json:"model"`
	} `json:"identity"`
	Verdicts []struct{ Part, Verdict, Detail string } `json:"verdicts"`
}

func checkCensusRun(r *harness.Run, hwid string, channels int, adc, clock string) []harness.Result {
	c := "hwid " + hwid
	lines := r.Lines("CENSUS ")
	if len(lines) == 0 {
		return []harness.Result{harness.Fail("census", c, "the census printed no report")}
	}
	var rep censusReport
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &rep); err != nil {
		return []harness.Result{harness.Fail("census", c, "report: %v", err)}
	}
	var res []harness.Result
	id := rep.Identity
	if fmt.Sprintf("%#x", id.HWID) == strings.ToLower(hwid) && id.Channels == channels && id.ADCPart == adc && id.ClockChip == clock {
		res = append(res, harness.Pass("census", c+" variant",
			fmt.Sprintf("%d channels, %s, %s clock chip, model %s", id.Channels, id.ADCPart, id.ClockChip, id.Model)))
	} else {
		res = append(res, harness.Fail("census", c+" variant", "got hwid %#x %d channels %s %s, want %d %s %s",
			id.HWID, id.Channels, id.ADCPart, id.ClockChip, channels, adc, clock))
	}
	verdict := map[string]string{}
	for _, v := range rep.Verdicts {
		verdict[v.Part] = v.Verdict
	}
	for _, p := range []string{"hw-id", "pl"} {
		if verdict[p] == "present" {
			res = append(res, harness.Pass("census", c+" "+p, "present"))
		} else {
			res = append(res, harness.Fail("census", c+" "+p, "verdict %q", verdict[p]))
		}
	}
	// Read-only: no plane register written, no side-effect register read.
	w := r.Log.Window("census start", "census exit")
	if w == nil {
		return append(res, harness.Fail("census", c+" read-only", "no census window in the log"))
	}
	writes, sideReads := 0, 0
	for i := range w {
		ev := &w[i]
		if ev.Ev != "mmio" {
			continue
		}
		reg, ok := harness.RegOf(ev)
		if ev.Op == "w" && ok {
			writes++
		}
		if ev.Op == "r" && ok && (reg == harness.Reg{Plane: "acq", Index: 0x160} || reg == harness.Reg{Plane: "map", Index: 0x16E}) {
			sideReads++
		}
	}
	if writes == 0 && sideReads == 0 {
		res = append(res, harness.Pass("census", c+" read-only", "no plane register written or side-effect register read"))
	} else {
		res = append(res, harness.Fail("census", c+" read-only", "%d plane writes, %d side-effect reads", writes, sideReads))
	}
	return res
}
