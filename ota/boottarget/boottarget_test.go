package boottarget

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/labeth/open-sds800xhd/ota/fsguard"
)

// TRLC-LINKS: REQ-HD-002, REQ-HD-016
func TestSetAndNext(t *testing.T) {
	root := t.TempDir()
	restore, err := fsguard.SetRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	dir := filepath.Join(root, "open")
	if Next(dir) != Open {
		t.Fatal("a fresh p12 boots the open firmware")
	}
	if err := Set(dir, Vendor, "owner"); err != nil {
		t.Fatal(err)
	}
	if Next(dir) != Vendor {
		t.Fatal("vendor not set")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, DisableName)); string(b) != "owner\n" {
		t.Errorf("disable file %q", b)
	}
	if err := Set(dir, Open, ""); err != nil {
		t.Fatal(err)
	}
	if Next(dir) != Open {
		t.Fatal("open not set")
	}
	if err := Set(dir, Open, ""); err != nil {
		t.Errorf("open twice: %v", err)
	}
	if err := Set(dir, "x", ""); !errors.Is(err, ErrTarget) {
		t.Errorf("bad target: %v", err)
	}
	// Outside p12 the write guard refuses.
	if err := Set(t.TempDir(), Vendor, ""); !errors.Is(err, fsguard.ErrRefused) {
		t.Errorf("outside p12: %v", err)
	}
	for in, want := range map[string]string{"OPEN": Open, "vend": Vendor, "Vendor\n": Vendor} {
		if got, err := Parse(in); err != nil || got != want {
			t.Errorf("parse %q: %q %v", in, got, err)
		}
	}
	if _, err := Parse("on"); err == nil {
		t.Error("parse on accepted")
	}
}
