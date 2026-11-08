// Package web is the SDS800X HD's web page: the core's shared web server
// and page over the core's web scope, with this scope's data the HAL does
// not carry (the probe factors and bandwidth limits it offers).
package web

import (
	"github.com/labeth/open-dso-core/engine/fanout"
	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-dso-core/instrument"
	"github.com/labeth/open-dso-core/sample"
	"github.com/labeth/open-dso-core/scopeweb"
	"github.com/labeth/open-dso-core/webserver"

	"github.com/labeth/open-sds800xhd/app/internal/frontend"
)

// Port is the shared web page's port: the instrument's own HTTP port.
const Port = ":80"

// Scope is this scope as the shared web server serves it: the core's
// web scope over the board, the instrument state every controller stages
// and the engine's frames, with the capabilities from the board's HAL, the
// shared UI model ui (the sheets, the panel and the phone remote; nil for
// none), the setup memory the panel shares (nil: the board's setups
// store holding instrument states) and the core's parts it serves
// (scopeapp.WebParts: the frequency response).
func Scope(b hal.Board, ctl *instrument.Model, frames *fanout.Fanout[sample.Frame], version string, ui webserver.UIModel, setups webserver.Setups, parts scopeweb.Parts) webserver.Scope {
	return scopeweb.New(scopeweb.Config{
		Board: b, Instrument: ctl, Frames: frames, Version: version, Product: "open-sds800xhd", UI: ui, Setups: setups, Parts: parts,
		Probes:   frontend.Probes(),
		BWLimits: frontend.BWLimits(),
	}).WithCaps()
}
