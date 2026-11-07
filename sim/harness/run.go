package harness

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Workspace is the private reference workspace holding the simulator.
func Workspace() string {
	if ws := os.Getenv("SDS800XHD_WS"); ws != "" {
		return ws
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "ws", "sds800xhd")
}

// tier1Script is the simulator's run script: the workspace's, or the one
// SDS800XHD_TIER1 names (a worktree's, which builds that tree's device model
// while the lock, rootfs and runs stay in the workspace).
func tier1Script(ws string) string {
	if p := os.Getenv("SDS800XHD_TIER1"); p != "" {
		return p
	}
	return filepath.Join(ws, "sim", "run", "tier1.sh")
}

// Spec is one simulator run.
type Spec struct {
	Name     string   // run name: analysis/testenv/runs/<Name>
	Secs     int      // wall-clock limit (the simulator caps it at 300)
	SimdArgs string   // device-model flags: input signals, hw-id, ...
	Scenario []string // timed control lines "@SECS command ..."
	Ports    bool     // publish the SCPI ports on host loopback
	Keep     bool     // keep the run's p12 and p9 from the last run of this name
	// Script pins register reads in the device model: physical address →
	// value (simd -script), for status the model does not compute.
	Script map[uint64]uint32
	// Seed starts the run from fresh partitions holding these files: run
	// directory paths ("usr/usr/acq_self_cal_usr.txt" is on p12,
	// "firmdata0/..." on p9) to host files. p9 also gets the simulator's
	// default factory seed.
	Seed map[string]string

	// Exactly one of Board, App and Guest: Board runs the in-guest board test
	// binary with BoardArgs in place of the vendor application; App runs the
	// launch hook, which starts the app from p12.
	Board     string // host path of the board test binary
	BoardArgs []string
	App       string // host path of the app binary
	Hook      string // the launch hook (ota/boot/extra.sh), with App
	GuestBoot string // the guest script that runs the hook, with App

	// Guest runs a guest shell script in place of the vendor application,
	// with Files (name in /sim/fw → host path) beside it.
	Guest string
	Files map[string]string

	// Host runs on the host while this run's simulator does (from when it
	// holds the simulator and has started, until it ends: ctx), given the
	// run's directory, where its outputs land; Exec waits for it before it
	// returns. With Ports it reaches the guest's published ports.
	Host func(ctx context.Context, dir string)

	// Ctx, when set, ends the run early once it is done: a run still
	// waiting for the simulator's lock is abandoned; a running app run (the
	// guest boot script) ends its guest, after which the simulator's run
	// script finishes as at its time limit (the device model stops, the
	// summary is written). Other guests run to their time limit.
	Ctx context.Context //nolint:containedctx // a run's optional abandon signal, read once by Exec
}

// Run is a finished run's outputs.
type Run struct {
	Name, Dir string
	Console   string
	Log       *Log
	Scenario  string // the scenario lines with the replies of scpi lines
	Exit      error  // the run's exit status, nil for 0
}

// Exec runs s under the simulator's lock and loads its outputs.
func Exec(s Spec) (*Run, error) {
	ws := Workspace()
	args := []string{
		filepath.Join(ws, "analysis", ".sim.lock"), tier1Script(ws),
		"-n", s.Name, "-t", strconv.Itoa(s.Secs),
	}
	if s.Ports {
		args = append(args, "-p")
	}
	env := os.Environ()
	simd := s.SimdArgs
	if len(s.Seed) > 0 {
		if err := seed(ws, s.Name, s.Seed); err != nil {
			return nil, err
		}
	}
	if s.Keep || len(s.Seed) > 0 {
		env = append(env, "KEEP=1")
	}
	tmp, err := os.MkdirTemp("", "simtest")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if len(s.Script) > 0 {
		var b strings.Builder
		b.WriteString("{")
		first := true
		for a, v := range s.Script {
			if !first {
				b.WriteString(",")
			}
			first = false
			fmt.Fprintf(&b, "%q: {\"value\": \"%#x\"}", fmt.Sprintf("%#x", a), v)
		}
		b.WriteString("}")
		p := filepath.Join(tmp, "script.json")
		if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil { //nolint:gosec // G306: an input the simulator reads, not a secret
			return nil, err
		}
		simd = strings.TrimSpace(simd + " -script " + p)
	}
	if simd != "" {
		env = append(env, "SIMD_ARGS="+simd)
	}
	if len(s.Scenario) > 0 {
		p := filepath.Join(tmp, "scenario.txt")
		if err := os.WriteFile(p, []byte(strings.Join(s.Scenario, "\n")+"\n"), 0o644); err != nil { //nolint:gosec // G306: an input the simulator reads, not a secret
			return nil, err
		}
		env = append(env, "SCENARIO="+p)
	}
	switch {
	case s.Board != "":
		args = append(args, "-R", s.Board, "-H", "modules", "--")
		args = append(args, s.BoardArgs...)
	case s.App != "":
		fw := filepath.Join(tmp, "fw")
		if err := os.MkdirAll(filepath.Join(fw, "open"), 0o755); err != nil { //nolint:gosec // G301: the guest's firmware tree keeps the unit's 0755 directories
			return nil, err
		}
		if err := copyFile(s.Hook, filepath.Join(fw, "extra.sh"), 0o755); err != nil {
			return nil, err
		}
		if err := copyFile(s.App, filepath.Join(fw, "open", "app"), 0o755); err != nil {
			return nil, err
		}
		env = append(env, "FWDIR="+fw)
		args = append(args, "-R", s.GuestBoot, "-H", "modules")
	case s.Guest != "":
		fw := filepath.Join(tmp, "fw")
		if err := os.MkdirAll(fw, 0o755); err != nil { //nolint:gosec // G301: the guest's firmware tree keeps the unit's 0755 directories
			return nil, err
		}
		for name, from := range s.Files {
			if err := copyFile(from, filepath.Join(fw, name), 0o755); err != nil {
				return nil, err
			}
		}
		env = append(env, "FWDIR="+fw)
		args = append(args, "-R", s.Guest, "-H", "modules")
	default:
		return nil, fmt.Errorf("run %s: no board test, app or guest script", s.Name)
	}
	ctx := s.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	runDir := filepath.Join(ws, "analysis", "testenv", "runs", s.Name)
	cmd := exec.CommandContext(ctx, "flock", args...) //nolint:gosec // G204: flock runs the workspace's simulator with the harness's own arguments
	cmd.Env = env
	since := time.Now()
	cmd.Cancel = func() error {
		if stopGuest(runDir, since) {
			return nil
		}
		return cmd.Process.Signal(syscall.SIGTERM) // still waiting for the lock: give up the place
	}
	cmd.WaitDelay = 60 * time.Second
	if s.Ctx != nil {
		// The run ends through Ctx alone: a terminal's Ctrl-C reaches the
		// caller, which stops the run in order, not the run script midway.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	hostDone := make(chan struct{})
	hostCtx, endHost := context.WithCancel(context.Background())
	if s.Host != nil {
		go func() {
			defer close(hostDone)
			if started(hostCtx, runDir) {
				s.Host(hostCtx, runDir)
			}
		}()
	} else {
		close(hostDone)
	}
	runErr := cmd.Run()
	endHost()
	<-hostDone
	r, err := Load(s.Name)
	if err != nil {
		if runErr != nil {
			return nil, fmt.Errorf("run %s: %w\n%s", s.Name, runErr, tail(out.String(), 20))
		}
		return nil, err
	}
	// The guest program's exit status reaches us as the run's: a failing
	// board test is reported by its cases, not as a broken run.
	r.Exit = runErr
	return r, nil
}

// Load reads the outputs of an earlier run.
func Load(name string) (*Run, error) {
	dir := filepath.Join(Workspace(), "analysis", "testenv", "runs", name)
	r := &Run{Name: name, Dir: dir}
	c, err := os.ReadFile(filepath.Join(dir, "console.txt")) //nolint:gosec // G304: a run's output in the simulator workspace
	if err != nil {
		return nil, err
	}
	r.Console = string(c)
	sc, _ := os.ReadFile(filepath.Join(dir, "scenario.txt")) //nolint:gosec // G304: a run's output in the simulator workspace
	r.Scenario = string(sc)
	if r.Log, err = ReadLog(filepath.Join(dir, "log.jsonl")); err != nil {
		return nil, err
	}
	return r, nil
}

// File returns the path of a file the run left, relative to its directory
// (p12 is usr/, p9 firmdata0/, captures shots/).
func (r *Run) File(rel string) string { return filepath.Join(r.Dir, rel) }

// Lines returns the console lines that start with prefix, prefix removed.
func (r *Run) Lines(prefix string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(r.Console))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if i := strings.Index(sc.Text(), prefix); i >= 0 {
			out = append(out, sc.Text()[i+len(prefix):])
		}
	}
	return out
}

func copyFile(from, to string, mode os.FileMode) error {
	b, err := os.ReadFile(from) //nolint:gosec // G304: a file the harness itself places into the guest
	if err != nil {
		return err
	}
	return os.WriteFile(to, b, mode) //nolint:gosec // G703: a path under the run's own directory
}

func tail(s string, n int) string {
	l := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, "\n")
}

// seed prepares a run directory's partitions: empty p12, p9 from the
// simulator's default factory seed, then the given files.
func seed(ws, name string, files map[string]string) error {
	dir := filepath.Join(ws, "analysis", "testenv", "runs", name)
	for _, p := range []string{"usr", "firmdata0"} {
		if err := os.RemoveAll(filepath.Join(dir, p)); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(dir, p), 0o755); err != nil { //nolint:gosec // G301: the guest's partitions keep the unit's 0755 directories
			return err
		}
	}
	sd := filepath.Join(ws, "analysis", "testenv", "seed", "firmdata0-sds824xhd")
	es, err := os.ReadDir(sd)
	if err != nil {
		return err
	}
	for _, e := range es {
		if e.Name() == "README.txt" || !e.Type().IsRegular() {
			continue
		}
		if err := copyFile(filepath.Join(sd, e.Name()), filepath.Join(dir, "firmdata0", e.Name()), 0o644); err != nil {
			return err
		}
	}
	for rel, from := range files {
		to := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil { //nolint:gosec // G301: the guest's partitions keep the unit's 0755 directories
			return err
		}
		if err := copyFile(from, to, 0o644); err != nil {
			return fmt.Errorf("seed %s: %w", rel, err)
		}
	}
	return nil
}

// SCPIReplies maps each scenario "scpi" line to its reply as the
// simulator recorded it ("[T] scpi LINE -> REPLY").
func SCPIReplies(r *Run) map[string]string {
	m := map[string]string{}
	for l := range strings.SplitSeq(r.Scenario, "\n") {
		_, rest, ok := strings.Cut(l, "] scpi ")
		if !ok {
			continue
		}
		line, reply, _ := strings.Cut(rest, " -> ")
		m[line] = reply
	}
	return m
}

// stopGuest asks the guest of the run in dir to end, if it has started
// since t: the app's guest boot script (sim/guest-boot.sh) ends the
// container when the file stop appears in the run's fw directory, and the
// simulator's run script then finishes as at its time limit. False when
// the run has not started (it still waits for the simulator's lock).
func stopGuest(dir string, t time.Time) bool {
	fi, err := os.Stat(filepath.Join(dir, "ctl.path"))
	if err != nil || fi.ModTime().Before(t) {
		return false
	}
	return os.WriteFile(filepath.Join(dir, "fw", "stop"), nil, 0o644) == nil //nolint:gosec // G306: a marker the guest reads, not a secret
}

// started waits until the run named by dir holds the simulator and has
// started it: its control socket's path is written anew (the runs queue on
// one lock, and the ports another run publishes are not this run's). False
// when ctx ends first.
func started(ctx context.Context, dir string) bool {
	since := time.Now()
	for {
		if fi, err := os.Stat(filepath.Join(dir, "ctl.path")); err == nil && !fi.ModTime().Before(since) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(200 * time.Millisecond):
		}
	}
}
