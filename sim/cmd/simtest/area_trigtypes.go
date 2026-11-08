package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/labeth/open-sds800xhd/sim/harness"
)

// The trigger area's app run: every register golden the host interfaces
// can express (app/internal/trigcases/host.json) is set up from SCPI or the
// page's controls, in the goldens' order, through the app's instrument
// state and owner loop; the registers the board then holds are the
// golden's, the type reads back the same over SCPI and the web, and what
// the trigger cannot run is refused with the reason.

// trigAppHostFile is where the host's checks of the trigger app run land.
const trigAppHostFile = "trigger-host.txt"

// trigMark is the mark the host logs before it sets up a golden.
const trigMark = "trigcase "

// hostSetup is one golden's setup as host.json holds it.
type hostSetup struct {
	Name string            `json:"name"`
	SCPI []string          `json:"scpi"`
	Web  []json.RawMessage `json:"web"`
	Want map[string]uint16 `json:"want"`
}

// hostSetups reads host.json.
func hostSetups(e *env) ([]hostSetup, error) {
	b, err := os.ReadFile(filepath.Join(e.root, "app", "internal", "trigcases", "host.json"))
	if err != nil {
		return nil, err
	}
	var hs []hostSetup
	return hs, json.Unmarshal(b, &hs)
}

// trigAppRun is the app run of the trigger area.
func trigAppRun(e *env) harness.Spec {
	return harness.Spec{
		Name: e.prefix + "trigger-app", Secs: 240, Ports: true, SimdArgs: signals + " -no-trig-model", // registers and read-backs, not firing (trigfire's)
		App: e.app, Hook: e.hook, GuestBoot: e.guestBoot,
		Host: func(ctx context.Context, dir string) { trigHost(ctx, e, dir) },
	}
}

// scpiTypes are :TRIGger:TYPE? replies by the web's type names.
var scpiTypes = map[string]string{
	"edge": "EDGE", "pulse": "PULSE", "slope": "SLOPe", "interval": "INTerval", "pattern": "PATTern", "runt": "RUNT",
	"window": "WINDow", "dropout": "DROPout", "video": "VIDeo", "qualified": "QUALified", "nth_edge": "NEDGe",
	"delay": "DELay", "setup_hold": "SHOLd", "i2c": "IIC", "spi": "SPI", "uart": "UART", "lin": "LIN", "can": "CAN",
}

// trigHost sets every golden up from the host and checks what reads back.
func trigHost(ctx context.Context, e *env, dir string) {
	f, err := os.Create(filepath.Join(dir, trigAppHostFile)) //nolint:gosec // G304: the run's own directory
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	u := &uiRig{ctx: ctx, dir: dir, h: hostLog{f}, cl: &http.Client{Timeout: 10 * time.Second}}
	h := u.h
	hs, err := hostSetups(e)
	if !h.check("setups", err == nil && len(hs) > 0, "%d golden setups from host.json (%v)", len(hs), err) {
		return
	}
	if !h.check("serving", u.until(func() bool { return u.status()["run"] != nil }), "the app's web server answered") {
		return
	}
	// Every line is answered (a command with *OPC?): separate connections
	// are separate sessions, which the app may serve out of order.
	scpi := func(line string) string {
		if !strings.Contains(line, "?") {
			line += ";*OPC?"
		}
		r, err := scpiQuery(ctx, line)
		if err != nil {
			return "err " + err.Error()
		}
		return r
	}
	scpi(":CHAN2:SWIT ON;:CHAN3:SWIT ON;:CHAN4:SWIT ON;:TRIG:MODE NORM;*CLS")
	time.Sleep(2 * time.Second)
	for _, s := range hs {
		u.ctl("mark " + trigMark + s.Name)
		if len(s.SCPI) > 0 {
			scpi(strings.Join(s.SCPI, ";"))
		}
		var bad strings.Builder
		for _, w := range s.Web {
			if out := u.set("/api/set", string(w)); out["ok"] != true {
				fmt.Fprintf(&bad, " %s: %v", w, out["err"])
			}
		}
		errq := scpi(":SYST:ERR?")
		h.check("set-"+s.Name, bad.Len() == 0 && strings.HasPrefix(errq, "0,"), "SCPI %q, web %d controls%s; error queue %q", s.SCPI, len(s.Web), bad.String(), errq)
		time.Sleep(1500 * time.Millisecond) // the owner commits at its next pass
		st := u.status()
		trig, _ := st["trig"].(map[string]any)
		web, _ := trig["type"].(string)
		typ := scpi(":TRIG:TYPE?")
		h.check("readback-"+s.Name, web != "" && scpiTypes[web] == typ, "the web reads %q, SCPI %q", web, typ)
	}
	u.ctl("mark " + trigMark + "end")

	// Refusals with the reason: a decode-only protocol at once, a CAN error
	// trigger when the board is asked to program it (the state goes back
	// to the last the board took), a choice a setting lacks from the web.
	scpi(":TRIG:TYPE SENT")
	q := scpi(":SYST:ERR?")
	h.check("refuse-sent", strings.HasPrefix(q, "-221") && strings.Contains(q, "decode only"), "TYPE SENT: %q", q)
	before := scpi(":TRIG:TYPE?")
	scpi(":TRIG:CAN:COND ERR;:TRIG:TYPE CAN")
	q = ""
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && !strings.HasPrefix(q, "-221"); time.Sleep(250 * time.Millisecond) {
		q = scpi(":SYST:ERR?")
	}
	h.check("refuse-can-error", strings.Contains(q, "CAN error trigger"), "the board's refusal reaches the queue: %q", q)
	time.Sleep(time.Second)
	after := scpi(":TRIG:TYPE?")
	h.check("refuse-keeps-type", after == before, "the type stays %q (now %q)", before, after)
	trig, _ := u.status()["trig"].(map[string]any)
	h.check("refuse-web-status", strings.Contains(fmt.Sprint(trig["refused"]), "CAN error trigger"), "the status says %v", trig["refused"])
	out := u.set("/api/set", `{"control":"trig.limit","text":"sideways"}`)
	h.check("refuse-web-choice", out["ok"] == false && out["err"] != "", "a choice the setting lacks: %v", out["err"])
	out = u.set("/api/set", `{"control":"trigtype","text":"zone"}`)
	h.check("refuse-web-type", out["ok"] == false && out["err"] != "", "a type this scope's state lacks: %v", out["err"])
	var caps struct {
		Trigger struct {
			Avail []struct {
				ID  string `json:"id"`
				OK  bool   `json:"ok"`
				Why string `json:"why"`
			} `json:"type_avail"`
		} `json:"trigger"`
	}
	_ = u.get("/api/caps", &caps) // unreadable caps fail the check below
	n := 0
	for _, a := range caps.Trigger.Avail {
		if a.OK {
			n++
		}
	}
	h.check("caps-types", n == len(scpiTypes) && len(caps.Trigger.Avail) == len(scpiTypes), "%d of %d types run here", n, len(caps.Trigger.Avail))
}

// trigAppCheck checks the app run: the host's checks, then each golden's
// registers as the board held them before the next golden's setup.
func trigAppCheck(e *env, r *harness.Run) []harness.Result {
	res := hostResultsOf(r, "trigger", trigAppHostFile)
	hs, err := hostSetups(e)
	if err != nil {
		return append(res, harness.Fail("trigger", "app goldens", "%v", err))
	}
	for _, s := range hs {
		id := "app " + s.Name
		got, ok := r.Log.FinalThrough(trigMark+s.Name, trigMark)
		if !ok {
			res = append(res, harness.Fail("trigger", id, "no mark %q", trigMark+s.Name))
			continue
		}
		var bad []string
		for k, w := range s.Want {
			reg, err := harness.ParseReg(k)
			if err != nil {
				bad = append(bad, err.Error())
				continue
			}
			if got[reg] != w {
				bad = append(bad, fmt.Sprintf("%v %#x, want %#x", reg, got[reg], w))
			}
		}
		via := "SCPI"
		if len(s.Web) > 0 {
			via = "the web"
		}
		if len(bad) > 0 {
			res = append(res, harness.Fail("trigger", id, "set from %s: %s", via, strings.Join(bad, "; ")))
		} else {
			res = append(res, harness.Pass("trigger", id, "set from "+via+": "+strconv.Itoa(len(s.Want))+" registers as the golden"))
		}
	}
	return res
}

// set posts a JSON body and decodes the reply (an error as its "err").
func (u *uiRig) set(path, body string) map[string]any {
	var out map[string]any
	if err := u.post(path, body, &out); err != nil && out == nil {
		return map[string]any{"err": err.Error()}
	}
	return out
}
