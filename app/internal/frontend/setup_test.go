package frontend

import (
	"bytes"
	"testing"

	"github.com/labeth/open-sds800xhd/app/internal/mmio"
	"github.com/labeth/open-sds800xhd/app/internal/mmio/mmiotest"
	"github.com/labeth/open-sds800xhd/app/internal/platform"
	"github.com/labeth/open-sds800xhd/app/internal/spi"
)

// fakeSPI records every frame per chip select.
type fakeSPI struct{ frames map[int][][]byte }

func newFakeSPI() *fakeSPI { return &fakeSPI{frames: map[int][][]byte{}} }

func (f *fakeSPI) Write(cs int, tx ...byte) {
	f.frames[cs] = append(f.frames[cs], append([]byte(nil), tx...))
}

func (f *fakeSPI) Xfer(cs int, tx []byte) []byte {
	f.Write(cs, tx...)
	return make([]byte, len(tx))
}

func (f *fakeSPI) last(cs int) []byte {
	fs := f.frames[cs]
	if len(fs) == 0 {
		return nil
	}
	return fs[len(fs)-1]
}

func newTestFE(part platform.ADCPart) (*FrontEnd, *fakeSPI, *mmiotest.Bus) {
	bus := mmiotest.New()
	s := newFakeSPI()
	fe := New(mmio.NewPlane("acq", bus), s, platform.Identity{ADC: part, Channels: 4}, func(string, ...any) {})
	return fe, s, bus
}

// ladderRow is one row of the mapping table of spec 06 §6.3 (default
// tables): class, attenuator bits of the control byte, VGA bits 4:0, the
// full-scale codes of both converters, digital ×2, the DAC attenuator
// nibble (§3.3) and the slope.
type ladderRow struct {
	vdiv        float64
	class       int
	att         byte
	vga         uint16
	fs1000      int
	fs1500      int
	dig2        bool
	nibble      byte
	slope       float32
	offsetLimit float64
}

var ladderTable = []ladderRow{
	{0.5e-3, 7, 0x06, 0x10, 11639, 35957, true, 0x1, 7161.6, 1.6},
	{1e-3, 8, 0x06, 0x10, 11639, 35957, false, 0x1, 7161.6, 1.6},
	{2e-3, 9, 0x06, 0x13, 11219, 35369, false, 0x1, 7161.6, 1.6},
	{5e-3, 10, 0x06, 0x17, 11336, 35533, false, 0x1, 7161.6, 1.6},
	{10e-3, 11, 0x06, 0x00, 12323, 36915, false, 0x0, 3755.6, 4},
	{20e-3, 12, 0x06, 0x03, 12671, 37402, false, 0x8, 1948.4, 8},
	{50e-3, 13, 0x06, 0x07, 12531, 37207, false, 0x8, 1948.4, 8},
	{0.1, 14, 0x06, 0x0A, 12677, 37411, false, 0x8, 1948.4, 8},
	{0.2, 15, 0x04, 0x03, 12671, 37402, false, 0x8, 194.8, 80},
	{0.5, 16, 0x04, 0x07, 12531, 37207, false, 0x8, 194.8, 80},
	{1, 17, 0x04, 0x0A, 12677, 37411, false, 0x8, 194.8, 80},
	{2, 18, 0x00, 0x03, 12671, 37402, false, 0x0, 36.24, 400},
	{5, 19, 0x00, 0x07, 12531, 37207, false, 0x0, 36.24, 400},
	{10, 20, 0x00, 0x0A, 12677, 37411, false, 0x0, 36.24, 400},
}

// TestLadderMapping runs the channel setup for every ladder step with the
// default tables on both converter parts and checks the derived settings
// and the frames on the bus against spec 06 §6.3, §3, §4.
func TestLadderMapping(t *testing.T) {
	for _, part := range []platform.ADCPart{platform.ADC12D1000, platform.ADC12DL1500} {
		for _, r := range ladderTable {
			for _, probe := range []float64{1, 10, 0.1, 1000} {
				for _, bwl := range []bool{false, true} {
					fe, s, bus := newTestFE(part)
					ch := Channel{On: true, VoltsDiv: r.vdiv * probe, Probe: probe, Coupling: DC, BWL20: bwl}
					fe.SetupChannel(0, ch, 1, 500)
					d := fe.Derived(0)
					fs := r.fs1000
					if part == platform.ADC12DL1500 {
						fs = r.fs1500
					}
					if d.Class != r.class || d.FSCode != fs || d.Digital2x != r.dig2 {
						t.Errorf("%v %g V/div ×%g: class %d fs %d dig2 %v, want %d %d %v", part, r.vdiv, probe, d.Class, d.FSCode, d.Digital2x, r.class, fs, r.dig2)
					}
					filt := uint16(4)
					if bwl {
						filt = 1
					}
					word := 0x0400 | filt<<6 | r.vga
					if got := s.last(spi.CSVGA1); !bytes.Equal(got, []byte{0, byte(word >> 8), byte(word)}) {
						t.Errorf("%g V/div bwl %v: VGA frame %x, want 00%04x", r.vdiv, bwl, got, word)
					}
					ctrl := r.att | 0x08 | 0x10 | 0x40
					want := []byte{0xFF, 0xF0 | r.nibble, 0x7E, 0x7E, 0x7E, ctrl}
					if got := s.last(spi.CSRelay); !bytes.Equal(got, want) {
						t.Errorf("%g V/div: relay frame %x, want %x", r.vdiv, got, want)
					}
					if got := fe.OffsetStep(0); got != 1/float64(r.slope) {
						t.Errorf("%g V/div: offset step %g, want 1/%g", r.vdiv, got, r.slope)
					}
					if v, _ := bus.Last(0x146 * 4); v != defaultCentre {
						t.Errorf("%g V/div: DAC %#x, want the centre %#x", r.vdiv, v, defaultCentre)
					}
					if got := OffsetLimit(r.vdiv); got != r.offsetLimit {
						t.Errorf("%g V/div: offset limit %g, want %g", r.vdiv, got, r.offsetLimit)
					}
					if dig, _ := bus.Last(0x19B * 4); (dig&1 != 0) != r.dig2 {
						t.Errorf("%g V/div: 0x19B = %#x", r.vdiv, dig)
					}
				}
			}
		}
	}
}

// TestCouplings checks the relay control byte and the VGA filter of AC and
// GND coupling, and the PL ground substitution (spec 06 §8).
func TestCouplings(t *testing.T) {
	fe, s, bus := newTestFE(platform.ADC12D1000)
	ac := Channel{On: true, VoltsDiv: 1, Probe: 1, Coupling: AC}
	fe.SetupChannel(0, ac, 1, 500)
	if got := s.last(spi.CSRelay)[5]; got != 0x04|0x10|0x40 {
		t.Errorf("AC control byte %#x", got)
	}
	gnd := Channel{On: true, VoltsDiv: 1, Probe: 1, Coupling: GND, OffsetV: 0.5}
	fe.SetupChannel(0, gnd, 1, 500)
	fe.ApplyGND([]Channel{gnd}, 500)
	if got := s.last(spi.CSRelay)[5]; got != 0x40 {
		t.Errorf("GND control byte %#x, want only Z_1M", got)
	}
	if got := s.last(spi.CSVGA1); got[1]&0x01 != 0 || (uint16(got[1])<<8|uint16(got[2]))>>6&7 != 1 {
		t.Errorf("GND VGA frame %x: want the 20 MHz filter", got)
	}
	// 0.5 V at 1 V/div is 30 px: 2048 + 8·30 = 2288.
	if v, _ := bus.Last(0x055 * 4); v != 0x0001 {
		t.Errorf("0x055 = %#x", v)
	}
	if v, _ := bus.Last(0x053 * 4); v != 2288 {
		t.Errorf("0x053 = %d, want 2288", v)
	}
}

// TestOffsetDAC checks the code of spec 06 §5: code = trunc(C − S·V_off).
func TestOffsetDAC(t *testing.T) {
	for _, c := range []struct {
		vdiv, off float64
		want      uint32
	}{
		{1, 0.1, 32766},      // X10 A1X, S 194.8: 32786 − 19.48
		{1, -1, 32980},       // 32786 + 194.8
		{0.01, 0.002, 32778}, // X1 A0.5X, S 3755.6: 32786 − 7.51
		{2, 100, 29161},      // X100 A0.5X, float32 S 36.24 (36.2400017): 32786 − 3624.00017
	} {
		fe, _, bus := newTestFE(platform.ADC12D1000)
		fe.SetupChannel(0, Channel{On: true, VoltsDiv: c.vdiv, OffsetV: c.off, Probe: 1, Coupling: DC}, 1, 500)
		if v, _ := bus.Last(0x146 * 4); v != c.want {
			t.Errorf("%g V/div offset %g: DAC %d, want %d", c.vdiv, c.off, v, c.want)
		}
	}
}

// TestLadderChecks covers the V/div range at the tip for probe factors and
// the refusal texts.
func TestLadderChecks(t *testing.T) {
	for _, c := range []struct {
		v, probe float64
		ok       bool
	}{
		{0.5e-3, 1, true},
		{0.4e-3, 1, false},
		{10, 1, true},
		{11, 1, false},
		{5e-3, 10, true},
		{2e-3, 10, false},
		{100, 10, true},
		{200, 10, false},
		{50e-6, 0.1, true},
		{1, 0.1, true},
		{2, 0.1, false},
		{0.5, 1000, true},
		{0.4, 1000, false},
		{10000, 1000, true},
		{0.7, 1, true}, // fine adjustment
		{0, 1, false},
		{-1, 1, false},
	} {
		why := CheckVoltsDiv(c.v, c.probe)
		if (why == "") != c.ok {
			t.Errorf("%g V/div at %gx: %q, want ok=%v", c.v, c.probe, why, c.ok)
		}
	}
	if why := CheckVoltsDiv(2e-3, 10); why != "2 mV/div at 10x: below the 5 mV/div tip floor" {
		t.Errorf("refusal text %q", why)
	}
	for _, p := range []float64{0.05, 20000} {
		if CheckProbe(p) == "" {
			t.Errorf("probe %g accepted", p)
		}
	}
	for _, p := range []float64{0.1, 1, 10, 10000} {
		if why := CheckProbe(p); why != "" {
			t.Errorf("probe %g: %s", p, why)
		}
		l := Ladder(p)
		if len(l) != 14 || l[0] != 0.5e-3*p || l[13] != 10*p {
			t.Errorf("ladder at %g: %v", p, l)
		}
	}
	if got := SnapVoltsDiv(0.1*10*1.0000001, 1); got != 1 {
		t.Errorf("snap %g", got)
	}
	if got := SnapVoltsDiv(0.7, 1); got != 0.7 {
		t.Errorf("fine value moved to %g", got)
	}
}
