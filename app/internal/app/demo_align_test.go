package app

import (
	"os"
	"slices"
	"testing"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-dso-core/profile"
	"github.com/labeth/open-dso-core/scopeapp"
	"github.com/labeth/open-dso-core/simdemo"
	"github.com/labeth/open-dso-core/simdemo/demotest"
	"github.com/labeth/open-sds800xhd/app/internal/frontend"
	"github.com/labeth/open-sds800xhd/app/internal/platform"
	"github.com/labeth/open-sds800xhd/app/internal/scopeboard"
)

// The browser demo's files of this scope.
const (
	demoFile    = "../../../demo/demo.json"
	demoProfile = "../../../demo/profile.yaml"
)

// demoFiles are the demo's profile and demo file.
func demoFiles(t *testing.T) (profile, demo []byte) {
	t.Helper()
	profile, err := os.ReadFile(demoProfile)
	if err != nil {
		t.Fatal(err)
	}
	demo, err = os.ReadFile(demoFile)
	if err != nil {
		t.Fatal(err)
	}
	return profile, demo
}

// The core keeps this scope's device composition, which its browser demo
// is compared with (open-dso-core simdemo.TestCompositionsAgree): the
// options the app runs with.
func TestDemoComposition(t *testing.T) {
	profile, demo := demoFiles(t)
	log := &platform.Log{}
	demotest.Composition(t, "open-sds800xhd", profile, demo, func(b hal.Board) scopeapp.Options {
		return options(b, log, "0.0.0.0")
	})
}

// The browser demo's front panel is this panel: its key table is the
// board's, its layout places every key and knob, and the demo's profile
// lists what they send, as the board's own profile does.
func TestDemoPanel(t *testing.T) {
	tab := scopeboard.KeyTable()
	demotest.Panel(t, demoFile, demoInputs(t), tab)
	keys, knobs := tab.Names()
	in := scopeboard.New(nil).Profile().Inputs
	if !slices.Equal(in.Keys, keys) || !slices.Equal(in.Knobs, knobs) {
		t.Errorf("the board's profile lists keys %v knobs %v, its key table sends %v %v", in.Keys, in.Knobs, keys, knobs)
	}
}

// The demo's profile offers the probe factors and bandwidth limits the
// board's does.
func TestDemoVertical(t *testing.T) {
	profile, demo := demoFiles(t)
	d, err := simdemo.ParseDemo(demo)
	if err != nil {
		t.Fatal(err)
	}
	b, err := simdemo.Board(profile, d, simdemo.Options{})
	if err != nil {
		t.Fatal(err)
	}
	v := b.Profile().Vertical
	if !slices.Equal(v.Probes, frontend.Probes()) || !slices.Equal(v.BWLimits, frontend.BWLimits()) {
		t.Errorf("the demo's profile offers %+v, the front end %v %v", v, frontend.Probes(), frontend.BWLimits())
	}
}

// demoInputs are the demo profile's inputs.
func demoInputs(t *testing.T) hal.InputSpec {
	t.Helper()
	raw, demo := demoFiles(t)
	d, err := simdemo.ParseDemo(demo)
	if err != nil {
		t.Fatal(err)
	}
	p, err := profile.Load(raw, hal.Identity{Vendor: d.Identity.Vendor, Model: d.Identity.Model})
	if err != nil {
		t.Fatal(err)
	}
	return p.Inputs
}
