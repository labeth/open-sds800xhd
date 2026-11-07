//go:build simboard

package boardtest

import (
	"math"
	"testing"
	"time"
)

// counterHz is the frequency the counter area injects on C1.
const counterHz = 25e3

// TestCounter reads the frequency counter until a gate completes and
// compares it with the injected signal; the totalizer must count.
func TestCounter(t *testing.T) {
	c := begin(t, "counter/frequency")
	ctr, ok := board.Counter()
	if !c.check(ok, "the board has a counter") {
		return
	}
	deadline := time.Now().Add(20 * time.Second)
	var f float64
	for time.Now().Before(deadline) {
		r, err := ctr.Read()
		if err != nil {
			c.noErr(err, "read")
			return
		}
		if r.Valid {
			f = r.FreqHz
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	c.check(math.Abs(f-counterHz) <= counterHz*1e-3, "%g Hz, want %g Hz", f, counterHz)

	c = begin(t, "counter/totalizer")
	r1, _ := ctr.Read()
	time.Sleep(500 * time.Millisecond)
	r2, _ := ctr.Read()
	c.check(r2.Total > r1.Total, "totalizer %d then %d", r1.Total, r2.Total)

	c = begin(t, "counter/absent")
	_, lp := board.LogicProbe()
	_, gen := board.Generator()
	c.check(!lp && !gen, "logic probe and generator absent")
}
