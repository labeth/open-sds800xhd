package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/labeth/open-sds800xhd/sim/harness"
)

// feSignals are the inputs of the front-end area: 100 kHz squares of four
// amplitudes (the in-guest test's feSignals).
const feSignals = "-signals C1=square,1e5,0.002,0.0005;C2=square,1e5,0.02,0.005;C3=square,1e5,0.2,0.05;C4=square,1e5,2,0.5"

func init() {
	register(boardArea("frontend", "front end: vsweep, ladder × coupling × BW × probe, refusals", "FrontEnd", 300, feSignals, nil, refSPIParity))
	register(boardArea("counter", "counter/totalizer vs the injected frequency", "Counter", 90,
		"-signals C1=square,25e3,2,0", nil, nil))
	register(area{
		name: "calibration", title: "calibration: factory DAC and self-cal files recalled",
		runs: func(e *env) []harness.Spec {
			s := harness.Spec{
				Name: e.prefix + "calibration", Secs: 90, Board: e.boardtest,
				BoardArgs: boardArgs("^TestCalibration$"),
			}
			fac, usr := refCalFiles()
			s.Seed = map[string]string{}
			if fac != "" {
				s.Seed["firmdata0/acq_self_cal_factory_dac.txt"] = fac
			}
			if usr != "" {
				s.Seed["usr/usr/acq_self_cal_usr.txt"] = usr
			}
			return []harness.Spec{s}
		},
		check: func(_ *env, runs []*harness.Run) []harness.Result {
			fac, usr := refCalFiles()
			if fac == "" || usr == "" {
				return []harness.Result{harness.Fail("calibration", "seed", "no reference calibration files in the workspace's runs")}
			}
			r := runs[0]
			res := harness.GuestResults(r, "calibration")
			res = append(res, harness.Missing(r, res, "calibration")...)
			if !strings.Contains(r.Console, "--- PASS: TestCalibration") {
				res = append(res, harness.Fail("calibration", "go test", "TestCalibration did not pass in the guest"))
			}
			return res
		},
	})
	register(area{
		name: "storage", title: "restart rows: settings KV on p12 survive a restart",
		runs: func(e *env) []harness.Spec {
			name := e.prefix + "storage"
			return []harness.Spec{
				{Name: name, Secs: 60, Board: e.boardtest, BoardArgs: boardArgs("^TestStorage(Write)?$")},
				{Name: name, Secs: 60, Keep: true, Board: e.boardtest, BoardArgs: boardArgs("^TestStorageRead$")},
			}
		},
		check: func(_ *env, runs []*harness.Run) []harness.Result {
			var res []harness.Result
			for i, r := range runs {
				g := harness.GuestResults(r, "storage")
				res = append(res, g...)
				res = append(res, harness.Missing(r, g, "storage")...)
				if strings.Contains(r.Console, "--- FAIL") || !strings.Contains(r.Console, "--- PASS") {
					res = append(res, harness.Fail("storage", fmt.Sprintf("go test run %d", i+1), "a storage test failed in the guest"))
				}
			}
			if len(runs) == 2 {
				w, rd := runs[0].Lines("TOKEN written "), runs[1].Lines("TOKEN read ")
				switch {
				case len(w) == 0 || len(rd) == 0:
					res = append(res, harness.Fail("storage", "restart", "no token written or read"))
				case strings.TrimSpace(w[0]) != strings.TrimSpace(rd[0]):
					res = append(res, harness.Fail("storage", "restart", "read %q after the restart, wrote %q", rd[0], w[0]))
				default:
					res = append(res, harness.Pass("storage", "restart", "the second run read the first run's token"))
				}
			}
			return res
		},
	})
}

// refCalFiles finds a factory DAC file and a self-calibration file the
// reference firmware produced in earlier simulator runs of the workspace.
func refCalFiles() (factory, user string) {
	runs := filepath.Join(harness.Workspace(), "analysis", "testenv", "runs")
	first := func(pattern string) string {
		m, _ := filepath.Glob(filepath.Join(runs, pattern))
		sort.Strings(m)
		for _, p := range m {
			if strings.Contains(p, "vendor") {
				return p
			}
		}
		if len(m) > 0 {
			return m[0]
		}
		return ""
	}
	return first("*/firmdata0/acq_self_cal_factory_dac.txt"), first("*/usr/usr/acq_self_cal_usr.txt")
}

// refSPI are reference runs of the workspace that set C1's V/div by SCPI:
// the run, the mark of the command and the V/div.
// ours is the input the in-guest test sets that V/div on.
var refSPI = []struct {
	run, mark string
	vdiv      float64
	ours      int
}{
	{"b2scope-vendor1", "scpi :CHANnel1:SCALe 0.005", 0.005, 1},
	{"b2scope-vendor1", "scpi :CHANnel1:SCALe 0.5", 0.5, 3},
	{"b1acq-vendor", "scpi :CHANnel1:SCALe 0.2", 0.2, 2},
}

// refSPIParity compares the front-end frames of a channel set to a V/div
// with the reference firmware's for C1 at the same V/div: the VGA word, and
// the relay frame's control byte (without BW_FULL, which follows the number
// of channels on, spec 06 §8.2) and DAC-attenuator nibble of the channel.
func refSPIParity(_ *env, r *harness.Run) []harness.Result {
	var res []harness.Result
	last := func(fs []string) string {
		if len(fs) == 0 {
			return ""
		}
		return fs[len(fs)-1]
	}
	chByte := func(relay string, ch int) (ctrl, nib byte, ok bool) {
		var b [6]byte
		if _, err := fmt.Sscanf(relay, "%02x%02x%02x%02x%02x%02x", &b[0], &b[1], &b[2], &b[3], &b[4], &b[5]); err != nil {
			return 0, 0, false
		}
		n := b[1-ch/2]
		if ch%2 == 1 {
			n >>= 4
		}
		return b[5-ch] &^ 0x20, n & 0xF, true
	}
	for _, v := range refSPI {
		c := fmt.Sprintf("ref %gV", v.vdiv)
		vl, err := harness.ReadLog(filepath.Join(harness.Workspace(), "analysis", "testenv", "runs", v.run, "log.jsonl"))
		if err != nil {
			res = append(res, harness.Fail("frontend", c, "reference run %s: %v", v.run, err))
			continue
		}
		vs := harness.SPIWrites(vl.Window(v.mark, "scpi "))
		os := harness.SPIWrites(r.Log.Window(fmt.Sprintf("app: case frontend/%gV-dc", v.vdiv), "app: case "))
		vv, ov := last(vs["vga1"]), last(os[fmt.Sprintf("vga%d", v.ours+1)])
		vc, vn, ok1 := chByte(last(vs["hc595"]), 0)
		oc, on, ok2 := chByte(last(os["hc595"]), v.ours)
		switch {
		case vv == "" || ov == "" || !ok1 || !ok2:
			res = append(res, harness.Fail("frontend", c, "frames missing: reference %q %q, ours %q %q", vv, last(vs["hc595"]), ov, last(os["hc595"])))
		case vv != ov || vc != oc || vn != on:
			res = append(res, harness.Fail("frontend", c, "reference C1 VGA %s %#02x/%x, ours C%d %s %#02x/%x", vv, vc, vn, v.ours+1, ov, oc, on))
		default:
			res = append(res, harness.Pass("frontend", c, fmt.Sprintf("C%d: VGA %s, control %#02x, nibble %x as the reference's C1", v.ours+1, ov, oc, on)))
		}
	}
	return res
}
