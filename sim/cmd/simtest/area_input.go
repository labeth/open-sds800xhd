package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/labeth/open-sds800xhd/sim/harness"
)

// The keys, knob steps and touches the in-guest TestInput expects, in its
// order (app/internal/boardtest/input_test.go): matrix row and bit of
// spec 08 §2.2, with a step for the knobs.
var inputScenario = []string{
	"@15 key 5 5",   // Run/Stop
	"@17 key 0 3",   // CH1
	"@19 key 0 0 3", // vertical scale, + 3
	"@21 key 3 0 2", // horizontal position, bit 0 is the − direction
	"@23 key 5 2",   // Adjust push
	"@25 key 7 3",   // Single
	"@27 touch 500 300",
}

func init() {
	register(boardArea("input", "workspaces/views: keys, knobs, touch, lamps", "Input", 120, "",
		inputScenario, checkInjected))
}

// checkInjected checks that every injected key reached a reader in read(2)
// (the device model logs a dropped key otherwise).
func checkInjected(_ *env, r *harness.Run) []harness.Result {
	b, err := os.ReadFile(r.File("log.jsonl"))
	if err != nil {
		return []harness.Result{harness.Fail("input", "key delivery", "%v", err)}
	}
	keys, dropped := 0, 0
	for l := range strings.SplitSeq(string(b), "\n") {
		if !strings.Contains(l, `"ev":"key"`) {
			continue
		}
		keys++
		if strings.Contains(l, "dropped") {
			dropped++
		}
	}
	res := harness.Pass("input", "key delivery", fmt.Sprintf("%d key records, none dropped", keys))
	if keys == 0 || dropped > 0 {
		res = harness.Fail("input", "key delivery", "%d key records, %d dropped by the driver", keys, dropped)
	}
	// Button presses and the touch click the beeper: TTC0 Counter_Control_1
	// bit 0 cleared, then set again (spec 08 §8.2).
	starts := 0
	for _, ev := range r.Log.Window("app: case input/events", "app: case") {
		if ev.Ev == "mmio" && ev.Op == "w" && ev.Address() == 0xFF11000C && ev.Value()&1 == 0 {
			starts++
		}
	}
	click := harness.Pass("input", "clicks", fmt.Sprintf("%d beeper clicks for 3 keys and a touch", starts))
	if starts != 4 {
		click = harness.Fail("input", "clicks", "%d beeper clicks, want 4 (run, ch1, single, touch)", starts)
	}
	return []harness.Result{res, click}
}
