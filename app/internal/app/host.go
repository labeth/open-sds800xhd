package app

import (
	"github.com/labeth/open-dso-core/hal"
	core "github.com/labeth/open-dso-core/scpi"
)

// The SCPI ports (spec 11 §1–§3).
const (
	rawPort    = ":5025"
	telnetPort = ":5024"
)

// extensions are the board's SCPI headers in the engine's shape.
func extensions(b hal.Board) []core.Extension {
	var out []core.Extension
	for _, x := range b.Extensions() {
		for _, h := range x.SCPI {
			out = append(out, core.Extension{Header: h.Header, Command: h.Command, Query: h.Query})
		}
	}
	return out
}
