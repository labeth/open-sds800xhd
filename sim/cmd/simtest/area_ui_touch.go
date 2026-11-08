package main

import (
	"fmt"
	"image"
	"strings"
	"time"

	"github.com/labeth/open-dso-core/uimodel"
)

// The touch-first layout on the simulated unit's screen (a touch screen
// and no F-keys, so the core lays the UI out for a finger): the host taps
// the readouts, the touch bar and the sheet panel's controls, and moves
// fingers over the waveform through the simulator's multi-touch injection
// (touchpath, protocol B slots), checking each step on the UI model, SCPI
// and fb0. The places are the core's touch geometry on 1024×600 with the
// unit's four channels (render/touch.go).
const (
	tW, tH    = 1024, 600
	tBarY     = tH - 28            // the touch bar's middle row
	tWsW      = 80                 // a workspace's tab
	tViewsX   = 4 + 5*tWsW + 10    // the views' segmented control
	tActX     = tW - 4 - 5*64      // the actions: AUTO RUN SINGLE TOOLS MENU
	tChipW    = 104                // a channel's chip
	tRowsX    = tW - 760 + 120 + 8 // the sheet panel's rows
	tRowsY    = 44
	tRowH     = 48
	tHdrH     = 24
	tCtlX     = tRowsX + 190 // a row's controls
	tPlotMidY = (40 + tH - 56) / 2
)

// tapAt taps the screen at p.
func (u *uiRig) tapAt(p image.Point) string { return u.ctl(fmt.Sprintf("touch %d %d", p.X, p.Y)) }

// wsTab is workspace i's tab on the touch bar.
func wsTab(i int) image.Point { return image.Pt(4+i*tWsW+tWsW/2, tBarY) }

// viewSeg is view k of n on the touch bar's segmented control.
func viewSeg(k, n int) image.Point {
	w := min(120, (tActX-10-tViewsX)/n)
	return image.Pt(tViewsX+k*w+w/2, tBarY)
}

// action is action i of the touch bar (0 AUTO .. 4 MENU).
func action(i int) image.Point { return image.Pt(tActX+i*64+32, tBarY) }

// chip is a readout chip: channel ch's (0-based), the timebase's (-1) or
// the trigger's (-2), with the unit's four channels.
func chip(ch int) image.Point {
	switch ch {
	case -1:
		return image.Pt(4+4*tChipW+60, 20)
	case -2:
		return image.Pt(4+4*tChipW+124+95, 20)
	}
	return image.Pt(4+ch*tChipW+50, 20)
}

// closeBtn is the sheet panel's Close.
var closeBtn = image.Pt(tW-52, 22)

// rowY is the middle row of sheet row id on the panel of its tab (just
// opened, its first row at the top), from the web's sheets: each section
// header above a row takes tHdrH.
func (u *uiRig) rowY(tab, id string) (int, bool) {
	var sheets uimodel.SheetsModel
	if err := u.get("/api/sheets", &sheets); err != nil {
		return 0, false
	}
	for _, t := range sheets.Tabs {
		if t.ID != tab {
			continue
		}
		y := tRowsY
		for _, r := range t.Rows {
			if r.Section != "" {
				y += tHdrH
			}
			if r.ID == id {
				return y + tRowH/2, true
			}
			y += tRowH
		}
	}
	return 0, false
}

// viewRows are the values of every row a gesture must leave alone: the
// trigger's, the channels', the timebase's and the acquisition's (all but
// the view's zoom and its pan).
func (u *uiRig) viewRows() string {
	var sheets uimodel.SheetsModel
	if err := u.get("/api/sheets", &sheets); err != nil {
		return "unreadable: " + err.Error()
	}
	var b strings.Builder
	for _, t := range sheets.Tabs {
		switch {
		case t.ID == "trig", t.ID == "time", t.ID == "acq", strings.HasPrefix(t.ID, "ch"):
		default:
			continue
		}
		for _, r := range t.Rows {
			if r.ID == "time.zoom" || r.ID == "time.zoom_pos" || r.Kind == "action" {
				continue
			}
			fmt.Fprintf(&b, "%s=%v ", r.ID, r.Value)
		}
	}
	return b.String()
}

// zoomRow is the view's zoom as the sheets read it.
func (u *uiRig) zoomRow() (float64, float64) {
	z, _ := u.sheetRow("time.zoom")
	p, _ := u.sheetRow("time.zoom_pos")
	zv, _ := z.Value.(float64)
	pv, _ := p.Value.(float64)
	return zv, pv
}

// touchpath moves fingers over the screen (protocol B): each path
// x0,y0,x1,y1 in steps reports 30 ms apart, then lifts them.
func (u *uiRig) touchpath(steps int, paths ...[4]int) string {
	var line strings.Builder
	fmt.Fprintf(&line, "touchpath %d 30", steps)
	for _, p := range paths {
		fmt.Fprintf(&line, " %d,%d,%d,%d", p[0], p[1], p[2], p[3])
	}
	return u.ctl(line.String())
}

// uiTouch checks the touch-first layout: the workspaces and views by the
// touch bar, the readouts opening their sheets, a sheet row's controls
// setting a value that reaches SCPI, and the gestures zooming and panning
// the view without changing a setting. Each workspace's screen and an
// open sheet are kept as shots (touch-*.png).
func uiTouch(u *uiRig) {
	h := u.h
	if u.panel().Mode != modeWorkspace {
		u.ctl(keyMenu)
		u.until(func() bool { return u.panel().Mode == modeWorkspace })
	}
	names := []string{"SCOPE", "BUS", "SPECTRUM", "MEASURE", "COMPARE"}
	for i, n := range []int{2, 4, 4, 2, 0} {
		rep := u.tapAt(wsTab(i))
		ok := u.until(func() bool { return u.panel().Workspace == names[i] })
		h.check("touch-ws-"+strings.ToLower(names[i]), ok, "the %s tab (%s): %s", names[i], rep, u.panel().Workspace)
		time.Sleep(time.Second) // the display loop draws the new workspace
		u.shot("touch-" + strings.ToLower(names[i]) + ".png")
		if n == 0 {
			continue
		}
		v0 := u.panel().View
		u.tapAt(viewSeg(n-1, n))
		ok = u.until(func() bool { return u.panel().View != v0 })
		h.check("touch-view-"+strings.ToLower(names[i]), ok, "%s's last view segment: view %q to %q", names[i], v0, u.panel().View)
		u.tapAt(viewSeg(0, n))
		ok = u.until(func() bool { return u.panel().View == v0 })
		h.check("touch-view-back-"+strings.ToLower(names[i]), ok, "%s's first view segment: view %q", names[i], u.panel().View)
	}
	u.tapAt(wsTab(0))
	u.until(func() bool { return u.panel().Workspace == "SCOPE" })

	// RUN/STOP on the touch bar stops and runs as the key does
	u.tapAt(action(1))
	h.check("touch-stop", u.until(func() bool { return !running(u.status()) }), "the bar's RUN/STOP: running %v", u.status()["running"])
	time.Sleep(200 * time.Millisecond)
	u.tapAt(action(1))
	h.check("touch-run", u.until(func() bool { return running(u.status()) }), "the bar's RUN/STOP again: running %v", u.status()["running"])

	// the readouts open their sheets; Close closes them
	for _, c := range []struct {
		ch  int
		tab string
	}{{0, "ch1"}, {-1, "time"}, {-2, "trig"}} {
		u.tapAt(chip(c.ch))
		ok := u.until(func() bool { s := u.panel(); return s.Sheet.Open && s.Sheet.Tab == c.tab })
		h.check("touch-open-"+c.tab, ok, "the %s readout: sheet %v on %q", c.tab, u.panel().Sheet.Open, u.panel().Sheet.Tab)
		if c.tab == "trig" {
			time.Sleep(time.Second)
			u.shot("touch-sheet.png")
		}
		u.tapAt(closeBtn)
		h.check("touch-close-"+c.tab, u.until(func() bool { return !u.panel().Sheet.Open }), "Close: sheet %v", u.panel().Sheet.Open)
	}

	// a row's + steps the timebase; it reaches the board as the knob does
	t0, _ := scpiQuery(u.ctx, "TDIV?")
	u.tapAt(chip(-1))
	u.until(func() bool { return u.panel().Sheet.Tab == "time" })
	if y, ok := u.rowY("time", "time.tdiv"); h.check("touch-row-tdiv", ok, "the time/div row's place") {
		u.tapAt(image.Pt(tW-8-124-50+22, y)) // its +
		got, ok := u.scpiUntil("TDIV?", func(r string) bool { return r != t0 && r != "" })
		h.check("touch-tdiv-scpi", ok, "the time/div row's +: TDIV? %q to %q", t0, got)
	}
	u.tapAt(closeBtn)
	u.until(func() bool { return !u.panel().Sheet.Open })

	// gestures: a box, a pinch, a pan and a double tap act on the view
	// only; the rows are read after each (its fingers lifted)
	// BUS's entry detection, which may set the time/div to read the bus it
	// finds, ends first; the rows must then hold still
	u.until(func() bool { s := u.panel(); return !s.Display.DecDetecting && !s.AutosetBusy })
	rows0 := u.viewRows()
	u.until(func() bool {
		time.Sleep(time.Second)
		r := u.viewRows()
		defer func() { rows0 = r }()
		return r == rows0
	})
	same := func(step string) {
		time.Sleep(400 * time.Millisecond) // the gesture's fingers lift
		if rows := u.viewRows(); rows != rows0 {
			p := u.panel()
			h.check("touch-settings-"+step, false, "the %s changed a setting (notice %q, bus %q, detecting %v): before %s after %s", step, p.Notice, p.Display.DecNote, p.Display.DecDetecting, rows0, rows)
			rows0 = rows
		}
	}
	u.touchpath(6, [4]int{300, tPlotMidY - 80, 420, tPlotMidY + 80})
	ok := u.until(func() bool { z, _ := u.zoomRow(); return z >= 5 })
	z, _ := u.zoomRow()
	h.check("touch-box-zoom", ok, "a box a ninth of the screen wide: zoom %v", z)
	same("box")
	time.Sleep(time.Second)
	u.shot("touch-box-zoomed.png")
	_, p0 := u.zoomRow()
	u.touchpath(6, [4]int{400, tPlotMidY, 600, tPlotMidY}, [4]int{460, tPlotMidY, 660, tPlotMidY})
	ok = u.until(func() bool { _, p := u.zoomRow(); return p != p0 })
	_, p1 := u.zoomRow()
	h.check("touch-pan", ok, "two fingers right: the zoom's pan %v to %v", p0, p1)
	same("pan")
	tries := 0
	for ok = false; !ok && tries < 2; tries++ { // a guest too busy for the 400 ms between the taps gets a second double tap
		u.ctl(fmt.Sprintf("touch %d %d 60", 500, tPlotMidY)) // a double tap: two taps of 60 ms, 150 ms apart
		time.Sleep(150 * time.Millisecond)
		u.ctl(fmt.Sprintf("touch %d %d 60", 500, tPlotMidY))
		ok = u.until(func() bool { z, _ := u.zoomRow(); return z == 1 })
		time.Sleep(500 * time.Millisecond)
	}
	z, _ = u.zoomRow()
	h.check("touch-double-tap", ok, "a double tap: zoom %v (%d double taps)", z, tries)
	same("double-tap")
	u.touchpath(8, [4]int{480, tPlotMidY, 200, tPlotMidY}, [4]int{544, tPlotMidY, 824, tPlotMidY})
	ok = u.until(func() bool { z, _ := u.zoomRow(); return z >= 5 })
	z, _ = u.zoomRow()
	h.check("touch-pinch", ok, "two fingers spreading 64 to 624 px: zoom %v", z)
	same("pinch")
	u.touchpath(8, [4]int{200, tPlotMidY, 480, tPlotMidY}, [4]int{824, tPlotMidY, 544, tPlotMidY})
	ok = u.until(func() bool { z, _ := u.zoomRow(); return z == 1 })
	z, _ = u.zoomRow()
	h.check("touch-pinch-out", ok, "a pinch back past 1x: zoom %v", z)
	same("pinch-out")
	h.check("touch-gestures-settings", u.viewRows() == rows0, "no trigger, channel, timebase or acquisition row changed by a gesture (%d bytes of rows)", len(rows0))
}
