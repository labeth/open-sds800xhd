package board

import (
	"errors"
	"fmt"
	"time"

	"github.com/labeth/open-dso-core/hal"
)

// MaxStarts is how many starts in a row without a healthy app the
// supervisor makes in one boot before it reboots the unit (the reboot
// policy of Reboot then bounds the boots).
const MaxStarts = 3

// RebootRetry is how long the supervisor waits after a reboot that did
// not happen (the reboot call failed) before it starts the app again.
const RebootRetry = 45 * time.Second

// pollEvery is the supervisor's health poll.
const pollEvery = 500 * time.Millisecond

// Supervisor keeps the current slot's app running on a scope without a
// watchdog (HAL v1 SupervisorBoard over Board): an app healthy for Grace is
// confirmed (its slot's trial ends, the reboot count starts again); an app
// that dies or goes stale is stopped, its slot rolled back when on trial,
// and started again. After MaxStarts starts in a row without a healthy app
// it reboots through Reboot, so a crash loop costs at most MaxStarts starts
// per boot and MaxReboots boots before the open firmware disables itself
// and the next power-up keeps the vendor firmware (SAFETY.txt). A reboot
// the policy cannot count (p12 read-only) ends the supervision for this
// boot: no more starts.
type Supervisor struct {
	B      *Board
	Grace  time.Duration
	Launch func() error  // runs the launch hook as the boot does
	Stop   func(pid int) // ends the app (SIGTERM, then SIGKILL)
	Done   func() bool   // true ends the supervision; nil: never
	Sleep  func(d time.Duration)
	Now    func() time.Time
	Logf   func(format string, a ...any)
}

// Run supervises until Done, the open firmware is disabled, or a reboot
// cannot be counted (the error).
// TRLC-LINKS: REQ-HD-012
func (s *Supervisor) Run() error {
	s.defaults()
	b := s.B
	fails := 0
	for !s.Done() {
		if b.Slots().Status().Disabled {
			s.Logf("supervise: the open firmware is disabled; nothing more is started")
			return nil
		}
		pid := s.awaitApp()
		if s.watch(pid) {
			fails = 0
		}
		if s.Done() {
			return nil
		}
		st := b.Slots().Status()
		s.Logf("supervise: slot %s not healthy (pid %d)", st.Current, pid)
		if pid != 0 && b.Alive(pid) {
			s.Stop(pid)
		}
		fails++
		if st.Trial {
			if to, err := b.Slots().Rollback(); err == nil {
				s.Logf("supervise: rolled back to slot %s", to)
			}
		}
		if fails >= MaxStarts {
			if err := s.reboot(fails); err != nil {
				return err
			}
			if b.Slots().Status().Disabled {
				continue
			}
			fails = 0
		}
		if err := s.Launch(); err != nil {
			s.Logf("supervise: launch: %v", err)
		}
	}
	return nil
}

// reboot asks the policy for a reboot after fails starts; it returns only
// when the reboot did not happen: nil to go on (a refusal disabled the open
// firmware, or the call failed and RebootRetry passed), an error when the
// reboot could not be counted.
// TRLC-LINKS: REQ-HD-012
func (s *Supervisor) reboot(fails int) error {
	err := s.B.Reboot(fmt.Sprintf("%d starts in a row without a healthy app", fails))
	if _, ok := hal.AsRefusal(err); ok {
		s.Logf("supervise: %v", err)
		return nil
	}
	if errors.Is(err, ErrUncounted) {
		s.Logf("supervise: %v: no more starts this boot", err)
		return err
	}
	s.Logf("supervise: reboot failed (%v); starting again in %v", err, RebootRetry)
	s.Sleep(RebootRetry)
	return nil
}

// awaitApp waits up to Grace for the current slot's app; 0 if none runs.
// TRLC-LINKS: REQ-HD-012
func (s *Supervisor) awaitApp() int {
	end := s.Now().Add(s.Grace)
	for !s.Done() {
		if pid := s.B.AppPID(); pid != 0 {
			return pid
		}
		if !s.Now().Before(end) {
			return 0
		}
		s.Sleep(200 * time.Millisecond)
	}
	return 0
}

// watch follows pid while it stays healthy and confirms it once healthy
// for Grace; it reports whether it confirmed.
// TRLC-LINKS: REQ-HD-012
func (s *Supervisor) watch(pid int) bool {
	if pid == 0 {
		return false
	}
	start, confirmed := s.Now(), false
	for !s.Done() && s.B.Healthy(pid, s.Now()) {
		if !confirmed && s.Now().Sub(start) >= s.Grace {
			if err := s.B.Confirmed(); err != nil {
				s.Logf("supervise: confirm: %v", err)
			} else {
				s.Logf("supervise: slot %s healthy for %v (pid %d): confirmed", s.B.Slots().Status().Current, s.Grace, pid)
			}
			confirmed = true
		}
		s.Sleep(pollEvery)
	}
	return confirmed
}

// TRLC-LINKS: REQ-HD-012
func (s *Supervisor) defaults() {
	if s.Done == nil {
		s.Done = func() bool { return false }
	}
	if s.Sleep == nil {
		s.Sleep = time.Sleep
	}
	if s.Now == nil {
		s.Now = time.Now
	}
	if s.Logf == nil {
		s.Logf = s.B.logf
	}
	if s.Grace <= 0 {
		s.Grace = 15 * time.Second
	}
}
