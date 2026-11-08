package main

import (
	"regexp"
	"strings"

	"github.com/labeth/open-sds800xhd/sim/harness"
)

// clauseLine is a go test -v verdict of one conformance clause, runLine
// the start of one.
var (
	clauseLine = regexp.MustCompile(`^\s*--- (PASS|FAIL|SKIP): TestConformance/(\S+)`)
	runLine    = regexp.MustCompile(`^=== (RUN|CONT|PAUSE|NAME)\s+TestConformance/(\S+)`)
)

// conformanceResults reads each clause's verdict from the board test's
// console; a failed or skipped clause carries the messages go test printed
// while it ran.
func conformanceResults(r *harness.Run) []harness.Result {
	msgs := map[string][]string{}
	cur := ""
	var out []harness.Result
	for l := range strings.SplitSeq(r.Console, "\n") {
		if m := runLine.FindStringSubmatch(l); m != nil {
			cur = m[2]
			continue
		}
		m := clauseLine.FindStringSubmatch(l)
		if m == nil {
			if cur != "" && strings.HasPrefix(l, "    ") && strings.TrimSpace(l) != "" {
				msgs[cur] = append(msgs[cur], strings.TrimSpace(l))
			}
			continue
		}
		detail := strings.Join(msgs[m[2]], "; ")
		switch m[1] {
		case "FAIL":
			out = append(out, harness.Fail("conformance", m[2], "%s", detail))
		case "SKIP":
			out = append(out, harness.Result{Area: "conformance", Case: m[2] + " (skipped)", Pass: true, Detail: detail})
		default:
			out = append(out, harness.Result{Area: "conformance", Case: m[2], Pass: true})
		}
	}
	return out
}

func init() {
	register(area{
		name: "conformance", title: "the core's HAL conformance suite, clause by clause",
		runs: func(e *env) []harness.Spec {
			return []harness.Spec{{
				Name: e.prefix + "conformance", Secs: 240, SimdArgs: signals,
				Board: e.boardtest, BoardArgs: boardArgs("^TestConformance$"),
			}}
		},
		check: func(_ *env, runs []*harness.Run) []harness.Result {
			res := conformanceResults(runs[0])
			if len(res) == 0 {
				res = append(res, harness.Fail("conformance", "go test", "no clause reported: the board test did not run"))
			}
			return res
		},
	})
}
