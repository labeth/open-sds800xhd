package platform

import (
	"fmt"
	"strconv"
	"strings"
	"syscall"

	"github.com/labeth/open-sds800xhd/ota/fsguard"
)

// Sysfs attributes are accessed with one plain syscall each, not os.File:
// the Go runtime poller is not wanted on sysfs and device nodes, and the test
// environment refuses epoll on emulated nodes.

// WriteAttr writes v to a sysfs attribute in one write, after the write
// guard's device check (no block device, eFuse, BBRAM or nvmem).
// TRLC-LINKS: REQ-HD-016
func WriteAttr(path, v string) error {
	if err := fsguard.CheckDevice(path); err != nil {
		return err
	}
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = syscall.Close(fd) }()
	if _, err := syscall.Write(fd, []byte(v)); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// ReadAttr reads a sysfs attribute in one read.
func ReadAttr(path string) (string, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = syscall.Close(fd) }()
	var b [256]byte
	n, err := syscall.Read(fd, b[:])
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return strings.TrimSpace(string(b[:max(n, 0)])), nil
}

func gpioDir(n int) string { return "/sys/class/gpio/gpio" + strconv.Itoa(n) }

// GPIOIn exports GPIO n if needed, makes it an input and reads it. The
// numbers are the vendor kernel's sysfs numbers (spec 01 §7).
func GPIOIn(n int) (int, error) {
	if _, err := ReadAttr(gpioDir(n) + "/value"); err != nil {
		if err := WriteAttr("/sys/class/gpio/export", strconv.Itoa(n)); err != nil {
			return 0, err
		}
	}
	_ = WriteAttr(gpioDir(n)+"/direction", "in")
	s, err := ReadAttr(gpioDir(n) + "/value")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(s)
}

// GPIOOut exports GPIO n (re-exporting it when reexport is set), makes it an
// output and drives it to v.
func GPIOOut(n, v int, reexport bool) error {
	if reexport {
		_ = WriteAttr("/sys/class/gpio/unexport", strconv.Itoa(n))
	}
	_ = WriteAttr("/sys/class/gpio/export", strconv.Itoa(n))
	if err := WriteAttr(gpioDir(n)+"/direction", "out"); err != nil {
		return err
	}
	return WriteAttr(gpioDir(n)+"/value", strconv.Itoa(v))
}
