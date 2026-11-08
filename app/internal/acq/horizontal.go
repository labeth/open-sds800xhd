package acq

import (
	"math"
	"math/bits"
	"time"

	"github.com/labeth/open-sds800xhd/app/internal/mmio"
	"github.com/labeth/open-sds800xhd/app/internal/timebase"
	"github.com/labeth/open-sds800xhd/app/internal/trigger"
)

// bank runs body inside the window of configuration bank n (spec 02 §4.3):
// open, select, the registers, commit pulse, close.
func (e *Engine) bank(n int, body func(t *mmio.Tx)) {
	e.acq.Locked(func(t *mmio.Tx) {
		t.Set(0x110, t.Shadow(0x110)|1)
		t.Set(0x110, uint16(n)<<4|1) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		body(t)
		t.Set(0x110, t.Shadow(0x110)|0x100)
		time.Sleep(10 * time.Microsecond)
		t.Set(0x110, t.Shadow(0x110)&^0x100)
		t.Set(0x110, t.Shadow(0x110)&^1)
	})
}

func (e *Engine) posInput(s *Settings, c int) timebase.PosInput {
	skew := -s.Ch[c].SkewS * 1e6
	if s.Peak && e.h.R <= 100 {
		skew -= 0.012
	}
	if s.Trig.Type == trigger.Dropout {
		skew += 0.002 // the dropout trigger's skew (spec 04 §9)
	}
	cp := s.Trig.Coupling
	return timebase.PosInput{
		DelayUs:  s.DelayS * 1e6,
		SkewUs:   skew,
		CoupHFLF: cp == trigger.CoupHFReject || cp == trigger.CoupLFReject,
	}
}

// timebasePass is spec 03 §3.3 steps 3–5: the ACQ banks with the §8.2
// set, the position, resize and search sets; the map timebase sets; the FIR
// selector.
func (e *Engine) timebasePass(s *Settings, h timebase.Set) {
	e.h = h
	if s.Roll {
		mask := s.Mask(e.nch)
		for n := range e.nch {
			e.bank(n, func(t *mmio.Tx) { writeRoll(t, h, mask) })
		}
		e.Map.RollSets(h, bits.OnesCount(uint(mask)))
		e.Map.DPOComputed(h, int(s.Trig.Slope))
		e.acq.Set(0x1FF, timebase.FIRSelector(e.id.BandwidthMHz, h.R))
		return
	}
	for n := range e.nch {
		in := e.posInput(s, n)
		e.bank(n, func(t *mmio.Tx) {
			writeTimebase(t, h)
			writePosition(t, h, in)
			writeResize(t)
			writeSearch(t, h)
			if e.redrawn {
				// The live set has no stored search; the stored pass set it
				// (spec 03 §3.7 step 7: rewrite what the stored pass changed).
				t.Set(0x118, 0)
			}
			if s.View.xyOnly() {
				writeXY(t, h, 1)
			}
		})
	}
	if s.View.XY && s.View.YTXY {
		for n := range e.nch {
			e.bank(4+n, func(t *mmio.Tx) { writeXY(t, h, 0x10) })
		}
	}
	e.Map.TimebaseSets(h)
	e.Map.DPOComputed(h, int(s.Trig.Slope))
	e.acq.Set(0x1FF, timebase.FIRSelector(e.id.BandwidthMHz, h.R))
}

// autoOvertime is the auto-trigger overtime the timebase setup programs
// (spec 05 §7: TR_TIMEOUT = 2·v).
const autoOvertime = 12_500_000

func b2u(b bool) uint16 {
	if b {
		return 1
	}
	return 0
}

// writeTimebase is the register set of spec 04 §8.2, in its order.
func writeTimebase(t *mmio.Tx, h timebase.Set) {
	// 1. Speed, roll off, down-sampling (spec 04 §5).
	t.Field(0x022, 14, 12, h.SpeedCode())
	t.Field(0x010, 15, 15, 1)
	t.Set32(0x05D, 0x05E, h.DownRatio(), 0xFFF)
	v := uint16(h.Decim) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	if h.Peak && h.Decim != 0 {
		v |= 1 << 8
	}
	t.Set(0x004, v)
	t.Field(0x022, 9, 8, uint16(h.Switch)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set32(0x04B, 0x04C, h.DownDivisor(), 0xFFF)
	t.Set(0x04E, b2u(h.Decim != 0 || h.Switch != 0))
	// 2. Interpolation.
	t.Set(0x083, uint16(h.I)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	writeInterp(t, h.I)
	// 3. Sections.
	t.Set(0x005, 5)
	t.Set(0x096, 1)
	var N int
	if h.S > 6250 {
		N = timebase.Sections(max(1, int(float32(h.I)*h.S/1000)))
		t.Set(0x098, uint16(int(h.S)/N)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	} else {
		N = int(math.Floor(float64(h.S) + 0.5))
		t.Set(0x098, 1)
	}
	// 4. Lengths.
	L0, L1 := (128+N)/4, h.I*N/4
	t.Set(0x10E, uint16(h.Delta/4+L0)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x092, uint16(L0))           //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x094, uint16(L1))           //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	// 5., 6. Depth and segment count.
	t.Set32(0x01A, 0x01B, uint32(h.D), 0xFFFF) //nolint:gosec // G115: the register pair takes the value's low 32 bits
	sv := h.SegmentValue()
	t.Set(0x027, uint16(sv))               //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Field(0x05B, 7, 7, uint16(sv>>16)&1) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	// 7.–10.
	ntr, tr := timebase.Timeouts(h.Enum, autoOvertime)
	t.Set32(0x00C, 0x00D, ntr, 0xFFFF)
	t.Set32(0x02A, 0x02B, tr, 0xFFFF)
	t.Field(0x039, 3, 0, timebase.TDCInsert(h.M, h.Enum))
	t.Set(0x1C7, uint16(h.Enum)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	if h.M == 0 && h.S == 1000 {
		t.Set(0x1EE, 0x1A8)
	} else {
		t.Set(0x1EE, 0x400)
	}
}

// writePosition is spec 04 §9.
func writePosition(t *mmio.Tx, h timebase.Set, in timebase.PosInput) {
	o := h.Position(in)
	t.Set(0x005, 5)
	t.Set32(0x09A, 0x09B, uint32(o.Start), 0xFFFF) //nolint:gosec // G115: the register pair takes the value's low 32 bits
	t.Set(0x08C, uint16(2*o.Start))                //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x09E, uint16(o.R))                      //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x08E, uint16(o.A1))                     //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x08A, uint16(o.A1+1))                   //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set32(0x007, 0x025, uint32(o.Delay), 0xFFFF) //nolint:gosec // G115: the register pair takes the value's low 32 bits
	t.Set32(0x006, 0x028, uint32(o.Pre), 0xFFFF)   //nolint:gosec // G115: the register pair takes the value's low 32 bits
	t.Set(0x0A7, uint16(o.A7))                     //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x0A8, uint16(o.A8))                     //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x0A9, uint16(o.A9))                     //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
}

// writeResize is the live resize set (spec 02 §4.2): full vertical range.
func writeResize(t *mmio.Tx) {
	t.Set32(0x1DF, 0x1E0, 0, 0xFFFF)
	t.Set32(0x1E1, 0x1E2, 16384, 0xFFFF)
	t.Set(0x1E3, 0x080)
	t.Set(0x1E4, 0xF7F)
}

// writeInterp is the interpolation set for i (spec 04 §8.2 step 2, after
// 0x083).
func writeInterp(t *mmio.Tx, i int) {
	if i < 2 {
		t.Set(0x0B6, 0)
		t.Set(0x086, 0)
		t.Set(0x085, 0)
		t.Set(0x090, 0)
		t.Set(0x0CE, 0)
		t.Set(0x0A0, 1)
		return
	}
	st, en := timebase.InterpWindow(i)
	t.Set(0x0B6, 1)
	t.Set(0x086, uint16(i))   //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x085, uint16(4*i)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x090, uint16(st))  //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x0CE, uint16(en))  //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x0A0, 4)
}

// writeSearch is the wave-search set of spec 04 §8.3.
func writeSearch(t *mmio.Tx, h timebase.Set) {
	t.Set(0x03D, 1)
	writeSearchSet(t, h)
}

// writeSearchSet is the wave-search set without its enable 0x03D.
func writeSearchSet(t *mmio.Tx, h timebase.Set) {
	I := h.I
	st, en := timebase.InterpWindow(I)
	t.Set(0x082, uint16(I)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x0BE, []uint16{0x1C, 0x0C, 0x04}[h.M])
	t.Set(0x0B8, uint16(2*I)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	if I > 1 {
		t.Set(0x03F, uint16(8*I-1)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		t.Set(0x0C0, 8)
		t.Set(0x0BA, uint16(6*I-2)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	} else {
		t.Set(0x03F, 6)
		t.Set(0x0C0, 8)
		t.Set(0x0BA, 6)
	}
	t.Set(0x071, uint16(I))   //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x070, uint16(4*I)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x074, uint16(st))  //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.Set(0x076, uint16(en))  //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
}
