package lab

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labeth/open-dso-core/design"
)

//go:embed static
var static embed.FS

// Server is simlab's HTTP side over a Runner.
type Server struct {
	run *Runner
	// WebURL is the firmware's web UI as the simulator publishes it.
	WebURL string
	// HoldMs is how long a key stays down.
	HoldMs int
	tmp    string

	input chan inputCmd // key, knob and touch commands, in order

	shotMu   sync.Mutex
	shotAt   time.Time
	shotPNG  []byte
	shotErr  error
	appShot  func(ctx context.Context) ([]byte, error)
	inputGap time.Duration
}

type inputCmd struct {
	cmds []string
	done chan error
}

// NewServer returns the server over run, with a scratch directory for
// screen dumps.
func NewServer(run *Runner, tmp string) *Server {
	s := &Server{
		run: run, WebURL: "http://127.0.0.1:8080/", HoldMs: 80, tmp: tmp,
		input: make(chan inputCmd, 64), inputGap: 40 * time.Millisecond,
	}
	s.appShot = s.fetchAppScreen
	return s
}

// Serve runs the input queue until ctx ends; call it once.
func (s *Server) Serve(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case in := <-s.input:
			var err error
			for _, c := range in.cmds {
				if _, err = ctl(ctx, s.run.ctlPath(), c); err != nil {
					break
				}
				// the next record after the key is released, so the driver
				// has its reader back (spec 08 §3.1)
				time.Sleep(time.Duration(s.HoldMs)*time.Millisecond + s.inputGap)
			}
			in.done <- err
		}
	}
}

// Handler is the page and its API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(static, "static")
	mux.Handle("GET /", http.FileServerFS(sub))
	mux.HandleFunc("GET /design/{name}", func(w http.ResponseWriter, r *http.Request) {
		b, err := design.CSS.ReadFile(r.PathValue("name"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = w.Write(b) //nolint:gosec // G705: the embedded design stylesheet, not request data
	})
	mux.HandleFunc("GET /api/state", s.hState)
	mux.HandleFunc("GET /api/screen.png", s.hScreen)
	mux.HandleFunc("POST /api/key", s.hKey)
	mux.HandleFunc("POST /api/knob", s.hKnob)
	mux.HandleFunc("POST /api/touch", s.hTouch)
	mux.HandleFunc("POST /api/signal", s.hSignal)
	mux.HandleFunc("POST /api/bus", s.hBus)
	mux.HandleFunc("POST /api/restart", func(w http.ResponseWriter, _ *http.Request) {
		s.run.Restart()
		reply(w, nil, nil)
	})
	return mux
}

// reply writes {"ok":true, ...extra} or {"ok":false,"err":...}.
func reply(w http.ResponseWriter, err error, extra map[string]any) {
	out := map[string]any{"ok": err == nil}
	maps.Copy(out, extra)
	code := http.StatusOK
	if err != nil {
		out["err"] = err.Error()
		code = http.StatusBadRequest
		if errors.Is(err, errNoSim) {
			code = http.StatusServiceUnavailable
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(out) // the client went away: nothing to do
}

func decode(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(v)
}

// queue runs device-model input commands in order and waits for them.
func (s *Server) queue(ctx context.Context, cmds []string) error {
	if s.run.ctlPath() == "" {
		return errNoSim
	}
	in := inputCmd{cmds: cmds, done: make(chan error, 1)}
	select {
	case s.input <- in:
	default:
		return errors.New("input queue full")
	}
	select {
	case err := <-in.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// State is the page's view of the bench.
type State struct {
	Run     RunState          `json:"run"`
	LEDWord uint32            `json:"led_word"`
	Lamps   []string          `json:"lamps"`
	Signals map[string]string `json:"signals"`
	WebURL  string            `json:"web_url"`
	Keys    []string          `json:"keys"`
	Knobs   []string          `json:"knobs"`
}

func (s *Server) hState(w http.ResponseWriter, r *http.Request) {
	st := State{Run: s.run.State(), WebURL: s.WebURL, Lamps: []string{}}
	for k := range keys {
		st.Keys = append(st.Keys, k)
	}
	for k := range knobs {
		st.Knobs = append(st.Knobs, k)
	}
	sort.Strings(st.Keys)
	sort.Strings(st.Knobs)
	path := s.run.ctlPath()
	if path != "" {
		lo, e1 := ctl(r.Context(), path, fmt.Sprintf("peek %#x", ledLoAddr))
		hi, e2 := ctl(r.Context(), path, fmt.Sprintf("peek %#x", ledHiAddr))
		if e1 == nil && e2 == nil {
			l, _ := strconv.ParseUint(lo, 0, 32)
			h, _ := strconv.ParseUint(hi, 0, 32)
			st.LEDWord = uint32(l&0xffff | (h&0xffff)<<16) //nolint:gosec // G115: two 16-bit halves
			st.Lamps = lampsOn(st.LEDWord)
		}
		if j, err := ctl(r.Context(), path, "sigshow"); err == nil {
			_ = json.Unmarshal([]byte(j), &st.Signals) // an older model without sigshow: no signals shown
			s.run.remember(st.Signals)
		}
	}
	if st.Signals == nil {
		st.Signals = s.run.remembered()
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(st) // the client went away: nothing to do
}

// screenMaxAge is how long one screen dump serves every viewer.
const screenMaxAge = 120 * time.Millisecond

func (s *Server) hScreen(w http.ResponseWriter, r *http.Request) {
	var b []byte
	var err error
	if r.URL.Query().Get("src") == "app" {
		b, err = s.appShot(r.Context())
	} else {
		b, err = s.simScreen(r.Context())
	}
	if err != nil {
		reply(w, err, nil)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

// simScreen is the device model's picture of the panel: the UI layer
// (fb0) blended over the waveform plane, as the PL's mixer shows it.
func (s *Server) simScreen(ctx context.Context) ([]byte, error) {
	s.shotMu.Lock()
	defer s.shotMu.Unlock()
	if time.Since(s.shotAt) < screenMaxAge && s.shotPNG != nil {
		return s.shotPNG, s.shotErr
	}
	path := s.run.ctlPath()
	if path == "" {
		return nil, errNoSim
	}
	f := filepath.Join(s.tmp, "screen.png")
	_, err := ctl(ctx, path, "shotcomp "+f)
	var b []byte
	if err == nil {
		b, err = os.ReadFile(f) //nolint:gosec // G304: simlab's own scratch file
	}
	s.shotAt, s.shotPNG, s.shotErr = time.Now(), b, err
	return b, err
}

// fetchAppScreen is the firmware's own capture, /api/screen.png.
func (s *Server) fetchAppScreen(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.WebURL, "/")+"/api/screen.png", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the app's screen: %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}

func (s *Server) hKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key string `json:"key"`
	}
	if err := decode(r, &req); err != nil {
		reply(w, err, nil)
		return
	}
	c, err := keyCmd(req.Key, s.HoldMs)
	if err == nil {
		err = s.queue(r.Context(), []string{c})
	}
	reply(w, err, nil)
}

func (s *Server) hKnob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Knob  string `json:"knob"`
		Steps int    `json:"steps"`
		Push  bool   `json:"push"`
	}
	if err := decode(r, &req); err != nil {
		reply(w, err, nil)
		return
	}
	if !req.Push && req.Steps == 0 {
		reply(w, errors.New("steps 0: give steps or push"), nil)
		return
	}
	if req.Push {
		req.Steps = 0
	}
	cmds, err := knobCmds(req.Knob, req.Steps, 30)
	if err == nil {
		err = s.queue(r.Context(), cmds)
	}
	reply(w, err, nil)
}

func (s *Server) hTouch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		X, Y int
	}
	if err := decode(r, &req); err != nil {
		reply(w, err, nil)
		return
	}
	if req.X < 0 || req.X >= 1024 || req.Y < 0 || req.Y >= 600 {
		reply(w, fmt.Errorf("touch (%d, %d) is off the 1024×600 screen", req.X, req.Y), nil)
		return
	}
	reply(w, s.queue(r.Context(), []string{fmt.Sprintf("touch %d %d %d", req.X, req.Y, s.HoldMs)}), nil)
}

// specChars are the characters a generator spec may hold: it travels in
// a control line and, for the next run, in the model's flags.
var specChars = regexp.MustCompile(`^[A-Za-z0-9.,=:+_-]*$`)

var chanName = regexp.MustCompile(`^C[1-4]$`)

func (s *Server) hSignal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ch   string `json:"ch"`
		Spec string `json:"spec"`
	}
	if err := decode(r, &req); err != nil {
		reply(w, err, nil)
		return
	}
	req.Ch = strings.ToUpper(req.Ch)
	if !chanName.MatchString(req.Ch) || req.Spec == "" || !specChars.MatchString(req.Spec) {
		reply(w, errors.New("want ch C1..C4 and spec kind,freq,vpp,offset[,key=value...]"), nil)
		return
	}
	path := s.run.ctlPath()
	if path == "" {
		reply(w, errNoSim, nil)
		return
	}
	_, err := ctl(r.Context(), path, "signals "+req.Ch+"="+req.Spec)
	if err == nil {
		s.run.remember(map[string]string{req.Ch: req.Spec})
	}
	reply(w, err, nil)
}

// BusReq places one protocol on one or several channels.
type BusReq struct {
	Kind   string            `json:"kind"`
	Lines  map[string]string `json:"lines"` // role → Cn
	Rate   float64           `json:"rate"`
	Vpp    float64           `json:"vpp"`
	Offset float64           `json:"offset"`
	Opts   map[string]string `json:"opts"`
}

// command is the device model's "bus" line.
func (b BusReq) command() (string, error) {
	if !specChars.MatchString(b.Kind) || b.Kind == "" {
		return "", fmt.Errorf("kind %q", b.Kind)
	}
	parts := []string{"bus", b.Kind}
	roles := make([]string, 0, len(b.Lines))
	for role := range b.Lines {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	for _, role := range roles {
		c := strings.ToUpper(b.Lines[role])
		if c == "" {
			continue
		}
		if !chanName.MatchString(c) || !specChars.MatchString(role) {
			return "", fmt.Errorf("line %s=%s: want C1..C4", role, c)
		}
		parts = append(parts, role+"="+c)
	}
	parts = append(parts, "rate="+strconv.FormatFloat(b.Rate, 'g', -1, 64),
		"vpp="+strconv.FormatFloat(b.Vpp, 'g', -1, 64), "offset="+strconv.FormatFloat(b.Offset, 'g', -1, 64))
	opts := make([]string, 0, len(b.Opts))
	for k := range b.Opts {
		opts = append(opts, k)
	}
	sort.Strings(opts)
	for _, k := range opts {
		v := b.Opts[k]
		if v == "" {
			continue
		}
		if !specChars.MatchString(k) || !specChars.MatchString(v) || strings.ContainsAny(k+v, ",=") {
			return "", fmt.Errorf("option %s=%s", k, v)
		}
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, " "), nil
}

func (s *Server) hBus(w http.ResponseWriter, r *http.Request) {
	var req BusReq
	if err := decode(r, &req); err != nil {
		reply(w, err, nil)
		return
	}
	cmd, err := req.command()
	if err != nil {
		reply(w, err, nil)
		return
	}
	path := s.run.ctlPath()
	if path == "" {
		reply(w, errNoSim, nil)
		return
	}
	rep, err := ctl(r.Context(), path, cmd)
	if err == nil {
		// "ok C1=spec;C2=spec"
		m := map[string]string{}
		for p := range strings.SplitSeq(strings.TrimSpace(strings.TrimPrefix(rep, "ok")), ";") {
			if c, spec, ok := strings.Cut(p, "="); ok {
				m[c] = spec
			}
		}
		s.run.remember(m)
	}
	reply(w, err, map[string]any{"cmd": cmd})
}

// remembered is a copy of the generators the next run starts with.
func (r *Runner) remembered() map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := make(map[string]string, len(r.signals))
	maps.Copy(m, r.signals)
	return m
}
