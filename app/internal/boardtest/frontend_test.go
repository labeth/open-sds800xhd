//go:build simboard

package boardtest

import (
	"fmt"
	"math"
	"strconv"
	"testing"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-dso-core/sample"
)

// The input signals of the front-end area (sim/cmd/simtest/area_frontend.go
// passes them to the device model): 100 kHz squares whose amplitudes suit
// four decades of V/div.
var feSignals = [4]struct{ vpp, mean float64 }{
	{0.002, 0.0005}, {0.02, 0.005}, {0.2, 0.05}, {2, 0.5},
}

// feStep is one ladder step at the BNC with what spec 06 §6.3 and §3 say
// the hardware gets with the default tables: the VGA bits 4:0, the
// attenuator bits of the relay control byte and the offset-DAC attenuator
// nibble.
type feStep struct {
	vdiv   float64
	vga    byte
	att    byte
	nibble byte
	ch     int // the input whose signal suits the step
}

var feLadder = []feStep{
	{0.5e-3, 0x10, 0x06, 0x1, 0},
	{1e-3, 0x10, 0x06, 0x1, 0},
	{2e-3, 0x13, 0x06, 0x1, 0},
	{5e-3, 0x17, 0x06, 0x1, 1},
	{10e-3, 0x00, 0x06, 0x0, 1},
	{20e-3, 0x03, 0x06, 0x8, 1},
	{50e-3, 0x07, 0x06, 0x8, 2},
	{0.1, 0x0A, 0x06, 0x8, 2},
	{0.2, 0x03, 0x04, 0x8, 2},
	{0.5, 0x07, 0x04, 0x8, 3},
	{1, 0x0A, 0x04, 0x8, 3},
	{2, 0x03, 0x00, 0x0, 3},
	{5, 0x07, 0x00, 0x0, 3},
	{10, 0x0A, 0x00, 0x0, 3},
}

// relayPattern is the expected relay frame with x for the other channels'
// bytes and nibbles (spec 06 §3.1: nibbles C3/C4, C1/C2, then the control
// bytes C4 … C1).
func relayPattern(ch int, ctrl, nibble byte) string {
	b := []string{"xx", "xx", "xx", "xx", "xx", "xx"}
	b[5-ch] = fmt.Sprintf("%02x", ctrl)
	nb := strconv.FormatUint(uint64(nibble), 16)
	switch ch {
	case 0:
		b[1] = "x" + nb
	case 1:
		b[1] = nb + "x"
	case 2:
		b[0] = "x" + nb
	case 3:
		b[0] = nb + "x"
	}
	return b[0] + b[1] + b[2] + b[3] + b[4] + b[5]
}

var vgaChips = [4]string{"vga1", "vga2", "vga3", "vga4"}

// TestFrontEnd sets every ladder step with every coupling and both
// bandwidths through FrontEnd.Set, checks the relay and VGA frames on the
// bus, reads a record and checks its volts against the injected signal.
func TestFrontEnd(t *testing.T) {
	n := board.Profile().Channels
	couplings := map[hal.Coupling]string{hal.DC: "dc", hal.AC: "ac", hal.GND: "gnd"}
	for _, st := range feLadder {
		ch := st.ch
		if ch >= n {
			ch = n - 1
		}
		sig := feSignals[st.ch]
		for _, cp := range []hal.Coupling{hal.DC, hal.AC, hal.GND} {
			for _, bwl := range []float64{0, 20e6} {
				id := fmt.Sprintf("frontend/%gV-%s", st.vdiv, couplings[cp])
				if bwl > 0 {
					id += "-bwl"
				}
				c := begin(t, id)
				got, err := only(ch, hal.Channel{VoltsDiv: st.vdiv, Probe: 1, Coupling: cp, BWLimit: bwl})
				if !c.noErr(err, "set") {
					continue
				}
				c.check(got.VoltsDiv == st.vdiv && got.Coupling == cp && got.BWLimit == bwl,
					"applied %g V/div coupling %d bwl %g", got.VoltsDiv, got.Coupling, got.BWLimit)
				f, err := acquireOne(ch, 10e-6)
				if !c.noErr(err, "record") || len(f.Traces) != 1 {
					continue
				}
				// Spec 06 §4.2, §8: 20 MHz filter with the limit or in GND.
				filt := 4
				if bwl > 0 || cp == hal.GND {
					filt = 1
				}
				word := 0x0400 | filt<<6 | int(st.vga)
				c.expectSPI(vgaChips[ch], []byte{0, byte(word >> 8), byte(word)})
				// Control byte (spec 06 §3.2, §8): one channel on is route 0,
				// so BW_FULL is set when the limit is off.
				ctrl := st.att | 0x40 | 0x10
				if cp == hal.DC {
					ctrl |= 0x08
				}
				if cp == hal.GND {
					ctrl = 0x40
				}
				if bwl == 0 {
					ctrl |= 0x20
				}
				fmt.Printf("EXPECTSPI %s hc595 %s\n", id, relayPattern(ch, ctrl, st.nibble))
				checkVolts(c, &f.Traces[0], st.vdiv, cp, sig.vpp, sig.mean)
			}
		}
	}

	// Probe factors: the same BNC step at the tip, volts scaled by the probe.
	for _, p := range []float64{0.1, 10, 100} {
		st := feLadder[4] // 10 mV/div at the BNC on C2
		ch := min(st.ch, n-1)
		c := begin(t, fmt.Sprintf("frontend/probe-%gx", p))
		got, err := only(ch, hal.Channel{VoltsDiv: st.vdiv * p, Probe: p, Coupling: hal.DC})
		if !c.noErr(err, "set") {
			continue
		}
		f, err := acquireOne(ch, 10e-6)
		if !c.noErr(err, "record") || len(f.Traces) != 1 {
			continue
		}
		word := 0x0400 | 4<<6 | int(st.vga)
		c.expectSPI(vgaChips[ch], []byte{0, byte(word >> 8), byte(word)})
		sig := feSignals[st.ch]
		checkVolts(c, &f.Traces[0], got.VoltsDiv, hal.DC, sig.vpp*p, sig.mean*p)
	}

	// An offset moves the trace, not the volts.
	c := begin(t, "frontend/offset")
	if _, err := only(min(2, n-1), hal.Channel{VoltsDiv: 0.1, OffsetV: -0.15, Probe: 1, Coupling: hal.DC}); c.noErr(err, "set") {
		if f, err := acquireOne(min(2, n-1), 10e-6); c.noErr(err, "record") && len(f.Traces) == 1 {
			sig := feSignals[2]
			checkVolts(c, &f.Traces[0], 0.1, hal.DC, sig.vpp, sig.mean)
		}
	}

	// Refusals.
	c = begin(t, "frontend/refusals")
	fe := board.FrontEnd()
	for name, s := range map[string]hal.Channel{
		"below the floor":   {VoltsDiv: 0.2e-3, Probe: 1},
		"below 10x floor":   {VoltsDiv: 2e-3, Probe: 10},
		"above the ceiling": {VoltsDiv: 20, Probe: 1},
		"probe 20000x":      {VoltsDiv: 1, Probe: 20000},
		"50 ohm":            {VoltsDiv: 1, Probe: 1, Impedance: 50},
		"100 MHz limit":     {VoltsDiv: 1, Probe: 1, BWLimit: 100e6},
	} {
		_, err := fe.Set(0, s)
		c.refused(err, name)
	}
	_, err := fe.Set(n, hal.Channel{VoltsDiv: 1, Probe: 1})
	c.refused(err, fmt.Sprintf("C%d", n+1))
	off, err := fe.Set(0, hal.Channel{VoltsDiv: 0.1, OffsetV: 100, Probe: 1})
	c.check(err == nil && off.OffsetV == 8, "offset clamped to %g V at 100 mV/div", off.OffsetV)
	c.check(len(fe.Ladder(0)) == 14 && len(fe.Couplings(0)) == 3, "ladder %d steps, %d couplings", len(fe.Ladder(0)), len(fe.Couplings(0)))
	g := fe.OffsetGrid(0)
	c.check(g.Step > 0 && g.Max == 8, "offset grid %+v", g)
	// Leave C1 on alone at 1 V/div for the areas after this one.
	_, _ = only(0, hal.Channel{VoltsDiv: 1, Probe: 1, Coupling: hal.DC})
}

// checkVolts compares a trace's two levels with the injected square:
// DC shows it as it is, AC without its mean, GND shows 0 V. The tolerance
// is three codes plus 1 % of the level.
func checkVolts(c *tcase, tr *sample.Trace, vdiv float64, cp hal.Coupling, vpp, mean float64) {
	lo, hi := levels(tr)
	wantLo, wantHi := mean-vpp/2, mean+vpp/2
	switch cp {
	case hal.DC:
		// the signal as it is
	case hal.AC:
		wantLo, wantHi = -vpp/2, vpp/2
	case hal.GND:
		wantLo, wantHi = 0, 0
	}
	tol := func(v float64) float64 { return 3*vdiv/480 + 0.01*math.Abs(v) }
	c.check(math.Abs(lo-wantLo) <= tol(wantLo) && math.Abs(hi-wantHi) <= tol(wantHi),
		"levels %.4g / %.4g V, want %.4g / %.4g V (±%.2g)", lo, hi, wantLo, wantHi, tol(wantHi))
}
