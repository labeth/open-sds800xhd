// Package panel is the SDS800X HD's front panel (spec 08): the key matrix
// and the six encoders through /dev/siglent_kb, the Goodix touch screen
// through evdev, the key LEDs and the beeper. It turns the hardware's
// records into the core's input events and the core's lamp names into the
// LED word.
package panel

import "github.com/labeth/open-dso-core/hal"

// knobAdjust is the universal knob's name: a knob, a lamp and the first LED bit.
const knobAdjust = "adjust"

// key is one position of the key matrix (spec 08 §2.2): a key, or one
// direction or the push of a knob.
type key struct {
	name string // the key's core name; a knob's push is "<knob>-push"
	knob string // a rotation of this knob
	sign int    // the rotation's sign (the UI sign of spec 08 §2.2)
}

// matrix maps a record's (byte index, bit position) to its key (spec 08
// §2.2). Byte 0, positions 0–2 are the shared vertical-scale knob; the
// hardware always reports the CH1 codes of the shared vertical knobs.
var matrix = map[[2]byte]key{
	{0, 0}: {knob: "vscale", sign: +1}, {0, 1}: {knob: "vscale", sign: -1}, {0, 2}: {name: "vscale-push"},
	{0, 3}: {name: "ch1"}, {0, 4}: {name: "ch2"}, {0, 5}: {name: "ch3"}, {0, 6}: {name: "ch4"}, {0, 7}: {name: "menu"},
	{1, 0}: {knob: "vpos", sign: +1}, {1, 1}: {knob: "vpos", sign: -1}, {1, 2}: {name: "vpos-push"},
	{1, 3}: {name: "digital"}, {1, 4}: {name: "cursors"}, {1, 5}: {name: "measure"}, {1, 6}: {name: "navigate"},
	{2, 0}: {knob: "hscale", sign: +1}, {2, 1}: {knob: "hscale", sign: -1}, {2, 2}: {name: "hscale-push"},
	{2, 3}: {name: "nav-prev"}, {2, 4}: {name: "nav-stop"}, {2, 5}: {name: "nav-next"}, {2, 6}: {name: "math"},
	{3, 0}: {knob: "hpos", sign: -1}, {3, 1}: {knob: "hpos", sign: +1}, {3, 2}: {name: "hpos-push"},
	{3, 3}: {name: "ref"}, {3, 4}: {name: "acquire"}, {3, 5}: {name: "display"}, {3, 6}: {name: "save"},
	{4, 0}: {knob: "level", sign: +1}, {4, 1}: {knob: "level", sign: -1}, {4, 2}: {name: "level-push"},
	{4, 3}: {name: "quick"}, {4, 4}: {name: "roll"}, {4, 5}: {name: "clear"}, {4, 6}: {name: "decode"},
	{5, 0}: {knob: knobAdjust, sign: +1}, {5, 1}: {knob: knobAdjust, sign: -1}, {5, 2}: {name: "adjust-push"},
	{5, 3}: {name: "touch"}, {5, 4}: {name: "zoom"}, {5, 5}: {name: "run"}, {5, 6}: {name: "autoset"},
	{6, 3}: {name: "default"}, {6, 4}: {name: "trigger"}, {6, 5}: {name: "auto"}, {6, 6}: {name: "normal"},
	{7, 3}: {name: "single"}, {7, 7}: {name: "print"},
}

// Keys are the panel's keys, knob pushes included, in matrix order.
func Keys() []string {
	var out []string
	for b := range byte(8) {
		for p := range byte(8) {
			if k, ok := matrix[[2]byte{b, p}]; ok && k.name != "" {
				out = append(out, k.name)
			}
		}
	}
	return out
}

// Knobs are the six encoders.
func Knobs() []string { return []string{"vscale", "vpos", "hscale", "hpos", "level", knobAdjust} }

// decoder turns /dev/siglent_kb records into events. A record names only
// the first active key; a record with no key is the release of the key the
// last record pressed (spec 08 §3.2, §4.2).
type decoder struct {
	down string // the key held since the last record, "" for none
}

// decode returns the event of one record; ok is false for a record that is
// no event (an unknown matrix position, or no key with none held).
func (d *decoder) decode(r [4]byte) (hal.InputEvent, bool) {
	k, known := matrix[[2]byte{r[0], r[1]}]
	if !known {
		if r[0] == 0xFF && d.down != "" {
			ev := hal.InputEvent{Source: hal.FromPanel, Kind: hal.KeyEvent, Key: d.down}
			d.down = ""
			return ev, true
		}
		return hal.InputEvent{}, false
	}
	if k.knob != "" {
		// The step byte counts detents (spec 08 §4.2); a step of 0 is
		// counted as one, never as "reset to default".
		s := max(1, int(r[2]))
		return hal.InputEvent{Source: hal.FromPanel, Kind: hal.KnobEvent, Knob: k.knob, Steps: k.sign * s}, true
	}
	d.down = k.name
	return hal.InputEvent{Source: hal.FromPanel, Kind: hal.KeyEvent, Key: k.name, Down: true}, true
}

// lamps maps the core's lamp names to LED word bits (spec 08 §6.1).
var lamps = map[string]uint32{
	knobAdjust: 1 << 0, "ch1": 1 << 1, "ch2": 1 << 2, "ch3": 1 << 3, "ch4": 1 << 4, "digital": 1 << 5,
	"cursors": 1 << 6, "measure": 1 << 7, "math": 1 << 8, "ref": 1 << 9, "roll": 1 << 10, "zoom": 1 << 11,
	"decode": 1 << 12, "touch": 1 << 13, "auto": 1 << 14, "normal": 1 << 15, "single": 1 << 16,
	"run": 1 << 17, "stop": 1 << 18,
}

// Lamps are the panel's lamp names.
func Lamps() []string {
	return []string{
		knobAdjust, "ch1", "ch2", "ch3", "ch4", "digital", "cursors", "measure", "math", "ref",
		"roll", "zoom", "decode", "touch", "auto", "normal", "single", "run", "stop",
	}
}

// LampWord is the LED word lighting the named lamps; names this panel has
// no lamp for are returned in unknown.
func LampWord(on []string) (w uint32, unknown []string) {
	for _, n := range on {
		b, ok := lamps[n]
		if !ok {
			unknown = append(unknown, n)
			continue
		}
		w |= b
	}
	return w, unknown
}
