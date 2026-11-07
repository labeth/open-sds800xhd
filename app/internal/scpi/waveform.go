package scpi

import (
	"math"
	"strconv"
	"strings"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-dso-core/sample"
	core "github.com/labeth/open-dso-core/scpi"
	"github.com/labeth/open-dso-core/scpi/wave"
)

// codesPerDiv is the 16-bit transfer scale: 7680 codes a division about
// 0x8000 in WORD, 30 in BYTE (spec 11 §7.4).
const codesPerDiv = 7680

func (d *dialect) waveform() []core.Command {
	nr1 := func(header string, p *int, least int) []core.Command {
		return []core.Command{
			d.bind(header, func(c *core.Call) error {
				v, ok, err := number(c)
				if err != nil || !ok {
					return err
				}
				if v < float64(least) || v > math.MaxInt32 {
					return c.Error(core.ErrDataOutOfRange)
				}
				*p = int(v)
				return nil
			}),
			d.bind(header+"?", func(c *core.Call) error { c.NR1(int64(*p)); return nil }),
		}
	}
	cmds := []core.Command{
		d.bind(":WAVeform:SOURce", func(c *core.Call) error {
			n, err := d.source(c)
			if err == nil {
				d.wfSource = n
			}
			return err
		}),
		d.bind(":WAVeform:SOURce?", func(c *core.Call) error { c.Text("C" + strconv.Itoa(d.wfSource+1)); return nil }),
		d.bind(":WAVeform:WIDTh", func(c *core.Call) error {
			i, err := choice(c, d.choices(":WAVeform:WIDTh", "type")...)
			if err == nil {
				d.wfWord = i == 1
			}
			return err
		}),
		d.bind(":WAVeform:WIDTh?", func(c *core.Call) error {
			c.Text(map[bool]string{false: "BYTE", true: "WORD"}[d.wfWord])
			return nil
		}),
		d.bind(":WAVeform:BYTeorder", func(c *core.Call) error {
			i, err := choice(c, d.choices(":WAVeform:BYTeorder", "order")...)
			if err == nil {
				d.wfMSB = i == 0
			}
			return err
		}),
		d.bind(":WAVeform:BYTeorder?", func(c *core.Call) error {
			c.Text(map[bool]string{false: "LSB", true: "MSB"}[d.wfMSB])
			return nil
		}),
		d.bind(":WAVeform:MAXPoint?", func(c *core.Call) error { c.NR1(maxPoints); return nil }),
		d.bind(":WAVeform:PREamble?", func(c *core.Call) error {
			desc, ok := d.preamble(c, d.wfSource)
			if !ok {
				c.Raw(wave.Block9(d.wfHeader(c, d.wfSource, "DESC"), desc))
				return nil
			}
			c.Raw(wave.Block9("", desc))
			return nil
		}),
		d.bind(":WAVeform:DATA?", func(c *core.Call) error { return d.data(c) }),
	}
	cmds = append(cmds, nr1(":WAVeform:STARt", &d.wfStart, 0)...)
	cmds = append(cmds, nr1(":WAVeform:INTerval", &d.wfInterval, 1)...)
	cmds = append(cmds, nr1(":WAVeform:POINt", &d.wfPoints, 0)...)
	return cmds
}

// wfHeader is a legacy waveform reply's header: "C1:WF DESC," under CHDR.
func (d *dialect) wfHeader(c *core.Call, ch int, block string) string {
	src := "C" + strconv.Itoa(ch+1)
	switch c.HeaderMode() {
	case core.HeaderOff:
		return ""
	case core.HeaderLong:
		return src + ":WAVEFORM " + block + ","
	case core.HeaderShort:
		// the short form below
	}
	return src + ":WF " + block + ","
}

// trace is channel ch's trace of the last frame, with the frame.
func trace(f *sample.Frame, ch int) (*sample.Trace, bool) {
	if f == nil {
		return nil, false
	}
	for i := range f.Traces {
		if t := &f.Traces[i]; t.Ch == ch && t.Valid > 0 {
			return t, true
		}
	}
	return nil, false
}

// tdivEnum is the descriptor's time/div enum: 0 = 200 ps … 38 = 1000 s in
// 2-5-10 steps (spec 11 §7.2).
func tdivEnum(t float64) int16 {
	best, bestD := 0, math.Inf(1)
	for i := range 39 {
		k := i + 1 // 200 ps is the second step of the 100 ps decade
		v := [...]float64{1, 2, 5}[k%3] * math.Pow(10, float64(k/3)-10)
		if dd := math.Abs(math.Log(v / t)); dd < bestD {
			best, bestD = i, dd
		}
	}
	return int16(best)
}

// preamble is the 346-byte descriptor of channel ch's last record (spec 11
// §7.2); ok false (and a descriptor with no points) without a record.
func (d *dialect) preamble(c *core.Call, ch int) ([]byte, bool) {
	s := c.Instrument().State()
	var cs hal.Channel
	if ch < len(s.Channels) {
		cs = s.Channels[ch]
	}
	probe := cs.Probe
	if !(probe > 0) {
		probe = 1
	}
	desc := wave.Desc{
		Template:          "WAVEACE",
		InstrumentName:    instrumentName(d.cfg.Identity),
		InstrumentNumber:  0xCDAB,
		SegmentIndex:      -1,
		SweepsPerAcq:      int32(d.wfPoints), //nolint:gosec // G115: the descriptor field is 32-bit; the :WAVeform setters keep to it, a larger legacy WFSU value wraps
		PointsPerPair:     1,
		VerticalGain:      float32(cs.VoltsDiv / probe),
		VerticalOffset:    float32(cs.OffsetV / probe),
		AcqVerticalOffset: float32(cs.OffsetV / probe),
		MaxValue:          codesPerDiv,
		NominalBits:       16,
		NomSubarrayCount:  -1,
		VertUnit:          "V",
		HorUnit:           "S",
		HorizUncertainty:  1e-9,
		RISSweeps:         1,
		Timebase:          tdivEnum(s.TimeDivS),
		VertCoupling:      int16(cs.Coupling), //nolint:gosec // G115: a coupling enum value
		ProbeAtt:          float32(probe),
		VerticalVernier:   1,
		FirstPoint:        int32(d.wfStart),    //nolint:gosec // G115: the descriptor field is 32-bit; the :WAVeform setters keep to it, a larger legacy WFSU value wraps
		Sparsing:          int32(d.wfInterval), //nolint:gosec // G115: the descriptor field is 32-bit; the :WAVeform setters keep to it, a larger legacy WFSU value wraps
		WaveSource:        int16(ch),           //nolint:gosec // G115: a channel index
	}
	if d.wfWord {
		desc.CommType = 1
	}
	if d.wfMSB {
		desc.CommOrder = 1
	}
	if cs.BWLimit > 0 {
		desc.BandwidthLimit = 1
	}
	ok := false
	c.Instrument().WithFrame(func(f *sample.Frame) {
		t, have := trace(f, ch)
		if !have || !(f.SampleS > 0) {
			return
		}
		ok = true
		n := t.Valid
		desc.WaveArray1 = int32(n * (1 + int(desc.CommType)))         //nolint:gosec // G115: a trace holds at most 100 Mpts, 200 MB as words
		desc.Reserved112, desc.WaveArrayCount = int32(n), int32(n)    //nolint:gosec // G115: a trace holds at most 100 Mpts
		desc.PointsPerScreen, desc.LastValid = int32(n-2), int32(n-1) //nolint:gosec // G115: a trace holds at most 100 Mpts
		desc.HorizInterval = float32(f.SampleS)
		// HORIZ_OFFSET places sample 0 at −5 divisions of a record that is
		// the screen: time(i) = −5·tdiv + HORIZ_OFFSET + i·interval
		// (spec 11 §7.4), with the trigger at time 0.
		trig := f.TriggerAt
		if trig < 0 {
			trig = float64(n) / 2
		}
		desc.HorizOffset = float64(n)*f.SampleS/2 - trig*f.SampleS
		desc.PixelOffset = desc.HorizOffset
	})
	if !ok {
		desc.LastValid, desc.PointsPerScreen = -1, -2
	}
	return desc.Encode(), ok
}

// instrumentName is the descriptor's INSTRUMENT_NAME: the manufacturer's
// first word and the model family ("<maker> SDS").
func instrumentName(id Identity) string {
	maker, _, _ := strings.Cut(id.Manufacturer, " ")
	family := id.Model
	if len(family) > 3 {
		family = family[:3]
	}
	return strings.TrimSpace(maker + " " + family)
}

// data answers :WAVeform:DATA? from the last complete record without
// touching the run state: a definite block, its LF, then the message's
// (spec 11 §7.3).
func (d *dialect) data(c *core.Call) error {
	ch := d.wfSource
	var out []byte
	text, have := "", false
	c.Instrument().WithFrame(func(f *sample.Frame) {
		t, ok := trace(f, ch)
		if !ok {
			return
		}
		have = true
		w := wave.Window{First: d.wfStart, Sparse: d.wfInterval, Points: d.wfPoints, Max: maxPoints}
		first, step, n, ok := w.Span(t.Valid)
		if !ok {
			text = "fp > current memory depth!"
			return
		}
		out = d.encode(t, first, step, n)
	})
	switch {
	case !have: // no record: the legacy empty block (spec 11 §7.1)
		c.Raw(append(wave.Block9(d.wfHeader(c, ch, "DAT2"), nil), '\n'))
	case text != "":
		c.Raw([]byte(text + "\n"))
	default:
		c.Raw(append(wave.Block("", out), '\n'))
	}
	return nil
}

// encode is n samples of t from first, every step-th, in the transfer's
// width and byte order: the stored 16-bit word less 0x8000 (WORD), or its
// high byte less 0x80 (BYTE); 8-bit records are code×256 in WORD.
func (d *dialect) encode(t *sample.Trace, first, step, n int) []byte {
	words := t.Codes16
	if t.Format.Storage != sample.U16 {
		words = make([]uint16, len(t.Codes8))
		for i, v := range t.Codes8 {
			words[i] = uint16(v) << 8
		}
	}
	if d.wfWord {
		return wave.Signed16(words, first, step, n, d.wfMSB)
	}
	return wave.High8(words, first, step, n)
}
