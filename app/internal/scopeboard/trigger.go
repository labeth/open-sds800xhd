package scopeboard

import (
	"errors"
	"fmt"
	"math"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-dso-core/hal/prototrig"
	"github.com/labeth/open-sds800xhd/app/internal/trigger"
)

type triggerFacet Board

// Time ranges of the trigger (spec 05 §5.3, §5.5, §6).
const (
	minTimeS    = 2e-9
	maxTimeS    = 20
	minIdleS    = 8e-9
	minHoldoffS = 8e-9
	maxHoldoffS = 30
)

var (
	allQualOps = []hal.QualOp{hal.Shorter, hal.Longer, hal.Inside, hal.Outside}
	twoSlopes  = []hal.Slope{hal.Rising, hal.Falling}
)

// trigCaps is what the vendor bitstream's trigger engine implements (spec
// 05 §1, §10). Window and video take no slope; a protocol node takes the
// protocols listed.
var trigCaps = hal.TrigCaps{
	Nodes: map[hal.TrigNode]hal.NodeCaps{
		hal.NodeEdge: {
			Slopes: []hal.Slope{hal.Rising, hal.Falling, hal.Either}, QualOps: allQualOps,
			MinS: minTimeS, MaxS: maxTimeS, Broken: []string{"interval either", "qualified either", "nth edge either with idle time"},
			Gate: true, MaxCount: 65535,
		},
		hal.NodePulse:     {Slopes: twoSlopes, QualOps: allQualOps, MinS: minTimeS, MaxS: maxTimeS},
		hal.NodeSlope:     {Slopes: twoSlopes, QualOps: allQualOps, MinS: minTimeS, MaxS: maxTimeS},
		hal.NodeRunt:      {Slopes: twoSlopes, QualOps: allQualOps, MinS: minTimeS, MaxS: maxTimeS},
		hal.NodeWindow:    {},
		hal.NodePattern:   {QualOps: allQualOps, MinS: minTimeS, MaxS: maxTimeS},
		hal.NodeSetupHold: {Slopes: twoSlopes, QualOps: allQualOps, MinS: minTimeS, MaxS: maxTimeS},
		hal.NodeTimeout:   {Slopes: twoSlopes, QualOps: []hal.QualOp{hal.Longer}, MinS: minTimeS, MaxS: maxTimeS},
		hal.NodeVideo:     {},
		hal.NodeProtocol:  {},
	},
	MaxStages: 2,
	Protocols: []string{"i2c", "spi", "uart", "can", "lin"},
	Sources:   []hal.SourceKind{hal.Analog, hal.ACLine},
}

// Program stages s, converted to the trigger engine's settings, or refuses
// it with the reason. Levels are converted at the commit with the front end
// staged before it.
func (t *triggerFacet) Program(s hal.TriggerSpec) error {
	b := (*Board)(t)
	ts, err := b.triggerSettings(s)
	if err != nil {
		return err
	}
	b.staged.Trig = ts
	return nil
}

func (t *triggerFacet) Caps() hal.TrigCaps {
	if b := (*Board)(t); b.open != nil {
		return openTrigCaps(b.open.pl)
	}
	return trigCaps
}

// defaultPx are the factory-default display levels L_disp in screen pixels
// (60 per division) of the nodes whose default is not 0 (spec 05 §3.2):
// Slope and Runt level 1 −3 px, Window level 1 (absolute low) −50 px. They
// are pixels for every channel, so their volts follow the source's V/div.
var defaultPx = map[hal.TrigNode]int{hal.NodeSlope: -3, hal.NodeRunt: -3, hal.NodeWindow: -50}

// syncTipV is the video sync-tip default before the pixel truncation (spec
// 05 §5.6).
const syncTipV = -0.150

// Default returns node's default stage on src (spec 05 §3.2, §5.6). The
// Video default is the sync-tip level L_disp = trunc(−150 mV · 60 /
// V/div) px, rising, NTSC line 1 field 1 on every line's sync (the SCPI
// defaults of spec 11): it applies on the first entry
// into Video after start-up when no Video level has been set since, and
// never over an explicit level. Which entry is the first is the settings
// layer's to know; the value is the board's.
func (t *triggerFacet) Default(node hal.TrigNode, src hal.Source) (hal.TrigStage, error) {
	b := (*Board)(t)
	var vdiv float64
	switch {
	case src.Kind == hal.ACLine:
		vdiv = 0.5
	case src.Kind == hal.Analog && src.Index >= 0 && src.Index < b.id.Channels:
		vdiv = b.staged.Ch[src.Index].VoltsDiv
	default:
		return hal.TrigStage{}, hal.Refuse("trigger default", "the source is not an analog channel of this model")
	}
	volts := func(px int) float64 { return float64(px) * vdiv / 60 }
	st := hal.TrigStage{Node: node, Source: src, Levels: hal.Levels{Slope: hal.Rising}}
	switch node { //nolint:exhaustive // default: the other nodes have no single-source default and are refused
	case hal.NodeEdge, hal.NodePulse, hal.NodeTimeout:
	case hal.NodeSlope, hal.NodeRunt, hal.NodeWindow:
		st.Levels.Low = volts(defaultPx[node])
	case hal.NodeVideo:
		if src.Kind != hal.Analog {
			return hal.TrigStage{}, hal.Refuse("video trigger", "the video trigger takes an analog channel")
		}
		st.Levels.High = volts(int(math.Trunc(math.Round(syncTipV*60/vdiv*1e9) / 1e9)))
		st.Video = &hal.Video{Standard: "ntsc", Line: 1, Field: 1, AnyLine: true}
	default:
		return hal.TrigStage{}, hal.Refuse("trigger default", fmt.Sprintf("the %v node has no single-source default", node))
	}
	return st, nil
}

// LevelGrid: one comparator step is 1/8 of a screen row, 480 steps per
// division of the source's displayed V/div, over ±4.1 divisions about the
// screen centre (spec 05 §3.2).
func (t *triggerFacet) LevelGrid(src hal.Source) hal.Grid {
	b := (*Board)(t)
	switch {
	case src.Kind == hal.ACLine:
		return hal.Grid{Step: 0.5 / 480, Min: -4.1 * 0.5, Max: 4.1 * 0.5}
	case src.Kind != hal.Analog || src.Index < 0 || src.Index >= b.id.Channels:
		return hal.Grid{}
	}
	c := b.staged.Ch[src.Index]
	v := c.VoltsDiv
	return hal.Grid{Step: v / 480, Min: -4.1*v - c.OffsetV, Max: 4.1*v - c.OffsetV}
}

func (t *triggerFacet) Events([]hal.Event) (int, error) {
	return 0, hal.Refuse("trigger events", "the vendor bitstream has no decoded-event queue")
}

func (t *triggerFacet) Matches() (hal.MatchStats, bool) { return hal.MatchStats{}, false }

// ---------------------------------------------------------------- conversion

// triggerSettings converts a HAL trigger to the engine's settings, refusing
// what the vendor bitstream cannot do. Nothing is substituted.
func (b *Board) triggerSettings(s hal.TriggerSpec) (trigger.Settings, error) {
	ts := trigger.Default()
	if s.Reset != nil {
		return ts, hal.Refuse("trigger reset", "the trigger engine has no reset stage")
	}
	if s.HoldoffEv != 0 {
		return ts, hal.Refuse("holdoff by events", "this trigger engine has only time holdoff")
	}
	if s.FreeRun {
		return ts, hal.Refuse("free-running trigger", "the trigger engine arms what it is given; AUTO runs it free")
	}
	if s.HoldoffS != 0 && (s.HoldoffS < minHoldoffS || s.HoldoffS > maxHoldoffS) {
		return ts, hal.Refuse("holdoff", fmt.Sprintf("%g s is outside 8 ns … 30 s", s.HoldoffS))
	}
	ts.HoldoffS, ts.NoiseReject = s.HoldoffS, s.NoiseReject
	switch s.Sweep {
	case hal.Auto:
		ts.Sweep = trigger.Auto
	case hal.NormalSweep:
		ts.Sweep = trigger.Normal
	case hal.Single:
		ts.Sweep = trigger.Single
	default:
		return ts, hal.Refuse("sweep", "not a sweep mode of this scope")
	}
	switch s.Coupling {
	case hal.TrigDC:
		ts.Coupling = trigger.CoupDC
	case hal.TrigAC:
		ts.Coupling = trigger.CoupAC
	case hal.TrigLFReject:
		ts.Coupling = trigger.CoupLFReject
	case hal.TrigHFReject:
		ts.Coupling = trigger.CoupHFReject
	default:
		return ts, hal.Refuse("trigger coupling", "not a coupling of this scope")
	}
	var err error
	switch len(s.Stages) {
	case 1:
		err = b.oneStage(&ts, s.Stages[0])
	case 2:
		err = b.delayStages(&ts, s.Stages[0], s.Stages[1])
	case 0:
		return ts, hal.Refuse("trigger", "no trigger stage")
	default:
		return ts, hal.Refuse("trigger sequence", "the trigger engine runs at most two stages (a pattern, then an edge)")
	}
	if err != nil {
		return ts, err
	}
	if trigger.ForcesHoldoffOff(ts.Type) && ts.HoldoffS != 0 {
		return ts, hal.Refuse("holdoff", fmt.Sprintf("the %v trigger runs with holdoff off", ts.Type))
	}
	if trigger.ForcesDC(ts.Type) && ts.Coupling != trigger.CoupDC {
		return ts, hal.Refuse("trigger coupling", fmt.Sprintf("the %v trigger runs with DC coupling", ts.Type))
	}
	if trigger.ForcesDC(ts.Type) && ts.NoiseReject && ts.Type != trigger.Pattern && ts.Type != trigger.Serial {
		return ts, hal.Refuse("noise reject", fmt.Sprintf("the %v trigger always uses the normal hysteresis", ts.Type))
	}
	return ts, nil
}

// channel checks an analog or AC-line source and returns its index.
func (b *Board) channel(src hal.Source, acLine bool) (int, error) {
	switch src.Kind {
	case hal.Analog:
		if src.Index < 0 || src.Index >= b.id.Channels {
			return 0, hal.Refuse("trigger source", fmt.Sprintf("C%d does not exist on this model", src.Index+1))
		}
		if !b.staged.Ch[src.Index].On {
			return 0, hal.Refuse("trigger source", fmt.Sprintf("C%d is off: only displayed channels trigger", src.Index+1))
		}
		return src.Index, nil
	case hal.ACLine:
		if acLine {
			return trigger.SourceACLine, nil
		}
		return 0, hal.Refuse("trigger source", "the AC line is a source of single-input triggers only")
	case hal.Digital:
		return 0, hal.Refuse("trigger source", "logic channels trigger in the SLA1016 probe, which this firmware does not drive")
	case hal.External:
		return 0, hal.Refuse("trigger source", "this model has no external trigger input")
	}
	return 0, hal.Refuse("trigger source", "unknown source")
}

func dir(s hal.Slope, either bool, what string) (trigger.Dir, error) {
	switch s {
	case hal.Rising, 0:
		return trigger.Rising, nil
	case hal.Falling:
		return trigger.Falling, nil
	case hal.Either:
		if either {
			return trigger.Alternate, nil
		}
	}
	return 0, hal.Refuse("trigger slope", what+" fires on a rising or a falling edge only")
}

// limit converts a time condition. needed: a missing one is refused.
func limit(ts *trigger.Settings, q *hal.Qualifier, what string) error {
	if q == nil {
		return hal.Refuse(what, "needs a time condition")
	}
	in := func(v float64) bool { return v >= minTimeS && v <= maxTimeS }
	switch q.Op {
	case hal.Shorter:
		if !in(q.MaxS) {
			return hal.Refuse(what, fmt.Sprintf("%g s is outside 2 ns … 20 s", q.MaxS))
		}
		ts.Limit, ts.UpperS = trigger.Less, q.MaxS
	case hal.Longer:
		if !in(q.MinS) {
			return hal.Refuse(what, fmt.Sprintf("%g s is outside 2 ns … 20 s", q.MinS))
		}
		ts.Limit, ts.LowerS = trigger.Greater, q.MinS
	case hal.Inside, hal.Outside:
		if !in(q.MinS) || !in(q.MaxS) {
			return hal.Refuse(what, "times outside 2 ns … 20 s")
		}
		if trigger.NS(q.MinS) >= trigger.NS(q.MaxS) {
			return hal.Refuse(what, "the lower time is not below the upper time")
		}
		ts.Limit, ts.LowerS, ts.UpperS = trigger.Inside, q.MinS, q.MaxS
		if q.Op == hal.Outside {
			ts.Limit = trigger.Outside
		}
	default:
		return hal.Refuse(what, "unknown time condition")
	}
	return nil
}

// configOf checks that a stage carries only the node configurations its
// node takes: one meant for another node is refused, not ignored.
func configOf(st hal.TrigStage) error {
	extra := func(what string) error {
		return hal.Refuse("trigger "+st.Node.String(), "the node takes no "+what)
	}
	switch {
	case st.Gate != nil && st.Node != hal.NodeEdge && st.Node != hal.NodeSetupHold:
		return extra("gate input")
	case st.Pattern != nil && st.Node != hal.NodePattern:
		return extra("pattern")
	case st.Video != nil && st.Node != hal.NodeVideo:
		return extra("video standard")
	case st.Timeout != hal.TimeoutEdge && st.Node != hal.NodeTimeout:
		return extra("timeout form")
	case st.Proto != nil && st.Node != hal.NodeProtocol && st.Node != hal.NodeSymbol &&
		st.Node != hal.NodeSequence && st.Node != hal.NodeFrameStart:
		return extra("protocol configuration")
	case len(st.Zones) != 0:
		return hal.Refuse("zone trigger", "the trigger engine has no zone qualifier")
	case st.Video != nil && st.Video.Positive:
		return hal.Refuse("positive video sync", "the video trigger has no sync polarity setting")
	}
	return nil
}

// noOp is st without a time condition whose operator is zero: the HAL's
// "no condition, limits kept".
func noOp(st hal.TrigStage) hal.TrigStage {
	if st.Qual != nil && st.Qual.Op == 0 {
		st.Qual = nil
	}
	return st
}

// oneStage converts a single-stage trigger.
func (b *Board) oneStage(ts *trigger.Settings, st hal.TrigStage) error {
	st = noOp(st)
	if err := configOf(st); err != nil {
		return err
	}
	if st.DelayS != 0 {
		return hal.Refuse("trigger delay", "the trigger engine has no delay stage; the horizontal position moves the record")
	}
	if st.Node != hal.NodeEdge && st.Count > 1 {
		return hal.Refuse("event count", "only the edge trigger counts edges")
	}
	var err error
	single := func(acLine bool) error {
		ts.Source, err = b.channel(st.Source, acLine)
		ts.Level = st.Levels.High
		return err
	}
	switch st.Node {
	case hal.NodeEdge:
		return b.edgeStage(ts, st)
	case hal.NodePulse, hal.NodeSlope, hal.NodeRunt:
		ts.Type = map[hal.TrigNode]trigger.Type{hal.NodePulse: trigger.Pulse, hal.NodeSlope: trigger.Slope, hal.NodeRunt: trigger.Runt}[st.Node]
		if err := single(false); err != nil {
			return err
		}
		if ts.Slope, err = dir(st.Levels.Slope, false, ts.Type.String()); err != nil {
			return err
		}
		if st.Node != hal.NodePulse {
			ts.Level, ts.Level2 = st.Levels.Low, st.Levels.High
			if ts.Level2 <= ts.Level {
				return hal.Refuse("trigger levels", "the high level is not above the low level")
			}
		}
		return limit(ts, st.Qual, ts.Type.String()+" time")
	case hal.NodeWindow:
		ts.Type = trigger.Window
		if err := single(false); err != nil {
			return err
		}
		ts.Level, ts.Level2 = st.Levels.Low, st.Levels.High
		if ts.Level2 <= ts.Level {
			return hal.Refuse("trigger levels", "the high level is not above the low level")
		}
		if st.Qual != nil {
			return hal.Refuse("window time", "the window trigger has no time condition")
		}
		return nil
	case hal.NodeTimeout:
		ts.Type = trigger.Dropout
		if err := single(false); err != nil {
			return err
		}
		if ts.Slope, err = dir(st.Levels.Slope, false, "dropout"); err != nil {
			return err
		}
		q := st.Qual
		if q == nil || q.Op != hal.Longer {
			return hal.Refuse("dropout time", "a dropout fires after the input stays quiet longer than a time")
		}
		if q.MinS < minTimeS || q.MinS > maxTimeS {
			return hal.Refuse("dropout time", fmt.Sprintf("%g s is outside 2 ns … 20 s", q.MinS))
		}
		switch st.Timeout {
		case hal.TimeoutEdge, hal.TimeoutState:
		default:
			return hal.Refuse("dropout", "unknown timeout form")
		}
		ts.TimeoutS, ts.DropoutEdge = q.MinS, st.Timeout == hal.TimeoutEdge
		return nil
	case hal.NodeVideo:
		ts.Type = trigger.Video
		if err := single(false); err != nil {
			return err
		}
		if st.Video == nil {
			return hal.Refuse("video trigger", "no video standard given")
		}
		ts.Video, err = videoSettings(st.Video)
		return err
	case hal.NodePattern:
		ts.Type = trigger.Pattern
		if err := b.patternSettings(&ts.Pattern, st.Pattern); err != nil {
			return err
		}
		if st.Qual == nil {
			ts.Limit, ts.LowerS = trigger.Greater, minTimeS
			return nil
		}
		return limit(ts, st.Qual, "pattern time")
	case hal.NodeSetupHold:
		ts.Type = trigger.SetupHold
		if err := single(false); err != nil {
			return err
		}
		if ts.Slope, err = dir(st.Levels.Slope, false, "the setup/hold clock"); err != nil {
			return err
		}
		if st.Gate == nil {
			return hal.Refuse("setup/hold", "no data input given")
		}
		data, err := b.channel(st.Gate.Source, false)
		if err != nil {
			return err
		}
		if data == ts.Source {
			return hal.Refuse("setup/hold", "clock and data are the same channel")
		}
		ts.SetupHold = trigger.SetupHoldSet{Hold: st.Gate.Hold, Data: data, DataLevel: st.Gate.Level, DataHigh: st.Gate.High}
		return limit(ts, st.Qual, "setup/hold time")
	case hal.NodeProtocol:
		ts.Type = trigger.Serial
		return b.serialSettings(&ts.Serial, st.Proto)
	case hal.NodeZone:
		return hal.Refuse("zone trigger", "the vendor bitstream has no zone trigger")
	case hal.NodeSymbol, hal.NodeSequence, hal.NodeFrameStart:
		return hal.Refuse("trigger "+st.Node.String(), "the vendor bitstream has no generic symbol receiver; serial triggers are I2C, SPI, UART, CAN and LIN")
	}
	return hal.Refuse("trigger "+st.Node.String(), "not a trigger of this scope")
}

// edgeStage is the edge node and its forms: edge, interval, Nth edge,
// qualified.
func (b *Board) edgeStage(ts *trigger.Settings, st hal.TrigStage) error {
	var err error
	acLine := st.Gate == nil && st.Qual == nil && st.Count <= 1
	if ts.Source, err = b.channel(st.Source, acLine); err != nil {
		return err
	}
	ts.Level = st.Levels.High
	switch {
	case st.Gate != nil:
		ts.Type = trigger.Qualified
		if st.Count > 1 {
			return hal.Refuse("qualified trigger", "a qualified edge is not counted")
		}
		if ts.Slope, err = dir(st.Levels.Slope, false, "a qualified edge"); err != nil {
			return err
		}
		q, err := b.channel(st.Gate.Source, false)
		if err != nil {
			return err
		}
		if q == ts.Source {
			return hal.Refuse("qualified trigger", "the qualifier and the edge are the same channel")
		}
		ts.Level2 = st.Levels.High
		ts.Qual = trigger.QualSet{Source: q, Level: st.Gate.Level, High: st.Gate.High}
		switch {
		case st.Gate.Edge && st.Qual != nil:
			ts.Qual.Mode = trigger.QualEdgeDelay
		case st.Gate.Edge:
			ts.Qual.Mode = trigger.QualEdge
		case st.Qual != nil:
			ts.Qual.Mode = trigger.QualStateDelay
		default:
			ts.Qual.Mode = trigger.QualState
		}
		if st.Qual == nil {
			ts.Limit, ts.UpperS, ts.LowerS = trigger.Less, minTimeS, minTimeS
			return nil
		}
		return limit(ts, st.Qual, "qualified delay")
	case st.Count > 1:
		ts.Type = trigger.NthEdge
		if st.Count > 65535 {
			return hal.Refuse("edge count", "at most 65535 edges")
		}
		ts.Count = st.Count
		if ts.Slope, err = dir(st.Levels.Slope, true, "the Nth edge"); err != nil {
			return err
		}
		ts.IdleS = minIdleS
		if q := st.Qual; q != nil {
			if q.Op != hal.Longer {
				return hal.Refuse("idle time", "the Nth edge trigger's condition is an idle time longer than a time")
			}
			if q.MinS < minIdleS || q.MinS > maxTimeS {
				return hal.Refuse("idle time", fmt.Sprintf("%g s is outside 8 ns … 20 s", q.MinS))
			}
			ts.IdleS = q.MinS
		}
		return nil
	case st.Qual != nil:
		ts.Type = trigger.Interval
		if ts.Slope, err = dir(st.Levels.Slope, false, "an interval"); err != nil {
			return err
		}
		return limit(ts, st.Qual, "interval")
	}
	ts.Type = trigger.Edge
	ts.Slope, err = dir(st.Levels.Slope, true, "")
	return err
}

// delayStages is the two-stage form: a pattern (event A), then an edge
// (event B) within the second stage's time condition.
func (b *Board) delayStages(ts *trigger.Settings, a, e hal.TrigStage) error {
	a, e = noOp(a), noOp(e)
	if err := errors.Join(configOf(a), configOf(e)); err != nil {
		return err
	}
	if a.Node != hal.NodePattern || e.Node != hal.NodeEdge || e.Gate != nil || e.Count > 1 {
		return hal.Refuse("trigger sequence", "the only two-stage trigger is a pattern followed by an edge (delay)")
	}
	if a.DelayS != 0 || e.DelayS != 0 {
		return hal.Refuse("trigger delay", "the sequence's delay is a time condition on the edge stage")
	}
	ts.Type = trigger.Delay
	if err := b.patternSettings(&ts.Pattern, a.Pattern); err != nil {
		return err
	}
	if a.Pattern.Logic != hal.PatternAnd {
		return hal.Refuse("delay pattern", "event A's pattern is an AND of the channel states")
	}
	switch a.Levels.Slope { //nolint:exhaustive // default: event A cannot fire on either edge
	case hal.Rising, 0:
		ts.DelayARise = true
	case hal.Falling:
	default:
		return hal.Refuse("delay pattern", "event A fires as the pattern becomes true or false, not on either")
	}
	var err error
	if ts.Source, err = b.channel(e.Source, false); err != nil {
		return err
	}
	ts.Level2 = e.Levels.High
	if ts.Slope, err = dir(e.Levels.Slope, false, "the delay trigger's edge"); err != nil {
		return err
	}
	return limit(ts, e.Qual, "delay time")
}

func (b *Board) patternSettings(p *trigger.PatternSet, hp *hal.Pattern) error {
	if hp == nil {
		return hal.Refuse("pattern trigger", "no pattern given")
	}
	*p = trigger.PatternSet{States: [4]trigger.State{trigger.DontCare, trigger.DontCare, trigger.DontCare, trigger.DontCare}}
	seen := 0
	for _, t := range hp.Terms {
		if t.State == hal.DontCare {
			continue
		}
		c, err := b.channel(t.Source, false)
		if err != nil {
			return err
		}
		if seen&(1<<c) != 0 {
			return hal.Refuse("pattern trigger", fmt.Sprintf("C%d is named twice", c+1))
		}
		seen |= 1 << c
		switch t.State { //nolint:exhaustive // default: a pattern term is low or high; any other state is refused
		case hal.Low:
			p.States[c] = trigger.Low
		case hal.High:
			p.States[c] = trigger.High
		default:
			return hal.Refuse("pattern trigger", "unknown state")
		}
		p.Levels[c] = t.Level
	}
	if seen == 0 {
		return hal.Refuse("pattern trigger", "every channel is don't care")
	}
	switch hp.Logic {
	case hal.PatternAnd:
		p.Logic = trigger.And
	case hal.PatternOr:
		p.Logic = trigger.Or
	case hal.PatternNand:
		p.Logic = trigger.Nand
	case hal.PatternNor:
		p.Logic = trigger.Nor
	default:
		return hal.Refuse("pattern trigger", "unknown logic")
	}
	return nil
}

var videoStandards = map[string]trigger.Standard{
	"ntsc": trigger.NTSC, "pal": trigger.PAL, "720p50": trigger.P720L50, "720p60": trigger.P720L60,
	"1080p50": trigger.P1080L50, "1080p60": trigger.P1080L60, "1080i50": trigger.I1080L50,
	"1080i60": trigger.I1080L60, "custom": trigger.Custom,
}

func videoSettings(v *hal.Video) (trigger.VideoSet, error) {
	std, ok := videoStandards[v.Standard]
	if !ok {
		return trigger.VideoSet{}, hal.Refuse("video standard", fmt.Sprintf("%q is not a standard of this scope", v.Standard))
	}
	out := trigger.VideoSet{Standard: std, Line: max(1, v.Line), Field: max(1, v.Field), AnyLine: v.AnyLine}
	if std == trigger.Custom {
		rates := map[float64]int{25: 0, 30: 1, 50: 2, 60: 3}
		r, ok := rates[v.FrameHz]
		if !ok {
			return out, hal.Refuse("video frame rate", "25, 30, 50 or 60 Hz")
		}
		for _, n := range []int{v.Interlace, v.Fields} {
			if n != 1 && n != 2 && n != 4 && n != 8 {
				return out, hal.Refuse("video interlace", "1, 2, 4 or 8")
			}
		}
		if v.Lines < 1 {
			return out, hal.Refuse("video lines", "no line count")
		}
		out.Rate, out.Lines, out.Interlace, out.Fields = r, v.Lines, v.Interlace, v.Fields
	}
	return out, nil
}

// line converts a bus line.
func (b *Board) line(l prototrig.Line) (trigger.Line, error) {
	c, err := b.channel(l.Source, false)
	return trigger.Line{Source: c, Level: l.Level}, err
}

func bytesOf(d []uint8) (trigger.Bytes, error) {
	out := trigger.NoBytes
	if len(d) > 2 {
		return out, hal.Refuse("trigger data", "at most two data bytes")
	}
	for i, v := range d {
		out[i] = int(v)
	}
	return out, nil
}

func baud(v float64, what string) (int, error) {
	if v <= 0 || v > 20e6 || v != math.Trunc(v) {
		return 0, hal.Refuse(what+" bit rate", fmt.Sprintf("%g bit/s is not a bit rate the decoder takes", v))
	}
	return int(v), nil
}

// serialSettings converts a protocol trigger. The protocols without
// trigger hardware (spec 05 §10.7) are refused: they are decode-only here.
func (b *Board) serialSettings(x *trigger.SerialSet, p hal.ProtoTrigger) error {
	var err error
	switch c := p.(type) {
	case prototrig.I2C:
		x.Protocol = trigger.I2C
		return b.i2cSettings(&x.I2C, c)
	case prototrig.SPI:
		x.Protocol = trigger.SPI
		return b.spiSettings(&x.SPI, c)
	case prototrig.UART:
		x.Protocol = trigger.UART
		u := &x.UART
		if u.Line, err = b.line(c.Line); err != nil {
			return err
		}
		if u.Baud, err = baud(c.Baud, "UART"); err != nil {
			return err
		}
		if c.Bits < 5 || c.Bits > 9 {
			return hal.Refuse("UART data bits", "5 … 9")
		}
		stops := map[float64]int{1: 0, 1.5: 1, 2: 2}
		st, ok := stops[c.StopBits]
		if !ok {
			return hal.Refuse("UART stop bits", "1, 1.5 or 2")
		}
		u.Condition, u.MSBFirst, u.Compare, u.Bits, u.Parity, u.Stop, u.IdleHigh =
			int(c.Condition), c.MSBFirst, int(c.Compare), c.Bits, int(c.Parity), st, c.IdleHigh
		u.Value, _, _ = trigger.UARTValue(c.Value, c.Bits)
	case prototrig.CAN:
		x.Protocol = trigger.CAN
		if c.Condition == prototrig.CANError {
			return hal.Refuse("CAN error trigger", "the decoder has no error trigger hardware")
		}
		return b.canSettings(&x.CAN, c)
	case prototrig.LIN:
		x.Protocol = trigger.LIN
		l := &x.LIN
		if l.Line, err = b.line(c.Line); err != nil {
			return err
		}
		if l.Baud, err = baud(c.Baud, "LIN"); err != nil {
			return err
		}
		if l.Data, err = bytesOf(c.Data); err != nil {
			return err
		}
		l.Condition, l.ID = int(c.Condition), c.ID
		if c.ID < 0 {
			l.ID = 0x40
		}
		l.ChecksumError, l.SyncError, l.ParityError, l.Spec2 = c.ChecksumError, c.SyncError, c.ParityError, c.Spec2
		l.ErrorID, l.ErrorLength = c.ErrorID, c.ErrorLength
	case nil:
		return hal.Refuse("serial trigger", "no protocol given")
	default:
		return hal.Refuse(p.Protocol()+" trigger", "no trigger hardware for this protocol in the vendor bitstream: decode only")
	}
	return nil
}

// i2cSettings converts an I2C trigger.
func (b *Board) i2cSettings(i *trigger.I2CSet, c prototrig.I2C) error {
	var err error
	if i.SCL, err = b.line(c.SCL); err != nil {
		return err
	}
	if i.SDA, err = b.line(c.SDA); err != nil {
		return err
	}
	i.Condition = int(c.Condition)
	if i.Data, err = bytesOf(c.Data); err != nil {
		return err
	}
	i.Address, i.RW, i.Compare = c.Address, int(c.RW), int(c.Compare)
	switch {
	case c.Condition == prototrig.I2CAddr10Data && c.Address < 0:
		i.Address = 0x400
	case c.Address < 0:
		i.Address = 0x80
	case c.Condition == prototrig.I2CAddr7Data && c.Address > 0x7F,
		c.Condition == prototrig.I2CAddr10Data && c.Address > 0x3FF:
		return hal.Refuse("I2C address", "wider than the address")
	}
	if c.Condition == prototrig.I2CDataLength && (c.DataLength < 1 || c.DataLength > 12) {
		return hal.Refuse("I2C data length", "1 … 12 bytes")
	}
	i.DataLength, i.Addr10 = c.DataLength, c.Addr10
	return nil
}

// spiSettings converts an SPI trigger.
func (b *Board) spiSettings(s *trigger.SPISet, c prototrig.SPI) error {
	var err error
	if s.Clock, err = b.line(c.Clock); err != nil {
		return err
	}
	if s.Data, err = b.line(c.Data); err != nil {
		return err
	}
	s.Select = int(c.SelectMode)
	if c.SelectMode != prototrig.SelectTimeout {
		if s.CS, err = b.line(c.Select); err != nil {
			return err
		}
	} else if c.IdleS < 8e-9 || trigger.NS(c.IdleS)>>3 > 0xFFFFF {
		return hal.Refuse("SPI idle time", "8 ns … 8.39 ms")
	}
	if len(c.Bits) == 0 || len(c.Bits) > 96 {
		return hal.Refuse("SPI pattern", "1 … 96 bits")
	}
	s.IdleS, s.LatchFalling, s.MSBFirst, s.N = c.IdleS, c.LatchFalling, c.MSBFirst, len(c.Bits)
	for k, v := range c.Bits {
		s.Bits[k] = map[hal.PatternState]trigger.State{hal.Low: trigger.Low, hal.High: trigger.High, hal.DontCare: trigger.DontCare}[v]
	}
	for k := len(c.Bits); k < 96; k++ {
		s.Bits[k] = trigger.DontCare
	}
	return nil
}

// canSettings converts a CAN trigger other than the error trigger.
func (b *Board) canSettings(k *trigger.CANSet, c prototrig.CAN) error {
	var err error
	if k.Line, err = b.line(c.Line); err != nil {
		return err
	}
	if k.Baud, err = baud(c.Baud, "CAN"); err != nil {
		return err
	}
	if k.Data, err = bytesOf(c.Data); err != nil {
		return err
	}
	k.Condition, k.Extended, k.ID = int(c.Condition), c.Extended, c.ID
	if c.ID < 0 {
		k.ID = 0x800
		if c.Extended {
			k.ID = 0x2000_0000
		}
	}
	return nil
}
