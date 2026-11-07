package lab

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/labeth/open-sds800xhd/sim/harness"
)

// RunState is what the page shows of the simulator run.
type RunState struct {
	// State: "queued" (waiting for the simulator, which other runs share),
	// "running", "ended" (the run reached its time limit or stopped; a new
	// one follows with auto-restart) or "attached" (a run simlab did not
	// start).
	State   string    `json:"state"`
	Name    string    `json:"name"`
	Dir     string    `json:"dir"`
	Started time.Time `json:"started"`
	Secs    int       `json:"secs"` // the run's time limit, 0 when attached
	Runs    int       `json:"runs"` // runs started by this simlab
	Err     string    `json:"err,omitempty"`
	ctl     string
}

// Runner keeps a simulator run of the app going, or attaches to one.
type Runner struct {
	mu      sync.Mutex
	st      RunState
	signals map[string]string // the generator per channel, kept across runs
	cancel  context.CancelFunc
	again   chan struct{}
}

// NewRunner returns a runner with the channels' generators at their
// start values (Cn → spec).
func NewRunner(signals map[string]string) *Runner {
	m := map[string]string{}
	maps.Copy(m, signals)
	return &Runner{signals: m, again: make(chan struct{}, 1), st: RunState{State: "ended"}}
}

// State returns the run's state.
func (r *Runner) State() RunState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.st
}

// ctlPath is the running simulator's control socket, "" when none.
func (r *Runner) ctlPath() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.st.State != "running" && r.st.State != "attached" {
		return ""
	}
	return r.st.ctl
}

// remember records the channels' generators so the next run starts with
// them.
func (r *Runner) remember(m map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, v := range m {
		if v != "" {
			r.signals[k] = v
		}
	}
}

// signalArgs is the device-model flag starting a run with the remembered
// generators.
func (r *Runner) signalArgs() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var parts []string
	for _, c := range []string{"C1", "C2", "C3", "C4"} {
		if s := r.signals[c]; s != "" {
			parts = append(parts, c+"="+s)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "-signals " + strings.Join(parts, ";")
}

// Restart ends the current run; the loop starts the next.
func (r *Runner) Restart() {
	r.mu.Lock()
	c := r.cancel
	r.mu.Unlock()
	if c != nil {
		c()
	}
	select {
	case r.again <- struct{}{}:
	default:
	}
}

// Loop runs spec (an app run with Ports) until ctx ends, starting a new
// run when one ends if auto is set, or when Restart is called.
func (r *Runner) Loop(ctx context.Context, spec harness.Spec, auto bool) {
	for ctx.Err() == nil {
		runCtx, cancel := context.WithCancel(ctx)
		r.mu.Lock()
		r.cancel = cancel
		r.st = RunState{State: "queued", Name: spec.Name, Secs: spec.Secs, Runs: r.st.Runs + 1}
		r.mu.Unlock()
		s := spec
		s.Ctx = runCtx
		s.SimdArgs = strings.TrimSpace(spec.SimdArgs + " " + r.signalArgs())
		s.Host = func(hctx context.Context, dir string) {
			p, _ := os.ReadFile(filepath.Join(dir, "ctl.path")) //nolint:gosec // G304: the run's own output
			r.mu.Lock()
			r.st.State, r.st.Dir, r.st.Started, r.st.ctl = "running", dir, time.Now(), strings.TrimSpace(string(p))
			r.mu.Unlock()
			<-hctx.Done()
		}
		_, err := harness.Exec(s)
		cancel()
		r.mu.Lock()
		r.st.State, r.st.ctl = "ended", ""
		if err != nil && ctx.Err() == nil {
			r.st.Err = err.Error()
		}
		r.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		select {
		case <-r.again: // a restart asked for while this run was going
			continue
		default:
		}
		var retry <-chan time.Time // nil: wait for the page's Restart
		switch {
		case auto && err == nil:
			continue
		case auto: // a broken run: retry slowly
			retry = time.After(15 * time.Second)
		}
		select {
		case <-ctx.Done():
			return
		case <-r.again:
		case <-retry:
		}
	}
}

// Attach follows a run simlab did not start, by its directory (the
// simulator's runs/NAME), until ctx ends.
func (r *Runner) Attach(ctx context.Context, dir string) {
	for ctx.Err() == nil {
		p, err := os.ReadFile(filepath.Join(dir, "ctl.path")) //nolint:gosec // G304: the run's own output
		path := strings.TrimSpace(string(p))
		st := RunState{State: "ended", Name: filepath.Base(dir), Dir: dir}
		if err == nil {
			if _, err := os.Stat(path); err == nil { //nolint:gosec // G703: the control socket the run itself named
				st.State, st.ctl = "attached", path
			}
		}
		r.mu.Lock()
		if r.st.State != st.State || r.st.ctl != st.ctl {
			st.Started = time.Now()
			r.st = st
		}
		r.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}
