// Package slots is the A/B slot layout of the open firmware on p12 and the
// atomic file writes everything on p12 uses (spec 01 §8, §12.4).
//
// Layout under the open directory (p12/open):
//
//	a/app, b/app        the two slots' app binaries; a/version, b/version;
//	a/sha256, b/sha256  "<hex SHA-256> <size>" of each app as installed
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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/labeth/open-dso-core/fsguard"
	"github.com/labeth/open-sds800xhd/app/hdguard"
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

// Digest is what an update image must be: its size and SHA-256, given by
// whoever built it and checked on the unit before the image becomes a slot
// (ADR-UPDATE-INTEGRITY). It guards against an image damaged on its way or
// on p12, not against a forged one: it is no signature.
type Digest struct {
	Size   int64
	SHA256 [sha256.Size]byte
}

// DigestOf is data's digest.
func DigestOf(data []byte) Digest {
	return Digest{Size: int64(len(data)), SHA256: sha256.Sum256(data)}
}

// ParseDigest reads a digest from its hex SHA-256 and its size in bytes.
func ParseDigest(hexSum string, size int64) (Digest, error) {
	var d Digest
	b, err := hex.DecodeString(strings.TrimSpace(hexSum))
	if err != nil || len(b) != sha256.Size {
		return d, fmt.Errorf("SHA-256 %q: want %d hex digits", hexSum, 2*sha256.Size)
	}
	if size <= 0 {
		return d, fmt.Errorf("image size %d: want the image's size in bytes", size)
	}
	copy(d.SHA256[:], b)
	d.Size = size
	return d, nil
}

// String is the digest as "<hex SHA-256> <size>", the form of a manifest
// line.
func (d Digest) String() string {
	return hex.EncodeToString(d.SHA256[:]) + " " + strconv.FormatInt(d.Size, 10)
}

// ErrDigest is an image whose size or SHA-256 is not the one it was sent
// with.
var ErrDigest = errors.New("the update image does not match its digest")

// check reports whether data is d.
func (d Digest) check(data []byte, what string) error {
	if got := DigestOf(data); got != d {
		return fmt.Errorf("%w: %s is %s, want %s", ErrDigest, what, got, d)
	}
	return nil
}

// Install writes app into the slot that is not current, makes it current on
// trial and remembers the old one as previous. The image must match want
// (its size and SHA-256), both as given and as written to the slot: a
// mismatch refuses the install before the slot becomes current. Each file is
// written atomically; the pointer to the new slot is written last, so a
// power cut at any point leaves a layout the launch hook starts a known-good
// slot from.
// TRLC-LINKS: REQ-HD-012
func (l Layout) Install(app []byte, version string, want Digest) (string, error) {
	if err := want.check(app, "the image"); err != nil {
		return "", err
	}
	st := l.Status()
	if st.Trial {
		return "", ErrOnTrial
	}
	to := Other(st.Current)
	if err := hdguard.FS.MkdirAll(l.path(to)); err != nil {
		return "", err
	}
	if err := WriteFile(l.path(to, "app"), app, 0o755); err != nil {
		return "", err
	}
	written, err := os.ReadFile(l.path(to, "app"))
	if err != nil {
		return "", err
	}
	if err := want.check(written, "slot "+to+"'s copy"); err != nil {
		return "", err
	}
	if err := WriteFile(l.path(to, "version"), []byte(version+"\n"), 0o644); err != nil {
		return "", err
	}
	if err := WriteFile(l.path(to, "sha256"), []byte(want.String()+"\n"), 0o644); err != nil {
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
	if err := hdguard.FS.MkdirAll(l.path(A)); err != nil {
		return err
	}
	if err := WriteFile(l.path(A, "app"), app, 0o755); err != nil {
		return err
	}
	if err := WriteFile(l.path(A, "version"), []byte(version+"\n"), 0o644); err != nil {
		return err
	}
	if err := WriteFile(l.path(A, "sha256"), []byte(DigestOf(app).String()+"\n"), 0o644); err != nil {
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

// Verify checks slot s's app against the digest recorded when it was
// installed (s/sha256): nil when they match or no digest was recorded (a
// slot from before digests), ErrDigest when the app was damaged since.
// TRLC-LINKS: REQ-HD-012
func (l Layout) Verify(s string) error {
	f := strings.Fields(l.read(filepath.Join(s, "sha256")))
	if len(f) < 2 {
		return nil
	}
	n, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil {
		return fmt.Errorf("%w: slot %s's recorded digest %q", ErrDigest, s, strings.Join(f, " "))
	}
	want, err := ParseDigest(f[0], n)
	if err != nil {
		return fmt.Errorf("%w: slot %s's recorded digest: %w", ErrDigest, s, err)
	}
	app, err := os.ReadFile(l.path(s, "app"))
	if err != nil {
		return fmt.Errorf("%w: slot %s: %w", ErrDigest, s, err)
	}
	return want.check(app, "slot "+s+"'s app")
}

// App is the current slot's app binary.
func (l Layout) App() string { return l.path(l.Status().Current, "app") }

// WriteFile writes data to path durably through the write guard (package
// fsguard): a temporary file next to it, fsync, mode, rename, then an fsync
// of the directory (spec 01 §12.4 step 4).
// TRLC-LINKS: REQ-HD-012, REQ-HD-016
func WriteFile(path string, data []byte, mode os.FileMode) error {
	return hdguard.FS.WriteFile(path, data, mode)
}

// Remove deletes path, if it exists, durably, through the write guard.
// TRLC-LINKS: REQ-HD-012, REQ-HD-016
func Remove(path string) error { return hdguard.FS.Remove(path) }

// SyncDir fsyncs a directory.
func SyncDir(dir string) error { return fsguard.SyncDir(dir) }
