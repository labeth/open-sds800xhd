package scopeboard

import (
	"testing"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-dso-core/sample"
	"github.com/labeth/open-sds800xhd/app/internal/platform"
)

func TestPlanPeakDetect(t *testing.T) {
	b := &Board{id: platform.Identity{Channels: 4, BandwidthMHz: 200}}
	tests := []struct {
		name  string
		tdiv  float64
		depth int
		pairs bool
	}{
		// 1 ch 10k 1 ms/div: 1 MSa/s, DECIM_TYPE 14 (spec 04 §5.2 example).
		{"decimated", 1e-3, 10_000, true},
		// 2 GSa/s: peak detect has no effect.
		{"full rate", 1e-6, 10_000, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := b.Planner().Plan(hal.Request{Channels: []int{0}, TimePerDivS: tt.tdiv, Depth: tt.depth, Mode: hal.PeakDetect})
			if err != nil {
				t.Fatal(err)
			}
			c := p.Content
			if c.Has(sample.PeakPairs) != tt.pairs || c.Has(sample.RawFullRate) == tt.pairs {
				t.Errorf("content %v, want peak pairs %v", c, tt.pairs)
			}
			if p.Mode != hal.PeakDetect {
				t.Errorf("mode %v", p.Mode)
			}
			if !tt.pairs && p.Note == "" {
				t.Error("no note that peak detect has no effect")
			}
		})
	}
}

// Every optional part the board lacks has the board's own reason.
func TestAbsentPartsHaveReasons(t *testing.T) {
	b := &Board{id: platform.Identity{Channels: 4, BandwidthMHz: 200}}
	p := b.Profile()
	present := map[string]bool{}
	_, present["streamer"] = b.Streamer()
	_, present["images"] = b.Images()
	_, present["stacker"] = b.Stacker()
	_, present["segments"] = b.Segments()
	_, present["counter"] = b.Counter()
	_, present["logic-probe"] = b.LogicProbe()
	_, present["generator"] = b.Generator()
	for _, part := range hal.OptionalParts {
		if have, reason := present[part], p.Absent[part]; have == (reason != "") {
			t.Errorf("%s: present %v, reason %q", part, have, reason)
		}
	}
}
