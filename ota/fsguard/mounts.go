package fsguard

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// P12Device is p12's block device: the one eMMC partition anything of ours
// mounts for writing.
const P12Device = "/dev/mmcblk0p12"

// Mount is one line of /proc/mounts.
type Mount struct {
	Source, Dir, FS string
	RW              bool
}

// ParseMounts reads a /proc/mounts listing.
// TRLC-LINKS: REQ-HD-016
func ParseMounts(r io.Reader) ([]Mount, error) {
	var ms []Mount
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			continue
		}
		rw := strings.Contains(","+f[3]+",", ",rw,")
		ms = append(ms, Mount{Source: unescape(f[0]), Dir: unescape(f[1]), FS: f[2], RW: rw})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("fsguard: mounts: %w", err)
	}
	return ms, nil
}

// CheckInstallMounts is the installer /init's check after it mounted p12 on
// dir: dir holds P12Device as ext4, and no other eMMC partition is mounted
// writable. Only then may the install write (with dir as the root).
// TRLC-LINKS: REQ-HD-016
func CheckInstallMounts(mounts io.Reader, dir string) error {
	ms, err := ParseMounts(mounts)
	if err != nil {
		return err
	}
	found := false
	for _, m := range ms {
		switch {
		case m.Dir == dir && m.Source == P12Device && m.FS == "ext4":
			found = true
		case m.Dir == dir:
			return refuse("mount", dir, fmt.Sprintf("holds %s (%s), not %s as ext4", m.Source, m.FS, P12Device))
		case isBlockSource(m.Source) && m.RW:
			return refuse("mount", m.Dir, m.Source+" is mounted writable; the installer writes only "+P12Device)
		}
	}
	if !found {
		return refuse("mount", dir, P12Device+" is not mounted there")
	}
	return nil
}

// CheckOffUnit returns nil when path may be written by a program that must
// not write the eMMC at all (the census, captures to a stick): the mount
// that holds it is not an eMMC partition, it lies in no vendor area, and it
// has no forbidden name. mounts is a /proc/mounts listing.
// TRLC-LINKS: REQ-HD-013, REQ-HD-016
func CheckOffUnit(path string, mounts io.Reader) error {
	clean, err := abs(path)
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
		if isPseudo(p) {
			return refuse("write", path, "a device node or kernel attribute is not a file")
		}
		if within(p, "/usr/bin/siglent") || within(p, "/mnt/logo") {
			return refuse("write", path, "on the unit's eMMC")
		}
	}
	ms, err := ParseMounts(mounts)
	if err != nil {
		return err
	}
	var best Mount
	for _, m := range ms {
		if within(rp, m.Dir) && len(m.Dir) >= len(best.Dir) {
			best = m
		}
	}
	if isBlockSource(best.Source) {
		return refuse("write", path, "on the unit's eMMC ("+best.Source+" at "+best.Dir+")")
	}
	return nil
}

// unescape undoes the octal escapes of /proc/mounts (\040 is a space).
// TRLC-LINKS: REQ-HD-016
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
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
