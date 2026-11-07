package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/labeth/open-sds800xhd/sim/harness"
)

// The storage areas: p12 full, p12 read-only, and the write guard against a
// p12 with symlinks out of it and against vendor roots.
func init() {
	l4register(area{
		name: "l4-storage", title: "faults: full p12, read-only p12",
		runs: func(e *env) []harness.Spec {
			full := l4Spec(e, "storage-full", "l4-full.sh", 150, nil)
			full.Ports, full.P12Mode = true, "tmpfs:24m"
			full.Host = func(ctx context.Context, dir string) { storageHost(ctx, dir, "storage-full") }
			prep := l4Spec(e, "storage-ro", "l4-ro-prep.sh", 60, nil)
			ro := l4Spec(e, "storage-ro", "l4-ro.sh", 150, nil)
			ro.Ports, ro.Keep, ro.P12Mode = true, true, "ro"
			ro.Host = func(ctx context.Context, dir string) { storageHost(ctx, dir, "storage-ro") }
			return []harness.Spec{full, prep, ro}
		},
		check: func(_ *env, runs []*harness.Run) []harness.Result { return storageChecks(runs) },
	})
	l4register(area{
		name: "l4-guard", title: "faults: symlinks out of p12, vendor roots: the write guard",
		runs:  func(e *env) []harness.Spec { return []harness.Spec{l4Spec(e, "guard", "l4-guard.sh", 120, nil)} },
		check: func(_ *env, runs []*harness.Run) []harness.Result { return guardAreaChecks(runs[0]) },
	})
}

// storageHost checks the app serves and takes a setting on the damaged
// storage, then ends the run.
func storageHost(ctx context.Context, dir, c string) {
	u, done, ok := l4Rig(ctx, dir, c+"-host.txt")
	defer done()
	if ok {
		_, _ = scpiQuery(ctx, ":TIM:SCAL 2E-6")
		r, got := u.scpiUntil(":TIM:SCAL?", func(s string) bool { return strings.HasPrefix(s, "2.00E-06") })
		u.h.check(c+"/settings", got, "a timebase set over SCPI reads back %q", r)
		time.Sleep(8 * time.Second) // the settings saver's turn: it must fail quietly
		ok, took := u.answers()
		u.h.check(c+"/serving", ok, "web status and *IDN? answered after the saver ran (%v)", took.Round(time.Millisecond))
	}
	_ = os.WriteFile(filepath.Join(dir, "fw", "stop"), nil, 0o644) //nolint:gosec // G306: a marker the guest reads
}

func storageChecks(runs []*harness.Run) []harness.Result {
	const a = "l4-storage"
	if len(runs) != 3 {
		return []harness.Result{harness.Fail(a, "storage-full/runs", "%d runs, want 3", len(runs))}
	}
	full, ro := runs[0], runs[2]
	res := hostResultsOf(full, a, "storage-full-host.txt")
	res = append(res, hostResultsOf(ro, a, "storage-ro-host.txt")...)
	line := func(r *harness.Run, prefix string) string {
		if l := l4Lines(r, prefix); len(l) > 0 {
			return l[0]
		}
		return ""
	}
	check := func(c string, ok bool, l string) {
		if ok {
			res = append(res, harness.Pass(a, c, l))
		} else {
			res = append(res, harness.Fail(a, c, "%s", l))
		}
	}
	l := line(full, "storage-full filled:")
	check("storage-full/filled", strings.Contains(l, " 100% ") || strings.Contains(l, " 99% "), "the device is full: "+l)
	l = line(full, "storage-full boot")
	check("storage-full/boot", knownBoot.MatchString(l), l)
	l = line(full, "storage-full update")
	check("storage-full/update-fails-cleanly", !strings.HasPrefix(l, "rc 0") && strings.Contains(l, "current=a") &&
		!strings.Contains(l, "trial=") && strings.HasSuffix(l, "slot-a app") && !strings.Contains(l, ".new:"), l)
	l = line(full, "storage-full alive-at-end")
	check("storage-full/alive", strings.HasPrefix(l, "app "), l)

	if line(ro, "storage-ro p12 is writable") != "" || len(l4Lines(ro, "storage-ro")) == 0 {
		return append(res, harness.Fail(a, "storage-ro/mode", "p12 was not read-only (P12MODE needs the simulator's run script with it: SDS800XHD_SIMSRC)"))
	}
	for i, b := range []string{"boot1", "boot2", "boot3"} {
		l = line(ro, "storage-ro "+b)
		if i < 2 {
			check("storage-ro/"+b, knownBoot.MatchString(l), l)
			continue
		}
		// the third start of a slot on trial whose count cannot be written
		check("storage-ro/trial-bounded", strings.HasPrefix(l, "app from "), "the third boot with slot b on trial and p12 read-only: "+l)
	}
	l = line(ro, "storage-ro update")
	check("storage-ro/update-refused", !strings.HasPrefix(l, "rc 0"), l)
	l = line(ro, "storage-ro acquire-held")
	check("storage-ro/acquire-route", strings.HasPrefix(l, "boot vendor"), "Acquire held at power-up: "+l)
	l = line(ro, "storage-ro final")
	check("storage-ro/final-boot", strings.HasPrefix(l, "boot app"), l)
	return res
}

func guardAreaChecks(r *harness.Run) []harness.Result {
	const a = "l4-guard"
	var res []harness.Result
	check := func(c string, ok bool, l string) {
		if ok {
			res = append(res, harness.Pass(a, c, l))
		} else {
			res = append(res, harness.Fail(a, c, "%s", l))
		}
	}
	if !strings.Contains(r.Console, "l4: guard scenario done") {
		check("write-guard/done", false, "the scenario did not finish")
	}
	for _, l := range append(l4Lines(r, "guard-symlinks"), l4Lines(r, "write-guard")...) {
		f := strings.Fields(l)
		switch {
		case strings.HasPrefix(f[0], "update-into-") || strings.HasPrefix(f[0], "root-"):
			pre := "guard-symlinks/"
			if strings.HasPrefix(f[0], "root-") {
				pre = "write-guard/"
			}
			check(pre+f[0], len(f) > 2 && f[2] != "0", l)
		case f[0] == "app" && f[1] == "alive":
			check("guard-symlinks/app-runs", strings.HasSuffix(l, " app"), "the app runs with its directories refused: "+l)
		case f[0] == "escape":
			check("guard-symlinks/nothing-escaped", strings.HasPrefix(l, "escape [] "), l)
		}
	}
	// p9 as the run left it against the factory seed it started from
	seed := filepath.Join(harness.Workspace(), "analysis", "testenv", "seed", "firmdata0-sds824xhd")
	ents, _ := os.ReadDir(r.File("firmdata0"))
	changed := []string{}
	for _, e := range ents {
		got, _ := os.ReadFile(filepath.Join(r.File("firmdata0"), e.Name()))
		want, err := os.ReadFile(filepath.Join(seed, e.Name())) //nolint:gosec // G304: the simulator's seed
		if err != nil || !bytes.Equal(got, want) {
			changed = append(changed, e.Name())
		}
	}
	check("write-guard/p9-untouched", len(ents) > 0 && len(changed) == 0, "p9 after the run: "+strings.Join(changed, " ")+" differ from the factory seed")
	return append(res, guardChecks(a, r)...)
}
