package scopeboard

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-sds800xhd/app/internal/platform"
	"github.com/labeth/open-sds800xhd/ota/boottarget"
)

// bootTargetFeature names the extension in the capabilities.
const bootTargetFeature = "boot-target"

// bootTarget is the owner's "next boot: open / vendor firmware" choice as a
// board extension: :SYSTem:BOOT OPEN|VENDOR (query :SYSTem:BOOT?) and
// GET/POST /api/boot. It writes or removes p12's open/disable through the
// write guard; nothing is uninstalled, and the running app is not stopped.
// It touches no hardware, so its handlers may run on the transports'
// goroutines.
// TRLC-LINKS: REQ-HD-002, REQ-HD-016
func bootTarget(dir string) hal.Extension {
	set := func(arg, via string) error {
		t, err := boottarget.Parse(arg)
		if err != nil {
			return hal.Refuse("boot target", err.Error())
		}
		return boottarget.Set(dir, t, "asked through "+via)
	}
	next := func() string { return boottarget.Next(dir) }
	return hal.Extension{
		Feature: bootTargetFeature,
		SCPI: []hal.SCPIHeader{{
			Header:  ":SYSTem:BOOT",
			Command: func(args string) error { return set(args, "SCPI") },
			Query:   func(string) ([]byte, error) { return []byte(strings.ToUpper(next())), nil },
		}},
		Routes: []hal.Route{
			{Pattern: "GET /api/boot", Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeBootJSON(w, next())
			})},
			{Pattern: "POST /api/boot", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				arg := r.URL.Query().Get("next")
				if arg == "" {
					b, _ := io.ReadAll(io.LimitReader(r.Body, 64))
					arg = string(b)
				}
				if err := set(arg, "the web page"); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				writeBootJSON(w, next())
			})},
		},
	}
}

// TRLC-LINKS: REQ-HD-002
func writeBootJSON(w http.ResponseWriter, next string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"next": next})
}

// Extensions are the board's own features: the boot target.
// TRLC-LINKS: REQ-HD-002
func (b *Board) Extensions() []hal.Extension {
	return []hal.Extension{bootTarget(platform.OpenDir)}
}
