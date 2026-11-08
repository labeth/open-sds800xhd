package scopeboard

import (
	"testing"

	"github.com/labeth/open-dso-core/hal"
	coreui "github.com/labeth/open-dso-core/uimodel/panel"
	"github.com/labeth/open-sds800xhd/app/internal/panel"
	"github.com/labeth/open-sds800xhd/app/internal/platform"
)

// Every key and knob of the panel reaches the UI model under one of its
// names, or is one the UI model has no job for; the shared vertical knobs
// and their pushes act on the channel whose key was pressed last.
func TestPanelInputNames(t *testing.T) {
	p := new(names)
	unused := map[string]bool{
		"digital": true, "navigate": true, "nav-prev": true, "nav-stop": true,
		"nav-next": true, "quick": true, "roll": true, "clear": true, "touch": true, "hpos-push": false,
	}
	for _, k := range panel.Keys() {
		ev, ok := p.translate(hal.InputEvent{Kind: hal.KeyEvent, Key: k, Down: true})
		if unused[k] {
			if ok {
				t.Errorf("key %s reached the UI model as %q", k, ev.Key)
			}
			continue
		}
		if n, known := coreui.KeyName(ev.Key); !ok || !known || n != ev.Key {
			t.Errorf("key %s: %q (%v)", k, ev.Key, ok)
		}
	}
	for _, k := range panel.Knobs() {
		ev, ok := p.translate(hal.InputEvent{Kind: hal.KnobEvent, Knob: k, Steps: 1})
		if !ok || ev.Knob == "" {
			t.Errorf("knob %s: %q (%v)", k, ev.Knob, ok)
		}
	}
	p.translate(hal.InputEvent{Kind: hal.KeyEvent, Key: "ch4", Down: true})
	if ev, _ := p.translate(hal.InputEvent{Kind: hal.KnobEvent, Knob: "vpos", Steps: 1}); ev.Knob != coreui.KnobCh4Pos {
		t.Fatalf("vpos after CH4: %+v", ev)
	}
	p.translate(hal.InputEvent{Kind: hal.KeyEvent, Key: "ch2", Down: true})
	if ev, _ := p.translate(hal.InputEvent{Kind: hal.KnobEvent, Knob: "vscale", Steps: -1}); ev.Knob != coreui.KnobCh2Vdiv || ev.Steps != -1 {
		t.Fatalf("vscale after CH2: %+v", ev)
	}
	if ev, _ := p.translate(hal.InputEvent{Kind: hal.KeyEvent, Key: "vpos-push", Down: true}); ev.Key != coreui.KeyCh2PosPush {
		t.Fatalf("vpos push after CH2: %+v", ev)
	}
	if ev, ok := p.translate(hal.InputEvent{Kind: hal.TouchEvent, TouchX: 3, TouchY: 4, Down: true}); !ok || ev.TouchX != 3 {
		t.Fatalf("a touch: %+v", ev)
	}
}

// forward drops what the UI model has no key for and closes with its
// source.
func TestForwardNames(t *testing.T) {
	src := make(chan hal.InputEvent, 4)
	dst := make(chan hal.InputEvent, 4)
	src <- hal.InputEvent{Kind: hal.KeyEvent, Key: "digital", Down: true}
	src <- hal.InputEvent{Kind: hal.KeyEvent, Key: "autoset", Down: true}
	close(src)
	new(names).forward(src, dst)
	var got []string
	for ev := range dst {
		got = append(got, ev.Key)
	}
	if len(got) != 1 || got[0] != coreui.KeyAuto {
		t.Fatalf("forwarded %v", got)
	}
}

// A channel selected without its key (a V/div set over SCPI) is the one
// the shared vertical knob then turns.
func TestSelectChannelMovesTheKnob(t *testing.T) {
	b := &Board{id: platform.Identity{Channels: 4}}
	b.SelectChannel(2)
	ev, ok := b.ui.names.translate(hal.InputEvent{Kind: hal.KnobEvent, Knob: "vscale", Steps: 1})
	if !ok || ev.Knob != "ch3vdiv" {
		t.Fatalf("vscale after selecting C3: %+v", ev)
	}
	b.SelectChannel(7) // no such channel: the selection stays
	if ev, _ := b.ui.names.translate(hal.InputEvent{Kind: hal.KnobEvent, Knob: "vpos", Steps: 1}); ev.Knob != "ch3pos" {
		t.Fatalf("vpos after a channel the model lacks: %+v", ev)
	}
}
