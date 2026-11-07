//go:build simboard

package boardtest

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-dso-core/hal/prototrig"
	"github.com/labeth/open-dso-core/sample"
	"github.com/labeth/open-sds800xhd/app/internal/platform"
)

// TestTriggerFire checks the trigger end to end: the simulator's input
// signals drive its model of the trigger nodes, which fires (or not) on what
// the board programmed; the record must then hold the trigger at the analytic
// instant the type defines, and the measured parameter (a width, a time, a
// count, a decoded value) must be the injected one. Each trigger type runs a
// signal that must fire and a near miss that must not. Every case prints a
// table row "L3ROW type|form|expected|measured|verdict" for the harness.
//
// Signals are written "Cn=kind,freq,vpp,offset[,options]" (the simulator's
// generator); every channel is at 1 V/div, offset 0, probe 1.
func TestTriggerFire(t *testing.T) {
	for _, fc := range fireCases() {
		if board.Profile().Channels < 4 && slices.ContainsFunc(fc.chans, func(c int) bool { return c > 1 }) {
			continue
		}
		runFire(t, fc)
	}
	sweepCases(t)
}

// Tolerance of the trigger position, in record samples: the trigger is the
// first sample at or after the analytic instant, so the instant lies within
// one sample before the trigger index; a quarter sample on either side covers
// the interpolation of the crossing between samples.
const (
	posLo = -0.25
	posHi = 1.25
)

type fireCase struct {
	typ, form string
	sigs      string
	chans     []int
	tdiv      float64
	spec      hal.TriggerSpec
	fires     bool
	check     func(r fireRec) (want, got string, ok bool)
	c1        *hal.Channel // C1's setting when not 1 V/div, offset 0, probe 1
}

func ch(i int) hal.Source { return hal.Source{Kind: hal.Analog, Index: i} }

func normal(st hal.TrigStage) hal.TriggerSpec {
	return hal.TriggerSpec{Sweep: hal.NormalSweep, Stages: []hal.TrigStage{st}}
}

func ql(op hal.QualOp, lo, hi float64) *hal.Qualifier {
	return &hal.Qualifier{Op: op, MinS: lo, MaxS: hi}
}

// fireRec is a read record.
type fireRec struct {
	f  *sample.Frame
	tr map[int]*sample.Trace
	dt float64
	at float64
}

func (r fireRec) v(c, i int) float64 { return r.tr[c].Volts(i) }
func (r fireRec) n(c int) int        { return r.tr[c].Valid }

// cross is the interpolated index of the crossing of level by channel c in
// direction dir (+1 rising, −1 falling, 0 either) nearest to index near,
// within span samples.
func (r fireRec) cross(c int, level float64, dir int, near float64, span int) (float64, bool) {
	best, ok := 0.0, false
	lo, hi := max(0, int(near)-span), min(r.n(c)-2, int(near)+span)
	for i := lo; i <= hi; i++ {
		a, b := r.v(c, i)-level, r.v(c, i+1)-level
		up, down := a < 0 && b >= 0, a >= 0 && b < 0
		if dir > 0 && !up || dir < 0 && !down || dir == 0 && !up && !down {
			continue
		}
		x := float64(i) + a/(a-b)
		if !ok || math.Abs(x-near) < math.Abs(best-near) {
			best, ok = x, true
		}
	}
	return best, ok
}

// before is the last crossing of level in direction dir before index at.
func (r fireRec) before(c int, level float64, dir int, at float64) (float64, bool) {
	for i := int(at) - 1; i >= 0; i-- {
		a, b := r.v(c, i)-level, r.v(c, i+1)-level
		up, down := a < 0 && b >= 0, a >= 0 && b < 0
		if dir > 0 && up || dir < 0 && down || dir == 0 && (up || down) {
			if x := float64(i) + a/(a-b); x < at-0.5 {
				return x, true
			}
		}
	}
	return 0, false
}

// posCheck is the position check: the crossing nearest the trigger.
func posCheck(c int, level float64, dir int) func(r fireRec) (string, string, bool) {
	return func(r fireRec) (string, string, bool) {
		x, ok := r.cross(c, level, dir, r.at, 50)
		if !ok {
			return posWant(level, dir), "no crossing near the trigger", false
		}
		e := r.at - x
		return posWant(level, dir), fmt.Sprintf("trigger %+.2f samples after it", e), e >= posLo && e <= posHi
	}
}

func posWant(level float64, dir int) string {
	d := map[int]string{1: "rising", -1: "falling", 0: "either"}[dir]
	return fmt.Sprintf("%s crossing of %g V at the trigger", d, level)
}

// widthCheck checks the trigger at the closing crossing and the time back to
// the opening one (a pulse's width, a slope's transition, an interval).
func widthCheck(c int, closeLvl float64, closeDir int, openLvl float64, openDir int, want float64) func(r fireRec) (string, string, bool) {
	return func(r fireRec) (string, string, bool) {
		w, g, ok := posCheck(c, closeLvl, closeDir)(r)
		x, _ := r.cross(c, closeLvl, closeDir, r.at, 50)
		o, have := r.before(c, openLvl, openDir, x)
		if !have {
			return w, g + "; no opening crossing", false
		}
		got := (x - o) * r.dt
		tol := 2*r.dt + 0.02*want
		return fmt.Sprintf("%s; %s", w, eng(want)), fmt.Sprintf("%s; %s", g, eng(got)), ok && math.Abs(got-want) <= tol
	}
}

func eng(s float64) string {
	switch a := math.Abs(s); {
	case a >= 1e-3:
		return fmt.Sprintf("%.4g ms", s*1e3)
	case a >= 1e-6:
		return fmt.Sprintf("%.4g µs", s*1e6)
	}
	return fmt.Sprintf("%.4g ns", s*1e9)
}

func fireCases() []fireCase {
	// 1.000123 MHz: not phase-locked to the sample clock, so crossings fall between samples
	sine := "C1=sine,1.000123e6,2,0"
	edge := func(lvl float64, s hal.Slope) hal.TriggerSpec {
		return normal(hal.TrigStage{Node: hal.NodeEdge, Source: ch(0), Levels: hal.Levels{High: lvl, Slope: s}})
	}
	nrej := edge(0.5, hal.Rising)
	nrej.NoiseReject = true
	ac := edge(0.25, hal.Rising)
	ac.Coupling = hal.TrigAC
	pulse := func(s hal.Slope, q *hal.Qualifier) hal.TriggerSpec {
		return normal(hal.TrigStage{Node: hal.NodePulse, Source: ch(0), Levels: hal.Levels{High: 0, Slope: s}, Qual: q})
	}
	// pulses with 4 ns edges, so a crossing interpolates between samples
	p80 := "C1=pulse,1e6,2,0,width=80e-9,rise=4e-9"
	p120 := "C1=pulse,1e6,2,0,width=120e-9,rise=4e-9"
	n80 := "C1=pulse,1e6,2,0,width=80e-9,rise=4e-9,inv=1"
	slope := func(s hal.Slope, q *hal.Qualifier) hal.TriggerSpec {
		return normal(hal.TrigStage{Node: hal.NodeSlope, Source: ch(0), Levels: hal.Levels{Low: -0.5, High: 0.5, Slope: s}, Qual: q})
	}
	tr := "C1=square,1e6,2,0,rise=100e-9"
	interval := func(q *hal.Qualifier) hal.TriggerSpec {
		return normal(hal.TrigStage{Node: hal.NodeEdge, Source: ch(0), Levels: hal.Levels{High: 0, Slope: hal.Rising}, Qual: q})
	}
	sq := "C1=square,1e6,2,0,rise=4e-9"
	window := normal(hal.TrigStage{Node: hal.NodeWindow, Source: ch(0), Levels: hal.Levels{Low: -0.5, High: 0.5}})
	runt := normal(hal.TrigStage{
		Node: hal.NodeRunt, Source: ch(0), Levels: hal.Levels{Low: 0.3, High: 0.7, Slope: hal.Rising},
		Qual: ql(hal.Shorter, 0, 1e-6),
	})
	runtNeg := normal(hal.TrigStage{
		Node: hal.NodeRunt, Source: ch(0), Levels: hal.Levels{Low: 0.3, High: 0.7, Slope: hal.Falling},
		Qual: ql(hal.Shorter, 0, 1e-6),
	})
	drop := func(to float64) hal.TriggerSpec {
		return normal(hal.TrigStage{
			Node: hal.NodeTimeout, Source: ch(0), Levels: hal.Levels{High: 0, Slope: hal.Rising},
			Qual: ql(hal.Longer, to, 0), Timeout: hal.TimeoutEdge,
		})
	}
	burst := "C1=square,1e6,2,0,rise=4e-9,burst=3:2"
	dropState := normal(hal.TrigStage{
		Node: hal.NodeTimeout, Source: ch(0), Levels: hal.Levels{High: 0, Slope: hal.Falling},
		Qual: ql(hal.Longer, 1.5e-6, 0), Timeout: hal.TimeoutState,
	})
	nth := normal(hal.TrigStage{
		Node: hal.NodeEdge, Source: ch(0), Levels: hal.Levels{High: 0, Slope: hal.Rising},
		Count: 3, Qual: ql(hal.Longer, 1.5e-6, 0),
	})
	pat := func(c2 hal.PatternState) hal.TriggerSpec {
		return normal(hal.TrigStage{Node: hal.NodePattern, Pattern: &hal.Pattern{Terms: []hal.PatternTerm{
			{Source: ch(0), State: hal.High, Level: 0}, {Source: ch(1), State: c2, Level: 0},
		}, Logic: hal.PatternAnd}, Qual: ql(hal.Longer, 2e-9, 0)})
	}
	qual := func(high bool) hal.TriggerSpec {
		return normal(hal.TrigStage{
			Node: hal.NodeEdge, Source: ch(0), Levels: hal.Levels{High: 0, Slope: hal.Rising},
			Gate: &hal.Gate{Source: ch(1), Level: 0, High: high},
		})
	}
	setup := func(lim float64) hal.TriggerSpec {
		return normal(hal.TrigStage{
			Node: hal.NodeSetupHold, Source: ch(0), Levels: hal.Levels{High: 0, Slope: hal.Rising},
			Gate: &hal.Gate{Source: ch(1), Level: 0, High: true}, Qual: ql(hal.Shorter, 0, lim),
		})
	}
	// hold: source = clock (C1), gate = data (C2) high; the data source is the trigger edge
	hold := func(lim float64) hal.TriggerSpec {
		return normal(hal.TrigStage{
			Node: hal.NodeSetupHold, Source: ch(0), Levels: hal.Levels{High: 0, Slope: hal.Rising},
			Gate: &hal.Gate{Source: ch(1), Level: 0, High: true, Hold: true}, Qual: ql(hal.Shorter, 0, lim),
		})
	}
	delay := func(lim float64) hal.TriggerSpec {
		return hal.TriggerSpec{Sweep: hal.NormalSweep, Stages: []hal.TrigStage{
			{
				Node: hal.NodePattern, Levels: hal.Levels{Slope: hal.Rising},
				Pattern: &hal.Pattern{Terms: []hal.PatternTerm{{Source: ch(1), State: hal.High, Level: 0}}},
			},
			{Node: hal.NodeEdge, Source: ch(0), Levels: hal.Levels{High: 0, Slope: hal.Falling}, Qual: ql(hal.Shorter, 0, lim)},
		}}
	}
	video := func() hal.TriggerSpec {
		return normal(hal.TrigStage{
			Node: hal.NodeVideo, Source: ch(0), Levels: hal.Levels{High: -0.15},
			Video: &hal.Video{Standard: "ntsc", AnyLine: true},
		})
	}
	return []fireCase{
		{
			typ: "edge", form: "rising 0.5 V", sigs: sine, chans: []int{0}, tdiv: 200e-9, spec: edge(0.5, hal.Rising), fires: true,
			check: posCheck(0, 0.5, 1),
		},
		{
			typ: "edge", form: "falling 0.5 V", sigs: sine, chans: []int{0}, tdiv: 200e-9, spec: edge(0.5, hal.Falling), fires: true,
			check: posCheck(0, 0.5, -1),
		},
		// either edge: the comparator's band is centred on the level (spec 05 §3.3: no bias), so a
		// rising edge fires half the hysteresis (31 codes) above it, a falling one below
		{
			typ: "edge", form: "either 0.5 V", sigs: sine, chans: []int{0}, tdiv: 200e-9, spec: edge(0.5, hal.Either), fires: true,
			check: func(r fireRec) (string, string, bool) {
				const half = 31.0 / 480
				_, g1, ok1 := posCheck(0, 0.5+half, 1)(r)
				_, g2, ok2 := posCheck(0, 0.5-half, -1)(r)
				w := "a crossing of 0.5 V ± half the hysteresis at the trigger"
				if ok1 {
					return w, "rising: " + g1, true
				}
				return w, "falling: " + g2, ok2
			},
		},
		{typ: "edge", form: "level above the signal", sigs: sine, chans: []int{0}, tdiv: 200e-9, spec: edge(1.5, hal.Rising)},
		{
			typ: "edge", form: "swing inside the hysteresis", sigs: "C1=sine,1e6,0.2,0.5", chans: []int{0}, tdiv: 200e-9,
			spec: edge(0.5, hal.Rising),
		},
		{
			typ: "edge", form: "AC coupling 0.25 V above a 2 V mean", sigs: "C1=sine,1.000123e6,1,2", chans: []int{0}, tdiv: 200e-9,
			spec: ac, fires: true, check: posCheck(0, 2.25, 1),
		},

		// the level in the channel's own scale: offset, probe and invert enter the comparator code
		{
			typ: "edge", form: "rising 0.25 V at 0.5 V/div, offset +0.3 V", sigs: "C1=sine,1.000123e6,1,0", chans: []int{0}, tdiv: 200e-9,
			spec: edge(0.25, hal.Rising), fires: true, check: posCheck(0, 0.25, 1), c1: &hal.Channel{VoltsDiv: 0.5, OffsetV: 0.3, Probe: 1},
		},
		{
			typ: "edge", form: "rising 5 V at the tip, 10x probe", sigs: sine, chans: []int{0}, tdiv: 200e-9,
			spec: edge(5, hal.Rising), fires: true, check: posCheck(0, 5, 1), c1: &hal.Channel{VoltsDiv: 10, Probe: 10},
		},
		{
			typ: "edge", form: "rising 0.5 V on an inverted channel", sigs: sine, chans: []int{0}, tdiv: 200e-9,
			spec: edge(0.5, hal.Rising), fires: true, check: posCheck(0, 0.5, 1), c1: &hal.Channel{VoltsDiv: 1, Probe: 1, Invert: true},
		},
		{
			typ: "edge", form: "noise reject off, ±0.15 V about the level", sigs: "C1=sine,1.000123e6,0.3,0.5", chans: []int{0}, tdiv: 200e-9,
			spec: edge(0.5, hal.Rising), fires: true, check: posCheck(0, 0.5, 1),
		},
		{
			typ: "edge", form: "noise reject on, ±0.15 V about the level", sigs: "C1=sine,1.000123e6,0.3,0.5", chans: []int{0}, tdiv: 200e-9,
			spec: nrej,
		},
		{
			typ: "pulse", form: "positive < 100 ns, 80 ns", sigs: p80, chans: []int{0}, tdiv: 50e-9, spec: pulse(hal.Rising, ql(hal.Shorter, 0, 100e-9)),
			fires: true, check: widthCheck(0, 0, -1, 0, 1, 80e-9),
		},
		{typ: "pulse", form: "positive < 100 ns, 120 ns", sigs: p120, chans: []int{0}, tdiv: 50e-9, spec: pulse(hal.Rising, ql(hal.Shorter, 0, 100e-9))},
		{
			typ: "pulse", form: "positive > 100 ns, 120 ns", sigs: p120, chans: []int{0}, tdiv: 50e-9, spec: pulse(hal.Rising, ql(hal.Longer, 100e-9, 0)),
			fires: true, check: widthCheck(0, 0, -1, 0, 1, 120e-9),
		},
		{typ: "pulse", form: "positive > 100 ns, 80 ns", sigs: p80, chans: []int{0}, tdiv: 50e-9, spec: pulse(hal.Rising, ql(hal.Longer, 100e-9, 0))},
		{
			typ: "pulse", form: "negative inside 70–90 ns, 80 ns", sigs: n80, chans: []int{0}, tdiv: 50e-9, spec: pulse(hal.Falling, ql(hal.Inside, 70e-9, 90e-9)),
			fires: true, check: widthCheck(0, 0, 1, 0, -1, 80e-9),
		},
		{typ: "pulse", form: "negative inside 85–95 ns, 80 ns", sigs: n80, chans: []int{0}, tdiv: 50e-9, spec: pulse(hal.Falling, ql(hal.Inside, 85e-9, 95e-9))},
		{
			typ: "pulse", form: "positive outside 85–95 ns, 80 ns", sigs: p80, chans: []int{0}, tdiv: 50e-9, spec: pulse(hal.Rising, ql(hal.Outside, 85e-9, 95e-9)),
			fires: true, check: widthCheck(0, 0, -1, 0, 1, 80e-9),
		},

		{
			typ: "slope", form: "rising < 60 ns, 50 ns", sigs: tr, chans: []int{0}, tdiv: 50e-9, spec: slope(hal.Rising, ql(hal.Shorter, 0, 60e-9)),
			fires: true, check: widthCheck(0, 0.5, 1, -0.5, 1, 50e-9),
		},
		{typ: "slope", form: "rising < 40 ns, 50 ns", sigs: tr, chans: []int{0}, tdiv: 50e-9, spec: slope(hal.Rising, ql(hal.Shorter, 0, 40e-9))},
		{
			typ: "slope", form: "falling > 40 ns, 50 ns", sigs: tr, chans: []int{0}, tdiv: 50e-9, spec: slope(hal.Falling, ql(hal.Longer, 40e-9, 0)),
			fires: true, check: widthCheck(0, -0.5, -1, 0.5, -1, 50e-9),
		},

		{
			typ: "interval", form: "rising > 0.9 µs, 1 µs", sigs: sq, chans: []int{0}, tdiv: 500e-9, spec: interval(ql(hal.Longer, 0.9e-6, 0)),
			fires: true, check: widthCheck(0, 0, 1, 0, 1, 1e-6),
		},
		{
			typ: "interval", form: "falling outside 0.2–0.9 µs, 1 µs", sigs: sq, chans: []int{0}, tdiv: 500e-9,
			spec:  normal(hal.TrigStage{Node: hal.NodeEdge, Source: ch(0), Levels: hal.Levels{High: 0, Slope: hal.Falling}, Qual: ql(hal.Outside, 0.2e-6, 0.9e-6)}),
			fires: true, check: widthCheck(0, 0, -1, 0, -1, 1e-6),
		},
		{typ: "interval", form: "rising > 1.1 µs, 1 µs", sigs: sq, chans: []int{0}, tdiv: 500e-9, spec: interval(ql(hal.Longer, 1.1e-6, 0))},

		{
			typ: "window", form: "±0.5 V, 2 Vpp sine", sigs: sine, chans: []int{0}, tdiv: 200e-9, spec: window, fires: true,
			check: func(r fireRec) (string, string, bool) {
				_, g1, ok1 := posCheck(0, 0.5, 1)(r)
				_, g2, ok2 := posCheck(0, -0.5, -1)(r)
				if ok1 {
					return "leaving the window at the trigger", "above: " + g1, true
				}
				return "leaving the window at the trigger", "below: " + g2, ok2
			},
		},
		{typ: "window", form: "±0.5 V, 0.8 Vpp sine", sigs: "C1=sine,1e6,0.8,0", chans: []int{0}, tdiv: 200e-9, spec: window},

		{
			typ: "runt", form: "positive 0.3/0.7 V, 0.5 V pulse", sigs: "C1=pulse,1e6,0.5,0.25,width=100e-9,rise=4e-9", chans: []int{0}, tdiv: 50e-9,
			spec: runt, fires: true, check: widthCheck(0, 0.3, -1, 0.3, 1, 100e-9),
		},
		{
			typ: "runt", form: "negative 0.3/0.7 V, 0.5 V dip", sigs: "C1=pulse,1e6,0.5,0.75,width=100e-9,rise=4e-9,inv=1", chans: []int{0}, tdiv: 50e-9,
			spec: runtNeg, fires: true, check: widthCheck(0, 0.7, 1, 0.7, -1, 100e-9),
		},
		{typ: "runt", form: "positive 0.3/0.7 V, 1 V pulse", sigs: "C1=pulse,1e6,1,0.5,width=100e-9,rise=4e-9", chans: []int{0}, tdiv: 50e-9, spec: runt},

		{
			typ: "dropout", form: "edge 1.5 µs, 2 µs gaps", sigs: burst, chans: []int{0}, tdiv: 500e-9, spec: drop(1.5e-6), fires: true,
			check: func(r fireRec) (string, string, bool) {
				o, ok := r.before(0, 0, 1, r.at)
				if !ok {
					return "1.5 µs after the last rising edge", "no rising edge before the trigger", false
				}
				got := (r.at - o) * r.dt
				return "1.5 µs after the last rising edge", eng(got), math.Abs(got-1.5e-6) <= 3*r.dt
			},
		},
		{
			typ: "dropout", form: "state low 1.5 µs, 2 µs lows", sigs: burst, chans: []int{0}, tdiv: 500e-9, spec: dropState, fires: true,
			check: func(r fireRec) (string, string, bool) {
				o, ok := r.before(0, 0, -1, r.at)
				if !ok {
					return "1.5 µs after the signal fell", "no falling edge before the trigger", false
				}
				got := (r.at - o) * r.dt
				return "1.5 µs after the signal fell", eng(got), math.Abs(got-1.5e-6) <= 3*r.dt
			},
		},
		{typ: "dropout", form: "edge 3.5 µs, 3 µs gaps", sigs: burst, chans: []int{0}, tdiv: 500e-9, spec: drop(3.5e-6)},

		{
			typ: "nth edge", form: "3rd rising after 1.5 µs idle", sigs: "C1=square,1e6,2,0,rise=4e-9,burst=5:5", chans: []int{0}, tdiv: 1e-6,
			spec: nth, fires: true, check: func(r fireRec) (string, string, bool) {
				_, g, ok := posCheck(0, 0, 1)(r)
				n, x := 1, r.at
				for {
					o, have := r.before(0, 0, 1, x)
					if !have || (x-o)*r.dt > 1.5e-6 {
						break
					}
					n, x = n+1, o
				}
				return "the 3rd rising edge of a burst", fmt.Sprintf("edge %d; %s", n, g), ok && n == 3
			},
		},

		{
			typ: "pattern", form: "C1 H & C2 L", sigs: "C1=square,1e6,2,0,rise=4e-9;C2=square,1e6,2,0,rise=4e-9,inv=1", chans: []int{0, 1}, tdiv: 200e-9,
			spec: pat(hal.Low), fires: true, check: func(r fireRec) (string, string, bool) {
				i := int(r.at + 2)
				st := fmt.Sprintf("C1 %+.2f V, C2 %+.2f V", r.v(0, i), r.v(1, i))
				_, g, ok := posCheck(0, 0, 1)(r)
				return "pattern true from the trigger (C1's rise)", st + "; " + g, ok && r.v(0, i) > 0 && r.v(1, i) < 0
			},
		},
		{
			typ: "pattern", form: "C1 H & C2 H, never both", sigs: "C1=square,1e6,2,0,rise=4e-9;C2=square,1e6,2,0,rise=4e-9,inv=1", chans: []int{0, 1},
			tdiv: 200e-9, spec: pat(hal.High),
		},

		{
			typ: "qualified", form: "C1 rise while C2 high", sigs: "C1=square,1e6,2,0,rise=4e-9;C2=square,5e5,2,0,rise=4e-9,duty=0.25,inv=1", chans: []int{0, 1},
			tdiv: 500e-9, spec: qual(true), fires: true, check: func(r fireRec) (string, string, bool) {
				_, g, ok := posCheck(0, 0, 1)(r)
				q := r.v(1, int(r.at))
				return "C1 rising at the trigger, C2 high", fmt.Sprintf("%s; C2 %+.2f V", g, q), ok && q > 0
			},
		},
		{
			typ: "qualified", form: "C1 rise while C2 high, C2 stays low", sigs: "C1=square,1e6,2,0,rise=4e-9;C2=dc,0,0,-1", chans: []int{0, 1},
			tdiv: 500e-9, spec: qual(true),
		},

		{
			typ: "setup/hold", form: "setup < 300 ns, data 200 ns early", sigs: "C1=square,1e6,2,0,rise=4e-9;C2=square,5e5,2,0,rise=4e-9,duty=0.9,inv=1",
			chans: []int{0, 1}, tdiv: 200e-9, spec: setup(300e-9), fires: true, check: func(r fireRec) (string, string, bool) {
				_, g, ok := posCheck(0, 0, 1)(r)
				d, have := r.before(1, 0, 1, r.at+1)
				got := (r.at - d) * r.dt
				return "clock rise 200 ns after the data rise", fmt.Sprintf("%s; data %s before", g, eng(got)), ok && have && math.Abs(got-200e-9) < 4*r.dt
			},
		},
		// hold: data (C2) falls 300 ns after the clock (C1) rises
		{
			typ: "setup/hold", form: "hold < 400 ns, data falls 300 ns late", sigs: "C1=square,1e6,2,0,rise=4e-9;C2=square,1e6,2,0,rise=4e-9,duty=0.3",
			chans: []int{0, 1}, tdiv: 200e-9, spec: hold(400e-9), fires: true, check: func(r fireRec) (string, string, bool) {
				_, g, ok := posCheck(1, 0, -1)(r)
				d, have := r.before(0, 0, 1, r.at)
				got := (r.at - d) * r.dt
				return "data falls 300 ns after the clock rise", fmt.Sprintf("%s; %s after", g, eng(got)), ok && have && math.Abs(got-300e-9) < 4*r.dt
			},
		},
		{
			typ: "setup/hold", form: "hold < 250 ns, data falls 300 ns late", sigs: "C1=square,1e6,2,0,rise=4e-9;C2=square,1e6,2,0,rise=4e-9,duty=0.3",
			chans: []int{0, 1}, tdiv: 200e-9, spec: hold(250e-9),
		},
		{
			typ: "setup/hold", form: "setup < 150 ns, data 200 ns early", sigs: "C1=square,1e6,2,0,rise=4e-9;C2=square,5e5,2,0,rise=4e-9,duty=0.9,inv=1",
			chans: []int{0, 1}, tdiv: 200e-9, spec: setup(150e-9),
		},

		{
			typ: "delay", form: "C2 rise, then C1 fall < 400 ns", sigs: "C1=square,1e6,2,0,rise=4e-9;C2=square,1e6,2,0,rise=4e-9,duty=0.3,inv=1",
			chans: []int{0, 1}, tdiv: 200e-9, spec: delay(400e-9), fires: true, check: func(r fireRec) (string, string, bool) {
				_, g, ok := posCheck(0, 0, -1)(r)
				d, have := r.before(1, 0, 1, r.at)
				got := (r.at - d) * r.dt
				return "C1 falls 200 ns after C2 rises", fmt.Sprintf("%s; %s after", g, eng(got)), ok && have && math.Abs(got-200e-9) < 4*r.dt
			},
		},
		{
			typ: "delay", form: "C2 rise, then C1 fall < 150 ns", sigs: "C1=square,1e6,2,0,rise=4e-9;C2=square,1e6,2,0,rise=4e-9,duty=0.3,inv=1",
			chans: []int{0, 1}, tdiv: 200e-9, spec: delay(150e-9),
		},

		{
			typ: "video", form: "NTSC any line, 4.7 µs syncs", sigs: "C1=pulse,15734.264,0.6,0,width=4.7e-6,rise=50e-9,inv=1", chans: []int{0}, tdiv: 2e-6,
			spec: video(), fires: true, check: widthCheck(0, -0.15, 1, -0.15, -1, 4.7e-6),
		},
		{
			typ: "video", form: "NTSC any line, 100 ns pulses", sigs: "C1=pulse,15734.264,0.6,0,width=100e-9,rise=4e-9,inv=1", chans: []int{0}, tdiv: 2e-6,
			spec: video(),
		},
	}
}

// serialCases are the protocol triggers on the generator's buses at 1 Mb/s:
// the trigger's distance from the frame's first edge is the analytic one.
func serialCases() []fireCase {
	line := func(c int) prototrig.Line { return prototrig.Line{Source: ch(c), Level: 0} }
	proto := func(p hal.ProtoTrigger) hal.TriggerSpec {
		return normal(hal.TrigStage{Node: hal.NodeProtocol, Proto: p})
	}
	const T = 1e-6
	// from the frame's first edge (after a gap of more than 8 bits) on channel c to the trigger
	fromFrame := func(c int, want float64, what string, alt ...float64) func(r fireRec) (string, string, bool) {
		return func(r fireRec) (string, string, bool) {
			x := r.at + 1 // an edge at the trigger itself counts
			first := -1.0
			for {
				o, ok := r.before(c, 0, 0, x)
				if !ok {
					break
				}
				first = o
				p, ok := r.before(c, 0, 0, o)
				if !ok || (o-p)*r.dt > 8*T {
					break
				}
				x = o
			}
			if first < 0 {
				return what, "no frame start before the trigger", false
			}
			got := (r.at - first) * r.dt
			ok := false
			for _, w := range append([]float64{want}, alt...) {
				ok = ok || math.Abs(got-w) <= 0.05*T+2*r.dt
			}
			return fmt.Sprintf("%s, %.1f bits after the frame start", what, want/T), fmt.Sprintf("%.2f bits", got/T), ok
		}
	}
	uart := func(cond prototrig.UARTCondition, v int) hal.TriggerSpec {
		return proto(prototrig.UART{Line: line(0), Condition: cond, Baud: 1e6, Bits: 8, StopBits: 1, IdleHigh: true, Value: v})
	}
	us := "C1=uart,1e6,2,0,data=55a3"
	i2c := func(cond prototrig.I2CCondition, addr int, rw prototrig.RW, data ...uint8) hal.TriggerSpec {
		return proto(prototrig.I2C{SCL: line(0), SDA: line(1), Condition: cond, Address: addr, RW: rw, Data: data})
	}
	is := "C1=i2c.scl,1e6,2,0,addr=0x50,data=12a5;C2=i2c.sda,1e6,2,0,addr=0x50,data=12a5"
	bits := func(v, n int) []hal.PatternState {
		var b []hal.PatternState
		for k := n - 1; k >= 0; k-- {
			b = append(b, map[int]hal.PatternState{0: hal.Low, 1: hal.High}[v>>k&1])
		}
		return b
	}
	spi := func(v, n int) hal.TriggerSpec {
		return proto(prototrig.SPI{
			Clock: line(0), Data: line(1), Select: line(2), SelectMode: prototrig.SelectActiveLow,
			MSBFirst: true, Bits: bits(v, n),
		})
	}
	ss := "C1=spi.clk,1e6,2,0,data=a53c;C2=spi.mosi,1e6,2,0,data=a53c;C3=spi.cs,1e6,2,0,data=a53c"
	can := func(id int) hal.TriggerSpec {
		return proto(prototrig.CAN{Line: line(0), Condition: prototrig.CANID, Baud: 1e6, ID: id})
	}
	lin := func(id int) hal.TriggerSpec {
		return proto(prototrig.LIN{Line: line(0), Condition: prototrig.LINID, Baud: 1e6, ID: id, Spec2: true})
	}
	return []fireCase{
		{
			typ: "uart", form: "start", sigs: us, chans: []int{0}, tdiv: 5e-6, spec: uart(prototrig.UARTStart, -1), fires: true,
			check: fromFrame(0, 0, "a start edge (the 1st or 2nd byte's)", 10*T),
		},
		{
			typ: "uart", form: "data 0xa3", sigs: us, chans: []int{0}, tdiv: 5e-6, spec: uart(prototrig.UARTData, 0xa3), fires: true,
			check: fromFrame(0, 19.5*T, "the 2nd byte's stop bit"),
		},
		{typ: "uart", form: "data 0xa4", sigs: us, chans: []int{0}, tdiv: 5e-6, spec: uart(prototrig.UARTData, 0xa4)},
		{
			typ: "i2c", form: "address 0x50 write", sigs: is, chans: []int{0, 1}, tdiv: 5e-6, spec: i2c(prototrig.I2CAddr7Data, 0x50, prototrig.Write),
			fires: true, check: fromFrame(0, 8.5*T, "the address ACK clock (from SCL's first fall)"),
		},
		{
			typ: "i2c", form: "address 0x50 write + 0x12", sigs: is, chans: []int{0, 1}, tdiv: 5e-6,
			spec: i2c(prototrig.I2CAddr7Data, 0x50, prototrig.Write, 0x12), fires: true, check: fromFrame(0, 17.5*T, "the data ACK clock (from SCL's first fall)"),
		},
		{typ: "i2c", form: "address 0x51", sigs: is, chans: []int{0, 1}, tdiv: 5e-6, spec: i2c(prototrig.I2CAddr7Data, 0x51, prototrig.Write)},
		{typ: "i2c", form: "address 0x50 read", sigs: is, chans: []int{0, 1}, tdiv: 5e-6, spec: i2c(prototrig.I2CAddr7Data, 0x50, prototrig.Read)},
		{
			typ: "spi", form: "8 bits 0xa5 MSB first", sigs: ss, chans: []int{0, 1, 2}, tdiv: 5e-6, spec: spi(0xa5, 8), fires: true,
			check: fromFrame(2, 8*T, "the 8th bit's latch (from CS)"),
		},
		{typ: "spi", form: "8 bits 0x5a", sigs: ss, chans: []int{0, 1, 2}, tdiv: 5e-6, spec: spi(0x5a, 8)},
		{
			typ: "can", form: "id 0x123", sigs: "C1=can,1e6,2,0,id=0x123,data=ab", chans: []int{0}, tdiv: 10e-6, spec: can(0x123), fires: true,
			check: fromFrame(0, 13.6*T, "the IDE bit's sample"),
		},
		{typ: "can", form: "id 0x124", sigs: "C1=can,1e6,2,0,id=0x123,data=ab", chans: []int{0}, tdiv: 10e-6, spec: can(0x124)},
		{
			typ: "lin", form: "id 0x22", sigs: "C1=lin,1e6,2,0,id=0x22,data=0102", chans: []int{0}, tdiv: 10e-6, spec: lin(0x22), fires: true,
			check: fromFrame(0, 20.5*T, "the PID's stop bit (from the end of the break)"),
		},
		{typ: "lin", form: "id 0x23", sigs: "C1=lin,1e6,2,0,id=0x22,data=0102", chans: []int{0}, tdiv: 10e-6, spec: lin(0x23)},
	}
}

// TestTriggerFireSerial runs the protocol triggers (separate: they need
// longer records).
func TestTriggerFireSerial(t *testing.T) {
	for _, fc := range serialCases() {
		runFire(t, fc)
	}
}

// row prints the table row of a case.
func row(typ, form, want, got string, ok bool) {
	v := "PASS"
	if !ok {
		v = "FAIL"
	}
	clean := func(s string) string { return strings.ReplaceAll(s, "|", "/") }
	fmt.Printf("L3ROW %s|%s|%s|%s|%s\n", clean(typ), clean(form), clean(want), clean(got), v)
}

// setUp switches on chans at 1 V/div, plans tdiv and programs spec.
func setUp(chans []int, tdiv float64, spec hal.TriggerSpec, c1 ...hal.Channel) error {
	n := board.Profile().Channels
	for c := range n {
		set := hal.Channel{On: slices.Contains(chans, c), VoltsDiv: 1, Probe: 1, Impedance: 1e6}
		if c == 0 && len(c1) > 0 {
			set = c1[0]
			set.On, set.Impedance = true, 1e6
		}
		if _, err := board.FrontEnd().Set(c, set); err != nil {
			return fmt.Errorf("C%d: %w", c+1, err)
		}
	}
	p, err := board.Planner().Plan(hal.Request{Channels: chans, TimePerDivS: tdiv})
	if err != nil {
		return fmt.Errorf("plan: %w", err)
	}
	if err := board.Acquirer().Configure(p); err != nil {
		return fmt.Errorf("configure: %w", err)
	}
	if err := board.Trigger().Program(spec); err != nil {
		return fmt.Errorf("trigger: %w", err)
	}
	return nil
}

// read reads the main window of chans, one window per channel when the
// board reads one channel per window.
func read(chans []int) (fireRec, error) {
	var ws [][]int
	if board.Readout().Limits().ChannelsPerWindow == 1 {
		for _, c := range chans {
			ws = append(ws, []int{c})
		}
	} else {
		ws = [][]int{chans}
	}
	req := hal.ReadRequest{}
	fs := make([]*sample.Frame, len(ws))
	for i, w := range ws {
		req.Windows = append(req.Windows, hal.Window{Role: hal.Main, Channels: w})
		fs[i] = &sample.Frame{}
	}
	if err := board.Readout().Read(context.Background(), req, fs, nil); err != nil {
		return fireRec{}, err
	}
	r := fireRec{f: fs[0], tr: map[int]*sample.Trace{}, dt: fs[0].SampleS, at: fs[0].TriggerAt}
	for _, f := range fs {
		if f.SampleS != r.dt || f.TriggerAt != r.at {
			return r, fmt.Errorf("windows disagree: %g s at %g, %g s at %g", r.dt, r.at, f.SampleS, f.TriggerAt)
		}
		for i := range f.Traces {
			r.tr[f.Traces[i].Ch] = &f.Traces[i]
		}
	}
	for _, c := range chans {
		if r.tr[c] == nil || r.tr[c].Valid < 16 {
			return r, fmt.Errorf("no trace of C%d", c+1)
		}
	}
	return r, nil
}

// waitFrame arms and waits up to d for a frame.
func waitFrame(d time.Duration) (hal.Status, error) {
	a := board.Acquirer()
	if a.State() == hal.Armed {
		_ = a.Stop()
	}
	if err := a.Arm(); err != nil {
		return hal.Status{}, fmt.Errorf("arm: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), d+2*time.Second)
	defer cancel()
	return a.Wait(ctx, d)
}

func runFire(t *testing.T, fc fireCase) {
	t.Helper()
	id := strings.NewReplacer(" ", "-", "/", "-", ",", "", "<", "lt", ">", "gt", "–", "-").Replace(fc.typ + "/" + fc.form)
	c := begin(t, "trigfire/"+id)
	platform.Mark("sim signals " + fc.sigs)
	want := "no trigger (Normal waits)"
	if fc.fires {
		want = "fires"
	}
	var c1 []hal.Channel
	if fc.c1 != nil {
		c1 = append(c1, *fc.c1)
	}
	if err := setUp(fc.chans, fc.tdiv, fc.spec, c1...); err != nil {
		c.check(false, "set up: %v", err)
		row(fc.typ, fc.form, want, "set up: "+err.Error(), false)
		return
	}
	defer func() { _ = board.Acquirer().Stop() }()
	st, err := waitFrame(1500 * time.Millisecond)
	if !fc.fires {
		ok := errors.Is(err, hal.ErrTimeout)
		got := "waiting after 1.5 s"
		if !ok {
			got = fmt.Sprintf("a frame (%v, %+v)", err, st)
		}
		c.check(ok, "Normal without the condition: %s", got)
		row(fc.typ, fc.form, want, got, ok)
		return
	}
	if err != nil || !st.Done {
		c.check(false, "no frame: %v %+v", err, st)
		row(fc.typ, fc.form, want, fmt.Sprintf("no frame (%v)", err), false)
		return
	}
	r, err := read(fc.chans)
	if err != nil {
		c.check(false, "read: %v", err)
		row(fc.typ, fc.form, want, "read: "+err.Error(), false)
		return
	}
	w, g, ok := fc.check(r)
	c.check(ok, "%s: %s (trigger at %.2f of %d, %s a sample)", w, g, r.at, r.n(fc.chans[0]), eng(r.dt))
	row(fc.typ, fc.form, w, g, ok)
}

// sweepCases checks the sweep modes, holdoff and force on an edge trigger.
func sweepCases(t *testing.T) {
	t.Helper()
	a := board.Acquirer()
	sine := "C1=sine,1e6,2,0"
	edge := func(sw hal.Sweep, lvl float64) hal.TriggerSpec {
		return hal.TriggerSpec{Sweep: sw, Stages: []hal.TrigStage{{Node: hal.NodeEdge, Source: ch(0), Levels: hal.Levels{High: lvl, Slope: hal.Rising}}}}
	}

	// Auto without the condition: frames keep coming, untriggered (the
	// crossing is not held at the trigger point)
	c := begin(t, "trigfire/sweep-auto-untriggered")
	platform.Mark("sim signals " + sine)
	if c.noErr(setUp([]int{0}, 200e-9, edge(hal.Auto, 1.5)), "auto, level above the signal") {
		locked, n := 0, 0
		var stats []string
		for range 4 {
			st, err := waitFrame(1500 * time.Millisecond)
			if err != nil {
				break
			}
			r, err := read([]int{0})
			if err != nil {
				break
			}
			n++
			stats = append(stats, fmt.Sprintf("trig=%v", st.Triggered))
			if _, _, ok := posCheck(0, 0.5, 1)(r); ok {
				locked++
			}
		}
		ok := n >= 3 && locked < n
		c.check(ok, "%d auto frames, %d with a 0.5 V rising crossing at the trigger point; board status %v", n, locked, stats)
		row("sweep", "auto, no condition", "frames, untriggered", fmt.Sprintf("%d frames, %d phase-locked; status %s", n, locked,
			strings.Join(stats, " ")), ok)
		trig := slices.Contains(stats, "trig=true")
		c.check(!trig, "the board reports auto frames as not triggered: %v", stats)
		row("sweep", "auto, no condition: status", "Triggered=false", strings.Join(stats, " "), !trig)
		_ = a.Stop()
	}

	// Auto with the condition: triggered frames
	c = begin(t, "trigfire/sweep-auto-triggered")
	if c.noErr(setUp([]int{0}, 200e-9, edge(hal.Auto, 0.5)), "auto, level on the signal") {
		st, err := waitFrame(1500 * time.Millisecond)
		r, rerr := read([]int{0})
		if err == nil && rerr == nil {
			w, g, ok := posCheck(0, 0.5, 1)(r)
			c.check(ok && st.Triggered, "%s: %s (%+v)", w, g, st)
			row("sweep", "auto, condition met", w, g, ok && st.Triggered)
		} else {
			c.check(false, "no frame: %v %v", err, rerr)
			row("sweep", "auto, condition met", "fires", fmt.Sprintf("%v %v", err, rerr), false)
		}
		_ = a.Stop()
	}

	// Single: one frame, then nothing armed
	c = begin(t, "trigfire/sweep-single")
	if c.noErr(setUp([]int{0}, 200e-9, edge(hal.Single, 0.5)), "single") {
		st, err := waitFrame(1500 * time.Millisecond)
		_, werr := a.Wait(context.Background(), 300*time.Millisecond)
		ok := err == nil && st.Done && werr != nil && a.State() != hal.Armed
		c.check(ok, "one frame (%v), then no second (%v), state %v", err, werr, a.State())
		row("sweep", "single", "one frame, then stopped", fmt.Sprintf("frame %v; next wait %v", err == nil, werr), ok)
		_ = a.Stop()
	}

	// Force in Normal without the condition: a forced frame
	c = begin(t, "trigfire/sweep-force")
	if c.noErr(setUp([]int{0}, 200e-9, edge(hal.NormalSweep, 1.5)), "normal, level above the signal") {
		_ = a.Arm()
		_, err := a.Wait(context.Background(), 300*time.Millisecond)
		waiting := errors.Is(err, hal.ErrTimeout)
		ferr := a.Force()
		st, err := a.Wait(context.Background(), 1500*time.Millisecond)
		ok := waiting && ferr == nil && err == nil && st.Forced
		c.check(ok, "waiting %v, force %v, frame %v %+v", waiting, ferr, err, st)
		row("sweep", "force in normal, no condition", "waits, then one forced frame", fmt.Sprintf("waiting %v; forced %v (%v)", waiting, st.Forced, err), ok)
		_ = a.Stop()
	}

	// Holdoff 0.4 s: triggers at least 0.4 s apart
	c = begin(t, "trigfire/sweep-holdoff")
	ho := edge(hal.NormalSweep, 0.5)
	ho.HoldoffS = 0.4
	if c.noErr(setUp([]int{0}, 200e-9, ho), "normal, holdoff 0.4 s") {
		var gaps []time.Duration
		_, _ = waitFrame(1500 * time.Millisecond)
		last := time.Now()
		for range 2 {
			if _, err := waitFrame(1500 * time.Millisecond); err != nil {
				break
			}
			gaps = append(gaps, time.Since(last))
			last = time.Now()
		}
		ok := len(gaps) == 2 && gaps[0] > 350*time.Millisecond && gaps[1] > 350*time.Millisecond
		c.check(ok, "frame gaps %v with holdoff 0.4 s", gaps)
		row("holdoff", "0.4 s, edge", "frames about 0.4 s apart (wall gaps > 0.35 s)", fmt.Sprint(gaps), ok)
		_ = a.Stop()
	}
}
