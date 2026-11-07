// Package app is the SDS800X HD's application: its board handed to the
// core's scope application (scopeapp.Run) with this scope's SCPI dialect,
// its web page's probe and bandwidth lists, its default state and its
// state check. Everything else (the owner loop, the UI model and its
// display over the waveform plane, the settings on p12, the web server)
// is the core's.
package app

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-dso-core/sample"
	"github.com/labeth/open-dso-core/scopeapp"
	core "github.com/labeth/open-dso-core/scpi"
	"github.com/labeth/open-dso-core/scpi/host"
	"github.com/labeth/open-dso-core/webserver"
	"github.com/labeth/open-sds800xhd/app/internal/frontend"
	"github.com/labeth/open-sds800xhd/app/internal/measurement"
	"github.com/labeth/open-sds800xhd/app/internal/platform"
	"github.com/labeth/open-sds800xhd/app/internal/scopeboard"
	"github.com/labeth/open-sds800xhd/app/internal/scpi"
	"github.com/labeth/open-sds800xhd/app/internal/web"
)

// appVersion is the application's four-part version (spec 11 §6.6).
const appVersion = "0.1.0.0"

// Run starts the application and returns the process exit code. It returns
// on SIGTERM or SIGINT, or when the takeover fails.
func Run() int {
	log, err := platform.OpenLog("app")
	if err != nil {
		log.Printf("log file: %v (stderr only)", err)
	}
	defer func() { _ = log.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	log.Printf("start pid %d", os.Getpid())
	b := scopeboard.New(log)
	version := platform.VersionPrefix() + "." + appVersion
	err = scopeapp.Run(ctx, b, scopeapp.Options{
		Logf: log.Printf, Product: "open-sds800xhd " + version, Version: version,
		Defaults: scpi.Defaults, Check: checkState,
		Publish: func(f *sample.Frame) { report(log, f) },
		SCPI:    func(env *scopeapp.Env) (scopeapp.SCPI, error) { return scpiEngine(env, b, log, version) },
		// spec 11 §1–§3: VXI-11 registered with the system portmapper, the
		// raw socket and the telnet line interface (its banner names the
		// unit).
		SCPIHost: host.Config{Raw: rawPort, Telnet: telnetPort, VXI11: true, Register: true},
		Listen:   web.Port,
		Web: func(env *scopeapp.Env) (webserver.Scope, error) {
			return web.Scope(b, env.Model, env.Frames, version, env.UI, scopeapp.WebSetups{SharedSetups: env.Setups}, scopeapp.WebParts(env), env.ScreenPNG), nil
		},
	})
	if err != nil {
		log.Printf("bring-up: %v", err)
		return 1
	}
	return 0
}

// scpiEngine is the core SCPI engine with this scope's dialect: *IDN? from
// the unit's identity, its channels, its measurement of 12-bit traces, the
// board's own headers and the self-calibration.
func scpiEngine(env *scopeapp.Env, b hal.Board, log *platform.Log, version string) (scopeapp.SCPI, error) {
	id := env.Identity
	cal := &selfCal{b: b, ctl: env.Model, log: log}
	dl, err := scpi.Dialect(scpi.Config{
		Identity:     scpi.Identity{Manufacturer: id.Vendor, Model: id.Model, Serial: id.Serial, Version: version},
		Channels:     b.Profile().Channels,
		Measure:      measurement.Trace,
		Extensions:   extensions(b),
		SelfCal:      cal.start,
		SelfCalState: cal.state,
	})
	if err != nil {
		log.Printf("%v", err)
	}
	eng := core.New(dl, env.Model)
	return scopeapp.SCPI{Exec: eng.Exec, Report: eng.Report}, nil
}

// checkState refuses what the front end can tell without hardware: a probe
// factor or V/div outside its ladder.
func checkState(s core.State) error {
	for _, ch := range s.Channels {
		probe := ch.Probe
		if probe == 0 {
			probe = 1
		}
		if why := frontend.CheckProbe(probe); why != "" {
			return hal.Refuse("probe factor", why)
		}
		if why := frontend.CheckVoltsDiv(ch.VoltsDiv, probe); why != "" {
			return hal.Refuse("V/div", why)
		}
	}
	if s.TimeDivS <= 0 {
		return errors.New("time/div must be positive")
	}
	return nil
}

// report logs the measurements of every 500th frame.
func report(log *platform.Log, f *sample.Frame) {
	if f.Seq%500 != 1 {
		return
	}
	for _, t := range f.Traces {
		r := measurement.Trace(t, f.SampleS)
		if r == nil {
			continue
		}
		log.Printf("frame %d C%d: %d words, trigger at %.1f (nearest rising edge %+d): Vpp %.4g Vmin %.4g Vmax %.4g Vmean %.4g freq %.6g duty %.3g",
			f.Seq, t.Ch+1, len(t.Codes16), f.TriggerAt, measurement.EdgeOffset(t, f.TriggerAt),
			r.Vpp, r.Vmin, r.Vmax, r.Vmean, r.Freq, r.Duty)
	}
}
