// Package mapengine drives the PL map engine through the MAP plane (spec 07
// §7): the PL renders the waveform layer from the record memory by itself,
// and the PS configures what and how it draws. It also owns the key LEDs,
// which live in the same plane (spec 08 §6).
package mapengine

import (
	"time"

	"github.com/labeth/open-sds800xhd/app/internal/mmio"
	"github.com/labeth/open-sds800xhd/app/internal/timebase"
)

// Display is the appearance part of the instrument state (spec 09 §2.1).
type Display struct {
	Vectors      bool
	ColorGrade   bool
	IntensityPct int
	GridPct      int
	MenuAlphaPct int
	BacklightPct int
	Persistence  Persistence
}

// Persistence is the waveform persistence of spec 07 §7.2.
type Persistence int

// The persistence types this model offers: off, infinite, and a decay of
// 1, 5, 10 or 30 s.
const (
	PersistOff Persistence = iota
	PersistInfinite
	Persist1s
	Persist5s
	Persist10s
	Persist30s
)

// PersistenceOf maps a persistence time (0 off, < 0 infinite, seconds) to
// a type; ok is false for a time the PL does not offer.
func PersistenceOf(s float64) (Persistence, bool) {
	switch {
	case s == 0:
		return PersistOff, true
	case s < 0:
		return PersistInfinite, true
	case s == 1:
		return Persist1s, true
	case s == 5:
		return Persist5s, true
	case s == 10:
		return Persist10s, true
	case s == 30:
		return Persist30s, true
	}
	return 0, false
}

// Seconds is the persistence time of p: 0 off, -1 infinite.
func (p Persistence) Seconds() float64 {
	return [...]float64{0, -1, 1, 5, 10, 30}[p]
}

// persistenceRegs are the 0x140 bits 1:0, 0x144 bits 7:0 and 0x080 values
// of a type (spec 07 §7.2).
func (p Persistence) regs() (mode, secs, ctl uint16) {
	switch p {
	case PersistInfinite:
		return 3, 3, 0
	case PersistOff:
		return 0, 0, 3
	case Persist1s, Persist5s, Persist10s, Persist30s:
		// timed: the seconds below
	}
	return 1, uint16(p.Seconds()), 0
}

// DefaultDisplay is the default appearance (spec 09 §2.1).
func DefaultDisplay() Display {
	return Display{Vectors: true, IntensityPct: 50, GridPct: 20, MenuAlphaPct: 60, BacklightPct: 50}
}

// Engine is the map engine of a model with Channels channels.
type Engine struct {
	mp, acq  *mmio.Plane
	channels int
	zoom     bool // the zoom window is on: the sets' code window changes
	xy       bool // XY-only display: the sets are the XY variant
	ytxy     bool // YT+XY display: XY variant in sets 4 + n
}

// New returns the map engine.
func New(mp, acq *mmio.Plane, channels int) *Engine {
	return &Engine{mp: mp, acq: acq, channels: channels}
}

func b2u(b bool) uint16 {
	if b {
		return 1
	}
	return 0
}

// SoftReset writes the map soft reset; a release waits for the engine
// (spec 07 §7.5).
func (m *Engine) SoftReset(v uint16) {
	m.mp.Set(0x103, v)
	if v == 0 {
		m.mp.Poll(0x188, 1<<8, false, 50002, time.Microsecond)
		m.mp.Poll(0x188, 0xF, false, 50002, time.Microsecond)
	}
}

// ClearBlock is one map restart block (spec 07 §7.2). Bits 0 and 4 of 0x170
// keep their shadow; the block pulses bit 8.
func (m *Engine) ClearBlock() {
	m.mp.Set(0x0AF, 0, 1)
	m.SoftReset(1)
	m.SoftReset(0)
	keep := m.mp.Shadow(0x170) & 0x0011
	m.mp.Set(0x170, keep|0x100, keep)
	m.mp.Set(0x15A, 0, 1)
}

// WaitReady waits for the map engine to be idle (ACQ 0x0FD bit 7 = 0).
func (m *Engine) WaitReady() bool {
	return m.acq.Poll(0x0FD, 1<<7, false, 5002, time.Microsecond)
}

// Appearance writes spec 07 §7.1 from the display settings.
func (m *Engine) Appearance(d Display) {
	m.mp.Field(0x0E7, 0, 0, b2u(d.ColorGrade))
	m.mp.Field(0x0FE, 7, 0, uint16(d.IntensityPct+16)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	g := uint32(d.GridPct * 255 / 100)                 //nolint:gosec // G115: GridPct is a percentage, so the grey level is 0 … 255
	grey := g<<16 | g<<8 | g
	m.mp.Set(0x0D0, uint16(grey)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	m.mp.Set(0x0D1, uint16(grey>>16)&0x3FFF)
	m.mp.Set(0x14D, uint16(18*d.BacklightPct+199))          //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	m.mp.Field(0x0FA, 7, 0, uint16(d.MenuAlphaPct*255/100)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
}

// DisplayType writes vectors (1) or dots (0) to 0x125 and clears the
// picture (spec 07 §7.1, §7.2).
func (m *Engine) DisplayType(vectors bool) {
	m.mp.Set(0x125, b2u(vectors))
	m.ClearBlock()
}

// set190 skips the last 0x190 word so a transaction never repeats a word
// (the PL sees the exact word sequence of spec 07 §7.3).
func (m *Engine) set190(w uint16) {
	if m.mp.Shadow(0x190) != w {
		m.mp.Set(0x190, w)
	}
}

// begin and end bracket the parameters of set n (spec 07 §7.3, normal set).
func (m *Engine) begin(n int) {
	w := m.mp.Shadow(0x190)&0x00FE | 1
	m.set190(w)
	w &= 0x00FF
	m.set190(w)
	m.set190(w&0x000F | uint16(n)<<4) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
}

func (m *Engine) end() {
	w := m.mp.Shadow(0x190)&0x00FF | 0x1000
	m.set190(w)
	m.set190(w & 0x00FE)
}

// PreviewLevel is the map-side copy of the trigger level (MAP 0x1E7–0x1E9).
func (m *Engine) PreviewLevel(code, h uint16) {
	m.mp.Set(0x1E7, code-h)
	m.mp.Set(0x1E9, h)
	m.mp.Set(0x1E8, code+h)
}

// Init is the start-up map sequence of spec 07 §7.10, groups 1–12 and
// 14, 17, 18, 20: the groups that need no channel or UI state. The trigger
// preview (group 6) is written with the trigger level, the channels (group
// 15) by Channels, the LEDs (group 13) by LEDs.
// TRLC-LINKS: REQ-HD-007
func (m *Engine) Init(d Display) {
	mp := m.mp
	mp.Set(0x189, 1) // 1. DPO
	mp.Set(0x1A9, 1)
	mp.Set(0x13A, 0)
	mp.Set(0x13E, 0x10)
	m.PassFailHold(true) // 2.
	for n := range 4 {   // 3. channels off
		mp.Set(0x106+n, 0)
	}
	m.ClearBlock()              // 4. restart block
	for n := range m.channels { // 5. boot geometry
		m.begin(n)
		mp.Set(0x19D, 10)
		mp.Set(0x060, 250)
		mp.Set(0x19F, 1)
		mp.Set(0x19B, 500)
		mp.Set(0x19A, 250)
		mp.Set(0x1A1, 0)
		mp.Set(0x1A3, 1)
		mp.Set(0x1A7, 2)
		mp.Set(0x1AB, 1)
		mp.Set(0x1AE, 0)
		mp.Set(0x1AF, 1000)
		mp.Set(0x153, 1000)
		mp.Set(0x110, 0)
		mp.Set(0x1B5, 0)
		mp.Set(0x1B6, 0)
		mp.Set(0x1B7, 0)
		m.end()
	}
	mp.Set(0x125, b2u(d.Vectors))
	mp.Set(0x111, 0)
	mp.Set(0x1BD, 0)
	mp.Set(0x1BE, 0x1DF)
	mode, secs, ctl := d.Persistence.regs() // 7. persistence
	mp.Set(0x080, ctl)
	mp.Field(0x140, 1, 0, mode)
	if d.Persistence != PersistOff {
		mp.Field(0x144, 7, 0, secs)
	}
	mp.Set(0x184, 0)
	mp.Set(0x16D, 75) // 8. knob period
	mp.Set(0x0D4, 1)  // 9. magic grid / trace
	mp.Set(0x0D5, 0)
	mp.Set(0x0D2, 2)
	mp.Set(0x0D3, 0)
	mp.Set(0x0FF, 1) // 10. waveform layer hidden until the first frame
	mp.Set(0x179, 0) // 11.
	mp.Set(0x0DB, 1)
	mp.Set(0x1FA, 0xA5)
	mp.Set(0x1FB, 0xB0)
	mp.Set(0x115, 1)
	m.ZoomSplit(false) // 12.
	mp.Set(0x192, 0)   // 14. XY off, sequence off
	mp.Set(0x1BA, 0)
	mp.Set(0x1BF, 0)
	mp.Set(0x1BB, 2)
	mp.Set(0x1BC, 0)
	m.Appearance(d)  // 17.
	mp.Set(0x1E6, 0) // 18. waveform search off
	mp.Set(0x1EB, 0)
	mp.Set(0x1EA, 0)
	mp.Set(0x1FD, 0x4B) // 20. search positions
	mp.Set(0x1FF, 0xD2)
}

// PixelMapping is group 19 of the start-up map sequence: the horizontal
// pixel mapping of the full-screen layout (spec 07 §7.4, §7.10).
func (m *Engine) PixelMapping() {
	mp := m.mp
	mp.Set(0x0C3, 1000)
	mp.Set(0x0C4, 1000)
	mp.Set(0x0C5, 0xA0)
	mp.Set(0x0C6, 0x9E)
	mp.Set(0x0C7, 0x3FF)
	mp.Set(0x08F, 1, 0)
}

// ZoomSplit writes the main/zoom split (spec 07 §7.4).
func (m *Engine) ZoomSplit(on bool) {
	const rows, gap = 480, 20
	mp := m.mp
	if on {
		mp.Set(0x10A, 1)
		mp.Set(0x090, 1)
		mp.Set(0x091, rows-gap)
		mp.Set(0x092, (rows-gap)/4)
		mp.Set(0x093, gap)
	} else {
		mp.Set(0x10A, 0)
		mp.Set(0x090, 1)
		mp.Set(0x091, rows)
		mp.Set(0x092, rows)
		mp.Set(0x093, 0)
	}
	mp.Set(0x08F, 1, 0)
}

// TimebaseSets writes the main timebase set of every channel of the model
// (spec 07 §7.8), or their XY variant while the XY-only display is on
// (spec 07 §7.7: twice the samples, no interpolation, the XY length). With
// the YT+XY display the main sets stay normal and the XY variant goes into
// sets 4 … 4 + C − 1 (spec 07 §7.7; static only).
func (m *Engine) TimebaseSets(h timebase.Set) {
	for n := range m.channels {
		m.timebaseSet(n, h, m.xy)
	}
	if m.ytxy {
		for n := range m.channels {
			m.timebaseSet(4+n, h, true)
		}
	}
}

// timebaseSet writes set n for h, normal or the XY variant.
func (m *Engine) timebaseSet(n int, h timebase.Set, xy bool) {
	S, I := h.S, h.I
	if xy {
		S, I = 2*h.S, 1
	}
	ms := timebase.MapSetFor(S, I)
	mp := m.mp
	m.begin(n)
	mp.Set(0x060, 250)
	mp.Set(0x19B, uint16(ms.P/10)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	if ms.Seg == 1 {
		mp.Set(0x19A, 2)
	} else {
		mp.Set(0x19A, uint16(ms.P/20)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	}
	m.pointsPerPixel(ms)
	mp.Set(0x120, 0)
	mp.Set(0x1A7, uint16(ms.Seg)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	m.lengths(ms)
	if xy {
		mp.Set(0x199, uint16(ms.P/2)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	}
	// The optimised-refresh form of 0x1B5–0x1B7 is not used (its
	// enabling condition is not known, spec 07 §7.8).
	mp.Set(0x1B5, 0)
	mp.Set(0x1B6, 0)
	mp.Set(0x1B7, 0)
	mp.Set(0x153, uint16(ms.P/5)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	mp.Set(0x110, 0)
	m.codeWindow()
	m.end()
}

// pointsPerPixel writes 0x19D, 0x19F, 0x1A1 and 0x1A3 of a set.
func (m *Engine) pointsPerPixel(ms timebase.MapSet) {
	mp := m.mp
	if ms.PPP%10 == 0 {
		mp.Set(0x19D, 10)
	} else {
		mp.Set(0x19D, uint16(ms.PPP)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	}
	mp.Field(0x19F, 3, 0, uint16(timebase.ReadTimes(ms.PPP))) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	mp.Set(0x1A1, uint16(timebase.MaskBase(ms.PPP)))          //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	if ms.PPP < 11 || ms.PPP == 25 || ms.PPP == 125 {
		mp.Set(0x1A3, 1)
	} else {
		mp.Set(0x1A3, uint16(ms.PPP/10)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	}
}

// lengths writes 0x1AB (lx count) and the x label window 0x1AE/0x1AF.
func (m *Engine) lengths(ms timebase.MapSet) {
	lx := 1
	if ms.PPP >= ms.P && ms.P > 0 {
		lx = ms.PPP / ms.P
	}
	m.mp.Field(0x1AB, 11, 0, uint16(lx))
	m.mp.Set(0x1AE, 0)
	m.mp.Set(0x1AF, 1000)
}

// codeWindow writes the max/min code window 0x1BD/0x1BE of a run-mode set.
func (m *Engine) codeWindow() {
	if m.zoom {
		m.mp.Set(0x1BD, 0x168)
	} else {
		m.mp.Set(0x1BD, 0)
	}
	m.mp.Set(0x1BE, 0x1DF)
}

// ZoomMapSet is the zoom window's map set for main screen samples s and
// zoom screen samples sz at interpolation iz (spec 07 §7.8): the main set's
// rule with the zoom values, except that a multi-section set counts its
// sections over the shorter of the two windows.
func ZoomMapSet(s, sz float32, iz int) timebase.MapSet {
	ms := timebase.MapSetFor(sz, iz)
	if sz > 6250 {
		n := timebase.Sections(ms.PPP)
		ms.Seg = int(min(s, sz)) / n
	}
	return ms
}

// ZoomSets writes the zoom-window set 4 + n of every channel and the zoom
// DPO triple (spec 07 §7.8, §7.9), after the main sets. s is the main
// window's screen samples, sz and iz the zoom window's samples and
// interpolation. Nothing is written for a window of fewer than 4 samples.
func (m *Engine) ZoomSets(s, sz float32, iz, slope int) {
	if sz < 4 {
		return
	}
	ms := ZoomMapSet(s, sz, iz)
	mp := m.mp
	for n := range m.channels {
		m.begin(4 + n)
		mp.Set(0x061, 250)
		mp.Set(0x19B, uint16(ms.P/10)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		if ms.Seg == 1 {
			mp.Set(0x19A, 2)
		} else {
			mp.Set(0x19A, uint16(ms.P/20)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		}
		m.pointsPerPixel(ms)
		mp.Set(0x1A7, uint16(ms.Seg)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		m.lengths(ms)
		mp.Set(0x1B5, 0)
		mp.Set(0x1B6, 0)
		mp.Set(0x1B7, 0)
		mp.Set(0x154, uint16(ms.P/5)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		mp.Set(0x110, 0)
		mp.Set(0x1BD, 0x168)
		mp.Set(0x1BE, 0x1DF)
		m.end()
	}
	if x, ud, a, ok := timebase.DPO(1, ms.PPP, iz&0xFF, slope, false, false); ok {
		mp.Set(0x1AA, uint16(x))  //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		mp.Set(0x14C, uint16(ud)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		mp.Set(0x13F, uint16(a))  //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	}
}

// SetZoom switches the zoom window: the main/zoom split (spec 07 §7.4) and
// the code window of the sets written next.
func (m *Engine) SetZoom(on bool) {
	m.zoom = on
	m.ZoomSplit(on)
}

// SetXY switches the XY display (spec 07 §7.7): the sets written next are
// the XY variant (XY-only), or normal main sets with XY sets 4 + n (YT+XY);
// MAP 0x192 is written by XYMode once the ACQ side is.
func (m *Engine) SetXY(xyOnly, ytxy bool) { m.xy, m.ytxy = xyOnly, ytxy }

// XYMode writes the XY mode value to MAP 0x192 (spec 07 §7.7).
func (m *Engine) XYMode(x uint16) { m.mp.Set(0x192, x) }

// XYMapping writes the horizontal pixel mapping of the XY display, or
// restores the full-screen YT mapping (spec 07 §7.7).
func (m *Engine) XYMapping(on bool) {
	if on {
		m.mp.Set(0x0C4, 0x1E0)
		m.mp.Set(0x0C3, 0x1E0)
		m.mp.Set(0x0C5, 0x138)
		return
	}
	m.mp.Set(0x0C4, 1000)
	m.mp.Set(0x0C3, 1000)
	m.mp.Set(0x0C5, 0xA0)
}

// SetPersistence is the "set persistence type" sequence of spec 07 §7.2
// with the acquisition stopped: wait for the map engine, then 0x080, 0x140
// bits 1:0 and (not for Off) 0x144 bits 7:0; switching Off clears.
func (m *Engine) SetPersistence(p Persistence) {
	m.WaitReady()
	mode, secs, ctl := p.regs()
	m.mp.Set(0x080, ctl)
	m.mp.Field(0x140, 1, 0, mode)
	if p == PersistOff {
		m.ClearPersistence()
		return
	}
	m.mp.Field(0x144, 7, 0, secs)
}

// ClearPersistence is the persistence clear of spec 07 §7.2.
func (m *Engine) ClearPersistence() {
	m.mp.Set(0x080, 3)
	v := m.mp.Shadow(0x140)
	m.mp.Set(0x140, 2, v)
}

// Backlight sets the LCD brightness, b in percent (spec 07 §6:
// 0x14D = 18·b + 199), and the panel enable 0x016.
func (m *Engine) Backlight(b int, on bool) {
	if on {
		m.mp.Set(0x14D, uint16(18*max(0, min(100, b))+199))
	}
	m.mp.Set(0x016, b2u(on))
}

// DPOComputed writes the computed DPO triple for h (spec 07 §7.9).
func (m *Engine) DPOComputed(h timebase.Set, slope int) {
	ms := timebase.MapSetFor(h.S, h.I)
	x, ud, a, ok := timebase.DPO(1, ms.PPP, h.I&0xFF, slope, false, false)
	m.mp.Set(0x189, 1)
	if !ok {
		return
	}
	m.mp.Set(0x1A9, uint16(x))  //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	m.mp.Set(0x13A, uint16(ud)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	m.mp.Set(0x13E, uint16(a))  //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
}

// DPORecall writes the run-start DPO triple (spec 07 §7.9).
func (m *Engine) DPORecall(enumFromMin int, multi bool) {
	v := timebase.DPORecall(enumFromMin, multi)
	m.mp.Set(0x1A9, uint16(v[0])) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	m.mp.Set(0x13A, uint16(v[1])) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	m.mp.Set(0x13E, uint16(v[2])) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
}

// Channels writes the channel enables, the visible mask and the priority
// ranks (spec 07 §7.1). recency lists channels most recently selected first.
func (m *Engine) Channels(on []bool, recency []int) {
	for n := range m.channels {
		m.mp.Set(0x106+n, b2u(on[n]))
	}
	m.mp.Set(0x105, 0xF)
	m.mp.Field(0x130, 3, 0, 0)
	ranks := [4]int{-1, -1, -1, -1}
	rank := 0
	for _, c := range recency {
		if c < m.channels && on[c] && ranks[c] < 0 {
			ranks[c] = rank
			rank++
		}
	}
	for c := m.channels - 1; c >= 0; c-- {
		if ranks[c] < 0 {
			ranks[c] = rank
			rank++
		}
	}
	for c := range m.channels {
		m.mp.Field(0x131+c, 3, 0, uint16(ranks[c])) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	}
}

// PassFailHold sets or releases the pass/fail output and mask hold (MAP
// 0x174, 0x172) that the start-up and a stored-record redraw hold while they
// reprogram the engine (spec 07 §7.10 group 2, spec 03 §3.7 steps 2 and 8).
func (m *Engine) PassFailHold(on bool) {
	if on {
		m.mp.Set(0x174, 0x20)
		m.mp.Set(0x172, 1)
		return
	}
	m.mp.Set(0x172, 0)
	m.mp.Set(0x174, 0)
}

// ShowWaveforms shows or hides the waveform layer (spec 07 §3.2).
func (m *Engine) ShowWaveforms(on bool) { m.mp.Set(0x0FF, b2u(!on)) }

// LEDs writes the 32-bit key-LED word (spec 08 §6).
func (m *Engine) LEDs(w uint32) {
	mp := m.mp
	mp.Set(0x16C, 0)
	mp.Set(0x16A, uint16(w)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	mp.Set(0x16C, 1, 0)
	mp.Set(0x16F, uint16(w>>16))
	mp.Set(0x16C, 1)
}
