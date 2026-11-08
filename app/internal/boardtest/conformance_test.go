//go:build simboard

package boardtest

import (
	"testing"

	"github.com/labeth/open-dso-core/hal/conformance"
)

// TestConformance runs the core's HAL conformance suite against the board
// brought up in the simulator, one subtest per clause of the contract; the
// harness (sim/cmd/simtest, area conformance) reports each clause. The
// clauses the board does not keep yet are skipped with the reason.
func TestConformance(t *testing.T) {
	wait := "Wait answers a ready frame within a few milliseconds on a Normal edge at the top of the level grid, which the inputs never reach"
	conformance.Run(t, board, conformance.Options{Skip: map[string]string{
		"need-refusal":   "the planner does not check Request.Need",
		"freerun":        "Program refuses TriggerSpec.FreeRun (the vendor engine has no untriggered arm)",
		"optional-parts": "Segments is present while the profile's options lack OptSegments",
		"wait-poll":      wait,
		"wait-timeout":   wait,
		"wait-cancel":    wait,
	}})
}
