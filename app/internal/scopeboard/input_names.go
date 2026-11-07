package scopeboard

import (
	"fmt"
	"sync"

	"github.com/labeth/open-dso-core/hal"
	coreui "github.com/labeth/open-dso-core/uimodel/panel"
)

// panelKeys are this panel's keys by the UI model's names. The panel's
// AUTO and NORMAL keys are the trigger mode's; its Autoset key is the UI
// model's AUTO.
var panelKeys = map[string]string{
	"run": coreui.KeyRunStop, "single": coreui.KeySingle, "autoset": coreui.KeyAuto,
	"auto": coreui.KeyTrigAuto, "normal": coreui.KeyTrigNormal,
	"default": coreui.KeyDefaultSetup, "print": coreui.KeyPrint, "menu": coreui.KeyMenu,
	"measure": coreui.KeyMeasure, "cursors": coreui.KeyCursors, "math": coreui.KeyMath, "ref": coreui.KeyRef,
	"acquire": coreui.KeyAcquire, "display": coreui.KeyDisplay, "save": coreui.KeySaveRecall,
	"trigger": coreui.KeyTrigMenu, "decode": coreui.KeyUtility, "zoom": coreui.KeyTdivPush,
	"ch1": coreui.KeyCh1, "ch2": coreui.KeyCh2, "ch3": coreui.KeyCh3, "ch4": coreui.KeyCh4,
	"level-push": coreui.KeyTrigLvlPush, "hscale-push": coreui.KeyTdivPush, "hpos-push": coreui.KeyHorizPosPush,
	"adjust-push": coreui.KeyAdjustPush,
}

// panelKnobs are this panel's knobs by the UI model's names; the shared
// vertical ones are the selected channel's.
var panelKnobs = map[string]string{
	"hscale": coreui.KnobTdiv, "hpos": coreui.KnobHorizPos, "level": coreui.KnobTrigLevel, "adjust": coreui.KnobAdjust,
}

// names turns the panel's events into the UI model's names (ADR-UI-KEYS-
// ARE-CORE-NAMES). The vertical knobs and their pushes are shared by the
// channels on this panel (spec 08 §2.2), so they act on the channel whose
// key was pressed last.
type names struct {
	mu  sync.Mutex
	sel int // the channel the vertical knobs act on
}

// translate is ev under the UI model's names, ok false for a key or knob it
// has none for (the digital and navigation keys).
func (n *names) translate(ev hal.InputEvent) (hal.InputEvent, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	switch ev.Kind {
	case hal.KeyEvent:
		switch ev.Key {
		case "ch1", "ch2", "ch3", "ch4":
			if ev.Down {
				n.sel = int(ev.Key[2] - '1')
			}
		case "vscale-push":
			ev.Key = fmt.Sprintf("ch%dvdivpush", n.sel+1)
			return ev, true
		case "vpos-push":
			ev.Key = fmt.Sprintf("ch%dpospush", n.sel+1)
			return ev, true
		}
		k, ok := panelKeys[ev.Key]
		ev.Key = k
		return ev, ok
	case hal.KnobEvent:
		switch ev.Knob {
		case "vscale":
			ev.Knob = fmt.Sprintf("ch%dvdiv", n.sel+1)
			return ev, true
		case "vpos":
			ev.Knob = fmt.Sprintf("ch%dpos", n.sel+1)
			return ev, true
		}
		k, ok := panelKnobs[ev.Knob]
		ev.Knob = k
		return ev, ok
	case hal.TouchEvent, hal.Overload: // the screen's place and the input's alarm pass as they are
	}
	return ev, true
}

// forward passes src's events under the UI model's names to dst until src
// closes, dropping what the UI model has no key for.
func (n *names) forward(src <-chan hal.InputEvent, dst chan<- hal.InputEvent) {
	for ev := range src {
		if ev, ok := n.translate(ev); ok {
			dst <- ev
		}
	}
	close(dst)
}
