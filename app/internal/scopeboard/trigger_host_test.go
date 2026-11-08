package scopeboard

import (
	"context"
	"strings"
	"testing"

	"github.com/labeth/open-dso-core/instrument"
	"github.com/labeth/open-dso-core/scopeweb"
	core "github.com/labeth/open-dso-core/scpi"
	"github.com/labeth/open-dso-core/webserver"
	"github.com/labeth/open-sds800xhd/app/internal/scpi"
	"github.com/labeth/open-sds800xhd/app/internal/trigcases"
)

// The register goldens set from the host interfaces: every golden the SCPI
// commands or the web controls can express, run in the goldens' order on
// one trigger engine, programs the registers the golden gives, through the
// instrument state the owner commits (its running type's HAL spec).
func TestTriggerGoldensFromTheHost(t *testing.T) {
	b, tr, ch, ab, mb := goldenBoard()
	st := scpi.Defaults(4)
	for c := range st.Channels {
		st.Channels[c].On = true
	}
	m := instrument.New(st, nil)
	m.SetTrigCaps((*triggerFacet)(b).Caps)
	d, err := scpi.Dialect(scpi.Config{Channels: 4})
	if err != nil {
		t.Fatal(err)
	}
	e := core.New(d, m)
	e.Exec([]byte(":TRIG:MODE NORM"))
	web := scopeweb.New(scopeweb.Config{Board: b, Instrument: m}).Settings()
	setWeb := func(name string, cs []trigcases.WebControl) {
		for _, c := range cs {
			if a := web.Set(context.Background(), webserver.Control{Name: c.Control, Text: c.Text, Value: c.Value, Ch: -1}); !a.OK {
				t.Fatalf("%s: web %s: %s", name, c.Control, a.Err)
			}
		}
	}
	for _, h := range trigcases.Hosts() {
		for _, msg := range h.SCPI {
			e.Exec([]byte(msg))
		}
		setWeb(h.Name, h.Web)
		if q := string(e.Exec([]byte(":SYST:ERR?"))); !strings.HasPrefix(q, "0,") {
			t.Fatalf("%s: %s", h.Name, q)
		}
		ts, err := b.triggerSettings(m.State().TriggerSpec())
		if err != nil {
			t.Fatalf("%s: %v", h.Name, err)
		}
		tr.Program(ts, ch)
		checkRegs(t, h.Name, ab, mb, h.Want)
	}
}

// The host interfaces refuse with the reason what the trigger cannot run:
// a protocol without trigger hardware, a CAN error trigger, times outside
// the trigger's range.
func TestTriggerRefusalsFromTheHost(t *testing.T) {
	b, _, _, _, _ := goldenBoard()
	st := scpi.Defaults(4)
	for c := range st.Channels {
		st.Channels[c].On = true
	}
	m := instrument.New(st, nil)
	d, err := scpi.Dialect(scpi.Config{Channels: 4})
	if err != nil {
		t.Fatal(err)
	}
	e := core.New(d, m)
	for _, c := range []struct{ cmd, want string }{
		{":TRIG:TYPE SENT", `-221,"Settings conflict;SENT trigger: the factory PL image has no trigger hardware`},
		{":TRIG:PULS:TLOW 1E-9", `-222,"Data out of range"`},
		{":TRIG:EDGE:HLDT 4E-9", `-222,"Data out of range"`},
		{":TRIG:INT:SLOP ALT", `-224,"Illegal parameter value"`},
	} {
		e.Exec([]byte(c.cmd))
		if got := string(e.Exec([]byte(":SYST:ERR?"))); !strings.HasPrefix(got, c.want) {
			t.Errorf("%s: %s, want %s", c.cmd, got, c.want)
		}
	}
	// A CAN error trigger is the board's to refuse when it is programmed.
	e.Exec([]byte(":TRIG:CAN:COND ERR;:TRIG:TYPE CAN"))
	_, err = b.triggerSettings(m.State().TriggerSpec())
	if err == nil || !strings.Contains(err.Error(), "CAN error trigger") {
		t.Fatalf("CAN error: %v", err)
	}
}
