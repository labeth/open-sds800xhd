package main

import (
	"context"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/labeth/open-sds800xhd/sim/harness"
)

// displayHostFile is where the host's checks of the display area's app run
// land.
const displayHostFile = "display-host.txt"

// The marks the host leaves in the device model's log around each step,
// which checkViewLog reads the map engine's writes between.
const (
	markZoomOn  = "display: zoom on"
	markTdiv    = "display: timebase"
	markPan     = "display: zoom pan"
	markZoomOff = "display: zoom off"
	markScreen  = "display: screen"
)

// Map registers of the main/zoom split (spec 07 §7.4).
const (
	mapZoomSplit = harness.BaseMAP + 4*0x10A
	mapMainRows  = harness.BaseMAP + 4*0x091
)

// peek reads a register of the device model.
func (u *uiRig) peek(addr uint64) (uint64, bool) {
	rep := u.ctl(fmt.Sprintf("peek %#x", addr))
	v, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(rep), "0x"), 16, 64)
	return v, err == nil
}

// peekUntil polls a register until it reads want, for up to 20 s.
func (u *uiRig) peekUntil(addr, want uint64) (uint64, bool) {
	var v uint64
	ok := u.until(func() bool {
		var read bool
		v, read = u.peek(addr)
		return read && v == want
	})
	return v, ok
}

// displayHost drives the app's UI model and reads back what reached the
// map engine and the screen.
func displayHost(ctx context.Context, dir string) {
	f, err := os.Create(filepath.Join(dir, displayHostFile)) //nolint:gosec // G304: the run's own directory
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	u := &uiRig{ctx: ctx, dir: dir, h: hostLog{f}, cl: &http.Client{Timeout: 10 * time.Second}}
	h := u.h
	if !h.check("serving", u.until(func() bool { return u.panel().Workspace != "" }), "the app's UI model answered") {
		return
	}
	_, _ = scpiQuery(ctx, ":TIM:SCAL 1E-6")
	_, ok := u.scpiUntil("TDIV?", func(r string) bool { return near(tdivReply(r), 1e-6, 0.01) })
	h.check("tdiv-start", ok, "1 µs/div before the zoom")
	time.Sleep(2 * time.Second) // the commit and a few frames at it

	// The UI's zoom switches the PL's main/zoom split on (spec 07 §7.4).
	u.ctl("mark " + markZoomOn)
	res := u.setRow("time.zoom", 10)
	v, ok := u.peekUntil(mapZoomSplit, 1)
	h.check("zoom-on", res.OK && ok, "time.zoom 10x (%+v): map 0x10A = %d", res, v)
	v, ok = u.peekUntil(mapMainRows, 460)
	h.check("zoom-rows", ok, "the main window's rows with the zoom window under it: map 0x091 = %d", v)
	time.Sleep(2 * time.Second)

	// A timebase change with the zoom on: the zoom window follows.
	u.ctl("mark " + markTdiv)
	_, _ = scpiQuery(ctx, ":TIM:SCAL 1E-5")
	_, ok = u.scpiUntil("TDIV?", func(r string) bool { return near(tdivReply(r), 1e-5, 0.01) })
	v, _ = u.peek(mapZoomSplit)
	h.check("zoom-tdiv", ok && v == 1, "10 µs/div with the zoom on: map 0x10A = %d", v)
	time.Sleep(3 * time.Second)

	// The zoom pan moves the zoom window along the record.
	u.ctl("mark " + markPan)
	res = u.setRow("time.zoom_pos", 20)
	h.check("zoom-pan", res.OK, "time.zoom_pos 20 %%: %+v", res)
	time.Sleep(3 * time.Second)

	// Zoom off: the split goes back to the full screen.
	u.ctl("mark " + markZoomOff)
	res = u.setRow("time.zoom", 1)
	v, ok = u.peekUntil(mapZoomSplit, 0)
	h.check("zoom-off", res.OK && ok, "time.zoom Off (%+v): map 0x10A = %d", res, v)
	v, ok = u.peekUntil(mapMainRows, 480)
	h.check("zoom-off-rows", ok, "the main window's rows: map 0x091 = %d", v)
	time.Sleep(2 * time.Second)

	u.ctl("mark " + markScreen)
	displayScreen(u)
	u.ctl("mark display: end")
}

// displayScreen stops the run (a still picture), reads /api/screen.png
// and dumps fb0: the PNG is the panel's 1024×600 and every opaque pixel of
// the UI layer is fb0's.
func displayScreen(u *uiRig) {
	h := u.h
	_, _ = scpiQuery(u.ctx, ":TRIG:STOP")
	h.check("stopped", u.until(func() bool { return !running(u.status()) }), "STOP: running %v", u.status()["running"])
	time.Sleep(2 * time.Second) // the stopped picture drawn
	shot, err := u.screenPNG(filepath.Join(u.dir, "shots", "display-screen.png"))
	if !h.check("screen-png", err == nil && shot.Rect.Dx() == 1024 && shot.Rect.Dy() == 600, "/api/screen.png: %v %v", shotSize(shot), err) {
		return
	}
	fb, rep := u.shot("display-screen-fb0.png")
	if !h.check("screen-fb0", fb != nil && fb.Rect == shot.Rect, "fb0 dumped: %s", rep) {
		return
	}
	opaque, same := 0, 0
	for y := range 600 {
		for x := range 1024 {
			p := fb.NRGBAAt(x, y)
			if p.A != 0xFF {
				continue
			}
			opaque++
			if q := shot.NRGBAAt(x, y); q.R == p.R && q.G == p.G && q.B == p.B {
				same++
			}
		}
	}
	h.check("screen-ui", opaque > 10000 && same >= opaque*995/1000, "%d of %d opaque UI pixels of fb0 as the PNG shows them", same, opaque)
}

// shotSize is a picture's size for a message.
func shotSize(img *image.NRGBA) string {
	if img == nil {
		return "no picture"
	}
	return fmt.Sprintf("%dx%d", img.Rect.Dx(), img.Rect.Dy())
}

// screenPNG fetches /api/screen.png, keeps it at path and decodes it.
func (u *uiRig) screenPNG(path string) (*image.NRGBA, error) {
	req, err := http.NewRequestWithContext(u.ctx, http.MethodGet, webBase+"/api/screen.png", nil)
	if err != nil {
		return nil, err
	}
	r, err := u.cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Body.Close() }()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	if r.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", r.StatusCode, strings.TrimSpace(string(b)))
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, err
	}
	img, err := png.Decode(strings.NewReader(string(b)))
	if err != nil {
		return nil, err
	}
	n := image.NewNRGBA(img.Bounds())
	draw.Draw(n, n.Rect, img, img.Bounds().Min, draw.Src)
	return n, nil
}

// zoomBank is the last value each register took in the zoom window's banks
// among events: the writes from a zoom bank's start (ACQ 0x097 = 1) to the
// zoom map sets (MAP 0x061 = 250) that follow the banks.
func zoomBank(events []harness.Event) map[harness.Reg]uint16 {
	start := harness.Reg{Plane: "acq", Index: 0x097}
	end := harness.Reg{Plane: "map", Index: 0x061}
	out := map[harness.Reg]uint16{}
	in := false
	for _, w := range harness.Writes(events) {
		switch {
		case w.Reg == start && w.Val == 1:
			in = true
		case w.Reg == end && w.Val == 250:
			in = false
		case in:
			out[w.Reg] = w.Val
		}
	}
	return out
}

// checkViewLog checks what the UI's zoom and timebase wrote to the PL: the
// zoom window's banks and map sets at each step, the banks rewritten for a
// new timebase (the zoom window's samples follow the main window's), its
// record start following the pan (ACQ 0x09A), and the split off at the end.
func checkViewLog(r *harness.Run) []harness.Result {
	const area = "display"
	if r.Log == nil {
		return []harness.Result{harness.Fail(area, "view-log", "no device model log")}
	}
	on := zoomBank(r.Log.Window(markZoomOn, "display: "))
	tdiv := zoomBank(r.Log.Window(markTdiv, "display: "))
	pan := zoomBank(r.Log.Window(markPan, "display: "))
	off := harness.Final(r.Log.Window(markZoomOff, "display: "))
	samples := harness.Reg{Plane: "acq", Index: 0x092}
	interp := harness.Reg{Plane: "acq", Index: 0x094}
	start := harness.Reg{Plane: "acq", Index: 0x09A}
	split := harness.Reg{Plane: "map", Index: 0x10A}
	var out []harness.Result
	add := func(c string, ok bool, format string, a ...any) {
		if ok {
			out = append(out, harness.Pass(area, c, fmt.Sprintf(format, a...)))
		} else {
			out = append(out, harness.Fail(area, c, format, a...))
		}
	}
	add("log-zoom-banks", len(on) > 0, "the zoom on wrote the zoom banks and map sets (%d registers)", len(on))
	add("log-zoom-tdiv", len(tdiv) > 0 && !maps.Equal(tdiv, on), "the zoom banks rewritten for 10 µs/div (%d registers) differ from 1 µs/div's: samples %#x/%#x, interpolated %#x/%#x",
		len(tdiv), tdiv[samples], on[samples], tdiv[interp], on[interp])
	add("log-zoom-pan", len(pan) > 0 && pan[start] != tdiv[start], "the zoom window's record start after the pan %#x, before %#x", pan[start], tdiv[start])
	v, wrote := off[split]
	add("log-zoom-off", wrote && v == 0, "the zoom off wrote map 0x10A = %d (written %v)", v, wrote)
	return out
}

// tdivReply is the number of a TDIV? reply ("2.00E-06S", or with its
// header).
func tdivReply(r string) string {
	r = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(r), "TDIV"))
	return strings.TrimSuffix(r, "S")
}
