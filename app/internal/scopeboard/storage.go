package scopeboard

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-sds800xhd/app/internal/platform"
	"github.com/labeth/open-sds800xhd/ota/fsguard"
)

// storageFacet is p12, the only writable partition (spec 01 §8, spec 09
// §4.1): settings and setup slots under OpenDir, each key one file written
// to a temporary name, synced, renamed and the directory synced (spec 01
// §12.4). Captures go to a USB stick when one is mounted.
type storageFacet Board

// Budgets of the stores, in bytes of all values together. p12 is shared
// with the vendor's user data, so the stores stay small.
const (
	settingsBudget = 256 << 10
	setupsBudget   = 2 << 20
	// SetupSlots is the number of numbered setup slots, keys "1" … "20".
	SetupSlots = 20
)

var (
	settingsKV = &fileKV{dir: filepath.Join(platform.OpenDir, "state"), budget: settingsBudget}
	setupsKV   = &fileKV{dir: filepath.Join(platform.OpenDir, "setups"), budget: setupsBudget, slots: SetupSlots}
)

// Settings is the settings store.
func (s *storageFacet) Settings() hal.KV { return settingsKV }

// Setups is the numbered setup slots.
func (s *storageFacet) Setups() (hal.KV, bool) { return setupsKV, true }

// Captures is a directory on the first writable USB stick mounted, or ""
// when there is none: p12 is too small for captures. The write guard
// confirms the directory is off the unit's eMMC. The vendor application
// mounted sticks itself (spec 01 §8); this app uses one only when the
// system has mounted it.
// TRLC-LINKS: REQ-HD-016
func (s *storageFacet) Captures() (string, error) {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }() // read only: nothing to flush
	dir, err := captureDir(f)
	if dir == "" || err != nil {
		return dir, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	if err := fsguard.CheckOffUnit(filepath.Join(dir, "x"), f); err != nil {
		return "", err
	}
	return dir, nil
}

// stickFS are the file systems a USB stick carries.
var stickFS = map[string]bool{"vfat": true, "exfat": true, "msdos": true, "ntfs": true, "ntfs3": true, "fuseblk": true}

// captureDir picks the mount point of the first writable USB stick (a
// /dev/sd* device with a stick's file system) in a /proc/mounts listing.
func captureDir(r io.Reader) (string, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 || !strings.HasPrefix(f[0], "/dev/sd") || !stickFS[f[2]] || strings.Contains(","+f[3]+",", ",ro,") {
			continue
		}
		return filepath.Join(unescapeMount(f[1]), "open-captures"), nil
	}
	return "", sc.Err()
}

// unescapeMount undoes the octal escapes of /proc/mounts (\040 is a space).
func unescapeMount(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Calibration is the front end's calibration source.
func (s *storageFacet) Calibration() hal.CalSource {
	return (*frontEndFacet)(s).Calibration()
}

// fileKV is a key-value store of one file per key. A Put past the budget
// (all values together) is a refusal and leaves the store as it was.
type fileKV struct {
	mu     sync.Mutex
	dir    string
	budget int
	slots  int // >0: the keys are the slot numbers 1 … slots
}

func (kv *fileKV) path(key string) (string, error) {
	if key == "" || key != filepath.Base(key) || strings.HasPrefix(key, ".") || strings.HasSuffix(key, ".new") {
		return "", hal.Refuse("store key "+strconv.Quote(key), "a key is a plain file name")
	}
	if kv.slots > 0 {
		n, err := strconv.Atoi(key)
		if err != nil || strconv.Itoa(n) != key || n < 1 || n > kv.slots {
			return "", hal.Refuse("setup slot "+key, fmt.Sprintf("the slots are 1 … %d", kv.slots))
		}
	}
	return filepath.Join(kv.dir, key), nil
}

// Get returns a key's value.
func (kv *fileKV) Get(key string) ([]byte, bool) {
	p, err := kv.path(key)
	if err != nil {
		return nil, false
	}
	b, err := os.ReadFile(p) //nolint:gosec // G304: a key under the store's directory; path refuses anything but a plain file name
	return b, err == nil
}

// used is the size of every value but key's.
func (kv *fileKV) used(except string) int {
	es, _ := os.ReadDir(kv.dir)
	n := 0
	for _, e := range es {
		if e.Name() == except || strings.HasSuffix(e.Name(), ".new") {
			continue
		}
		if fi, err := e.Info(); err == nil && fi.Mode().IsRegular() {
			n += int(fi.Size())
		}
	}
	return n
}

// Put writes a key's value durably through the write guard: temporary
// file, fsync, rename, fsync of the directory.
// TRLC-LINKS: REQ-HD-011, REQ-HD-016
func (kv *fileKV) Put(key string, val []byte) error {
	p, err := kv.path(key)
	if err != nil {
		return err
	}
	kv.mu.Lock()
	defer kv.mu.Unlock()
	if u := kv.used(key); u+len(val) > kv.budget {
		return hal.Refuse("store "+key, fmt.Sprintf("%d bytes do not fit: %d of the %d-byte budget are in use", len(val), u, kv.budget))
	}
	if err := fsguard.MkdirAll(kv.dir); err != nil {
		return err
	}
	return fsguard.WriteFile(p, val, 0o644)
}

// Budget is the store's size budget in bytes.
func (kv *fileKV) Budget() int { return kv.budget }
