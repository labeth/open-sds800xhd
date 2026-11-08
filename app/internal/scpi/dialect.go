package scpi

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-dso-core/measure"
	"github.com/labeth/open-dso-core/sample"
	core "github.com/labeth/open-dso-core/scpi"
	"github.com/labeth/open-dso-core/scpi/meas"
)

// Identity is what *IDN? reports (spec 11 §6.6).
type Identity struct {
	Manufacturer, Model, Serial, Version string
}

// Config is what the dialect needs from the app.
type Config struct {
	Identity Identity
	Channels int
	// Measure measures one trace of a frame (the scope's code format).
	Measure func(t sample.Trace, sampleS float64) *measure.Result
	// Extensions are the board's own headers.
	Extensions []core.Extension
	// SelfCal starts the self-calibration (it runs on the owner) and
	// SelfCalState reports DOING or DONE; nil when the app has none.
	SelfCal      func() error
	SelfCalState func() string
	// TriggerDefault is the board's default stage of a trigger node on a
	// source (hal.Trigger.Default): the video level of the first entry into
	// Video comes from it; nil leaves the level as set.
	TriggerDefault func(node hal.TrigNode, src hal.Source) (hal.TrigStage, error)
	// Mask is the mask test the owner runs on the frames; nil: the :MTESt
	// commands are refused.
	Mask MaskTest
}

// specHeaders are headers spec 11's text gives whose form the inventory
// does not hold as a row: the legacy replies of §6.3.2 and *OPC (§6.4).
var specHeaders = map[string]bool{"TDIV": true, "TDIV?": true, "SARA?": true, "STOP": true, "*OPC": true}

// dialect is one engine's dialect state: the waveform transfer and the
// measurement settings that live in the host interface, not the instrument.
type dialect struct {
	cfg     Config
	missing []string // headers bound that the tables do not hold

	wfSource, wfStart, wfInterval, wfPoints int
	wfWord, wfMSB                           bool
	measSource                              int
	trig                                    trigState
	mask                                    maskState
}

// maxPoints is the largest DATA? transfer (spec 11 §7.1 :WAVeform:MAXPoint?).
const maxPoints = 5_000_000

// Dialect is the SDS800X HD's dialect. The error lists the headers it binds
// that are neither inventory rows nor spec 11 legacy forms.
// TRLC-LINKS: REQ-HD-009
func Dialect(cfg Config) (core.Dialect, error) {
	d := &dialect{cfg: cfg, wfInterval: 1, trig: newTrigState(), mask: newMaskState()}
	dl := core.Dialect{
		Name:    "SDS800X HD",
		Grammar: core.IEEE,
		Header:  core.HeaderRules{Long: true, OffDropsUnit: true},
		Errors:  errorTexts,
		Status:  core.EdgeSummaries,
	}
	dl.Commands = d.commands()
	if len(d.missing) > 0 {
		sort.Strings(d.missing)
		return dl, fmt.Errorf("scpi: headers not in the inventory: %s", strings.Join(d.missing, " "))
	}
	return dl, nil
}

// bind is a command whose header the inventory (or spec 11's legacy
// replies) must hold in that form.
func (d *dialect) bind(header string, h core.Handler) core.Command {
	base := strings.TrimSuffix(header, "?")
	r, ok := inventory[base]
	switch {
	case specHeaders[header]:
	case !ok:
		d.missing = append(d.missing, header)
	case strings.HasSuffix(header, "?") && !r.queries(), !strings.HasSuffix(header, "?") && !r.commands():
		d.missing = append(d.missing, header)
	}
	return core.Command{Pattern: header, Handler: h}
}

// choices are a row's alternatives for <name>.
func (d *dialect) choices(header, name string) []string {
	c := inventory[header].choices(name)
	if len(c) == 0 {
		d.missing = append(d.missing, header+" <"+name+">")
	}
	return c
}

// --- parameters, by the product handler rules of spec 11 §6.2 ---

// one checks that the unit has a parameter: a missing one is -200.
func one(c *core.Call) error {
	if c.Count() == 0 {
		return core.ErrFailed
	}
	return nil
}

// number reads a numeric setting; a non-numeric value is ignored without
// an error (ok false).
func number(c *core.Call) (float64, bool, error) {
	if err := one(c); err != nil {
		return 0, false, err
	}
	t, _, _ := c.Param(true)
	if t.Kind != core.TokNumber && t.Kind != core.TokNonDec {
		return 0, false, nil
	}
	m, _, ok := core.ResolveUnit(t.Unit)
	if !ok {
		return 0, false, c.Error(core.ErrInvalidSuffix)
	}
	return t.Num * m, true, nil
}

// choice reads an enumerated setting: missing -200, unknown -224.
func choice(c *core.Call, choices ...string) (int, error) {
	if c.Remaining() == 0 {
		return 0, core.ErrFailed
	}
	i, _, err := c.Choice(true, choices...)
	return i, err
}

// onOff reads ON/OFF (or 1/0).
func onOff(c *core.Call) (bool, error) {
	i, err := choice(c, "OFF", "ON", "0", "1")
	return i%2 == 1, err
}

// state is ON or OFF.
func state(on bool) string {
	if on {
		return "ON"
	}
	return "OFF"
}

// channel is the call's channel suffix, 0-based; past the model is -200.
func (d *dialect) channel(c *core.Call) (int, error) {
	n := c.N(0)
	if n < 1 || n > d.cfg.Channels {
		return 0, core.ErrFailed
	}
	return n - 1, nil
}

// source reads "C<n>" for an existing channel; anything else is -224.
func (d *dialect) source(c *core.Call) (int, error) {
	if err := one(c); err != nil {
		return 0, err
	}
	t, _, _ := c.Param(true)
	s := strings.ToUpper(t.Str)
	if n, err := strconv.Atoi(strings.TrimPrefix(s, "C")); err == nil && strings.HasPrefix(s, "C") && n >= 1 && n <= d.cfg.Channels {
		return n - 1, nil
	}
	return 0, c.Error(core.ErrIllegalParamValue)
}

// change stages fn on the instrument; a refusal is -222.
func change(c *core.Call, fn func(s *core.State) error) error {
	if err := c.Instrument().Change(fn); err != nil {
		if errors.Is(err, core.ErrReported) {
			return err
		}
		return c.Error(core.ErrDataOutOfRange)
	}
	return nil
}

// runControl is the instrument model's run control (instrument.Controls):
// its setters carry the meaning of the press they stand for, which a raw
// Change of Sweep and Running cannot (a NORM written after a single shot
// would read as RUN and bring back the mode the shot replaced).
type runControl interface {
	SetRunning(on bool)
	SetNorm(on bool)
	SetSingle()
}

// setRun sets the trigger mode and run state as the panel's keys do: STOP
// (running false), SINGLE (a shot armed), or AUTO/NORM running. An
// instrument without the explicit setters takes the raw change.
func setRun(c *core.Call, sw hal.Sweep, running bool) error {
	rc, ok := c.Instrument().(runControl)
	if !ok {
		return change(c, func(s *core.State) error {
			s.Running = running
			if running {
				s.Sweep = sw
			}
			return nil
		})
	}
	switch {
	case !running:
		rc.SetRunning(false)
	case sw == hal.Single:
		rc.SetSingle()
	default:
		rc.SetNorm(sw == hal.NormalSweep)
		rc.SetRunning(true)
	}
	return nil
}

// changeCh stages fn on channel ch.
func changeCh(c *core.Call, ch int, fn func(ch *hal.Channel)) error {
	return change(c, func(s *core.State) error {
		if ch >= len(s.Channels) {
			return core.ErrFailed
		}
		fn(&s.Channels[ch])
		return nil
	})
}

// commands is the dialect's command table.
func (d *dialect) commands() []core.Command {
	id := d.cfg.Identity
	var cmds []core.Command
	commons := core.Commons{
		IDN: func() string { return strings.Join([]string{id.Manufacturer, id.Model, id.Serial, id.Version}, ",") },
		Reset: func(c *core.Call) error {
			return change(c, func(s *core.State) error {
				*s = Defaults(d.cfg.Channels)
				return nil
			})
		},
		// No option is installed in this build: one 0 per option type
		// (spec 11 §6.4).
		Option: func() string { return "0,0,0" },
	}
	for _, cmd := range commons.Commands("*IDN?", "*RST", "*CLS", "*ESE", "*ESE?", "*SRE", "*SRE?", "*ESR?",
		"*STB?", "*OPC", "*OPC?", "*WAI", "*TST?", "*OPT?") {
		cmds = append(cmds, d.bind(cmd.Pattern, cmd.Handler))
	}
	cmds = append(cmds, core.System()...)
	for _, cmd := range core.LegacyStatus("INR?", "ALST?") {
		cmds = append(cmds, d.bind(cmd.Pattern, cmd.Handler))
	}
	cmds = append(cmds, d.bind(":FORMat:DATA", func(c *core.Call) error {
		i, err := choice(c, d.choices(":FORMat:DATA", "option")...)
		if err != nil {
			return err
		}
		digits := []int{7, 14, 3}[i]
		if c.Remaining() > 0 {
			n, _, err := c.Int(true)
			if err != nil {
				return err
			}
			if i != 2 || n < 1 {
				return c.Error(core.ErrIllegalParamValue)
			}
			digits = int(n)
		}
		c.SetDigits(digits)
		return nil
	}), d.bind(":FORMat:DATA?", func(c *core.Call) error {
		c.Text("CUSTOM," + strconv.Itoa(c.Digits()))
		return nil
	}))
	cmds = append(cmds, d.timebase()...)
	cmds = append(cmds, d.channels()...)
	cmds = append(cmds, d.acquire()...)
	cmds = append(cmds, d.trigger()...)
	cmds = append(cmds, d.maskTest()...)
	cmds = append(cmds, d.waveform()...)
	cmds = append(cmds, d.measure()...)
	cmds = append(cmds, d.legacy()...)
	if d.cfg.SelfCal != nil && d.cfg.SelfCalState != nil {
		cmds = append(cmds, d.bind(":SYSTem:SELFCal", func(c *core.Call) error {
			if err := d.cfg.SelfCal(); err != nil {
				return c.Error(core.ErrSettingsConflict)
			}
			return nil
		}), d.bind(":SYSTem:SELFCal?", func(c *core.Call) error {
			c.Text(d.cfg.SelfCalState())
			return nil
		}))
	}
	for _, cmd := range core.Extensions(d.cfg.Extensions) {
		cmds = append(cmds, d.bind(cmd.Pattern, cmd.Handler))
	}
	return cmds
}

// Defaults is the instrument's state after *RST: the built-in setup
// (spec 09 §2.2) with CH1 on, as the spec recommends for a replacement.
func Defaults(channels int) core.State {
	s := core.State{
		TimeDivS: 200e-9, Depth: 10_000_000, Sweep: hal.Auto, Running: true,
	}
	for range channels {
		s.Channels = append(s.Channels, hal.Channel{VoltsDiv: 1, Probe: 1, Coupling: hal.DC, Impedance: 1e6})
	}
	if channels > 0 {
		s.Channels[0].On = true
	}
	triggerDefaults(&s)
	return s
}

func (d *dialect) timebase() []core.Command {
	return []core.Command{
		d.bind(":TIMebase:SCALe", func(c *core.Call) error {
			v, ok, err := number(c)
			if err != nil || !ok {
				return err
			}
			return change(c, func(s *core.State) error { s.TimeDivS = v; return nil })
		}),
		d.bind(":TIMebase:SCALe?", func(c *core.Call) error { c.NR3(c.Instrument().State().TimeDivS); return nil }),
		// :TIMebase:DELay is positive with the trigger right of the screen
		// centre: the centre is that long before the trigger.
		d.bind(":TIMebase:DELay", func(c *core.Call) error {
			v, ok, err := number(c)
			if err != nil || !ok {
				return err
			}
			return change(c, func(s *core.State) error { s.DelayS = -v; return nil })
		}),
		d.bind(":TIMebase:DELay?", func(c *core.Call) error { c.NR3(-c.Instrument().State().DelayS + 0); return nil }),
	}
}

func (d *dialect) channels() []core.Command {
	chq := func(header string, reply func(c *core.Call, ch hal.Channel)) core.Command {
		return d.bind(header, func(c *core.Call) error {
			n, err := d.channel(c)
			if err != nil {
				return err
			}
			reply(c, c.Instrument().State().Channels[n])
			return nil
		})
	}
	chset := func(header string, set func(c *core.Call) (func(*hal.Channel), error)) core.Command {
		return d.bind(header, func(c *core.Call) error {
			n, err := d.channel(c)
			if err != nil {
				return err
			}
			fn, err := set(c)
			if err != nil || fn == nil {
				return err
			}
			return changeCh(c, n, fn)
		})
	}
	numeric := func(apply func(ch *hal.Channel, v float64)) func(c *core.Call) (func(*hal.Channel), error) {
		return func(c *core.Call) (func(*hal.Channel), error) {
			v, ok, err := number(c)
			if err != nil || !ok {
				return nil, err
			}
			return func(ch *hal.Channel) { apply(ch, v) }, nil
		}
	}
	couplings := d.choices(":CHANnel#:COUPling", "coupling_mode")
	bwl := d.choices(":CHANnel#:BWLimit", "bwlimit")
	imp := d.choices(":CHANnel#:IMPedance", "impedance")
	return []core.Command{
		chset(":CHANnel#:SCALe", numeric(func(ch *hal.Channel, v float64) { ch.VoltsDiv = v })),
		chq(":CHANnel#:SCALe?", func(c *core.Call, ch hal.Channel) { c.NR3(ch.VoltsDiv) }),
		chset(":CHANnel#:OFFSet", numeric(func(ch *hal.Channel, v float64) { ch.OffsetV = v })),
		chq(":CHANnel#:OFFSet?", func(c *core.Call, ch hal.Channel) { c.NR3(ch.OffsetV) }),
		chset(":CHANnel#:SKEW", func(c *core.Call) (func(*hal.Channel), error) {
			v, ok, err := number(c)
			if err != nil || !ok {
				return nil, err
			}
			if v < -1e-7 || v > 1e-7 {
				return nil, c.Error(core.ErrDataOutOfRange)
			}
			return func(ch *hal.Channel) { ch.SkewS = v }, nil
		}),
		chq(":CHANnel#:SKEW?", func(c *core.Call, ch hal.Channel) { c.NR3(ch.SkewS) }),
		chset(":CHANnel#:SWITch", func(c *core.Call) (func(*hal.Channel), error) {
			on, err := onOff(c)
			return func(ch *hal.Channel) { ch.On = on }, err
		}),
		chq(":CHANnel#:SWITch?", func(c *core.Call, ch hal.Channel) { c.Text(state(ch.On)) }),
		chset(":CHANnel#:INVert", func(c *core.Call) (func(*hal.Channel), error) {
			on, err := onOff(c)
			return func(ch *hal.Channel) { ch.Invert = on }, err
		}),
		chq(":CHANnel#:INVert?", func(c *core.Call, ch hal.Channel) { c.Text(state(ch.Invert)) }),
		chset(":CHANnel#:COUPling", func(c *core.Call) (func(*hal.Channel), error) {
			i, err := choice(c, couplings...)
			return func(ch *hal.Channel) { ch.Coupling = [...]hal.Coupling{hal.DC, hal.AC, hal.GND}[i] }, err
		}),
		chq(":CHANnel#:COUPling?", func(c *core.Call, ch hal.Channel) {
			c.Text([...]string{"DC", "AC", "GND"}[min(max(int(ch.Coupling), 0), 2)])
		}),
		// The hardware limit is 20 MHz; 200M raises no error and leaves the
		// setting (spec 11 §6.7).
		chset(":CHANnel#:BWLimit", func(c *core.Call) (func(*hal.Channel), error) {
			i, err := choice(c, bwl...)
			if err != nil || bwl[i] == "200M" {
				return nil, err
			}
			return func(ch *hal.Channel) { ch.BWLimit = map[string]float64{"FULL": 0, "20M": 20e6}[bwl[i]] }, nil
		}),
		chq(":CHANnel#:BWLimit?", func(c *core.Call, ch hal.Channel) {
			if ch.BWLimit > 0 {
				c.Text("20M")
			} else {
				c.Text("FULL")
			}
		}),
		// A 1 MΩ input only: FIFTy is accepted and ignored (spec 11 §6.7).
		chset(":CHANnel#:IMPedance", func(c *core.Call) (func(*hal.Channel), error) {
			_, err := choice(c, imp...)
			return nil, err
		}),
		chq(":CHANnel#:IMPedance?", func(c *core.Call, _ hal.Channel) { c.Text("ONEMeg") }),
		chset(":CHANnel#:PROBe", func(c *core.Call) (func(*hal.Channel), error) {
			i, err := choice(c, d.choices(":CHANnel#:PROBe", "attenuation")...)
			if err != nil {
				return nil, err
			}
			v := 1.0
			if i == 1 {
				x, ok, err := number(c)
				if err != nil || !ok {
					return nil, err
				}
				v = x
			}
			if !(v > 0) || math.IsInf(v, 0) {
				return nil, c.Error(core.ErrDataOutOfRange)
			}
			// The V/div and offset stay the same at the BNC.
			return func(ch *hal.Channel) {
				if ch.Probe > 0 {
					ch.VoltsDiv *= v / ch.Probe
					ch.OffsetV *= v / ch.Probe
				}
				ch.Probe = v
			}, nil
		}),
		chq(":CHANnel#:PROBe?", func(c *core.Call, ch hal.Channel) { c.NR3(ch.Probe) }),
	}
}

// depths are the memory depths by their spelling (spec 11 §6.7).
var depths = []struct {
	name string
	n    int
}{{"10k", 10_000}, {"100k", 100_000}, {"1M", 1_000_000}, {"10M", 10_000_000}, {"25M", 25_000_000}, {"50M", 50_000_000}, {"100M", 100_000_000}}

func depthName(n int) string {
	for _, d := range depths {
		if d.n == n {
			return d.name
		}
	}
	return strconv.Itoa(n)
}

func (d *dialect) acquire() []core.Command {
	return []core.Command{
		d.bind(":ACQuire:MDEPth", func(c *core.Call) error {
			names := make([]string, len(depths))
			for i, x := range depths {
				names[i] = x.name
			}
			i, err := choice(c, names...)
			if err != nil {
				return err
			}
			return change(c, func(s *core.State) error { s.Depth = depths[i].n; return nil })
		}),
		d.bind(":ACQuire:MDEPth?", func(c *core.Call) error {
			c.Text(depthName(c.Instrument().State().Depth))
			return nil
		}),
		// Only NORMal and PEAK are acquisition modes here; AVERage, ERES and
		// HIRES are parsed (their second value must be valid) and ignored,
		// and TYPE? keeps the mode (spec 11 §6.7.1).
		d.bind(":ACQuire:TYPE", func(c *core.Call) error {
			i, err := choice(c, "NORMal", "PEAK", "AVERage", "ERES", "HIRES")
			if err != nil {
				return err
			}
			switch i {
			case 0, 1:
				return change(c, func(s *core.State) error {
					s.Mode = [...]hal.AcqMode{hal.Normal, hal.PeakDetect}[i]
					return nil
				})
			case 2:
				_, err = choice(c, d.choices(":ACQuire:TYPE", "times")...)
			default:
				_, err = choice(c, d.choices(":ACQuire:TYPE", "bits")...)
			}
			return err
		}),
		d.bind(":ACQuire:TYPE?", func(c *core.Call) error {
			if c.Instrument().State().Mode == hal.PeakDetect {
				c.Text("PEAK")
			} else {
				c.Text("NORMal")
			}
			return nil
		}),
		d.bind(":ACQuire:SRATe?", func(c *core.Call) error {
			rate, ok := frameRate(c)
			if !ok {
				return core.ErrFailed
			}
			c.NR3(rate)
			return nil
		}),
		d.bind(":ACQuire:POINts?", func(c *core.Call) error {
			n := -1
			c.Instrument().WithFrame(func(f *sample.Frame) {
				if f != nil {
					n = f.Len()
				}
			})
			if n < 0 {
				return core.ErrFailed
			}
			c.NR3(float64(n))
			return nil
		}),
	}
}

// frameRate is the last frame's sample rate.
func frameRate(c *core.Call) (float64, bool) {
	rate := 0.0
	c.Instrument().WithFrame(func(f *sample.Frame) {
		if f != nil && f.SampleS > 0 {
			rate = 1 / f.SampleS
		}
	})
	return rate, rate > 0
}

// force triggers once.
func (d *dialect) force(c *core.Call) error {
	if err := c.Instrument().Force(); err != nil {
		return c.Error(core.ErrSettingsConflict)
	}
	return nil
}

// trigSource parses an edge source name.
func (d *dialect) trigSource(s string) (hal.Source, bool) {
	switch s {
	case "LINE":
		return hal.Source{Kind: hal.ACLine}, true
	case "EX", "EX5":
		return hal.Source{Kind: hal.External}, true
	}
	if n, err := strconv.Atoi(strings.TrimPrefix(s, "C")); err == nil && strings.HasPrefix(s, "C") && n >= 1 && n <= d.cfg.Channels {
		return hal.Source{Kind: hal.Analog, Index: n - 1}, true
	}
	return hal.Source{}, false
}

// sourceName spells a trigger source.
func sourceName(s hal.Source) string {
	switch s.Kind {
	case hal.ACLine:
		return "LINE"
	case hal.External:
		return "EX"
	case hal.Analog, hal.Digital:
		// numbered below
	}
	return "C" + strconv.Itoa(s.Index+1)
}

func (d *dialect) measure() []core.Command {
	params := d.choices(":MEASure:SIMPle:VALue", "type")
	return []core.Command{
		d.bind(":MEASure:SIMPle:SOURce", func(c *core.Call) error {
			n, err := d.source(c)
			if err == nil {
				d.measSource = n
			}
			return err
		}),
		d.bind(":MEASure:SIMPle:SOURce?", func(c *core.Call) error {
			c.Text("C" + strconv.Itoa(d.measSource+1))
			return nil
		}),
		d.bind(":MEASure:SIMPle:VALue?", func(c *core.Call) error {
			i, err := choice(c, params...)
			if err != nil {
				return err
			}
			r := d.measureCh(c, d.measSource)
			if r == nil {
				return core.ErrFailed
			}
			name := strings.ToUpper(params[i])
			if name == "ALL" {
				for k := range meas.Params {
					p := &meas.Params[k]
					c.Text(p.Name + "," + reading(c, p, r))
				}
				return nil
			}
			p, ok := meas.Lookup(name)
			if !ok {
				return c.Error(core.ErrSettingsConflict) // a parameter the measurement engine does not compute
			}
			c.Text(reading(c, p, r))
			return nil
		}),
	}
}

// reading is a parameter's NR3 value, or the no-reading mark.
func reading(c *core.Call, p *meas.Param, r *measure.Result) string {
	if v, ok := p.Reading(r); ok {
		return core.FormatNR3(v, c.Digits())
	}
	return meas.NoReading
}

// measureCh measures channel ch on the last frame; nil without one.
func (d *dialect) measureCh(c *core.Call, ch int) *measure.Result {
	var r *measure.Result
	c.Instrument().WithFrame(func(f *sample.Frame) {
		if f == nil || d.cfg.Measure == nil {
			return
		}
		for _, t := range f.Traces {
			if t.Ch == ch {
				r = d.cfg.Measure(t, f.SampleS)
			}
		}
	})
	return r
}

// sweepOf is the trigger mode a query names: SINGLE while a shot is armed.
func sweepOf(s core.State) hal.Sweep {
	if s.Single {
		return hal.Single
	}
	return s.Sweep
}
