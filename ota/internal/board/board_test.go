package board

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-sds800xhd/app/hdguard"
)

// fakeProcs builds /proc entries: pid → argv, parent, state.
func fakeProcs(t *testing.T, root string, ps map[int][]string, ppid map[int]int, state map[int]string) {
	t.Helper()
	for pid, argv := range ps {
		d := filepath.Join(root, "proc", strconv.Itoa(pid))
		_ = os.MkdirAll(d, 0o755)
		_ = os.WriteFile(filepath.Join(d, "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0o644)
		st := state[pid]
		if st == "" {
			st = "S"
		}
		_ = os.WriteFile(filepath.Join(d, "stat"),
			[]byte(strconv.Itoa(pid)+" (x y) "+st+" "+strconv.Itoa(ppid[pid])+" 0 0\n"), 0o644)
	}
}

// guardRoot roots the write guard at dir for one test.
func guardRoot(t *testing.T, dir string) {
	t.Helper()
	restore, err := hdguard.FS.SetRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
}

func testBoard(t *testing.T) (*Board, *[]string) {
	t.Helper()
	root := t.TempDir()
	guardRoot(t, filepath.Join(root, "p12"))
	var sent []string
	b := &Board{P12: filepath.Join(root, "p12"), Root: root}
	b.signal = func(pid int, sig syscall.Signal) {
		sent = append(sent, strconv.Itoa(pid)+":"+sig.String())
		_ = os.RemoveAll(filepath.Join(root, "proc", strconv.Itoa(pid))) // the process goes
	}
	b.reboot = func() error { return errors.New("rebooted") }
	return b, &sent
}

func TestTakeoverOrder(t *testing.T) {
	b, sent := testBoard(t)
	fakeProcs(t, b.Root, map[int][]string{
		10: {"ash", "/usr/bin/siglent/drivers/insmod_after_app.sh"},
		11: {"sleep", "10"},
		20: {"/usr/bin/siglent/main.app"},
		21: {"/usr/bin/siglent/vncserver"},
		30: {"lighttpd", "-f", "x"},
		31: {"/usr/sbin/rpcbind"},
	}, map[int]int{11: 10}, nil)
	if err := b.Takeover(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"10:killed", "11:killed"}
	if len(*sent) < 2 || !slices.Equal((*sent)[:2], want) {
		t.Fatalf("first signals %v, want the script then its sleep", *sent)
	}
	for _, s := range []string{"20:terminated", "21:terminated", "30:killed"} {
		if !slices.Contains(*sent, s) {
			t.Errorf("no %s in %v", s, *sent)
		}
	}
	for _, s := range *sent {
		if strings.HasPrefix(s, "31:") {
			t.Errorf("rpcbind signalled: %v", *sent)
		}
	}
}

func TestRebootPolicy(t *testing.T) {
	b, _ := testBoard(t)
	for i := range MaxReboots {
		if err := b.Reboot("app hung"); err == nil || err.Error() != "rebooted" {
			t.Fatalf("reboot %d: %v", i+1, err)
		}
	}
	err := b.Reboot("app hung")
	if _, ok := hal.AsRefusal(err); !ok {
		t.Fatalf("reboot past the limit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(b.SlotsDir(), "disable")); err != nil {
		t.Fatal("not disabled after the refused reboot")
	}
	_ = os.Remove(filepath.Join(b.SlotsDir(), "disable"))
	if err := b.Confirmed(); err != nil {
		t.Fatal(err)
	}
	if err := b.Reboot("again"); err == nil || err.Error() != "rebooted" {
		t.Fatalf("reboot after a healthy app: %v", err)
	}
	log, _ := os.ReadFile(filepath.Join(b.SlotsDir(), "log", "reboot.log"))
	if strings.Count(string(log), "\n") != MaxReboots+2 {
		t.Fatalf("reboot log:\n%s", log)
	}
}

func TestHealthAndAppPID(t *testing.T) {
	b, _ := testBoard(t)
	l := b.Slots()
	_ = os.MkdirAll(l.Dir, 0o755)
	_ = l.Bootstrap([]byte("x"), "1")
	app := filepath.Join(b.SlotsDir(), "a", "app")
	fakeProcs(t, b.Root, map[int][]string{40: {app}, 41: {"/bin/sh", app}, 42: {app}},
		nil, map[int]string{40: "Z"})
	pid := b.AppPID()
	if pid != 41 && pid != 42 {
		t.Fatalf("app pid %d", pid)
	}
	now := time.Now()
	if !b.Healthy(42, now) {
		t.Fatal("a running app without a token is healthy")
	}
	_ = os.WriteFile(b.HealthToken(), []byte("ok"), 0o644)
	if b.Healthy(42, now.Add(HealthMaxAge+time.Second)) {
		t.Fatal("a stale token is healthy")
	}
	if b.Healthy(40, now) {
		t.Fatal("a zombie is healthy")
	}
	env, files := b.LaunchEnv()
	if files != nil || !slices.Contains(env, "OTA_SLOT=a") {
		t.Fatalf("launch env %v %v", env, files)
	}
	if _, ok := b.Watchdog(); ok {
		t.Fatal("a watchdog on a scope without one")
	}
}

// On the unit Root is "": /proc is the absolute one.
func TestAliveOnTheRealProc(t *testing.T) {
	b := New(nil)
	if !b.Alive(os.Getpid()) {
		t.Fatal("this process is not alive in /proc")
	}
}

// The census is the supervisor's: only its read-only form runs.
func TestCensusRefusesWrites(t *testing.T) {
	b := &Board{Root: t.TempDir()}
	if _, err := b.Census().Run(context.Background(), true); err == nil {
		t.Fatal("an active census ran")
	} else if _, ok := hal.AsRefusal(err); !ok {
		t.Fatalf("active census: %v, want a refusal", err)
	}
}

// The owner's boot target: vendor writes the disable file the launch hook
// honours, open removes it, and an asked reboot does not count towards the
// unhealthy-reboot policy.
// TRLC-LINKS: REQ-HD-002, REQ-HD-016
func TestBootTarget(t *testing.T) {
	b, _ := testBoard(t)
	if b.BootTarget() != hal.BootOpen {
		t.Fatal("fresh unit boots open")
	}
	if err := b.SetBootTarget(hal.BootVendor, "test"); err != nil {
		t.Fatal(err)
	}
	if b.BootTarget() != hal.BootVendor || !b.Slots().Status().Disabled {
		t.Fatal("vendor not set")
	}
	if err := b.RebootNow("test"); err == nil || err.Error() != "rebooted" {
		t.Fatalf("reboot now: %v", err)
	}
	if b.reboots() != 0 {
		t.Errorf("an asked reboot counted: %d", b.reboots())
	}
	if err := b.SetBootTarget(hal.BootOpen, "test"); err != nil {
		t.Fatal(err)
	}
	if b.BootTarget() != hal.BootOpen {
		t.Fatal("open not set")
	}
}
