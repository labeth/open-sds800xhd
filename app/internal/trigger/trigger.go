// Package trigger programs the trigger engine of the SDS800X HD's PL (spec
// 05): every trigger type of the vendor bitstream — edge, slope, pulse,
// video, window, interval, dropout, runt, pattern, qualified, Nth edge,
// delay, setup/hold and the I2C, SPI, UART, CAN and LIN serial triggers —
// with their sources, levels and hysteresis, time conditions, holdoff and
// sweep modes. The trigger lives in the ACQ plane (the A block of spec 05);
// the serial decoder in the MAP plane (the M block).
//
// Program writes the complete configuration of the selected type, in the
// order of the type-change sequence (spec 05 §5.1) inside the sweep
// programming (§7). Whatever changed, the registers end at the values the
// type's own setting rules give; the per-setting partial writes of §5.10
// reach the same final values.
package trigger

import (
	"math"

	"github.com/labeth/open-sds800xhd/app/internal/mmio"
)

// Type is a trigger type, by the API enumeration of spec 05.
type Type int

// The trigger types.
const (
	Edge Type = iota
	Slope
	Pulse
	Video
	Window
	Interval
	Dropout
	Runt
	Pattern
	Serial
	Qualified
	NthEdge
	Delay
	SetupHold
	numTypes
)

var typeNames = [numTypes]string{
	"edge", "slope", "pulse", "video", "window", "interval", "dropout", "runt",
	"pattern", "serial", "qualified", "nth edge", "delay", "setup/hold",
}

func (t Type) String() string {
	if t >= 0 && t < numTypes {
		return typeNames[t]
	}
	return "unknown"
}

// The PL type code (A 0x0E[3:0]) and A 0x0E7 of each type (spec 05 §1).
var (
	typeCode = [numTypes]uint16{0, 1, 2, 3, 0, 6, 5, 7, 4, 9, 8, 10, 8, 8}
	typeAux  = [numTypes]uint16{9, 9, 9, 9, 9, 9, 10, 9, 10, 9, 9, 9, 9, 9}
)

// codeDisabled is the type code of Auto sweep in roll mode (spec 05 §1).
const codeDisabled = 15

// Sweep is the sweep mode (spec 05 §7).
type Sweep int

// The sweep modes. Force is the one-shot forced trigger.
const (
	Auto Sweep = iota
	Normal
	Single
	Force
)

// Dir is an edge direction. For Pulse and Runt Rising is positive
// polarity; for Interval, Dropout and the edges of Qualified, Delay and
// Setup/Hold only Rising and Falling exist.
type Dir int

// The slopes, as A 0x011 bits 9:8 code them.
const (
	Rising Dir = iota
	Falling
	Alternate
)

// Coupling is the trigger path's filter (A 0x0E4, spec 05 §4).
type Coupling int

// The trigger couplings.
const (
	CoupDC Coupling = iota
	CoupAC
	CoupLFReject
	CoupHFReject
)

// Limit is a time condition (spec 05 §5.3).
type Limit int

// The limits.
const (
	Less Limit = iota
	Greater
	Inside
	Outside
)

// SourceACLine is the AC-line source; 0–3 are the channels.
const SourceACLine = 6

// State is one input's pattern state, as A 0x0AC codes it (spec 05 §5.8).
type State uint8

// The pattern states.
const (
	Low      State = 0
	High     State = 1
	DontCare State = 2
)

// Logic combines pattern states.
type Logic int

// The pattern logic functions.
const (
	And Logic = iota
	Or
	Nand
	Nor
)

// logicCode is A 0x0AD[1:0] of each logic (spec 05 §5.8).
var logicCode = [4]uint16{And: 0, Or: 2, Nand: 1, Nor: 3}

// PatternSet is a pattern over the channels: the Pattern type and Delay's
// event A.
type PatternSet struct {
	States [4]State
	Levels [4]float64 // volts at the tip
	Logic  Logic
}

// QualMode is a qualified trigger's mode (A 0x080[14:12], spec 05 §5.9).
type QualMode int

// The qualified modes.
const (
	QualState QualMode = iota
	QualStateDelay
	QualEdge
	QualEdgeDelay
)

// QualSet is the qualifier of a Qualified trigger.
type QualSet struct {
	Mode   QualMode
	Source int
	Level  float64
	High   bool // state High, or a rising qualifier edge
}

// SetupHoldSet is a setup/hold trigger's inputs. The clock is
// Settings.Source with Settings.Level and Settings.Slope.
type SetupHoldSet struct {
	Hold      bool // check the hold time, not the setup time
	Data      int
	DataLevel float64
	DataHigh  bool
}

// Settings is the whole trigger configuration.
type Settings struct {
	Type  Type
	Sweep Sweep
	Roll  bool // roll mode: the driver's smp = 0

	Source      int     // trigger source; Pattern has none
	Slope       Dir     // edge slope or polarity
	Level       float64 // level 1 (Slope, Runt, Window: the low level), volts at the tip
	Level2      float64 // level 2 (Slope, Runt, Window: the high level; Qualified, Delay: the edge's level)
	Coupling    Coupling
	NoiseReject bool
	HoldoffS    float64 // 0: off

	Limit          Limit
	LowerS, UpperS float64

	TimeoutS    float64 // Dropout
	DropoutEdge bool    // Dropout: the EDGE overtime type, not STATE
	Count       int     // Nth edge
	IdleS       float64 // Nth edge

	Video      VideoSet
	Pattern    PatternSet // Pattern, and Delay's event A
	Qual       QualSet
	DelayARise bool // Delay: event A fires as the pattern becomes true (rising)
	SetupHold  SetupHoldSet
	Serial     SerialSet
}

// Default is the start-up trigger: edge on CH1, rising, 0 V, Auto.
func Default() Settings {
	return Settings{Type: Edge, Sweep: Auto, Limit: Less, UpperS: 2e-9, LowerS: 2e-9, Count: 1}
}

// Chan is what the trigger needs of a channel's vertical setting.
type Chan struct {
	VoltsDiv float64 // displayed V/div (probe included)
	PosPx    float64 // trace position in screen rows
	Class    int     // V/div class of spec 06 §6.1
	Invert   bool
}

// Trigger is the trigger engine.
type Trigger struct {
	a, m *mmio.Plane
	nch  int

	s  Settings
	ch [4]Chan
}

// New returns the trigger engine on the ACQ (A) and MAP (M) planes of a
// unit with nch analog channels.
func New(acq, mp *mmio.Plane, nch int) *Trigger { return &Trigger{a: acq, m: mp, nch: nch} }

// ---------------------------------------------------------------- levels (§3)

// NoiseValue is the hysteresis h of spec 05 §3.5 for a V/div class.
func NoiseValue(class int, reject bool) int {
	var c float32
	switch {
	case class >= 12:
		c = 0.13
		if reject {
			c = 0.33
		}
	case class >= 10:
		c = 0.26
		if reject {
			c = 0.33
		}
	default:
		c = 0.5
	}
	return min(int(uint16(float32(480)*c)), 255)
}

// LevelPx is L_px of spec 05 §3.2: the level in screen rows from the
// centre, with the channel's position for DC and HF reject, clamped to
// ±246 rows (±4.1 div).
func LevelPx(v, voltsDiv, posPx float64, cp Coupling) int {
	l := v * 60 / voltsDiv
	if l >= 0 {
		l = math.Trunc(l + 0.5001)
	} else {
		l = math.Trunc(l - 0.5001)
	}
	if cp == CoupDC || cp == CoupHFReject {
		l += math.Trunc(posPx)
	}
	return int(max(-246, min(246, l)))
}

// LevelCode is the comparator code of spec 05 §3.3: the threshold the
// signal crosses from the far side of the hysteresis band.
func LevelCode(px int, s Dir, h int) uint16 {
	d := 0
	switch s {
	case Rising:
		d = h
	case Falling:
		d = -h
	case Alternate:
		// either edge: no hysteresis bias
	}
	return uint16(2048 + int(float32(px)*8.0) - d) //nolint:gosec // G115: the comparator register takes the code's low 16 bits (spec 05 §3.3)
}

// chanOf is the vertical setting a level of src is converted with; the AC
// line counts as 0.5 V/div at probe 1 (§3.2).
func (t *Trigger) chanOf(src int) Chan {
	if src >= 0 && src < 4 {
		return t.ch[src]
	}
	return Chan{VoltsDiv: 0.5, Class: 12}
}

// px converts a level of src. An inverted channel keeps its level in the
// displayed, inverted domain (§3.3).
func (t *Trigger) px(src int, v float64, cp Coupling) int {
	c := t.chanOf(src)
	if c.Invert {
		v = -v
	}
	return LevelPx(v, c.VoltsDiv, c.PosPx, cp)
}

// bias is the slope a level of src is biased with: an inverted channel
// takes the opposite slope (§3.3).
func (t *Trigger) bias(src int, s Dir) Dir {
	if src >= 0 && src < 4 && t.ch[src].Invert && s != Alternate {
		return 1 - s
	}
	return s
}

// Per-channel comparator pairs (code, hysteresis), spec 05 §2.1.
var (
	pair1     = [4][2]int{{0x0D7, 0x0D6}, {0x0DB, 0x0DA}, {0x0F1, 0x0F0}, {0x101, 0x100}}
	pair2     = [4][2]int{{0x0D9, 0x0D8}, {0x0DD, 0x0DC}, {0x0F3, 0x0F2}, {0x103, 0x102}}
	searchCmp = [4][2]int{{0x0B4, 0x0B2}, {0x0CC, 0x045}, {0x104, 0x108}, {0x106, 0x109}}
	edgeSel   = [4]int{0x0B5, 0x0CD, 0x105, 0x107}
)

// multiSource reports the types that write only per-channel pairs.
func multiSource(t Type) bool {
	return t == Pattern || t == Qualified || t == Delay || t == SetupHold
}

// h is the noise value of the current type (§3.5, §5.8).
func (t *Trigger) h() int {
	s := &t.s
	reject := s.NoiseReject
	src := t.source()
	switch s.Type { //nolint:exhaustive // only the multi-source types turn noise reject off
	case Qualified, Delay, SetupHold:
		reject = false
	}
	switch s.Type { //nolint:exhaustive // only the pattern-class types take their noise class from the pattern
	case Pattern:
		src = t.patternClassChannel(&s.Pattern)
	case Delay:
		src = t.patternClassChannel(&s.Pattern)
	}
	h := NoiseValue(t.chanOf(src).Class, reject)
	if h == 0 {
		h = 128
	}
	return h
}

// patternClassChannel is the channel whose V/div class gives a pattern's
// noise value: the last channel the pattern addresses (§5.8).
func (t *Trigger) patternClassChannel(p *PatternSet) int {
	last := t.nch - 1
	for c := t.nch - 1; c >= 0; c-- {
		if p.States[c] != DontCare {
			return c
		}
	}
	return last
}

// writeLevel writes LEVEL1 (n 1) or LEVEL2 (n 2) and the source channel's
// pair of that level (single-source types, §3.3).
func (t *Trigger) writeLevel(n, src int, v float64, s Dir, h int, cp Coupling) {
	px := t.px(src, v, cp)
	s = t.bias(src, s)
	code, hr, pr := 0x0E0, 0x0E1, pair1
	if n == 2 {
		code, hr, pr = 0x0E2, 0x0E3, pair2
	}
	t.a.Set(code, LevelCode(px, s, h))
	t.a.Set(hr, uint16(h)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	if src >= 0 && src < 4 {
		hc := min(h, 240)
		t.a.Set(pr[src][0], LevelCode(px, s, hc))
		t.a.Set(pr[src][1], uint16(hc)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	}
}

// writePair1 writes channel ch's level-1 pair with the unclamped h
// (Pattern, Qualified, Delay, Setup/Hold).
func (t *Trigger) writePair1(ch int, v float64, s Dir, h int) {
	if ch < 0 || ch >= t.nch {
		return
	}
	t.a.Set(pair1[ch][0], LevelCode(t.px(ch, v, CoupDC), t.bias(ch, s), h))
	t.a.Set(pair1[ch][1], uint16(h)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
}

// patternSlope is EDGE_SEL and the level bias of a pattern state under a
// logic code (§5.8).
func patternSlope(st State, logic uint16) (sel uint16, s Dir, ok bool) {
	inv := logic == 1 || logic == 3
	switch st {
	case Low:
		if inv {
			return 2, Falling, true
		}
		return 1, Rising, true
	case High:
		if inv {
			return 1, Rising, true
		}
		return 2, Falling, true
	case DontCare:
		// the channel takes no part in the pattern: below
	}
	return 0, Alternate, false
}

// writeLevels writes the current type's levels, then the search step.
func (t *Trigger) writeLevels() {
	s := &t.s
	h := t.h()
	switch s.Type { //nolint:exhaustive // default: the single-source types write one or two levels
	case Serial:
		t.writeSlicers()
		return
	case Pattern:
		// written per channel with the states (writePattern)
	case Delay:
		// Event A's channels and event B's channel, all biased by event B's
		// slope (§3.3).
		for c := range t.nch {
			if s.Pattern.States[c] != DontCare && c != s.Source {
				t.writePair1(c, s.Pattern.Levels[c], s.Slope, h)
			}
		}
		t.writePair1(s.Source, s.Level2, s.Slope, h)
	case Qualified:
		t.writePair1(s.Qual.Source, s.Qual.Level, s.Slope, h)
		t.writePair1(s.Source, s.Level2, s.Slope, h)
	case SetupHold:
		sl := Rising
		if s.SetupHold.DataHigh {
			sl = Falling
		}
		t.writePair1(s.Source, s.Level, sl, h)
		t.writePair1(s.SetupHold.Data, s.SetupHold.DataLevel, sl, h)
	default:
		s1, s2 := t.levelSlopes()
		t.writeLevel(1, s.Source, s.Level, s1, h, s.Coupling)
		if s.Type == Slope || s.Type == Window || s.Type == Runt {
			t.writeLevel(2, s.Source, s.Level2, s2, h, s.Coupling)
		}
	}
	t.searchStep(h)
}

// levelSlopes are the biases of level 1 and level 2 of a single-source
// type (§3.3).
func (t *Trigger) levelSlopes() (Dir, Dir) {
	s := &t.s
	switch s.Type { //nolint:exhaustive // the other single-source types bias both levels by the slope, below
	case Pulse, Runt:
		return 1 - s.Slope, 1 - s.Slope
	case Window:
		return Falling, Rising
	case Video:
		return Rising, Rising
	}
	return s.Slope, s.Slope
}

// searchStep copies the levels into the waveform-search comparators and,
// for the multi-source types, writes the per-channel edge selects (§3.3).
func (t *Trigger) searchStep(h int) {
	s := &t.s
	a := t.a
	hc := uint16(min(h, 240)) //nolint:gosec // G115: the noise value is 0 … 255 (NoiseValue)
	if multiSource(s.Type) {
		pat, lc := a.Shadow(0x0AC), a.Shadow(0x0AD)&3
		for c := range t.nch {
			a.Set(searchCmp[c][0], a.Shadow(pair1[c][0]))
			a.Set(searchCmp[c][1], hc)
			if sel, _, ok := patternSlope(State(pat>>(4*c))&3, lc); ok { //nolint:gosec // G115: a 2-bit pattern state
				a.Set(edgeSel[c], sel)
			}
		}
		return
	}
	use0, use1 := true, false
	switch s.Type { //nolint:exhaustive // default: the other types use level 1 for either slope only
	case Slope:
		use0, use1 = s.Slope != Rising, s.Slope == Rising
	case Runt:
		use0, use1 = s.Slope == Rising, s.Slope != Rising
	case Window:
		use1 = true
	default:
		use1 = s.Slope == Alternate
	}
	if use0 {
		a.Set(0x0B4, a.Shadow(0x0E0))
		a.Set(0x0B2, hc)
	}
	if use1 {
		a.Set(0x0CC, a.Shadow(0x0E2))
		a.Set(0x045, hc)
	}
}

// ---------------------------------------------------------------- source (§3.1, §3.4, §4)

// source is the channel A 0x0E[10:8] carries for the current type.
func (t *Trigger) source() int {
	s := &t.s
	switch s.Type { //nolint:exhaustive // the other types carry their own source, below
	case Pattern:
		return 0
	case SetupHold:
		if s.SetupHold.Hold {
			return s.SetupHold.Data
		}
	}
	return s.Source
}

func sourceCode(src int) uint16 {
	switch {
	case src >= 0 && src < 4:
		return uint16(src)
	case src == SourceACLine:
		return 5
	}
	return 6
}

// writeSource writes the source path, the coupling and the search source.
func (t *Trigger) writeSource() {
	s := &t.s
	a := t.a
	src := t.source()
	a.Field(0x00E, 10, 8, sourceCode(src))
	a.Field(0x00A, 15, 15, b2u(src == SourceACLine))
	a.Field(0x033, 0, 0, 0)
	if t.rollAuto() {
		a.Field(0x00A, 15, 15, 0)
	}
	if s.Type != Delay {
		a.Set(0x0E4, uint16(s.Coupling)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	}
	if src >= 0 && src < 4 && s.Type != Pattern {
		a.Set(0x0B0, uint16(src))
	}
}

// counterSource is the frequency counter's source: the trigger source, or
// for Pattern the channel the pattern addresses last (spec 05 §9). The AC
// line leaves it as it is.
func (t *Trigger) counterSource() (int, bool) {
	switch t.s.Type { //nolint:exhaustive // the other types count on the trigger source, below
	case Pattern:
		return t.patternClassChannel(&t.s.Pattern), true
	case Serial:
		return 0, false
	}
	src := t.source()
	return src, src >= 0 && src < 4
}

func (t *Trigger) rollAuto() bool { return t.s.Roll && t.s.Sweep == Auto }

// ---------------------------------------------------------------- slope (§5.2)

func (t *Trigger) writeSlope() {
	s := &t.s
	a := t.a
	sl := t.bias(t.source(), s.Slope)
	switch s.Type { //nolint:exhaustive // Pattern and Serial have no slope field (§5.2)
	case Edge, NthEdge:
		a.Field(0x011, 9, 8, uint16(sl)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		a.Set(0x0B5, uint16(sl+1))       //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	case Slope:
		a.Field(0x011, 9, 8, uint16(sl)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		a.Field(0x02C, 0, 0, uint16(sl)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		a.Set(0x0B5, uint16(sl+1))       //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	case Pulse:
		a.Field(0x011, 7, 7, b2u(sl == Rising))
		a.Set(0x0B5, pick(sl == Rising, 2, 1))
	case Video:
		a.Field(0x011, 9, 8, 0)
		a.Set(0x0B5, 1)
	case Interval:
		a.Field(0x02D, 0, 0, b2u(sl == Rising))
		a.Set(0x0B5, uint16(sl+1)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	case Dropout:
		a.Field(0x02C, 8, 8, b2u(sl == Rising))
		a.Set(0x0B5, pick(sl == Rising, 1, 2))
	case Window:
		a.Field(0x011, 9, 8, 3)
		a.Set(0x0B5, 2)
		a.Set(0x0CD, 1)
	case Runt:
		a.Field(0x02D, 8, 8, b2u(sl == Rising))
		a.Set(0x0B5, pick(sl == Rising, 2, 1))
	case Qualified:
		a.Field(0x080, 7, 7, b2u(sl == Rising))
	case Delay:
		a.Field(0x080, 7, 7, b2u(sl == Rising))
		a.Field(0x080, 5, 5, b2u(s.DelayARise))
	case SetupHold:
		t.writeSetupHold()
	}
}

// ---------------------------------------------------------------- times (§5.3)

// NS rounds seconds to whole nanoseconds.
func NS(sec float64) uint64 { return uint64(math.Max(0, math.Round(sec*1e9))) }

func (t *Trigger) setTA(ns uint64) {
	t.a.Set(0x012, uint16(ns))                 //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.a.Set(0x013, uint16(ns>>16))             //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.a.Field(0x072, 3, 0, uint16(ns>>32)&0xF) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
}

func (t *Trigger) setTB(ns uint64) {
	t.a.Set(0x014, uint16(ns))                  //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.a.Set(0x015, uint16(ns>>16))              //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.a.Field(0x072, 11, 8, uint16(ns>>32)&0xF) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
}

// limitField is where each type's limit code goes.
var limitField = map[Type][3]int{
	Slope: {0x02C, 2, 1}, Pulse: {0x011, 1, 0}, Interval: {0x02D, 2, 1}, Runt: {0x02D, 13, 12},
	Pattern: {0x0AD, 5, 4}, Qualified: {0x080, 9, 8}, Delay: {0x080, 9, 8}, SetupHold: {0x080, 9, 8},
}

func (t *Trigger) writeTimes() {
	s := &t.s
	switch s.Type { //nolint:exhaustive // the other types take their times from the limit, below
	case Dropout:
		t.setTA(NS(s.TimeoutS))
		return
	case NthEdge:
		t.setTB(NS(s.IdleS))
		return
	}
	f, ok := limitField[s.Type]
	if !ok {
		return
	}
	t.a.Field(f[0], f[1], f[2], uint16(s.Limit)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	switch s.Limit {                             //nolint:exhaustive // default: the in-range and out-of-range limits use both times
	case Less:
		t.setTA(NS(s.UpperS))
	case Greater:
		t.setTB(NS(s.LowerS))
	default:
		t.setTA(NS(s.LowerS))
		t.setTB(NS(s.UpperS))
	}
}

// ---------------------------------------------------------------- holdoff (§6)

// forcesHoldoffOff and forcesDC are the types that run with holdoff off and
// with DC coupling whatever is set (§1, §5.1 step 6).
func forcesHoldoffOff(t Type) bool {
	return t == Video || t == Serial || t == Qualified || t == Delay || t == SetupHold
}

func forcesDC(t Type) bool { return forcesHoldoffOff(t) || t == Pattern }

// ForcesHoldoffOff reports a type that runs with holdoff off.
func ForcesHoldoffOff(t Type) bool { return forcesHoldoffOff(t) }

// ForcesDC reports a type that runs with DC trigger coupling.
func ForcesDC(t Type) bool { return forcesDC(t) }

// HoldoffTicks is the holdoff count: whole nanoseconds over 4 (§6).
func HoldoffTicks(sec float64) uint64 { return NS(sec) / 4 }

func (t *Trigger) writeHoldoff() {
	s := &t.s
	a := t.a
	if forcesHoldoffOff(s.Type) || s.HoldoffS <= 0 || s.Sweep == Force {
		a.Field(0x00A, 8, 8, 0)
		return
	}
	ticks := HoldoffTicks(s.HoldoffS)
	a.Set(0x008, uint16(ticks))     //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	a.Set(0x009, uint16(ticks>>16)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	a.Set(0x052, uint16(ticks>>32)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	a.Field(0x00A, 8, 8, 1)
}

// ---------------------------------------------------------------- type-specific parts

// writePattern writes A 0x0AC one channel at a time, each followed by the
// channel's level and the search step (§5.8).
func (t *Trigger) writePattern() {
	p := &t.s.Pattern
	h := t.h()
	for c := range t.nch {
		t.a.Field(0x0AC, 4*c+1, 4*c, uint16(p.States[c]))
		if t.s.Type == Pattern && p.States[c] != DontCare {
			_, sl, _ := patternSlope(p.States[c], t.a.Shadow(0x0AD)&3)
			t.writePair1(c, p.Levels[c], sl, h)
		}
		t.searchStep(h)
	}
}

// qualSource is a qualifier source field value (§5.9).
func qualSource(src int) uint16 {
	if src >= 0 && src < 4 {
		return uint16(src)
	}
	return uint16(src + 12) //nolint:gosec // G115: a qualifier source field code
}

func (t *Trigger) writeQual() {
	q := &t.s.Qual
	t.a.Field(0x080, 4, 0, qualSource(q.Source))
	t.a.Field(0x080, 14, 12, uint16(q.Mode)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.a.Field(0x080, 5, 5, b2u(q.High))
}

func (t *Trigger) writeSetupHold() {
	s := &t.s
	sh := &s.SetupHold
	a := t.a
	rise := s.Slope == Rising
	if !sh.Hold {
		a.Field(0x080, 14, 12, 1)
		a.Field(0x080, 4, 0, qualSource(sh.Data))
		a.Field(0x080, 5, 5, b2u(sh.DataHigh))
		a.Field(0x080, 7, 7, b2u(rise))
		return
	}
	a.Field(0x080, 14, 12, 3)
	a.Field(0x080, 4, 0, qualSource(s.Source))
	a.Field(0x080, 5, 5, b2u(rise))
	a.Field(0x080, 7, 7, b2u(!sh.DataHigh))
}

// ---------------------------------------------------------------- programming

// Program writes s with the channels' vertical settings ch: the sweep mode
// bits, then the type sequence of the type (spec 05 §7, §5.1). The caller
// holds the acquisition stopped.
func (t *Trigger) Program(s Settings, ch [4]Chan) {
	t.s, t.ch = s, ch
	a := t.a
	var cont, auto uint16
	switch s.Sweep {
	case Auto:
		if !s.Roll {
			cont, auto = 1, 1
		}
	case Normal:
		cont = b2u(!s.Roll)
	case Force:
		auto = 1
	case Single:
		// one capture: neither mode bit
	}
	a.Field(0x00E, 12, 12, cont)
	a.Field(0x00A, 0, 0, auto)
	if s.Sweep == Force {
		t.forceRow()
	}
	t.typeSequence()
	if src, ok := t.counterSource(); ok {
		a.Field(0x00E, 6, 4, uint16(src)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	}
}

// forceRow is the Force row of §7: holdoff off, both timeouts 1.
func (t *Trigger) forceRow() {
	t.a.Field(0x00A, 8, 8, 0)
	t.a.Set32(0x00C, 0x00D, 1, 0xFFFF)
	t.a.Set32(0x02A, 0x02B, 1, 0xFFFF)
}

// ForceOnce is the one-shot forced trigger of §7: the Force row's mode bits
// over the programmed type. Restore re-programs the selected mode.
func (t *Trigger) ForceOnce() {
	t.a.Field(0x00E, 12, 12, 0)
	t.a.Field(0x00A, 0, 0, 1)
	t.forceRow()
}

// Restore re-programs the selected sweep mode's bits and holdoff after
// ForceOnce. The timeouts stay 1 until the next timebase setup (§7).
func (t *Trigger) Restore() {
	s := &t.s
	var cont, auto uint16
	switch s.Sweep {
	case Auto:
		if !s.Roll {
			cont, auto = 1, 1
		}
	case Normal:
		cont = b2u(!s.Roll)
	case Single, Force:
		// neither mode bit
	}
	t.a.Field(0x00E, 12, 12, cont)
	t.a.Field(0x00A, 0, 0, auto)
	t.writeHoldoff()
}

// typeSequence is the type-change sequence of §5.1.
func (t *Trigger) typeSequence() {
	s := &t.s
	a := t.a
	// 1.–2.
	a.Field(0x011, 9, 8, 0)
	switch s.Type { //nolint:exhaustive // default: the other types clear the qualifier enable
	case Delay:
		a.Field(0x080, 14, 12, 7)
		a.Field(0x080, 6, 6, 1)
		a.Field(0x0AD, 1, 0, 0)
	case SetupHold:
		a.Field(0x080, 6, 6, 1)
	default:
		a.Field(0x080, 6, 6, 0)
	}
	// 3.
	code := typeCode[s.Type]
	if t.rollAuto() {
		code = codeDisabled
	}
	a.Field(0x00E, 3, 0, code)
	a.Set(0x042, 26)
	a.Set(0x0E7, typeAux[s.Type])
	// 4.
	switch s.Type { //nolint:exhaustive // the other types have no type-specific registers (§5.1 step 4)
	case Video:
		a.Field(0x011, 7, 7, 0)
		a.Field(0x011, 1, 0, 1)
		t.setTB(150)
		t.writeVideo()
	case Qualified:
		t.writeQual()
	case SetupHold:
		t.writeSetupHold()
	case Delay:
		a.Field(0x080, 5, 5, b2u(s.DelayARise))
	case Pattern:
		a.Field(0x0AD, 1, 0, logicCode[s.Pattern.Logic&3])
	case NthEdge:
		a.Set(0x087, uint16(max(1, min(65535, s.Count))))
	case Dropout:
		a.Field(0x02C, 12, 12, b2u(s.DropoutEdge))
	case Serial:
		t.writeSerial()
	}
	// 5.
	if s.Type != Serial {
		t.writeSource()
		t.writeSlope()
	}
	switch s.Type { //nolint:exhaustive // default: the other types write their levels
	case Pattern, Delay:
		t.writePattern()
		if s.Type == Delay {
			t.writeLevels()
		}
	default:
		t.writeLevels()
	}
	t.writeTimes()
	// 6.
	t.writeHoldoff()
	if forcesDC(s.Type) {
		a.Set(0x0E4, 0)
	}
}

// PreviewCodes is the level-1 code without slope bias and the hysteresis of
// a single-source type, for the map engine's level preview.
func (t *Trigger) PreviewCodes() (code, h uint16) {
	s := &t.s
	if multiSource(s.Type) || s.Type == Serial {
		return 2048, 62
	}
	hh := t.h()
	return LevelCode(t.px(s.Source, s.Level, s.Coupling), Alternate, 0), uint16(hh) //nolint:gosec // G115: the noise value is 0 … 255 (NoiseValue)
}

// RestoreDefaults writes the start-up values of every trigger type's
// registers (spec 05 §7, start-up restore), so nothing the vendor
// application programmed is left behind.
func (t *Trigger) RestoreDefaults() {
	a := t.a
	a.Set(0x00F, 0)
	a.Field(0x0AF, 8, 8, 0)
	a.Set(0x198, 0)
	a.Set(0x199, 0)
	a.Field(0x033, 8, 8, 1)
	a.Set(0x0DB, 0x7C2)
	a.Set(0x0DA, 0x3E)
	a.Set(0x111, 0x1000, 0)
	for _, pr := range [][2]int{{0x0F1, 0x0F0}, {0x101, 0x100}, {0x0F3, 0x0F2}} {
		a.Set(pr[0], 0x7C2)
		a.Set(pr[1], 0x3E)
	}
	a.Set(0x012, 2)
	a.Set(0x013, 0)
	a.Set(0x072, 0)
	a.Set(0x014, 2)
	a.Set(0x015, 0)
	a.Set(0x017, 0x13E0)
	a.Set(0x016, 0x1902)
	a.Set(0x02C, 0)
	a.Set(0x0AC, 0x2222)
	a.Set(0x0AD, 0)
	for _, pr := range slicer {
		a.Set(pr[0], 0xAE1)
		a.Set(pr[1], 0x3E)
	}
	a.Set(0x080, 0x1001)
	a.Set(0x087, 1)
	a.Set(0x044, 0)
	a.Set(0x029, 0)
	for _, r := range serialRestore {
		t.m.Set(int(r[0]), r[1])
	}
}

func b2u(b bool) uint16 {
	if b {
		return 1
	}
	return 0
}

func pick(b bool, x, y uint16) uint16 {
	if b {
		return x
	}
	return y
}
