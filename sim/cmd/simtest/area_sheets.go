package main

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	dsorender "github.com/labeth/open-dso-core/render"
	"github.com/labeth/open-dso-core/uimodel"

	"github.com/labeth/open-sds800xhd/sim/harness"
)

// The sheets area sweeps every row of the UI model's sheet catalogue (GET
// /api/sheets) twice: through the touch-first layout on the simulated
// unit's screen (the tab tapped, the row brought into view and tapped, its
// own controls changing it: an enum's option or its chooser, a number's −
// and +, an action's button, the match row's keyboard) and through the
// web's sheets (POST /api/sheets/set and /api/sheets/action). Each change
// is read back from the UI model and the row is restored. A row not in use
// must say why; a refused change must give its reason (a notice on the
// screen, the web's error) and is flagged, not failed; a change that does
// not stick and says nothing is a failure. A per-row table lands in each
// run's directory (sheets-rows.txt).
//
// The catalogue does not fit one simulator run (the simulator ends a run
// after 300 s, and the app is several times slower under qemu than on a
// unit), so the tabs are split over runs by sheetGroups; the last group
// also takes any tab no group names, so a new tab is swept, not skipped.
func init() {
	register(area{
		name: "sheets", title: "every sheet row changed and read back by touch and by the web",
		runs: func(e *env) []harness.Spec {
			var out []harness.Spec
			for i, g := range sheetGroups {
				if e.cases != "" && !slices.ContainsFunc(strings.Split(e.cases, ","), func(w string) bool { return strings.Contains(g.name, w) }) {
					continue
				}
				out = append(out, harness.Spec{
					Name: e.prefix + "sheets-" + g.name, Secs: 300, Ports: true, SimdArgs: uiSignals,
					App: e.app, Hook: e.hook, GuestBoot: e.guestBoot,
					Host: func(ctx context.Context, dir string) { sheetsHost(ctx, dir, i) },
				})
			}
			return out
		},
		check: func(_ *env, runs []*harness.Run) []harness.Result {
			var out []harness.Result
			for _, r := range runs {
				out = append(out, hostResultsOf(r, "sheets", sheetsHostFile)...)
			}
			return out
		},
	})
}

// sheetGroup is the tabs one run sweeps.
type sheetGroup struct {
	name string
	tabs []string
}

// sheetGroups split the tabs over the runs, each well within a run's time
// (a row's two paths take about 2 s under qemu; some actions, an Autoset
// or a bus protocol's start, take longer).
var sheetGroups = []sheetGroup{
	{"trig-ch-time", []string{tabTrig, "ch1", "ch2", "ch3", "ch4", "time"}},
	{"acq-view-snd-meas", []string{"acq", "view", "snd", "meas", "curs"}},
	{"cmp-file", []string{"cmp", "file"}},
	{"bus-sys", []string{"bus", "sys"}},
}

// tabTrig is the trigger's tab.
const tabTrig = "trig"

// The sheets area's outputs in a run's directory.
const (
	sheetsHostFile  = "sheets-host.txt"
	sheetsTableFile = "sheets-rows.txt"
)

// sheetSkips are the rows the sweep leaves alone, and why: what would wipe
// the settings or end the run.
var sheetSkips = map[string]string{
	"sys.default": "Factory reset: a default setup wipes every setting (destructive)",
}

// sheetUndo is the action that undoes a row's action, where it is another
// row's (an action row's own Clear, Cancel or Undo is found on the row).
var sheetUndo = map[string][2]string{
	"cmp.build_mask": {"cmp.clear_mask", "clear"},
}

// The row kinds of the sheet catalogue.
const (
	kindEnum   = "enum"
	kindNumber = "number"
	kindAction = "action"
	kindBytes  = "bytes"
)

// Verdicts of one path of a row.
const (
	vPass = "pass"
	vFlag = "flag" // refused, with its reason
	vFail = "fail"
	vSkip = "skip"
)

// The touch-first layout's sheet panel on the unit's 1024×600 screen
// (render/touch.go): the tab column, the header's paging and the rows area
// with a row's label column, and the chooser's grid.
const (
	sPanelX   = tW - 760     // the panel's left edge
	sTabX     = sPanelX + 62 // a tab's middle
	sRowsX0   = tRowsX       // the rows area
	sRowsX1   = tW - 8
	sRowsY0   = tRowsY
	sRowsY1   = tH - 8
	sLabelW   = 190 // a row's label column
	sCtlH     = 36  // a control's height
	sPageRows = (sRowsY1 - sRowsY0) / tRowH
	sCtlX     = sRowsX0 + sLabelW
	sMaxTabH  = 44
)

// The page buttons in the sheet's header.
var (
	pageUp   = image.Pt(tW-96-128+30, 22)
	pageDown = image.Pt(tW-96-64+30, 22)
)

// sweepPath is one path's outcome of a row.
type sweepPath struct{ verdict, detail string }

// sweepRow is one row's line of the table.
type sweepRow struct {
	tab, id, kind string
	avail         bool
	why           string
	touch, web    sweepPath
}

// sheetSweep is the sweep's state over a run.
type sheetSweep struct {
	*uiRig
	table  *os.File
	scroll int               // the first view row the open tab shows, as the host believes
	before map[string]string // every setting's value before the row's sweep
	start  time.Time
}

// sheetsHost runs group gi's sweep while the run's app runs.
func sheetsHost(ctx context.Context, dir string, gi int) {
	f, err := os.Create(filepath.Join(dir, sheetsHostFile)) //nolint:gosec // G304: the run's own directory
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	t, err := os.Create(filepath.Join(dir, sheetsTableFile)) //nolint:gosec // G304: the run's own directory
	if err != nil {
		return
	}
	defer func() { _ = t.Close() }()
	g := sheetGroups[gi]
	u := &uiRig{ctx: ctx, dir: dir, h: hostLog{f}, cl: &http.Client{Timeout: 10 * time.Second}}
	sw := &sheetSweep{uiRig: u, table: t, start: time.Now()}
	h := u.h
	pre := g.name + "/"
	if !h.check(pre+"serving", u.until(func() bool { return u.panel().Workspace != "" }), "the app's UI model answered") {
		return
	}
	up := time.Since(sw.start)
	// the start's own work (BUS entry detection, an Autoset) ends first
	u.until(func() bool { s := u.panel(); return !s.Display.DecDetecting && !s.AutosetBusy })
	var sheets uimodel.SheetsModel
	if err := u.get("/api/sheets", &sheets); !h.check(pre+"catalogue", err == nil && len(sheets.Tabs) > 0, "the web's sheets: %d tabs (%v)", len(sheets.Tabs), err) {
		return
	}
	tabs := groupTabs(sheets, gi)
	var rows []sweepRow
	for _, tb := range tabs {
		for _, r := range tb.Rows {
			rows = append(rows, sweepRow{tab: tb.ID, id: r.ID, kind: r.Kind, avail: r.Available, why: r.Why})
		}
	}
	_, _ = fmt.Fprintf(t, "# %s: %d tabs, %d rows; the app answered %.0f s after the run's host started\n", g.name, len(tabs), len(rows), up.Seconds())
	_, _ = fmt.Fprintf(t, "%-6s %-22s %-7s %-5s %-5s %-5s %s\n", "TAB", "ROW", "KIND", "AVAIL", "TOUCH", "WEB", "DETAIL")
	if !h.check(pre+"open", sw.openSheet(), "MENU on the touch bar opened the sheet: %+v", u.panel().Sheet.Tab) {
		return
	}
	done := 0
	for _, tb := range tabs {
		if ctx.Err() != nil {
			break
		}
		before := sw.values()
		if !sw.openTab(tb) {
			h.check(pre+"tab-"+tb.ID, false, "the %s tab did not open by its tab (sheet %+v)", tb.Name, u.panel().Sheet)
			continue
		}
		for _, r := range tb.Rows {
			if ctx.Err() != nil {
				break
			}
			i := slices.IndexFunc(rows, func(x sweepRow) bool { return x.id == r.ID })
			sw.sweep(tb, &rows[i])
			sw.write(rows[i])
			done++
		}
		if drift := diffValues(before, sw.values()); drift != "" {
			_, _ = fmt.Fprintf(t, "# after %s, rows other than the swept ones' own restore differ: %s\n", tb.ID, drift)
		}
		_, _ = fmt.Fprintf(t, "# %s done at %.0f s\n", tb.ID, time.Since(sw.start).Seconds())
	}
	sw.report(pre, rows, done)
}

// groupTabs are the tabs group gi sweeps, in the catalogue's order: its
// own, and for the last group every tab no group names.
func groupTabs(m uimodel.SheetsModel, gi int) []uimodel.TabModel {
	named := map[string]bool{}
	for _, g := range sheetGroups {
		for _, t := range g.tabs {
			named[t] = true
		}
	}
	var out []uimodel.TabModel
	for _, t := range m.Tabs {
		if slices.Contains(sheetGroups[gi].tabs, t.ID) || gi == len(sheetGroups)-1 && !named[t.ID] {
			out = append(out, t)
		}
	}
	return out
}

// report writes the counts as checks, a failing row's line as its own
// check, and a row not reached in the run's time as a failure.
func (sw *sheetSweep) report(pre string, rows []sweepRow, done int) {
	h := sw.h
	count := func(path func(sweepRow) sweepPath) (n map[string]int, flags []string) {
		n = map[string]int{}
		for _, r := range rows {
			p := path(r)
			if p.verdict == "" {
				continue
			}
			n[p.verdict]++
			if p.verdict == vFlag || p.verdict == vSkip {
				flags = append(flags, r.id+": "+p.detail)
			}
		}
		return n, flags
	}
	touch := func(r sweepRow) sweepPath { return r.touch }
	web := func(r sweepRow) sweepPath { return r.web }
	tn, tf := count(touch)
	wn, wf := count(web)
	h.check(pre+"reached", done == len(rows), "%d of %d rows swept in the run (%.0f s)", done, len(rows), time.Since(sw.start).Seconds())
	h.check(pre+"touch", tn[vFail] == 0 && tn[vPass]+tn[vFlag]+tn[vSkip] > 0, "touch: %d pass, %d flagged, %d skipped, %d failed", tn[vPass], tn[vFlag], tn[vSkip], tn[vFail])
	h.check(pre+"web", wn[vFail] == 0 && wn[vPass]+wn[vFlag]+wn[vSkip] > 0, "web: %d pass, %d flagged, %d skipped, %d failed", wn[vPass], wn[vFlag], wn[vSkip], wn[vFail])
	if len(tf) > 0 {
		h.check(pre+"touch-flagged", true, "%s", strings.Join(tf, "; "))
	}
	if len(wf) > 0 {
		h.check(pre+"web-flagged", true, "%s", strings.Join(wf, "; "))
	}
	var na []string
	for _, r := range rows {
		if !r.avail {
			na = append(na, r.id+" (needs "+r.why+")")
			if r.why == "" {
				h.check(pre+"why-"+r.id, false, "%s is not in use and does not say why", r.id)
			}
		}
		if r.touch.verdict == vFail || r.web.verdict == vFail {
			h.check(pre+"row-"+r.id, false, "touch %s: %s | web %s: %s", r.touch.verdict, r.touch.detail, r.web.verdict, r.web.detail)
		}
	}
	if len(na) > 0 {
		h.check(pre+"not-in-use", true, "%d rows not in use, each saying why: %s", len(na), strings.Join(na, "; "))
	}
}

// write adds row r's line to the table.
func (sw *sheetSweep) write(r sweepRow) {
	d := "touch: " + r.touch.detail + " | web: " + r.web.detail
	if !r.avail {
		d = "[not in use: needs " + r.why + "] " + d
	}
	_, _ = fmt.Fprintf(sw.table, "%-6s %-22s %-7s %-5v %-5s %-5s %s\n", r.tab, r.id, r.kind, r.avail, r.touch.verdict, r.web.verdict, d)
}

// values are every setting row's value, by id.
func (sw *sheetSweep) values() map[string]string {
	var m uimodel.SheetsModel
	out := map[string]string{}
	if err := sw.get("/api/sheets", &m); err != nil {
		return out
	}
	for _, t := range m.Tabs {
		for _, r := range t.Rows {
			if r.Kind != kindAction {
				out[r.ID] = fmt.Sprint(r.Value)
			}
		}
	}
	return out
}

// diffValues lists the rows whose value differs between a and b.
func diffValues(a, b map[string]string) string {
	var out []string
	for k, v := range a {
		if w, ok := b[k]; ok && w != v {
			out = append(out, fmt.Sprintf("%s %s->%s", k, v, w))
		}
	}
	slices.Sort(out)
	return strings.Join(out, ", ")
}

// row is row id as the web's sheets read it now (ID "" when gone).
func (sw *sheetSweep) row(id string) uimodel.RowModel {
	r, _ := sw.sheetRow(id)
	return r
}

// within polls fn every 200 ms for up to d.
func (sw *sheetSweep) within(d time.Duration, fn func() bool) bool {
	for deadline := time.Now().Add(d); sw.ctx.Err() == nil; time.Sleep(200 * time.Millisecond) {
		if fn() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
	}
	return false
}

// quiet waits for a notice on the screen to go: a refusal that repeats
// the same words would not show as new.
func (sw *sheetSweep) quiet() string {
	sw.within(3*time.Second, func() bool { return sw.panel().Notice == "" })
	return sw.panel().Notice
}

// tap taps p and lets the finger lift before anything else touches.
func (sw *sheetSweep) tap(p image.Point) {
	sw.tapAt(p)
	time.Sleep(150 * time.Millisecond)
}

// awake wakes the screen when the saver or the screen-off took it: a touch
// then only wakes it.
func (sw *sheetSweep) awake() {
	if s := sw.panel().Screen; s != "" && s != "on" {
		sw.tap(image.Pt(sPanelX/2, tPlotMidY))
		sw.within(5*time.Second, func() bool { return sw.panel().Screen == "on" })
	}
}

// openSheet opens the sheet by the touch bar's MENU.
func (sw *sheetSweep) openSheet() bool {
	if sw.panel().Sheet.Open {
		return true
	}
	sw.awake()
	sw.tap(action(4))
	return sw.within(10*time.Second, func() bool { return sw.panel().Sheet.Open })
}

// openTab taps tab t in the sheet's tab column.
func (sw *sheetSweep) openTab(t uimodel.TabModel) bool {
	for range 3 {
		if !sw.openSheet() {
			continue
		}
		st := sw.panel()
		i := slices.Index(st.Sheet.Tabs, t.Name)
		if i < 0 {
			return false
		}
		pitch := min(sMaxTabH, tH/max(len(st.Sheet.Tabs), 1))
		sw.awake()
		sw.tap(image.Pt(sTabX, i*pitch+pitch/2))
		if sw.within(8*time.Second, func() bool { s := sw.panel(); return s.Sheet.Open && s.Sheet.Tab == t.ID }) {
			sw.scroll = 0
			sw.homeRows(t)
			return true
		}
	}
	return false
}

// homeRows pages the open tab's rows back to its first.
func (sw *sheetSweep) homeRows(t uimodel.TabModel) {
	for range viewCount(t.Rows)/sPageRows + 1 {
		sw.tap(pageUp) // with nothing above, the panel's header: no target
	}
	sw.scroll = 0
}

// viewCount is how many view rows the rows take: a section header is one.
func viewCount(rows []uimodel.RowModel) int {
	n := len(rows)
	for _, r := range rows {
		if r.Section != "" {
			n++
		}
	}
	return n
}

// viewIndex is row i's view row (the section headers above it count).
func viewIndex(rows []uimodel.RowModel, i int) int {
	n := 0
	for k, r := range rows[:i+1] {
		if r.Section != "" {
			n++
		}
		if k == i {
			return n
		}
		n++
	}
	return n
}

// slotsFrom are the view rows shown from first and each one's top.
func slotsFrom(rows []uimodel.RowModel, first int) map[int]int {
	hdr := map[int]bool{}
	n := 0
	for _, r := range rows {
		if r.Section != "" {
			hdr[n] = true
			n++
		}
		n++
	}
	out := map[int]int{}
	y := sRowsY0
	for v := max(first, 0); v < n; v++ {
		h := tRowH
		if hdr[v] {
			h = tHdrH
		}
		if y+h > sRowsY1 {
			break
		}
		out[v] = y
		y += h
	}
	return out
}

// rowTop is where the open tab shows row i with the host's scroll, paging
// it into view first.
func (sw *sheetSweep) rowTop(rows []uimodel.RowModel, i int) int {
	vi := viewIndex(rows, i)
	n := viewCount(rows)
	for range 6 {
		sl := slotsFrom(rows, sw.scroll)
		if y, ok := sl[vi]; ok {
			return y
		}
		dir := 1
		if vi < sw.scroll {
			dir = -1
		}
		if dir > 0 {
			sw.tap(pageDown)
		} else {
			sw.tap(pageUp)
		}
		sw.scroll = min(max(0, sw.scroll+dir*sPageRows), max(0, n-1))
		time.Sleep(200 * time.Millisecond)
	}
	return slotsFrom(rows, sw.scroll)[vi]
}

// focusRow taps row i's label until the UI model names it focused; when
// another row takes the tap, the scroll that puts that row there is the
// panel's, and the tap goes again.
func (sw *sheetSweep) focusRow(rows []uimodel.RowModel, i int) (int, bool) {
	id := rows[i].ID
	for range 4 {
		sw.awake()
		y := sw.rowTop(rows, i)
		ty := y + tRowH/2
		sw.tap(image.Pt(sRowsX0+60, ty))
		var got string
		if sw.within(6*time.Second, func() bool {
			s := sw.panel()
			if s.Sheet.Row != nil {
				got = s.Sheet.Row.ID
			}
			return got == id
		}) {
			return y, true
		}
		j := slices.IndexFunc(rows, func(r uimodel.RowModel) bool { return r.ID == got })
		if j < 0 {
			continue
		}
		vj := viewIndex(rows, j)
		for s := range viewCount(rows) {
			if top, ok := slotsFrom(rows, s)[vj]; ok && ty >= top && ty < top+tRowH {
				sw.scroll = s
				break
			}
		}
	}
	return 0, false
}

// sweep runs row r's two paths, the touch first.
func (sw *sheetSweep) sweep(tb uimodel.TabModel, r *sweepRow) {
	if why, ok := sheetSkips[r.id]; ok {
		r.touch = sweepPath{vSkip, why}
		r.web = sweepPath{vSkip, why}
		return
	}
	sw.waitIdle()
	sw.before = sw.values()
	r.touch = sw.touchRow(tb, r.id)
	if moved := sw.putBack(r.id); moved != "" {
		r.touch.detail += "; others it moved put back: " + moved
	}
	sw.waitIdle()
	var webChanged bool
	r.web, webChanged = sw.webRow(r.id)
	if moved := sw.putBack(r.id); moved != "" {
		r.web.detail += "; others it moved put back: " + moved
	}
	if r.kind == kindAction && webChanged && r.touch.verdict == vPass && strings.Contains(r.touch.detail, "no visible change") {
		r.touch = sweepPath{vFail, r.touch.detail + "; the web's run of the same action changed the row"}
	}
}

// waitIdle waits for an Autoset or a bus detection to end: the scope
// refuses a remote change meanwhile.
func (sw *sheetSweep) waitIdle() {
	sw.within(30*time.Second, func() bool { s := sw.panel(); return !s.AutosetBusy && !s.Display.DecDetecting })
}

// noticeSince is the screen's notice when it is not before.
func (sw *sheetSweep) noticeSince(before string) string {
	if n := sw.panel().Notice; n != before {
		return n
	}
	return ""
}

// change waits up to d for row id to meet ok or a new notice to show.
func (sw *sheetSweep) change(d time.Duration, id, notice0 string, ok func(uimodel.RowModel) bool) (uimodel.RowModel, string, bool) {
	var r uimodel.RowModel
	var notice string
	got := sw.within(d, func() bool {
		r = sw.row(id)
		if ok(r) {
			return true
		}
		notice = sw.noticeSince(notice0)
		return notice != ""
	})
	if got && ok(r) && sw.holds(id, ok) {
		return r, "", true
	}
	if notice == "" {
		notice = sw.noticeSince(notice0)
	}
	return r, notice, false
}

// holdFor is how long a change must hold: the board refuses a commit
// after the setter returned, and the instrument state then goes back.
const holdFor = 800 * time.Millisecond

// holds reports whether row id still meets ok once a board's refusal would
// have taken the change back.
func (sw *sheetSweep) holds(id string, ok func(uimodel.RowModel) bool) bool {
	time.Sleep(holdFor)
	return ok(sw.row(id))
}

// sameValue compares two row values as the JSON carries them.
func sameValue(a, b any) bool {
	fa, oka := a.(float64)
	fb, okb := b.(float64)
	if oka && okb {
		// relative, with a floor far under any row's step (a skew of 0.2 ns
		// is a step) but over float residue
		return math.Abs(fa-fb) <= max(1e-9*math.Max(math.Abs(fa), math.Abs(fb)), 1e-15)
	}
	return fmt.Sprint(a) == fmt.Sprint(b)
}

// touchRow changes row id by its touch controls, reads it back and
// restores it.
func (sw *sheetSweep) touchRow(tb uimodel.TabModel, id string) sweepPath {
	var t uimodel.TabModel
	var m uimodel.SheetsModel
	if err := sw.get("/api/sheets", &m); err != nil {
		return sweepPath{vFail, "the sheets unreadable: " + err.Error()}
	}
	for _, x := range m.Tabs {
		if x.ID == tb.ID {
			t = x
		}
	}
	i := slices.IndexFunc(t.Rows, func(r uimodel.RowModel) bool { return r.ID == id })
	if i < 0 {
		return sweepPath{vFail, "the row left its tab"}
	}
	if s := sw.panel(); !s.Sheet.Open || s.Sheet.Tab != tb.ID {
		if !sw.openTab(tb) {
			return sweepPath{vFail, "its tab did not open again"}
		}
	}
	y, ok := sw.focusRow(t.Rows, i)
	if !ok {
		return sweepPath{vFail, fmt.Sprintf("a tap on its label did not focus it (focused %v)", sw.focusedID())}
	}
	r := t.Rows[i]
	cy := ctlMid(y)
	switch r.Kind {
	case kindEnum:
		return sw.touchEnum(r, cy)
	case kindNumber:
		return sw.touchNumber(r, cy)
	case kindAction:
		return sw.touchAction(r, cy)
	case kindBytes:
		return sw.touchBytes(r, cy)
	}
	return sweepPath{vFail, "kind " + r.Kind + " has no touch control"}
}

// focusedID is the row the UI model names focused.
func (sw *sheetSweep) focusedID() string {
	if r := sw.panel().Sheet.Row; r != nil {
		return r.ID
	}
	return ""
}

// optionIndex is the option of value v (-1: none).
func optionIndex(r uimodel.RowModel, v any) int {
	return slices.IndexFunc(r.Options, func(o uimodel.OptModel) bool { return sameValue(float64(o.Value), v) })
}

// nextOption is the option after the current one that changes the value.
func nextOption(r uimodel.RowModel) int {
	ci := optionIndex(r, r.Value)
	for k := 1; k <= len(r.Options); k++ {
		j := (ci + k + len(r.Options)) % len(r.Options)
		if !sameValue(float64(r.Options[j].Value), r.Value) {
			return j
		}
	}
	return -1
}

// inlineOptions reports whether an enum row draws its options as buttons
// (else one target opens its chooser), and option k's middle.
func inlineOptions(r uimodel.RowModel, k int) (bool, int) {
	w, mid, x := 0, 0, sCtlX
	for j, o := range r.Options {
		ow := max(44, dsorender.TextWidth(o.Text, 1)+18)
		if j == k {
			mid = x + ow/2
		}
		x += ow + 6
		w += ow + 6
	}
	return w <= sRowsX1-sCtlX, mid
}

// chooseOption taps option k of an enum row at cy: its button, or its
// chooser and the choice (on the chooser's first page).
func (sw *sheetSweep) chooseOption(r uimodel.RowModel, k, cy int) string {
	inline, mid := inlineOptions(r, k)
	if inline {
		sw.tap(image.Pt(mid, cy))
		return "option"
	}
	per, cols := dsorender.TouchChooserPage(sRowsX1-sRowsX0, sRowsY1-sRowsY0)
	if k >= per {
		return ""
	}
	sw.tap(image.Pt((sCtlX+sRowsX1)/2, cy))
	time.Sleep(300 * time.Millisecond)
	w := (sRowsX1 - sRowsX0 - 16) / cols
	sw.tap(image.Pt(sRowsX0+8+(k%cols)*w+w/2, sRowsY0+50+(k/cols)*tRowH+(sCtlH+4)/2))
	return "chooser"
}

// touchEnum picks another option of r by touch, reads it back and picks
// the first again.
func (sw *sheetSweep) touchEnum(r uimodel.RowModel, cy int) sweepPath {
	k := nextOption(r)
	if k < 0 {
		return sweepPath{vFlag, fmt.Sprintf("one option only (%d)", len(r.Options))}
	}
	orig, want := r.Value, r.Options[k]
	if per, _ := dsorender.TouchChooserPage(sRowsX1-sRowsX0, sRowsY1-sRowsY0); k >= per {
		k = slices.IndexFunc(r.Options, func(o uimodel.OptModel) bool { return !sameValue(float64(o.Value), orig) })
		want = r.Options[k]
	}
	n0 := sw.quiet()
	how := sw.chooseOption(r, k, cy)
	got, notice, ok := sw.change(8*time.Second, r.ID, n0, func(x uimodel.RowModel) bool { return sameValue(x.Value, float64(want.Value)) })
	if f := sw.focusedID(); f != r.ID {
		return sweepPath{vFail, fmt.Sprintf("the %s tap for %q landed on %s", how, want.Text, f)}
	}
	if !ok {
		if notice != "" {
			sw.tap(closeChooser(how))
			return sweepPath{vFlag, fmt.Sprintf("%q by %s refused: %s", want.Text, how, notice)}
		}
		return sweepPath{vFail, fmt.Sprintf("%q by %s did not stick, no reason shown (reads %q)", want.Text, how, got.Text)}
	}
	detail := fmt.Sprintf("%s -> %s by %s", r.Text, got.Text, how)
	return sw.touchRestoreEnum(r, orig, detail)
}

// closeChooser is where a tap leaves a chooser that a refusal left open:
// its Cancel (a no-op target on the panel when none is open).
func closeChooser(how string) image.Point {
	if how == "chooser" {
		return image.Pt(sRowsX1-54, sRowsY0+6+sCtlH/2)
	}
	return image.Pt(sRowsX0+300, 10)
}

// touchRestoreEnum puts r's value orig back by touch (the web when the
// touch cannot).
func (sw *sheetSweep) touchRestoreEnum(r uimodel.RowModel, orig any, detail string) sweepPath {
	now := sw.row(r.ID)
	ci := optionIndex(now, orig)
	if ci >= 0 {
		if y, ok := sw.refocus(r.ID); ok {
			sw.chooseOption(now, ci, ctlMid(y))
			if sw.within(8*time.Second, func() bool { return sameValue(sw.row(r.ID).Value, orig) }) {
				return sweepPath{vPass, detail + ", restored"}
			}
		}
	}
	return sw.webRestore(r.ID, orig, detail)
}

// refocus focuses row id of the open tab again (a change may move rows in
// and out of the tab) and returns its top.
func (sw *sheetSweep) refocus(id string) (int, bool) {
	var m uimodel.SheetsModel
	if err := sw.get("/api/sheets", &m); err != nil {
		return 0, false
	}
	tab := sw.panel().Sheet.Tab
	for _, t := range m.Tabs {
		if t.ID != tab {
			continue
		}
		if i := slices.IndexFunc(t.Rows, func(r uimodel.RowModel) bool { return r.ID == id }); i >= 0 {
			return sw.focusRow(t.Rows, i)
		}
	}
	return 0, false
}

// ctlMid is the middle row of the controls of a sheet row whose top is y.
func ctlMid(y int) int { return y + (tRowH-sCtlH)/2 + sCtlH/2 }

// webRestore puts a row's value back through the web, as the last resort
// of a touch path: the rows its change moved first (picking an average
// count also selects Average), then the row.
func (sw *sheetSweep) webRestore(id string, orig any, detail string) sweepPath {
	moved := sw.putBack(id)
	if moved != "" {
		detail += "; others it moved put back: " + moved
	}
	if sameValue(sw.row(id).Value, orig) {
		return sweepPath{vPass, detail + ", restored"}
	}
	res := sw.setRow(id, orig)
	if res.OK && sw.within(5*time.Second, func() bool { return sameValue(sw.row(id).Value, orig) }) {
		return sweepPath{vPass, detail + ", set back by the web"}
	}
	if r := sw.row(id); r.Kind == kindEnum && optionIndex(r, orig) < 0 {
		return sweepPath{vFlag, fmt.Sprintf("%s; its start value %v is none of its options, so it cannot be picked back (%s)", detail, orig, res.Error)}
	}
	return sweepPath{vFail, fmt.Sprintf("%s, not restored: %+v", detail, res)}
}

// putBack sets every other setting the row's sweep moved back to its value
// before it, in the catalogue's order (a mode before its counts), and names
// them.
func (sw *sheetSweep) putBack(id string) string {
	var m uimodel.SheetsModel
	if sw.before == nil || sw.get("/api/sheets", &m) != nil {
		return ""
	}
	var out []string
	for _, t := range m.Tabs {
		for _, r := range t.Rows {
			was, ok := sw.before[r.ID]
			if !ok || r.ID == id || r.Kind == kindAction || fmt.Sprint(r.Value) == was || was == "<nil>" {
				continue
			}
			var v any = was
			if f, err := strconv.ParseFloat(was, 64); err == nil && r.Kind != kindBytes {
				v = f
			}
			res := sw.setRow(r.ID, v)
			out = append(out, fmt.Sprintf("%s %s->%s (%v)", r.ID, fmt.Sprint(r.Value), was, res.OK))
		}
	}
	return strings.Join(out, ", ")
}

// numberDir is the step that changes a number row: up, or down at its top.
func numberDir(r uimodel.RowModel) int {
	v, _ := r.Value.(float64)
	if n := len(r.Ladder); n > 0 && v >= r.Ladder[n-1]*(1-1e-9) {
		return -1
	}
	if r.Max != nil && v >= *r.Max {
		return -1
	}
	return 1
}

// stepBtn is a number row's − (dir -1) or + at cy.
func stepBtn(dir, cy int) image.Point {
	if dir < 0 {
		return image.Pt(sCtlX+22, cy)
	}
	return image.Pt(sRowsX1-124-50+22, cy)
}

// touchNumber steps r by its − or +, reads it back and steps it back.
func (sw *sheetSweep) touchNumber(r uimodel.RowModel, cy int) sweepPath {
	dir := numberDir(r)
	orig := r.Value
	n0 := sw.quiet()
	sw.tap(stepBtn(dir, cy))
	name := map[int]string{-1: "-", 1: "+"}[dir]
	got, notice, ok := sw.change(8*time.Second, r.ID, n0, func(x uimodel.RowModel) bool { return !sameValue(x.Value, orig) })
	if f := sw.focusedID(); f != r.ID {
		return sweepPath{vFail, fmt.Sprintf("the %s tap landed on %s", name, f)}
	}
	if !ok {
		if notice != "" {
			return sweepPath{vFlag, fmt.Sprintf("%s refused: %s", name, notice)}
		}
		return sweepPath{vFail, fmt.Sprintf("%s did not change it, no reason shown (reads %q)", name, got.Text)}
	}
	detail := fmt.Sprintf("%s %s -> %s", name, r.Text, got.Text)
	if y, ok := sw.refocus(r.ID); ok {
		sw.tap(stepBtn(-dir, ctlMid(y)))
		if sw.within(8*time.Second, func() bool { return sameValue(sw.row(r.ID).Value, orig) }) {
			return sweepPath{vPass, detail + ", restored"}
		}
	}
	return sw.webRestore(r.ID, orig, detail)
}

// actButton is button k's middle of an action row whose buttons read
// texts, at cy; ok false when it is not drawn (no room).
func actButton(texts []string, k, cy int) (image.Point, bool) {
	x := sCtlX
	for j, t := range texts {
		w := max(64, dsorender.TextWidth(t, 1)+20)
		if x+w > sRowsX1 {
			return image.Point{}, false
		}
		if j == k {
			return image.Pt(x+w/2, cy), true
		}
		x += w + 6
	}
	return image.Point{}, false
}

// primaryAction is the action the sweep runs on r: the first in use, else
// the first.
func primaryAction(r uimodel.RowModel) int {
	return max(slices.IndexFunc(r.Actions, func(a uimodel.ActModel) bool { return a.Available || !r.Available }), 0)
}

// dimAction reports an action drawn dim on a row in use: it must refuse,
// saying why. A row not in use is still swept (its actions run, as its
// settings are set), so an action of it is not dim by that alone.
func dimAction(r uimodel.RowModel, a uimodel.ActModel) bool { return r.Available && !a.Available }

// undoAction is the action of row r (as it is after action ran) that
// undoes it: its Cancel, Undo or Clear, in use; -1 when it has none.
func undoAction(r uimodel.RowModel, ran string) int {
	for _, id := range []string{"cancel", "undo", "clear"} {
		if id == ran {
			continue
		}
		if j := slices.IndexFunc(r.Actions, func(a uimodel.ActModel) bool { return a.ID == id && (a.Available || !r.Available) }); j >= 0 {
			return j
		}
	}
	return -1
}

// touchAction runs r's action by its button and reads the row (or a
// notice) back; an action that a row of its own undoes is undone.
func (sw *sheetSweep) touchAction(r uimodel.RowModel, cy int) sweepPath {
	if len(r.Actions) == 0 {
		return sweepPath{vFail, "an action row without a button"}
	}
	k := primaryAction(r)
	a := r.Actions[k]
	var texts []string
	for _, x := range r.Actions {
		texts = append(texts, x.Text)
	}
	p, ok := actButton(texts, k, cy)
	if !ok {
		return sweepPath{vFail, fmt.Sprintf("%q is not drawn (no room in the row)", a.Text)}
	}
	n0 := sw.quiet()
	sw.tap(p)
	got, notice, changed := sw.change(3*time.Second, r.ID, n0, func(x uimodel.RowModel) bool { return x.Text != r.Text })
	if !sw.panel().Sheet.Open {
		return sweepPath{vPass, fmt.Sprintf("%q ran: the sheet closed for it (notice %q)", a.Text, notice)}
	}
	if f := sw.focusedID(); f != r.ID {
		return sweepPath{vFail, fmt.Sprintf("the %q tap landed on %s", a.Text, f)}
	}
	if dimAction(r, a) {
		switch {
		case changed:
			return sweepPath{vFail, fmt.Sprintf("%q is not in use but ran: %q -> %q", a.Text, r.Text, got.Text)}
		case notice != "":
			return sweepPath{vFlag, fmt.Sprintf("%q not in use: %s", a.Text, notice)}
		}
		return sweepPath{vFail, fmt.Sprintf("%q not in use: the tap did nothing and said nothing", a.Text)}
	}
	detail := fmt.Sprintf("%q ran", a.Text)
	switch {
	case changed:
		detail += fmt.Sprintf(": %q -> %q", r.Text, got.Text)
	case notice != "":
		detail += ": notice " + strconvQuote(notice)
	default:
		detail += " (no visible change)"
	}
	if changed {
		detail += sw.touchUndo(r, a.ID)
	}
	if u, ok := sheetUndo[r.ID]; ok && changed {
		res := sw.action(u[0], u[1])
		detail += fmt.Sprintf(", %s %s: %v", u[0], u[1], res.OK)
	}
	return sweepPath{vPass, detail}
}

// touchUndo taps the Cancel, Undo or Clear that row r offers after its
// action ran, and says what it left.
func (sw *sheetSweep) touchUndo(r uimodel.RowModel, ran string) string {
	now := sw.row(r.ID)
	j := undoAction(now, ran)
	if j < 0 {
		return ""
	}
	y, ok := sw.refocus(r.ID)
	if !ok {
		return ", not focused again for its undo"
	}
	var tx []string
	for _, x := range now.Actions {
		tx = append(tx, x.Text)
	}
	q, ok := actButton(tx, j, ctlMid(y))
	if !ok {
		return fmt.Sprintf(", %q is not drawn", now.Actions[j].Text)
	}
	sw.tap(q)
	if sw.within(5*time.Second, func() bool { return sw.row(r.ID).Text == r.Text }) {
		return fmt.Sprintf(", %q restored it", now.Actions[j].Text)
	}
	return fmt.Sprintf(", %q left %q", now.Actions[j].Text, sw.row(r.ID).Text)
}

// strconvQuote quotes a notice for the table.
func strconvQuote(s string) string { return fmt.Sprintf("%q", s) }

// The match editor's places (render/render_match.go on 1024×600 under the
// touch layout): the hex keyboard's 6×3 keys and the bar's choices.
const (
	mGridY0 = 80
	mKeyH   = 56
	mCols   = 6
)

// matchKey is the hex keyboard's key at row kr, column kc.
func matchKey(kr, kc int) image.Point {
	cw := (tW - 8) / mCols
	return image.Pt(4+kc*cw+cw/2, mGridY0+kr*mKeyH+mKeyH/2)
}

// matchBar is the match editor's bar choice i (Keys, Type, Delete, OK,
// Cancel, the cursor's two and Clear all).
func matchBar(i int) image.Point {
	w := min(160, (tW-4-8)/8)
	return image.Pt(4+i*w+w/2, tBarY)
}

// touchBytes types the match "55" on the row's keyboard (Edit bytes, the
// keys, OK), reads it back and clears it (Any byte) or types the old one
// back through the web.
func (sw *sheetSweep) touchBytes(r uimodel.RowModel, cy int) sweepPath {
	orig, _ := r.Value.(string)
	p, ok := actButton(matchActs, 0, cy)
	if !ok {
		return sweepPath{vFail, "Edit bytes is not drawn"}
	}
	n0 := sw.quiet()
	sw.tap(p)
	var notice string
	opened := sw.within(6*time.Second, func() bool {
		s := sw.panel()
		if s.Notice != n0 {
			notice = s.Notice
		}
		return s.Picker.Open && s.Picker.Mode == "editor"
	})
	if !opened {
		if notice != "" {
			return sweepPath{vFlag, "Edit bytes refused: " + notice}
		}
		return sweepPath{vFail, "Edit bytes opened no editor and said nothing"}
	}
	for range 3 {
		if strings.EqualFold(sw.panel().Picker.KeySet, "hex") {
			break
		}
		sw.tap(matchBar(0))
	}
	sw.tap(matchBar(7)) // Clear all
	sw.tap(matchKey(0, 5))
	sw.tap(matchKey(0, 5))
	toks := sw.panel().Picker.Tokens
	n1 := sw.panel().Notice
	sw.tap(matchBar(3)) // OK
	got, notice, ok := sw.change(8*time.Second, r.ID, n1, func(x uimodel.RowModel) bool { return x.Value == "55" })
	if s := sw.panel(); s.Picker.Open {
		sw.tap(matchBar(4)) // Cancel
	}
	if !ok {
		if notice != "" {
			return sweepPath{vFlag, fmt.Sprintf("typed %v, OK refused: %s", toks, notice)}
		}
		return sweepPath{vFail, fmt.Sprintf("typed %v, OK: the match reads %q, no reason shown", toks, got.Text)}
	}
	detail := fmt.Sprintf("typed 5 5 + OK: %q -> %q", r.Text, got.Text)
	if orig == "" && sw.anyByte(r.ID) {
		return sweepPath{vPass, detail + ", Any byte restored it"}
	}
	return sw.webRestore(r.ID, orig, detail)
}

// matchActs are the match row's buttons.
var matchActs = []string{"Edit bytes", "From screen", "Any byte"}

// anyByte taps the match row's Any byte and reports that the match then
// reads empty.
func (sw *sheetSweep) anyByte(id string) bool {
	y, ok := sw.refocus(id)
	if !ok {
		return false
	}
	q, ok := actButton(matchActs, 2, ctlMid(y))
	if !ok {
		return false
	}
	sw.tap(q)
	return sw.within(5*time.Second, func() bool { return sw.row(id).Value == "" })
}

// action runs a row's action through the web.
func (sw *sheetSweep) action(row, act string) uimodel.SetResult {
	b, _ := json.Marshal(map[string]string{"row": row, "action": act}) // two strings always encode
	var res uimodel.SetResult
	if err := sw.post("/api/sheets/action", string(b), &res); err != nil {
		res.Error = err.Error()
	}
	return res
}

// webTarget is the value the web sets a row to: another option, the next
// rung or step, or the match "55".
func webTarget(r uimodel.RowModel) (any, bool) {
	switch r.Kind {
	case kindEnum:
		k := nextOption(r)
		if k < 0 {
			return nil, false
		}
		return r.Options[k].Value, true
	case kindNumber:
		v, _ := r.Value.(float64)
		if n := len(r.Ladder); n > 0 {
			i := slices.IndexFunc(r.Ladder, func(x float64) bool { return x > v*(1+1e-9) && x > v+1e-15 })
			if i < 0 {
				i = n - 2
			}
			return r.Ladder[max(i, 0)], n > 1
		}
		step := 1.0
		if r.Step != nil && *r.Step > 0 {
			step = *r.Step
		} else if r.Min != nil && r.Max != nil && *r.Max > *r.Min {
			step = (*r.Max - *r.Min) / 10
		}
		t := v + step
		if r.Max != nil && t > *r.Max {
			t = v - step
		}
		return t, true
	case kindBytes:
		return "55", true
	}
	return nil, false
}

// webRow changes row id through the web's sheets, reads it back and
// restores it; changed reports that an action changed the row's text.
func (sw *sheetSweep) webRow(id string) (sweepPath, bool) {
	r := sw.row(id)
	if r.ID == "" {
		return sweepPath{vFail, "the row is gone from the web's sheets"}, false
	}
	if r.Kind == kindAction {
		return sw.webAction(r)
	}
	target, ok := webTarget(r)
	if !ok {
		return sweepPath{vFlag, "nothing else to set it to"}, false
	}
	orig := r.Value
	n0 := sw.quiet()
	res := sw.setRow(id, target)
	if !res.OK {
		if res.Error == "" {
			return sweepPath{vFail, fmt.Sprintf("set %v refused without a reason", target)}, false
		}
		return sweepPath{vFlag, fmt.Sprintf("set %v refused: %s", target, res.Error)}, false
	}
	moved := func(x uimodel.RowModel) bool { return !sameValue(x.Value, orig) }
	got, notice, stuck := sw.change(5*time.Second, id, n0, moved)
	if !stuck {
		if notice != "" {
			return sweepPath{vFlag, fmt.Sprintf("set %v answered ok, then refused: %s", target, notice)}, false
		}
		return sweepPath{vFail, fmt.Sprintf("set %v answered ok (%q) but reads %v, no reason shown", target, res.Text, got.Value)}, false
	}
	detail := fmt.Sprintf("%s -> %s", r.Text, got.Text)
	if !sameValue(got.Value, target) {
		detail += fmt.Sprintf(" (asked %v)", target)
	}
	back := sw.setRow(id, orig)
	if !back.OK || !sw.within(5*time.Second, func() bool { return sameValue(sw.row(id).Value, orig) }) {
		return sw.webRestore(id, orig, detail), false
	}
	return sweepPath{vPass, detail + ", restored"}, false
}

// webAction runs r's action through the web and undoes it as the touch
// path does.
func (sw *sheetSweep) webAction(r uimodel.RowModel) (sweepPath, bool) {
	if len(r.Actions) == 0 {
		return sweepPath{vFail, "an action row without an action"}, false
	}
	a := r.Actions[primaryAction(r)]
	res := sw.action(r.ID, a.ID)
	if dimAction(r, a) {
		if res.OK {
			return sweepPath{vFail, fmt.Sprintf("%q is not in use but ran", a.Text)}, false
		}
		if res.Error == "" {
			return sweepPath{vFail, fmt.Sprintf("%q not in use: refused without a reason", a.Text)}, false
		}
		return sweepPath{vFlag, fmt.Sprintf("%q not in use: %s", a.Text, res.Error)}, false
	}
	if !res.OK {
		if res.Error == "" {
			return sweepPath{vFail, fmt.Sprintf("%q refused without a reason", a.Text)}, false
		}
		return sweepPath{vFlag, fmt.Sprintf("%q refused: %s", a.Text, res.Error)}, false
	}
	got := sw.row(r.ID)
	changed := got.Text != r.Text
	detail := fmt.Sprintf("%q ran", a.Text)
	if changed {
		detail += fmt.Sprintf(": %q -> %q", r.Text, got.Text)
	}
	if j := undoAction(got, a.ID); changed && j >= 0 {
		u := sw.action(r.ID, got.Actions[j].ID)
		detail += fmt.Sprintf(", %s: %v %s", got.Actions[j].Text, u.OK, u.Error)
	}
	if u, ok := sheetUndo[r.ID]; ok && changed {
		res := sw.action(u[0], u[1])
		detail += fmt.Sprintf(", %s %s: %v", u[0], u[1], res.OK)
	}
	return sweepPath{vPass, detail}, changed
}
