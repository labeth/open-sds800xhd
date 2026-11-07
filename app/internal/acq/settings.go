// Package acq is the acquisition owner of the SDS800X HD: the start-up of
// the PL and the analog front end (spec 03 §2), the horizontal setup (spec
// 03 §3.3, spec 04 §8–§9), the arm/stop state machine and the frame loop
// (spec 03 §3), and the record readout (spec 03 §4).
//
// One goroutine owns an Engine; every method is called from it.
package acq

import (
	"github.com/labeth/open-sds800xhd/app/internal/frontend"
	"github.com/labeth/open-sds800xhd/app/internal/mapengine"
	"github.com/labeth/open-sds800xhd/app/internal/trigger"
)

// Settings is the acquisition-relevant instrument state (spec 09 §2).
type Settings struct {
	Ch       [4]frontend.Channel
	Enum     int     // time/div enum (spec 04 §2)
	Depth    int     // depth enum (spec 04 §3)
	DelayS   float64 // horizontal delay, positive moves the trigger left
	Peak     bool
	Trig     trigger.Settings
	Display  mapengine.Display
	Roll     bool // roll mode is on (spec 04 §7): smp = 0
	Segments int  // sequence mode with this many segments; 0 or 1: off (spec 03 §6.2)
	View     View
}

// DefaultSettings is the built-in setup of spec 09 §2.2 with CH1 on,
// as the spec recommends for a replacement.
func DefaultSettings() Settings {
	s := Settings{Enum: 9, Depth: 3, Display: mapengine.DefaultDisplay()}
	for c := range s.Ch {
		s.Ch[c] = frontend.Channel{VoltsDiv: 1, Probe: 1, Coupling: frontend.DC}
	}
	s.Ch[0].On = true
	s.Trig = trigger.Default()
	return s
}

// Mask is the enabled-channel mask of the first n channels.
func (s *Settings) Mask(n int) int {
	m := 0
	for c := range n {
		if s.Ch[c].On {
			m |= 1 << c
		}
	}
	return m
}
