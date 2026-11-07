// Package lab is simlab, the virtual bench for the SDS800X HD simulator: it
// starts (or attaches to) a simulator run of the app, and serves one page
// with a drawing of the front panel whose keys, knobs and touch screen
// inject into the same input path the firmware reads, the live screen, the
// key LEDs, a signal generator per input channel with protocol generators,
// and the firmware's own web UI.
package lab

import "fmt"

// matrixPos is a key matrix position (spec 08 §2.2): byte index and bit
// position from the MSB, as the device model's "key" command takes them.
type matrixPos struct{ B, P byte }

// keys are the panel's keys by the firmware's key names (spec 08 §2.2).
var keys = map[string]matrixPos{
	"ch1": {0, 3}, "ch2": {0, 4}, "ch3": {0, 5}, "ch4": {0, 6},
	"digital": {1, 3}, "cursors": {1, 4}, "measure": {1, 5}, "navigate": {1, 6},
	"nav-prev": {2, 3}, "nav-stop": {2, 4}, "nav-next": {2, 5}, "math": {2, 6},
	"ref": {3, 3}, "acquire": {3, 4}, "display": {3, 5}, "save": {3, 6},
	"quick": {4, 3}, "roll": {4, 4}, "clear": {4, 5}, "decode": {4, 6},
	"touch": {5, 3}, "zoom": {5, 4}, "run": {5, 5}, "autoset": {5, 6},
	"default": {6, 3}, "trigger": {6, 4}, "auto": {6, 5}, "normal": {6, 6},
	"single": {7, 3},
}

// knob is one encoder: the positions of its two rotation codes by UI sign
// and of its push (spec 08 §2.2; horizontal position has + on bit 1).
type knob struct{ Plus, Minus, Push matrixPos }

var knobs = map[string]knob{
	"vscale": {Plus: matrixPos{0, 0}, Minus: matrixPos{0, 1}, Push: matrixPos{0, 2}},
	"vpos":   {Plus: matrixPos{1, 0}, Minus: matrixPos{1, 1}, Push: matrixPos{1, 2}},
	"hscale": {Plus: matrixPos{2, 0}, Minus: matrixPos{2, 1}, Push: matrixPos{2, 2}},
	"hpos":   {Plus: matrixPos{3, 1}, Minus: matrixPos{3, 0}, Push: matrixPos{3, 2}},
	"level":  {Plus: matrixPos{4, 0}, Minus: matrixPos{4, 1}, Push: matrixPos{4, 2}},
	"adjust": {Plus: matrixPos{5, 0}, Minus: matrixPos{5, 1}, Push: matrixPos{5, 2}},
}

// lampBits are the LED word's bits by lamp name (spec 08 §6.1).
var lampBits = []string{
	0: "adjust", 1: "ch1", 2: "ch2", 3: "ch3", 4: "ch4", 5: "digital", 6: "cursors", 7: "measure",
	8: "math", 9: "ref", 10: "roll", 11: "zoom", 12: "decode", 13: "touch", 14: "auto", 15: "normal",
	16: "single", 17: "run", 18: "stop",
}

// LED word registers: map idx 0x16A (bits 15:0) and 0x16F (bits 31:16),
// spec 08 §1.
const (
	ledLoAddr = 0x80040000 + 4*0x16A
	ledHiAddr = 0x80040000 + 4*0x16F
)

// lampsOn names the lamps the LED word lights.
func lampsOn(w uint32) []string {
	out := []string{}
	for b, n := range lampBits {
		if w&(1<<uint(b)) != 0 {
			out = append(out, n)
		}
	}
	return out
}

// keyCmd is the device-model command pressing a key for hold ms.
func keyCmd(name string, holdMs int) (string, error) {
	p, ok := keys[name]
	if !ok {
		return "", fmt.Errorf("no key %q", name)
	}
	return fmt.Sprintf("key %d %d 0 %d", p.B, p.P, holdMs), nil
}

// maxStep bounds the detents one knob record carries.
const maxStep = 8

// knobCmds are the device-model commands turning a knob by steps detents
// (UI sign: positive increases) or, with steps 0, pushing it.
func knobCmds(name string, steps, holdMs int) ([]string, error) {
	k, ok := knobs[name]
	if !ok {
		return nil, fmt.Errorf("no knob %q", name)
	}
	if steps == 0 {
		return []string{fmt.Sprintf("key %d %d 0 %d", k.Push.B, k.Push.P, holdMs)}, nil
	}
	if steps > 100 || steps < -100 {
		return nil, fmt.Errorf("%d steps: at most 100 a turn", steps)
	}
	p, n := k.Plus, steps
	if steps < 0 {
		p, n = k.Minus, -steps
	}
	var out []string
	for n > 0 {
		s := min(n, maxStep)
		out = append(out, fmt.Sprintf("key %d %d %d %d", p.B, p.P, s, holdMs))
		n -= s
	}
	return out, nil
}
