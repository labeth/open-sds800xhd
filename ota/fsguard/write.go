package fsguard

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// WriteFile writes data to path durably, after Check: a temporary file next
// to it, fsync, mode, rename, then an fsync of the directory, without which
// the rename is not durable (spec 01 §12.4 step 4).
// TRLC-LINKS: REQ-HD-016, REQ-HD-011, REQ-HD-012
func WriteFile(path string, data []byte, mode os.FileMode) error {
	if err := Check(path); err != nil {
		return err
	}
	tmp := path + ".new"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode) //nolint:gosec // G304: the path passed Check
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	serr := f.Sync()
	if err := errors.Join(werr, serr, f.Close()); err != nil {
		_ = os.Remove(tmp) // best effort: the write already failed
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return SyncDir(filepath.Dir(path))
}

// OpenFile opens path for writing (a log appended line by line) after
// Check.
// TRLC-LINKS: REQ-HD-016
func OpenFile(path string, flag int, mode os.FileMode) (*os.File, error) {
	if err := Check(path); err != nil {
		return nil, err
	}
	return os.OpenFile(path, flag, mode) //nolint:gosec // G304: the path passed Check
}

// Rename renames a file after checking both its old and its new path.
// TRLC-LINKS: REQ-HD-016
func Rename(from, to string) error {
	if err := Check(from); err != nil {
		return err
	}
	if err := Check(to); err != nil {
		return err
	}
	return os.Rename(from, to)
}

// Remove deletes path, if it exists, durably, after Check.
// TRLC-LINKS: REQ-HD-016
func Remove(path string) error {
	if err := Check(path); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return SyncDir(filepath.Dir(path))
}

// MkdirAll creates dir and its missing parents 0755 after Check. The open/
// tree is 0755 root:root (spec 01 §12.4).
// TRLC-LINKS: REQ-HD-016
func MkdirAll(dir string) error {
	if err := Check(dir); err != nil {
		return err
	}
	return os.MkdirAll(dir, 0o755) //nolint:gosec // G301: the open/ tree is 0755 root:root (spec 01 §12.4)
}

// Mkdir creates dir 0755 after Check; an existing dir is no error.
// TRLC-LINKS: REQ-HD-016
func Mkdir(dir string) error {
	if err := Check(dir); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, os.ErrExist) { //nolint:gosec // G301: the open/ tree is 0755 root:root (spec 01 §12.4)
		return err
	}
	return nil
}

// SyncDir fsyncs a directory (a read-only open: no check needed).
// TRLC-LINKS: REQ-HD-016
func SyncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // G304: a directory opened read-only to sync it
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) {
		return err
	}
	return nil
}
