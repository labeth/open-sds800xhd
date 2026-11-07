//go:build linux

// Command oinit is /init of the install and uninstall sticks' initramfs
// (spec 01 §12.4): it mounts p12, installs the payload under /payload (or
// uninstalls), writes the stick's marker and reboots. /oinst.conf holds
// "mode=install|uninstall", "mark=<marker relative to p12>", "version=".
//
// On any error it writes no marker and does not reboot (a reboot would
// repeat the install on every boot): it unmounts, syncs, reports on the
// console and sleeps forever as PID 1, which must never exit.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/labeth/open-sds800xhd/ota/fsguard"
	"github.com/labeth/open-sds800xhd/ota/internal/install"
)

const (
	p12Dev = fsguard.P12Device
	mnt    = "/mnt"
)

func main() {
	setup()
	conf := readConf("/oinst.conf")
	err := run(conf)
	if err != nil {
		say("oinit: %v; nothing more is written. Remove the stick and power-cycle.", err)
		halt()
	}
	say("oinit: %s done (%s), rebooting", conf["mode"], conf["mark"])
	syscall.Sync()
	_ = syscall.Reboot(syscall.LINUX_REBOOT_CMD_RESTART)
	halt()
}

func setup() {
	_ = os.MkdirAll("/dev", 0o755) //nolint:gosec // G301: a system mount point
	_ = syscall.Mount("devtmpfs", "/dev", "devtmpfs", 0, "")
	_ = os.MkdirAll("/proc", 0o755) //nolint:gosec // G301: a system mount point
	_ = syscall.Mount("proc", "/proc", "proc", 0, "")
	_ = os.MkdirAll("/sys", 0o755) //nolint:gosec // G301: a system mount point
	_ = syscall.Mount("sysfs", "/sys", "sysfs", 0, "")
	if c, err := os.OpenFile("/dev/console", os.O_RDWR, 0); err == nil {
		os.Stdout, os.Stderr = c, c
	}
}

func run(conf map[string]string) error {
	mode, mark := conf["mode"], conf["mark"]
	if (mode != "install" && mode != "uninstall") || mark == "" {
		return fmt.Errorf("bad /oinst.conf: mode %q mark %q", mode, mark)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(p12Dev); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not appear in 15 s", p12Dev)
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = os.MkdirAll(mnt, 0o755) //nolint:gosec // G301: a mount point
	if err := syscall.Mount(p12Dev, mnt, "ext4", 0, ""); err != nil {
		return fmt.Errorf("mount %s: %w", p12Dev, err)
	}
	err := guardP12()
	if err == nil && mode == "install" {
		err = install.Payload("/payload", mnt, mark, conf["version"])
	} else if err == nil {
		err = install.Uninstall(mnt, mark, conf["version"])
	}
	syscall.Sync()
	if uerr := syscall.Unmount(mnt, 0); uerr != nil && err == nil {
		err = fmt.Errorf("umount: %w", uerr)
	}
	syscall.Sync()
	return err
}

// guardP12 lets the install write only once the kernel's mount table shows
// p12 on mnt and no other eMMC partition writable, and roots the write
// guard there: the payload and markers can then land nowhere but p12.
func guardP12() error {
	m, err := os.Open("/proc/mounts")
	if err != nil {
		return err
	}
	defer func() { _ = m.Close() }() // read only: nothing to flush
	if err := fsguard.CheckInstallMounts(m, mnt); err != nil {
		return err
	}
	_, err = fsguard.SetRoot(mnt)
	return err
}

func readConf(path string) map[string]string {
	m := map[string]string{}
	f, err := os.Open(path) //nolint:gosec // G304: the stick's own configuration file
	if err != nil {
		return m
	}
	defer func() { _ = f.Close() }() // read only: nothing to flush
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "="); ok {
			m[k] = v
		}
	}
	return m
}

func say(format string, a ...any) { _, _ = fmt.Fprintf(os.Stdout, format+"\n", a...) }

func halt() {
	for {
		time.Sleep(time.Hour)
	}
}
