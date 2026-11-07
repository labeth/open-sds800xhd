package main

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/labeth/open-sds800xhd/sim/harness"
)

// The start-up area compares what the app's start-up (spec 03 §2) leaves in
// the ACQ and MAP planes and on the front-end SPI chips with what the reference
// firmware leaves after its own start-up in the reference run port-vendor,
// with the simulator's trace diff (final values, SPI frames per chip).
// Write order is not compared: the reference window holds the whole reference
// run, which repeats its writes.
//
// The differences the specs explain are allowed, each with its reason; any
// other difference fails.
var startupAllowed = map[string]string{
	"0x0002e": "the per-frame switch-index toggle: the reference sits inside a running frame cycle",
	"0x000b1": "channel enables: the replacement starts with CH1 on (spec 09 §2.2), the reference firmware's default setup has none",
	"0x00106": "map channel ranks follow the enabled channels",
	"0x00132": "map channel ranks follow the enabled channels",
	"0x00133": "map channel ranks follow the enabled channels",
	"0x00134": "map channel ranks follow the enabled channels",
	"0x00170": "map restart word: the reference's last clear block is part of its later display activity",
	"0x001ff": "FIR selector of the rate route, which follows the channel set",
	"0x00147": "offset DAC of a channel that is off: the reference writes the calibrated mid code",
	"0x00148": "offset DAC of a channel that is off: the reference writes the calibrated mid code",
	"0x00149": "offset DAC of a channel that is off: the reference writes the calibrated mid code",
	"adc0":    "ADC mode word of the channel set",
	"adc1":    "ADC mode word of the channel set",
	"hc595":   "relay image of the enabled channels",
}

func init() {
	register(area{
		name: "startup", title: "write parity: start-up registers and SPI vs the reference",
		runs: func(e *env) []harness.Spec {
			return []harness.Spec{{
				Name: e.prefix + "startup", Secs: 20, SimdArgs: signals,
				App: e.app, Hook: e.hook, GuestBoot: e.guestBoot,
			}}
		},
		check: func(_ *env, runs []*harness.Run) []harness.Result {
			ws := harness.Workspace()
			cmd := exec.CommandContext(context.Background(), filepath.Join(ws, "analysis", "testenv", "bin", "simdiff"), //nolint:gosec // G204: the workspace's own simdiff tool
				"-s", "final,spi", "-blk", "acq,map", "-max", "200",
				"port-vendor@mark=app start", runs[0].Name+"@mark=app start..app: startup done")
			out, _ := cmd.CombinedOutput()
			return startupDiff(string(out))
		},
	})
}

// startupDiff checks simdiff's final-value and SPI sections.
func startupDiff(out string) []harness.Result {
	var res []harness.Result
	section := ""
	n := 0
	for l := range strings.SplitSeq(out, "\n") {
		switch {
		case strings.HasPrefix(l, "== final"):
			section = "final"
			continue
		case strings.HasPrefix(l, "== SPI"):
			section = "spi"
			continue
		case strings.HasPrefix(l, "=="):
			section = ""
			continue
		}
		f := strings.Fields(l)
		var key string
		switch {
		case section == "final" && len(f) >= 3 && strings.HasPrefix(f[0], "0x8"):
			key = f[1]
		case section == "spi" && len(f) >= 2 && f[0] == "!=":
			key = f[1]
		default:
			continue
		}
		n++
		if why, ok := startupAllowed[key]; ok {
			res = append(res, harness.Pass("startup", key, "differs as explained: "+why))
		} else {
			res = append(res, harness.Fail("startup", key, "differs from the reference: %s", strings.Join(f, " ")))
		}
	}
	if n == 0 && !strings.Contains(out, "no differences") && !strings.Contains(out, "difference(s)") {
		res = append(res, harness.Fail("startup", "simdiff", "no comparison: %s", firstLine(out)))
	}
	res = append(res, harness.Pass("startup", "compared", fmt.Sprintf("%d differences, all explained or listed above", n)))
	return res
}

func firstLine(s string) string { l, _, _ := strings.Cut(s, "\n"); return l }
