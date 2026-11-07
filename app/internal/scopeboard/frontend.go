package scopeboard

import (
	"fmt"
	"math"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-sds800xhd/app/internal/frontend"
)

type frontEndFacet Board

func toFrontend(c hal.Channel) frontend.Channel {
	return frontend.Channel{
		On: c.On, VoltsDiv: c.VoltsDiv, OffsetV: c.OffsetV, Probe: c.Probe,
		Coupling: frontend.Coupling(c.Coupling), BWL20: c.BWLimit > 0 && c.BWLimit <= 20e6,
		Invert: c.Invert, SkewS: c.SkewS,
	}
}

func fromFrontend(c frontend.Channel) hal.Channel {
	h := hal.Channel{
		On: c.On, Invert: c.Invert, VoltsDiv: c.VoltsDiv, OffsetV: c.OffsetV, Probe: c.Probe,
		Coupling: hal.Coupling(c.Coupling), Impedance: 1e6, SkewS: c.SkewS,
	}
	if c.BWL20 {
		h.BWLimit = 20e6
	}
	return h
}

// Set stages a channel: the V/div is checked against the ladder range at the
// probe (fine values between the steps are kept), the offset clamped to the
// range of its V/div class (spec 06 §7.2). Impedance other than 1 MΩ, a
// bandwidth limit other than 20 MHz, and a probe factor outside
// 0.1x … 10000x are refused.
func (f *frontEndFacet) Set(ch int, c hal.Channel) (hal.Channel, error) {
	b := (*Board)(f)
	if ch < 0 || ch >= b.id.Channels {
		return hal.Channel{}, hal.Refuse("channel", fmt.Sprintf("C%d does not exist on this model", ch+1))
	}
	if c.Impedance != 0 && c.Impedance != 1e6 {
		return hal.Channel{}, hal.Refuse(fmt.Sprintf("%g Ω input", c.Impedance), "this scope has a 1 MΩ input only (spec 06 §8.4)")
	}
	if c.BWLimit != 0 && c.BWLimit != 20e6 {
		return hal.Channel{}, hal.Refuse(fmt.Sprintf("%g MHz bandwidth limit", c.BWLimit/1e6), "the only limit is 20 MHz")
	}
	switch c.Coupling {
	case hal.DC, hal.AC, hal.GND:
	default:
		return hal.Channel{}, hal.Refuse("coupling", "DC, AC or GND")
	}
	if c.Probe == 0 {
		c.Probe = 1
	}
	if why := frontend.CheckProbe(c.Probe); why != "" {
		return hal.Channel{}, hal.Refuse("probe factor", why)
	}
	if why := frontend.CheckVoltsDiv(c.VoltsDiv, c.Probe); why != "" {
		return hal.Channel{}, hal.Refuse("V/div", why)
	}
	c.VoltsDiv = frontend.SnapVoltsDiv(c.VoltsDiv, c.Probe)
	lim := frontend.OffsetLimit(c.VoltsDiv/c.Probe) * c.Probe
	c.OffsetV = math.Max(-lim, math.Min(lim, c.OffsetV))
	b.staged.Ch[ch] = toFrontend(c)
	c.Impedance = 1e6
	return c, nil
}

func (f *frontEndFacet) Get(ch int) hal.Channel {
	b := (*Board)(f)
	if b.eng == nil {
		return fromFrontend(b.staged.Ch[ch])
	}
	return fromFrontend(b.eng.Settings().Ch[ch])
}

// Ladder is 500 µV … 10 V per division at the BNC in 1-2-5 steps, times
// the staged probe factor (spec 06 §6.1, §12).
func (f *frontEndFacet) Ladder(ch int) []hal.Step {
	p := (*Board)(f).staged.Ch[ch].Probe
	var out []hal.Step
	for _, v := range frontend.Ladder(p) {
		out = append(out, hal.Step{VoltsDiv: v, Zoom: 1})
	}
	return out
}

// OffsetGrid is one offset-DAC code at the channel's last setup, at the
// tip, and the offset range of the staged V/div.
func (f *frontEndFacet) OffsetGrid(ch int) hal.Grid {
	b := (*Board)(f)
	c := b.staged.Ch[ch]
	lim := frontend.OffsetLimit(c.VoltsDiv/c.Probe) * c.Probe
	step := 0.0
	if b.eng != nil {
		step = b.eng.FE.OffsetStep(ch) * c.Probe
	}
	return hal.Grid{Step: step, Min: -lim, Max: lim}
}

// Couplings are DC and AC by relay and GND by the PL's sample substitution
// (spec 06 §8.1, §8.3): every one is done before the record, so none is
// left to the core.
func (f *frontEndFacet) Couplings(int) []hal.CouplingOption {
	return []hal.CouplingOption{{Coupling: hal.DC}, {Coupling: hal.AC}, {Coupling: hal.GND}}
}

// Calibration is "open-selfcal" after a self-calibration this process
// ran (spec 10 §5), "vendor" when a calibration file (the factory DAC file
// on p9 or the self-calibration on p12) was applied at start-up, "none"
// when the front end runs on the default tables (spec 10 §4.3). The user
// self-cal file does not say who wrote it (the vendor firmware or an
// earlier run of this one), so with it the author is unknown.
func (f *frontEndFacet) Calibration() hal.CalSource {
	b := (*Board)(f)
	if b.eng == nil {
		return hal.CalSource{Source: "none"}
	}
	st := b.eng.FE.CalStatus()
	switch {
	case st.SelfCal:
		return hal.CalSource{Source: "open-selfcal", Author: "open", Valid: true}
	case st.User:
		return hal.CalSource{Source: "vendor", Valid: true}
	case st.Factory:
		return hal.CalSource{Source: "vendor", Author: "vendor", Valid: true}
	}
	return hal.CalSource{Source: "none"}
}
