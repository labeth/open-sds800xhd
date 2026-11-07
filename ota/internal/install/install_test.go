package install

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/labeth/open-dso-core/fsguard"
	"github.com/labeth/open-sds800xhd/app/hdguard"
)

// guardRoot roots the write guard at dir for one test.
func guardRoot(t *testing.T, dir string) {
	t.Helper()
	restore, err := hdguard.FS.SetRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
}

func write(t *testing.T, p, s string, mode os.FileMode) {
	t.Helper()
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), mode); err != nil {
		t.Fatal(err)
	}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestInstallUninstallCycle(t *testing.T) {
	src, p12 := t.TempDir(), t.TempDir()
	guardRoot(t, p12)
	write(t, filepath.Join(src, "extra.sh"), "#!/bin/sh\n", 0o644)
	write(t, filepath.Join(src, "open/a/app"), "app", 0o755)
	write(t, filepath.Join(src, "open/current"), "a\n", 0o644)
	write(t, filepath.Join(p12, "open/removed-1"), "1\n", 0o644)
	write(t, filepath.Join(p12, "config/keep"), "user data", 0o644)

	if err := Payload(src, p12, "open/installed-1", "v1"); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(p12, "extra.sh"))
	if err != nil || st.Mode().Perm() != 0o755 {
		t.Fatalf("hook: %v %v", st, err)
	}
	if st, _ := os.Stat(filepath.Join(p12, "open/a/app")); st.Mode().Perm() != 0o755 {
		t.Fatalf("app mode %v", st.Mode())
	}
	if exists(filepath.Join(p12, "open/removed-1")) || !exists(filepath.Join(p12, "open/installed-1")) {
		t.Fatal("markers after install")
	}
	if !exists(filepath.Join(p12, "config/keep")) {
		t.Fatal("user data touched")
	}
	matches, _ := filepath.Glob(filepath.Join(p12, "*", "*.new"))
	if len(matches) > 0 {
		t.Fatalf("temporary files left: %v", matches)
	}

	if err := Uninstall(p12, "open/removed-1", "v1"); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(p12, "extra.sh")) || exists(filepath.Join(p12, "open/installed-1")) || !exists(filepath.Join(p12, "open/removed-1")) {
		t.Fatal("state after uninstall")
	}
	if !exists(filepath.Join(p12, "open/a/app")) {
		t.Fatal("uninstall deleted the slots")
	}
}

// A failing payload file stops the install before the hook and the marker.
func TestInstallFailureWritesNoMarker(t *testing.T) {
	src, p12 := t.TempDir(), t.TempDir()
	guardRoot(t, p12)
	write(t, filepath.Join(src, "extra.sh"), "#!/bin/sh\n", 0o644)
	write(t, filepath.Join(src, "open/a/app"), "app", 0o755)
	write(t, filepath.Join(p12, "open"), "a file where a directory must be", 0o644)
	if err := Payload(src, p12, "open/installed-1", "v1"); err == nil {
		t.Fatal("install over a blocked directory succeeded")
	}
	if exists(filepath.Join(p12, "extra.sh")) {
		t.Fatal("the hook was installed although the payload failed")
	}
}

// A payload that would reach past p12 (a symlink on p12 leading out, or
// p12/updatefile/, which U-Boot flashes to p1) is refused by the write
// guard before the hook and the marker, and nothing lands outside.
// TRLC-LINKS: REQ-HD-016
func TestInstallRefusesPastP12(t *testing.T) {
	outside := t.TempDir()
	for name, setup := range map[string]func(src, p12 string){
		"symlink out": func(src, p12 string) {
			write(t, filepath.Join(src, "open/log/x"), "x", 0o644)
			if err := os.Symlink(outside, filepath.Join(p12, "open")); err != nil {
				t.Fatal(err)
			}
		},
		"updatefile": func(src, _ string) {
			write(t, filepath.Join(src, "updatefile/BOOT.bin"), "boot", 0o644)
		},
		"uboot.env": func(src, _ string) {
			write(t, filepath.Join(src, "uboot.env"), "bootcmd=x", 0o644)
		},
	} {
		src, p12 := t.TempDir(), t.TempDir()
		guardRoot(t, p12)
		write(t, filepath.Join(src, "extra.sh"), "#!/bin/sh\n", 0o644)
		setup(src, p12)
		err := Payload(src, p12, "marker-1", "v1")
		if !errors.Is(err, fsguard.ErrRefused) {
			t.Errorf("%s: %v", name, err)
		}
		if exists(filepath.Join(p12, "extra.sh")) || exists(filepath.Join(p12, "marker-1")) {
			t.Errorf("%s: hook or marker written after a refusal", name)
		}
		if es, _ := os.ReadDir(outside); len(es) > 0 {
			t.Errorf("%s: %d files written outside p12", name, len(es))
		}
	}
}
