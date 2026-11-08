package scopeboard

import (
	"strings"

	"github.com/labeth/open-dso-core/hal/keytable"
	"github.com/labeth/open-sds800xhd/app/internal/panel"
)

// KeyTable is this panel's key table: every key of the matrix by its
// panel name and the UI model's name the board sends for it (input_names.go;
// "" for a key it drops), each reported as a press and a release, the
// channel keys selecting the channel the shared vertical controls act on,
// and the encoders, whose step byte counts up to 255 detents an event
// (spec 08 §4.2). The browser demo's panel is held to it.
func KeyTable() keytable.Table {
	var t keytable.Table
	for _, k := range panel.Keys() {
		e := keytable.Key{Panel: k, Release: true}
		switch k {
		case "vscale-push":
			e.Name = "ch" + keytable.Chan + "vdivpush"
		case "vpos-push":
			e.Name = "ch" + keytable.Chan + "pospush"
		default:
			e.Name = panelKeys[k]
		}
		if n, ok := strings.CutPrefix(k, "ch"); ok && len(n) == 1 && n[0] >= '1' && n[0] <= '4' {
			e.Selects = int(n[0] - '0')
		}
		t.Keys = append(t.Keys, e)
	}
	for _, k := range panel.Knobs() {
		e := keytable.Knob{Panel: k, Max: maxDetents}
		switch k {
		case "vscale":
			e.Name = "ch" + keytable.Chan + "vdiv"
		case "vpos":
			e.Name = "ch" + keytable.Chan + "pos"
		default:
			e.Name = panelKnobs[k]
		}
		t.Knobs = append(t.Knobs, e)
	}
	return t
}

// maxDetents is the most detents one encoder record counts (its step byte).
const maxDetents = 255
