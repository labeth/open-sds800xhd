// Package trigcases is the trigger's register goldens: HAL trigger
// specifications with the register values spec 05 gives for them. The
// scopeboard unit tests program them on a fake register plane and the
// in-guest board test programs them on the simulated unit; both check the
// same values, so a golden is written once.
//
// The cases run in order on one trigger engine after the start-up restore
// and the start-up edge trigger (spec 05 §7): registers a type does not
// write keep what the cases before it left, and the goldens rely on that.
// Every channel is on at 1 V/div with offset 0 and probe 1: a level of v
// volts is round(60·v) screen rows, and the hysteresis is 62 codes (158
// with noise reject).
package trigcases

import (
	"fmt"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-dso-core/hal/prototrig"
)

// Case is one trigger and what the registers hold after it is programmed.
type Case struct {
	Name string
	Spec hal.TriggerSpec
	// Want is the last value of each register, keyed "acq:0x0e0" or
	// "map:0x030". Empty for a refused case.
	Want map[string]uint16
	// Refuse is set when the board must refuse the spec; the text is the
	// refused feature.
	Refuse string
}

// Unit lists the cases the host unit test can run but the simulated board
// cannot be put into (roll mode needs an acquisition plan in roll).
type Unit struct {
	Name  string
	Spec  hal.TriggerSpec
	Roll  bool
	Want  map[string]uint16
	After string // the case it follows
}

// A and M name registers of the ACQ and MAP planes.
func A(i int) string { return fmt.Sprintf("acq:0x%03x", i) }

// M names a MAP-plane register.
func M(i int) string { return fmt.Sprintf("map:0x%03x", i) }

func ch(i int) hal.Source { return hal.Source{Kind: hal.Analog, Index: i} }

func one(s hal.TrigStage) hal.TriggerSpec {
	return hal.TriggerSpec{Stages: []hal.TrigStage{s}, Sweep: hal.NormalSweep}
}

func lv(v float64, s hal.Slope) hal.Levels { return hal.Levels{High: v, Slope: s} }

func q(op hal.QualOp, minS, maxS float64) *hal.Qualifier {
	return &hal.Qualifier{Op: op, MinS: minS, MaxS: maxS}
}

func r(kv ...any) map[string]uint16 {
	m := map[string]uint16{}
	for i := 0; i < len(kv); i += 2 {
		k, kok := kv[i].(string)
		v, vok := kv[i+1].(int)
		if !kok || !vok {
			panic(fmt.Sprintf("trigcases: register list entry %d is not a name, value pair", i/2))
		}
		m[k] = uint16(v) //nolint:gosec // G115: register values in the tables are 16-bit
	}
	return m
}

// manchester stands for a protocol without trigger hardware (spec 05 §10.7).
type manchester struct{ hal.ProtoTriggerType }

func (manchester) Protocol() string { return "manchester" }

func line(c int, v float64) prototrig.Line { return prototrig.Line{Source: ch(c), Level: v} }

// Cases are the goldens, in the order they run.
var Cases = func() []Case {
	holdoff := one(hal.TrigStage{Node: hal.NodeEdge, Source: ch(1), Levels: lv(-1, hal.Falling)})
	holdoff.HoldoffS, holdoff.Coupling, holdoff.NoiseReject = 1.0000003e-6, hal.TrigAC, true
	acLine := one(hal.TrigStage{Node: hal.NodeEdge, Source: hal.Source{Kind: hal.ACLine}, Levels: lv(0, hal.Either)})
	long := one(hal.TrigStage{Node: hal.NodeEdge, Source: ch(0), Levels: lv(0, hal.Rising)})
	long.HoldoffS = 30
	single := one(hal.TrigStage{Node: hal.NodeEdge, Source: ch(0), Levels: lv(0, hal.Rising)})
	single.Sweep = hal.Single
	auto := single
	auto.Sweep = hal.Auto
	pat := func(logic hal.PatternLogic) *hal.Pattern {
		return &hal.Pattern{Terms: []hal.PatternTerm{
			{Source: ch(0), State: hal.High, Level: 0.5},
			{Source: ch(1), State: hal.Low, Level: -0.5},
			{Source: ch(3), State: hal.High, Level: 1},
		}, Logic: logic}
	}
	videoHoldoff := one(hal.TrigStage{Node: hal.NodeVideo, Source: ch(0), Video: &hal.Video{Standard: "ntsc", Line: 1, Field: 1}})
	videoHoldoff.HoldoffS = 1e-6
	qualAC := one(hal.TrigStage{
		Node: hal.NodeEdge, Source: ch(0), Levels: lv(0, hal.Rising),
		Gate: &hal.Gate{Source: ch(1), High: true},
	})
	qualAC.Coupling = hal.TrigAC
	events := long
	events.HoldoffS, events.HoldoffEv = 0, 3
	reset := one(hal.TrigStage{Node: hal.NodeEdge, Source: ch(0)})
	reset.Reset = &hal.TrigStage{Node: hal.NodeEdge, Source: ch(1)}

	return []Case{
		{
			Name: "edge-c1-rising", Spec: one(hal.TrigStage{Node: hal.NodeEdge, Source: ch(0), Levels: lv(0.5, hal.Rising)}),
			Want: r(A(0x00E), 0x1000, A(0x0E7), 9, A(0x042), 0x1a, A(0x0B5), 1, A(0x0E0), 0x8b2, A(0x0E1), 0x3e,
				A(0x0D7), 0x8b2, A(0x0D6), 0x3e, A(0x0B4), 0x8b2, A(0x0B2), 0x3e, A(0x0B0), 0, A(0x0E4), 0, A(0x00A), 0),
		},
		{
			Name: "edge-c2-falling-ac-holdoff", Spec: holdoff,
			Want: r(A(0x00E), 0x1110, A(0x011), 0x100, A(0x0B5), 2, A(0x0E0), 0x6be, A(0x0E1), 0x9e,
				A(0x0DB), 0x6be, A(0x0DA), 0x9e, A(0x0B4), 0x6be, A(0x0B2), 0x9e, A(0x0B0), 1, A(0x0E4), 1,
				A(0x008), 0xfa, A(0x009), 0, A(0x052), 0, A(0x00A), 0x100),
		},
		{
			Name: "edge-acline-either", Spec: acLine,
			Want: r(A(0x00E), 0x1510, A(0x00A), 0x8000, A(0x011), 0x200, A(0x0B5), 3, A(0x0E0), 0x800, A(0x0E1), 0x3e,
				A(0x0B4), 0x800, A(0x033), 0x100),
		},
		{
			Name: "edge-holdoff-30s", Spec: long,
			Want: r(A(0x008), 0xeb00, A(0x009), 0xbf08, A(0x052), 1, A(0x00A), 0x100, A(0x00E), 0x1000),
		},
		{
			Name: "slope-c1-rising-less", Spec: one(hal.TrigStage{
				Node: hal.NodeSlope, Source: ch(0),
				Levels: hal.Levels{Low: -0.5, High: 0.5, Slope: hal.Rising}, Qual: q(hal.Shorter, 0, 20e-9),
			}),
			Want: r(A(0x00E), 0x1001, A(0x0E7), 9, A(0x011), 0, A(0x02C), 0, A(0x0B5), 1, A(0x0E0), 0x6d2, A(0x0E2), 0x8b2,
				A(0x0E3), 0x3e, A(0x0D7), 0x6d2, A(0x0D9), 0x8b2, A(0x0CC), 0x8b2, A(0x045), 0x3e,
				A(0x012), 0x14, A(0x013), 0, A(0x00A), 0),
		},
		{
			Name: "pulse-c2-negative-inside", Spec: one(hal.TrigStage{
				Node: hal.NodePulse, Source: ch(1),
				Levels: lv(0.25, hal.Falling), Qual: q(hal.Inside, 100e-9, 1e-6),
			}),
			Want: r(A(0x00E), 0x1112, A(0x011), 0x002, A(0x0B5), 1, A(0x0E0), 0x83a, A(0x0DB), 0x83a, A(0x0B4), 0x83a,
				A(0x012), 0x64, A(0x014), 0x3e8),
		},
		{
			Name: "pulse-c1-positive-greater", Spec: one(hal.TrigStage{
				Node: hal.NodePulse, Source: ch(0),
				Levels: lv(0, hal.Rising), Qual: q(hal.Longer, 1e-6, 0),
			}),
			Want: r(A(0x00E), 0x1002, A(0x011), 0x081, A(0x0B5), 2, A(0x0E0), 0x83e, A(0x014), 0x3e8),
		},
		{
			Name: "runt-c1-positive", Spec: one(hal.TrigStage{
				Node: hal.NodeRunt, Source: ch(0),
				Levels: hal.Levels{Low: 0.1, High: 0.15, Slope: hal.Rising}, Qual: q(hal.Shorter, 0, 50e-9),
			}),
			Want: r(A(0x00E), 0x1007, A(0x02D), 0x100, A(0x0B5), 2, A(0x0E0), 0x86e, A(0x0E2), 0x886, A(0x0B4), 0x86e,
				A(0x012), 0x32),
		},
		{
			Name: "window-c1", Spec: one(hal.TrigStage{
				Node: hal.NodeWindow, Source: ch(0),
				Levels: hal.Levels{Low: -0.833, High: 0},
			}),
			Want: r(A(0x00E), 0x1000, A(0x011), 0x381, A(0x0B5), 2, A(0x0CD), 1, A(0x0E0), 0x6ae, A(0x0E2), 0x7c2,
				A(0x0D7), 0x6ae, A(0x0D9), 0x7c2, A(0x0B4), 0x6ae, A(0x0CC), 0x7c2),
		},
		{
			Name: "interval-c1-falling-outside", Spec: one(hal.TrigStage{
				Node: hal.NodeEdge, Source: ch(0),
				Levels: lv(0, hal.Falling), Qual: q(hal.Outside, 10e-9, 2e-6),
			}),
			Want: r(A(0x00E), 0x1006, A(0x0E7), 9, A(0x02D), 0x106, A(0x0B5), 2, A(0x0E0), 0x83e, A(0x011), 0x081,
				A(0x012), 0xa, A(0x014), 0x7d0),
		},
		{
			Name: "dropout-c2-rising-edge", Spec: one(hal.TrigStage{
				Node: hal.NodeTimeout, Source: ch(1),
				Levels: lv(0.5, hal.Rising), Qual: q(hal.Longer, 1e-6, 0),
			}),
			Want: r(A(0x00E), 0x1115, A(0x0E7), 10, A(0x02C), 0x1100, A(0x0B5), 1, A(0x012), 0x3e8, A(0x0E0), 0x8b2,
				A(0x0DB), 0x8b2),
		},
		{
			Name: "dropout-c2-falling-state", Spec: one(hal.TrigStage{
				Node: hal.NodeTimeout, Source: ch(1),
				Levels: lv(0.5, hal.Falling), Qual: q(hal.Longer, 20e-9, 0), Timeout: hal.TimeoutState,
			}),
			Want: r(A(0x00E), 0x1115, A(0x02C), 0, A(0x0B5), 2, A(0x012), 0x14, A(0x0E0), 0x92e),
		},
		{
			Name: "nth-edge-c1", Spec: one(hal.TrigStage{
				Node: hal.NodeEdge, Source: ch(0), Levels: lv(0, hal.Rising),
				Count: 5, Qual: q(hal.Longer, 100e-9, 0),
			}),
			Want: r(A(0x00E), 0x100a, A(0x087), 5, A(0x014), 0x64, A(0x0B5), 1, A(0x011), 0x081, A(0x0E0), 0x7c2),
		},
		{
			Name: "video-ntsc", Spec: one(hal.TrigStage{
				Node: hal.NodeVideo, Source: ch(0), Levels: lv(-0.15, 0),
				Video: &hal.Video{Standard: "ntsc", Line: 10, Field: 1},
			}),
			Want: r(A(0x00E), 0x1003, A(0x0E7), 9, A(0x011), 0x001, A(0x014), 0x96, A(0x017), 0x13e0, A(0x016), 0x1005,
				A(0x0B5), 1, A(0x0E0), 0x77a, A(0x0E4), 0, A(0x00A), 0),
		},
		{
			Name: "video-720p", Spec: one(hal.TrigStage{
				Node: hal.NodeVideo, Source: ch(0), Levels: lv(-0.15, 0),
				Video: &hal.Video{Standard: "720p50", Line: 2, Field: 1},
			}),
			Want: r(A(0x017), 0x01a0, A(0x016), 0x02ed),
		},
		{
			Name: "video-custom", Spec: one(hal.TrigStage{
				Node: hal.NodeVideo, Source: ch(0), Levels: lv(-0.15, 0),
				Video: &hal.Video{Standard: "custom", Line: 10, Field: 3, Lines: 300, FrameHz: 25, Interlace: 1, Fields: 4},
			}),
			Want: r(A(0x017), 0x03ff, A(0x016), 0x3009),
		},
		{
			Name: "pattern-and", Spec: one(hal.TrigStage{
				Node: hal.NodePattern, Pattern: pat(hal.PatternAnd),
				Qual: q(hal.Longer, 10e-9, 0),
			}),
			Want: r(A(0x00E), 0x1034, A(0x0E7), 10, A(0x0AD), 0x10, A(0x0AC), 0x1201, A(0x0D7), 0x92e, A(0x0D6), 0x3e,
				A(0x0DB), 0x6d2, A(0x101), 0xa1e, A(0x0B5), 2, A(0x0CD), 1, A(0x107), 2, A(0x0B4), 0x92e,
				A(0x0CC), 0x6d2, A(0x106), 0xa1e, A(0x109), 0x3e, A(0x014), 0xa, A(0x0E4), 0),
		},
		{
			Name: "pattern-nor", Spec: one(hal.TrigStage{Node: hal.NodePattern, Pattern: pat(hal.PatternNor)}),
			Want: r(A(0x0AD), 0x13, A(0x0D7), 0x8b2, A(0x0DB), 0x74e, A(0x0B5), 1, A(0x0CD), 2, A(0x014), 2),
		},
		{
			Name: "qualified-state", Spec: one(hal.TrigStage{
				Node: hal.NodeEdge, Source: ch(0), Levels: lv(0, hal.Rising),
				Gate: &hal.Gate{Source: ch(1), Level: 0.5, High: true},
			}),
			Want: r(A(0x00E), 0x1008, A(0x0E7), 9, A(0x080), 0x00a1, A(0x0DB), 0x8b2, A(0x0D7), 0x7c2, A(0x012), 2, A(0x0E4), 0),
		},
		{
			Name: "qualified-edge-delay", Spec: one(hal.TrigStage{
				Node: hal.NodeEdge, Source: ch(3), Levels: lv(0, hal.Falling),
				Gate: &hal.Gate{Source: ch(2), Level: -0.5, Edge: true}, Qual: q(hal.Inside, 10e-9, 1e-6),
			}),
			Want: r(A(0x00E), 0x1338, A(0x080), 0x3202, A(0x0F1), 0x74e, A(0x101), 0x83e, A(0x012), 0xa, A(0x014), 0x3e8),
		},
		{
			Name: "qualified-state-delay", Spec: one(hal.TrigStage{
				Node: hal.NodeEdge, Source: ch(0), Levels: lv(0, hal.Rising),
				Gate: &hal.Gate{Source: ch(1)}, Qual: q(hal.Longer, 50e-9, 0),
			}),
			Want: r(A(0x080), 0x1181, A(0x014), 0x32),
		},
		{
			Name: "qualified-edge", Spec: one(hal.TrigStage{
				Node: hal.NodeEdge, Source: ch(0), Levels: lv(0, hal.Rising),
				Gate: &hal.Gate{Source: ch(1), Edge: true, High: true},
			}),
			Want: r(A(0x080), 0x20a1, A(0x012), 2),
		},
		{
			Name: "setup", Spec: one(hal.TrigStage{
				Node: hal.NodeSetupHold, Source: ch(0), Levels: lv(0, hal.Rising),
				Gate: &hal.Gate{Source: ch(1), Level: 0.5, High: true}, Qual: q(hal.Shorter, 0, 20e-9),
			}),
			Want: r(A(0x00E), 0x1008, A(0x080), 0x10e1, A(0x0D7), 0x83e, A(0x0DB), 0x92e, A(0x012), 0x14),
		},
		{
			Name: "hold", Spec: one(hal.TrigStage{
				Node: hal.NodeSetupHold, Source: ch(0), Levels: lv(0, hal.Falling),
				Gate: &hal.Gate{Source: ch(1), Level: 0.5, Hold: true}, Qual: q(hal.Longer, 5e-9, 0),
			}),
			Want: r(A(0x00E), 0x1118, A(0x080), 0x31c0, A(0x0D7), 0x7c2, A(0x0DB), 0x8b2, A(0x014), 5),
		},
		{
			Name: "delay-pattern-then-edge", Spec: hal.TriggerSpec{Sweep: hal.NormalSweep, Stages: []hal.TrigStage{
				{
					Node: hal.NodePattern, Levels: hal.Levels{Slope: hal.Rising},
					Pattern: &hal.Pattern{Terms: []hal.PatternTerm{{Source: ch(0), State: hal.High, Level: 0.5}}},
				},
				{Node: hal.NodeEdge, Source: ch(2), Levels: lv(0.05, hal.Falling), Qual: q(hal.Shorter, 0, 1e-6)},
			}},
			Want: r(A(0x00E), 0x1228, A(0x080), 0x7060, A(0x0AD), 0x10, A(0x0AC), 0x2221, A(0x0D7), 0x92e, A(0x0F1), 0x856,
				A(0x0B5), 2, A(0x012), 0x3e8, A(0x0E4), 0),
		},
		{
			Name: "i2c-addr7-read", Spec: one(hal.TrigStage{Node: hal.NodeProtocol, Proto: prototrig.I2C{
				SCL: line(0, 0.5), SDA: line(1, 0.5), Condition: prototrig.I2CAddr7Data, Address: 0x50, RW: prototrig.Read,
				Data: []uint8{0x12},
			}}),
			Want: r(A(0x00E), 0x1229, A(0x0E7), 9, A(0x0E6), 0x8d1, A(0x0E5), 0x3e, A(0x0EC), 0x8d1, A(0x0EB), 0x3e,
				M(0x030), 0x0001, M(0x044), 0, M(0x048), 0x1fd4, M(0x047), 0x0c50, M(0x032), 0, M(0x036), 0x12,
				M(0x042), 0xff, M(0x043), 1, M(0x049), 0x800f),
		},
		{
			Name: "i2c-eeprom", Spec: one(hal.TrigStage{Node: hal.NodeProtocol, Proto: prototrig.I2C{
				SCL: line(0, 0.5), SDA: line(1, 0.5), Condition: prototrig.I2CEEPROM, Address: -1,
				Compare: prototrig.Greater, Data: []uint8{0x40},
			}}),
			Want: r(M(0x030), 0x0021, M(0x048), 0x1e14, M(0x047), 0x0c50, M(0x036), 0x40, M(0x042), 0xff, M(0x043), 1),
		},
		{
			Name: "spi-msb", Spec: one(hal.TrigStage{Node: hal.NodeProtocol, Proto: prototrig.SPI{
				Clock: line(0, 0.5), Data: line(1, 0.5), Select: line(2, 0.5), SelectMode: prototrig.SelectActiveLow,
				MSBFirst: true, Bits: []hal.PatternState{hal.High, hal.Low, hal.High, hal.DontCare, hal.Low, hal.Low, hal.High, hal.High},
			}}),
			Want: r(M(0x030), 0x0101, M(0x044), 0x0002, M(0x04F), 0, M(0x049), 0x800f, M(0x04E), 0xffff,
				M(0x036), 0xa3, M(0x042), 0xef, M(0x035), 0, M(0x041), 0, M(0x043), 8, A(0x10B), 0x8d1),
		},
		{
			Name: "uart-data", Spec: one(hal.TrigStage{Node: hal.NodeProtocol, Proto: prototrig.UART{
				Line: line(0, 1), Condition: prototrig.UARTData, Baud: 115200, Bits: 8, Parity: prototrig.Even,
				StopBits: 1, IdleHigh: true, Value: 0x55,
			}}),
			Want: r(M(0x030), 0x0200, M(0x050), 0x0466, M(0x051), 0x1000, M(0x036), 0x55, M(0x042), 0xff,
				M(0x045), 0x12d, M(0x046), 0xfd69, M(0x053), 0x12a, M(0x043), 8, A(0x0E6), 0x9c1),
		},
		{
			Name: "can-id-data", Spec: one(hal.TrigStage{Node: hal.NodeProtocol, Proto: prototrig.CAN{
				Line: line(1, 0.5), Condition: prototrig.CANIDData, Baud: 500000, ID: 0x123, Data: []uint8{0xab},
			}}),
			Want: r(M(0x030), 0x0501, M(0x051), 0x1003, M(0x031), 0x48c, M(0x03D), 0x1ffc, M(0x036), 0xab,
				M(0x042), 0xff, M(0x032), 0, M(0x043), 8, M(0x045), 0x51e, M(0x046), 0xb851),
		},
		{
			Name: "lin-id", Spec: one(hal.TrigStage{Node: hal.NodeProtocol, Proto: prototrig.LIN{
				Line: line(0, 0.5), Condition: prototrig.LINID, Baud: 19200, ID: 0x22,
				ChecksumError: true, SyncError: true, ParityError: true, Spec2: true,
			}}),
			Want: r(M(0x030), 0x0300, M(0x052), 0x3d, M(0x032), 0x22, M(0x03E), 0x3f, M(0x036), 0, M(0x043), 0,
				M(0x045), 0x32, M(0x046), 0x54e6),
		},
		{Name: "sweep-single", Spec: single, Want: r(A(0x00E), 0x0000, A(0x00A), 0)},
		{Name: "sweep-auto", Spec: auto, Want: r(A(0x00E), 0x1000, A(0x00A), 1)},

		{
			Name: "refuse-digital", Refuse: "trigger source",
			Spec: one(hal.TrigStage{Node: hal.NodeEdge, Source: hal.Source{Kind: hal.Digital, Index: 3}}),
		},
		{
			Name: "refuse-external", Refuse: "trigger source",
			Spec: one(hal.TrigStage{Node: hal.NodeEdge, Source: hal.Source{Kind: hal.External}}),
		},
		{Name: "refuse-zone", Refuse: "zone trigger", Spec: one(hal.TrigStage{Node: hal.NodeZone, Source: ch(0)})},
		{Name: "refuse-symbol", Refuse: "trigger symbol", Spec: one(hal.TrigStage{Node: hal.NodeSymbol, Source: ch(0)})},
		{Name: "refuse-holdoff-events", Refuse: "holdoff by events", Spec: events},
		{Name: "refuse-reset", Refuse: "trigger reset", Spec: reset},
		{Name: "refuse-video-holdoff", Refuse: "holdoff", Spec: videoHoldoff},
		{Name: "refuse-qualified-ac", Refuse: "trigger coupling", Spec: qualAC},
		{Name: "refuse-interval-either", Refuse: "trigger slope", Spec: one(hal.TrigStage{
			Node:   hal.NodeEdge,
			Source: ch(0), Levels: lv(0, hal.Either), Qual: q(hal.Shorter, 0, 1e-6),
		})},
		{Name: "refuse-time-1ns", Refuse: "pulse time", Spec: one(hal.TrigStage{
			Node:   hal.NodePulse,
			Source: ch(0), Levels: lv(0, hal.Rising), Qual: q(hal.Shorter, 0, 1e-9),
		})},
		{Name: "refuse-holdoff-4ns", Refuse: "holdoff", Spec: func() hal.TriggerSpec {
			s := long
			s.HoldoffS = 4e-9
			return s
		}()},
		{Name: "refuse-can-error", Refuse: "CAN error trigger", Spec: one(hal.TrigStage{
			Node:  hal.NodeProtocol,
			Proto: prototrig.CAN{Line: line(0, 0.5), Condition: prototrig.CANError, Baud: 500000},
		})},
		{Name: "refuse-manchester", Refuse: "manchester trigger", Spec: one(hal.TrigStage{
			Node:  hal.NodeProtocol,
			Proto: manchester{},
		})},
		{Name: "refuse-three-stages", Refuse: "trigger sequence", Spec: hal.TriggerSpec{Stages: make([]hal.TrigStage, 3)}},
		{Name: "refuse-pulse-no-width", Refuse: "pulse time", Spec: one(hal.TrigStage{
			Node:   hal.NodePulse,
			Source: ch(0), Levels: lv(0, hal.Rising),
		})},
	}
}()

// UnitCases need the engine in roll mode; they run after the case named in
// After on the host only.
var UnitCases = []Unit{
	{
		Name: "roll-auto-disables", After: "sweep-auto", Roll: true,
		Spec: hal.TriggerSpec{Stages: []hal.TrigStage{{Node: hal.NodeEdge, Source: ch(0), Levels: lv(0, hal.Rising)}}, Sweep: hal.Auto},
		Want: r(A(0x00E), 0x000f, A(0x00A), 0, A(0x0E7), 9),
	},
	{
		Name: "roll-normal", After: "sweep-auto", Roll: true,
		Spec: hal.TriggerSpec{Stages: []hal.TrigStage{{Node: hal.NodeEdge, Source: ch(1), Levels: lv(0, hal.Rising)}}, Sweep: hal.NormalSweep},
		Want: r(A(0x00E), 0x0110, A(0x00A), 0),
	},
	{
		Name: "roll-auto-acline", After: "sweep-auto", Roll: true,
		Spec: hal.TriggerSpec{Stages: []hal.TrigStage{{Node: hal.NodeEdge, Source: hal.Source{Kind: hal.ACLine}, Levels: lv(0, hal.Rising)}}, Sweep: hal.Auto},
		Want: r(A(0x00E), 0x051f, A(0x00A), 0),
	},
}

// Parity are triggers set up exactly as a reference run of the simulator set
// them up with SCPI (all four channels on at 1 V/div): the parity harness
// compares the registers they leave with the reference's (sim/cmd/simtest,
// area trigger). They run after Cases, in order; a case named "-prep" only prepares
// the state the next one needs.
var Parity = []Case{
	{Name: "ref-qual-state", Spec: one(hal.TrigStage{
		Node: hal.NodeEdge, Source: ch(0), Levels: lv(0, hal.Falling),
		Gate: &hal.Gate{Source: ch(1), High: true},
	})},
	{Name: "ref-qual-state-delay", Spec: one(hal.TrigStage{
		Node: hal.NodeEdge, Source: ch(0), Levels: lv(0, hal.Falling),
		Gate: &hal.Gate{Source: ch(1), High: true}, Qual: q(hal.Inside, 2e-9, 2e-6),
	})},
	{Name: "ref-pattern-or", Spec: one(hal.TrigStage{Node: hal.NodePattern, Pattern: &hal.Pattern{Terms: []hal.PatternTerm{
		{Source: ch(0), State: hal.High}, {Source: ch(1), State: hal.Low}, {Source: ch(3), State: hal.High},
	}, Logic: hal.PatternOr}})},
	{Name: "ref-hold", Spec: one(hal.TrigStage{
		Node: hal.NodeSetupHold, Source: ch(0), Levels: lv(0, hal.Rising),
		Gate: &hal.Gate{Source: ch(1), Hold: true}, Qual: q(hal.Shorter, 0, 2e-9),
	})},
	{Name: "ref-i2c-prep", Spec: one(hal.TrigStage{Node: hal.NodeProtocol, Proto: prototrig.I2C{
		SCL: line(2, 0.5), SDA: line(3, 0.4),
		Condition: prototrig.I2CAddr7Data, Address: -1, RW: prototrig.AnyRW,
	}})},
	{Name: "ref-i2c-eeprom", Spec: one(hal.TrigStage{Node: hal.NodeProtocol, Proto: prototrig.I2C{
		SCL: line(2, 0.5),
		SDA: line(3, 0.4), Condition: prototrig.I2CEEPROM, Address: -1,
	}})},
}
