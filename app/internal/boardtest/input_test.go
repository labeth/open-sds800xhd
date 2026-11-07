//go:build simboard

package boardtest

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/labeth/open-dso-core/hal"
)

// inputScript is what the harness injects (sim/cmd/simtest area "input")
// and the events the board must deliver for it, in order, under the UI
// model's names (the shared V/div knob is the last channel key's).
var inputWant = []string{
	"key run down", "key run up",
	"key ch1 down", "key ch1 up",
	"knob ch1vdiv +3",
	"knob horizpos -2",
	"key adjustpush down", "key adjustpush up",
	"key single down", "key single up",
	"touch 500,300 down", "touch 500,300 up",
}

func describe(ev hal.InputEvent) string {
	ud := map[bool]string{true: "down", false: "up"}[ev.Down]
	switch ev.Kind {
	case hal.KeyEvent:
		return "key " + ev.Key + " " + ud
	case hal.KnobEvent:
		return fmt.Sprintf("knob %s %+d", ev.Knob, ev.Steps)
	case hal.TouchEvent:
		return fmt.Sprintf("touch %d,%d %s", ev.TouchX, ev.TouchY, ud)
	case hal.Overload:
		// described below
	}
	return fmt.Sprintf("%+v", ev)
}

// TestInput reads the events the scenario injects and drives the lamps.
func TestInput(t *testing.T) {
	in := board.Input()

	c := begin(t, "input/profile")
	p := board.Profile().Inputs
	c.check(len(p.Keys) == 37 && len(p.Knobs) == 6 && len(p.Lamps) == 19 && p.Touch,
		"%d keys, %d knobs, %d lamps, touch %v", len(p.Keys), len(p.Knobs), len(p.Lamps), p.Touch)
	c.check(slices.Equal(in.Keys(), p.Keys), "Keys() matches the profile")

	c = begin(t, "input/lamps")
	c.noErr(in.SetLamps([]string{"normal", "run", "ch2", "touch"}), "lamps normal, run, ch2, touch")
	c.expect(map[string]uint16{"map:0x16a": 0xa004, "map:0x16f": 0x2, "map:0x16c": 1})

	c = begin(t, "input/events")
	var got []string
	deadline := time.After(60 * time.Second)
	ev := in.Events()
	for len(got) < len(inputWant) {
		select {
		case e := <-ev:
			got = append(got, describe(e))
			fmt.Println("event:", describe(e))
			continue
		case <-deadline:
		}
		break
	}
	c.check(slices.Equal(got, inputWant), "events [%s]", strings.Join(got, "; "))

	c = begin(t, "input/stop-lamps")
	c.noErr(in.SetLamps([]string{"stop", "touch", "ch1"}), "lamps stop, touch, ch1")
	c.expect(map[string]uint16{"map:0x16a": 0x2002, "map:0x16f": 0x4})
}
