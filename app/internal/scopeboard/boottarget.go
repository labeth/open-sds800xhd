package scopeboard

import (
	"github.com/labeth/open-dso-core/boottarget"
	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-sds800xhd/app/hdguard"
	"github.com/labeth/open-sds800xhd/app/internal/platform"
)

// bootSwitch is the owner's "next boot: open / vendor firmware" choice:
// the launch hook's disable file in the open directory dir, written
// through the write guard (spec 01 §12.3). Nothing is uninstalled, and the
// running app is not stopped.
// TRLC-LINKS: REQ-HD-002, REQ-HD-016
func bootSwitch(dir string) boottarget.Switch {
	return boottarget.Switch{Guard: hdguard.FS, Dir: dir}
}

// BootTarget is the boot target of p12's open directory; the core shows it
// in the SYS sheet.
// TRLC-LINKS: REQ-HD-002
func (b *Board) BootTarget() (hal.BootTarget, bool) { return bootSwitch(platform.OpenDir), true }

// Extensions are the board's own features: the boot target as :SYSTem:BOOT
// and /api/boot.
// TRLC-LINKS: REQ-HD-002
func (b *Board) Extensions() []hal.Extension {
	return []hal.Extension{boottarget.Extension(bootSwitch(platform.OpenDir))}
}
