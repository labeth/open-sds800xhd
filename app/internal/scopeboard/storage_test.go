package scopeboard

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labeth/open-dso-core/fsguard"
	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-sds800xhd/app/hdguard"
)

// guardedDir is a temporary directory the write guard is rooted at for one
// test.
func guardedDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	restore, err := hdguard.FS.SetRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	return dir
}

func TestFileKV(t *testing.T) {
	kv := &fileKV{dir: guardedDir(t), budget: 100}
	if err := kv.Put("settings", bytes.Repeat([]byte{'a'}, 60)); err != nil {
		t.Fatal(err)
	}
	if v, ok := kv.Get("settings"); !ok || len(v) != 60 {
		t.Fatalf("get %d %v", len(v), ok)
	}
	// Replacing a value counts only the new size.
	if err := kv.Put("settings", bytes.Repeat([]byte{'b'}, 90)); err != nil {
		t.Fatal(err)
	}
	err := kv.Put("other", make([]byte, 20))
	if _, ok := errors.AsType[*hal.Refusal](err); !ok {
		t.Fatalf("past the budget: %v", err)
	}
	if _, ok := kv.Get("other"); ok {
		t.Error("a refused value was stored")
	}
	if v, _ := kv.Get("settings"); v[0] != 'b' {
		t.Error("value not replaced")
	}
	for _, k := range []string{"", "../x", "a/b", ".hidden", "x.new"} {
		if kv.Put(k, nil) == nil {
			t.Errorf("key %q accepted", k)
		}
	}
	es, _ := os.ReadDir(kv.dir)
	for _, e := range es {
		if strings.HasSuffix(e.Name(), ".new") {
			t.Errorf("temporary %s left", e.Name())
		}
	}
}

func TestSetupSlots(t *testing.T) {
	kv := &fileKV{dir: guardedDir(t), budget: 1 << 10, slots: 20}
	for _, k := range []string{"1", "20"} {
		if err := kv.Put(k, []byte(k)); err != nil {
			t.Errorf("slot %s: %v", k, err)
		}
	}
	for _, k := range []string{"0", "21", "01", "a"} {
		if _, ok := hal.AsRefusal(kv.Put(k, nil)); !ok {
			t.Errorf("slot %q not refused", k)
		}
	}
	if _, err := os.Stat(filepath.Join(kv.dir, "20")); err != nil {
		t.Error(err)
	}
}

func TestCaptureDir(t *testing.T) {
	mounts := `/dev/root / ext4 ro 0 0
/dev/mmcblk0p12 /usr/bin/siglent/usr ext4 rw 0 0
/dev/sda1 /mnt/usb\040stick vfat rw,relatime 0 0
`
	if d, err := captureDir(strings.NewReader(mounts)); err != nil || d != "/mnt/usb stick/open-captures" {
		t.Errorf("%q %v", d, err)
	}
	if d, _ := captureDir(strings.NewReader("/dev/sda1 /mnt/x vfat ro 0 0\n")); d != "" {
		t.Errorf("read-only stick used: %q", d)
	}
	if d, _ := captureDir(strings.NewReader("/dev/sdb1 /simd ext4 rw 0 0\n")); d != "" {
		t.Errorf("a disk that is not a stick: %q", d)
	}
	if d, _ := captureDir(strings.NewReader("/dev/root / ext4 ro 0 0\n")); d != "" {
		t.Errorf("no stick: %q", d)
	}
}

// A store whose directory is outside p12 (here: the write guard's default
// root, p12 itself, does not hold the test's directory) is refused and
// writes nothing.
// TRLC-LINKS: REQ-HD-016
func TestFileKVOutsideP12Refused(t *testing.T) {
	kv := &fileKV{dir: t.TempDir(), budget: 100}
	if err := kv.Put("settings", []byte("x")); !errors.Is(err, fsguard.ErrRefused) {
		t.Fatalf("put outside p12: %v", err)
	}
	if es, _ := os.ReadDir(kv.dir); len(es) != 0 {
		t.Errorf("%d files written outside p12", len(es))
	}
}
