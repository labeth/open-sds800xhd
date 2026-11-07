package scopeboard

import (
	"testing"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-sds800xhd/app/internal/acq"
	"github.com/labeth/open-sds800xhd/app/internal/trigger"
)

func TestSequenceSweep(t *testing.T) {
	tests := []struct {
		name     string
		segments int
		sweep    trigger.Sweep
		refused  bool
	}{
		{"sequence in AUTO", 10, trigger.Auto, true},
		{"sequence in Normal", 10, trigger.Normal, false},
		{"sequence in Single", 10, trigger.Single, false},
		{"single records in AUTO", 0, trigger.Auto, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := acq.DefaultSettings()
			s.Segments, s.Trig.Sweep = tt.segments, tt.sweep
			_, refused := hal.AsRefusal(sequenceSweep(&s))
			if refused != tt.refused {
				t.Errorf("refused %v, want %v", refused, tt.refused)
			}
		})
	}
}
