package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/labeth/open-sds800xhd/sim/harness"
)

// openInputs is every input at 0 V: the self-calibration's open inputs.
const openInputs = "-signals C1=dc,0,0,0;C2=dc,0,0,0;C3=dc,0,0,0;C4=dc,0,0,0"

func init() {
	sc := boardArea("selfcal", "calibration: self-cal end to end, file on p12", "SelfCal", 300, openInputs, nil, checkSelfCalFile)
	register(sc)
}

// calLine is one key="values" line of the self-cal file.
var calLine = regexp.MustCompile(`^(\w+)="([^"]*)"$`)

// checkSelfCalFile checks the file the guest's self-calibration left on p12
// against the device model (spec 16 §5.4: open inputs give the 0 V DAC code
// 0x8012 as every centre) and counts the mean-engine starts (ACQ 0x1AE
// written 1) in the log.
func checkSelfCalFile(e *env, r *harness.Run) []harness.Result {
	const area = "selfcal"
	var res []harness.Result
	starts := 0
	for _, ev := range r.Log.Events {
		if ev.Ev == "mmio" && ev.Op == "w" && ev.Address() == harness.BaseACQ+4*0x1AE && ev.Value() == 1 {
			starts++
		}
	}
	if starts > 1000 {
		res = append(res, harness.Pass(area, "mean engine", fmt.Sprintf("%d mean-engine starts", starts)))
	} else {
		res = append(res, harness.Fail(area, "mean engine", "%d mean-engine starts, want more than 1000", starts))
	}
	raw, err := os.ReadFile(r.File("usr/usr/acq_self_cal_usr.txt"))
	if err != nil {
		return append(res, harness.Fail(area, "file", "%v", err))
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if lines[0] != "VER:4" || len(lines) != 1+4*191 {
		res = append(res, harness.Fail(area, "layout", "VER %q, %d lines, want 1 + 4·191", lines[0], len(lines)))
	} else {
		res = append(res, harness.Pass(area, "layout", "VER:4 and 4 × 191 lines"))
	}
	centres, off, fs := 0, 0, 0
	for _, l := range lines[1:] {
		m := calLine.FindStringSubmatch(strings.TrimSpace(l))
		if m == nil {
			return append(res, harness.Fail(area, "layout", "line %q", l))
		}
		var v []int
		for f := range strings.SplitSeq(m[2], ",") {
			n, err := strconv.Atoi(f)
			if err != nil {
				return append(res, harness.Fail(area, "layout", "line %q: %v", l, err))
			}
			v = append(v, n)
		}
		if strings.HasPrefix(m[1], "CH") {
			fs++
			mid := v[len(v)/2]
			if mid < 980_000 || mid > 1_020_000 {
				res = append(res, harness.Fail(area, "full scale", "%s middle %d, want 1.0 ±2 %%", m[1], mid))
			}
			continue
		}
		if len(v) != 6 || v[2] != 0 || v[3] != 0 {
			return append(res, harness.Fail(area, "layout", "%s: want g0,c0,0,0,g2,c2", m[1]))
		}
		for _, i := range []int{1, 5} {
			centres++
			if d := v[i] - 0x8012; d < -2 || d > 2 {
				off++
			}
		}
	}
	if off == 0 && centres == 4*189*2 {
		res = append(res, harness.Pass(area, "centres", fmt.Sprintf("%d centre codes at 0x8012 ±2", centres)))
	} else {
		res = append(res, harness.Fail(area, "centres", "%d centre codes, %d off 0x8012 ±2", centres, off))
	}
	if fs != 8 {
		res = append(res, harness.Fail(area, "full scale", "%d full-scale lines, want 8", fs))
	}
	return append(res, compareVendorCal(area, e.prefix, raw))
}

// parseCal maps every key of a self-cal file to its values.
func parseCal(raw []byte) map[string][]float64 {
	out := map[string][]float64{}
	for l := range strings.SplitSeq(string(raw), "\n") {
		m := calLine.FindStringSubmatch(strings.TrimSpace(l))
		if m == nil {
			continue
		}
		var v []float64
		for f := range strings.SplitSeq(m[2], ",") {
			n, _ := strconv.ParseFloat(f, 64)
			v = append(v, n)
		}
		out[m[1]] = v
	}
	return out
}

// compareVendorCal compares the file with a self-cal file the vendor
// application wrote in an earlier simulator run of the workspace with the
// same open inputs: every value within 0.2 % (the mean engine has no noise,
// so both searches land on the same line).
func compareVendorCal(area, prefix string, raw []byte) harness.Result {
	const c = "vendor file"
	usr := vendorSelfCalFile(prefix)
	if usr == "" {
		return harness.Fail(area, c, "no vendor self-cal file in the workspace's runs")
	}
	vb, err := os.ReadFile(usr) //nolint:gosec // G304: a run's file in the simulator workspace
	if err != nil {
		return harness.Fail(area, c, "%v", err)
	}
	ours, theirs := parseCal(raw), parseCal(vb)
	if len(ours) != len(theirs) {
		return harness.Fail(area, c, "%d keys, the vendor file %s has %d", len(ours), usr, len(theirs))
	}
	worst, at := 0.0, ""
	for k, v := range theirs {
		o, ok := ours[k]
		if !ok || len(o) != len(v) {
			return harness.Fail(area, c, "key %s missing or of another length", k)
		}
		for i := range v {
			d := math.Abs(o[i] - v[i])
			if v[i] != 0 {
				d /= math.Abs(v[i])
			}
			if d > worst {
				worst, at = d, fmt.Sprintf("%s[%d] %g vs %g", k, i, o[i], v[i])
			}
		}
	}
	if worst > 0.002 {
		return harness.Fail(area, c, "worst relative difference %.4f at %s", worst, at)
	}
	return harness.Pass(area, c, fmt.Sprintf("%d keys within 0.2 %% of %s (worst %.5f)", len(theirs), usr, worst))
}

// vendorSelfCalFile is a self-cal file the vendor application wrote in a
// self-calibration run of the workspace (a run named for it, not one of
// ours or of a replacement), preferring runs named vendor.
func vendorSelfCalFile(prefix string) string {
	m, _ := filepath.Glob(filepath.Join(harness.Workspace(), "analysis", "testenv", "runs", "*selfcal*", "usr", "usr", "acq_self_cal_usr.txt"))
	sort.Strings(m)
	best := ""
	for _, p := range m {
		run := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(p))))
		if strings.HasPrefix(run, prefix) || strings.Contains(run, "repl") {
			continue
		}
		if strings.Contains(run, "vendor") {
			return p
		}
		if best == "" {
			best = p
		}
	}
	return best
}
