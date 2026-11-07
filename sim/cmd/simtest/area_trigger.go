package main

import (
	"fmt"
	"strings"

	"github.com/labeth/open-sds800xhd/sim/harness"
)

func init() {
	register(boardArea("trigger", "triggers: edge, pulse/slope/video/window/runt/pattern/serial",
		"Trigger", 120, "", nil, refTriggerParity))
}

// triggerParity pairs a board-test case with the reference run of the
// simulator whose SCPI put the reference firmware into the same trigger state,
// and the registers that state fixes. Only states the reference run reaches
// from a known start are comparable: every channel on at 1 V/div, levels at
// their defaults or set by the run, and time pairs the reference firmware's range check
// leaves known. Registers whose reference value depends on the order of earlier
// commands (the stale pattern states a qualified trigger reads, the I2C
// read/write code an EEPROM condition keeps) are left out or prepared.
var triggerParity = []struct {
	ours, run, mark string
	regs            []string
}{
	{
		"ref-qual-state", "tdeep-qual", "scpi :TRIGger:QUALified:TYPE STATe,HIGH",
		[]string{"acq:0x080", "acq:0x00e", "acq:0x0db", "acq:0x0d7", "acq:0x012"},
	},
	{
		"ref-qual-state-delay", "tdeep-qual", "scpi :TRIGger:QUALified:TUPPer 2E-6",
		[]string{"acq:0x080", "acq:0x00e", "acq:0x012", "acq:0x014"},
	},
	{
		"ref-pattern-or", "tdeep-fix2", "scpi :TRIGger:PATTern:LOGic OR",
		[]string{"acq:0x0ac", "acq:0x0ad", "acq:0x0d7", "acq:0x0db", "acq:0x101", "acq:0x0b5", "acq:0x0cd", "acq:0x107", "acq:0x00e"},
	},
	{
		"ref-hold", "tdeep-qual", "scpi :TRIGger:SHOLd:TYPE HOLD",
		[]string{"acq:0x080", "acq:0x00e", "acq:0x012", "acq:0x0db"},
	},
	{
		"ref-i2c-eeprom", "tdeep-qual", "scpi :TRIGger:IIC:CONDition EEPRom",
		[]string{"map:0x030", "map:0x047", "map:0x048", "map:0x036", "map:0x042", "map:0x043", "acq:0x10b", "acq:0x10d"},
	},
}

// refTriggerParity compares the trigger registers of each paired state
// with the reference run's.
func refTriggerParity(_ *env, r *harness.Run) []harness.Result {
	var out []harness.Result
	refs := map[string]*harness.Run{}
	for _, p := range triggerParity {
		id := "parity " + p.ours
		v := refs[p.run]
		if v == nil {
			var err error
			if v, err = harness.Load(p.run); err != nil {
				out = append(out, harness.Fail("trigger", id, "reference run %s: %v", p.run, err))
				continue
			}
			refs[p.run] = v
		}
		want, ok := v.Log.FinalThrough(p.mark, "scpi ")
		if !ok {
			out = append(out, harness.Fail("trigger", id, "no mark %q in %s", p.mark, p.run))
			continue
		}
		got, ok := r.Log.FinalThrough("app: case trigger/"+p.ours, "app: case")
		if !ok {
			out = append(out, harness.Fail("trigger", id, "the board test did not run the case"))
			continue
		}
		var bad []string
		for _, k := range p.regs {
			reg, _ := harness.ParseReg(k)
			if got[reg] != want[reg] {
				bad = append(bad, fmt.Sprintf("%v ours %#x, reference %#x", reg, got[reg], want[reg]))
			}
		}
		if len(bad) > 0 {
			out = append(out, harness.Fail("trigger", id, "%s", strings.Join(bad, "; ")))
		} else {
			out = append(out, harness.Pass("trigger", id, fmt.Sprintf("%d registers as %s after %q", len(p.regs), p.run, p.mark)))
		}
	}
	return out
}
