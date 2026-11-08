package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/labeth/open-dso-core/uimodel"
	"github.com/labeth/open-dso-core/webserver/wire"
)

// The chpair area checks a channel alone in its converter pair: with C1
// off, C2 is the pair's only channel (the PL switches the pair to it), and
// its 1 MHz, 1 V sine must read the same in SCPI's measurement, the web's
// frames and the UI model's MEASURE values as any other channel's signal.
func init() {
	register(appHostArea("chpair", "a channel alone in its converter pair: C2 with C1 off in SCPI, the web and the UI model",
		200, uiSignals, chpairHostFile, chpairHost))
}

// chpairHostFile is where the host's checks of the chpair run land.
const chpairHostFile = "chpair-host.txt"

// chpairHost drives the check.
func chpairHost(ctx context.Context, dir string) {
	f, err := os.Create(filepath.Join(dir, chpairHostFile)) //nolint:gosec // G304: the run's own directory
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	u := &uiRig{ctx: ctx, dir: dir, h: hostLog{f}, cl: &http.Client{Timeout: 10 * time.Second}}
	h := u.h
	if !h.check("serving", u.until(func() bool { return u.status()["run"] != nil }), "the app's web server answered") {
		return
	}
	// The trigger moves to C2 first: a source that is off is refused.
	_, ok := u.scpiUntil(":CHAN2:SWIT ON;:CHAN3:SWIT ON;:TIM:SCAL 1E-6;:CHAN2:SWIT?", func(r string) bool { return r == "ON" })
	h.check("c2-on", ok, "C2 on")
	_, _ = scpiQuery(ctx, ":TRIG:EDGE:SOUR C2;*OPC?")
	time.Sleep(time.Second)
	sw, ok := u.scpiUntil(":CHAN1:SWIT OFF;:CHAN1:SWIT?", func(r string) bool { return r == "OFF" })
	h.check("c1-off", ok, "C1 off: %q", sw)
	reply, ok := u.scpiUntil(":MEAS:SIMP:SOUR C2;:MEAS:SIMP:VAL? FREQ;:MEAS:SIMP:VAL? PKPK", func(r string) bool {
		f := strings.Split(r, ";")
		return len(f) == 2 && near(f[0], 1e6, 0.02) && near(f[1], 1, 0.1)
	})
	h.check("scpi-c2", ok, "C2 over SCPI with C1 off: %q (1 MHz, 1 V peak to peak injected)", reply)

	get := func(path string) ([]byte, int) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, webBase+path, nil)
		if err != nil {
			return nil, 0
		}
		r, err := u.cl.Do(req)
		if err != nil {
			return nil, 0
		}
		defer func() { _ = r.Body.Close() }()
		var b strings.Builder
		buf := make([]byte, 1<<16)
		for {
			n, err := r.Body.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		return []byte(b.String()), r.StatusCode
	}
	fr, ok := webFrame(ctx, get, func(fr wire.Frame) bool { return traceOf(fr, 1) != nil && traceOf(fr, 0) == nil })
	var desc []string
	for _, t := range fr.Traces {
		lo, hi := swingOf(&t)
		desc = append(desc, fmt.Sprintf("C%d %d values %.3f…%.3f V", t.Ch+1, t.Codes.Len(), lo, hi))
	}
	if h.check("web-c2", ok, "a v2 frame without C1: %s", strings.Join(desc, "; ")) {
		lo, hi := swingOf(traceOf(fr, 1))
		h.check("web-c2-volts", lo < -0.4 && lo > -0.6 && hi > 0.4 && hi < 0.6, "C2 decodes to %.3f … %.3f V (the ±0.5 V sine)", lo, hi)
	}

	for _, row := range []string{"meas.freq", "meas.pkpk"} {
		if res := u.setRow(row, 2); !res.OK { // C2 alone
			h.check("meas-rows", false, "%s on C2: %+v", row, res)
			return
		}
	}
	if u.panel().Mode != modeWorkspace {
		u.ctl(keyMenu)
		u.until(func() bool { return u.panel().Mode == modeWorkspace })
	}
	u.ctl(keyMeas)
	var freq, pkpk uimodel.RowModel
	ok = u.until(func() bool {
		freq, _ = u.sheetRow("meas.freq")
		pkpk, _ = u.sheetRow("meas.pkpk")
		return len(freq.Live) > 1 && len(pkpk.Live) > 1 && nearSI(freq.Live[1], 1e6, 0.02) && nearSI(pkpk.Live[1], 1, 0.1)
	})
	h.check("meas-ui-c2", ok, "the UI model's C2 values with C1 off: freq %q, Vpp %q (1 MHz, 1 V injected)", freq.Live, pkpk.Live)
	chpairAlone(u)
}

// chpairAlone checks C2 as the only channel on: one trace in every frame.
func chpairAlone(u *uiRig) {
	h := u.h
	sw, ok := u.scpiUntil(":CHAN3:SWIT OFF;:CHAN3:SWIT?", func(r string) bool { return r == "OFF" })
	h.check("c3-off", ok, "C3 off: %q", sw)
	var freq, pkpk uimodel.RowModel
	ok = u.until(func() bool {
		freq, _ = u.sheetRow("meas.freq")
		pkpk, _ = u.sheetRow("meas.pkpk")
		return len(freq.Live) > 2 && freq.Live[2] == "" && nearSI(freq.Live[1], 1e6, 0.02) && nearSI(pkpk.Live[1], 1, 0.1)
	})
	h.check("meas-ui-c2-only", ok, "the UI model's C2 values as the only channel: freq %q, Vpp %q", freq.Live, pkpk.Live)
	reply, ok := u.scpiUntil(":MEAS:SIMP:SOUR C2;:MEAS:SIMP:VAL? FREQ", func(r string) bool { return near(r, 1e6, 0.02) })
	h.check("scpi-c2-only", ok, "C2 over SCPI as the only channel: %q", reply)
}
