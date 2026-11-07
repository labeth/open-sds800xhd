// Package plimage is the gate in front of loading an open PL image through
// fpga_manager (spec 13 §3.3, spec 20 §12).
//
// An image drives the PL's pins. Where a ball's function on this board is
// still UNVERIFIED (spec 20 §14), an image that drives it may short an
// output against another driver or a rail and damage the board, which no
// stick can undo. The build refuses a bitstream while fpga/vivado/
// verified.tcl leaves a needed board fact open, and writes the facts it was
// built against into the image's manifest (<image>.json next to the .bin).
// This package refuses to load an image whose manifest does not declare
// every board fact verified, or whose bytes are not the ones the manifest
// describes; an image built before the gate, or edited after it, never
// reaches the PL.
package plimage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/labeth/open-sds800xhd/app/internal/platform"
)

// RequiredFacts are the board facts of spec 20 §14 an image depends on: the
// keys of fpga/vivado/verified.tcl.
var RequiredFacts = []string{"U1", "U2", "U3", "U4", "U5", "U6", "U7", "U8", "U9", "U10", "U13", "U14", "U15"}

// ErrUnverified is what a refused image wraps.
var ErrUnverified = errors.New("open PL image refused")

// Manifest is an image's build manifest as fpga/vivado/build.tcl writes it.
type Manifest struct {
	Image              string          `json:"image"`
	SHA256             string          `json:"sha256"`
	Build              string          `json:"build"`
	BoardFacts         map[string]bool `json:"board_facts"`
	BoardFactsVerified bool            `json:"board_facts_verified"`
}

// Verified returns nil when the manifest declares the board facts verified
// and lists every required fact as verified.
// TRLC-LINKS: REQ-HD-016
func (m Manifest) Verified() error {
	if !m.BoardFactsVerified {
		return fmt.Errorf("%w: image %q: its manifest does not declare the board facts verified (spec 20 §14)", ErrUnverified, m.Image)
	}
	var open []string
	for _, f := range RequiredFacts {
		if !m.BoardFacts[f] {
			open = append(open, f)
		}
	}
	if len(open) > 0 {
		return fmt.Errorf("%w: image %q: board facts not verified: %s (spec 20 §14)", ErrUnverified, m.Image, strings.Join(open, " "))
	}
	return nil
}

// Check returns nil when the manifest is Verified and describes bin.
// TRLC-LINKS: REQ-HD-016
func Check(m Manifest, bin []byte) error {
	if err := m.Verified(); err != nil {
		return err
	}
	sum := sha256.Sum256(bin)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, m.SHA256) {
		return fmt.Errorf("%w: image %q: sha256 %s, its manifest says %q", ErrUnverified, m.Image, got, m.SHA256)
	}
	return nil
}

// ReadManifest reads the manifest of the image at bin (bin without its
// extension, plus .json).
// TRLC-LINKS: REQ-HD-016
func ReadManifest(bin string) (Manifest, error) {
	var m Manifest
	p := strings.TrimSuffix(bin, filepath.Ext(bin)) + ".json"
	b, err := os.ReadFile(p) //nolint:gosec // G304: the manifest next to the image the board loads
	if err != nil {
		return m, fmt.Errorf("%w: no manifest: %w", ErrUnverified, err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("%w: manifest %s: %w", ErrUnverified, p, err)
	}
	return m, nil
}

// Sysfs is the attribute access a load needs.
type Sysfs interface {
	WriteAttr(path, v string) error
	ReadAttr(path string) (string, error)
}

// HostSysfs is the unit's sysfs, its writes through the write guard.
type HostSysfs struct{}

// WriteAttr writes one attribute.
// TRLC-LINKS: REQ-HD-016
func (HostSysfs) WriteAttr(path, v string) error { return platform.WriteAttr(path, v) }

// ReadAttr reads one attribute.
// TRLC-LINKS: REQ-HD-016
func (HostSysfs) ReadAttr(path string) (string, error) { return platform.ReadAttr(path) }

// fpga_manager and the firmware loader's attributes (spec 13 §3.3).
const (
	firmwarePath = "/sys/module/firmware_class/parameters/path"
	fpgaFlags    = "/sys/class/fpga_manager/fpga0/flags"
	fpgaFirmware = "/sys/class/fpga_manager/fpga0/firmware"
	fpgaState    = "/sys/class/fpga_manager/fpga0/state"
)

// Load configures the PL with the image at bin after Check: nothing reaches
// fpga_manager for an image whose manifest is missing, unverified or does
// not match. It points the firmware loader at bin's directory (no rw
// remount of /), asks for a full, unencrypted, unauthenticated load and
// checks the state. The caller has stopped every user of the PL before
// (spec 13 §3.3).
// TRLC-LINKS: REQ-HD-016, REQ-HD-015
func Load(fs Sysfs, bin string) error {
	m, err := ReadManifest(bin)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(bin) //nolint:gosec // G304: the image the board loads
	if err != nil {
		return err
	}
	if err := Check(m, b); err != nil {
		return err
	}
	for _, w := range [][2]string{{firmwarePath, filepath.Dir(bin)}, {fpgaFlags, "0"}, {fpgaFirmware, filepath.Base(bin)}} {
		if err := fs.WriteAttr(w[0], w[1]); err != nil {
			return err
		}
	}
	st, err := fs.ReadAttr(fpgaState)
	if err != nil {
		return err
	}
	if st != "operating" {
		return fmt.Errorf("fpga_manager state %q after loading %s: no PL register may be touched", st, filepath.Base(bin))
	}
	return nil
}
