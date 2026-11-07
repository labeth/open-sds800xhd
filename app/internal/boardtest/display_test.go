//go:build simboard

package boardtest

import (
	"image"
	"testing"
	"time"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-sds800xhd/app/internal/display"
)

// holdFB is how long the pattern stays in fb0 after the cases, for the
// harness's dump (scenario line "shotfb 0").
const holdFB = 25 * time.Second

// uiPattern is the picture TestDisplay draws into fb0; the harness checks
// the simulator's dump of fb0 against the same function (sim/cmd/simtest).
// The graticule interior is transparent (alpha 0) with a grid line every
// 100 columns at alpha 0x80; the rest is opaque and position-coded.
func uiPattern(x, y int) uint32 {
	if x >= 19 && x <= 1018 && y >= 50 && y <= 529 {
		if (x-19)%100 == 0 {
			return display.ARGB(0x80, 227, 227, 227)
		}
		return 0
	}
	return display.ARGB(0xFF, uint8(x), uint8(y), uint8(x>>8|y>>8<<2|0x40))
}

// stage plans and configures an acquisition of channels at tdiv, then
// commits it with an arm and stops.
func commit(t *testing.T, c *tcase) bool {
	t.Helper()
	a := board.Acquirer()
	if !c.noErr(a.Arm(), "arm commits the staged settings") {
		return false
	}
	return c.noErr(a.Stop(), "stop")
}

func configure(t *testing.T, c *tcase, channels []int, tdiv float64) bool {
	t.Helper()
	for ch := range board.Profile().Channels {
		on := false
		for _, x := range channels {
			on = on || x == ch
		}
		got := board.FrontEnd().Get(ch)
		got.On = on
		if _, err := board.FrontEnd().Set(ch, got); err != nil {
			return c.noErr(err, "front end")
		}
	}
	p, err := board.Planner().Plan(hal.Request{Channels: channels, TimePerDivS: tdiv})
	if err != nil {
		return c.noErr(err, "plan")
	}
	return c.noErr(board.Acquirer().Configure(p), "configure")
}

// TestDisplay drives the UI layer, the renderer and the capture.
func TestDisplay(t *testing.T) {
	d := board.Display()

	c := begin(t, "display/framebuffer")
	fb := d.Framebuffer()
	if c.check(fb != nil, "fb0 mapped") {
		back := fb.Back()
		for y := range display.H {
			for x := range display.W {
				display.Put(back, x, y, uiPattern(x, y))
			}
		}
		c.check(fb.Stride() == 4096, "stride %d", fb.Stride())
		c.noErr(fb.Present(), "present the pattern")
	}

	c = begin(t, "display/capture")
	img := image.NewRGBA(image.Rect(0, 0, display.W, display.H))
	if c.noErr(d.Capture(img), "capture") {
		// The capture is RGB565 of the mixed picture; the simulator mixes
		// no waveform layer, so the opaque UI pixels come back quantised.
		bad, n := 0, 0
		for y := 0; y < display.H; y += 7 {
			for x := 0; x < display.W; x += 13 {
				p := uiPattern(x, y)
				if p>>24 != 0xFF {
					continue
				}
				n++
				q := img.RGBAAt(x, y)
				if q.R>>3 != uint8(p>>16)>>3 || q.G>>2 != uint8(p>>8)>>2 || q.B>>3 != uint8(p)>>3 {
					bad++
				}
			}
		}
		c.check(bad == 0, "%d of %d opaque sample pixels captured as drawn", n-bad, n)
	}

	c = begin(t, "display/backlight")
	c.noErr(d.Backlight(0.5), "backlight 50 %")
	c.expect(map[string]uint16{"map:0x14d": 1099, "map:0x016": 1})

	r, ok := d.Renderer()
	c = begin(t, "display/appearance")
	if !c.check(ok, "the PL renderer") {
		return
	}
	configure(t, c, []int{0}, 1e-6)
	c.noErr(r.SetAppearance(hal.Appearance{Intensity: 0.8, ColorGrade: true, PersistenceS: 10, Vectors: false}), "appearance")
	commit(t, c)
	c.expect(map[string]uint16{"map:0x0fe": 96, "map:0x0e7": 1, "map:0x125": 0, "map:0x080": 0, "map:0x140": 1, "map:0x144": 10})

	c = begin(t, "display/persistence-off")
	c.noErr(r.SetAppearance(hal.Appearance{Intensity: 0.5, Vectors: true}), "appearance")
	commit(t, c)
	c.expect(map[string]uint16{"map:0x0fe": 66, "map:0x0e7": 0, "map:0x125": 1, "map:0x080": 3, "map:0x140": 0})

	c = begin(t, "display/zoom")
	tdiv := 1e-6
	c.noErr(r.Configure(hal.View{Channels: []int{0}, Zoom: &hal.View{StartS: -0.5e-6, SpanS: 1e-6}}), "zoom 100 ns/div under 1 µs/div")
	commit(t, c)
	c.expect(map[string]uint16{
		"map:0x10a": 1, "map:0x091": 460, "map:0x092": 115, "map:0x093": 20,
		"map:0x061": 250, "map:0x1bd": 0x168, "acq:0x097": 1,
	})

	c = begin(t, "display/zoom-off")
	c.noErr(r.Configure(hal.View{Channels: []int{0}, StartS: -5 * tdiv, SpanS: 10 * tdiv}), "main window only")
	commit(t, c)
	c.expect(map[string]uint16{"map:0x10a": 0, "map:0x091": 480, "map:0x092": 480, "map:0x093": 0, "map:0x1bd": 0})

	c = begin(t, "display/zoom-gain")
	n := board.Profile().Channels
	gains, centres := make([]float64, n), make([]float64, n)
	for i := range n {
		gains[i], centres[i] = 2, 1
	}
	c.refused(r.Configure(hal.View{Channels: []int{0}, Zoom: &hal.View{StartS: -0.5e-6, SpanS: 1e-6, Gain: []float64{-1}}}),
		"a negative zoom gain")
	c.refused(r.Configure(hal.View{Channels: []int{0}, Gain: []float64{2}}), "a gain on the main view")
	c.noErr(r.Configure(hal.View{Channels: []int{0}, Zoom: &hal.View{StartS: -0.5e-6, SpanS: 1e-6, OffsetDiv: centres, Gain: gains}}),
		"zoom 100 ns/div, gain 2, centred 1 div above the centre")
	commit(t, c)
	// round(16384·2) and −2·1·480 = −960 in the zoom banks, written after the main banks' 0x4000/0.
	c.expect(map[string]uint16{
		"acq:0x1e1": 0x8000, "acq:0x1e2": 0, "acq:0x1df": 0xfc40, "acq:0x1e0": 0xffff,
		"acq:0x1e3": 0x080, "acq:0x1e4": 0xf7f, "map:0x10a": 1,
	})
	c = begin(t, "display/zoom-gain-off")
	c.noErr(r.Configure(hal.View{Channels: []int{0}}), "zoom off")
	commit(t, c)

	c = begin(t, "display/yt-xy")
	configure(t, c, []int{0, 1}, 200e-9)
	c.noErr(r.Configure(hal.View{Mode: hal.DisplayYTXY, StartS: -1e-6, SpanS: 2e-6}), "YT+XY display of C1 and C2")
	commit(t, c)
	// Sets 4 + n carry the XY variant (S_xy = 4000: XY length 2000); the
	// YT pixel mapping stays.
	c.expect(map[string]uint16{
		"map:0x192": 0x10, "acq:0x198": 0x10, "acq:0x199": 1, "acq:0x095": 1, "acq:0x09f": 1,
		"map:0x199": 0x7d0, "map:0x0c4": 1000,
	})
	c = begin(t, "display/yt-xy-off")
	c.noErr(r.Configure(hal.View{}), "YT display")
	commit(t, c)
	c.expect(map[string]uint16{"map:0x192": 0, "acq:0x198": 0})

	heldRedraw(t, r)

	c = begin(t, "display/xy")
	configure(t, c, []int{0, 1}, 200e-9)
	c.noErr(r.Configure(hal.View{Mode: hal.DisplayXY}), "XY display of C1 and C2")
	commit(t, c)
	c.expect(map[string]uint16{
		"map:0x192": 1, "acq:0x198": 1, "acq:0x199": 1,
		"map:0x0c4": 0x1e0, "map:0x0c3": 0x1e0, "map:0x0c5": 0x138,
	})

	c = begin(t, "display/xy-off")
	c.noErr(r.Configure(hal.View{}), "YT display")
	commit(t, c)
	c.expect(map[string]uint16{"map:0x192": 0, "acq:0x198": 0, "map:0x0c4": 1000, "map:0x0c3": 1000, "map:0x0c5": 0xa0})

	c = begin(t, "display/refusals")
	c.refused(r.Colors(nil), "trace colours")
	c.refused(r.SetAppearance(hal.Appearance{PersistenceS: 2}), "persistence 2 s")
	c.refused(r.Configure(hal.View{Channels: []int{3}}), "a channel not acquired")
	c.refused(r.Configure(hal.View{SpanS: 1, StartS: -0.5}), "a window other than the acquisition's")
	configure(t, c, []int{0}, 1e-6)
	c.refused(r.Configure(hal.View{Mode: hal.DisplayXY}), "XY with one channel")
	c.refused(r.Configure(hal.View{Mode: hal.DisplaySplit}), "a split screen")

	// The harness dumps fb0 while the pattern is still there.
	c = begin(t, "display/hold")
	time.Sleep(holdFB)
	c.check(true, "fb0 held %v for the dump", holdFB)
}

// heldRedraw takes a 1 ch record at 200 ns/div, 10k (2 GSa/s, 4000 screen
// samples), stops and redraws it through the PL renderer (spec 03 §3.7,
// spec 04 §12, spec 07 §7.8), then resumes.
func heldRedraw(t *testing.T, r hal.Renderer) {
	t.Helper()
	a := board.Acquirer()
	c := begin(t, "display/held-redraw")
	if _, err := setup(t, []int{0}, hal.Request{TimePerDivS: 200e-9, Depth: 10_000}); !c.noErr(err, "stage 1 ch 200 ns/div 10k") {
		return
	}
	if _, ok := acquire(c); !ok {
		return
	}
	c.noErr(a.Stop(), "stop: the record stays held")
	c.noErr(r.Configure(hal.View{Held: true, Channels: []int{0}, StartS: -5e-6, SpanS: 10e-6}), "redraw the held record at 1 µs/div")
	// The example of spec 04 §12 / spec 07 §7.8.
	c.expect(map[string]uint16{
		"acq:0x098": 1, "acq:0x092": 0x408, "acq:0x094": 0x3e8, "acq:0x10e": 0x430,
		"acq:0x01a": 0x20, "acq:0x09a": 2, "acq:0x08c": 4, "acq:0x09e": 0x24, "acq:0x118": 1, "acq:0x010": 0x8000,
		"acq:0x1e1": 0x4000, "acq:0x1df": 0, "acq:0x115": 1,
		"map:0x19b": 0x190, "map:0x19a": 0xc8, "map:0x1a7": 1, "map:0x1ae": 0x190, "map:0x1af": 0x258,
		"map:0x153": 0x320, "map:0x13e": 5, "map:0x172": 0, "map:0x174": 0,
	})

	// Every channel gets the change: the harness checks the last value
	// written, which is the last bank's.
	c = begin(t, "display/held-vdiv")
	if c.noErr(stageAll(func(ch *hal.Channel) { ch.VoltsDiv = 0.5 }), "stage every channel at 0.5 V/div while stopped") {
		c.noErr(r.Configure(hal.View{Held: true, Channels: []int{0}, StartS: -1e-6, SpanS: 2e-6}), "redraw at 200 ns/div")
		// κ = 1 V/div ÷ 0.5 V/div = 2 (spec 04 §12).
		c.expect(map[string]uint16{"acq:0x1e1": 0x8000, "acq:0x1df": 0, "acq:0x115": 1})
	}

	c = begin(t, "display/held-offset")
	if c.noErr(stageAll(func(ch *hal.Channel) { ch.VoltsDiv, ch.OffsetV = 1, 0.4 }), "stage every channel at 1 V/div, offset +0.4 V") {
		c.noErr(r.Configure(hal.View{Held: true, Channels: []int{0}, StartS: -1e-6, SpanS: 2e-6}), "redraw at 200 ns/div")
		// +0.4 div = 192 codes (spec 04 §12).
		c.expect(map[string]uint16{"acq:0x1e1": 0x4000, "acq:0x1df": 0xc0, "acq:0x115": 1})
	}

	c = begin(t, "display/held-refusals")
	c.refused(r.Configure(hal.View{Held: true, Channels: []int{0}, StartS: -5e-12, SpanS: 1e-11}), "a held view with fewer than 4 record samples")
	c.refused(r.Configure(hal.View{Held: true, Mode: hal.DisplayXY}), "a held XY view")

	c = begin(t, "display/held-resume")
	_ = stageAll(func(ch *hal.Channel) { ch.OffsetV = 0 })
	c.noErr(r.Configure(hal.View{Channels: []int{0}}), "live view")
	if _, ok := acquire(c); ok {
		// The arm rewrote the live set: stored search off, full-range
		// resize, the live map window, draw stop released.
		c.expect(map[string]uint16{
			"acq:0x115": 0, "acq:0x118": 0, "acq:0x1e1": 0x4000, "acq:0x1df": 0,
			"map:0x1ae": 0, "map:0x1af": 1000, "map:0x19b": 0x190, "map:0x19a": 2,
		})
	}
	if c.noErr(a.Arm(), "arm") {
		c.refused(r.Configure(hal.View{Held: true}), "a held view while armed")
	}
	_ = a.Stop()
	c.refused(r.Configure(hal.View{Held: true}), "a held view after a stop mid-frame (no stored record)")
}

// stageAll stages f's change on every channel's applied setting.
func stageAll(f func(ch *hal.Channel)) error {
	fe := board.FrontEnd()
	for i := range board.Profile().Channels {
		ch := fe.Get(i)
		f(&ch)
		if _, err := fe.Set(i, ch); err != nil {
			return err
		}
	}
	return nil
}
