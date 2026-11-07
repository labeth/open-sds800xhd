package scopeboard

import (
	"context"
	"errors"
	"time"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-sds800xhd/app/internal/acq"
	"github.com/labeth/open-sds800xhd/app/internal/trigger"
)

type acquirerFacet Board

// Configure stages the plan's horizontal settings and channel set.
func (a *acquirerFacet) Configure(p hal.Plan) error {
	b := (*Board)(a)
	c, err := (*plannerFacet)(b).config(p.Request)
	if err != nil {
		return err
	}
	b.staged.Enum, b.staged.Depth, b.staged.DelayS = c.Enum, c.Depth, p.Request.DelayS
	b.staged.Roll, b.staged.Peak = c.Roll, c.Peak
	b.staged.Segments = 0
	if p.Segments > 1 {
		b.staged.Segments = p.Segments
	}
	for ch := range b.staged.Ch {
		b.staged.Ch[ch].On = c.Mask&(1<<ch) != 0
	}
	return nil
}

// Arm commits the staged state and arms. After a commit, a stop or the
// start-up the arm runs the pending ADC calibration and the start delay;
// the per-frame re-arm does not (spec 03 §3.2).
func (a *acquirerFacet) Arm() error {
	b := (*Board)(a)
	if b.eng == nil {
		return errors.New("not brought up")
	}
	if b.open != nil {
		return b.openArm()
	}
	if b.staged.Roll {
		return hal.Refuse("arm", "a roll plan is not armed: it streams through the Streamer")
	}
	if err := sequenceSweep(&b.staged); err != nil {
		return err
	}
	if b.eng.Rolling() {
		b.eng.RollStop()
	}
	withDelay := b.rerun
	if b.eng.Commit(b.staged) {
		withDelay = true
	}
	if err := b.eng.Arm(withDelay); err != nil {
		b.errs.arms++
		return err
	}
	b.rerun = false
	b.state, b.stored = hal.Armed, false
	return nil
}

// sequenceSweep refuses a sequence under the AUTO sweep. Sequence mode
// runs under the Normal or Single sweep (spec 03 §6.2, spec 05 §7); the
// board does not substitute a sweep the
// caller did not ask for. The check is made at the commit, so it does not
// depend on whether the plan or the trigger was staged first.
func sequenceSweep(s *acq.Settings) error {
	if s.Segments > 1 && s.Trig.Sweep == trigger.Auto {
		return hal.Refuse("sequence", "sequence mode runs with the Normal or Single sweep, not AUTO (spec 03 §6.2); set the sweep to Normal")
	}
	return nil
}

// Wait polls the last-frame flag (spec 03 §3.6), then halts the record so
// Readout can run. Status.Done is false when the halt did not complete or
// the record's parameters are not valid; the owner then re-arms.
func (a *acquirerFacet) Wait(ctx context.Context, timeout time.Duration) (hal.Status, error) {
	b := (*Board)(a)
	if b.open != nil {
		return b.openWait(ctx, timeout)
	}
	if b.state != hal.Armed {
		return hal.Status{}, errors.New("not armed")
	}
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	deadline := time.Now().Add(timeout)
	ready := b.eng.FrameReady
	if b.eng.Settings().Segments > 1 {
		// A sequence is done when all N segments are captured (spec 03
		// §6.2); the frame flag rises with every segment.
		ready = func() bool { done, _ := b.eng.SequenceDone(); return done }
	}
	for !ready() {
		if timeout <= 0 || time.Now().After(deadline) {
			return hal.Status{}, hal.ErrTimeout // a zero timeout polls once
		}
		select {
		case <-ctx.Done():
			return hal.Status{}, ctx.Err()
		case <-tick.C:
		}
	}
	halted, valid := b.eng.Halt()
	if !halted {
		b.state = hal.Idle
		return hal.Status{}, nil
	}
	if n := b.eng.Settings().Segments; n > 1 {
		// Sequence mode: the frame is the N segments (spec 03 §6.2); the
		// acquisition stays stopped showing them.
		done, got := b.eng.SequenceDone()
		if !done || got < n {
			b.state = hal.Idle
			return hal.Status{}, nil
		}
		b.eng.SequenceComplete(n)
		b.rerun = true
	}
	b.seq++
	b.state, b.stored = hal.Ready, true
	forced := b.forced
	b.forced = false
	b.status = hal.Status{
		Triggered: !forced, Forced: forced, Done: valid, Coherent: valid,
		Record: hal.RecordID{Epoch: b.epoch, Seq: uint32(b.seq)}, //nolint:gosec // G115: the record sequence wraps at 32 bits
	}
	return b.status, nil
}

// Force is the one-shot forced trigger (spec 05 §7): the armed
// acquisition completes without a trigger condition; the next Wait reports
// it as forced.
func (a *acquirerFacet) Force() error {
	b := (*Board)(a)
	if b.open != nil {
		return b.openForce()
	}
	if b.state != hal.Armed {
		return hal.Refuse("force trigger", "nothing is armed")
	}
	b.eng.Force()
	b.forced = true
	return nil
}

// Stop is the user's stop. The next Arm is a RUN: it gets the start delay.
// A record already halted stays readable.
func (a *acquirerFacet) Stop() error {
	b := (*Board)(a)
	if b.eng == nil {
		return nil
	}
	if b.open != nil {
		return b.openStop()
	}
	if b.eng.Rolling() {
		b.eng.RollStop()
	}
	if b.state == hal.Armed {
		// A stop mid-frame leaves no complete record (spec 03 Open:
		// whether the PL abandons the frame is unknown).
		b.stored = false
	}
	b.eng.Stop()
	b.state, b.rerun = hal.Idle, true
	return nil
}

func (a *acquirerFacet) State() hal.AcqState { return (*Board)(a).state }
