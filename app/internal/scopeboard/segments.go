package scopeboard

import (
	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-sds800xhd/app/internal/timebase"
)

// Sequence (segmented) acquisition, spec 03 §6.2: Request.Segments > 1
// captures that many triggered segments; Readout reads any range of them.

type segmentsFacet Board

// Max is the sequence capacity at depth samples per channel with the
// staged time/div and channel set (spec 04 §8.2 step 6).
func (s *segmentsFacet) Max(depth int) int {
	b := (*Board)(s)
	c := timebase.Config{
		Enum: b.staged.Enum, Depth: depthEnum(depth), Mask: b.staged.Mask(b.id.Channels),
		Limited: b.id.BandwidthMHz < 200, Seq: true,
	}
	return timebase.Compute(c).MaxSegments()
}

// Timestamps reads the stored sequence's segment times, seconds from the
// first segment (spec 03 §6.3: a 48-bit counter in µs).
func (s *segmentsFacet) Timestamps(dst []float64) (int, error) {
	b := (*Board)(s)
	if b.open != nil {
		return b.openTimestamps(dst)
	}
	n := b.eng.Settings().Segments
	if !b.stored || n < 2 {
		return 0, hal.Refuse("segment timestamps", "no stored sequence")
	}
	n = min(n, len(dst))
	var t0 uint64
	for k := range n {
		st := b.eng.SegmentStamp(k)
		if k == 0 {
			t0 = st
		}
		dst[k] = float64(int64(st-t0)) * 1e-6 //nolint:gosec // G115: segment timestamps differ by far less than 2^63 µs
	}
	return n, nil
}
