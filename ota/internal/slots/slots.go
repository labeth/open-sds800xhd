// Package slots is the A/B slot layout of the open firmware on p12 and the
// atomic file writes everything on p12 uses (spec 01 §8, §12.4).
//
// Layout under the open directory (p12/open):
//
//	a/app, b/app        the two slots' app binaries; a/version, b/version
//	current             "a" or "b": the slot the launch hook starts
//	previous            the slot to go back to when current fails
//	trial               "<slot> <starts>" while current is not yet confirmed
//	disable             present: the launch hook leaves the unit to the vendor firmware
//	log/                launch.log, reboot.log
//
// A freshly installed slot runs on trial. The launch hook (boot/extra.sh)
// counts its starts in trial; the supervisor confirms it once the app is
// healthy, which deletes trial. A start of a slot still on trial after one
// unconfirmed start goes back to previous. The hook never changes after the
// install stick put it there, so this layout is its contract.
package slots

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/labeth/open-sds800xhd/ota/fsguard"
)

// The slot names.
const (
	A = "a"
	B = "b"
)

// MaxUnconfirmedStarts is how many starts a slot on trial gets before the
// launch hook goes back to the previous slot (boot/extra.sh uses the same).
const MaxUnconfirmedStarts = 1

// Layout is the slot layout in one open directory.
type Layout struct{ Dir string }

// Status is the layout's state.
type Status struct {
	Current, Previous string
	Trial             bool // current is not confirmed
	Starts            int  // starts of current while on trial
	Versions          map[string]string
	Disabled          bool
}

// Other returns the slot that is not s.
func Other(s string) string {
	if s == A {
		return B
	}
	return A
}

func valid(s string) bool { return s == A || s == B }

func (l Layout) path(p ...string) string { return filepath.Join(append([]string{l.Dir}, p...)...) }

func (l Layout) read(name string) string {
	b, err := os.ReadFile(l.path(name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Status reads the layout. A missing or broken current pointer is slot a,
// as the launch hook reads it.
func (l Layout) Status() Status {
	s := Status{Current: l.read("current"), Previous: l.read("previous"), Versions: map[string]string{}}
	if !valid(s.Current) {
		s.Current = A
	}
	if !valid(s.Previous) || s.Previous == s.Current {
		s.Previous = ""
	}
	if f := strings.Fields(l.read("trial")); len(f) >= 1 && f[0] == s.Current {
		s.Trial = true
		if len(f) == 2 {
			s.Starts, _ = strconv.Atoi(f[1])
		}
	}
	for _, x := range []string{A, B} {
		if v := l.read(filepath.Join(x, "version")); v != "" {
			s.Versions[x] = v
		} else if _, err := os.Stat(l.path(x, "app")); err == nil {
			s.Versions[x] = "unknown"
		}
	}
	_, err := os.Stat(l.path("disable"))
	s.Disabled = err == nil
	return s
}

// ErrOnTrial refuses an install while the current slot is unconfirmed: the
// other slot is then the only known-good one.
var ErrOnTrial = errors.New("the current slot is on trial: confirm it or roll back before installing")

// Install writes app into the slot that is not current, makes it current on
// trial and remembers the old one as previous. Each file is written
// atomically; the pointer to the new slot is written last, so a power cut at
// any point leaves a layout the launch hook starts a known-good slot from.
func (l Layout) Install(app []byte, version string) (string, error) {
	st := l.Status()
	if st.Trial {
		return "", ErrOnTrial
	}
	to := Other(st.Current)
	if err := fsguard.MkdirAll(l.path(to)); err != nil {
		return "", err
	}
	if err := WriteFile(l.path(to, "app"), app, 0o755); err != nil {
		return "", err
	}
	if err := WriteFile(l.path(to, "version"), []byte(version+"\n"), 0o644); err != nil {
		return "", err
	}
	if err := WriteFile(l.path("previous"), []byte(st.Current+"\n"), 0o644); err != nil {
		return "", err
	}
	if err := WriteFile(l.path("trial"), []byte(to+" 0\n"), 0o644); err != nil {
		return "", err
	}
	return to, WriteFile(l.path("current"), []byte(to+"\n"), 0o644)
}

// Bootstrap makes app slot a, current and confirmed: the layout the install
// stick leaves.
func (l Layout) Bootstrap(app []byte, version string) error {
	if err := fsguard.MkdirAll(l.path(A)); err != nil {
		return err
	}
	if err := WriteFile(l.path(A, "app"), app, 0o755); err != nil {
		return err
	}
	if err := WriteFile(l.path(A, "version"), []byte(version+"\n"), 0o644); err != nil {
		return err
	}
	if err := Remove(l.path("trial")); err != nil {
		return err
	}
	return WriteFile(l.path("current"), []byte(A+"\n"), 0o644)
}

// Confirm ends the current slot's trial: it has proven healthy.
func (l Layout) Confirm() error {
	if !l.Status().Trial {
		return nil
	}
	return Remove(l.path("trial"))
}

// Rollback makes the previous slot current again. It is refused when there
// is no previous slot with an app.
func (l Layout) Rollback() (string, error) {
	st := l.Status()
	if st.Previous == "" {
		return st.Current, fmt.Errorf("no previous slot to go back to from slot %s", st.Current)
	}
	if _, err := os.Stat(l.path(st.Previous, "app")); err != nil {
		return st.Current, fmt.Errorf("previous slot %s has no app", st.Previous)
	}
	if err := WriteFile(l.path("current"), []byte(st.Previous+"\n"), 0o644); err != nil {
		return st.Current, err
	}
	return st.Previous, Remove(l.path("trial"))
}

// App is the current slot's app binary.
func (l Layout) App() string { return l.path(l.Status().Current, "app") }

// WriteFile writes data to path durably through the write guard (package
// fsguard): a temporary file next to it, fsync, mode, rename, then an fsync
// of the directory (spec 01 §12.4 step 4).
func WriteFile(path string, data []byte, mode os.FileMode) error {
	return fsguard.WriteFile(path, data, mode)
}

// Remove deletes path, if it exists, durably, through the write guard.
func Remove(path string) error { return fsguard.Remove(path) }

// SyncDir fsyncs a directory.
func SyncDir(dir string) error { return fsguard.SyncDir(dir) }
