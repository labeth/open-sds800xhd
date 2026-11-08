package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/labeth/open-sds800xhd/sim/harness"
)

// The mask area runs the pass/fail test through the app: the factory PL image's
// pass/fail registers carry no comparison this firmware programs, so the
// test is the core's software mask on every frame the owner reads. The
// host makes a mask from the injected square over SCPI, runs it and sees
// it pass; changes the signal through the simulator and sees it fail and,
// with stop on fail, stop the run; and reads the same counts from the web.
func init() {
	register(appHostArea("mask", "mask test: create, pass, fail, stop on fail; SCPI and the web agree", 150, signals, maskHostFile, maskHost))
}

// appHostArea is an area of one app run on the simulator flags simd that
// the host drives with host, whose checks land in file.
func appHostArea(name, title string, secs int, simd, file string, host func(ctx context.Context, dir string)) area {
	return area{
		name: name, title: title,
		runs: func(e *env) []harness.Spec {
			return []harness.Spec{{
				Name: e.prefix + name, Secs: secs, Ports: true, SimdArgs: simd,
				App: e.app, Hook: e.hook, GuestBoot: e.guestBoot, Host: host,
			}}
		},
		check: func(_ *env, runs []*harness.Run) []harness.Result { return hostResultsOf(runs[0], name, file) },
	}
}

// maskHostFile is where the host's checks of the mask run land.
const maskHostFile = "mask-host.txt"

// maskCounts parses :MTESt:COUNt?'s FAIL,f,PASS,p,TOTAL,t.
func maskCounts(s string) (fail, pass int, ok bool) {
	f := strings.Split(s, ",")
	if len(f) != 6 || f[0] != failWord || f[4] != "TOTAL" {
		return 0, 0, false
	}
	a, e1 := strconv.Atoi(f[1])
	b, e2 := strconv.Atoi(f[3])
	return a, b, e1 == nil && e2 == nil
}

// maskHost drives the mask test from the host.
func maskHost(ctx context.Context, dir string) {
	f, err := os.Create(filepath.Join(dir, maskHostFile)) //nolint:gosec // G304: the run's own directory
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	u := &uiRig{ctx: ctx, dir: dir, h: hostLog{f}, cl: &http.Client{Timeout: 10 * time.Second}}
	h := u.h
	if !h.check("serving", u.until(func() bool { return u.status()["run"] != nil }), "the app's web server answered") {
		return
	}
	// Every line is answered (a command with *OPC?): separate connections
	// are separate sessions, which the app may serve out of order.
	scpi := func(line string) string {
		if !strings.Contains(line, "?") {
			line += ";*OPC?"
		}
		r, err := scpiQuery(ctx, line)
		if err != nil {
			return "err " + err.Error()
		}
		return r
	}
	// The ±1 V 5 MHz square on C1 at 200 ns/div, triggered on its rising
	// edge: every frame shows the same ten periods.
	scpi(":TIM:SCAL 2E-7;:TRIG:MODE NORM;:TRIG:EDGE:SOUR C1;SLOP RIS;LEV 0")
	time.Sleep(3 * time.Second)
	scpi(":MTES:SOUR C1;:MTES:MASK:CRE 0.08,0.3;:MTES ON;:MTES:OPER ON")
	q := scpi(":SYST:ERR?")
	h.check("create", strings.HasPrefix(q, "0,"), "mask created from the waveform: %q", q)
	time.Sleep(4 * time.Second)
	c := scpi(":MTES:COUN?")
	fail, pass, ok := maskCounts(c)
	h.check("pass", ok && pass > 0 && fail == 0, "the waveform the mask was made from passes: %q", c)
	st := u.status()
	h.check("web-pass", st["mask_mode"] == 1.0 && st["mask_set"] == true && st["mask_pass"] != nil && st["mask_fail"] == 0.0,
		"the web reads mode %v, set %v, pass %v, fail %v", st["mask_mode"], st["mask_set"], st["mask_pass"], st["mask_fail"])

	// Half the amplitude leaves the mask; stop on fail stops the run.
	scpi(":MTES:FUNC:SOF ON")
	h.check("signals", u.ctl("signals C1=square,5e6,1,0") == "ok", "C1 to a ±0.5 V square")
	stat := ""
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline) && stat != "Stop"; time.Sleep(250 * time.Millisecond) {
		stat = scpi(":TRIG:STAT?")
	}
	h.check("stop-on-fail", stat == "Stop", "the run after the failure: %q", stat)
	c = scpi(":MTES:COUN?")
	fail, _, ok = maskCounts(c)
	h.check("fail", ok && fail > 0, "the counts: %q", c)
	st = u.status()
	h.check("web-fail", st["mask_stopped"] == true && fmt.Sprint(st["mask_fail"]) != "0", "the web reads stopped %v, fail %v", st["mask_stopped"], st["mask_fail"])
	// The web clears the counts; SCPI reads them cleared.
	out := u.set("/api/set", `{"control":"maskclear","value":1}`)
	c = scpi(":MTES:COUN?")
	h.check("web-clear", out["ok"] == true && c == "FAIL,0,PASS,0,TOTAL,0", "maskclear from the page: %v; SCPI reads %q", out["ok"], c)
	scpi(":MTES:TYPE ANY_OUT")
	q = scpi(":SYST:ERR?")
	h.check("refuse-type", strings.HasPrefix(q, "-221") && strings.Contains(q, "ALL_IN"), "another pass criterion: %q", q)
}
