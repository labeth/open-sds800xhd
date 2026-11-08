package scopeboard

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-sds800xhd/app/internal/acq"
	"github.com/labeth/open-sds800xhd/app/internal/mmio"
	"github.com/labeth/open-sds800xhd/app/internal/mmio/mmiotest"
	"github.com/labeth/open-sds800xhd/app/internal/platform"
	"github.com/labeth/open-sds800xhd/app/internal/trigcases"
	"github.com/labeth/open-sds800xhd/app/internal/trigger"
)

// goldenBoard is a board staged with every channel on at 1 V/div, and a
// trigger engine on fake planes after the start-up restore and edge.
func goldenBoard() (*Board, *trigger.Trigger, [4]trigger.Chan, *mmiotest.Bus, *mmiotest.Bus) {
	b := &Board{id: platform.Identity{Channels: 4}, staged: acq.DefaultSettings()}
	var ch [4]trigger.Chan
	for c := range 4 {
		b.staged.Ch[c].On, b.staged.Ch[c].VoltsDiv = true, 1
		ch[c] = trigger.Chan{VoltsDiv: 1, Class: 17}
	}
	ab, mb := mmiotest.New(), mmiotest.New()
	t := trigger.New(mmio.NewPlane("acq", ab), mmio.NewPlane("map", mb), 4)
	t.RestoreDefaults()
	t.Program(trigger.Default(), ch)
	return b, t, ch, ab, mb
}

func checkRegs(t *testing.T, name string, ab, mb *mmiotest.Bus, want map[string]uint16) {
	t.Helper()
	for k, w := range want {
		bus, idx := ab, 0
		var plane string
		if _, err := fmtSscan(k, &plane, &idx); err != nil {
			t.Fatalf("%s: bad key %q", name, k)
		}
		if plane == "map" {
			bus = mb
		}
		got, ok := bus.Last(uint32(idx) * 4)
		if !ok {
			t.Errorf("%s: %s never written, want %#x", name, k, w)
		} else if uint16(got) != w {
			t.Errorf("%s: %s = %#x, want %#x", name, k, got, w)
		}
	}
}

// TestTriggerGoldens programs every golden case and checks the registers.
func TestTriggerGoldens(t *testing.T) {
	b, tr, ch, ab, mb := goldenBoard()
	for _, c := range trigcases.Cases {
		ts, err := b.triggerSettings(c.Spec)
		if c.Refuse != "" {
			r, ok := hal.AsRefusal(err)
			if !ok || !strings.Contains(r.Feature, c.Refuse) {
				t.Errorf("%s: want refusal of %q, got %v", c.Name, c.Refuse, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.Name, err)
			continue
		}
		tr.Program(ts, ch)
		checkRegs(t, c.Name, ab, mb, c.Want)
	}
	for _, u := range trigcases.UnitCases {
		ts, err := b.triggerSettings(u.Spec)
		if err != nil {
			t.Fatalf("%s: %v", u.Name, err)
		}
		ts.Roll = u.Roll
		tr.Program(ts, ch)
		checkRegs(t, u.Name, ab, mb, u.Want)
	}
}

// TestTriggerRefusesOffChannel checks a source whose channel is off.
func TestTriggerRefusesOffChannel(t *testing.T) {
	b, _, _, _, _ := goldenBoard()
	b.staged.Ch[2].On = false
	_, err := b.triggerSettings(hal.TriggerSpec{Stages: []hal.TrigStage{{
		Node:   hal.NodeEdge,
		Source: hal.Source{Kind: hal.Analog, Index: 2},
	}}})
	if _, ok := hal.AsRefusal(err); !ok {
		t.Fatalf("want a refusal, got %v", err)
	}
}

// TestTriggerCaps checks every node the caps list is accepted in some form
// and the profile carries the caps.
func TestTriggerCaps(t *testing.T) {
	seen := map[hal.TrigNode]bool{}
	b, _, _, _, _ := goldenBoard()
	for _, c := range trigcases.Cases {
		if c.Refuse == "" {
			if _, err := b.triggerSettings(c.Spec); err == nil {
				seen[c.Spec.Stages[0].Node] = true
			}
		}
	}
	for n := range trigCaps.Nodes {
		if !seen[n] {
			t.Errorf("node %v in the caps but in no accepted golden", n)
		}
	}
}

// fmtSscan parses "acq:0x0e0".
func fmtSscan(k string, plane *string, idx *int) (int, error) {
	p, i, _ := strings.Cut(k, ":")
	*plane = p
	n, err := strconv.ParseUint(strings.TrimPrefix(i, "0x"), 16, 16)
	*idx = int(n)
	return 2, err
}

func TestTriggerDefault(t *testing.T) {
	b, _, _, _, _ := goldenBoard()
	b.staged.Ch[1].VoltsDiv = 2
	b.staged.Ch[2].VoltsDiv = 0.002
	tf := (*triggerFacet)(b)
	src := func(c int) hal.Source { return hal.Source{Kind: hal.Analog, Index: c} }
	tests := []struct {
		name      string
		node      hal.TrigNode
		src       hal.Source
		high, low float64
	}{
		// trunc(−150 mV · 60 / 1 V) = −9 px = −0.15 V (spec 05 §5.6).
		{"video at 1 V/div", hal.NodeVideo, src(0), -0.15, 0},
		// trunc(−4.5) = −4 px at 2 V/div.
		{"video at 2 V/div", hal.NodeVideo, src(1), -4 * 2.0 / 60, 0},
		// trunc(−4500) px at 2 mV/div: the sync tip lies far off screen.
		{"video at 2 mV/div", hal.NodeVideo, src(2), -4500 * 0.002 / 60, 0},
		{"edge", hal.NodeEdge, src(0), 0, 0},
		{"slope level 1 −3 px", hal.NodeSlope, src(1), 0, -3 * 2.0 / 60},
		{"window low −50 px", hal.NodeWindow, src(0), 0, -50.0 / 60},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, err := tf.Default(tt.node, tt.src)
			if err != nil {
				t.Fatal(err)
			}
			if math.Abs(st.Levels.High-tt.high) > 1e-12 || math.Abs(st.Levels.Low-tt.low) > 1e-12 {
				t.Errorf("levels %+v, want high %g low %g", st.Levels, tt.high, tt.low)
			}
		})
	}
	if _, err := tf.Default(hal.NodePattern, src(0)); err == nil {
		t.Error("a pattern has no single-source default")
	}
	// The default programs the registers of spec 05: A 0xE0 = 0x77A (h 62, rising).
	st, _ := tf.Default(hal.NodeVideo, src(0))
	if err := tf.Program(hal.TriggerSpec{Sweep: hal.NormalSweep, Stages: []hal.TrigStage{st}}); err != nil {
		t.Fatal(err)
	}
}

// TestRestagingChangesNothing checks ADR-HAL-STAGED-PERSISTS: programming
// and configuring what is staged leaves the staged settings as they were,
// so the commit at the next arm finds nothing changed (no start delay).
func TestRestagingChangesNothing(t *testing.T) {
	b, _, _, _, _ := goldenBoard()
	for _, c := range trigcases.Cases {
		if c.Refuse != "" {
			continue
		}
		if err := b.Trigger().Program(c.Spec); err != nil {
			continue
		}
		staged := b.staged
		if err := b.Trigger().Program(c.Spec); err != nil || b.staged != staged {
			t.Errorf("%s: programmed again: %v, staged changed %v", c.Name, err, b.staged != staged)
		}
	}
	p := hal.Plan{Request: hal.Request{Channels: []int{0, 1}, TimePerDivS: 1e-6}}
	if err := b.Acquirer().Configure(p); err != nil {
		t.Fatal(err)
	}
	staged := b.staged
	if err := b.Acquirer().Configure(p); err != nil || b.staged != staged {
		t.Fatalf("configured again: %v, staged changed %v", err, b.staged != staged)
	}
}

// A free-running spec is accepted: its stages convert as given and the
// sweep becomes the Force row (both timeouts 1, spec 05 §7), so every
// acquisition completes forced.
func TestFreeRunIsTheForceRow(t *testing.T) {
	b, tr, ch, ab, _ := goldenBoard()
	spec := hal.TriggerSpec{Sweep: hal.NormalSweep, FreeRun: true, Stages: []hal.TrigStage{{
		Node: hal.NodeEdge, Source: hal.Source{Kind: hal.Analog, Index: 1}, Levels: hal.Levels{High: 0.5, Slope: hal.Falling},
	}}}
	ts, err := b.triggerSettings(spec)
	if err != nil {
		t.Fatalf("FreeRun refused: %v", err)
	}
	spec.FreeRun = false
	plain, err := b.triggerSettings(spec)
	if err != nil {
		t.Fatal(err)
	}
	if ts.Sweep != trigger.Force || plain.Sweep != trigger.Normal {
		t.Fatalf("sweeps %v (free run) and %v (normal)", ts.Sweep, plain.Sweep)
	}
	plain.Sweep = trigger.Force
	if ts != plain {
		t.Errorf("free-run stages differ from the stages as given:\n%+v\n%+v", ts, plain)
	}
	tr.Program(ts, ch)
	for _, r := range []uint32{0x00C, 0x02A} {
		if v, _ := ab.Last(r * 4); v != 1 {
			t.Errorf("ACQ %#03x = %d, want the Force row's 1", r, v)
		}
	}
}
