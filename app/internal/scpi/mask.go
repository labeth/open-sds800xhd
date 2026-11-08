package scpi

import (
	"strconv"

	"github.com/labeth/open-dso-core/engine"
	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-dso-core/sample"
	core "github.com/labeth/open-dso-core/scpi"
)

// MaskTest is the pass/fail test the owner runs on the frames. The factory
// PL image's pass/fail registers (spec 02, spec 07) carry no comparison this
// firmware can program, so the test is the core's software mask on every
// frame read (engine.FrameMask).
type MaskTest interface {
	SetMask(m *engine.Mask)
	SetMaskMode(m int)
	ClearMaskFails()
	Counts() engine.MaskCounts
}

// maskState is the pass/fail settings the host interface keeps: the
// function on, the test running, stop on fail, the source and the stored,
// inert actions (buzzer, fail to history, capture on fail, information
// display).
type maskState struct {
	on, operate, sof           bool
	source                     int
	buzzer, fth, cof, idisplay bool
}

func newMaskState() maskState { return maskState{} }

// maskMode is the test's mode the settings make.
func (m *maskState) mode() int {
	switch {
	case !m.on || !m.operate:
		return engine.MaskOff
	case m.sof:
		return engine.MaskStopFail
	}
	return engine.MaskTest
}

// maskTest binds :MTESt (spec 11 §6.7's pass/fail rows).
func (d *dialect) maskTest() []core.Command {
	x := d.cfg.Mask
	if x == nil {
		return nil
	}
	ms := &d.mask
	var cmds []core.Command
	add := func(h string, set, query core.Handler) {
		if set != nil {
			cmds = append(cmds, d.bind(h, set))
		}
		if query != nil {
			cmds = append(cmds, d.bind(h+"?", query))
		}
	}
	flag := func(h string, p *bool, apply bool) {
		add(h, func(c *core.Call) error {
			on, err := onOff(c)
			if err != nil {
				return err
			}
			*p = on
			if apply {
				x.SetMaskMode(ms.mode())
			}
			return nil
		}, func(c *core.Call) error { c.Text(state(*p)); return nil })
	}
	flag(":MTESt", &ms.on, true)
	flag(":MTESt:OPERate", &ms.operate, true)
	flag(":MTESt:FUNCtion:SOF", &ms.sof, true)
	flag(":MTESt:FUNCtion:BUZZer", &ms.buzzer, false)
	flag(":MTESt:FUNCtion:FTH", &ms.fth, false)
	flag(":MTESt:FUNCtion:COF", &ms.cof, false)
	flag(":MTESt:IDISplay", &ms.idisplay, false)
	add(":MTESt:SOURce", func(c *core.Call) error {
		n, err := d.source(c)
		if err == nil {
			ms.source = n
		}
		return err
	}, func(c *core.Call) error { c.Text("C" + strconv.Itoa(ms.source+1)); return nil })
	// The software mask fails a frame with any sample outside it: ALL_IN.
	types := d.choices(":MTESt:TYPE", "type")
	add(":MTESt:TYPE", func(c *core.Call) error {
		i, err := choice(c, types...)
		if err != nil || i == 0 {
			return err
		}
		return c.Refused(hal.Refuse("mask test "+types[i], "the mask test passes a frame only with every sample inside the mask (ALL_IN)"))
	}, func(c *core.Call) error { c.Text(types[0]); return nil })
	add(":MTESt:COUNt", nil, func(c *core.Call) error {
		k := x.Counts()
		c.Text("FAIL," + strconv.FormatUint(k.Fail, 10) + ",PASS," + strconv.FormatUint(k.Pass, 10) +
			",TOTAL," + strconv.FormatUint(k.Fail+k.Pass, 10))
		return nil
	})
	add(":MTESt:RESet", func(*core.Call) error { x.ClearMaskFails(); return nil }, nil)
	add(":MTESt:MASK:CREate", func(c *core.Call) error {
		xm, _, err := c.Float(true)
		if err != nil {
			return err
		}
		ym, _, err := c.Float(true)
		if err != nil {
			return err
		}
		if xm < 0.08 || xm > 4 || ym < 0.08 || ym > 4 {
			return c.Error(core.ErrDataOutOfRange)
		}
		st := c.Instrument().State()
		pos := .5
		if st.TimeDivS > 0 {
			pos = .5 - st.DelayS/(10*st.TimeDivS)
		}
		var m *engine.Mask
		c.Instrument().WithFrame(func(f *sample.Frame) { m = engine.MaskFromFrame(f, ms.source, pos, xm, ym, 10) })
		if m == nil {
			return c.Refused(hal.Refuse("mask", "no waveform of the source to build it from"))
		}
		x.SetMask(m)
		return nil
	}, nil)
	add(":MTESt:MASK:LOAD", func(c *core.Call) error {
		return c.Refused(hal.Refuse("mask load", "this firmware keeps no mask files; :MTESt:MASK:CREate builds one"))
	}, nil)
	return cmds
}
