package scopeboard

import (
	"fmt"
	"math"
	"strings"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-dso-core/sample"
	"github.com/labeth/open-sds800xhd/app/internal/timebase"
)

type plannerFacet Board

// depthEnum maps a requested depth to the depth enums of spec 04 §3
// (10k, 100k, 1M, 10M; 4 = the deepest of the route).
func depthEnum(n int) int {
	switch {
	case n <= 0:
		return 3
	case n <= 10_000:
		return 0
	case n <= 100_000:
		return 1
	case n <= 1_000_000:
		return 2
	case n <= 10_000_000:
		return 3
	}
	return 4
}

// rollMinEnum is 50 ms/div, where roll becomes available (spec 04 §7).
const rollMinEnum = 25

func (p *plannerFacet) config(r hal.Request) (timebase.Config, error) {
	b := (*Board)(p)
	if r.PretrigRoom != 0 {
		return timebase.Config{}, hal.Refuse("pretrigger room", "the record's pretrigger follows the horizontal position")
	}
	mask := 0
	for _, c := range r.Channels {
		if c < 0 || c >= b.id.Channels {
			return timebase.Config{}, hal.Refuse("channels", fmt.Sprintf("C%d does not exist on this model", c+1))
		}
		mask |= 1 << c
	}
	minEnum := 3
	if b.id.BandwidthMHz >= 200 {
		minEnum = 2
	}
	c := timebase.Config{
		Enum: timebase.EnumOf(r.TimePerDivS, minEnum), Depth: depthEnum(r.Depth), Mask: mask,
		Limited: b.id.BandwidthMHz < 200, Seq: r.Segments > 1,
	}
	switch r.Mode {
	case hal.Normal:
	case hal.RollMode:
		if c.Enum < rollMinEnum {
			return c, hal.Refuse("roll", "roll needs 50 ms/div or slower (spec 04 §7)")
		}
		if r.Segments > 1 {
			return c, hal.Refuse("roll", "sequence mode does not roll")
		}
		c.Roll = true
	case hal.PeakDetect:
		c.Peak = true
	case hal.Average, hal.HighRes:
		return c, hal.Refuse("acquisition mode", "the PL has no averaging or ERES; they are math on frames (spec 03 §6.1)")
	case hal.Stream:
		return c, hal.Refuse("stream", "no gap-free stream path; roll is the only stream (spec 03 §6.4)")
	default:
		return c, hal.Refuse("acquisition mode", "unknown mode")
	}
	if r.Segments > 1 {
		if m := timebase.Compute(c).MaxSegments(); r.Segments > m {
			return c, hal.Refuse("segments", fmt.Sprintf("%d segments do not fit: at most %d at this time/div and depth", r.Segments, m))
		}
	}
	return c, nil
}

// maxRate is the route's converter rate, MSa/s (spec 04 §1).
var maxRate = [3]float64{2000, 1000, 500}

func (p *plannerFacet) Plan(r hal.Request) (hal.Plan, error) {
	c, err := p.config(r)
	if err != nil {
		return hal.Plan{}, err
	}
	h := timebase.Compute(c)
	pl := hal.Plan{
		Request: r, Mode: r.Mode,
		Content: sample.ContentSetOf(sample.RawFullRate, sample.RawDecimated),
		SampleS: 1 / (h.R * 1e6), Depth: h.D * h.Delta, AcqDecim: 1, ETSFactor: 1, Segments: max(1, r.Segments),
		WinSamples: int(h.S),
	}
	if top := maxRate[h.Route]; c.Roll {
		pl.AcqDecim = int(math.Round(2000 / h.R))
	} else if h.R < top {
		pl.AcqDecim = int(math.Round(top / h.R))
	}
	var notes []string
	if t := timebase.TimeDiv(c.Enum); t != r.TimePerDivS {
		notes = append(notes, fmt.Sprintf("time/div %g s", t))
	}
	switch {
	case peakActive(h):
		pl.Content = sample.ContentSetOf(sample.PeakPairs)
	case c.Peak:
		notes = append(notes, "peak detect has no effect at this undecimated rate (spec 04 §5.2): plain samples")
	}
	pl.Note = strings.Join(notes, "; ")
	for _, f := range r.Need {
		if f.Accepts&pl.Content == 0 {
			return hal.Plan{}, hal.Refuse(f.Name, fmt.Sprintf("this timebase gives %v records, which it does not take", pl.Content))
		}
		if f.MinDepth > pl.Depth {
			return hal.Plan{}, hal.Refuse(f.Name, fmt.Sprintf("needs %d samples, the record holds %d", f.MinDepth, pl.Depth))
		}
	}
	return pl, nil
}

// peakActive reports whether the PL records max/min pairs for h: peak
// detect at a decimated rate (PEAK_EN, spec 04 §5.2: rate < 500 MSa/s and
// not 400, which are exactly the rates with a decimation index).
func peakActive(h timebase.Set) bool { return h.Peak && h.Decim != 0 }
