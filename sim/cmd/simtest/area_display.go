package main

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"

	"github.com/labeth/open-sds800xhd/sim/harness"
)

// uiPattern is the picture the in-guest TestDisplay draws into fb0
// (app/internal/boardtest/display_test.go has the same function).
func uiPattern(x, y int) (a, r, g, b uint8) {
	if x >= 19 && x <= 1018 && y >= 50 && y <= 529 {
		if (x-19)%100 == 0 {
			return 0x80, 227, 227, 227
		}
		return 0, 0, 0, 0
	}
	return 0xFF, uint8(x), uint8(y), uint8(x>>8 | y>>8<<2 | 0x40) //nolint:gosec // G115: the low bytes of the coordinates, as the device model's test pattern packs them
}

// The display area runs the board's display test in the guest (the UI
// layer, the renderer and the capture through the HAL), then the app,
// whose host side zooms and pans through the UI model and changes the
// timebase while the device model's log records what reached the map
// engine, and reads /api/screen.png against fb0.
func init() {
	// the board test checks registers, not firing (trigfire's): on idle
	// inputs a modelled trigger would never close its records
	board := boardArea("display", "", "Display", 120, "-no-trig-model", []string{"@30 shotfb 0 display-fb0.png"}, checkFB0)
	register(area{
		name: "display", title: "display: UI layer, renderer, capture; the UI's zoom and timebase drive the PL; screen.png",
		runs: func(e *env) []harness.Spec {
			return append(board.runs(e), harness.Spec{
				Name: e.prefix + "display-app", Secs: 180, Ports: true, SimdArgs: signals,
				App: e.app, Hook: e.hook, GuestBoot: e.guestBoot,
				Host: func(ctx context.Context, dir string) { displayHost(ctx, dir) },
			})
		},
		check: func(e *env, runs []*harness.Run) []harness.Result {
			res := board.check(e, runs[:1])
			res = append(res, hostResultsOf(runs[1], "display", displayHostFile)...)
			return append(res, checkViewLog(runs[1])...)
		},
	})
}

// checkFB0 compares the simulator's dump of fb0 with the pattern, every
// pixel and its alpha byte.
func checkFB0(_ *env, r *harness.Run) []harness.Result {
	const c = "fb0 dump"
	f, err := os.Open(r.File("shots/display-fb0.png"))
	if err != nil {
		return []harness.Result{harness.Fail("display", c, "%v", err)}
	}
	defer func() { _ = f.Close() }() // read only: nothing to flush
	img, err := png.Decode(f)
	if err != nil {
		return []harness.Result{harness.Fail("display", c, "%v", err)}
	}
	n, ok := img.(*image.NRGBA)
	if !ok || n.Rect.Dx() != 1024 || n.Rect.Dy() != 600 {
		return []harness.Result{harness.Fail("display", c, "dump is %T %v, want 1024x600 RGBA", img, img.Bounds())}
	}
	bad, first := 0, ""
	for y := range 600 {
		for x := range 1024 {
			a, rr, g, b := uiPattern(x, y)
			p := n.NRGBAAt(x, y)
			if p.A != a || p.R != rr || p.G != g || p.B != b {
				if bad == 0 {
					first = fmt.Sprintf(" first at (%d,%d): %v, want %d/%d/%d/%d", x, y, p, rr, g, b, a)
				}
				bad++
			}
		}
	}
	if bad > 0 {
		return []harness.Result{harness.Fail("display", c, "%d of 614400 pixels differ;%s", bad, first)}
	}
	return []harness.Result{harness.Pass("display", c, "all 614400 pixels and alpha bytes as drawn")}
}
