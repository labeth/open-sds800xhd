// Package fsguard is the one write guard of the open firmware: every file
// our programs write, rename or delete on the unit goes through it, and so
// does every write to a device node or sysfs attribute and every writable
// /dev/mem window.
//
// The vendor U-Boot reads a USB stick at every boot before Linux runs; that
// stick route is both the unbrick path and the install path (spec 01 §2.2,
// §11.3, §12.4), and it lives in BOOT.bin on eMMC p1. The unit stays
// recoverable as long as nothing of ours touches what that route depends
// on, so the guard refuses, with an error that says why:
//
//   - any file outside p12's root (/usr/bin/siglent/usr), after cleaning
//     the path and resolving symlinks: p1 (BOOT.bin, uEnv.txt), p5 (kernel,
//     device tree), p7/p10 (vendor system) and p9 (factory data:
//     calibration, serial, MAC, licences) are never written;
//   - inside p12, the two directories the vendor boot turns into eMMC
//     writes: updatefile/ (U-Boot flashes BOOT.bin, the kernel and raw
//     partition images found there) and usr/upgrade/ (update.sh runs as
//     root with / writable);
//   - a U-Boot environment file anywhere (uboot.env: a valid one replaces
//     U-Boot's built-in boot command for good) and the stick's recovery
//     environment;
//   - block devices and raw eMMC nodes, eFuse, BBRAM and nvmem in /dev and
//     /sys, and /dev/mem windows over the CSU, eFuse and BBRAM controllers
//     and the SD/eMMC host controllers.
//
// The writable root is p12 at its run-time mount point unless the process
// says otherwise once at start (SetRoot): the installer's /init, which has
// p12 mounted on /mnt, and tests. SetRoot itself refuses a root that is a
// vendor area.
package fsguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// P12 is p12's root at run time: the only partition our code writes.
const P12 = "/usr/bin/siglent/usr"

// ErrRefused is what every refusal of the guard wraps.
var ErrRefused = errors.New("refused by the write guard")

// RefusedError is a refused write: the path and the rule it breaks.
type RefusedError struct {
	Op, Path, Reason string
}

// TRLC-LINKS: REQ-HD-016
func (e *RefusedError) Error() string {
	return fmt.Sprintf("%s %s: %v: %s", e.Op, e.Path, ErrRefused, e.Reason)
}

// Unwrap makes errors.Is(err, ErrRefused) hold.
// TRLC-LINKS: REQ-HD-016
func (e *RefusedError) Unwrap() error { return ErrRefused }

// TRLC-LINKS: REQ-HD-016
func refuse(op, path, reason string) error { return &RefusedError{Op: op, Path: path, Reason: reason} }

var (
	mu   sync.RWMutex
	root = P12
)

// Root is the directory writes may go under.
// TRLC-LINKS: REQ-HD-016
func Root() string {
	mu.RLock()
	defer mu.RUnlock()
	return root
}

// SetRoot makes dir the writable root and returns the function that puts
// the previous one back. It refuses a dir that is, contains or lies inside
// a vendor area: only p12 itself, a directory below it, or a directory
// outside every vendor area (the installer's mount point of p12, a test's
// temporary directory) can be a root.
// TRLC-LINKS: REQ-HD-016
func SetRoot(dir string) (restore func(), err error) {
	clean, err := abs(dir)
	if err != nil {
		return nil, err
	}
	if !within(clean, P12) {
		if within("/usr/bin/siglent", clean) {
			return nil, refuse("root", dir, "contains the vendor partitions under /usr/bin/siglent")
		}
		for _, v := range vendorAreas {
			if within(clean, v) {
				return nil, refuse("root", dir, "a vendor area ("+v+") cannot be the writable root")
			}
		}
	}
	mu.Lock()
	prev := root
	root = clean
	mu.Unlock()
	return func() {
		mu.Lock()
		root = prev
		mu.Unlock()
	}, nil
}

// vendorAreas are the directories of the vendor system and the kernel's
// pseudo file systems. p12 lies inside /usr/bin/siglent and is the one
// exception.
var vendorAreas = []string{
	"/bin", "/boot", "/dev", "/etc", "/lib", "/lib64", "/mnt/logo", "/proc", "/root", "/sbin", "/sys", "/usr", "/var",
}

// p12Forbidden are the directories of p12 the vendor boot turns into writes
// of other partitions (spec 01 §2.2, §5 step 3, §11.1, §12.3).
var p12Forbidden = []string{"updatefile", "usr/upgrade"}

// forbiddenNames are file names no program of ours writes anywhere:
// U-Boot's saved environment (spec 01 §2.2), the boot partition's files and
// the stick's recovery environment (§11.3).
var forbiddenNames = []string{"uboot.env", "uboot-redund.env", "boot.bin", "uenv.txt", "huluwa_udiskenv.txt"}

// Check returns nil when path may be written, created, renamed or deleted:
// it lies inside the writable root once cleaned and with every symlink of
// its existing part resolved, not in a p12 directory the vendor boot
// flashes from, and has no forbidden name.
// TRLC-LINKS: REQ-HD-016
func Check(path string) error {
	clean, err := abs(path)
	if err != nil {
		return err
	}
	if err := checkName(clean); err != nil {
		return err
	}
	if isPseudo(clean) {
		return refuse("write", path, "a device node or kernel attribute is not a file (CheckDevice guards those)")
	}
	r := Root()
	rr, err := resolve(r)
	if err != nil {
		return err
	}
	rp, err := resolve(clean)
	if err != nil {
		return err
	}
	if err := checkName(rp); err != nil {
		return err
	}
	for _, p := range []string{clean, rp} {
		if within(p, "/mnt/logo") || within(p, "/usr/bin/siglent") && !within(p, P12) {
			return refuse("write", path, "a vendor partition (p8, p9 or p10)")
		}
	}
	if !within(clean, r) {
		return refuse("write", path, "outside p12's root "+r)
	}
	if !within(rp, rr) {
		return refuse("write", path, "resolves to "+rp+", outside p12's root "+r)
	}
	rel, _ := filepath.Rel(rr, rp)
	for _, d := range p12Forbidden {
		if within(filepath.Join("/", rel), filepath.Join("/", d)) {
			return refuse("write", path, "p12/"+d+" is flashed or run by the vendor boot")
		}
	}
	return nil
}

// TRLC-LINKS: REQ-HD-016
func checkName(p string) error {
	b := strings.ToLower(filepath.Base(p))
	for _, n := range forbiddenNames {
		if b == n || b == n+".new" {
			return refuse("write", p, "a boot or U-Boot environment file is never written")
		}
	}
	return nil
}

// TRLC-LINKS: REQ-HD-016
func isPseudo(p string) bool {
	return within(p, "/dev") || within(p, "/sys") || within(p, "/proc")
}

// abs is the cleaned absolute path.
// TRLC-LINKS: REQ-HD-016
func abs(p string) (string, error) {
	if p == "" {
		return "", refuse("write", p, "empty path")
	}
	a, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("fsguard: %w", err)
	}
	return a, nil
}

// resolve resolves every symlink of the longest existing prefix of p and
// appends the rest, so a path that does not exist yet is judged by where it
// would be created.
// A dangling symlink is followed to where its target would be created.
// TRLC-LINKS: REQ-HD-016
func resolve(p string) (string, error) {
	return resolveDepth(p, 0)
}

// maxLinks bounds the symlinks followed, as the kernel's ELOOP does.
const maxLinks = 40

// TRLC-LINKS: REQ-HD-016
func resolveDepth(p string, depth int) (string, error) {
	if depth > maxLinks {
		return "", refuse("write", p, "too many levels of symbolic links")
	}
	rest := ""
	cur := p
	for {
		r, err := filepath.EvalSymlinks(cur)
		if err == nil {
			return filepath.Join(r, rest), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("fsguard: resolve %s: %w", p, err)
		}
		if st, lerr := os.Lstat(cur); lerr == nil && st.Mode()&os.ModeSymlink != 0 {
			target, rerr := os.Readlink(cur)
			if rerr != nil {
				return "", fmt.Errorf("fsguard: resolve %s: %w", p, rerr)
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(cur), target)
			}
			return resolveDepth(filepath.Join(target, rest), depth+1)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p, nil
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// within reports whether p is dir or below it (both clean and absolute).
// TRLC-LINKS: REQ-HD-016
func within(p, dir string) bool {
	if dir == "/" {
		return true
	}
	return p == dir || strings.HasPrefix(p, dir+"/")
}
