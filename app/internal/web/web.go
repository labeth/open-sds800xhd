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
// (scopeapp.WebParts: the frequency response, the software mask test).
func Scope(b hal.Board, ctl *instrument.Model, frames *fanout.Fanout[sample.Frame], version string, ui webserver.UIModel, setups webserver.Setups, parts scopeweb.Parts) webserver.Scope {
	return scopeweb.New(scopeweb.Config{
		Board: b, Instrument: ctl, Frames: frames, Version: version, Product: "open-sds800xhd", UI: ui, Setups: setups, Parts: parts,
		Probes:   probes(),
		BWLimits: []string{"full", "20M"}, // spec 06: one 20 MHz limit
	}).WithCaps()
}

// probes are the probe factors the page offers: 1-2-5 steps across the
// front end's range (any factor in the range is taken over SCPI).
func probes() []float64 {
	var out []float64
	for d := frontend.MinProbe; d <= frontend.MaxProbe; d *= 10 {
		for _, m := range []float64{1, 2, 5} {
			if v := d * m; v <= frontend.MaxProbe*(1+1e-9) {
				out = append(out, sig3(v))
			}
		}
	}
	return out
}

// sig3 rounds v to three significant digits: the decades' products carry
// binary noise (0.1·10 is not 1 in float).
func sig3(v float64) float64 {
	e := 1.0
	for v*e < 100 {
		e *= 10
	}
	for v*e >= 1000 {
		e /= 10
	}
	return float64(int64(v*e+0.5)) / e
}
