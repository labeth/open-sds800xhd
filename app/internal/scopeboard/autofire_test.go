package scopeboard

import (
	"testing"
	"time"

	"github.com/labeth/open-sds800xhd/app/internal/timebase"
	"github.com/labeth/open-sds800xhd/app/internal/trigger"
)

// An AUTO frame that took the trigger timeout from its arm completed
// through the timeout, not its condition: it is not reported triggered. A
// faster one is; a forced one is the user's; Normal and Single never time
// out.
func TestAutoFired(t *testing.T) {
	_, tr := timebase.Timeouts(9, autoOvertime) // 200 ns/div: 100 ms at the 4 ns tick
	for _, c := range []struct {
		sweep  trigger.Sweep
		forced bool
		took   time.Duration
		want   bool
	}{
		{trigger.Auto, false, 20 * time.Millisecond, false},
		{trigger.Auto, false, 100 * time.Millisecond, true},
		{trigger.Auto, false, time.Second, true},
		{trigger.Auto, true, time.Second, false},
		{trigger.Normal, false, time.Second, false},
		{trigger.Single, false, time.Second, false},
	} {
		if got := autoFired(c.sweep, c.forced, c.took, tr); got != c.want {
			t.Errorf("sweep %v forced %v after %v: auto %v, want %v", c.sweep, c.forced, c.took, got, c.want)
		}
	}
	if s := float64(tr) * autoTick; s < 0.099 || s > 0.101 {
		t.Fatalf("the default trigger timeout is %g s, want 100 ms", s)
	}
}
