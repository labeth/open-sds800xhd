package scopeboard

import (
	"context"
	"testing"

	"github.com/labeth/open-dso-core/hal"
)

// TestSelfCalBeforeBringUp: the self-calibration is refused before
// bring-up, and no board extension serves it: the core runs
// FrontEnd.SelfCal.
func TestSelfCalBeforeBringUp(t *testing.T) {
	b := New(nil)
	if _, err := b.FrontEnd().SelfCal(context.Background(), nil); err == nil {
		t.Fatal("self-cal before bring-up was not refused")
	} else if _, ok := hal.AsRefusal(err); !ok {
		t.Fatalf("err %v, want a refusal", err)
	}
	for _, x := range b.Extensions() {
		if x.Feature != bootTargetFeature {
			t.Errorf("extension %q", x.Feature)
		}
	}
}
