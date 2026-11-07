package app

import (
	"context"
	"sync"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-dso-core/instrument"
	"github.com/labeth/open-sds800xhd/app/internal/platform"
)

// selfCal runs :SYSTem:SELFCal: the host asks, the owner runs
// FrontEnd.SelfCal (LONG, OWNER) between frames, the query reports DOING
// until it has finished.
type selfCal struct {
	b     hal.Board
	ctl   *instrument.Model
	log   *platform.Log
	mu    sync.Mutex
	doing bool
}

// start queues one self-calibration; a request while one runs is the same
// request.
func (s *selfCal) start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.doing {
		return nil
	}
	s.doing = true
	s.ctl.Post(func(ctx context.Context) {
		rep, err := s.b.FrontEnd().SelfCal(ctx, nil)
		switch {
		case err != nil:
			s.log.Printf("self-cal: %v", err)
		case len(rep.Failed) > 0:
			s.log.Printf("self-cal: stored %s, failed %v", rep.Stored, rep.Failed)
		default:
			s.log.Printf("self-cal: stored %s", rep.Stored)
		}
		s.mu.Lock()
		s.doing = false
		s.mu.Unlock()
	})
	return nil
}

// state is DOING or DONE.
func (s *selfCal) state() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.doing {
		return "DOING"
	}
	return "DONE"
}
