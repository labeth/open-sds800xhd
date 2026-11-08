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

// The acquisition and input fault areas run the open firmware as the boot
// starts it, watched by otactl's health check in a loop (sim/guest/
// l4-super.sh, the supervisor the scope would run), and inject the device
// model's faults while the host checks the app over its ports.
func init() {
	l4register(area{
		name: "l4-acq", title: "faults: DMA timeout and error, lost interrupt, clock lock loss, PL reload",
		runs: func(e *env) []harness.Spec {
			return []harness.Spec{{
				Name: e.prefix + "acq", Secs: 290, Ports: true, SimdArgs: signals,
				Guest: l4Guest(e, "l4-super.sh"), Files: l4Files(e),
				Host: func(ctx context.Context, dir string) { l4AcqHost(ctx, dir) },
			}}
		},
		check: func(_ *env, runs []*harness.Run) []harness.Result {
			r := runs[0]
			res := hostResultsOf(r, "l4-acq", l4AcqFile)
			res = append(res,
				logHas("l4-acq", "dma-timeout/reported", r, `(?i)DMA block of \d+ bytes timed out`, "the timed-out transfer"),
				logHas("l4-acq", "dma-error/reported", r, `(?i)DMA[^\n]*(error|SlvErr|DecErr|status 0x[0-9a-f]*[4-7]0\b)`, "the errored transfer"),
				logHas("l4-acq", "pll-unlock/reported", r, `(?i)(ADC not working|xPLL not locked|PLL[^\n]*lock)`, "the lost clock lock"),
				logHas("l4-acq", "pl-reload/reported", r, `(?i)(PL|FPGA|bitstream|image)[^\n]*(reload|reconfigur|lost|reset)`, "the reconfigured PL"),
				takeovers("l4-acq", "dma-timeout/no-restart", r, 1),
			)
			res = append(res, plReinit(r))
			return append(res, guardChecks("l4-acq", r)...)
		},
	})
	l4register(area{
		name: "l4-input", title: "faults: stuck, chattering and spinning input; bus error once and every start",
		runs: func(e *env) []harness.Spec {
			return []harness.Spec{{
				Name: e.prefix + "input", Secs: 290, Ports: true, SimdArgs: signals,
				Guest: l4Guest(e, "l4-super.sh"), Files: l4Files(e),
				Host: func(ctx context.Context, dir string) { l4InputHost(ctx, dir) },
			}}
		},
		check: func(_ *env, runs []*harness.Run) []harness.Result {
			r := runs[0]
			res := hostResultsOf(r, "l4-input", l4InputFile)
			res = append(res, droppedKeys(r))
			return append(res, guardChecks("l4-input", r)...)
		},
	})
}

const (
	l4AcqFile   = "l4-acq-host.txt"
	l4InputFile = "l4-input-host.txt"
)

// l4Rig opens a run's host check file and waits for the app to serve.
func l4Rig(ctx context.Context, dir, file string) (*uiRig, func(), bool) {
	f, err := os.Create(filepath.Join(dir, file)) //nolint:gosec // G304: the run's own directory
	if err != nil {
		return nil, func() {}, false
	}
	u := &uiRig{ctx: ctx, dir: dir, h: hostLog{f}, cl: &http.Client{Timeout: 10 * time.Second}}
	done := func() { _ = f.Close() }
	up := false
	for end := time.Now().Add(90 * time.Second); !up && time.Now().Before(end) && ctx.Err() == nil; time.Sleep(time.Second) {
		up = len(u.status()) > 0
	}
	if !u.h.check("serving", up, "the app's web server answered") {
		return u, done, false
	}
	ok, n := u.flowing(30 * time.Second)
	u.h.check("baseline", ok, "frames flow before any fault (+%.0f)", n)
	return u, done, true
}

// phase injects a fault for hold (the knobs in set, then clear), checks the
// app answers while it is on and that frames flow again after it, and
// reports how often it fired.
func (u *uiRig) phase(name string, set []string, hold time.Duration, kind string, kinds ...string) {
	h := u.h
	u.ctl("mark l4: " + name + " on")
	for _, s := range set {
		if r := u.ctl("fault " + s); r != "ok" {
			h.check(name+"/knob", false, "fault %s: %s", s, r)
		}
	}
	time.Sleep(hold / 2)
	ok, took := u.answers()
	h.check(name+"/serving", ok, "web status and *IDN? answered while the fault was on (%v)", took.Round(time.Millisecond))
	time.Sleep(hold / 2)
	counts := faultCounts(u.ctl("fault"))
	u.ctl("fault clear " + kind)
	u.ctl("mark l4: " + name + " off")
	n := 0
	for _, k := range kinds {
		n += counts[k]
	}
	h.check(name+"/fired", n > 0, "the fault fired %d times (%v)", n, kinds)
	ok, more := u.flowing(40 * time.Second)
	h.check(name+"/recovered", ok, "frames flow again after the fault (+%.0f)", more)
}

// l4AcqHost injects the acquisition faults one after the other.
func l4AcqHost(ctx context.Context, dir string) {
	u, done, ok := l4Rig(ctx, dir, l4AcqFile)
	defer done()
	if !ok {
		return
	}
	u.phase("dma-timeout", []string{"dma timeout"}, 10*time.Second, "dma", "dma timeout")
	u.phase("dma-error", []string{"dma error slv 4"}, 8*time.Second, "dma", "dma error")
	u.phase("irq-lost", []string{"irq dmadata_irq 3"}, 12*time.Second, "irq", "irq dmadata_irq")

	// The lock check runs on an ADC mode change (spec 06 §9.2): a channel
	// switched on and off again while the clock is unlocked.
	u.ctl("mark l4: pll-unlock on")
	u.ctl("fault pll unlock")
	_, _ = scpiQuery(ctx, "C2:TRA ON")
	time.Sleep(5 * time.Second)
	_, _ = scpiQuery(ctx, "C2:TRA OFF")
	time.Sleep(5 * time.Second)
	ok, took := u.answers()
	u.h.check("pll-unlock/serving", ok, "web status and *IDN? answered while unlocked (%v)", took.Round(time.Millisecond))
	counts := faultCounts(u.ctl("fault"))
	u.ctl("fault pll lock")
	u.ctl("fault clear")
	u.ctl("mark l4: pll-unlock off")
	u.h.check("pll-unlock/fired", counts["bits"] > 0, "the app read the cleared lock bits %d times", counts["bits"])
	ok, more := u.flowing(40 * time.Second)
	u.h.check("pll-unlock/recovered", ok, "frames flow with the lock back (+%.0f)", more)

	// A PL reload under the running firmware: the registers go back to
	// their power-up values.
	u.ctl("mark l4: pl-reload on")
	r := u.ctl("fault plreload")
	u.h.check("pl-reload/fired", r == "ok", "fault plreload: %s", r)
	time.Sleep(10 * time.Second)
	ok, took = u.answers()
	u.h.check("pl-reload/serving", ok, "web status and *IDN? answered after the reload (%v)", took.Round(time.Millisecond))
	ok, more = u.flowing(40 * time.Second)
	u.h.check("pl-reload/frames", ok, "frames flow after the reload (+%.0f)", more)
	u.ctl("mark l4: pl-reload off")
}

// keyRunStop and keyCH1 are matrix positions of spec 08 §2.2; knobVScale is
// the vertical scale knob.
const (
	keyRunStop = "5 5"
	knobVScale = "0 0"
)

// l4InputHost injects the input faults, then the bus errors.
func l4InputHost(ctx context.Context, dir string) {
	u, done, ok := l4Rig(ctx, dir, l4InputFile)
	defer done()
	if !ok {
		return
	}
	h := u.h
	run0 := running(u.status())

	// A stuck Run/Stop: its press acts once, then it stays down.
	u.ctl("mark l4: key-stuck on")
	u.ctl("fault key stuck " + keyRunStop)
	time.Sleep(6 * time.Second)
	ok, took := u.answers()
	h.check("key-stuck/serving", ok, "web status and *IDN? answered with Run/Stop held (%v)", took.Round(time.Millisecond))
	run1 := running(u.status())
	h.check("key-stuck/acts-once", run1 != run0, "the held key's press toggled the run state once (%v → %v)", run0, run1)
	u.ctl("fault key release")
	u.ctl("fault clear key")
	u.ctl("mark l4: key-stuck off")
	time.Sleep(2 * time.Second)
	u.ctl("key " + keyRunStop)
	h.check("key-stuck/after-release", u.until(func() bool { return running(u.status()) != run1 }), "a Run/Stop press after the release acts (running %v)", running(u.status()))
	if !running(u.status()) {
		u.ctl("key " + keyRunStop)
		u.until(func() bool { return running(u.status()) })
	}

	// A bouncing Run/Stop: 15 press/release pairs 40 ms apart.
	u.ctl("mark l4: key-chatter on")
	u.ctl("fault key chatter " + keyRunStop + " 15 40")
	time.Sleep(8 * time.Second)
	ok, took = u.answers()
	h.check("key-chatter/serving", ok, "web status and *IDN? answered through the bouncing key (%v)", took.Round(time.Millisecond))
	counts := faultCounts(u.ctl("fault"))
	h.check("key-chatter/fired", counts["key chatter"] > 0, "%d press/release pairs delivered", counts["key chatter"])
	u.ctl("fault clear key")
	u.ctl("mark l4: key-chatter off")
	if !running(u.status()) {
		u.ctl("key " + keyRunStop)
	}
	h.check("key-chatter/recovered", u.until(func() bool { return running(u.status()) }), "Run/Stop still runs the scope after the bounce")

	// The vertical scale knob turning on its own: 40 steps 50 ms apart.
	v0, _ := scpiQuery(ctx, ":CHAN1:SCAL?")
	u.ctl("mark l4: knob-spin on")
	u.ctl("fault knob spin " + knobVScale + " 1 40 50")
	time.Sleep(8 * time.Second)
	ok, took = u.answers()
	h.check("knob-spin/serving", ok, "web status and *IDN? answered with the knob spinning (%v)", took.Round(time.Millisecond))
	u.ctl("fault clear key")
	u.ctl("mark l4: knob-spin off")
	v1, err := scpiQuery(ctx, ":CHAN1:SCAL?")
	f1, perr := strconv.ParseFloat(v1, 64)
	h.check("knob-spin/bounded", err == nil && perr == nil && f1 >= 0.5e-3 && f1 <= 10, "C1 V/div %s → %s, inside the ladder", v0, v1)
	_, _ = scpiQuery(ctx, ":CHAN1:SCAL "+v0)
	ok, _ = u.flowing(30 * time.Second)
	h.check("knob-spin/recovered", ok, "frames flow after the spin")

	// One bus error on a PL register access ends the app (a synchronous
	// external abort); the supervisor relaunches it.
	u.ctl("mark l4: bus-error on")
	r := u.ctl("fault buserr 0x80030000-0x8004ffff exe=/open/ n=1")
	h.check("bus-error/knob", r == "ok", "fault buserr: %s", r)
	gone := u.until(func() bool { return len(u.status()) == 0 })
	h.check("bus-error/fired", gone && faultCounts(u.ctl("fault"))["buserr"] == 1, "the app stopped answering at the bus error")
	back := false
	for end := time.Now().Add(60 * time.Second); !back && time.Now().Before(end) && ctx.Err() == nil; time.Sleep(time.Second) {
		back = len(u.status()) > 0
	}
	h.check("bus-error/relaunched", back, "the app answered again after the supervisor's relaunch")
	ok, _ = u.flowing(30 * time.Second)
	h.check("bus-error/frames", ok, "frames flow after the relaunch")
	u.ctl("mark l4: bus-error off")

	// A bus error on every start: how many starts in 45 s.
	u.ctl("mark l4: bus-error-loop on")
	u.ctl("fault buserr 0x80030000-0x8004ffff exe=/open/ n=0")
	time.Sleep(45 * time.Second)
	n := faultCounts(u.ctl("fault"))["buserr"] - 1
	u.ctl("fault clear")
	u.ctl("mark l4: bus-error-loop off")
	h.check("bus-error-loop/bounded", n <= 3, "%d starts ended by the bus error in 45 s (the supervisor's bound: %d)", n, 3)
	back = false
	for end := time.Now().Add(60 * time.Second); !back && time.Now().Before(end) && ctx.Err() == nil; time.Sleep(time.Second) {
		back = len(u.status()) > 0
	}
	h.check("bus-error-loop/recovers", back, "the app serves again once the fault is gone")
}

// takeovers checks the app took the unit over exactly want times before the
// first bus error (a restart would be a second takeover).
func takeovers(area, c string, r *harness.Run, want int) harness.Result {
	n := 0
	for _, m := range r.Log.Marks("") {
		if strings.HasPrefix(m, "l4: bus-error") {
			break
		}
		if m == "app: takeover" {
			n++
		}
	}
	if n == want {
		return harness.Pass(area, c, fmt.Sprintf("%d takeover(s) through the acquisition faults: no restart", n))
	}
	return harness.Fail(area, c, "%d takeovers through the acquisition faults, want %d", n, want)
}

// plReinit checks whether the app set its PL up again after the reload: a
// write of the start-up's register init (ACQ 0x0F4, spec 03 §2) after the
// reload mark.
func plReinit(r *harness.Run) harness.Result {
	n := 0
	for _, e := range r.Log.Window("l4: pl-reload on", "l4: pl-reload off") {
		if e.Ev == evMMIO && e.Op == "w" && e.Address() == harness.BaseACQ+4*0x0F4 {
			n++
		}
	}
	if n > 0 {
		return harness.Pass("l4-acq", "pl-reload/setup-redone", fmt.Sprintf("the start-up's ACQ 0x0F4 written %d times after the reload", n))
	}
	return harness.Fail("l4-acq", "pl-reload/setup-redone", "no start-up register init after the reload: the app runs on with the PL at power-up values")
}

// droppedKeys reports the key records the driver dropped (no reader in
// read(2)), which a wedged input reader would cause.
func droppedKeys(r *harness.Run) harness.Result {
	keys, dropped := 0, 0
	for _, e := range r.Log.Events {
		if e.Ev != "key" {
			continue
		}
		keys++
		if strings.Contains(string(e.Val), "dropped") || strings.Contains(e.Res, "dropped") {
			dropped++
		}
	}
	if dropped == 0 {
		return harness.Pass("l4-input", "key-stuck/reader", fmt.Sprintf("%d key records, none dropped: the input reader never wedged", keys))
	}
	return harness.Fail("l4-input", "key-stuck/reader", "%d of %d key records dropped by the driver (no reader in read)", dropped, keys)
}
