package slots

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labeth/open-sds800xhd/ota/fsguard"
)

// fakeApp is an app that records its start in OTA_DIR/started.
func fakeApp(name string) []byte {
	return []byte("#!/bin/sh\necho \"" + name + " $OTA_SLOT\" >>\"$OTA_DIR/started\"\n")
}

// guardRoot roots the write guard at dir for one test.
func guardRoot(t *testing.T, dir string) {
	t.Helper()
	restore, err := fsguard.SetRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
}

func p12(t *testing.T) (string, Layout) {
	t.Helper()
	root := t.TempDir()
	guardRoot(t, root)
	hook, err := os.ReadFile("../../boot/extra.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "extra.sh"), hook, 0o755); err != nil {
		t.Fatal(err)
	}
	l := Layout{Dir: filepath.Join(root, "open")}
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return root, l
}

// boot runs the launch hook as S99extra does and returns the app it started.
func boot(t *testing.T, root string, l Layout) string {
	t.Helper()
	_ = os.Remove(filepath.Join(l.Dir, "started"))
	if out, err := exec.CommandContext(t.Context(), "sh", filepath.Join(root, "extra.sh")).CombinedOutput(); err != nil {
		t.Fatalf("hook: %v: %s", err, out)
	}
	for range 100 {
		if b, err := os.ReadFile(filepath.Join(l.Dir, "started")); err == nil && len(b) > 0 {
			return strings.TrimSpace(string(b))
		}
		time.Sleep(20 * time.Millisecond)
	}
	return ""
}

func TestInstallConfirm(t *testing.T) {
	root, l := p12(t)
	if err := l.Bootstrap(fakeApp("good"), "1"); err != nil {
		t.Fatal(err)
	}
	if got := boot(t, root, l); got != "good a" {
		t.Fatalf("first boot started %q", got)
	}
	s, err := l.Install(fakeApp("new"), "2")
	if err != nil || s != B {
		t.Fatalf("install: %s %v", s, err)
	}
	st := l.Status()
	if st.Current != B || st.Previous != A || !st.Trial || st.Starts != 0 || st.Versions[B] != "2" {
		t.Fatalf("after install: %+v", st)
	}
	if _, err := l.Install(fakeApp("other"), "3"); !errors.Is(err, ErrOnTrial) {
		t.Fatalf("install on trial: %v", err)
	}
	if got := boot(t, root, l); got != "new b" {
		t.Fatalf("trial boot started %q", got)
	}
	if st := l.Status(); st.Starts != 1 {
		t.Fatalf("starts %d", st.Starts)
	}
	if err := l.Confirm(); err != nil {
		t.Fatal(err)
	}
	if got := boot(t, root, l); got != "new b" {
		t.Fatalf("confirmed boot started %q", got)
	}
	if st := l.Status(); st.Trial {
		t.Fatalf("still on trial: %+v", st)
	}
}

// An unconfirmed slot gets one start; the hook's next start goes back.
func TestHookRollsBackUnconfirmed(t *testing.T) {
	root, l := p12(t)
	_ = l.Bootstrap(fakeApp("good"), "1")
	if _, err := l.Install([]byte("#!/bin/sh\nexit 3\n"), "broken"); err != nil {
		t.Fatal(err)
	}
	_ = boot(t, root, l) // the broken app starts and dies; nothing confirms it
	if st := l.Status(); st.Current != B || st.Starts != 1 {
		t.Fatalf("after the trial start: %+v", st)
	}
	if got := boot(t, root, l); got != "good a" {
		t.Fatalf("second boot started %q, want the previous slot", got)
	}
	st := l.Status()
	if st.Current != A || st.Trial {
		t.Fatalf("after rollback: %+v", st)
	}
	log, _ := os.ReadFile(filepath.Join(l.Dir, "log", "launch.log"))
	if !strings.Contains(string(log), "back to slot a") {
		t.Fatalf("launch log: %s", log)
	}
}

func TestRollbackAndDisable(t *testing.T) {
	root, l := p12(t)
	_ = l.Bootstrap(fakeApp("good"), "1")
	if _, err := l.Rollback(); err == nil {
		t.Fatal("rollback without a previous slot")
	}
	_, _ = l.Install(fakeApp("new"), "2")
	if s, err := l.Rollback(); err != nil || s != A {
		t.Fatalf("rollback: %s %v", s, err)
	}
	if st := l.Status(); st.Current != A || st.Trial {
		t.Fatalf("after rollback: %+v", st)
	}
	_ = os.WriteFile(filepath.Join(l.Dir, "disable"), nil, 0o644)
	if got := boot(t, root, l); got != "" {
		t.Fatalf("disabled hook started %q", got)
	}
}

// Before slots exist the hook starts open/app, the single-binary layout.
func TestHookSingleBinary(t *testing.T) {
	root, l := p12(t)
	_ = os.WriteFile(filepath.Join(l.Dir, "app"), fakeApp("single"), 0o755)
	if got := boot(t, root, l); got != "single a" {
		t.Fatalf("started %q", got)
	}
}

// A torn write leaves the old file whole: WriteFile renames a complete
// temporary file, so only *.new can be partial.
func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	guardRoot(t, dir)
	p := filepath.Join(dir, "current")
	if err := WriteFile(p, []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(p+".new", []byte("b"), 0o644) // a write cut short before its rename
	if b, _ := os.ReadFile(p); string(b) != "a\n" {
		t.Fatalf("current = %q", b)
	}
	if st := (Layout{Dir: dir}).Status(); st.Current != A {
		t.Fatalf("status %+v", st)
	}
	if err := WriteFile(p, []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".new"); !os.IsNotExist(err) {
		t.Fatal("temporary file left behind")
	}
}

// The Acquire key held at power-up (MAP 0x80040588 bit 3 low) leaves this
// boot to the vendor firmware without writing the disable file or using
// up a trial start; released, the next boot starts the app.
// TRLC-LINKS: REQ-HD-002
func TestHookKeyHeld(t *testing.T) {
	root, l := p12(t)
	if err := l.Bootstrap(fakeApp("one"), "1"); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	devmem := func(word string) {
		s := "#!/bin/sh\n[ \"$1\" = 0x80040588 ] && echo " + word + "\n"
		if err := os.WriteFile(filepath.Join(bin, "devmem"), []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	hook := func() string {
		_ = os.Remove(filepath.Join(l.Dir, "started"))
		cmd := exec.CommandContext(t.Context(), "sh", filepath.Join(root, "extra.sh"))
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("hook: %v: %s", err, out)
		}
		time.Sleep(300 * time.Millisecond)
		b, _ := os.ReadFile(filepath.Join(l.Dir, "started"))
		return strings.TrimSpace(string(b))
	}
	devmem("0x0000FFF7")
	if got := hook(); got != "" {
		t.Fatalf("hook started %q with Acquire held", got)
	}
	if l.Status().Disabled {
		t.Fatal("a held key wrote the disable file")
	}
	if lg, _ := os.ReadFile(filepath.Join(l.Dir, "log", "launch.log")); !strings.Contains(string(lg), "Acquire held") {
		t.Errorf("launch log %q", lg)
	}
	devmem("0x0000FFFF")
	if got := hook(); got != "one a" {
		t.Fatalf("hook started %q with no key held", got)
	}
}
