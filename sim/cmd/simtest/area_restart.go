package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/labeth/open-sds800xhd/sim/harness"
)

// The restart area runs the app twice on the same p12: the first run sets
// the timebase and channel scales over SCPI and is stopped once the
// settings saver has written them; the second, started on the kept p12,
// must come up with them (the core's settings persistence on this
// board's settings store).
func init() {
	register(area{
		name: "restart", title: "restart rows: the app's settings on p12 survive a restart",
		runs: func(e *env) []harness.Spec {
			name := e.prefix + "restart"
			spec := func(keep bool, host func(context.Context, string)) harness.Spec {
				return harness.Spec{
					Name: name, Secs: 150, Ports: true, Keep: keep, SimdArgs: signals,
					App: e.app, Hook: e.hook, GuestBoot: e.guestBoot, Host: host,
				}
			}
			return []harness.Spec{
				spec(false, func(ctx context.Context, dir string) { restartHost(ctx, dir, true) }),
				spec(true, func(ctx context.Context, dir string) { restartHost(ctx, dir, false) }),
			}
		},
		check: func(_ *env, runs []*harness.Run) []harness.Result {
			var res []harness.Result
			for i, r := range runs {
				res = append(res, hostResultsOf(r, "restart", restartHostFile(i == 0))...)
			}
			if len(runs) != 2 {
				res = append(res, harness.Fail("restart", "runs", "%d runs, want 2", len(runs)))
			}
			return res
		},
	})
}

// restartHostFile is where the host's checks of the first or second run
// land (the runs share their directory).
func restartHostFile(first bool) string {
	if first {
		return "restart-host-1.txt"
	}
	return "restart-host-2.txt"
}

// restartSet are the settings the first run makes, and the queries that
// read them back.
var restartSet = []struct{ set, query string }{
	{":TIM:SCAL 5E-6", ":TIM:SCAL?"},
	{":CHAN2:SCAL 0.5", ":CHAN2:SCAL?"},
	{":CHAN3:SCAL 2", ":CHAN3:SCAL?"},
}

// restartWant is where the first run leaves the replies the second must
// give, beside the run directory (the runs share the name, not the
// directory's contents).
func restartWant(dir string) string { return filepath.Join(filepath.Dir(dir), "restart-want.txt") }

// restartHost waits for the app, then either sets the settings and waits
// out the saver's settle time (first), or checks they came back (second).
func restartHost(ctx context.Context, dir string, first bool) {
	f, err := os.Create(filepath.Join(dir, restartHostFile(first))) //nolint:gosec // G304: the run's own directory
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	h := hostLog{f}
	cl := &http.Client{Timeout: 5 * time.Second}
	up := false
	for !up && ctx.Err() == nil {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, webBase+"/api/status", nil)
		if err == nil {
			if r, err := cl.Do(req); err == nil {
				_ = r.Body.Close()
				up = r.StatusCode == http.StatusOK
			}
		}
		if !up {
			time.Sleep(time.Second)
		}
	}
	run := map[bool]string{true: "first", false: "second"}[first]
	if !h.check(run+"-serving", up, "the app's web server answered") {
		return
	}
	if first {
		var want []byte
		for _, s := range restartSet {
			before, _ := scpiQuery(ctx, s.query)
			_, _ = scpiQuery(ctx, s.set)
			// A setting reaches its query once the owner has committed it.
			reply, err := before, error(nil)
			for deadline := time.Now().Add(10 * time.Second); reply == before && time.Now().Before(deadline) && ctx.Err() == nil; time.Sleep(250 * time.Millisecond) {
				reply, err = scpiQuery(ctx, s.query)
			}
			h.check("set-"+caseName(s.query), err == nil && reply != "" && reply != before, "%s: %q (%v)", s.set, reply, err)
			want = fmt.Appendf(want, "%s\n", reply)
		}
		_ = os.WriteFile(restartWant(dir), want, 0o600)
		// The saver writes a change that held for 2 s, polling each second.
		time.Sleep(6 * time.Second)
		h.check("saved", true, "waited out the saver")
		return
	}
	want, err := os.ReadFile(restartWant(dir))
	if !h.check("first-run", err == nil, "the first run's replies: %v", err) {
		return
	}
	lines := strings.Split(strings.TrimRight(string(want), "\n"), "\n")
	for i, s := range restartSet {
		if i >= len(lines) {
			break
		}
		var reply string
		ok := false
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline) && ctx.Err() == nil; time.Sleep(500 * time.Millisecond) {
			if reply, _ = scpiQuery(ctx, s.query); reply == lines[i] {
				ok = true
				break
			}
		}
		h.check("restored-"+caseName(s.query), ok, "%q after the restart, %q before", reply, lines[i])
	}
}

// caseName is a query as a result's case name: no spaces, colons or marks.
func caseName(q string) string {
	return strings.ToLower(strings.NewReplacer(":", "", "?", "").Replace(q))
}
