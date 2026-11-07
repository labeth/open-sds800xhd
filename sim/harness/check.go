package harness

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Result is one checked case.
type Result struct {
	Area, Case string
	Pass       bool
	Detail     string
}

// Pass and Fail build results.
func Pass(area, c, detail string) Result { return Result{area, c, true, detail} }

// Fail is a failed case.
func Fail(area, c, format string, a ...any) Result {
	return Result{area, c, false, fmt.Sprintf(format, a...)}
}

// The in-guest board tests talk to these checks through the guest console
// and the log's marks:
//
//	mark    "app: case AREA/NAME"           starts the case's window of the log
//	console "RESULT AREA/NAME PASS|FAIL text" an assertion the guest made
//	console "EXPECT AREA/NAME REG=VAL ..."  the window's last write of each
//	                                        register (REG as "acq:0x0E")
//	console "EXPECTSPI AREA/NAME CHIP TX"   the window holds this SPI frame;
//	                                        an x in TX matches any hex digit
//
// A window ends at the next "app: case" mark.
const casePrefix = "app: case "

// GuestResults checks every RESULT, EXPECT and EXPECTSPI line of a board
// test run, for the areas listed (all when none).
// TRLC-LINKS: REQ-HD-014
func GuestResults(r *Run, areas ...string) []Result {
	var out []Result
	want := func(id string) (string, string, bool) {
		a, c, _ := strings.Cut(id, "/")
		return a, c, len(areas) == 0 || slices.Contains(areas, a)
	}
	for _, l := range r.Lines("RESULT ") {
		f := strings.SplitN(l, " ", 3)
		if len(f) < 2 {
			continue
		}
		a, c, ok := want(f[0])
		if !ok {
			continue
		}
		d := ""
		if len(f) == 3 {
			d = f[2]
		}
		out = append(out, Result{a, c, f[1] == "PASS", d})
	}
	for _, l := range r.Lines("EXPECT ") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		a, c, ok := want(f[0])
		if !ok {
			continue
		}
		out = append(out, expectRegs(r, a, c, f[0], f[1:]))
	}
	for _, l := range r.Lines("EXPECTSPI ") {
		f := strings.Fields(l)
		if len(f) != 3 {
			continue
		}
		a, c, ok := want(f[0])
		if !ok {
			continue
		}
		w := r.Log.Window(casePrefix+f[0], casePrefix)
		if slices.ContainsFunc(SPIWrites(w)[f[1]], func(tx string) bool { return HexMatch(f[2], tx) }) {
			out = append(out, Pass(a, c+" spi", f[1]+" "+f[2]))
		} else {
			out = append(out, Fail(a, c+" spi", "no %s frame %s on the bus", f[1], f[2]))
		}
	}
	return out
}

func expectRegs(r *Run, area, c, id string, kvs []string) Result {
	w := r.Log.Window(casePrefix+id, casePrefix)
	if w == nil {
		return Fail(area, c+" regs", "no window: the case's mark is missing")
	}
	final := Final(w)
	var bad []string
	for _, kv := range kvs {
		k, v, _ := strings.Cut(kv, "=")
		reg, err := ParseReg(k)
		if err != nil {
			bad = append(bad, err.Error())
			continue
		}
		want, err := strconv.ParseUint(strings.TrimPrefix(v, "0x"), 16, 16)
		if err != nil {
			bad = append(bad, "bad value "+kv)
			continue
		}
		got, ok := final[reg]
		switch {
		case !ok:
			bad = append(bad, fmt.Sprintf("%v not written (want %#x)", reg, want))
		case uint64(got) != want:
			bad = append(bad, fmt.Sprintf("%v = %#x, want %#x", reg, got, want))
		}
	}
	if len(bad) > 0 {
		return Fail(area, c+" regs", "%s", strings.Join(bad, "; "))
	}
	return Pass(area, c+" regs", fmt.Sprintf("%d registers on the bus as expected", len(kvs)))
}

// Missing returns a failed result for every case the guest announced with a
// mark but never reported on: a panic or hang in the middle of an area.
func Missing(r *Run, results []Result, areas ...string) []Result {
	seen := map[string]bool{}
	for _, x := range results {
		seen[x.Area+"/"+strings.Fields(x.Case + " ")[0]] = true
	}
	var out []Result
	for _, m := range r.Log.Marks(casePrefix) {
		id := strings.TrimPrefix(m, casePrefix)
		a, c, _ := strings.Cut(id, "/")
		if len(areas) > 0 && !slices.Contains(areas, a) {
			continue
		}
		if !seen[id] {
			out = append(out, Fail(a, c, "the guest marked the case but reported nothing"))
		}
	}
	return out
}

// Summary is one area's counts.
type Summary struct {
	Area       string
	Pass, Fail int
}

// Summarise counts results per area, in the order of areas.
func Summarise(results []Result, areas []string) []Summary {
	m := map[string]*Summary{}
	for _, a := range areas {
		m[a] = &Summary{Area: a}
	}
	for _, r := range results {
		s := m[r.Area]
		if s == nil {
			s = &Summary{Area: r.Area}
			m[r.Area] = s
			areas = append(areas, r.Area)
		}
		if r.Pass {
			s.Pass++
		} else {
			s.Fail++
		}
	}
	out := make([]Summary, 0, len(areas))
	for _, a := range areas {
		out = append(out, *m[a])
	}
	return out
}

// SortResults orders results by area, then case.
func SortResults(rs []Result) {
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].Area != rs[j].Area {
			return rs[i].Area < rs[j].Area
		}
		return rs[i].Case < rs[j].Case
	})
}

// HexMatch reports whether a frame in hex matches a pattern of hex digits
// in which x matches any digit.
func HexMatch(pattern, tx string) bool {
	pattern, tx = strings.ToLower(pattern), strings.ToLower(tx)
	if len(pattern) != len(tx) {
		return false
	}
	for i := range len(pattern) {
		if pattern[i] != 'x' && pattern[i] != tx[i] {
			return false
		}
	}
	return true
}
