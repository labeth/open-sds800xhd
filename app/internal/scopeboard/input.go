package scopeboard

import (
	"fmt"
	"time"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-sds800xhd/app/internal/panel"
)

// inputSpec is the panel's controls by core name (spec 08 §2, §6).
func inputSpec() hal.InputSpec {
	return hal.InputSpec{Keys: panel.Keys(), Knobs: panel.Knobs(), Lamps: panel.Lamps(), Touch: true, Beeper: true}
}

// inputFacet is the front panel. Its events come from the panel's two
// reader goroutines (key device and touch screen), started at bring-up.
type inputFacet Board

// Events is the key, knob and touch stream under the UI model's names
// (input_names.go); nil before bring-up.
func (in *inputFacet) Events() <-chan hal.InputEvent {
	if e := (*Board)(in).ui.events; e != nil {
		return e
	}
	return nil
}

// SetLamps writes the LED word at once (spec 08 §6.2): the lamps are not
// part of the acquisition commit. Names this panel has no lamp for light
// nothing.
func (in *inputFacet) SetLamps(on []string) error {
	p := (*Board)(in).ui.panel
	if p == nil {
		return hal.Refuse("lamps", "the board is not brought up")
	}
	return p.SetLamps(on)
}

func (in *inputFacet) Keys() []string  { return panel.Keys() }
func (in *inputFacet) Knobs() []string { return panel.Knobs() }

// SetSound switches the key and touch clicks of the beeper (the Sound
// setting, on at bring-up).
func (in *inputFacet) SetSound(on bool) error {
	p := (*Board)(in).ui.panel
	if p == nil {
		return hal.Refuse("sound", "the board is not brought up")
	}
	p.SetSound(on)
	return nil
}

// Beep sounds the buzzer for d (spec 08 §8.2); it returns at once.
func (in *inputFacet) Beep(d time.Duration) error {
	p := (*Board)(in).ui.panel
	if p == nil || !p.Beep(d) {
		return hal.Refuse("beep", "the board is not brought up")
	}
	return nil
}

// SetTouch suspends or resumes the touch controller (spec 08 §7).
func (in *inputFacet) SetTouch(on bool) error {
	if err := panel.SetTouch(on); err != nil {
		return fmt.Errorf("touch switch: %w", err)
	}
	return nil
}

// panelDiag is the panel's counters for Platform.Diag.
func (b *Board) panelDiag() map[string]any {
	if b.ui.panel == nil {
		return nil
	}
	rec, touch, drop := b.ui.panel.Stats()
	return map[string]any{"key_records": rec, "touches": touch, "dropped": drop, "leds": b.ui.panel.Word()}
}
