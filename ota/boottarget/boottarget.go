// Package boottarget is the owner's choice of what the next power-up runs:
// the open firmware or the vendor firmware. The launch hook (boot/extra.sh)
// exits at once, leaving the unit to the vendor firmware, while the disable
// file open/disable exists on p12 (spec 01 §12.3). The supervisor writes it
// itself after too many unhealthy boots; otactl, SCPI (:SYSTem:BOOT) and the
// web page write or remove it here, through the write guard. Nothing is
// uninstalled: removing the file runs the open firmware again.
package boottarget

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/labeth/open-sds800xhd/ota/fsguard"
)

// The two targets.
const (
	Open   = "open"
	Vendor = "vendor"
)

// DisableName is the disable file's name in the open directory.
const DisableName = "disable"

// Next returns the target of the next power-up for the open directory dir
// (p12/open): Vendor while the disable file exists.
// TRLC-LINKS: REQ-HD-002, REQ-HD-016
func Next(dir string) string {
	if _, err := os.Lstat(filepath.Join(dir, DisableName)); err == nil {
		return Vendor
	}
	return Open
}

// Set makes target the next power-up's: Vendor writes the disable file with
// reason as its content, Open removes it. Both go through the write guard.
// TRLC-LINKS: REQ-HD-002, REQ-HD-016
func Set(dir, target, reason string) error {
	p := filepath.Join(dir, DisableName)
	switch target {
	case Vendor:
		if err := fsguard.MkdirAll(dir); err != nil {
			return err
		}
		if reason == "" {
			reason = "asked for the vendor firmware"
		}
		return fsguard.WriteFile(p, []byte(strings.TrimSpace(reason)+"\n"), 0o644)
	case Open:
		return fsguard.Remove(p)
	}
	return fmt.Errorf("boot target %q: %w", target, ErrTarget)
}

// ErrTarget is a target other than Open and Vendor.
var ErrTarget = errors.New("the boot targets are open and vendor")

// Parse reads a target as SCPI, the web page and otactl spell it:
// OPEN/VENDOR in any case, or the SCPI short forms OPEN/VEND.
// TRLC-LINKS: REQ-HD-002
func Parse(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "open":
		return Open, nil
	case "vendor", "vend":
		return Vendor, nil
	}
	return "", fmt.Errorf("boot target %q: %w", s, ErrTarget)
}
