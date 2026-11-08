// Package scopeboard is the SDS800X HD's implementation of the board
// interface (open-dso-core's hal, HAL v1):
// the hardware packages of this repository behind the interfaces the core
// engine drives.
//
// Setters stage; Acquirer.Arm commits the staged front end, plan and trigger
// in the order of spec 03 §3.3 and arms.
package scopeboard

import (
	"fmt"
	"image"
	"strconv"
	"time"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-dso-core/sample"
	"github.com/labeth/open-sds800xhd/app/internal/acq"
	"github.com/labeth/open-sds800xhd/app/internal/board"
	"github.com/labeth/open-sds800xhd/app/internal/counter"
	"github.com/labeth/open-sds800xhd/app/internal/frontend"
	"github.com/labeth/open-sds800xhd/app/internal/openpl"
	"github.com/labeth/open-sds800xhd/app/internal/platform"
	"github.com/labeth/open-sds800xhd/app/internal/timebase"
)

// Board is the SDS800X HD.
type Board struct {
	log      *platform.Log
	id       platform.Identity
	vendorFW string // the vendor system's version (boot.rootfs.kernel), read at bring-up

	regs    *board.Board
	dma     *acq.DMA
	eng     *acq.Engine
	staged  acq.Settings // what the next Arm commits
	state   hal.AcqState
	rerun   bool      // the next Arm follows a stop or a change: start delay, pending calibration
	forced  bool      // a forced trigger was issued since the last arm
	armedAt time.Time // when the last Arm armed the PL
	seq     uint64
	epoch   uint32     // bumped by every bring-up: record IDs of an earlier run never match
	stored  bool       // a halted record is in the record memory and may be read
	status  hal.Status // of the stored record
	stream  *streamer  // the roll stream while it runs
	errs    struct{ readouts, arms int }
	scope   uint64 // PL device DNA
	ui      ui     // UI layer, capture, front panel (display.go, input.go)
	ctr     *counter.Counter
	open    *openBackend // stage 2: the open PL (openpl.go); nil on the vendor image
}

// New returns the board before bring-up. log is the app's log.
func New(log *platform.Log) *Board { return &Board{log: log, staged: acq.DefaultSettings()} }

var _ hal.Board = (*Board)(nil)

// Profile is this unit's capabilities (valid after BringUp).
func (b *Board) Profile() hal.Profile {
	p := b.profile1()
	if b.open != nil {
		return b.openProfile(p)
	}
	return p
}

func (b *Board) profile1() hal.Profile {
	minEnum := 3
	if b.id.BandwidthMHz >= 200 {
		minEnum = 2
	}
	var tdivs []float64
	for e := minEnum; e <= timebase.MaxEnum; e++ {
		tdivs = append(tdivs, timebase.TimeDiv(e))
	}
	return hal.Profile{
		Identity: b.identity(),
		Channels: b.id.Channels,
		Format:   format12,
		Rates: []hal.Rate{
			{SamplesPerS: 2e9, MaxChannels: 1}, {SamplesPerS: 1e9, MaxChannels: 2}, {SamplesPerS: 500e6, MaxChannels: 4},
		},
		MaxDepth:  100_000_000,
		Bandwidth: float64(b.id.BandwidthMHz) * 1e6,
		Content:   sample.ContentSetOf(sample.RawFullRate, sample.RawDecimated, sample.PeakPairs),
		RawAccess: hal.RawAccess{MaxDepth: 2 * timebase.PieceSize, PerWindow: true},
		Triggers:  trigCaps,
		Display: hal.DisplaySpec{
			W: 1024, H: 600, Format: hal.ARGB8888, Touch: true, HWRenderer: true,
			DivsH: 10, DivsV: 8, Area: hal.Rect{X: waveArea.Min.X, Y: waveArea.Min.Y, W: waveArea.Dx(), H: waveArea.Dy()},
		},
		Vertical:   hal.VerticalSpec{Probes: frontend.Probes(), BWLimits: frontend.BWLimits()},
		Inputs:     inputSpec(),
		Supervisor: hal.SupervisorSpec{Restart: "software-health"},
		Options:    hal.OptCounter,
		Absent:     absentParts,
		Budget:     hal.Budget{RAMBytes: 504 << 20, Cores: 2},
		Stage:      1,
		Timebase:   hal.TimebaseSpec{TdivS: tdivs, FullRateTdivS: timebase.TimeDiv(minEnum)},
	}
}

// absentParts are the reasons for the optional parts this board lacks.
var absentParts = map[string]string{
	"images":      "this firmware runs the vendor bitstream: there is no image to switch",
	"stacker":     "the vendor bitstream has no hardware stacker",
	"logic-probe": "the SLA1016 logic probe is not driven by this firmware",
	"generator":   "the external signal generator is not driven by this firmware",
}

// format12 is the record word: 12-bit codes left-aligned in 16 bits,
// screen limits 0x080 … 0xF7F (spec 03 §5).
var format12 = hal.CodeFormat{Bits: 12, Storage: sample.U16, Shift: 4, Min: 0x0800, Max: 0xF7F0, Mid: 0x8000}

// codesPerDiv is 480 codes per division in word units.
const codesPerDiv = 480 * 16

// waveArea is where the PL's waveform plane lands on the screen (spec 07
// §3.1; the simulator places it so, the mixer is not confirmed).
var waveArea = image.Rect(19, 50, 19+1000, 50+480)

func (b *Board) identity() hal.Identity {
	return hal.Identity{
		Vendor: b.id.Manufacturer, Model: b.id.Model,
		Serial:         fmt.Sprintf("%016X", b.scope),
		VendorFirmware: b.vendorFW,
		HWRevision:     fmt.Sprintf("pcb %d bom %d", b.id.PCB, b.id.BOM),
		ADCPart:        b.id.ADC.String(),
		Fitted:         map[string]string{"channels": strconv.Itoa(b.id.Channels)},
	}
}

// Platform is the unit's identity, bring-up and system services.
func (b *Board) Platform() hal.Platform { return (*platformFacet)(b) }

// Planner is the timebase arithmetic.
func (b *Board) Planner() hal.Planner { return (*plannerFacet)(b) }

// Acquirer is the acquisition owner.
func (b *Board) Acquirer() hal.Acquirer { return (*acquirerFacet)(b) }

// Readout is the record readout.
func (b *Board) Readout() hal.Readout { return (*readoutFacet)(b) }

// Trigger is the trigger engine.
func (b *Board) Trigger() hal.Trigger { return (*triggerFacet)(b) }

// FrontEnd is the analog path.
func (b *Board) FrontEnd() hal.FrontEnd { return (*frontEndFacet)(b) }

// Display is fb0 over the PL renderer.
func (b *Board) Display() hal.Display { return (*displayFacet)(b) }

// Input is the front panel.
func (b *Board) Input() hal.Input { return (*inputFacet)(b) }

// Storage is p12.
func (b *Board) Storage() hal.Storage { return (*storageFacet)(b) }

// Streamer is the roll stream.
func (b *Board) Streamer() (hal.Streamer, bool) { return (*streamerFacet)(b), true }

// Images is absent on this board.
func (b *Board) Images() (hal.Images, bool) { return nil, false }

// Stacker is absent.
func (b *Board) Stacker() (hal.Stacker, bool) {
	if b.open != nil && b.open.pl.Has(openpl.CapStacker) {
		return (*openStacker)(b), true
	}
	return nil, false
}

// Segments is the sequence (segmented) acquisition.
func (b *Board) Segments() (hal.Segments, bool) { return (*segmentsFacet)(b), true }

// Counter is the hardware frequency counter.
func (b *Board) Counter() (hal.Counter, bool) { return (*counterFacet)(b), true }

// LogicProbe is absent.
func (b *Board) LogicProbe() (hal.LogicProbe, bool) { return nil, false }

// Generator is absent.
func (b *Board) Generator() (hal.Generator, bool) { return nil, false }
