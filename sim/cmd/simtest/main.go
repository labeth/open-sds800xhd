// Command simtest is the simulator parity harness: it runs every HAL area of
// the board in the tier-1 simulator (the in-guest board tests, and the app
// for the areas only the app exercises), checks what reached the simulated
// hardware and prints pass/fail per area. The areas follow the CML 1.2
// regression checklist as the HAL v1 parity plan maps it onto this scope.
//
// Usage (from the repository root, after make app boardtest):
//
//	go run ./sim/cmd/simtest [-area a,b] [-reuse] [-prefix p] [-list]
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/labeth/open-sds800xhd/sim/harness"
)

// env is where the harness finds what it runs.
type env struct {
	root      string // the repository
	prefix    string // run-name prefix
	reuse     bool   // check the runs of an earlier invocation instead of running
	boardtest string
	app       string
	hook      string
	guestBoot string
	cosim     string // the open PL co-simulation binary (-cosim): the run's PL is the open design
}

// area is one checklist area: the runs it needs and its checks.
type area struct {
	name, title string
	runs        func(e *env) []harness.Spec
	check       func(e *env, runs []*harness.Run) []harness.Result
}

var areas []area

// boardArgs are the board test binary's arguments to run the tests named
// by the pattern, verbosely (the harness reads their PASS lines).
func boardArgs(pattern string) []string { return []string{"-test.v", "-test.run", pattern} }

func register(a area) { areas = append(areas, a) }

// boardArea is an area checked by the in-guest board test Test<test> alone.
func boardArea(name, title, test string, secs int, simd string, scenario []string,
	extra func(e *env, r *harness.Run) []harness.Result,
) area {
	return area{
		name: name, title: title,
		runs: func(e *env) []harness.Spec {
			return []harness.Spec{{
				Name: e.prefix + name, Secs: secs, SimdArgs: simd, Scenario: scenario,
				Board: e.boardtest, BoardArgs: boardArgs("^Test" + test + "$"),
			}}
		},
		check: func(e *env, runs []*harness.Run) []harness.Result {
			r := runs[0]
			res := harness.GuestResults(r, name)
			res = append(res, harness.Missing(r, res, name)...)
			if !strings.Contains(r.Console, "--- PASS: Test"+test) {
				res = append(res, harness.Fail(name, "go test", "Test%s did not pass in the guest", test))
			}
			if extra != nil {
				res = append(res, extra(e, r)...)
			}
			return res
		},
	}
}

func main() {
	only := flag.String("area", "", "comma-separated areas to run (default: all)")
	reuse := flag.Bool("reuse", false, "check the runs of an earlier invocation instead of running the simulator")
	prefix := flag.String("prefix", "o800-", "run-name prefix")
	list := flag.Bool("list", false, "list the areas")
	cosim := flag.Bool("cosim", false, "run the open-PL co-simulation areas (fpga/build/cosim/opl_cosim, simd -pl open)")
	flag.Parse()

	root, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	e := &env{
		root: root, prefix: *prefix, reuse: *reuse,
		boardtest: filepath.Join(root, "build", "boardtest"),
		app:       filepath.Join(root, "build", "app"),
		hook:      filepath.Join(root, "ota", "boot", "extra.sh"),
		guestBoot: filepath.Join(root, "sim", "guest-boot.sh"),
	}
	if *cosim {
		areas, e.cosim = cosimAreas, filepath.Join(root, "fpga", "build", "cosim", "opl_cosim")
		if *prefix == "o800-" {
			e.prefix = "cos-"
		}
	}
	if *list {
		for _, a := range areas {
			fmt.Printf("%-12s %s\n", a.name, a.title)
		}
		return
	}
	var sel []area
	for _, a := range areas {
		if *only == "" || slices.Contains(strings.Split(*only, ","), a.name) {
			sel = append(sel, a)
		}
	}
	if len(sel) == 0 {
		fatal(fmt.Errorf("no area matches %q (-list)", *only))
	}

	var results []harness.Result
	var names []string
	for _, a := range sel {
		names = append(names, a.name)
		fmt.Fprintf(os.Stderr, "simtest: %s ...\n", a.name)
		runs, err := run(e, a)
		if err != nil {
			results = append(results, harness.Fail(a.name, "run", "%v", err))
			continue
		}
		results = append(results, a.check(e, runs)...)
	}
	harness.SortResults(results)
	report := render(sel, results, names)
	if e.cosim != "" {
		report += renderCosim(results, names)
	}
	fmt.Print(report)
	_ = os.MkdirAll(filepath.Join(root, "build"), 0o755) //nolint:gosec // G301: a build output directory of the repository
	out := "sim-test.txt"
	if e.cosim != "" {
		out = "cosim-test.txt"
	}
	_ = os.WriteFile(filepath.Join(root, "build", out), []byte(report), 0o644) //nolint:gosec // G306: a build report of the repository
	for _, r := range results {
		if !r.Pass {
			os.Exit(1)
		}
	}
}

func run(e *env, a area) ([]*harness.Run, error) {
	var out []*harness.Run
	for _, s := range a.runs(e) {
		if e.cosim != "" {
			s.SimdArgs = cosimArgs(e, s)
		}
		var r *harness.Run
		var err error
		if e.reuse {
			r, err = harness.Load(s.Name)
		} else {
			r, err = harness.Exec(s)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func render(sel []area, results []harness.Result, names []string) string {
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	// tw writes to a strings.Builder, which does not fail.
	_, _ = fmt.Fprintln(tw, "AREA\tCML 1.2 AREA\tPASS\tFAIL\tVERDICT")
	for _, s := range harness.Summarise(results, names) {
		title := ""
		for _, a := range sel {
			if a.name == s.Area {
				title = a.title
			}
		}
		v := passWord
		if s.Fail > 0 || s.Pass == 0 {
			v = "FAIL"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\n", s.Area, title, s.Pass, s.Fail, v)
	}
	_ = tw.Flush()
	var fails []string
	for _, r := range results {
		if !r.Pass {
			fails = append(fails, fmt.Sprintf("  FAIL %s/%s: %s", r.Area, r.Case, r.Detail))
		}
	}
	if len(fails) > 0 {
		b.WriteString("\n" + strings.Join(fails, "\n") + "\n")
	}
	return b.String()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "simtest:", err)
	os.Exit(2)
}
