package scopeboard

import (
	"context"
	"errors"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-sds800xhd/app/internal/frontend"
)

// SelfCal runs the user self-calibration of spec 10 §5 with the inputs
// open and writes the user self-cal file on p12. It is refused while the
// acquisition is armed, reading or streaming (stop it first: the
// self-calibration reprograms the front end and the timebase) and when the
// PL's mean engine does not respond. A channel whose search fails keeps
// its tables and is listed in the report. The stored record is lost; the
// next Arm is a commit arm. OWNER, LONG (minutes).
func (f *frontEndFacet) SelfCal(ctx context.Context, progress func(float64)) (hal.CalReport, error) {
	b := (*Board)(f)
	const what = "self-calibration"
	switch {
	case b.eng == nil:
		return hal.CalReport{}, hal.Refuse(what, "the board is not brought up")
	case b.stream != nil || b.eng.Rolling():
		return hal.CalReport{}, hal.Refuse(what, "the roll stream runs: stop it first")
	case b.state == hal.Armed || b.state == hal.Reading:
		return hal.CalReport{}, hal.Refuse(what, "the acquisition runs: stop it first (the self-calibration reprograms the front end and the timebase)")
	}
	res, err := b.eng.SelfCal(ctx, progress)
	b.state, b.stored, b.rerun = hal.Idle, false, true
	rep := hal.CalReport{Failed: res.Failed, Stored: res.File, Source: f.Calibration()}
	if errors.Is(err, frontend.ErrMeanEngine) {
		return rep, hal.Refuse(what, err.Error())
	}
	return rep, err
}
