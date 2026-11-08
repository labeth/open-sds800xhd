package scpi

import (
	"strconv"
	"strings"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-dso-core/sample"
	core "github.com/labeth/open-dso-core/scpi"
	"github.com/labeth/open-dso-core/scpi/wave"
)

// specLong are the long spellings spec 11 §6.3.2 gives where the
// inventory's row does not name its long form.
var specLong = map[string]string{
	"C#:TRA": "C#:TRACE", "C#:CPL": "C#:COUPLING", "C#:OFST": "C#:OFFSET", "C#:ATTN": "C#:ATTENUATION",
	"TDIV": "TIME_DIV", "SARA": "SAMPLE_RATE", "WFSU": "WAVEFORM_SETUP", "C#:WF": "C#:WAVEFORM",
}

// longName is a legacy header's long spelling, "" without one.
func longName(short string) string {
	if l := inventory[short].long(); l != "" {
		return l
	}
	return specLong[short]
}

// legacy are the legacy short forms of spec 11 §6.3.2 with their long
// spellings: CHDR SHORT answers "<short header> <value><unit>", LONG the
// long header, OFF the value alone.
func (d *dialect) legacy() []core.Command {
	var cmds []core.Command
	// both registers a header under its short and its long spelling.
	both := func(short string, h core.Handler) {
		query := strings.HasSuffix(short, "?")
		base := strings.TrimSuffix(short, "?")
		cmds = append(cmds, d.bind(short, h))
		if long := longName(base); long != "" && (inventory[long].Command != "" || specHeaders[long]) {
			if query {
				long += "?"
			}
			cmds = append(cmds, d.bind(long, h))
		}
	}
	chHeader := func(c *core.Call, h string) string { return "C" + strconv.Itoa(c.N(0)) + ":" + h }
	longOf := func(c *core.Call, short string) string {
		l := longName(short)
		return strings.Replace(l, "C#", "C"+strconv.Itoa(c.N(0)), 1)
	}
	chq := func(short string, reply func(c *core.Call, ch hal.Channel) (string, string)) {
		both(short+"?", func(c *core.Call) error {
			n, err := d.channel(c)
			if err != nil {
				return err
			}
			v, unit := reply(c, c.Instrument().State().Channels[n])
			name := strings.TrimPrefix(short, "C#:")
			c.Legacy(chHeader(c, name), longOf(c, short), v, unit)
			return nil
		})
	}
	chset := func(short string, set func(c *core.Call) (func(*hal.Channel), error)) {
		both(short, func(c *core.Call) error {
			n, err := d.channel(c)
			if err != nil {
				return err
			}
			fn, err := set(c)
			if err != nil || fn == nil {
				return err
			}
			if err := changeCh(c, n, fn); err != nil {
				return err
			}
			d.selected(short, n)
			return nil
		})
	}
	volts := func(apply func(ch *hal.Channel, v float64)) func(c *core.Call) (func(*hal.Channel), error) {
		return func(c *core.Call) (func(*hal.Channel), error) {
			v, ok, err := number(c)
			if err != nil || !ok {
				return nil, err
			}
			return func(ch *hal.Channel) { apply(ch, v) }, nil
		}
	}
	nr3 := func(c *core.Call, v float64) string { return core.FormatNR3(v, c.Digits()) }

	chset("C#:VDIV", volts(func(ch *hal.Channel, v float64) { ch.VoltsDiv = v }))
	chq("C#:VDIV", func(c *core.Call, ch hal.Channel) (string, string) { return nr3(c, ch.VoltsDiv), "V" })
	chset("C#:OFST", volts(func(ch *hal.Channel, v float64) { ch.OffsetV = v }))
	chq("C#:OFST", func(c *core.Call, ch hal.Channel) (string, string) { return nr3(c, ch.OffsetV), "V" })
	chset("C#:TRA", func(c *core.Call) (func(*hal.Channel), error) {
		on, err := onOff(c)
		return func(ch *hal.Channel) { ch.On = on }, err
	})
	chq("C#:TRA", func(_ *core.Call, ch hal.Channel) (string, string) { return state(ch.On), "" })
	// A 1 MΩ input: the couplings are A1M, D1M and GND.
	chset("C#:CPL", func(c *core.Call) (func(*hal.Channel), error) {
		i, err := choice(c, "D1M", "A1M", "GND")
		return func(ch *hal.Channel) { ch.Coupling = [...]hal.Coupling{hal.DC, hal.AC, hal.GND}[i] }, err
	})
	chq("C#:CPL", func(_ *core.Call, ch hal.Channel) (string, string) {
		return [...]string{"D1M", "A1M", "GND"}[min(max(int(ch.Coupling), 0), 2)], ""
	})
	chq("C#:BWL", func(_ *core.Call, ch hal.Channel) (string, string) { return state(ch.BWLimit > 0), "" })
	chset("C#:ATTN", func(c *core.Call) (func(*hal.Channel), error) {
		v, ok, err := number(c)
		if err != nil || !ok {
			return nil, err
		}
		if !(v > 0) {
			return nil, c.Error(core.ErrDataOutOfRange)
		}
		return func(ch *hal.Channel) {
			if ch.Probe > 0 {
				ch.VoltsDiv *= v / ch.Probe
				ch.OffsetV *= v / ch.Probe
			}
			ch.Probe = v
		}, nil
	})
	chq("C#:ATTN", func(_ *core.Call, ch hal.Channel) (string, string) {
		return strconv.FormatFloat(ch.Probe, 'g', -1, 64), ""
	})
	// One level exists, the running trigger's at its source.
	both("C#:TRLV", func(c *core.Call) error {
		v, ok, err := number(c)
		if err != nil || !ok {
			return err
		}
		if c.Instrument().State().Trigger.Source() != (hal.Source{Kind: hal.Analog, Index: c.N(0) - 1}) {
			return c.Error(core.ErrSettingsConflict)
		}
		return change(c, func(s *core.State) error { return s.Trigger.SetLevel(v, len(s.Channels)) })
	})
	both("C#:TRLV?", func(c *core.Call) error {
		s := c.Instrument().State()
		if s.Trigger.Source() != (hal.Source{Kind: hal.Analog, Index: c.N(0) - 1}) {
			return c.Error(core.ErrSettingsConflict)
		}
		c.Legacy(chHeader(c, "TRLV"), longOf(c, "C#:TRLV"), nr3(c, s.Trigger.LevelV()), "V")
		return nil
	})
	// TDIV? never carries a header but keeps its unit in SHORT and LONG.
	both("TDIV", func(c *core.Call) error {
		v, ok, err := number(c)
		if err != nil || !ok {
			return err
		}
		return change(c, func(s *core.State) error { s.TimeDivS = v; return nil })
	})
	both("TDIV?", func(c *core.Call) error {
		v := nr3(c, c.Instrument().State().TimeDivS)
		if c.HeaderMode() != core.HeaderOff {
			v += "S"
		}
		c.Text(v)
		return nil
	})
	both("TRMD", func(c *core.Call) error {
		i, err := choice(c, "AUTO", "NORM", "SINGLE", "STOP")
		if err != nil {
			return err
		}
		if i == 3 {
			return setRun(c, hal.Auto, false)
		}
		return setRun(c, [...]hal.Sweep{hal.Auto, hal.NormalSweep, hal.Single}[i], true)
	})
	both("TRMD?", func(c *core.Call) error {
		s := c.Instrument().State()
		v := map[hal.Sweep]string{hal.Auto: "AUTO", hal.NormalSweep: "NORM", hal.Single: "SINGLE"}[sweepOf(s)]
		if !s.Running {
			v = "STOP"
		}
		c.Legacy("TRMD", "TRIG_MODE", v, "")
		return nil
	})
	// TRSE EDGE,SR,<source>[,HT,<holdoff>]: the edge type and its source;
	// the query names the running type.
	both("TRSE", d.legacyTRSE)
	both("TRSE?", func(c *core.Call) error {
		s := c.Instrument().State()
		ht := "OFF"
		if h := s.Trigger.Active().HoldoffS; h > 0 {
			ht = "TI,HV," + nr3(c, h)
		}
		c.Legacy("TRSE", "", strings.ToUpper(d.typeName(s.Trigger.Type))+",SR,"+sourceName(s.Trigger.Source())+",HT,"+ht, "")
		return nil
	})
	both("TRST?", func(c *core.Call) error {
		c.Legacy("TRST", "TRIG_STATUS", state(c.Instrument().Acquisition() == core.Triggered), "")
		return nil
	})
	both("MSIZ", func(c *core.Call) error {
		names := make([]string, len(depths))
		for i, x := range depths {
			names[i] = x.name
		}
		i, err := choice(c, names...)
		if err != nil {
			return err
		}
		return change(c, func(s *core.State) error { s.Depth = depths[i].n; return nil })
	})
	both("MSIZ?", func(c *core.Call) error {
		c.Legacy("MSIZ", "MEMORY_SIZE", depthName(c.Instrument().State().Depth), "")
		return nil
	})
	cmds = append(cmds, d.bind("SARA?", func(c *core.Call) error {
		rate, ok := frameRate(c)
		if !ok {
			return core.ErrFailed
		}
		c.Legacy("SARA", "SAMPLE_RATE", nr3(c, rate), "Sa/s")
		return nil
	}))
	both("ARM", func(c *core.Call) error {
		return setRun(c, hal.Single, true)
	})
	cmds = append(cmds, d.bind("STOP", func(c *core.Call) error {
		return setRun(c, hal.Auto, false)
	}))
	for _, cmd := range core.CommHeader() {
		both(cmd.Pattern, cmd.Handler)
	}
	// The legacy waveform: WFSU sets the transfer window of :WAVeform, and
	// Cn:WF? answers the descriptor or the 8-bit data under CHDR's header.
	both("WFSU", d.legacyWFSU)
	both("WFSU?", func(c *core.Call) error {
		c.Legacy("WFSU", "WAVEFORM_SETUP", "SP,"+strconv.Itoa(d.wfInterval)+",NP,"+strconv.Itoa(d.wfPoints)+
			",FP,"+strconv.Itoa(d.wfStart), "")
		return nil
	})
	both("C#:WF?", d.legacyWF)
	return cmds
}

// legacyTRSE is TRSE EDGE,SR,<source>[,HT,<holdoff>]: the edge type and its source.
func (d *dialect) legacyTRSE(c *core.Call) error {
	if err := one(c); err != nil {
		return err
	}
	var toks []string
	for c.Remaining() > 0 {
		t, _, _ := c.Param(true)
		toks = append(toks, strings.ToUpper(t.Str))
	}
	if toks[0] != edgeName {
		return c.Error(core.ErrSettingsConflict)
	}
	for i := 1; i+1 < len(toks); i += 2 {
		if toks[i] != "SR" {
			continue
		}
		src, ok := d.trigSource(toks[i+1])
		if !ok {
			return c.Error(core.ErrIllegalParamValue)
		}
		return change(c, func(s *core.State) error {
			if src.Kind == hal.Analog && !s.Channels[src.Index].On || src.Kind == hal.External {
				return nil
			}
			s.Trigger.Type = core.TrigEdge
			return s.Trigger.SetSource(src, len(s.Channels))
		})
	}
	return nil
}

// legacyWFSU sets the transfer window of :WAVeform.
func (d *dialect) legacyWFSU(c *core.Call) error {
	if err := one(c); err != nil {
		return err
	}
	for c.Remaining() > 0 {
		k, _, _ := c.Param(true)
		v, ok, err := c.Int(false)
		if err != nil {
			return err
		}
		if !ok || v < 0 {
			return c.Error(core.ErrIllegalParamValue)
		}
		switch strings.ToUpper(k.Str) {
		case "SP":
			d.wfInterval = max(int(v), 1)
		case "NP":
			d.wfPoints = int(v)
		case "FP":
			d.wfStart = int(v)
		case "SN":
		default:
			return c.Error(core.ErrIllegalParamValue)
		}
	}
	return nil
}

// legacyWF answers Cn:WF? with the descriptor or the 8-bit data under CHDR's header.
func (d *dialect) legacyWF(c *core.Call) error {
	n, err := d.channel(c)
	if err != nil {
		return err
	}
	i, err := choice(c, "DESC", "DAT2")
	if err != nil {
		return err
	}
	if i == 0 {
		desc, _ := d.preamble(c, n)
		c.Raw(wave.Block9(d.wfHeader(c, n, "DESC"), desc))
		return nil
	}
	var data []byte
	word := d.wfWord
	d.wfWord = false // the legacy data block is 8-bit
	c.Instrument().WithFrame(func(f *sample.Frame) {
		t, ok := trace(f, n)
		if !ok {
			return
		}
		w := wave.Window{First: d.wfStart, Sparse: d.wfInterval, Points: d.wfPoints, Max: maxPoints}
		if first, step, cnt, ok := w.Span(t.Valid); ok {
			data = d.encode(t, first, step, cnt)
		}
	})
	d.wfWord = word
	c.Raw(append(wave.Block9(d.wfHeader(c, n, "DAT2"), data), '\n'))
	return nil
}
