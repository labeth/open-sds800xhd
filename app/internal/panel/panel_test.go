package panel

import (
	"slices"
	"testing"
	"time"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-sds800xhd/app/internal/mmio/mmiotest"
)

func TestDecodeKeys(t *testing.T) {
	var d decoder
	cases := []struct {
		rec  [4]byte
		want hal.InputEvent
	}{
		{[4]byte{0, 3, 0, 0}, hal.InputEvent{Kind: hal.KeyEvent, Key: "ch1", Down: true}},
		{[4]byte{0xFF, 0xFF, 0, 0}, hal.InputEvent{Kind: hal.KeyEvent, Key: "ch1"}},
		{[4]byte{5, 5, 0, 0}, hal.InputEvent{Kind: hal.KeyEvent, Key: "run", Down: true}},
		{[4]byte{0xFF, 0xFF, 0, 0}, hal.InputEvent{Kind: hal.KeyEvent, Key: "run"}},
		{[4]byte{7, 3, 0, 0}, hal.InputEvent{Kind: hal.KeyEvent, Key: "single", Down: true}},
		{[4]byte{0xFF, 0xFF, 0, 0}, hal.InputEvent{Kind: hal.KeyEvent, Key: "single"}},
		// knobs: the UI sign of spec 08 §2.2; horizontal position is reversed
		{[4]byte{0, 0, 3, 0}, hal.InputEvent{Kind: hal.KnobEvent, Knob: "vscale", Steps: 3}},
		{[4]byte{0, 1, 1, 0}, hal.InputEvent{Kind: hal.KnobEvent, Knob: "vscale", Steps: -1}},
		{[4]byte{3, 0, 2, 0}, hal.InputEvent{Kind: hal.KnobEvent, Knob: "hpos", Steps: -2}},
		{[4]byte{3, 1, 0, 0}, hal.InputEvent{Kind: hal.KnobEvent, Knob: "hpos", Steps: 1}},
		{[4]byte{5, 2, 0, 0}, hal.InputEvent{Kind: hal.KeyEvent, Key: "adjust-push", Down: true}},
		{[4]byte{0xFF, 0xFF, 0, 0}, hal.InputEvent{Kind: hal.KeyEvent, Key: "adjust-push"}},
	}
	for i, c := range cases {
		got, ok := d.decode(c.rec)
		c.want.Source = hal.FromPanel
		if !ok || got != c.want {
			t.Errorf("%d: record %v → %+v %v, want %+v", i, c.rec, got, ok, c.want)
		}
	}
	// A record with no key and nothing held, and an unused position.
	for _, r := range [][4]byte{{0xFF, 0xFF, 0, 0}, {6, 0, 0, 0}} {
		if ev, ok := d.decode(r); ok {
			t.Errorf("record %v gave %+v", r, ev)
		}
	}
}

func TestKeyTables(t *testing.T) {
	keys := Keys()
	if len(keys) != 37 {
		t.Errorf("%d keys", len(keys))
	}
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			t.Errorf("key %q twice", k)
		}
		seen[k] = true
	}
	for _, k := range Knobs() {
		if !seen[k+"-push"] {
			t.Errorf("knob %s has no push", k)
		}
	}
}

// Spec 08 §6.1, §6.3: the words.
func TestLampWord(t *testing.T) {
	for _, c := range []struct {
		on   []string
		want uint32
	}{
		{[]string{"auto", "run", "touch"}, 0x26000},
		{[]string{"auto", "run", "touch", "ch1"}, 0x26002},
		{[]string{"single", "run", "touch"}, 0x32000},
		{[]string{"stop", "touch", "ch1"}, 0x42002},
		{[]string{"normal", "run", "touch", "zoom", "ch1"}, 0x2A802},
	} {
		if w, unk := LampWord(c.on); w != c.want || len(unk) != 0 {
			t.Errorf("%v → %#x %v, want %#x", c.on, w, unk, c.want)
		}
	}
	if _, unk := LampWord([]string{"run", "wavegen"}); !slices.Equal(unk, []string{"wavegen"}) {
		t.Errorf("unknown lamps %v", unk)
	}
	for _, l := range Lamps() {
		if _, ok := lamps[l]; !ok {
			t.Errorf("lamp %q has no bit", l)
		}
	}
}

func TestSetLamps(t *testing.T) {
	var got uint32
	p := New(func(w uint32) { got = w }, nil)
	if err := p.SetLamps([]string{"auto", "run", "touch", "ch2"}); err != nil {
		t.Fatal(err)
	}
	if got != 0x26004 || p.Word() != 0x26004 {
		t.Errorf("LED word %#x", got)
	}
}

func TestTouch(t *testing.T) {
	var s touchState
	feed := s.feed
	feed(evAbs, absMTX, 300)
	feed(evAbs, absMTY, 200)
	feed(evKey, btnTouch, 1)
	ev, ok := feed(evSyn, 0, 0)
	if want := (hal.InputEvent{Source: hal.FromTouch, Kind: hal.TouchEvent, TouchX: 300, TouchY: 200, Down: true}); !ok || ev != want {
		t.Errorf("down: %+v %v", ev, ok)
	}
	feed(evAbs, absMTX, 310)
	if _, ok := feed(evSyn, 0, 0); ok {
		t.Error("a move gave an event")
	}
	feed(evKey, btnTouch, 0)
	ev, ok = feed(evSyn, 0, 0)
	if !ok || ev.Down || ev.TouchX != 310 {
		t.Errorf("up: %+v %v", ev, ok)
	}
}

func TestParseInputDevices(t *testing.T) {
	list := `I: Bus=0019 Vendor=0001
N: Name="gpio-keys"
H: Handlers=kbd event0

I: Bus=0018 Vendor=dead
N: Name="goodix-ts"
H: Handlers=mouse0 event1
`
	if n, err := parseInputDevices(list, "goodix-ts"); err != nil || n != "/dev/input/event1" {
		t.Errorf("%q %v", n, err)
	}
	if _, err := parseInputDevices(list, "x"); err == nil {
		t.Error("missing device found")
	}
}

// Spec 08 §8.1: the timer set-up ends silent.
func TestBeeper(t *testing.T) {
	bus := mmiotest.New()
	b := NewBeeper(bus)
	if v, _ := bus.Last(ttcInterval1); v != 27770 {
		t.Errorf("interval %d", v)
	}
	if v, _ := bus.Last(ttcMatch1); v != 13885 {
		t.Errorf("match %d", v)
	}
	ctl, _ := bus.Last(ttcCounter1)
	if ctl != 0b1011 {
		t.Errorf("counter control %#b, want disabled, interval, match, output on", ctl)
	}
	b.Beep(time.Millisecond)
	if v, _ := bus.Last(ttcCounter1); v&1 != 0 {
		t.Error("beep did not start")
	}
	time.Sleep(20 * time.Millisecond)
	if v, _ := bus.Last(ttcCounter1); v&1 != 1 {
		t.Error("beep did not stop")
	}
}
