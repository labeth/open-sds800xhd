package scpi

import (
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-dso-core/instrument"
	core "github.com/labeth/open-dso-core/scpi"
)

// The trigger commands of spec 11 bind every trigger type's headers onto
// the instrument state's trigger settings (instrument.TrigParams): each
// :TRIGger:<type>:… header reads and writes its type's own settings
// whether the type runs or not, as spec 11 keeps them per type,
// and :TRIGger:TYPE picks the type that runs. What the board cannot serve
// is refused when the owner programs it, and the refusal reaches the error
// queue as -221 with the board's reason.

// trigTypes are :TRIGger:TYPE's choices with the instrument's types; the
// protocols spec 05 §10.7 gives no trigger hardware have none.
var trigTypes = map[string]instrument.TrigType{
	edgeName: instrument.TrigEdge, "PULSE": instrument.TrigPulse, "SLOPE": instrument.TrigSlope,
	"INTERVAL": instrument.TrigInterval, "PATTERN": instrument.TrigPattern, "RUNT": instrument.TrigRunt,
	"WINDOW": instrument.TrigWindow, "DROPOUT": instrument.TrigDropout, "VIDEO": instrument.TrigVideo,
	"QUALIFIED": instrument.TrigQualified, "NEDGE": instrument.TrigNthEdge, "DELAY": instrument.TrigDelay,
	"SHOLD": instrument.TrigSetupHold, "IIC": instrument.TrigI2C, "SPI": instrument.TrigSPI,
	"UART": instrument.TrigUART, "LIN": instrument.TrigLIN, "CAN": instrument.TrigCAN,
}

// edgeName is the edge trigger's type as spec 11 spells it.
const edgeName = "EDGE"

// Time limits of the trigger (spec 11 §6.7).
const (
	minTrigS    = 2e-9
	maxTrigS    = 20
	minHoldoffS = 8e-9
	maxHoldoffS = 30
)

// holdoffSet is one type's holdoff as the host interface keeps it: its
// form, its time (kept while off) and the stored, inert event count and
// start (spec 11 §6.7.1).
type holdoffSet struct {
	form   int // 0 OFF, 1 EVENts, 2 TIME
	timeS  float64
	events int64
	start  int
}

// lineSet is a protocol line the host interface keeps beside the one the
// trigger watches (SPI's MISO/MOSI and CS/NCS, UART's RX/TX).
type lineSet struct {
	src   string
	level float64
}

// trigState is the trigger settings the host interface keeps that the
// instrument state does not: per-type holdoff forms, the inactive protocol
// lines, the window's relative form, the input impedance the EXT path
// echoes.
type trigState struct {
	holdoff         map[instrument.TrigType]*holdoffSet
	miso, mosi      lineSet
	cs, ncs         lineSet
	rx, tx          lineSet
	spiMOSI         bool // the SPI trigger watches MOSI
	uartTX          bool // the UART trigger watches TX
	spiSelect       int  // 0 NCS, 1 CS, 2 TIMeout
	windowRelative  bool
	edgeImpedance50 bool
	videoLevelSet   bool // a video level was set since start-up (spec 05 §5.6)
}

// newTrigState is the host interface's trigger settings after start-up
// (the appendix's defaults).
func newTrigState() trigState {
	ts := trigState{holdoff: map[instrument.TrigType]*holdoffSet{}}
	for t := range instrument.NumTrigTypes {
		ts.holdoff[t] = &holdoffSet{timeS: 80e-9, events: 1, start: 1}
	}
	l := lineSet{src: "C1", level: 1.6}
	ts.miso, ts.mosi, ts.cs, ts.ncs, ts.rx, ts.tx = l, l, l, l, l, l
	return ts
}

// tb binds one trigger type's headers under prefix.
type tb struct {
	d      *dialect
	t      instrument.TrigType
	prefix string
	cmds   *[]core.Command
}

// add binds header (with its query when the inventory has it).
func (b tb) add(sub string, set, query core.Handler) {
	h := b.prefix + ":" + sub
	if set != nil {
		*b.cmds = append(*b.cmds, b.d.bind(h, set))
	}
	if query != nil {
		*b.cmds = append(*b.cmds, b.d.bind(h+"?", query))
	}
}

// get is setting id of the type in the instrument state.
func (b tb) get(c *core.Call, id string, ch int) instrument.ParamValue {
	v, _ := c.Instrument().State().TrigParam(b.t, id, ch)
	return v
}

// trigSet stages setting id of type t: a value the setting does not take
// is -224, a refusal (a type the trigger lacks) -221 with its reason.
func trigSet(c *core.Call, t instrument.TrigType, id string, v instrument.ParamValue) error {
	return trigChange(c, func(s *core.State) error { return s.SetTrigParam(t, id, v) })
}

// trigChange stages fn on the instrument with the trigger's error rules.
func trigChange(c *core.Call, fn func(s *core.State) error) error {
	err := c.Instrument().Change(fn)
	switch {
	case err == nil, errors.Is(err, core.ErrReported):
		return err
	case isRefusal(err):
		return c.Refused(err)
	}
	return c.Error(core.ErrIllegalParamValue)
}

func isRefusal(err error) bool {
	_, ok := hal.AsRefusal(err)
	return ok
}

// set stages setting id from a value already read.
func (b tb) set(c *core.Call, id string, v instrument.ParamValue) error {
	return trigSet(c, b.t, id, v)
}

// choice binds an enumerated setting: names are the header's SCPI
// choices, values the setting's choices they stand for (in order).
func (b tb) choice(sub, id string, names, values []string) {
	b.add(sub, func(c *core.Call) error {
		i, err := choice(c, names...)
		if err != nil {
			return err
		}
		return b.set(c, id, instrument.ParamValue{Text: values[i]})
	}, func(c *core.Call) error {
		v := b.get(c, id, 0)
		for i, x := range values {
			if x == v.Text {
				c.Text(names[i])
				return nil
			}
		}
		c.Text(names[0])
		return nil
	})
}

// number binds a numeric setting held to [lo, hi] (-222 outside).
func (b tb) number(sub, id string, lo, hi float64) {
	b.add(sub, func(c *core.Call) error {
		v, ok, err := number(c)
		if err != nil || !ok {
			return err
		}
		if v < lo || v > hi {
			return c.Error(core.ErrDataOutOfRange)
		}
		return b.set(c, id, instrument.ParamValue{Num: v})
	}, func(c *core.Call) error { c.NR3(b.get(c, id, 0).Num); return nil })
}

// whole binds a whole-number setting held to [lo, hi].
func (b tb) whole(sub, id string, lo, hi int64) {
	b.add(sub, func(c *core.Call) error {
		v, ok, err := c.Int(true)
		if err != nil || !ok {
			return err
		}
		if v < lo || v > hi {
			return c.Error(core.ErrDataOutOfRange)
		}
		return b.set(c, id, instrument.ParamValue{Num: float64(v)})
	}, func(c *core.Call) error { c.NR1(int64(b.get(c, id, 0).Num)); return nil })
}

// level binds a level held to ±4.1 divisions about the centre of the
// source named by setting srcID (spec 11 §6.7).
func (b tb) level(sub, id, srcID string) {
	b.add(sub, func(c *core.Call) error {
		v, ok, err := number(c)
		if err != nil || !ok {
			return err
		}
		return trigChange(c, func(s *core.State) error {
			src, _ := s.TrigParam(b.t, srcID, 0)
			return s.SetTrigParam(b.t, id, instrument.ParamValue{Num: clampLevel(*s, src.Text, v)})
		})
	}, func(c *core.Call) error { c.NR3(b.get(c, id, 0).Num); return nil })
}

// clampLevel holds v to ±4.1 divisions of source name's channel.
func clampLevel(s core.State, name string, v float64) float64 {
	src, err := instrument.ParseSource(name, len(s.Channels))
	if err != nil || src.Kind != hal.Analog {
		return v
	}
	ch := s.Channels[src.Index]
	return math.Max(-4.1*ch.VoltsDiv-ch.OffsetV, math.Min(4.1*ch.VoltsDiv-ch.OffsetV, v))
}

// source binds a source setting: a channel that is off, EX and EX5 are
// refused without an error and the source stays (spec 11 §6.7.1); LINE
// only where line is set; D<d> reaches the board, which refuses it.
func (b tb) source(sub, id string, line bool) {
	b.add(sub, func(c *core.Call) error {
		if err := one(c); err != nil {
			return err
		}
		t, _, _ := c.Param(true)
		name := strings.ToUpper(t.Str)
		switch {
		case name == "EX" || name == "EX5":
			return nil
		case name == "LINE" && !line:
			return c.Error(core.ErrIllegalParamValue)
		}
		src, err := instrument.ParseSource(name, b.d.cfg.Channels)
		if err != nil || src.Kind == hal.External {
			return c.Error(core.ErrIllegalParamValue)
		}
		return trigChange(c, func(s *core.State) error {
			if src.Kind == hal.Analog && !s.Channels[src.Index].On {
				return nil
			}
			return s.SetTrigParam(b.t, id, instrument.ParamValue{Text: name})
		})
	}, func(c *core.Call) error { c.Text(b.get(c, id, 0).Text); return nil })
}

// onOff binds an ON/OFF setting.
func (b tb) onOff(sub, id string) {
	b.add(sub, func(c *core.Call) error {
		on, err := onOff(c)
		if err != nil {
			return err
		}
		return b.set(c, id, instrument.ParamValue{Num: b2f(on)})
	}, func(c *core.Call) error { c.Text(state(b.get(c, id, 0).Num != 0)); return nil })
}

// bit binds a 0/1 setting.
func (b tb) bit(sub, id string) {
	b.add(sub, func(c *core.Call) error {
		i, err := choice(c, "0", "1")
		if err != nil {
			return err
		}
		return b.set(c, id, instrument.ParamValue{Num: float64(i)})
	}, func(c *core.Call) error { c.NR1(int64(b.get(c, id, 0).Num)); return nil })
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// The choices' spellings.
var (
	slopeNames    = []string{"RISing", "FALLing", "ALTernate"}
	slopeValues   = []string{"rising", "falling", "either"}
	polarityNames = []string{"POSitive", "NEGative"}
	limitNames    = []string{"LESSthan", "GREATerthan", "INNer", "OUTer"}
	limitValues   = []string{"less", "greater", "inside", "outside"}
	couplingNames = []string{"DC", "AC", "LFREJect", "HFREJect"}
	couplingVals  = []string{"dc", "ac", "lf_reject", "hf_reject"}
	compareNames  = []string{"EQUal", "GREaterthan", "LESSthan"}
	compareValues = []string{"equal", "greater", "less"}
	orderNames    = []string{"LSB", "MSB"}
	orderValues   = []string{"lsb", "msb"}
)

// slope binds a slope among the first n of rising, falling, alternate.
func (b tb) slope(sub, id string, n int) {
	b.choice(sub, id, slopeNames[:n], slopeValues[:n])
}

// limits binds LIMit, TLOWer and TUPPer.
func (b tb) limits() {
	b.choice("LIMit", "limit", limitNames, limitValues)
	b.number("TLOWer", "lower", minTrigS, maxTrigS)
	b.number("TUPPer", "upper", minTrigS, maxTrigS)
}

// holdoff binds HOLDoff, HLDTime, HLDEVent and HSTart: EVENts acts as TIME
// with the stored time; the event count and start are stored and inert
// (spec 11 §6.7.1).
func (b tb) holdoff() {
	h := b.d.trig.holdoff[b.t]
	forms := []string{"OFF", "EVENts", "TIME"}
	apply := func(c *core.Call) error {
		v := 0.0
		if h.form != 0 {
			v = h.timeS
		}
		return b.set(c, "holdoff", instrument.ParamValue{Num: v})
	}
	b.add("HOLDoff", func(c *core.Call) error {
		i, err := choice(c, forms...)
		if err != nil {
			return err
		}
		h.form = i
		return apply(c)
	}, func(c *core.Call) error {
		if b.get(c, "holdoff", 0).Num == 0 {
			h.form = 0
		}
		c.Text(forms[h.form])
		return nil
	})
	b.add("HLDTime", func(c *core.Call) error {
		v, ok, err := number(c)
		if err != nil || !ok {
			return err
		}
		if v < minHoldoffS || v > maxHoldoffS {
			return c.Error(core.ErrDataOutOfRange)
		}
		h.timeS = v
		return apply(c)
	}, func(c *core.Call) error { c.NR3(h.timeS); return nil })
	b.add("HLDEVent", func(c *core.Call) error {
		v, ok, err := c.Int(true)
		if err != nil || !ok {
			return err
		}
		if v < 1 || v > 100_000_000 {
			return c.Error(core.ErrDataOutOfRange)
		}
		h.events = v
		return nil
	}, func(c *core.Call) error { c.NR1(h.events); return nil })
	starts := []string{"LAST_TRIG", "ACQ_START"}
	b.add("HSTart", func(c *core.Call) error {
		i, err := choice(c, starts...)
		if err == nil {
			h.start = i
		}
		return err
	}, func(c *core.Call) error { c.Text(starts[h.start]); return nil })
}

// common binds a type's coupling, noise reject and holdoff.
func (b tb) common(coupling bool) {
	if coupling {
		b.choice("COUPling", "coupling", couplingNames, couplingVals)
	}
	b.onOff("NREJect", "noise_reject")
	b.holdoff()
}

// trigger binds the :TRIGger subsystem.
func (d *dialect) trigger() []core.Command {
	var cmds []core.Command
	cmds = append(cmds, d.runControl()...)
	cmds = append(cmds, d.bind(":TRIGger:TYPE", d.setTrigType), d.bind(":TRIGger:TYPE?", func(c *core.Call) error {
		c.Text(d.typeName(c.Instrument().State().Trigger.Type))
		return nil
	}))
	pre := func(name string, t instrument.TrigType) tb {
		return tb{d: d, t: t, prefix: ":TRIGger:" + name, cmds: &cmds}
	}
	d.edgeTypes(pre)
	d.patternTypes(pre)
	d.protocolTypes(pre)
	return cmds
}

// runControl is MODE, RUN, STOP and STATus.
func (d *dialect) runControl() []core.Command {
	modes := d.choices(":TRIGger:MODE", "mode")
	q := func(header string, reply func(c *core.Call, s core.State)) core.Command {
		return d.bind(header, func(c *core.Call) error { reply(c, c.Instrument().State()); return nil })
	}
	return []core.Command{
		d.bind(":TRIGger:MODE", func(c *core.Call) error {
			i, err := choice(c, modes...)
			if err != nil {
				return err
			}
			if strings.EqualFold(modes[i], "FTRIG") { // one-shot: the mode stays
				return d.force(c)
			}
			sw := map[string]hal.Sweep{"SINGLE": hal.Single, "NORMAL": hal.NormalSweep, "AUTO": hal.Auto}[strings.ToUpper(modes[i])]
			return change(c, func(s *core.State) error { s.Sweep, s.Running = sw, true; return nil })
		}),
		q(":TRIGger:MODE?", func(c *core.Call, s core.State) {
			c.Text(map[hal.Sweep]string{hal.Auto: "AUTO", hal.NormalSweep: "NORMal", hal.Single: "SINGle"}[sweepOf(s)])
		}),
		d.bind(":TRIGger:RUN", func(c *core.Call) error {
			return change(c, func(s *core.State) error { s.Running = true; return nil })
		}),
		d.bind(":TRIGger:STOP", func(c *core.Call) error {
			return change(c, func(s *core.State) error { s.Running = false; return nil })
		}),
		d.bind(":TRIGger:STATus?", func(c *core.Call) error {
			c.Text(c.Instrument().Acquisition().String())
			return nil
		}),
	}
}

// typeName is type t as :TRIGger:TYPE? answers it.
func (d *dialect) typeName(t instrument.TrigType) string {
	for _, n := range d.choices(":TRIGger:TYPE", "type") {
		if tt, ok := trigTypes[strings.ToUpper(n)]; ok && tt == t {
			return n
		}
	}
	return edgeName
}

// setTrigType picks the trigger type. A protocol without trigger hardware
// (spec 05 §10.7) and a type the trigger cannot run are refused with the
// reason (-221); the type stays. The first entry into Video after start-up
// with no video level set takes the board's sync-tip level (spec 05 §5.6).
func (d *dialect) setTrigType(c *core.Call) error {
	names := d.choices(":TRIGger:TYPE", "type")
	i, err := choice(c, names...)
	if err != nil {
		return err
	}
	t, ok := trigTypes[strings.ToUpper(names[i])]
	if !ok {
		return c.Refused(hal.Refuse(names[i]+" trigger", "the factory PL image has no trigger hardware for this protocol: decode only"))
	}
	return trigChange(c, func(s *core.State) error {
		if t == instrument.TrigVideo && !d.trig.videoLevelSet && d.cfg.TriggerDefault != nil {
			src := s.Trigger.Configs[t].Stages[0].Source
			if st, err := d.cfg.TriggerDefault(hal.NodeVideo, src); err == nil {
				if err := s.SetTrigParam(t, "level", instrument.ParamValue{Num: st.Levels.High}); err != nil {
					return err
				}
				d.trig.videoLevelSet = true
			}
		}
		s.Trigger.Type = t
		return nil
	})
}

// edgeTypes binds the single-input and qualified types.
func (d *dialect) edgeTypes(pre func(string, instrument.TrigType) tb) {
	e := pre("EDGE", instrument.TrigEdge)
	e.source("SOURce", "source", true)
	e.slope("SLOPe", "slope", 3)
	e.level("LEVel", "level", "source")
	e.common(true)
	imp := []string{"ONEMeg", "FIFTy"}
	e.add("IMPedance", func(c *core.Call) error {
		i, err := choice(c, imp...)
		if err == nil {
			d.trig.edgeImpedance50 = i == 1
		}
		return err
	}, func(c *core.Call) error { c.Text(imp[int(b2f(d.trig.edgeImpedance50))]); return nil })

	p := pre("PULSe", instrument.TrigPulse)
	p.source("SOURce", "source", false)
	p.choice("POLarity", "slope", polarityNames, slopeValues[:2])
	p.level("LEVel", "level", "source")
	p.limits()
	p.common(true)

	for _, x := range []struct {
		name string
		t    instrument.TrigType
		pol  bool
	}{{"SLOPe", instrument.TrigSlope, false}, {"RUNT", instrument.TrigRunt, true}} {
		s := pre(x.name, x.t)
		s.source("SOURce", "source", false)
		if x.pol {
			s.choice("POLarity", "slope", polarityNames, slopeValues[:2])
		} else {
			s.slope("SLOPe", "slope", 2)
		}
		s.level("HLEVel", "level", "source")
		s.level("LLEVel", "level_low", "source")
		s.limits()
		s.common(true)
	}

	w := pre("WINDow", instrument.TrigWindow)
	w.source("SOURce", "source", false)
	w.level("HLEVel", "level", "source")
	w.level("LLEVel", "level_low", "source")
	w.common(true)
	d.windowRelative(w)

	iv := pre("INTerval", instrument.TrigInterval)
	iv.source("SOURce", "source", false)
	iv.slope("SLOPe", "slope", 2)
	iv.level("LEVel", "level", "source")
	iv.limits()
	iv.common(true)

	dr := pre("DROPout", instrument.TrigDropout)
	dr.source("SOURce", "source", false)
	dr.slope("SLOPe", "slope", 2)
	dr.level("LEVel", "level", "source")
	dr.number("TIME", "timeout", minTrigS, maxTrigS)
	dr.choice("TYPE", "form", []string{"EDGE", "STATe"}, []string{"edge", "state"})
	dr.common(true)

	n := pre("NEDGe", instrument.TrigNthEdge)
	n.source("SOURce", "source", false)
	n.slope("SLOPe", "slope", 2)
	n.level("LEVel", "level", "source")
	n.number("IDLE", "idle", 8e-9, maxTrigS)
	n.whole("EDGE", "count", 1, 65535)
	n.common(false)

	v := pre("VIDeo", instrument.TrigVideo)
	v.source("SOURce", "source", false)
	v.add("LEVel", func(c *core.Call) error {
		x, ok, err := number(c)
		if err != nil || !ok {
			return err
		}
		d.trig.videoLevelSet = true
		return trigChange(c, func(s *core.State) error {
			src, _ := s.TrigParam(instrument.TrigVideo, "source", 0)
			return s.SetTrigParam(instrument.TrigVideo, "level", instrument.ParamValue{Num: clampLevel(*s, src.Text, x)})
		})
	}, func(c *core.Call) error { c.NR3(v.get(c, "level", 0).Num); return nil })
	v.choice("STANdard", "standard",
		[]string{"NTSC", "PAL", "P720L50", "P720L60", "P1080L50", "P1080L60", "I1080L50", "I1080L60", "CUSTom"},
		[]string{"ntsc", "pal", "720p50", "720p60", "1080p50", "1080p60", "1080i50", "1080i60", "custom"})
	v.add("SYNC", func(c *core.Call) error {
		i, err := choice(c, "SELect", "ANY")
		if err != nil {
			return err
		}
		return v.set(c, "any_line", instrument.ParamValue{Num: float64(i)})
	}, func(c *core.Call) error {
		c.Text([]string{"SELect", "ANY"}[int(v.get(c, "any_line", 0).Num)])
		return nil
	})
	v.whole("LINE", "line", 1, 2000)
	v.whole("FIELd", "field", 1, 8)
	v.add("FRATe", func(c *core.Call) error {
		i, err := choice(c, "25Hz", "30Hz", "50Hz", "60Hz")
		if err != nil {
			return err
		}
		return v.set(c, "frame_hz", instrument.ParamValue{Num: []float64{25, 30, 50, 60}[i]})
	}, func(c *core.Call) error { c.Text(strconv.Itoa(int(v.get(c, "frame_hz", 0).Num)) + "Hz"); return nil })
	for _, x := range []struct{ sub, id string }{{"INTerlace", "interlace"}, {"FCNT", "fields"}} {
		v.add(x.sub, func(c *core.Call) error {
			i, err := choice(c, "1", "2", "4", "8")
			if err != nil {
				return err
			}
			return v.set(c, x.id, instrument.ParamValue{Num: float64(int(1) << i)})
		}, func(c *core.Call) error { c.NR1(int64(v.get(c, x.id, 0).Num)); return nil })
	}
	v.whole("LCNT", "lines", 300, 2000)

	q := pre("QUALified", instrument.TrigQualified)
	q.source("ESource", "source", false)
	q.slope("ESLope", "slope", 2)
	q.level("ELEVel", "level", "source")
	q.source("QSource", "gate_source", false)
	q.level("QLEVel", "gate_level", "gate_source")
	q.limits()
	forms := []string{"STATe", "STATE_DLY", "EDGE", "EDGE_DLY"}
	formVals := []string{"state", "state_delay", "edge", "edge_delay"}
	q.add("TYPE", func(c *core.Call) error {
		i, err := choice(c, forms...)
		if err != nil {
			return err
		}
		high := -1
		if c.Remaining() > 0 {
			opts := []string{"LOW", "HIGH"}
			if i >= 2 {
				opts = []string{"FALLing", "RISing"}
			}
			if high, err = choice(c, opts...); err != nil {
				return err
			}
		}
		return trigChange(c, func(s *core.State) error {
			if err := s.SetTrigParam(instrument.TrigQualified, "gate_form", instrument.ParamValue{Text: formVals[i]}); err != nil {
				return err
			}
			if high >= 0 {
				return s.SetTrigParam(instrument.TrigQualified, "gate_high", instrument.ParamValue{Num: float64(high)})
			}
			return nil
		})
	}, func(c *core.Call) error {
		f := q.get(c, "gate_form", 0)
		i := int(f.Num)
		opts := []string{"LOW", "HIGH"}
		if i >= 2 {
			opts = []string{"FALLing", "RISing"}
		}
		c.Text(forms[i] + "," + opts[int(q.get(c, "gate_high", 0).Num)])
		return nil
	})

	sh := pre("SHOLd", instrument.TrigSetupHold)
	sh.choice("TYPE", "form", []string{"SETup", "HOLD"}, []string{"setup", "hold"})
	sh.source("CSource", "source", false)
	sh.level("CTHReshold", "level", "source")
	sh.slope("SLOPe", "slope", 2)
	sh.source("DSource", "data_source", false)
	sh.level("DTHReshold", "data_level", "data_source")
	sh.add("LEVel", func(c *core.Call) error {
		i, err := choice(c, "LOW", "HIGH")
		if err != nil {
			return err
		}
		return sh.set(c, "data_high", instrument.ParamValue{Num: float64(i)})
	}, func(c *core.Call) error {
		c.Text([]string{"LOW", "HIGH"}[int(sh.get(c, "data_high", 0).Num)])
		return nil
	})
	sh.limits()
}

// windowRelative binds the window's relative form: CLEVel and DLEVel are
// the centre and the width, turned into the absolute levels (spec 11
// §6.7.1); TYPE is the host interface's own.
func (d *dialect) windowRelative(w tb) {
	types := []string{"ABSolute", "RELative"}
	w.add("TYPE", func(c *core.Call) error {
		i, err := choice(c, types...)
		if err == nil {
			d.trig.windowRelative = i == 1
		}
		return err
	}, func(c *core.Call) error { c.Text(types[int(b2f(d.trig.windowRelative))]); return nil })
	centre := func(s core.State) (c, delta float64) {
		hi, _ := s.TrigParam(instrument.TrigWindow, "level", 0)
		lo, _ := s.TrigParam(instrument.TrigWindow, "level_low", 0)
		return (hi.Num + lo.Num) / 2, (hi.Num - lo.Num) / 2
	}
	rel := func(sub string, edit func(c, delta, v float64) (float64, float64), read func(c, delta float64) float64) {
		w.add(sub, func(c *core.Call) error {
			v, ok, err := number(c)
			if err != nil || !ok {
				return err
			}
			return trigChange(c, func(s *core.State) error {
				ce, de := centre(*s)
				hi, lo := edit(ce, de, v)
				src, _ := s.TrigParam(instrument.TrigWindow, "source", 0)
				if err := s.SetTrigParam(instrument.TrigWindow, "level", instrument.ParamValue{Num: clampLevel(*s, src.Text, hi)}); err != nil {
					return err
				}
				return s.SetTrigParam(instrument.TrigWindow, "level_low", instrument.ParamValue{Num: clampLevel(*s, src.Text, lo)})
			})
		}, func(c *core.Call) error {
			ce, de := centre(c.Instrument().State())
			c.NR3(read(ce, de))
			return nil
		})
	}
	rel("CLEVel", func(_, delta, v float64) (float64, float64) { return v + delta, v - delta },
		func(c, _ float64) float64 { return c })
	rel("DLEVel", func(c, _, v float64) (float64, float64) { return c + v, c - v },
		func(_, delta float64) float64 { return delta })
}

// statesParam reads a list of X, L, H (or 0, 1, X) states.
func statesParam(c *core.Call) (string, error) {
	if err := one(c); err != nil {
		return "", err
	}
	var st []string
	for c.Remaining() > 0 {
		t, _, _ := c.Param(true)
		v := strings.ToUpper(strings.TrimSpace(t.Raw))
		switch v {
		case "X", "L", "H", "0", "1":
		default:
			return "", c.Error(core.ErrIllegalParamValue)
		}
		st = append(st, v)
	}
	return strings.Join(st, ","), nil
}

// patternTypes binds the pattern and delay types.
func (d *dialect) patternTypes(pre func(string, instrument.TrigType) tb) {
	for _, x := range []struct {
		name, states, level string
		t                   instrument.TrigType
	}{{"PATTern", "INPut", "LEVel", instrument.TrigPattern}, {"DELay", "SOURce", "LEVel", instrument.TrigDelay}} {
		p := pre(x.name, x.t)
		p.add(x.states, func(c *core.Call) error {
			st, err := statesParam(c)
			if err != nil {
				return err
			}
			return p.set(c, "states", instrument.ParamValue{Text: st})
		}, func(c *core.Call) error {
			// The query lists every analog channel, then the logic
			// channels this firmware does not drive as don't care.
			st := strings.Split(p.get(c, "states", 0).Text, ",")
			for len(st) < d.cfg.Channels {
				st = append(st, "X")
			}
			for range 16 {
				st = append(st, "X")
			}
			c.Text(strings.Join(st, ","))
			return nil
		})
		p.add(x.level, func(c *core.Call) error {
			n, err := d.source(c)
			if err != nil {
				return err
			}
			v, _, err := c.Float(true)
			if err != nil {
				return err
			}
			return trigChange(c, func(s *core.State) error {
				return s.SetTrigParam(x.t, "pattern_level", instrument.ParamValue{Num: clampLevel(*s, "C"+strconv.Itoa(n+1), v), Ch: n})
			})
		}, func(c *core.Call) error {
			n, err := d.source(c)
			if err != nil {
				return err
			}
			c.NR3(p.get(c, "pattern_level", n).Num)
			return nil
		})
		p.limits()
		if x.t == instrument.TrigPattern {
			p.choice("LOGic", "logic", []string{"AND", "OR", "NAND", "NOR"}, []string{"and", "or", "nand", "nor"})
			p.holdoff()
			continue
		}
		p.slope("SLOPe", "slope_a", 2)
		p.source("SOURce2", "source", false)
		p.slope("SLOPe2", "slope", 2)
		p.level("LEVel2", "level", "source")
	}
}

// baudChoices are a protocol's listed rates (in kbit/s from 1 kbit/s
// up when kilo is set, else bit/s; Mbit/s from 1 Mbit/s) and CUSTom.
func baudChoices(kilo bool, rates ...float64) []string {
	var out []string
	for _, r := range rates {
		switch {
		case r >= 1e6:
			out = append(out, strconv.FormatFloat(r/1e6, 'f', -1, 64)+"Mbps")
		case kilo:
			out = append(out, strconv.FormatFloat(r/1e3, 'f', -1, 64)+"kbps")
		default:
			out = append(out, strconv.FormatFloat(r, 'f', -1, 64)+"bps")
		}
	}
	return append(out, "CUSTom")
}

// baud binds a protocol's BAUD: a listed rate, or CUSTom,<rate>.
func (b tb) baud(kilo bool, rates ...float64) {
	names := baudChoices(kilo, rates...)
	b.add("BAUD", func(c *core.Call) error {
		i, err := choice(c, names...)
		if err != nil {
			return err
		}
		v := 0.0
		if i < len(rates) {
			v = rates[i]
		} else {
			n, ok, err := c.Int(true)
			if err != nil || !ok {
				return err
			}
			if n < 1 || n > 20_000_000 {
				return c.Error(core.ErrDataOutOfRange)
			}
			v = float64(n)
		}
		return b.set(c, "baud", instrument.ParamValue{Num: v})
	}, func(c *core.Call) error {
		v := b.get(c, "baud", 0).Num
		for i, r := range rates {
			if r == v {
				c.Text(names[i])
				return nil
			}
		}
		c.Text("CUSTom," + strconv.FormatInt(int64(v), 10))
		return nil
	})
}

// dataByte binds a data byte whose 256 is none (-1 in the setting).
func (b tb) dataByte(sub, id string) {
	b.add(sub, func(c *core.Call) error {
		v, ok, err := c.Int(true)
		if err != nil || !ok {
			return err
		}
		if v < 0 || v > 256 {
			return c.Error(core.ErrDataOutOfRange)
		}
		if v == 256 {
			v = -1
		}
		return b.set(c, id, instrument.ParamValue{Num: float64(v)})
	}, func(c *core.Call) error {
		v := int64(b.get(c, id, 0).Num)
		if v < 0 {
			v = 256
		}
		c.NR1(v)
		return nil
	})
}

// anyValue binds an address or ID whose any value is anyAt (-1 in the
// setting).
func (b tb) anyValue(sub, id string, hi int64, anyAt func(c *core.Call) int64) {
	b.add(sub, func(c *core.Call) error {
		v, ok, err := c.Int(true)
		if err != nil || !ok {
			return err
		}
		if v < 0 || v > hi {
			return c.Error(core.ErrDataOutOfRange)
		}
		if v == anyAt(c) {
			v = -1
		}
		return b.set(c, id, instrument.ParamValue{Num: float64(v)})
	}, func(c *core.Call) error {
		v := int64(b.get(c, id, 0).Num)
		if v < 0 {
			v = anyAt(c)
		}
		c.NR1(v)
		return nil
	})
}

// altLine binds the source and threshold of one of two protocol lines the
// trigger chooses between: the line the trigger watches is the setting
// srcID/lvlID, the other is kept in the host interface.
func (b tb) altLine(srcSub, lvlSub, srcID, lvlID string, kept *lineSet, active func() bool) {
	b.add(srcSub, func(c *core.Call) error {
		if err := one(c); err != nil {
			return err
		}
		t, _, _ := c.Param(true)
		name := strings.ToUpper(t.Str)
		if _, err := instrument.ParseSource(name, b.d.cfg.Channels); err != nil {
			return c.Error(core.ErrIllegalParamValue)
		}
		kept.src = name
		if active() {
			return b.set(c, srcID, instrument.ParamValue{Text: name})
		}
		return nil
	}, func(c *core.Call) error {
		if active() {
			c.Text(b.get(c, srcID, 0).Text)
		} else {
			c.Text(kept.src)
		}
		return nil
	})
	b.add(lvlSub, func(c *core.Call) error {
		v, ok, err := number(c)
		if err != nil || !ok {
			return err
		}
		kept.level = v
		if active() {
			return b.set(c, lvlID, instrument.ParamValue{Num: v})
		}
		return nil
	}, func(c *core.Call) error {
		if active() {
			c.NR3(b.get(c, lvlID, 0).Num)
		} else {
			c.NR3(kept.level)
		}
		return nil
	})
}

// useLine makes kept the line the trigger watches.
func (b tb) useLine(c *core.Call, kept lineSet, srcID, lvlID string) error {
	return trigChange(c, func(s *core.State) error {
		if err := s.SetTrigParam(b.t, srcID, instrument.ParamValue{Text: kept.src}); err != nil {
			return err
		}
		return s.SetTrigParam(b.t, lvlID, instrument.ParamValue{Num: kept.level})
	})
}

// protocolTypes binds the serial triggers with hardware (spec 05 §10).
func (d *dialect) protocolTypes(pre func(string, instrument.TrigType) tb) {
	ts := &d.trig
	i2c := pre("IIC", instrument.TrigI2C)
	i2c.source("SCLSource", "scl_source", false)
	i2c.level("SCLThreshold", "scl_level", "scl_source")
	i2c.source("SDASource", "sda_source", false)
	i2c.level("SDAThreshold", "sda_level", "sda_source")
	i2c.choice("CONDition", "condition",
		[]string{"STARt", "STOP", "RESTart", "NACK", "EEPRom", "7ADDRess", "10ADDRess", "DLENgth"},
		[]string{"start", "stop", "restart", "no_ack", "eeprom", "addr7_data", "addr10_data", "data_length"})
	i2c.anyValue("ADDRess", "address", 1024, func(c *core.Call) int64 {
		if i2c.get(c, "condition", 0).Text == "addr10_data" || i2c.get(c, "addr10", 0).Num != 0 {
			return 1024
		}
		return 128
	})
	i2c.add("ALENgth", func(c *core.Call) error {
		i, err := choice(c, "7BIT", "10BIT")
		if err != nil {
			return err
		}
		return i2c.set(c, "addr10", instrument.ParamValue{Num: float64(i)})
	}, func(c *core.Call) error {
		c.Text([]string{"7BIT", "10BIT"}[int(i2c.get(c, "addr10", 0).Num)])
		return nil
	})
	i2c.choice("RWBit", "rw", []string{"WRITe", "READ", "ANY"}, []string{"write", "read", "any"})
	i2c.dataByte("DATA", "data1")
	i2c.dataByte("DAT2", "data2")
	i2c.whole("DLENgth", "length", 1, 12)
	i2c.choice("LIMit", "compare", compareNames, compareValues)

	spi := pre("SPI", instrument.TrigSPI)
	spi.source("CLKSource", "clk_source", false)
	spi.level("CLKThreshold", "clk_level", "clk_source")
	spi.choice("LATChedge", "edge", slopeNames[:2], slopeValues[:2])
	spi.altLine("MISOSource", "MISOThreshold", "data_source", "data_level", &ts.miso, func() bool { return !ts.spiMOSI })
	spi.altLine("MOSISource", "MOSIThreshold", "data_source", "data_level", &ts.mosi, func() bool { return ts.spiMOSI })
	spi.add("TTYPe", func(c *core.Call) error {
		i, err := choice(c, "MISO", "MOSI")
		if err != nil {
			return err
		}
		ts.spiMOSI = i == 1
		kept := ts.miso
		if ts.spiMOSI {
			kept = ts.mosi
		}
		return spi.useLine(c, kept, "data_source", "data_level")
	}, func(c *core.Call) error { c.Text([]string{"MISO", "MOSI"}[int(b2f(ts.spiMOSI))]); return nil })
	spi.altLine("CSSource", "CSThreshold", "cs_source", "cs_level", &ts.cs, func() bool { return ts.spiSelect == 1 })
	spi.altLine("NCSSource", "NCSThreshold", "cs_source", "cs_level", &ts.ncs, func() bool { return ts.spiSelect == 0 })
	spi.add("CSTYpe", func(c *core.Call) error {
		i, err := choice(c, "NCS", "CS", "TIMeout")
		if err != nil {
			return err
		}
		idle := -1.0
		if i == 2 && c.Remaining() > 0 {
			v, ok, err := number(c)
			if err != nil {
				return err
			}
			if ok {
				idle = v
			}
		}
		ts.spiSelect = i
		return trigChange(c, func(s *core.State) error {
			mode := []string{"active_low", "active_high", "timeout"}[i]
			if err := s.SetTrigParam(instrument.TrigSPI, "cs_mode", instrument.ParamValue{Text: mode}); err != nil {
				return err
			}
			if idle >= 0 {
				if err := s.SetTrigParam(instrument.TrigSPI, "idle", instrument.ParamValue{Num: idle}); err != nil {
					return err
				}
			}
			if i == 2 {
				return nil
			}
			kept := ts.ncs
			if i == 1 {
				kept = ts.cs
			}
			if err := s.SetTrigParam(instrument.TrigSPI, "cs_source", instrument.ParamValue{Text: kept.src}); err != nil {
				return err
			}
			return s.SetTrigParam(instrument.TrigSPI, "cs_level", instrument.ParamValue{Num: kept.level})
		})
	}, func(c *core.Call) error {
		switch spi.get(c, "cs_mode", 0).Text {
		case "active_high":
			c.Text("CS")
		case "timeout":
			c.Text("TIMeout," + core.FormatNR3(spi.get(c, "idle", 0).Num, c.Digits()))
		default:
			c.Text("NCS")
		}
		return nil
	})
	// DATA lists the pattern's bits, first-sent first; DLENgth sets how
	// many there are, padded with don't care.
	spi.add("DATA", func(c *core.Call) error {
		st, err := statesParam(c)
		if err != nil {
			return err
		}
		st = strings.NewReplacer("1", "H", "0", "L").Replace(st)
		n := len(strings.Split(spi.get(c, "bits", 0).Text, ","))
		bits := strings.Split(st, ",")
		for len(bits) < n {
			bits = append(bits, "X")
		}
		return spi.set(c, "bits", instrument.ParamValue{Text: strings.Join(bits[:max(n, 1)], ",")})
	}, nil)
	spi.add("DLENgth", func(c *core.Call) error {
		v, ok, err := c.Int(true)
		if err != nil || !ok {
			return err
		}
		if v < 4 || v > 96 {
			return c.Error(core.ErrDataOutOfRange)
		}
		bits := strings.Split(spi.get(c, "bits", 0).Text, ",")
		for int64(len(bits)) < v {
			bits = append(bits, "X")
		}
		return spi.set(c, "bits", instrument.ParamValue{Text: strings.Join(bits[:v], ",")})
	}, func(c *core.Call) error {
		c.NR1(int64(len(strings.Split(spi.get(c, "bits", 0).Text, ","))))
		return nil
	})
	spi.choice("BITorder", "bit_order", orderNames, orderValues)

	d.lineProtocols(pre)
}

// lineProtocols binds the one-line serial triggers: UART, CAN and LIN.
func (d *dialect) lineProtocols(pre func(string, instrument.TrigType) tb) {
	ts := &d.trig
	u := pre("UART", instrument.TrigUART)
	u.altLine("RXSource", "RXTHreshold", "source", "level", &ts.rx, func() bool { return !ts.uartTX })
	u.altLine("TXSource", "TXTHreshold", "source", "level", &ts.tx, func() bool { return ts.uartTX })
	u.add("TTYPe", func(c *core.Call) error {
		i, err := choice(c, "RX", "TX")
		if err != nil {
			return err
		}
		ts.uartTX = i == 1
		kept := ts.rx
		if ts.uartTX {
			kept = ts.tx
		}
		return u.useLine(c, kept, "source", "level")
	}, func(c *core.Call) error { c.Text([]string{"RX", "TX"}[int(b2f(ts.uartTX))]); return nil })
	u.choice("CONDition", "condition", []string{"STARt", "STOP", "DATA", "ERRor"}, []string{"start", "stop", "data", "error"})
	u.baud(false, 600, 1200, 2400, 4800, 9600, 19200, 38400, 57600, 115200)
	u.whole("DLENgth", "bits", 5, 8)
	u.choice("PARity", "parity", []string{"NONE", "ODD", "EVEN", "MARK", "SPACe"}, []string{"none", "odd", "even", "mark", "space"})
	u.add("STOP", func(c *core.Call) error {
		i, err := choice(c, "1", "1.5", "2")
		if err != nil {
			return err
		}
		return u.set(c, "stop", instrument.ParamValue{Num: []float64{1, 1.5, 2}[i]})
	}, func(c *core.Call) error {
		c.Text(strconv.FormatFloat(u.get(c, "stop", 0).Num, 'f', -1, 64))
		return nil
	})
	u.choice("IDLE", "idle", []string{"LOW", "HIGH"}, []string{"low", "high"})
	u.choice("BITorder", "bit_order", orderNames, orderValues)
	u.choice("LIMit", "compare", compareNames, compareValues)
	u.whole("DATA", "value", 0, 511)

	cn := pre("CAN", instrument.TrigCAN)
	cn.source("SOURce", "source", false)
	cn.level("THReshold", "level", "source")
	cn.baud(true, 5e3, 10e3, 20e3, 50e3, 100e3, 125e3, 250e3, 500e3, 800e3, 1e6)
	cn.choice("CONDition", "condition", []string{"STARt", "REMote", "ID", "ID_AND_DATA", "ERRor"},
		[]string{"start", "remote", "id", "id_data", "error"})
	cn.anyValue("ID", "id", 1<<29, func(c *core.Call) int64 {
		if cn.get(c, "extended", 0).Num != 0 {
			return 1 << 29
		}
		return 2048
	})
	cn.add("IDLength", func(c *core.Call) error {
		i, err := choice(c, "11BITS", "29BITS")
		if err != nil {
			return err
		}
		return cn.set(c, "extended", instrument.ParamValue{Num: float64(i)})
	}, func(c *core.Call) error {
		c.Text([]string{"11BITS", "29BITS"}[int(cn.get(c, "extended", 0).Num)])
		return nil
	})
	cn.dataByte("DATA", "data1")
	cn.dataByte("DAT2", "data2")

	l := pre("LIN", instrument.TrigLIN)
	l.source("SOURce", "source", false)
	l.level("THReshold", "level", "source")
	l.baud(false, 600, 1200, 2400, 4800, 9600, 19200)
	l.choice("CONDition", "condition", []string{"BReak", "ID", "ID_AND_DATA", "DATA_ERROR"},
		[]string{"break", "id", "id_data", "data_error"})
	l.anyValue("ID", "id", 64, func(*core.Call) int64 { return 64 })
	l.dataByte("DATA", "data1")
	l.dataByte("DAT2", "data2")
	l.bit("ERRor:CHECksum", "checksum_error")
	l.bit("ERRor:PARity", "parity_error")
	l.bit("ERRor:SYNC", "sync_error")
	l.whole("ERRor:ID", "error_id", 0, 63)
	l.whole("ERRor:DLENgth", "error_length", 1, 8)
	l.bit("STANdard", "spec2")
}

// triggerDefaults are the trigger settings of the built-in setup:
// the appendix's default column (spec 11 §6.7) for every type, with the
// levels of the factory-default pixels at 1 V/div (spec 05 §3.2).
func triggerDefaults(s *core.State) {
	type kv struct {
		id string
		v  instrument.ParamValue
	}
	n := func(id string, v float64) kv { return kv{id, instrument.ParamValue{Num: v}} }
	t := func(id, v string) kv { return kv{id, instrument.ParamValue{Text: v}} }
	times := []kv{t("limit", "less"), n("lower", minTrigS), n("upper", minTrigS)}
	c2 := "C1"
	if len(s.Channels) > 1 {
		c2 = "C2"
	}
	set := map[instrument.TrigType][]kv{
		instrument.TrigEdge:      {t("source", "C1"), t("slope", "rising"), n("level", 0)},
		instrument.TrigPulse:     append([]kv{t("source", "C1"), t("slope", "rising"), n("level", 0)}, times...),
		instrument.TrigSlope:     append([]kv{t("source", "C1"), t("slope", "rising"), n("level", 0), n("level_low", -0.05)}, times...),
		instrument.TrigRunt:      append([]kv{t("source", "C1"), t("slope", "rising"), n("level", 0), n("level_low", -0.05)}, times...),
		instrument.TrigWindow:    {t("source", "C1"), n("level", 0), n("level_low", -50.0/60)},
		instrument.TrigInterval:  append([]kv{t("source", "C1"), t("slope", "rising"), n("level", 0)}, times...),
		instrument.TrigDropout:   {t("source", "C1"), t("slope", "rising"), n("level", 0), n("timeout", 20e-9), t("form", "state")},
		instrument.TrigNthEdge:   {t("source", "C1"), t("slope", "rising"), n("level", 0), n("count", 1), n("idle", 8e-9)},
		instrument.TrigPattern:   {t("states", "X"), t("logic", "and"), t("limit", "greater"), n("lower", minTrigS), n("upper", minTrigS)},
		instrument.TrigQualified: append([]kv{t("source", "C1"), t("slope", "rising"), n("level", 0), t("gate_source", "C1"), n("gate_level", 0), t("gate_form", "state"), n("gate_high", 0)}, times...),
		instrument.TrigDelay:     append([]kv{t("states", "X"), t("slope_a", "rising"), t("source", c2), t("slope", "rising"), n("level", 0)}, times...),
		instrument.TrigSetupHold: append([]kv{t("form", "setup"), t("source", "C1"), n("level", 0), t("slope", "rising"), t("data_source", c2), n("data_level", 0), n("data_high", 0)}, times...),
		instrument.TrigVideo: {
			t("source", "C1"), n("level", 0), t("standard", "ntsc"), n("any_line", 1), n("line", 1), n("field", 1),
			n("frame_hz", 25), n("interlace", 1), n("fields", 1), n("lines", 300),
		},
		instrument.TrigI2C: {
			t("scl_source", "C1"), n("scl_level", 1.6), t("sda_source", c2), n("sda_level", 1.6), t("condition", "start"),
			n("address", -1), n("addr10", 0), t("rw", "write"), n("data1", -1), n("data2", -1), n("length", 1), t("compare", "equal"),
		},
		instrument.TrigSPI: {
			t("clk_source", "C1"), n("clk_level", 1.6), t("edge", "rising"), t("data_source", "C1"), n("data_level", 1.6),
			t("cs_mode", "active_low"), t("cs_source", "C1"), n("cs_level", 1.6), t("bits", "X,X,X,X,X,X,X,X"), t("bit_order", "lsb"),
		},
		instrument.TrigUART: {
			t("source", "C1"), n("level", 1.6), t("condition", "start"), n("baud", 9600), n("bits", 8), t("parity", "none"),
			n("stop", 1), t("idle", "low"), t("bit_order", "lsb"), t("compare", "equal"), n("value", 256),
		},
		instrument.TrigCAN: {
			t("source", "C1"), n("level", 1.6), n("baud", 100e3), t("condition", "start"), n("id", -1), n("extended", 0),
			n("data1", -1), n("data2", -1),
		},
		instrument.TrigLIN: {
			t("source", "C1"), n("level", 1.6), n("baud", 19200), t("condition", "break"), n("id", -1), n("data1", -1), n("data2", -1),
			n("checksum_error", 1), n("error_length", 1), n("error_id", 0), n("parity_error", 1), n("sync_error", 1), n("spec2", 1),
		},
	}
	s.Trigger.Fill(len(s.Channels))
	for tt, kvs := range set {
		for _, x := range kvs {
			if err := s.SetTrigParam(tt, x.id, x.v); err != nil {
				panic("scpi: trigger default " + tt.String() + " " + x.id + ": " + err.Error())
			}
		}
		// Every type runs DC coupled, noise reject off, holdoff off.
		s.Trigger.Configs[tt].Coupling, s.Trigger.Configs[tt].NoiseReject, s.Trigger.Configs[tt].HoldoffS = hal.TrigDC, false, 0
	}
	s.Trigger.Type = instrument.TrigEdge
}
