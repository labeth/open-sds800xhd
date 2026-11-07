// Package frontend is the analog path of the SDS800X HD: the relay shift
// register, the per-channel VGAs, the offset DACs, the ADC full scale and
// the converters themselves (spec 06), with the calibration that maps a
// V/div and offset to their codes (spec 10).
package frontend

import (
	"math"
	"time"

	"github.com/labeth/open-sds800xhd/app/internal/mmio"
	"github.com/labeth/open-sds800xhd/app/internal/platform"
	"github.com/labeth/open-sds800xhd/app/internal/spi"
)

// Coupling is a channel's input coupling (spec 06 §8.1).
type Coupling int

// The couplings.
const (
	DC Coupling = iota
	AC
	GND
)

// Channel is one channel's vertical setting as the user sees it: V/div and
// offset are the displayed values, at the probe tip (spec 06 §12).
type Channel struct {
	On       bool
	VoltsDiv float64
	OffsetV  float64
	Probe    float64
	Coupling Coupling
	BWL20    bool
	Invert   bool
	SkewS    float64
}

// bncMilliVoltsDiv and bncOffset remove the probe factor (spec 06 §12),
// rounded to 12 significant digits so that a displayed value times and
// then divided by a probe factor (0.05·0.1/0.1) lands on its class bound
// instead of a rounding error above it.
func (c Channel) bncMilliVoltsDiv() float64 { return sig12(c.VoltsDiv / c.Probe * 1000) }
func (c Channel) bncOffset() float64        { return sig12(c.OffsetV / c.Probe) }

func sig12(v float64) float64 {
	if v == 0 || math.IsInf(v, 0) || math.IsNaN(v) {
		return v
	}
	e := math.Pow10(11 - int(math.Floor(math.Log10(math.Abs(v)))))
	return math.Round(v*e) / e
}

// Derived is what the channel setup computed for a channel.
type Derived struct {
	Class, Group, Att, Step, FSCode, DACCode int
	Digital2x                                bool
	// PosPx is the trace position in screen rows (60 per division); the
	// trace's zero volts sits PosPx rows above the screen centre.
	PosPx float64
}

// vdivClass is the V/div class of spec 06 §6.1 (mV/div at the BNC).
func vdivClass(mv float64) int {
	for i, b := range [...]float64{0.995, 1.01, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000} {
		if b >= mv {
			return 7 + i
		}
	}
	return 20
}

func frontGroup(mv float64) int {
	switch {
	case mv > 1000:
		return groupX100
	case mv > 100:
		return groupX10
	}
	return groupX1
}

// dacAtt is the offset-DAC attenuator for a V/div in volts (spec 06 §3.3).
func dacAtt(v float64) int {
	switch {
	case v > 1:
		return att0p5X
	case v > 0.01:
		return att1X
	case v > 0.005:
		return att0p5X
	}
	return att0p25X
}

var dacNibble = [4]uint64{0x8, 0x0, 0x1, 0x2}

// OffsetLimit is the offset range ± V at the BNC for a V/div class (spec 06 §7.2).
func offsetLimit(class int) float64 {
	switch {
	case class <= 10:
		return 1.6
	case class == 11:
		return 4
	case class <= 14:
		return 8
	case class <= 17:
		return 80
	}
	return 400
}

// Per-channel ACQ registers.
var (
	dacReg = [4]int{0x146, 0x148, 0x149, 0x147} // offset DAC (spec 06 §5)
	GNDReg = [4]int{0x053, 0x056, 0x05A, 0x05F} // GND substitute codes (spec 06 §8.3)
)

// FrontEnd is the analog path of all channels.
type FrontEnd struct {
	ADC *ADCs

	acq       *mmio.Plane
	spi       SPI
	part      platform.ADCPart
	cal       [4]Cal
	relay     uint64 // the 48-bit relay image (spec 06 §3.4)
	vga       [4]uint16
	dac       [4]int
	dacSet    [4]bool
	derived   [4]Derived
	route     int // relay route mode: 0 = one channel per ADC pair
	selfCal   bool
	calStatus CalStatus
	log       func(string, ...any)
	sleep     func(time.Duration) // the self-calibration's waits; nil: time.Sleep
}

// New returns the front end of a unit with default calibration.
func New(acq *mmio.Plane, bus SPI, id platform.Identity, log func(string, ...any)) *FrontEnd {
	f := &FrontEnd{ADC: NewADCs(acq, bus, id, log), acq: acq, spi: bus, part: id.ADC, route: 1, log: log}
	for c := range f.cal {
		f.cal[c] = DefaultCal(id.ADC)
	}
	f.relay = 0xFFFF<<32 | 0x7E7E7E7E
	for c := range f.vga {
		f.vga[c] = 0x040A
	}
	return f
}

// Derived returns what the last setup of channel c computed.
func (f *FrontEnd) Derived(c int) Derived { return f.derived[c] }

// OffsetDACInit is start-up step 3 (spec 03 §2).
func (f *FrontEnd) OffsetDACInit() {
	f.acq.Set(0x141, 4)
	time.Sleep(2 * time.Millisecond)
	for _, r := range []int{0x146, 0x148, 0x149, 0x147, 0x144, 0x143, 0x142, 0x145} {
		f.acq.Set(r, 0)
	}
	time.Sleep(2 * time.Millisecond)
	f.acq.Set(0x141, 8)
}

// VGAInit writes the power-up word to all four VGAs (spec 06 §4), on
// 2-channel models too.
func (f *FrontEnd) VGAInit() {
	for c := range 4 {
		f.vga[c] = 0x040A
		f.sendVGA(c)
	}
}

func (f *FrontEnd) sendVGA(c int) {
	f.spi.Write(spi.CSVGA1+c, 0x00, byte(f.vga[c]>>8), byte(f.vga[c])) //nolint:gosec // G115: the two bytes of the 16-bit VGA word
}

func (f *FrontEnd) sendRelay() {
	r := f.relay
	f.spi.Write(spi.CSRelay, byte(r>>40), byte(r>>32), byte(r>>24), byte(r>>16), byte(r>>8), byte(r)) //nolint:gosec // G115: the six bytes of the 48-bit relay word
}

// RelayEnable drives the relay-chain enable line, sysfs GPIO 343 (spec 06
// §3.4, start-up step 10).
func (f *FrontEnd) RelayEnable() {
	if err := platform.GPIOOut(343, 1, false); err != nil {
		f.log("relay enable: %v", err)
	}
}

func (f *FrontEnd) ctrl(c int) byte { return byte(f.relay >> (8 * c)) } //nolint:gosec // G115: one channel's byte of the relay word

func (f *FrontEnd) setCtrl(c int, mask, val byte) {
	sh := uint(8 * c)
	b := f.ctrl(c)&^mask | val&mask
	f.relay = f.relay&^(0xFF<<sh) | uint64(b)<<sh
}

func (f *FrontEnd) setNibble(c int, v uint64) {
	sh := uint(32 + 4*c)
	f.relay = f.relay&^(0xF<<sh) | v<<sh
}

func (f *FrontEnd) bwBit(ch Channel) byte {
	if !ch.BWL20 && f.route == 0 {
		return 1 << 5
	}
	return 0
}

// SetRoute recomputes relay bit 5 of every channel when the route changes
// (spec 06 §8.2): one relay frame per changed channel, CH1 first.
func (f *FrontEnd) SetRoute(route int, chans []Channel) {
	if route == f.route {
		return
	}
	f.route = route
	for c := range chans {
		old := f.relay
		f.setCtrl(c, 1<<5, f.bwBit(chans[c]))
		if f.relay != old {
			f.sendRelay()
		}
	}
}

func attBits(class int) byte {
	switch {
	case class <= 14:
		return 1<<2 | 1<<1
	case class <= 17:
		return 1 << 2
	}
	return 0
}

func (f *FrontEnd) gnd(ch Channel) bool { return ch.Coupling == GND && !f.selfCal }

// relayBits sets coupling, attenuator, bandwidth and impedance bits of
// channel c (spec 06 §3.2, §8).
func (f *FrontEnd) relayBits(c int, ch Channel) {
	att, caln := attBits(vdivClass(ch.bncMilliVoltsDiv())), byte(1<<4)
	var dc byte
	if ch.Coupling == DC {
		dc = 1 << 3
	}
	if f.gnd(ch) {
		att, caln = 0, 0
	}
	f.setCtrl(c, 0x7E, att|dc|caln|f.bwBit(ch)|1<<6)
}

func (f *FrontEnd) vgaFilter(ch Channel) uint16 {
	if ch.BWL20 || f.gnd(ch) {
		return 1
	}
	return 4
}

// stepSearch picks the VGA step and the ADC full-scale code for a target
// gain t (spec 06 §6.2 steps 3–6).
func (f *FrontEnd) stepSearch(c int, t float32, g, d, m int, fullRate bool) (step, fs int) {
	tab := &f.cal[c].vga[g][min(d, 2)]
	gain := func(s int) float32 { return tab[s].gain[m] }
	pick := func(lo, hi int) bool {
		for i := lo; i <= hi; i++ {
			if t < gain(i) {
				step = i
				if i > 0 && abs32(gain(i-1)-t) < abs32(gain(i)-t) {
					step = i - 1
				}
				return true
			}
		}
		return false
	}
	if (g != groupX1 || !pick(11, 20)) && !pick(0, 10) {
		return 10, 0
	}
	r := t / gain(step)
	p := fsFor(f.part)
	col := 1
	if fullRate {
		col = 0
	}
	F := func(j int) float32 { return float32(f.cal[c].fs[j][col]) }
	j := -1
	for k := range p.n {
		eps := float32(1e-6)
		if F(k) >= 1 {
			eps = 1e-5
		}
		if r < F(k)-eps {
			j = k
			break
		}
	}
	if j <= 0 {
		return step, 0
	}
	x := float32(j-1) + (r-F(j-1))/(F(j)-F(j-1))
	code := int(fma32(float32((p.codeMax-p.codeMin)/(p.n-1)), x, float32(p.codeMin)))
	if f.part == platform.ADC12D1000 {
		code = max(0, min(code, 0x7FFF))
	}
	return step, code
}

func abs32(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}

// dig2Lanes are the lanes of 0x19B (digital ×2) a channel uses at a rate
// (spec 06 §6.4).
func dig2Lanes(c, mask int, rateMSa float64) []int {
	switch rateMSa {
	case 2000:
		return []int{0, 1, 2, 3}
	case 1000:
		switch mask {
		case 1, 2:
			return []int{0, 1}
		case 4, 8:
			return []int{2, 3}
		case 3:
			return []int{2 * c, 2*c + 1}
		case 5, 6, 9, 0xA:
			return []int{c & 2, c&2 + 1}
		case 0xC:
			return []int{2 * (c & 1), 2*(c&1) + 1}
		}
	}
	return []int{c}
}

// SetupChannel is the channel setup of spec 06 §11.1 for channel c at the
// table rate rateMSa with the enabled-channel mask. An enabled channel always
// sends its relay image, VGA word and full scale; a disabled one only its
// relay bits and VGA filter.
func (f *FrontEnd) SetupChannel(c int, ch Channel, mask int, rateMSa float64) {
	mv := ch.bncMilliVoltsDiv()
	d := Derived{Class: vdivClass(mv), Group: frontGroup(mv)}
	gnd := f.gnd(ch)
	fullRate := rateMSa == 2000
	filt := f.vgaFilter(ch)
	if !ch.On {
		prev := f.relay
		f.relayBits(c, ch)
		if f.relay != prev {
			f.sendRelay()
		}
		if nv := f.vga[c]&^(7<<6) | filt<<6; nv != f.vga[c] {
			f.vga[c] = nv
			f.sendVGA(c)
		}
		return
	}
	// 1. Relays; in GND first CAL_N off, then coupling and attenuator (spec 06 §8.1).
	if gnd {
		f.setCtrl(c, 1<<4|1<<5, 0)
		f.sendRelay()
	}
	f.relayBits(c, ch)
	f.sendRelay()
	// 2. Digital ×2 below 1 mV/div.
	t := float32(mv)
	d.Digital2x = mv < 0.995
	if d.Digital2x {
		t = float32(2 * mv)
	}
	var b uint16
	if d.Digital2x && !gnd {
		b = 1
	}
	for _, l := range dig2Lanes(c, mask, rateMSa) {
		f.acq.Field(0x19B, l, l, b)
	}
	// 3. VGA step and full scale.
	d.Att = dacAtt(mv / 1000)
	m := rateUnite
	if fullRate {
		m = rateSingle
	}
	d.Step, d.FSCode = f.stepSearch(c, t, d.Group, d.Att, m, fullRate)
	if f.part == platform.ADC12DL1500 && d.FSCode < 0x2000 {
		d.FSCode = 0x2000
	}
	bits := uint16(d.Step) //nolint:gosec // G115: a ladder step is 0 … 20
	if d.Step > 10 {
		bits = 0x10 + uint16(d.Step-11) //nolint:gosec // G115: a ladder step is 0 … 20
	}
	f.vga[c] = 0x0400 | filt<<6 | bits
	f.sendVGA(c)
	// 4. Offset-DAC attenuator in the relay image, then the offset DAC.
	f.setNibble(c, dacNibble[d.Att])
	if !gnd {
		f.sendRelay()
	}
	pos := PositionPx(ch)
	d.PosPx = pos
	voff := pos / 60 * (mv / 1000)
	slope := float64(f.cal[c].slope[d.Att][d.Group])
	centre := float64(f.cal[c].vga[d.Group][min(d.Att, 2)][d.Step].centre[m])
	d.DACCode = max(0, min(int(centre-slope*voff), 65535))
	if !f.dacSet[c] || f.dac[c] != d.DACCode {
		f.acq.Set(dacReg[c], uint16(d.DACCode)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		f.dac[c], f.dacSet[c] = d.DACCode, true
		time.Sleep(5 * time.Millisecond) // DAC settle (spec 06 §11.1)
	}
	// 5. ADC full scale.
	f.ADC.SetFullScale(c, d.FSCode, fullRate)
	f.derived[c] = d
}

// FilterPass rewrites every VGA word with its filter bits, the start-up
// step after the channel setups (spec 03 §2).
func (f *FrontEnd) FilterPass(chans []Channel) {
	for c := range 4 {
		if c < len(chans) {
			f.vga[c] = f.vga[c]&^(7<<6) | f.vgaFilter(chans[c])<<6
		}
		f.sendVGA(c)
	}
}

// ApplyGND programs the PL's ground substitution for every enabled channel
// in GND coupling (spec 06 §8.3).
func (f *FrontEnd) ApplyGND(chans []Channel, rateMSa float64) {
	var mask uint16
	for c, ch := range chans {
		if !ch.On || !f.gnd(ch) {
			continue
		}
		k := 2048 + 8*f.derived[c].PosPx
		if ch.Invert {
			k = 2048 - 8*f.derived[c].PosPx
		}
		code := uint16(max(0, min(4095, int(k))))
		if rateMSa == 2000 {
			mask = 0x1111
			for _, r := range GNDReg {
				f.acq.Set(r, code)
			}
		} else {
			mask |= 1 << (4 * c)
			f.acq.Set(GNDReg[c], code)
		}
	}
	f.acq.Set(0x055, mask)
}

// ScreenVolts converts a 12-bit display code of channel c (0x800 = screen
// centre, 8 codes per screen row, 60 rows per division, spec 03 §5) to volts
// at the probe tip. For an inverted channel it assumes the record holds the
// inverted trace, as displayed (where the PL applies the 0x00F inversion is
// not known).
func ScreenVolts(code float64, ch Channel, d Derived) float64 {
	rows := (code - 0x800) / 8
	if ch.Invert {
		return -(rows + d.PosPx) / 60 * ch.VoltsDiv
	}
	return (rows - d.PosPx) / 60 * ch.VoltsDiv
}

// PositionPx is the screen position of channel ch's zero volts, in rows
// above the screen centre (60 per division): its offset clamped to the
// range of its V/div class (spec 06 §7.2), negated for an inverted channel.
func PositionPx(ch Channel) float64 {
	mv := ch.bncMilliVoltsDiv()
	lim := offsetLimit(vdivClass(mv))
	off := math.Max(-lim, math.Min(lim, ch.bncOffset()))
	pos := 60 * off / (mv / 1000)
	if ch.Invert {
		pos = -pos
	}
	return pos
}

// OffsetStep is one offset-DAC code of channel c in volts at the BNC, at
// its last setup (spec 10 §2: the DAC slope in codes per volt).
func (f *FrontEnd) OffsetStep(c int) float64 {
	d := f.derived[c]
	if s := f.cal[c].slope[d.Att][d.Group]; s > 0 {
		return 1 / float64(s)
	}
	return 0
}

// OffsetLimit is the offset range ± V at the BNC for a V/div at the BNC
// (spec 06 §7.2).
func OffsetLimit(voltsDiv float64) float64 { return offsetLimit(vdivClass(sig12(voltsDiv * 1000))) }
