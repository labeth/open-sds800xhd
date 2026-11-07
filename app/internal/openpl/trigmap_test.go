package openpl

import (
	"testing"

	"github.com/labeth/open-sds800xhd/app/internal/trigger"
)

var oneV = [4]Chan{{VoltsDiv: 1, Class: 12}, {VoltsDiv: 1, Class: 12}, {VoltsDiv: 1, Class: 12}, {VoltsDiv: 1, Class: 12}}

// TestFromSettings checks rows of spec 20 §7.4's table and the comparator
// rule of §7.2.
func TestFromSettings(t *testing.T) {
	const all = 0x1FFFF
	h := trigger.NoiseValue(12, false)
	for _, c := range []struct {
		name               string
		s                  trigger.Settings
		open, close, abort uint32
		cond               int
		always, reach      bool
		cmp                Cmp
	}{
		{"edge rising", trigger.Settings{Type: trigger.Edge, Source: 1, Slope: trigger.Rising}, 0, 1 << 4, 0, CondAny, true, false, Cmp{2048, uint16(2048 - h)}},
		{"edge falling", trigger.Settings{Type: trigger.Edge, Source: 1, Slope: trigger.Falling}, 0, 1 << 5, 0, CondAny, true, false, Cmp{uint16(2048 + h), 2048}},
		{"pulse positive <", trigger.Settings{Type: trigger.Pulse, Source: 0, Slope: trigger.Rising, Limit: trigger.Less, UpperS: 1e-6}, 1, 2, 0, CondLess, false, false, Cmp{uint16(2048 + h/2), uint16(2048 - h/2)}},
		{"interval", trigger.Settings{Type: trigger.Interval, Source: 0, Slope: trigger.Rising, Limit: trigger.Greater, LowerS: 1e-6}, 1, 1, 0, CondGreater, false, false, Cmp{2048, uint16(2048 - h)}},
		{"dropout state", trigger.Settings{Type: trigger.Dropout, Source: 0, Slope: trigger.Rising, TimeoutS: 1e-6}, 1, 0, 2, CondGreater, false, true, Cmp{uint16(2048 + h/2), uint16(2048 - h/2)}},
	} {
		tr, err := FromSettings(c.s, oneV, all, 1e-6)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		src := c.s.Source
		if tr.Open != c.open || tr.Close != c.close || tr.Abort != c.abort || tr.Cond != c.cond || tr.AlwaysArmed != c.always ||
			tr.Reach != c.reach || tr.Cmp[src][0] != c.cmp {
			t.Errorf("%s: open %#x close %#x abort %#x cond %d always %v reach %v cmp %+v", c.name, tr.Open, tr.Close, tr.Abort,
				tr.Cond, tr.AlwaysArmed, tr.Reach, tr.Cmp[src][0])
		}
	}
	if _, err := FromSettings(trigger.Settings{Type: trigger.Video}, oneV, all, 1e-6); err == nil {
		t.Error("video: want a refusal (no RTL)")
	}
	if tr, _ := FromSettings(trigger.Settings{Type: trigger.Edge, Source: 0, Slope: trigger.Rising, Sweep: trigger.Auto}, oneV, all, 1e-3); tr.AutoClocks != uint64(11e-3*CclkHz) {
		t.Errorf("auto timeout %d clocks, want record + 10 ms", tr.AutoClocks)
	}
}

func TestRecordSized(t *testing.T) {
	r, err := Record{Mask: 1, Decim: 1, Pre: 500, Post: 499}.Sized()
	if err != nil || r.Groups != 33 || r.RingWords() < r.Words()+32 {
		t.Errorf("groups %d, %v", r.Groups, err)
	}
	if _, err := (Record{Mask: 15, Decim: 1, Pre: 50_000_000, Post: 50_000_000}).Sized(); err == nil {
		t.Error("4 × 100 M words: want a refusal (record region)")
	}
	if RouteFor(1) != 0 || RouteFor(5) != 1 || RouteFor(7) != 2 {
		t.Error("routes")
	}
}
