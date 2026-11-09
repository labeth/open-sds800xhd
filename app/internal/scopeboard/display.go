package scopeboard

import (
	"fmt"
	"image"
	"image/color"
	"math"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-sds800xhd/app/internal/acq"
	"github.com/labeth/open-sds800xhd/app/internal/display"
	"github.com/labeth/open-sds800xhd/app/internal/mapengine"
	"github.com/labeth/open-sds800xhd/app/internal/panel"
	"github.com/labeth/open-sds800xhd/app/internal/timebase"
)

// ui is the board's UI layer, capture and front panel, set up by bringUpUI.
type ui struct {
	fb      *display.FB
	capture *display.Capturer
	panel   *panel.Panel
	events  chan hal.InputEvent // the panel's events under the UI model's names
	names   names               // the panel's names: which channel the vertical knobs act on
}

// The device nodes of the UI layer and the capture buffer (spec 07 §2).
const (
	fb0 = "/dev/fb0"
	fb1 = "/dev/fb1"
)

// bringUpUI opens the UI framebuffer and the capture, initialises the
// beeper, starts the panel's readers and lights the power-on lamps (spec
// 08 §6.3). A part that does not open is logged and left absent.
func (b *Board) bringUpUI() {
	var err error
	if b.ui.fb, err = display.OpenFB(fb0); err != nil {
		b.log.Printf("UI layer unavailable: %v", err)
		b.ui.fb = nil
	}
	b.ui.capture = display.NewCapturer(b.regs.UIVDMA, b.regs.MAP, fb1)
	b.ui.panel = panel.New(b.eng.Map.LEDs, panel.NewBeeper(b.regs.TTC0))
	if err := b.ui.panel.Start(b.log.Printf); err != nil {
		b.log.Printf("front panel unavailable: %v", err)
	}
	b.ui.events = make(chan hal.InputEvent, 64)
	go b.ui.names.forward(b.ui.panel.Events(), b.ui.events)
	lit := []string{"auto", "run", "touch"}
	for c := range b.id.Channels {
		if b.staged.Ch[c].On {
			lit = append(lit, fmt.Sprintf("ch%d", c+1))
			break
		}
	}
	_ = b.ui.panel.SetLamps(lit)
}

type displayFacet Board

func (d *displayFacet) Spec() hal.DisplaySpec { return (*Board)(d).Profile().Display }

// Framebuffer is the UI layer, /dev/fb0 (ARGB8888, alpha 0 where the
// waveform layer shows through); nil before bring-up or when it did not
// open.
func (d *displayFacet) Framebuffer() hal.Framebuffer {
	if fb := (*Board)(d).ui.fb; fb != nil {
		return fb
	}
	return nil
}

// Renderer is the PL map engine. The open image has none (spec 20 §9):
// the PS draws the traces, and the map engine's held-record redraw would
// poll vendor registers the open plane never sets.
func (d *displayFacet) Renderer() (hal.Renderer, bool) {
	if (*Board)(d).open != nil {
		return nil, false
	}
	return (*rendererFacet)(d), true
}

// Capture copies the PL's capture of the mixed picture (spec 07 §5).
func (d *displayFacet) Capture(dst *image.RGBA) error {
	b := (*Board)(d)
	if b.ui.capture == nil {
		return hal.Refuse("screen capture", "the board is not brought up")
	}
	return b.ui.capture.Capture(dst)
}

// Backlight sets the LCD brightness at once (spec 07 §6); 0 switches the
// panel off.
func (d *displayFacet) Backlight(level float64) error {
	b := (*Board)(d)
	pct := int(max(0, min(1, level))*100 + 0.5)
	if level > 0 {
		b.staged.Display.BacklightPct = pct
	}
	if b.eng != nil {
		b.eng.Map.Backlight(pct, level > 0)
	}
	return nil
}

type rendererFacet Board

// relEq reports whether a and b agree to 0.1 % of scale.
func relEq(a, b, scale float64) bool { return math.Abs(a-b) <= 1e-3*math.Abs(scale) }

// Configure stages the view; the acquisition commit (Acquirer.Arm) writes
// it. The PL draws the acquisition's window: a live main window other than
// the staged timebase is refused. A held view (View.Held) redraws the
// stored record at once at the view's window with the staged vertical
// settings (spec 03 §3.7). Vertical placement follows the front end's
// offsets, so the main view's OffsetDiv is not used.
//
// The zoom window's OffsetDiv and Gain, indexed by channel number, are
// where each channel's zoom window centres (main-screen divisions above the
// main screen's centre) and its vertical zoom; the zoom banks' resize
// carries the gain (spec 02 §4.2). The PL draws YT, XY and YT+XY, not a
// split screen.
func (r *rendererFacet) Configure(v hal.View) error {
	b := (*Board)(r)
	if v.Held {
		return b.redraw(v)
	}
	for c, g := range v.Gain {
		if g != 0 && g != 1 {
			return hal.Refuse("view", fmt.Sprintf("a main-view gain of %g for C%d: the map engine draws the main view at the front end's scale", g, c+1))
		}
	}
	var xy bool
	switch v.Mode {
	case hal.DisplayYT:
	case hal.DisplayXY, hal.DisplayYTXY:
		xy = true
	case hal.DisplaySplit:
		return hal.Refuse("split display", "the map engine draws every channel on one grid")
	default:
		return hal.Refuse("display mode", "not a display mode of this scope")
	}
	s := &b.staged
	tdiv := timebase.TimeDiv(s.Enum)
	if v.SpanS != 0 {
		if !relEq(v.SpanS, 10*tdiv, tdiv) {
			return hal.Refuse("view", fmt.Sprintf("the map engine draws the acquisition window of %g s, not %g s: change the timebase", 10*tdiv, v.SpanS))
		}
		if !relEq(v.StartS, s.DelayS-5*tdiv, tdiv) {
			return hal.Refuse("view", "the map engine draws the acquisition window: move the horizontal position instead")
		}
	}
	var view acq.View
	acquired := s.Mask(b.id.Channels)
	if len(v.Channels) > 0 {
		shown := 0
		for _, c := range v.Channels {
			if c < 0 || c >= b.id.Channels || acquired&(1<<c) == 0 {
				return hal.Refuse("view", fmt.Sprintf("C%d is not acquired", c+1))
			}
			shown |= 1 << c
		}
		view.Hidden = uint8(acquired &^ shown) //nolint:gosec // G115: a channel mask of at most 4 channels
	}
	for i, c := range v.Selected {
		if i < len(view.Selected) && c >= 0 && c < b.id.Channels {
			view.Selected[i] = int8(c + 1) //nolint:gosec // G115: c is a channel of this model (at most 4)
		}
	}
	if v.Zoom != nil {
		if err := b.zoomView(&view, v.Zoom, xy, tdiv, acquired); err != nil {
			return err
		}
	}
	if xy {
		if acquired&3 != 3 && acquired&0xC != 0xC {
			return hal.Refuse("XY display", "needs C1 and C2, or C3 and C4, acquired")
		}
		view.XY = true
		view.YTXY = v.Mode == hal.DisplayYTXY
	}
	s.View = view
	return nil
}

// zoomView checks the zoom window z against the staged acquisition and
// sets it on view.
func (b *Board) zoomView(view *acq.View, z *hal.View, xy bool, tdiv float64, acquired int) error {
	s := &b.staged
	switch {
	case xy:
		return hal.Refuse("zoom", "the zoom window and the XY display do not run together")
	case z.Zoom != nil || z.Mode != hal.DisplayYT:
		return hal.Refuse("zoom", "a zoom window is a time window of the main view")
	case z.SpanS <= 0 || z.SpanS > 10*tdiv*(1+1e-3):
		return hal.Refuse("zoom", "the zoom window must be inside the main window's time span")
	}
	h := timebase.Compute(timebase.Config{
		Enum: s.Enum, Depth: s.Depth, Mask: acquired,
		Limited: b.id.BandwidthMHz < 200, Peak: s.Peak,
	})
	if _, ok := acq.ZoomOf(h, tdiv, z.SpanS/10, 2); !ok {
		return hal.Refuse("zoom", "fewer than 4 samples in the zoom window (spec 04 §11)")
	}
	view.Zoom, view.ZoomTdivS = true, z.SpanS/10
	view.ZoomDelayS = z.StartS + z.SpanS/2 - s.DelayS
	for c, d := range z.OffsetDiv {
		if c >= b.id.Channels {
			return hal.Refuse("zoom", fmt.Sprintf("zoom centre for C%d: the model has %d channels", c+1, b.id.Channels))
		}
		if math.Abs(d) > 4 {
			return hal.Refuse("zoom", fmt.Sprintf("zoom centre %g div for C%d is off the screen (±4 div)", d, c+1))
		}
		view.ZoomCentreDiv[c] = d
	}
	for c, g := range z.Gain {
		if c >= b.id.Channels {
			return hal.Refuse("zoom gain", fmt.Sprintf("a gain for C%d: the model has %d channels", c+1, b.id.Channels))
		}
		if g < 0 || g > maxZoomGain || math.IsNaN(g) {
			return hal.Refuse("zoom gain", fmt.Sprintf("%g for C%d: the zoom gain is 0 (unity) or up to %d", g, c+1, maxZoomGain))
		}
		view.ZoomGain[c] = g
	}
	return nil
}

// maxZoomGain is the largest vertical zoom gain accepted: the zoom window
// still shows 1/maxZoomGain of a division of the main screen.
const maxZoomGain = 100

// redraw is a held view: the stored record redrawn by the PL at the view's
// window with the staged vertical settings, while stopped (spec 03 §3.7).
// The staged acquisition settings are unchanged; the next Arm applies them.
func (b *Board) redraw(v hal.View) error {
	switch {
	case b.eng == nil || !b.stored:
		return hal.Refuse("held view", "no stored record to redraw")
	case b.state == hal.Armed:
		return hal.Refuse("held view", "the acquisition is running: a held record is redrawn only while stopped")
	case v.Mode != hal.DisplayYT || v.Zoom != nil && !b.eng.Settings().View.Zoom:
		return hal.Refuse("held view", "a held record is redrawn in the display it was taken in (no XY, no new zoom window)")
	}
	tdiv, delay := timebase.TimeDiv(b.staged.Enum), b.staged.DelayS
	if v.SpanS > 0 {
		tdiv, delay = v.SpanS/10, v.StartS+v.SpanS/2
	}
	if err := b.eng.Redraw(tdiv, delay, b.staged.Ch); err != nil {
		return hal.Refuse("held view", err.Error())
	}
	return nil
}

// SetAppearance stages vectors, intensity, colour grade and persistence
// (spec 07 §7.1, §7.2); the acquisition commit writes them.
func (r *rendererFacet) SetAppearance(a hal.Appearance) error {
	b := (*Board)(r)
	p, ok := mapengine.PersistenceOf(a.PersistenceS)
	if !ok {
		return hal.Refuse("persistence", fmt.Sprintf("%g s: this model offers off, infinite, 1, 5, 10 and 30 s", a.PersistenceS))
	}
	b.staged.Display.Vectors = a.Vectors
	b.staged.Display.ColorGrade = a.ColorGrade
	b.staged.Display.IntensityPct = int(max(0, min(1, a.Intensity))*100 + 0.5)
	b.staged.Display.Persistence = p
	return nil
}

func (r *rendererFacet) Area() image.Rectangle { return waveArea }

func (r *rendererFacet) Colors([]color.RGBA) error {
	return hal.Refuse("trace colours", "fixed in the PL on this model (spec 07 §7.1)")
}
