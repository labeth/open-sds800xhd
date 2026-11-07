//go:build simboard

package boardtest

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-sds800xhd/app/internal/platform"
)

// TestPlatform checks the bring-up's identity and the profile it implies.
func TestPlatform(t *testing.T) {
	c := begin(t, "platform/identity")
	c.check(ident.Model != "" && ident.Vendor != "", "identity %s %s, serial %s, %s", ident.Vendor, ident.Model, ident.Serial, ident.ADCPart)

	c = begin(t, "platform/profile")
	p := board.Profile()
	c.check(p.Channels == 2 || p.Channels == 4, "%d channels", p.Channels)
	c.noErr(p.Format.Validate(), "code format valid")
	c.check(p.Display.W == 1024 && p.Display.H == 600 && p.Display.Format == hal.ARGB8888, "display %dx%d", p.Display.W, p.Display.H)
	c.check(len(p.Timebase.TdivS) > 20, "%d time/div steps", len(p.Timebase.TdivS))

	c = begin(t, "platform/diag")
	var d map[string]any
	c.noErr(json.Unmarshal(board.Platform().Diag(), &d), "diag is JSON")

	// The boot target toggles p12's disable file, which the launch hook
	// honours at the next power-up, and back.
	c = begin(t, "platform/boot-target")
	var boot *hal.SCPIHeader
	for _, x := range board.Extensions() {
		for i := range x.SCPI {
			if x.SCPI[i].Header == ":SYSTem:BOOT" {
				boot = &x.SCPI[i]
			}
		}
	}
	if !c.check(boot != nil, ":SYSTem:BOOT extension present") {
		return
	}
	disabled := func() bool { _, err := os.Stat(platform.DisableFile); return err == nil }
	c.noErr(boot.Command("VENDOR"), "SYSTem:BOOT VENDOR")
	q, _ := boot.Query("")
	c.check(disabled() && string(q) == "VENDOR", "VENDOR: disable file %v, query %s", disabled(), q)
	c.noErr(boot.Command("OPEN"), "SYSTem:BOOT OPEN")
	q, _ = boot.Query("")
	c.check(!disabled() && string(q) == "OPEN", "OPEN: disable file %v, query %s", disabled(), q)
}
