package board

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labeth/open-sds800xhd/ota/internal/slots"
)

// fakeSupervisor supervises b on a fake clock; app says whether a launch
// starts an app (pid 50, the current slot's binary) that keeps running.
func fakeSupervisor(t *testing.T, b *Board, app func(n int) bool) (*Supervisor, *int) {
	t.Helper()
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	launches := 0
	s := &Supervisor{
		B: b, Grace: 6 * time.Second,
		Launch: func() error {
			launches++
			if app(launches) {
				fakeProcs(t, b.Root, map[int][]string{50: {b.Slots().App()}}, nil, nil)
			}
			return nil
		},
		Stop:  func(pid int) { b.signal(pid, 9) },
		Sleep: func(d time.Duration) { now = now.Add(d) },
		Now:   func() time.Time { return now },
		Logf:  func(f string, a ...any) { t.Logf(f, a...) },
	}
	return s, &launches
}

func bootstrapped(t *testing.T) *Board {
	t.Helper()
	b, _ := testBoard(t)
	if err := os.MkdirAll(b.SlotsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := b.Slots().Bootstrap([]byte("x"), "1"); err != nil {
		t.Fatal(err)
	}
	return b
}

// An app that never comes up costs MaxStarts starts per boot and
// MaxReboots counted reboots; then the open firmware is disabled and
// nothing more is started.
func TestSuperviseCrashLoopIsBounded(t *testing.T) {
	b := bootstrapped(t)
	s, launches := fakeSupervisor(t, b, func(int) bool { return false })
	if err := s.Run(); err != nil {
		t.Fatal(err)
	}
	if !b.Slots().Status().Disabled {
		t.Fatal("not disabled after the crash loop")
	}
	// Boot 1 starts once (the hook) and MaxStarts−1 more times, as does
	// every boot after a reboot (the reboot does not happen in the test:
	// each retry stands for one).
	if want := (MaxReboots+1)*MaxStarts - 1; *launches != want {
		t.Errorf("%d launches, want %d", *launches, want)
	}
	log, _ := os.ReadFile(filepath.Join(b.SlotsDir(), "log", "reboot.log"))
	if n := strings.Count(string(log), "reboot "); n != MaxReboots {
		t.Errorf("%d reboots logged, want %d:\n%s", n, MaxReboots, log)
	}
}

// A healthy app is confirmed after the grace period and the reboot count
// starts again; when it dies it is started again.
func TestSuperviseConfirmsAndRestarts(t *testing.T) {
	b := bootstrapped(t)
	if _, err := b.Slots().Install([]byte("y"), "2", slots.DigestOf([]byte("y"))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.rebootsPath(), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, launches := fakeSupervisor(t, b, func(int) bool { return true })
	fakeProcs(t, b.Root, map[int][]string{50: {b.Slots().App()}}, nil, nil) // the hook's start
	polls := 0
	s.Done = func() bool {
		polls++
		if polls == 40 { // the app dies once
			_ = os.RemoveAll(filepath.Join(b.Root, "proc", "50"))
		}
		return *launches >= 1 && polls > 80
	}
	if err := s.Run(); err != nil {
		t.Fatal(err)
	}
	st := b.Slots().Status()
	if st.Trial || st.Current != "b" || b.reboots() != 0 {
		t.Fatalf("after a healthy app: %+v, reboots %d", st, b.reboots())
	}
	if *launches != 1 {
		t.Errorf("%d launches after one death, want 1", *launches)
	}
}

// A reboot the policy cannot count (p12 read-only) ends the supervision:
// no more starts this boot.
func TestSuperviseStopsWhenRebootsCannotBeCounted(t *testing.T) {
	b := bootstrapped(t)
	if err := os.Mkdir(b.rebootsPath(), 0o755); err != nil { // the count cannot be written
		t.Fatal(err)
	}
	s, launches := fakeSupervisor(t, b, func(int) bool { return false })
	err := s.Run()
	if !errors.Is(err, ErrUncounted) {
		t.Fatalf("Run: %v, want ErrUncounted", err)
	}
	if *launches != MaxStarts-1 {
		t.Errorf("%d launches, want %d", *launches, MaxStarts-1)
	}
}
