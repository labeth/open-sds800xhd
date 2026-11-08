//go:build simboard

package boardtest

import (
	"testing"

	"github.com/labeth/open-dso-core/hal/conformance"
)

// TestConformance runs the core's HAL conformance suite against the board
// brought up in the simulator, one subtest per clause of the contract; the
// harness (sim/cmd/simtest, area conformance) reports each clause.
func TestConformance(t *testing.T) {
	conformance.Run(t, board, conformance.Options{})
}
