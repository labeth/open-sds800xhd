package acq

import (
	"fmt"
	"time"

	"github.com/labeth/open-sds800xhd/app/internal/board"
	"github.com/labeth/open-sds800xhd/app/internal/frontend"
	"github.com/labeth/open-sds800xhd/app/internal/mapengine"
	"github.com/labeth/open-sds800xhd/app/internal/mmio"
	"github.com/labeth/open-sds800xhd/app/internal/platform"
	"github.com/labeth/open-sds800xhd/app/internal/timebase"
	"github.com/labeth/open-sds800xhd/app/internal/trigger"
)

// Engine owns the acquisition hardware.
type Engine struct {
	acq, mp *mmio.Plane
	id      platform.Identity
	nch     int
	minEnum int // smallest time/div enum of the bandwidth class (spec 04 §2)

	FE   *frontend.FrontEnd
	trig *trigger.Trigger
	Map  *mapengine.Engine
	dma  *DMA

	cur Settings
	h   timebase.Set

	calPending bool // an ADC mode change needs the internal calibration
	calJustRan bool
	delayD     int // start delay programmed last
	delayAt    time.Time
	armed      bool
	recency    []int // channels, most recently switched on or selected first
	rolling    bool  // the roll stream runs
	redrawn    bool  // the banks hold a stored-record set (Redraw): the next Commit rewrites the live set

	ver      [3]uint16 // the PL image's version words at start-up (ACQ 0x000–0x002)
	sentinel bool      // ACQ 0x1EF read back the 1 the start-up wrote: a reload shows as 0
	checkAt  time.Time // the last reload check

	log func(string, ...any)
}

// New returns the engine for a unit. dma may be nil: the engine then runs
// without readout.
func New(b *board.Board, bus frontend.SPI, id platform.Identity, dma *DMA, log func(string, ...any)) *Engine {
	e := &Engine{
		acq: b.ACQ, mp: b.MAP, id: id, nch: id.Channels,
		FE:   frontend.New(b.ACQ, bus, id, log),
		trig: trigger.New(b.ACQ, b.MAP, id.Channels),
		Map:  mapengine.New(b.MAP, b.ACQ, id.Channels),
		dma:  dma, log: log,
		minEnum: 3,
	}
	if id.BandwidthMHz >= 200 {
		e.minEnum = 2
	}
	return e
}

// Settings returns the applied settings.
func (e *Engine) Settings() Settings { return e.cur }

// Timebase returns the applied horizontal set.
func (e *Engine) Timebase() timebase.Set { return e.h }

func (e *Engine) horizontal(s *Settings) timebase.Set {
	return timebase.Compute(timebase.Config{
		Enum: max(s.Enum, e.minEnum), Depth: s.Depth, Mask: s.Mask(e.nch),
		Limited: e.id.BandwidthMHz < 200, Peak: s.Peak,
		Seq: s.Segments > 1, Roll: s.Roll,
	})
}

// trigChans is what the trigger's level conversion needs of every channel.
func (e *Engine) trigChans(s *Settings) [4]trigger.Chan {
	var ch [4]trigger.Chan
	for c := range e.nch {
		d := e.FE.Derived(c)
		ch[c] = trigger.Chan{VoltsDiv: s.Ch[c].VoltsDiv, PosPx: d.PosPx, Class: d.Class, Invert: s.Ch[c].Invert}
	}
	return ch
}

// programTrigger writes the trigger of s and the map engine's level preview.
func (e *Engine) programTrigger(s *Settings) {
	t := s.Trig
	t.Roll = s.Roll
	e.trig.Program(t, e.trigChans(s))
	e.Map.PreviewLevel(e.trig.PreviewCodes())
}

// Force is the AUTO sweep's one-shot forced trigger (spec 05 §7): the Force
// mode bits over the armed acquisition, then the selected mode again.
func (e *Engine) Force() {
	e.trig.ForceOnce()
	time.Sleep(100 * time.Millisecond)
	e.trig.Restore()
}

// Startup is the start-up order of spec 03 §2, from a PL that the vendor
// boot configured and the vendor application may have left in any state. It
// ends ready for the first arm (step 19), which the owner issues with Arm.
// TRLC-LINKS: REQ-HD-004
func (e *Engine) Startup(s Settings) {
	a := e.acq
	// A. Chip creation and init.
	platform.Mark("startup A")
	ver := [3]uint16{a.HW(0x000), a.HW(0x001), a.HW(0x002)}
	e.ver = ver
	e.log("PL version %04x-%04x-%04x", ver[2], ver[1], ver[0])
	a.Set(0x1FB, uint16(e.id.ADC)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	e.FE.OffsetDACInit()
	if a.HW(0x002)>>8 == 0xBD {
		e.log("board-test bitstream: no scope ID")
	}
	a.Field(0x019, 3, 3, 1)
	time.Sleep(10 * time.Millisecond)
	a.Set(0x1EF, 1)
	e.sentinel = a.HW(0x1EF)&1 == 1
	e.checkAt = time.Now()
	e.FE.VGAInit()
	e.FE.ADC.Init()
	a.Set(0x04F, 1, 0)
	e.FE.RelayEnable()
	if err := platform.GPIOOut(373, 1, true); err != nil {
		e.log("gpio 373: %v", err)
	}

	// B. Phase training.
	platform.Mark("startup B")
	e.FE.ADC.TrainPhase()

	// C. Register init and first configuration.
	platform.Mark("startup C")
	if !a.Poll(0x01F, 1<<12, true, 5000, time.Microsecond) || !a.Poll(0x01F, 1<<13, true, 5000, time.Microsecond) {
		e.log("xPLL not locked")
	}
	a.Set(0x003, 1, 0)
	a.Set(0x0F8, 0x100) // history pause and the history window boot values (spec 03 §6.3)
	a.Set(0x0F4, 1)
	a.Set(0x10F, 0)
	a.Set(0x0F5, 0)
	a.Set(0x0F6, 0)
	a.Set(0x0F7, 0)
	a.Set(0x0F8, 0x101)
	a.Set(0x05C, 0)
	a.Set(0x02E, 0)
	a.Set(0x03C, 1)
	a.Set(0x0EA, 1, 0)
	a.Set(0x128, 0)
	a.Set(0x09C, 1)
	a.Set(0x09D, 1)
	e.Map.Init(s.Display)
	e.stop() // 13.

	// 13a. Calibration load (spec 10 §7): the factory DAC file from p9, the
	// user self-calibration from p12; a missing or rejected file keeps the
	// defaults of its part.
	cs := e.FE.LoadCalibration(platform.FactoryDir, frontend.UserCalDir, e.nch)
	e.log("calibration: factory %v, self-cal %v %s", cs.Factory, cs.User, cs.Detail)

	// 14. Channels.
	mask := s.Mask(e.nch)
	h := e.horizontal(&s)
	e.cur, e.h = s, h
	a.Set(0x0B1, uint16(mask)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	e.FE.SetRoute(h.Route, s.Ch[:e.nch])
	for c := range e.nch {
		e.FE.SetupChannel(c, s.Ch[c], mask, h.R)
		if s.Ch[c].On {
			e.recency = append(e.recency, c)
		}
	}
	e.FE.FilterPass(s.Ch[:e.nch])
	for _, r := range frontend.GNDReg {
		a.Set(r, 0)
	}
	for _, r := range []int{0x0D5, 0x0CA, 0x024} {
		a.Set(r, 0)
	}
	a.Set(0x018, 2) // the sequence segment setting outside sequence mode
	e.writeInvert(&s, h)
	e.FE.ApplyGND(s.Ch[:e.nch], h.R)

	// 15. ADC mode.
	e.adcMode(mask, h)
	e.calPending = true

	// 16. Timebase and FIR selector.
	platform.Mark("startup timebase")
	e.timebasePass(&s, h)

	// 17. Trigger: the other types' defaults, then the edge trigger; zoom off.
	platform.Mark("startup trigger")
	e.trig.RestoreDefaults()
	e.programTrigger(&s)
	a.Set(0x089, 0)

	// 18. Soft reset.
	e.softReset()
	e.Map.Channels(e.onList(&s), e.recency)
	e.Map.PixelMapping()
	e.Map.ShowWaveforms(true)
	e.Map.PassFailHold(false)
	platform.Mark("startup done")
}

// reloadEvery is how often Reloaded reads the PL: two register reads.
const reloadEvery = 500 * time.Millisecond

// Reloaded reports a PL that was reconfigured under the running firmware
// since the start-up: its image identity (ACQ 0x002) changed, or ACQ 0x1EF,
// which the start-up set to 1 and nothing else writes, reads 0 again (a
// reconfigured PL is at its power-up values). Which ACQ registers read
// back what was written is UNVERIFIED: the 0x1EF check is used only when
// it read back the start-up's 1. It reads the PL at most every
// reloadEvery; a caller then runs Restart.
// TRLC-LINKS: REQ-HD-004
func (e *Engine) Reloaded() (bool, string) {
	if time.Since(e.checkAt) < reloadEvery {
		return false, ""
	}
	e.checkAt = time.Now()
	if v := e.acq.HW(0x002); v != e.ver[2] {
		return true, fmt.Sprintf("PL image identity changed: ACQ 0x002 %04x, %04x at start-up", v, e.ver[2])
	}
	if e.sentinel && e.acq.HW(0x1EF)&1 == 0 {
		return true, "PL registers back at power-up values (ACQ 0x1EF reads 0): the PL was reconfigured"
	}
	return false, ""
}

// Restart runs the start-up again with the settings last committed, after
// a PL reload: every register the firmware set is lost (spec 01 §12.2 step
// 8: never rely on state the PL holds).
// TRLC-LINKS: REQ-HD-004
func (e *Engine) Restart() {
	s := e.cur
	e.recency, e.armed, e.rolling, e.redrawn = nil, false, false, false
	e.Startup(s)
}

func (e *Engine) onList(s *Settings) []bool {
	on := make([]bool, e.nch)
	for c := range on {
		on[c] = s.Ch[c].On
	}
	return on
}

// adcMode is the ADC mode refresh of spec 06 §9.2 (spec 03 §2 step 15,
// §3.3 step 2): mode word, lock check, the 0x07D pulse.
func (e *Engine) adcMode(mask int, h timebase.Set) {
	if e.FE.ADC.SetMode(mask, h.R) {
		e.calPending = true
	}
	if v := e.acq.HW(0x01F); v&(1<<8) == 0 || v&(1<<12) == 0 {
		e.log("ADC not working: 0x01F = %#04x", v)
	}
	e.acq.Set(0x07D, 0x102, 0x002)
}

// writeInvert writes ACQ 0x00F (spec 02 §7).
func (e *Engine) writeInvert(s *Settings, h timebase.Set) {
	var v uint16
	for c := range e.nch {
		if !s.Ch[c].On || !s.Ch[c].Invert {
			continue
		}
		if h.Route == 0 && h.M == 0 {
			if c < 2 {
				v |= 3
			} else {
				v |= 0xC
			}
		} else {
			v |= 1 << c
		}
	}
	e.acq.Set(0x00F, v)
}

// ScopeID reads the 64-bit device DNA of the PL (spec 02 §3); 0 if the PL
// does not report it ready.
func (e *Engine) ScopeID() uint64 {
	a := e.acq
	a.Set(0x180, 0, 1)
	if !a.Poll(0x181, 1, true, 1000, time.Millisecond) {
		return 0
	}
	var id uint64
	for i := 3; i >= 0; i-- {
		id = id<<16 | uint64(a.HW(0x182+i))
	}
	return id
}

// MinEnum is the smallest time/div enum of the unit's bandwidth class.
func (e *Engine) MinEnum() int { return e.minEnum }

// Channels is the model's channel count.
func (e *Engine) Channels() int { return e.nch }
