package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/labeth/open-sds800xhd/sim/harness"
)

// The operator fuzz lane: the core's random operator (webclient/e2e/
// fuzz.mjs in open-dso-core, next to this repository) against the app's web
// UI in the simulator, one seed per run, at the scope's own panel size
// (1024x600) with its four channels' actions. Its invariants are checked
// after every action; a finding is replayed with the same seed and
// DEBUG_AT=<iteration> (simtest -l4 -area l4-fuzz with L4_FUZZ_SEEDS=<seed>).
//
//	L4_FUZZ_SEEDS  seeds: "1-20" (default) or "3,7,11"
//	L4_FUZZ_ITERS  actions per seed (default 60: one seed fits one run)
//	DEBUG_AT       passed to the fuzzer: diagnostics before that iteration
func init() {
	l4register(area{
		name: "l4-fuzz", title: "operator fuzz: seeded random operator on the web UI, 4 channels, 1024x600",
		runs: func(e *env) []harness.Spec {
			var specs []harness.Spec
			for _, seed := range fuzzSeeds() {
				specs = append(specs, harness.Spec{
					Name: fmt.Sprintf("%sfuzz-%d", e.prefix, seed), Secs: fuzzRunSecs, Ports: true, SimdArgs: uiSignals,
					Guest: l4Guest(e, "l4-super.sh"), Files: l4Files(e),
					Host: func(ctx context.Context, dir string) { fuzzHost(ctx, e, dir, seed) },
				})
			}
			return specs
		},
		check: func(_ *env, runs []*harness.Run) []harness.Result {
			var res []harness.Result
			for _, r := range runs {
				res = append(res, fuzzChecks(r)...)
			}
			return res
		},
		// a seed the run's end cut, or that did not start for want of
		// time, runs again on a fresh run (a restart is not a finding)
		again: func(r *harness.Run) bool {
			txt, _ := os.ReadFile(r.File("fuzz/fuzz.txt"))
			return !strings.Contains(string(txt), "\ndone: ")
		},
	})
}

// fuzzRunSecs is a fuzz run's limit.
const fuzzRunSecs = 295

// runLeft is the time left in the run of dir, started when its control
// socket's path was written, with a limit of secs.
func runLeft(dir string, secs int) time.Duration {
	fi, err := os.Stat(filepath.Join(dir, "ctl.path"))
	if err != nil {
		return 0
	}
	return time.Until(fi.ModTime().Add(time.Duration(secs) * time.Second))
}

// fuzzSeeds are the seeds of L4_FUZZ_SEEDS.
func fuzzSeeds() []int {
	spec := os.Getenv("L4_FUZZ_SEEDS")
	if spec == "" {
		spec = "1-20"
	}
	var out []int
	for p := range strings.SplitSeq(spec, ",") {
		lo, hi, isRange := strings.Cut(strings.TrimSpace(p), "-")
		a, err := strconv.Atoi(lo)
		if err != nil {
			continue
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(hi); err != nil {
				continue
			}
		}
		for s := a; s <= b; s++ {
			out = append(out, s)
		}
	}
	return out
}

func fuzzIters() int {
	if n, err := strconv.Atoi(os.Getenv("L4_FUZZ_ITERS")); err == nil && n > 0 {
		return n
	}
	return 60
}

// fuzzHost waits for the app, then runs the fuzzer against it until it ends
// or the run does; it then ends the run. Its output and findings land in the
// run's directory (fuzz/).
func fuzzHost(ctx context.Context, e *env, dir string, seed int) {
	out := filepath.Join(dir, "fuzz")
	_ = os.RemoveAll(out)
	_ = os.MkdirAll(out, 0o755) //nolint:gosec // G301: a run's output directory
	u, done, ok := l4Rig(ctx, dir, "fuzz-host.txt")
	defer done()
	// never start a seed on a run with too little time left for it: about
	// 3.5 s an action under the emulator, and the page's load
	need := time.Duration(fuzzIters())*3500*time.Millisecond + 30*time.Second
	if left := runLeft(dir, fuzzRunSecs); ok && left < need {
		u.h.check("fuzz-run", false, "seed %d: %v left in the run, %v needed: not started", seed, left.Round(time.Second), need)
		ok = false
	}
	if ok {
		e2e := filepath.Join(filepath.Dir(e.root), "open-dso-core", "webclient", "e2e")
		tmp := filepath.Join(e.root, "build", "e2e-tmp")                                            // Chromium on a real disk, not a tmpfs
		_ = os.MkdirAll(tmp, 0o755)                                                                 //nolint:gosec // G301: a build scratch directory
		cmd := exec.CommandContext(ctx, "nice", "-n", "10", "node", filepath.Join(e2e, "fuzz.mjs")) //nolint:gosec // G204: the core's fuzzer with the harness's own arguments
		cmd.Dir = e2e
		cmd.Env = append(os.Environ(), "SCOPE_URL="+webBase, "SEED="+strconv.Itoa(seed), "ITERS="+strconv.Itoa(fuzzIters()),
			"OUT="+out, "VIEWPORT=1024x600", "CHROMIUM_ARGS=--use-gl=egl --ignore-gpu-blocklist", "TMPDIR="+tmp)
		log, err := os.Create(filepath.Join(out, "fuzz.txt")) //nolint:gosec // G304: the run's own directory
		if err == nil {
			cmd.Stdout, cmd.Stderr = log, log
			t0 := time.Now()
			err = cmd.Run()
			_ = log.Close()
			u.h.check("fuzz-run", true, "seed %d: the fuzzer ended after %v (%v)", seed, time.Since(t0).Round(time.Second), err)
		}
	}
	_ = os.WriteFile(filepath.Join(dir, "fuzz", "seed"), []byte(strconv.Itoa(seed)+"\n"), 0o644) //nolint:gosec // G306: a run's output
	_ = os.WriteFile(filepath.Join(dir, "fw", "stop"), nil, 0o644)                               //nolint:gosec // G306: a marker the guest reads
}

// fuzzFinding is one line of the fuzzer's findings.jsonl.
type fuzzFinding struct {
	Iter   int    `json:"iter"`
	Seed   int    `json:"seed"`
	Action string `json:"action"`
	Detail string `json:"detail"`
	Errs   []struct {
		Inv    string `json:"inv"`
		Detail string `json:"detail"`
	} `json:"errs"`
	Tail []string `json:"tail"`
}

// fuzzChecks reads one seed's run: how far the fuzzer got and each finding.
func fuzzChecks(r *harness.Run) []harness.Result {
	const a = "l4-fuzz"
	seed := strings.TrimPrefix(r.Name[strings.LastIndex(r.Name, "fuzz-"):], "fuzz-")
	c := "fuzz/seed-" + seed
	txt, _ := os.ReadFile(r.File("fuzz/fuzz.txt"))
	s := string(txt)
	done := strings.Contains(s, "\ndone: ")
	var res []harness.Result
	if strings.Contains(s, "SKIP: playwright") {
		return []harness.Result{harness.Fail(a, c, "playwright is not installed")}
	}
	f, err := os.Open(r.File("fuzz/findings.jsonl"))
	n := 0
	if err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var x fuzzFinding
			if json.Unmarshal(sc.Bytes(), &x) != nil {
				continue
			}
			n++
			var errs []string
			for _, e := range x.Errs {
				errs = append(errs, e.Inv+": "+e.Detail)
			}
			res = append(res, harness.Fail(a, fmt.Sprintf("%s-iter-%d", c, x.Iter), "%s(%s): %s; replay L4_FUZZ_SEEDS=%s DEBUG_AT=%d",
				x.Action, x.Detail, strings.Join(errs, " | "), seed, x.Iter))
		}
		_ = f.Close()
	}
	reached := 0 // the fuzzer prints its count every 20 actions
	for w := range strings.FieldsSeq(s) {
		if v, err := strconv.Atoi(w); err == nil && v > reached && v <= fuzzIters() {
			reached = v
		}
	}
	switch {
	case done && n == 0:
		res = append(res, harness.Pass(a, c, fmt.Sprintf("%d actions, no finding", fuzzIters())))
	case done:
		res = append(res, harness.Pass(a, c+"-ran", fmt.Sprintf("%d actions, %d findings", fuzzIters(), n)))
	default:
		res = append(res, harness.Fail(a, c+"-ran", "the fuzzer did not finish within the run (reached about %d): %s", reached, lastLines(s, 3)))
	}
	return res
}

func lastLines(s string, n int) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, " / ")
}
