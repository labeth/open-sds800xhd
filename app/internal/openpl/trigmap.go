package openpl

import (
	"fmt"
	"math"

	"github.com/labeth/open-sds800xhd/app/internal/trigger"
)

// Trigger engines (TRIG_CTRL.engine).
const (
	EngOff      = 0
	EngInterval = 1
	EngNth      = 2
	EngSerial   = 4
)

// Interval-engine conditions (TRIG_COND.cond, spec 20 §7.4).
const (
	CondAny     = 0
	CondLess    = 1
	CondGreater = 2
	CondInside  = 3
	CondOutside = 4
)

// Event bits (spec 20 §7.3): channel c's comparator A rise/fall, B
// rise/fall; the pattern; the protocol match. State bits: A of c, B of c,
// the pattern.
func evARise(c int) uint32 { return 1 << (4 * c) }
func evAFall(c int) uint32 { return 1 << (4*c + 1) }
func evBRise(c int) uint32 { return 1 << (4*c + 2) }
func evBFall(c int) uint32 { return 1 << (4*c + 3) }

const (
	evPatTrue  = 1 << 16
	evPatFalse = 1 << 17
	evMatch    = 1 << 18
	stPattern  = 1 << 8
)

// Cmp is one comparator's thresholds (codes, hi > lo).
type Cmp struct{ Hi, Lo uint16 }

// Proto is the PROTO block.
type Proto struct {
	Engine          int // 1 UART, 2 SPI, 3 I2C
	A, B            int // channels of lines A and B
	SliceA, SliceB  Cmp
	TimingClocks    int // UART bit time or SPI idle gap, cclk cycles
	CPOL, CPHA, MSB bool
	Address         int
	AnyAddress      bool
	Direction       int // 0 write, 1 read, 2 any
	Pattern         uint32
	PatternLen      int
}

// Trig is the TRIG block (and PROTO for the serial engine).
type Trig struct {
	Engine              int
	Open, Close, Abort  uint32
	QualMask, QualReq   uint16
	Cond                int
	Reach, AlwaysArmed  bool
	TA, TB              uint64 // ticks
	Holdoff             uint64 // ticks, 0: off
	AutoClocks          uint64 // cclk cycles, 0: no auto trigger
	Nth                 int
	PatCare, PatLevel   uint8
	PatOr, PatInvert    bool
	Cmp                 [4][2]Cmp // channel, comparator A/B
	Interp              bool
	InterpCh, InterpCmp int
	InterpFalling       bool
	Proto               Proto
	Node                string // what fires, for frames ("edge", "pulse", ...)
}

func (p *PL) writeTrig(t Trig) {
	w := p.r.Write32
	ctrl := uint32(t.Engine & 15)
	if t.Holdoff > 0 {
		ctrl |= 1 << 8
	}
	if t.AutoClocks > 0 {
		ctrl |= 1 << 9
	}
	if t.Interp {
		ctrl |= 1<<10 | uint32(t.InterpCh&3)<<12 | uint32(t.InterpCmp&1)<<14
		if t.InterpFalling {
			ctrl |= 1 << 15
		}
	}
	w(TrigCtrl, ctrl)
	w(TrigOpen, t.Open)
	w(TrigClose, t.Close)
	w(TrigAbort, t.Abort)
	w(TrigQualMask, uint32(t.QualMask&0x1FF))
	w(TrigQualReq, uint32(t.QualReq&0x1FF))
	c := uint32(t.Cond & 7)
	if t.Reach {
		c |= 1 << 4
	}
	if t.AlwaysArmed {
		c |= 1 << 5
	}
	w(TrigCond, c)
	w48 := func(lo uint32, v uint64) { w(lo, uint32(v)); w(lo+4, uint32(v>>32)&0xFFFF) } //nolint:gosec // G115: a 48-bit value in two words
	w48(TrigTaLo, t.TA)
	w48(TrigTbLo, t.TB)
	w48(TrigHoldoffLo, t.Holdoff)
	w48(TrigAutoLo, t.AutoClocks)
	w(TrigNth, uint32(max(1, t.Nth)&0xFFFF))
	pat := uint32(t.PatCare&15) | uint32(t.PatLevel&15)<<4
	if t.PatOr {
		pat |= 1 << 8
	}
	if t.PatInvert {
		pat |= 1 << 9
	}
	w(TrigPattern, pat)
	for ch := range 4 {
		base := TrigCmp0AHi + uint32(16*ch)
		w(base, uint32(t.Cmp[ch][0].Hi))
		w(base+4, uint32(t.Cmp[ch][0].Lo))
		w(base+8, uint32(t.Cmp[ch][1].Hi))
		w(base+12, uint32(t.Cmp[ch][1].Lo))
	}
	pr := t.Proto
	if pr.Engine == 0 {
		w(ProtoCtrl, 0)
		return
	}
	w(ProtoCtrl, uint32(pr.Engine&15)|1<<8)
	lines := uint32(pr.A&3) | uint32(pr.B&3)<<4
	w(ProtoLines, lines)
	w(ProtoSliceA, uint32(pr.SliceA.Hi)|uint32(pr.SliceA.Lo)<<16)
	w(ProtoSliceB, uint32(pr.SliceB.Hi)|uint32(pr.SliceB.Lo)<<16)
	w(ProtoTiming, uint32(pr.TimingClocks)&0xFFFFFF) //nolint:gosec // G115: 24 bits
	o := uint32(pr.Address&0x7F)<<8 | uint32(pr.Direction&3)<<16
	for i, b := range []bool{pr.CPOL, pr.CPHA, pr.MSB} {
		if b {
			o |= 1 << i
		}
	}
	if pr.AnyAddress {
		o |= 1 << 15
	}
	w(ProtoOpts, o)
	w(ProtoPattern, pr.Pattern)
	w(ProtoPatternLen, uint32(pr.PatternLen&7))
}

// Chan is what the conversion needs of a channel's vertical setting.
type Chan = trigger.Chan

// LevelCode is the 12-bit code of a level, in the comparator domain of
// spec 05 §3.2/§3.3 (480 codes a division about mid-scale with the trace
// position), before any hysteresis.
func LevelCode(v float64, c Chan, cp trigger.Coupling) int {
	if c.Invert {
		v = -v
	}
	return 2048 + 8*trigger.LevelPx(v, c.VoltsDiv, c.PosPx, cp)
}

func clamp12(v int) uint16 { return uint16(max(1, min(4094, v))) }

// edgeCmp is a comparator for an edge of slope dir at level code l with
// hysteresis h (spec 20 §7.2): rising hi = L, lo = L − h; falling lo = L,
// hi = L + h; either or a state hi = L + h/2, lo = L − h/2.
func edgeCmp(l, h int, d trigger.Dir) Cmp {
	switch d {
	case trigger.Rising:
		return Cmp{clamp12(l), clamp12(l - h)}
	case trigger.Falling:
		return Cmp{clamp12(l + h), clamp12(l)}
	case trigger.Alternate:
	}
	return Cmp{clamp12(l + h/2), clamp12(l - h/2)}
}

// Ticks is a time in ticks.
func Ticks(s float64) uint64 { return uint64(max(0, math.Round(s/TickS))) }

// RefusalError is a trigger the open image cannot run, with the reason.
type RefusalError struct{ What, Why string }

func (r *RefusalError) Error() string { return r.What + ": " + r.Why }

func refuse(what, why string) error { return &RefusalError{What: what, Why: why} }

// FromSettings maps the board's trigger settings onto the engines of spec
// 20 §7.4 (the table there, row by row). ch are the channels' vertical
// settings; recordS the record's duration (it sizes the auto timeout).
func FromSettings(s trigger.Settings, ch [4]Chan, caps uint32, recordS float64) (Trig, error) {
	t := Trig{Engine: EngInterval}
	if s.Type != trigger.Pattern && (s.Source < 0 || s.Source > 3) {
		return t, refuse("trigger source", "the open image triggers on the analog channels only (AC line: spec 20 §14 U11)")
	}
	m := &mapper{t: &t, s: s, ch: ch, caps: caps, src: s.Source}
	done, err := m.levelTypes()
	if err == nil && !done {
		done, err = m.logicTypes()
	}
	if err != nil {
		return t, err
	}
	if !done {
		return t, refuse("trigger", fmt.Sprintf("the %v type has no mapping onto the open engines", s.Type))
	}
	if s.HoldoffS > 0 {
		t.Holdoff = Ticks(s.HoldoffS)
	}
	if s.Sweep == trigger.Auto {
		// The auto timeout: a record's time and 10 ms more, so a trigger
		// that comes at the display rate is never pre-empted (spec 05 §7).
		t.AutoClocks = uint64(math.Ceil((recordS + 10e-3) * CclkHz))
	}
	return t, nil
}

// mapper holds one conversion's inputs.
type mapper struct {
	t    *Trig
	s    trigger.Settings
	ch   [4]Chan
	caps uint32
	src  int
}

func (m *mapper) h(c int) int { return trigger.NoiseValue(m.ch[c].Class, m.s.NoiseReject) }

func (m *mapper) lvl(c int, v float64) int { return LevelCode(v, m.ch[c], m.s.Coupling) }

// limit is the time condition of the settings.
func (m *mapper) limit() {
	t, s := m.t, m.s
	switch s.Limit {
	case trigger.Less:
		t.Cond, t.TA = CondLess, Ticks(s.UpperS)
	case trigger.Greater:
		t.Cond, t.TB = CondGreater, Ticks(s.LowerS)
	case trigger.Inside:
		t.Cond, t.TA, t.TB = CondInside, Ticks(s.LowerS), Ticks(s.UpperS)
	case trigger.Outside:
		t.Cond, t.TA, t.TB = CondOutside, Ticks(s.LowerS), Ticks(s.UpperS)
	}
}

// interp places the trigger with the interpolator on comparator cmp of c.
func (m *mapper) interp(c, cmp int, falling bool) {
	if m.caps&CapInterp != 0 {
		m.t.Interp, m.t.InterpCh, m.t.InterpCmp, m.t.InterpFalling = true, c, cmp, falling
	}
}

// slopeEv is comparator A's events of a slope.
func slopeEv(c int, d trigger.Dir) uint32 {
	switch d {
	case trigger.Rising:
		return evARise(c)
	case trigger.Falling:
		return evAFall(c)
	case trigger.Alternate:
	}
	return evARise(c) | evAFall(c)
}

// levelTypes maps the single-input level types (edge, pulse, interval,
// slope, runt, window, dropout); false for another type.
func (m *mapper) levelTypes() (bool, error) {
	t, s, src := m.t, m.s, m.src
	h, lvl, limit, interp := m.h, m.lvl, m.limit, m.interp
	switch s.Type { //nolint:exhaustive // the other types are logicTypes'
	case trigger.Edge:
		t.Node = "edge"
		t.Cmp[src][0] = edgeCmp(lvl(src, s.Level), h(src), s.Slope)
		t.Close, t.AlwaysArmed = slopeEv(src, s.Slope), true
		if s.Slope != trigger.Alternate {
			interp(src, 0, s.Slope == trigger.Falling)
		}
	case trigger.Pulse:
		t.Node = "pulse"
		t.Cmp[src][0] = edgeCmp(lvl(src, s.Level), h(src), trigger.Alternate)
		if s.Slope == trigger.Rising {
			t.Open, t.Close = evARise(src), evAFall(src)
		} else {
			t.Open, t.Close = evAFall(src), evARise(src)
		}
		limit()
		interp(src, 0, s.Slope == trigger.Rising)
	case trigger.Interval:
		t.Node = "interval"
		t.Cmp[src][0] = edgeCmp(lvl(src, s.Level), h(src), s.Slope)
		t.Open, t.Close = slopeEv(src, s.Slope), slopeEv(src, s.Slope)
		limit()
		if s.Slope != trigger.Alternate {
			interp(src, 0, s.Slope == trigger.Falling)
		}
	case trigger.Slope, trigger.Runt, trigger.Window:
		lo, hi := lvl(src, s.Level), lvl(src, s.Level2)
		t.Cmp[src][0] = edgeCmp(lo, h(src), trigger.Alternate)
		t.Cmp[src][1] = edgeCmp(hi, h(src), trigger.Alternate)
		a, b := 0, 1
		switch {
		case s.Type == trigger.Window:
			t.Node = "window"
			t.Close, t.AlwaysArmed = evAFall(src)|evBRise(src), true
		case s.Type == trigger.Slope && s.Slope == trigger.Rising:
			t.Node = "slope"
			t.Open, t.Close, t.Abort = evARise(src), evBRise(src), evAFall(src)
			limit()
			interp(src, b, false)
		case s.Type == trigger.Slope:
			t.Node = "slope"
			t.Open, t.Close, t.Abort = evBFall(src), evAFall(src), evBRise(src)
			limit()
			interp(src, a, true)
		case s.Slope == trigger.Rising:
			t.Node = "runt"
			t.Open, t.Close, t.Abort = evARise(src), evAFall(src), evBRise(src)
			limit()
		default:
			t.Node = "runt"
			t.Open, t.Close, t.Abort = evBFall(src), evBRise(src), evAFall(src)
			limit()
		}
	case trigger.Dropout:
		t.Node = "dropout"
		t.Cmp[src][0] = edgeCmp(lvl(src, s.Level), h(src), trigger.Alternate)
		t.Cond, t.Reach, t.TB = CondGreater, true, Ticks(s.TimeoutS)
		switch {
		case s.DropoutEdge:
			t.Open = slopeEv(src, s.Slope)
		case s.Slope == trigger.Falling:
			t.Open, t.Abort = evAFall(src), evARise(src)
		default:
			t.Open, t.Abort = evARise(src), evAFall(src)
		}
	default:
		return false, nil
	}
	return true, nil
}

// logicTypes maps the types over several inputs or engines (pattern, delay,
// qualified, Nth edge, setup/hold, video, serial); false for another type.
func (m *mapper) logicTypes() (bool, error) {
	t, s, src := m.t, m.s, m.src
	h, lvl, limit, interp := m.h, m.lvl, m.limit, m.interp
	switch s.Type { //nolint:exhaustive // the level types are levelTypes'
	case trigger.Pattern:
		if err := pattern(t, s, m.ch, lvl, h); err != nil {
			return true, err
		}
		t.Node = "pattern"
		if s.Limit == trigger.Greater && s.LowerS <= 2e-9 {
			t.Close, t.AlwaysArmed = evPatTrue, true
			return true, nil
		}
		t.Open, t.Close = evPatTrue, evPatFalse
		limit()
		t.Reach = t.Cond == CondGreater
	case trigger.Delay:
		if err := pattern(t, s, m.ch, lvl, h); err != nil {
			return true, err
		}
		t.Node = "delay"
		if src < 0 || src > 3 {
			return true, refuse("delay trigger", "event B is an edge of an analog channel")
		}
		if t.PatCare&(1<<src) != 0 {
			return true, refuse("delay trigger", "event B's channel is also in event A's pattern: the channel has one comparator for both")
		}
		t.Cmp[src][0] = edgeCmp(lvl(src, s.Level2), h(src), s.Slope)
		t.Open = evPatTrue
		if !s.DelayARise {
			t.Open = evPatFalse
		}
		t.Close = slopeEv(src, s.Slope)
		limit()
		interp(src, 0, s.Slope == trigger.Falling)
	case trigger.Qualified:
		t.Node = "qualified"
		q := s.Qual
		if q.Source < 0 || q.Source > 3 || q.Source == src {
			return true, refuse("qualified trigger", "the qualifier is another analog channel")
		}
		t.Cmp[src][0] = edgeCmp(lvl(src, s.Level2), h(src), s.Slope)
		qd := trigger.Rising
		if !q.High {
			qd = trigger.Falling
		}
		t.Cmp[q.Source][0] = edgeCmp(lvl(q.Source, q.Level), h(q.Source), trigger.Alternate)
		edge := slopeEv(src, s.Slope)
		switch q.Mode {
		case trigger.QualState:
			t.Close, t.AlwaysArmed = edge, true
			t.QualMask = 1 << q.Source
			if q.High {
				t.QualReq = 1 << q.Source
			}
		case trigger.QualStateDelay:
			t.Open, t.Close, t.Abort = slopeEv(q.Source, qd), edge, slopeEv(q.Source, 1-qd)
			limit()
		case trigger.QualEdge:
			t.Open, t.Close = slopeEv(q.Source, qd), edge
		case trigger.QualEdgeDelay:
			t.Open, t.Close = slopeEv(q.Source, qd), edge
			limit()
		}
		interp(src, 0, s.Slope == trigger.Falling)
	case trigger.NthEdge:
		if m.caps&CapNth == 0 {
			return true, refuse("Nth edge trigger", "not in this image")
		}
		t.Node = "nth edge"
		t.Engine = EngNth
		t.Cmp[src][0] = edgeCmp(lvl(src, s.Level), h(src), s.Slope)
		t.Close, t.Nth, t.TB = slopeEv(src, s.Slope), s.Count, Ticks(s.IdleS)
		if s.Slope != trigger.Alternate {
			interp(src, 0, s.Slope == trigger.Falling)
		}
	case trigger.SetupHold:
		t.Node = "setup/hold"
		sh := s.SetupHold
		if sh.Data < 0 || sh.Data > 3 || sh.Data == src {
			return true, refuse("setup/hold", "the data line is another analog channel")
		}
		t.Cmp[src][0] = edgeCmp(lvl(src, s.Level), h(src), s.Slope)
		t.Cmp[sh.Data][0] = edgeCmp(lvl(sh.Data, sh.DataLevel), h(sh.Data), trigger.Alternate)
		clk, data := slopeEv(src, s.Slope), evARise(sh.Data)|evAFall(sh.Data)
		if sh.Hold {
			t.Open, t.Close = clk, data
			t.Cond, t.TA = CondLess, Ticks(s.UpperS)
		} else {
			t.Open, t.Close = data, clk
			t.Cond, t.TA = CondLess, Ticks(s.UpperS)
			interp(src, 0, s.Slope == trigger.Falling)
		}
	case trigger.Video:
		return true, refuse("video trigger", "not in this image (spec 20 §7.5: the video engine is specified, its RTL follows)")
	case trigger.Serial:
		if err := serial(t, s, m.ch, m.caps); err != nil {
			return true, err
		}
	default:
		return false, nil
	}
	return true, nil
}

func pattern(t *Trig, s trigger.Settings, ch [4]Chan, lvl func(int, float64) int, h func(int) int) error {
	p := s.Pattern
	for c := range 4 {
		switch p.States[c] {
		case trigger.Low, trigger.High:
			t.PatCare |= 1 << c
			if p.States[c] == trigger.High {
				t.PatLevel |= 1 << c
			}
			t.Cmp[c][0] = edgeCmp(lvl(c, p.Levels[c]), h(c), trigger.Alternate)
		case trigger.DontCare:
		}
	}
	if t.PatCare == 0 {
		return refuse("pattern", "every input is don't care")
	}
	t.PatOr = p.Logic == trigger.Or || p.Logic == trigger.Nor
	t.PatInvert = p.Logic == trigger.Nand || p.Logic == trigger.Nor
	_ = ch
	return nil
}

func serial(t *Trig, s trigger.Settings, ch [4]Chan, caps uint32) error {
	t.Engine = EngSerial
	t.Close, t.AlwaysArmed = evMatch, true
	slice := func(l trigger.Line) Cmp {
		c := l.Source
		code := LevelCode(l.Level, ch[c], trigger.CoupDC)
		return edgeCmp(code, trigger.NoiseValue(ch[c].Class, false), trigger.Alternate)
	}
	line := func(l trigger.Line, what string) error {
		if l.Source < 0 || l.Source > 3 {
			return refuse(what, "the line is an analog channel")
		}
		return nil
	}
	pr := &t.Proto
	sr := s.Serial
	switch sr.Protocol { //nolint:exhaustive // CAN and LIN: refused below
	case trigger.UART:
		if caps&CapUART == 0 {
			return refuse("UART trigger", "not in this image")
		}
		u := sr.UART
		if err := line(u.Line, "UART"); err != nil {
			return err
		}
		if u.Bits != 8 || u.Parity != 0 || u.MSBFirst {
			return refuse("UART trigger", "the open engine receives 8 data bits, no parity, LSB first (8N1)")
		}
		if u.Baud <= 0 {
			return refuse("UART trigger", "no baud rate")
		}
		t.Node = "uart"
		pr.Engine, pr.A, pr.SliceA = 1, u.Line.Source, slice(u.Line)
		pr.TimingClocks = int(math.Round(CclkHz / float64(u.Baud)))
		if u.Value >= 0 && u.Value < 256 {
			pr.Pattern, pr.PatternLen = uint32(u.Value), 1
		}
		return nil
	case trigger.SPI:
		if caps&CapSPI == 0 {
			return refuse("SPI trigger", "not in this image")
		}
		sp := sr.SPI
		if err := errorsJoin(line(sp.Clock, "SPI clock"), line(sp.Data, "SPI data")); err != nil {
			return err
		}
		if sp.N%8 != 0 || sp.N > 32 {
			return refuse("SPI trigger", "the open engine matches up to four whole bytes")
		}
		var pat uint32
		for i := range sp.N {
			switch sp.Bits[i] { //nolint:exhaustive // default: don't care is refused
			case trigger.High:
				pat = pat<<1 | 1
			case trigger.Low:
				pat <<= 1
			default:
				return refuse("SPI trigger", "the open engine matches exact bytes (no don't-care bits)")
			}
		}
		t.Node = "spi"
		pr.Engine, pr.A, pr.B = 2, sp.Clock.Source, sp.Data.Source
		pr.SliceA, pr.SliceB = slice(sp.Clock), slice(sp.Data)
		pr.TimingClocks = max(2, int(math.Round(sp.IdleS*CclkHz)))
		pr.CPHA = sp.LatchFalling
		pr.MSB = sp.MSBFirst
		pr.Pattern, pr.PatternLen = pat, sp.N/8
		return nil
	case trigger.I2C:
		if caps&CapI2C == 0 {
			return refuse("I2C trigger", "not in this image")
		}
		ic := sr.I2C
		if err := errorsJoin(line(ic.SCL, "I2C SCL"), line(ic.SDA, "I2C SDA")); err != nil {
			return err
		}
		if ic.Addr10 {
			return refuse("I2C trigger", "the open engine matches 7-bit addresses")
		}
		t.Node = "i2c"
		pr.Engine, pr.A, pr.B = 3, ic.SCL.Source, ic.SDA.Source
		pr.SliceA, pr.SliceB = slice(ic.SCL), slice(ic.SDA)
		pr.AnyAddress = ic.Address >= 0x80
		pr.Address = ic.Address & 0x7F
		pr.Direction = ic.RW
		for _, b := range ic.Data {
			if b >= 0 && b < 256 {
				pr.Pattern = pr.Pattern<<8 | uint32(b)
				pr.PatternLen++
			}
		}
		return nil
	}
	return refuse("serial trigger", "CAN and LIN are not in this image (spec 20 §7.7: planned)")
}

func errorsJoin(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
