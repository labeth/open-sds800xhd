package lab

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeModel is a device model's control socket: it records the commands
// and answers like the model does.
type fakeModel struct {
	mu   sync.Mutex
	cmds []string
}

func (f *fakeModel) serve(t *testing.T, path string) {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					line := sc.Text()
					f.mu.Lock()
					f.cmds = append(f.cmds, line)
					f.mu.Unlock()
					_, _ = io.WriteString(c, f.answer(line)+"\n")
				}
			}()
		}
	}()
}

func (f *fakeModel) answer(line string) string {
	fs := strings.Fields(line)
	switch fs[0] {
	case "peek":
		if fs[1] == "0x800405a8" {
			return "0x6004" // CH2, touch, auto
		}
		return "0x2" // run
	case "sigshow":
		return `{"C1":"sine,1000,1,0","C2":"spi.mosi,1e6,3.3,1.65,data=a5","C3":"dc,0,0,0","C4":"dc,0,0,0"}`
	case "shotcomp":
		w, _ := os.Create(fs[1])
		_ = png.Encode(w, image.NewRGBA(image.Rect(0, 0, 4, 3)))
		_ = w.Close()
		return "ok"
	case "signals":
		if strings.Contains(line, "bogus") {
			return "err signal C1: unknown kind"
		}
		return "ok"
	case "bus":
		return "ok C1=spi.clk,1e6,3.3,1.65;C2=spi.mosi,1e6,3.3,1.65"
	}
	return "ok"
}

func (f *fakeModel) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.cmds
	f.cmds = nil
	var keep []string
	for _, c := range out {
		if !strings.HasPrefix(c, "peek") && c != "sigshow" {
			keep = append(keep, c)
		}
	}
	return keep
}

// bench is a simlab attached to a fake model's run directory, whose app
// serves.
func bench(t *testing.T) (*httptest.Server, *fakeModel) {
	t.Helper()
	hs, m, _ := benchProbe(t, func(context.Context) bool { return true })
	return hs, m
}

// benchProbe is bench with the app's serving probe given.
func benchProbe(t *testing.T, probe func(context.Context) bool) (*httptest.Server, *fakeModel, *Runner) {
	t.Helper()
	dir, err := os.MkdirTemp("", "lab") //nolint:usetesting // short: a unix socket path is limited, t.TempDir's are long
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "ctl.sock")
	m := &fakeModel{}
	m.serve(t, sock)
	if err := os.WriteFile(filepath.Join(dir, "ctl.path"), []byte(sock+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	run := NewRunner(nil)
	run.Probe = probe
	go run.Attach(ctx, dir)
	for run.ctlPath() == "" {
		time.Sleep(5 * time.Millisecond)
	}
	s := NewServer(run, t.TempDir())
	s.HoldMs, s.inputGap = 1, 0
	go s.Serve(ctx)
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)
	return hs, m, run
}

func post(t *testing.T, hs *httptest.Server, path, body string) (int, map[string]any) {
	t.Helper()
	r, err := http.Post(hs.URL+path, "application/json", strings.NewReader(body)) //nolint:noctx // a test against a local server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(r.Body).Decode(&out)
	return r.StatusCode, out
}

func TestStateLampsAndSignals(t *testing.T) {
	hs, _ := bench(t)
	r, err := http.Get(hs.URL + "/api/state") //nolint:noctx // a test against a local server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Body.Close() }()
	var st State
	if err := json.NewDecoder(r.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.LEDWord != 0x26004 || strings.Join(st.Lamps, ",") != "ch2,touch,auto,run" {
		t.Errorf("LED word %#x lamps %v", st.LEDWord, st.Lamps)
	}
	if st.Signals["C2"] != "spi.mosi,1e6,3.3,1.65,data=a5" || st.Run.State != "attached" {
		t.Errorf("signals %v run %+v", st.Signals, st.Run)
	}
}

func TestInputReachesTheModel(t *testing.T) {
	hs, m := bench(t)
	for _, c := range []struct{ path, body string }{
		{"/api/key", `{"key":"run"}`},
		{"/api/knob", `{"knob":"hpos","steps":3}`},
		{"/api/knob", `{"knob":"vscale","steps":-20}`},
		{"/api/knob", `{"knob":"level","push":true}`},
		{"/api/touch", `{"x":512,"y":300}`},
	} {
		if code, out := post(t, hs, c.path, c.body); code != http.StatusOK || out["ok"] != true {
			t.Fatalf("%s %s: %d %v", c.path, c.body, code, out)
		}
	}
	want := []string{"key 5 5 0 1", "key 3 1 3 30", "key 0 1 8 30", "key 0 1 8 30", "key 0 1 4 30", "key 4 2 0 30", "touch 512 300 1"}
	if got := m.take(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("commands\n%v\nwant\n%v", got, want)
	}
	for _, c := range []struct{ path, body string }{
		{"/api/key", `{"key":"selfdestruct"}`},
		{"/api/knob", `{"knob":"vscale"}`},
		{"/api/touch", `{"x":1024,"y":0}`},
	} {
		if code, _ := post(t, hs, c.path, c.body); code != http.StatusBadRequest {
			t.Errorf("%s %s: %d, want 400", c.path, c.body, code)
		}
	}
	if got := m.take(); len(got) != 0 {
		t.Errorf("refused input reached the model: %v", got)
	}
}

func TestGenerators(t *testing.T) {
	hs, m := bench(t)
	if code, out := post(t, hs, "/api/signal", `{"ch":"c3","spec":"square,2e3,1,0,duty=0.3"}`); code != http.StatusOK {
		t.Fatalf("signal: %d %v", code, out)
	}
	if code, out := post(t, hs, "/api/signal", `{"ch":"C1","spec":"bogus,1,1,0"}`); code != http.StatusBadRequest || !strings.Contains(fmt.Sprint(out["err"]), "unknown kind") {
		t.Errorf("model's refusal: %d %v", code, out)
	}
	for _, bad := range []string{`{"ch":"C5","spec":"dc,0,0,0"}`, `{"ch":"C1","spec":"dc,0,0,0;C2=sine,1,1,0"}`, `{"ch":"C1","spec":"dc 0"}`} {
		if code, _ := post(t, hs, "/api/signal", bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, code)
		}
	}
	code, out := post(t, hs, "/api/bus", `{"kind":"spi","lines":{"clk":"C1","mosi":"c2","miso":""},"rate":1e6,"vpp":3.3,"offset":1.65,"opts":{"data":"a5","mode":"0","miso":""}}`)
	if code != http.StatusOK {
		t.Fatalf("bus: %d %v", code, out)
	}
	if code, _ := post(t, hs, "/api/bus", `{"kind":"spi","lines":{"clk":"C1"},"rate":1,"opts":{"data":"a5 b6"}}`); code != http.StatusBadRequest {
		t.Errorf("an option with a space: %d", code)
	}
	want := []string{
		"signals C3=square,2e3,1,0,duty=0.3", "signals C1=bogus,1,1,0",
		"bus spi clk=C1 mosi=C2 rate=1e+06 vpp=3.3 offset=1.65 data=a5 mode=0",
	}
	if got := m.take(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("commands\n%v\nwant\n%v", got, want)
	}
}

func TestScreenAndPage(t *testing.T) {
	hs, _ := bench(t)
	for _, c := range []struct{ path, ctype string }{
		{"/api/screen.png", "image/png"},
		{"/", "text/html; charset=utf-8"},
		{"/lab.js", "text/javascript; charset=utf-8"},
		{"/design/tokens.css", "text/css; charset=utf-8"},
		{"/design/base.css", "text/css; charset=utf-8"},
	} {
		r, err := http.Get(hs.URL + c.path) //nolint:noctx // a test against a local server
		if err != nil {
			t.Fatal(err)
		}
		_ = r.Body.Close()
		if r.StatusCode != http.StatusOK || r.Header.Get("Content-Type") != c.ctype {
			t.Errorf("%s: %d %s", c.path, r.StatusCode, r.Header.Get("Content-Type"))
		}
	}
}

func TestNoSimulator(t *testing.T) {
	s := NewServer(NewRunner(nil), t.TempDir())
	hs := httptest.NewServer(s.Handler())
	defer hs.Close()
	if code, _ := post(t, hs, "/api/key", `{"key":"run"}`); code != http.StatusServiceUnavailable {
		t.Errorf("key without a simulator: %d", code)
	}
	if code, _ := post(t, hs, "/api/signal", `{"ch":"C1","spec":"dc,0,0,0"}`); code != http.StatusServiceUnavailable {
		t.Errorf("signal without a simulator: %d", code)
	}
}

func TestRunnerSignalArgs(t *testing.T) {
	r := NewRunner(map[string]string{"C1": "sine,1,1,0", "C3": "dc,0,0,0"})
	r.remember(map[string]string{"C3": "uart,9600,3.3,1.65,data=41", "C4": ""})
	if got := r.signalArgs(); got != "-signals C1=sine,1,1,0;C3=uart,9600,3.3,1.65,data=41" {
		t.Errorf("%q", got)
	}
}

// TestServing: the state says the app serves only once the probe passes,
// and again not when it stops passing, within the same run.
func TestServing(t *testing.T) {
	var up atomic.Bool
	hs, _, _ := benchProbe(t, func(context.Context) bool { return up.Load() })
	state := func() RunState {
		r, err := http.Get(hs.URL + "/api/state") //nolint:noctx // a test against a local server
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = r.Body.Close() }()
		var st State
		if err := json.NewDecoder(r.Body).Decode(&st); err != nil {
			t.Fatal(err)
		}
		return st.Run
	}
	wait := func(want bool) RunState {
		t.Helper()
		var st RunState
		for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			if st = state(); st.Serving == want {
				return st
			}
		}
		t.Fatalf("serving never became %v: %+v", want, st)
		return st
	}
	st := wait(false)
	if st.State != "attached" || st.Runs == 0 {
		t.Fatalf("run %+v", st)
	}
	up.Store(true)
	st2 := wait(true)
	if st2.Runs != st.Runs || st2.ServingAt.IsZero() {
		t.Errorf("serving changed the run or has no time: %+v", st2)
	}
	up.Store(false)
	wait(false)
}

// TestModelGone: a control socket that refuses ends the run's serving at
// once, and the API answers 503 as with no simulator.
func TestModelGone(t *testing.T) {
	hs, _, run := benchProbe(t, func(context.Context) bool { return true })
	for end := time.Now().Add(5 * time.Second); !run.State().Serving; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("never serving")
		}
	}
	path := run.ctlPath()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	code, out := post(t, hs, "/api/signal", `{"ch":"C1","spec":"sine,1,1,0"}`)
	if code != http.StatusServiceUnavailable || run.State().Serving {
		t.Errorf("after the model went: %d %v, serving %v", code, out, run.State().Serving)
	}
	time.Sleep(1500 * time.Millisecond) // a probe that passes again does not bring the run back
	if run.State().Serving {
		t.Error("serving again after the model went")
	}
}
