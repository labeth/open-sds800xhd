package lab

import (
	"bufio"
	"context"
	"io"
	"maps"
	"net"
	"net/http"
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
	Runs    int       `json:"runs"` // runs started (or attached to) by this simlab: a new value is a new run
	// Serving: the app in this run answers its web API and SCPI, so pages
	// and tests may talk to it. False while the run boots and after it ends.
	Serving   bool      `json:"serving"`
	ServingAt time.Time `json:"serving_at,omitzero"` // when it started serving
	Left      int       `json:"left"`                // seconds until the run's time limit, 0 when unknown
	Err       string    `json:"err,omitempty"`
	ctl       string
	gone      bool // the device model stopped: the run is ending
}

// Runner keeps a simulator run of the app going, or attaches to one.
type Runner struct {
	mu      sync.Mutex
	st      RunState
	signals map[string]string // the generator per channel, kept across runs
	cancel  context.CancelFunc
	again   chan struct{}
	// Probe reports whether the run's app serves (its web API and SCPI);
	// the runner calls it about once a second while a run is up.
	Probe func(ctx context.Context) bool
}

// NewRunner returns a runner with the channels' generators at their
// start values (Cn → spec).
func NewRunner(signals map[string]string) *Runner {
	m := map[string]string{}
	maps.Copy(m, signals)
	return &Runner{signals: m, again: make(chan struct{}, 1), st: RunState{State: "ended"}, Probe: AppServes}
}

// State returns the run's state.
func (r *Runner) State() RunState {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.st
	if st.State == "running" && st.Secs > 0 {
		st.Left = max(0, st.Secs-int(time.Since(st.Started).Seconds()))
		if st.Left == 0 { // the simulator stops it now; its ports may already be gone
			st.Serving = false
		}
	}
	return st
}

// lost records that the run's control socket refused a command: the device
// model is gone, so the run is ending (the simulator writes its summary
// before the run reads as ended) and its app no longer serves.
func (r *Runner) lost(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.st.ctl == path && !r.st.gone {
		r.st.Serving, r.st.gone = false, true
	}
}

// probeMisses is how many probes in a row must fail before a serving app
// reads as gone: one slow answer on a loaded host is not a crash.
const probeMisses = 3

// watch follows whether run gen's app serves until ctx ends: Serving is
// set on the first probe that passes and cleared after probeMisses fail,
// so a crashed app reads as not serving while its run still holds the
// simulator.
func (r *Runner) watch(ctx context.Context, gen int) {
	misses := 0
	for ctx.Err() == nil {
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		ok := r.Probe != nil && r.Probe(pctx)
		cancel()
		if ok {
			misses = 0
		} else {
			misses++
		}
		r.mu.Lock()
		if r.st.Runs == gen && (r.st.State == "running" || r.st.State == "attached") {
			switch {
			case ok && !r.st.Serving && !r.st.gone:
				r.st.Serving, r.st.ServingAt = true, time.Now()
			case !ok && r.st.Serving && misses >= probeMisses:
				r.st.Serving = false
			}
		}
		r.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
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
			gen := r.st.Runs
			r.mu.Unlock()
			r.watch(hctx, gen)
		}
		_, err := harness.Exec(s)
		cancel()
		r.mu.Lock()
		r.st.State, r.st.ctl, r.st.Serving = "ended", "", false
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
	var stopWatch context.CancelFunc = func() {}
	defer func() { stopWatch() }()
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
			st.Runs = r.st.Runs + 1
			r.st = st
			stopWatch()
			if st.State == "attached" {
				wctx, cancel := context.WithCancel(ctx)
				stopWatch = cancel
				go r.watch(wctx, st.Runs)
			}
		}
		r.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

// AppServes is the default probe: the app's web API answers /api/status
// on 127.0.0.1:8080 and its SCPI socket on 127.0.0.1:5025 answers *IDN?,
// the two ports the simulator publishes for the run holding it.
func AppServes(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:8080/api/status", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close() // the status code is the answer
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", "127.0.0.1:5025")
	if err != nil {
		return false
	}
	defer func() { _ = c.Close() }() // one query per connection
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	if _, err := io.WriteString(c, "*IDN?\n"); err != nil {
		return false
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	return err == nil && strings.TrimSpace(line) != ""
}
