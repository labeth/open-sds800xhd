package acq

import (
	"context"

	"github.com/labeth/open-sds800xhd/app/internal/frontend"
)

// The acquisition state of the user self-calibration (spec 10 §5.3 step 1):
// 50 µs/div, the 1M depth, every input at 50 mV/div DC.
const (
	selfCalEnum  = 16
	selfCalDepth = 2
)

// SelfCal runs the user self-calibration of spec 10 §5 with the acquisition
// stopped, then brings the hardware back to the settings it had, sending
// every channel setup again with the new tables. The stored record is lost.
func (e *Engine) SelfCal(ctx context.Context, progress func(float64)) (frontend.SelfCalResult, error) {
	e.stop()
	e.RollStop()
	prev := e.cur
	base := prev
	base.Enum, base.Depth = selfCalEnum, selfCalDepth
	base.Roll, base.Segments, base.Peak, base.View = false, 0, false, View{}
	env := frontend.SelfCalEnv{
		Channels: func(mask int) (bool, error) {
			s := base
			for c := range e.nch {
				s.Ch[c] = frontend.Channel{On: mask&(1<<c) != 0, VoltsDiv: 0.05, Probe: 1, Coupling: frontend.DC}
			}
			e.Commit(s)
			return e.h.R == 2000, nil
		},
		Progress: progress,
	}
	res, err := e.FE.SelfCal(ctx, e.nch, env)
	e.resendChannels()
	e.Commit(prev)
	return res, err
}

// resendChannels makes the next Commit send every channel setup: the
// self-calibration left the analog path in states no setting describes.
func (e *Engine) resendChannels() {
	for c := range e.cur.Ch {
		e.cur.Ch[c].VoltsDiv = 0
	}
	e.FE.ForgetDAC()
}
