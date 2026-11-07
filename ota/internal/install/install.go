// Package install is what the install and uninstall sticks do to p12 (spec
// 01 §12.4): copy a payload tree durably, launch hook last, and manage the
// markers that make each stick inert once it has run.
package install

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/labeth/open-sds800xhd/app/hdguard"
	"github.com/labeth/open-sds800xhd/ota/internal/slots"
)

// Hook is the launch hook's path relative to p12's root.
const Hook = "extra.sh"

// Payload copies every file under src to the same path under p12: missing
// directories 0755, each file written durably with its mode, the launch
// hook last. Then it deletes the uninstall markers (open/removed-*) and
// writes marker (relative to p12, e.g. "open/installed-1") with version as
// content. On an error nothing after the failing file is written, and the
// marker is not.
func Payload(src, p12, marker, version string) error {
	var files []string
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			rel, _ := filepath.Rel(src, p)
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// The hook last: until it is in place nothing of the payload runs.
	slices.SortStableFunc(files, func(a, b string) int {
		switch {
		case a == Hook && b != Hook:
			return 1
		case b == Hook && a != Hook:
			return -1
		}
		return strings.Compare(a, b)
	})
	for _, rel := range files {
		if err := copyOne(filepath.Join(src, rel), filepath.Join(p12, rel)); err != nil {
			return fmt.Errorf("install %s: %w", rel, err)
		}
	}
	if err := removeMatching(filepath.Join(p12, "open"), "removed-"); err != nil {
		return err
	}
	return writeMarker(p12, marker, version)
}

// Uninstall deletes the launch hook and every install marker
// (open/installed-*) and writes the uninstall marker. The rest of open/
// stays: without the hook nothing runs it, and a later install overwrites it.
func Uninstall(p12, marker, version string) error {
	if err := slots.Remove(filepath.Join(p12, Hook)); err != nil {
		return err
	}
	if err := removeMatching(filepath.Join(p12, "open"), "installed-"); err != nil {
		return err
	}
	return writeMarker(p12, marker, version)
}

func copyOne(from, to string) error {
	st, err := os.Stat(from)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(from) //nolint:gosec // G304: a payload file of the install stick
	if err != nil {
		return err
	}
	if err := mkdirs(filepath.Dir(to)); err != nil {
		return err
	}
	mode := st.Mode().Perm()
	if filepath.Base(to) == Hook {
		mode = 0o755
	}
	return slots.WriteFile(to, b, mode)
}

// mkdirs creates missing directories 0755, syncing each new one's parent.
func mkdirs(dir string) error {
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	if err := mkdirs(filepath.Dir(dir)); err != nil {
		return err
	}
	if err := hdguard.FS.Mkdir(dir); err != nil {
		return err
	}
	return slots.SyncDir(filepath.Dir(dir))
}

func removeMatching(dir, prefix string) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), prefix) {
			if err := slots.Remove(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeMarker(p12, marker, version string) error {
	if marker == "" {
		return errors.New("no marker name")
	}
	p := filepath.Join(p12, marker)
	if err := mkdirs(filepath.Dir(p)); err != nil {
		return err
	}
	if version == "" {
		version = "installed"
	}
	return slots.WriteFile(p, []byte(version+"\n"), 0o644)
}
