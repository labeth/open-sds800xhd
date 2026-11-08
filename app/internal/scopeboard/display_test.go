package scopeboard

import (
	"testing"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-sds800xhd/app/internal/mapengine"
	"github.com/labeth/open-sds800xhd/app/internal/timebase"
)

func testBoard() *Board {
	b := New(nil)
	b.id.Channels, b.id.BandwidthMHz = 4, 200
	b.staged.Ch[1].On = true // C1 and C2 acquired
	return b
}

func refused(t *testing.T, err error, what string) {
	t.Helper()
	if _, ok := hal.AsRefusal(err); !ok {
		t.Errorf("%s: want a refusal, got %v", what, err)
	}
}

func TestConfigureView(t *testing.T) {
	b := testBoard()
	r := (*rendererFacet)(b)
	tdiv := timebase.TimeDiv(b.staged.Enum)
	main := hal.View{Channels: []int{1}, Selected: []int{1, 0}, StartS: -5 * tdiv, SpanS: 10 * tdiv}
	if err := r.Configure(main); err != nil {
		t.Fatal(err)
	}
	if v := b.staged.View; v.Hidden != 1 || v.Selected != [4]int8{2, 1} || v.Zoom || v.XY {
		t.Errorf("staged view %+v", v)
	}
	z := main
	z.Zoom = &hal.View{StartS: -tdiv / 2, SpanS: tdiv}
	if err := r.Configure(z); err != nil {
		t.Fatal(err)
	}
	if v := b.staged.View; !v.Zoom || v.ZoomTdivS != tdiv/10 || v.ZoomDelayS != 0 {
		t.Errorf("zoom view %+v", v)
	}
	xy := hal.View{Mode: hal.DisplayXY}
	if err := r.Configure(xy); err != nil || !b.staged.View.XY || b.staged.View.YTXY {
		t.Errorf("XY on C1/C2: %v", err)
	}
	if err := r.Configure(hal.View{Mode: hal.DisplayYTXY}); err != nil || !b.staged.View.YTXY {
		t.Errorf("YT+XY on C1/C2: %v", err)
	}
	zg := main
	zg.Zoom = &hal.View{StartS: -tdiv / 2, SpanS: tdiv, Gain: []float64{0, 2}}
	if err := r.Configure(zg); err != nil || b.staged.View.ZoomGain != [4]float64{0, 2} {
		t.Errorf("zoom gain: %v, staged %v", err, b.staged.View.ZoomGain)
	}
	refused(t, r.Configure(hal.View{Mode: hal.DisplaySplit}), "a split screen")
	refused(t, r.Configure(hal.View{Gain: []float64{2}}), "a main-view gain")

	refused(t, r.Configure(hal.View{SpanS: 20 * tdiv, StartS: -10 * tdiv}), "a window other than the acquisition's")
	refused(t, r.Configure(hal.View{Channels: []int{2}}), "a channel not acquired")
	refused(t, r.Configure(hal.View{Zoom: &hal.View{SpanS: 20 * tdiv}}), "a zoom wider than the main window")
	refused(t, r.Configure(hal.View{Zoom: &hal.View{SpanS: 1e-12}}), "a zoom of fewer than 4 samples")
	refused(t, r.Configure(hal.View{Mode: hal.DisplayXY, Zoom: &hal.View{SpanS: tdiv}}), "zoom with XY")
	b.staged.Ch[1].On = false
	refused(t, r.Configure(xy), "XY without a channel pair")
}

func TestSetAppearance(t *testing.T) {
	b := testBoard()
	r := (*rendererFacet)(b)
	if err := r.SetAppearance(hal.Appearance{Intensity: 0.8, ColorGrade: true, PersistenceS: -1, Vectors: false}); err != nil {
		t.Fatal(err)
	}
	d := b.staged.Display
	if d.IntensityPct != 80 || !d.ColorGrade || d.Vectors || d.Persistence != mapengine.PersistInfinite {
		t.Errorf("staged %+v", d)
	}
	refused(t, r.SetAppearance(hal.Appearance{PersistenceS: 2}), "a persistence of 2 s")
	refused(t, r.Colors(nil), "trace colours")
}

// The vendor image's map engine is the renderer; the open image has none
// (spec 20 §9), so the core draws the traces and sends no held views.
func TestRendererOnOpenImage(t *testing.T) {
	b := testBoard()
	if r, ok := (*displayFacet)(b).Renderer(); !ok || r == nil {
		t.Error("vendor image: no renderer")
	}
	b.open = &openBackend{}
	if r, ok := (*displayFacet)(b).Renderer(); ok || r != nil {
		t.Error("open image: a renderer is offered")
	}
}
