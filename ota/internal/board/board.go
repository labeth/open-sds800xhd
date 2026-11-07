// Package board is the SDS800X HD's side of the supervisor: the hooks the
// core supervisor calls (HAL v1 SupervisorBoard) and the policy that stands
// in for a watchdog on a scope that has none.
//
// The SDS800X HD has no usable watchdog (both WDTs are disabled in the
// device tree, panic_timeout is 0; spec 01 §10), so a hung app is found by
// software health only, and the supervisor's last resort is a reboot. The
// reboot policy bounds that: after MaxReboots reboots without a healthy app
// the open firmware disables itself (open/disable), so the next power-up
// keeps the vendor firmware instead of rebooting forever.
package board

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-sds800xhd/ota/boottarget"
	"github.com/labeth/open-sds800xhd/ota/census"
	"github.com/labeth/open-sds800xhd/ota/fsguard"
	"github.com/labeth/open-sds800xhd/ota/internal/slots"
)

// Paths of the vendor system (spec 01 §3, §12).
const (
	P12           = "/usr/bin/siglent/usr"
	vendorApp     = "/usr/bin/siglent/main.app"
	vendorVNC     = "/usr/bin/siglent/vncserver"
	vendorDrivers = "/usr/bin/siglent/drivers/"
	displayScript = "insmod_after_app.sh"
)

// MaxReboots is how many supervisor reboots in a row, without a healthy app
// in between, the policy allows.
const MaxReboots = 3

// HealthMaxAge is how old the health token may be for a healthy app.
const HealthMaxAge = 10 * time.Second

// unwantedServices are the vendor services the app does not want running
// (spec 01 §12.2 step 3); rpcbind stays for VXI-11.
var unwantedServices = []string{
	"udhcpc", "ntpd", "smbd", "nmbd", "avahi-daemon", "telnetd", "lighttpd", "php-cgi", "vsftpd",
}

// Board is the supervisor board of one unit.
type Board struct {
	P12    string // p12's root
	Root   string // prefix of /proc ("" on the unit; a fixture tree in tests)
	Log    func(format string, a ...any)
	reboot func() error
	signal func(pid int, sig syscall.Signal)
}

var (
	_ hal.SupervisorBoard = (*Board)(nil)
	_ hal.HealthChecker   = (*Board)(nil)
	_ hal.CensusBoard     = (*Board)(nil)
)

// New returns the board on the unit's p12.
func New(log func(string, ...any)) *Board {
	return &Board{P12: P12, Log: log, reboot: func() error {
		syscall.Sync()
		return syscall.Reboot(syscall.LINUX_REBOOT_CMD_RESTART)
	}, signal: func(pid int, sig syscall.Signal) { _ = syscall.Kill(pid, sig) }}
}

// Slots is the slot layout.
func (b *Board) Slots() slots.Layout { return slots.Layout{Dir: b.SlotsDir()} }

// SlotsDir is p12/open, holding slots a and b.
func (b *Board) SlotsDir() string { return filepath.Join(b.P12, "open") }

// HealthToken is where the app's health writer writes.
func (b *Board) HealthToken() string { return filepath.Join(b.SlotsDir(), "health") }

// Watchdog is absent: none on this scope (spec 01 §10).
func (b *Board) Watchdog() (hal.Watchdog, bool) { return nil, false }

// Heartbeat does nothing: no service expects one.
func (b *Board) Heartbeat() error { return nil }

// LaunchEnv is what the app inherits: the state directory, the slot and the
// health token's path. No descriptors: the app maps /dev/mem itself.
func (b *Board) LaunchEnv() ([]string, []*os.File) {
	return []string{
		"OTA_DIR=" + b.SlotsDir(),
		"OTA_SLOT=" + b.Slots().Status().Current,
		"OTA_HEALTH_PATH=" + b.HealthToken(),
	}, nil
}

// Alive reports whether pid runs.
func (b *Board) Alive(pid int) bool { return alive(b.Root, pid) }

// Census is the unit's read-only census (package census), the same code
// the census stick runs.
func (b *Board) Census() hal.Census { return readOnlyCensus{root: b.Root} }

// readOnlyCensus is the census as a hal.Census: only the read-only census
// exists, so writes (the active census, which would exercise the
// hardware) is refused.
type readOnlyCensus struct{ root string }

func (c readOnlyCensus) Run(ctx context.Context, writes bool) ([]hal.Verdict, error) {
	if writes {
		return nil, hal.Refuse("active census", "this board has only the read-only census")
	}
	r, err := census.Run(ctx, census.Unit{Root: c.root})
	if r == nil {
		return nil, err
	}
	out := make([]hal.Verdict, 0, len(r.Verdicts))
	for _, v := range r.Verdicts {
		out = append(out, hal.Verdict{Part: v.Part, Verdict: v.Verdict, Detail: v.Detail})
	}
	return out, err
}

// Healthy is the software health check that replaces a watchdog: the app
// runs and, once it has written the health token, keeps it fresh.
func (b *Board) Healthy(pid int, now time.Time) bool {
	if !b.Alive(pid) {
		return false
	}
	st, err := os.Stat(b.HealthToken())
	if err != nil {
		return true // no token written yet: liveness only
	}
	return now.Sub(st.ModTime()) <= HealthMaxAge
}

// AppPID returns the pid of the running app of the current slot (or of the
// single-binary layout), 0 if none runs.
func (b *Board) AppPID() int {
	cur := b.Slots().Status().Current
	paths := []string{filepath.Join(b.SlotsDir(), cur, "app"), filepath.Join(b.SlotsDir(), "app")}
	for _, p := range procs(b.Root) {
		for _, path := range paths {
			if len(p.argv) > 0 && (p.argv[0] == path || slices.Contains(p.argv, path)) && alive(b.Root, p.pid) {
				return p.pid
			}
		}
	}
	return 0
}

// Takeover runs steps 1–3 of spec 01 §12.2: it stops the display-switch
// script before its children, the vendor application and its VNC server
// (SIGTERM, 5 s, SIGKILL, 5 s), main.app's leftovers and the unwanted
// services. It touches no hardware; the app re-initialises everything. An
// error means a vendor process survived: nothing may touch the hardware.
func (b *Board) Takeover(ctx context.Context) error {
	var script, children []int
	all := procs(b.Root)
	for _, p := range all {
		if mentions(p.argv, displayScript) {
			script = append(script, p.pid)
		}
	}
	for _, p := range all {
		if slices.Contains(script, p.ppid) {
			children = append(children, p.pid)
		}
	}
	if len(script) > 0 {
		b.logf("takeover: killing %s %v and its children %v", displayScript, script, children)
		b.kill(script, syscall.SIGKILL)
		b.kill(children, syscall.SIGKILL)
	}
	var vendor []int
	for _, p := range procs(b.Root) {
		if isProgram(p.argv, vendorApp) || isProgram(p.argv, vendorVNC) {
			vendor = append(vendor, p.pid)
		}
	}
	if len(vendor) > 0 {
		b.logf("takeover: stopping vendor processes %v", vendor)
		b.kill(vendor, syscall.SIGTERM)
		if left := waitGone(b.Root, vendor, 5*time.Second); len(left) > 0 {
			b.kill(left, syscall.SIGKILL)
			if left = waitGone(b.Root, left, 5*time.Second); len(left) > 0 {
				return fmt.Errorf("takeover: vendor processes %v survived SIGKILL", left)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var rest []int
	for _, p := range procs(b.Root) {
		if mentions(p.argv, vendorDrivers) || len(p.argv) > 0 && slices.Contains(unwantedServices, filepath.Base(p.argv[0])) {
			rest = append(rest, p.pid)
		}
	}
	if len(rest) > 0 {
		b.logf("takeover: stopping leftovers and services %v", rest)
		b.kill(rest, syscall.SIGKILL)
		waitGone(b.Root, rest, 2*time.Second)
	}
	return nil
}

// Reboot is the supervisor's last resort. It is refused once MaxReboots
// reboots followed each other without a healthy app (Confirmed resets the
// count): the open firmware then disables itself so the unit returns to
// the vendor firmware at the next power-up instead of rebooting forever.
// Every reboot and refusal is logged on p12 first.
func (b *Board) Reboot(reason string) error {
	if err := fsguard.MkdirAll(b.SlotsDir()); err != nil {
		return err
	}
	n := b.reboots()
	if n >= MaxReboots {
		_ = slots.WriteFile(filepath.Join(b.SlotsDir(), "disable"),
			[]byte(fmt.Sprintf("%d reboots without a healthy app; last: %s\n", n, reason)), 0o644)
		b.rebootLog("refused (%d in a row): %s; open firmware disabled", n, reason)
		return hal.Refuse("reboot", fmt.Sprintf("%d reboots in a row without a healthy app: "+
			"the open firmware is disabled and the next power-up keeps the vendor firmware", n))
	}
	if err := slots.WriteFile(b.rebootsPath(), []byte(strconv.Itoa(n+1)+"\n"), 0o644); err != nil {
		return err
	}
	b.rebootLog("reboot %d: %s", n+1, reason)
	return b.reboot()
}

// BootTarget is what the next power-up runs: boottarget.Open, or
// boottarget.Vendor while the disable file exists.
// TRLC-LINKS: REQ-HD-002
func (b *Board) BootTarget() string { return boottarget.Next(b.SlotsDir()) }

// SetBootTarget makes the next power-up run the open firmware or leave the
// unit to the vendor firmware (the disable file), without uninstalling.
// TRLC-LINKS: REQ-HD-002, REQ-HD-016
func (b *Board) SetBootTarget(target, reason string) error {
	if err := boottarget.Set(b.SlotsDir(), target, reason); err != nil {
		return err
	}
	b.rebootLog("next boot: %s firmware (%s)", target, reason)
	return nil
}

// RebootNow reboots because the owner asked: it is logged on p12 and does
// not count towards the unhealthy-reboot policy of Reboot.
// TRLC-LINKS: REQ-HD-002
func (b *Board) RebootNow(reason string) error {
	b.rebootLog("reboot asked: %s", reason)
	return b.reboot()
}

// Confirmed records a healthy app: the slot's trial ends and the reboot
// count starts again.
func (b *Board) Confirmed() error {
	if err := b.Slots().Confirm(); err != nil {
		return err
	}
	return slots.Remove(b.rebootsPath())
}

func (b *Board) rebootsPath() string { return filepath.Join(b.SlotsDir(), "reboots") }

func (b *Board) reboots() int {
	s, _ := os.ReadFile(b.rebootsPath())
	n, _ := strconv.Atoi(strings.TrimSpace(string(s)))
	return n
}

func (b *Board) rebootLog(format string, a ...any) {
	dir := filepath.Join(b.SlotsDir(), "log")
	_ = fsguard.MkdirAll(dir)
	f, err := fsguard.OpenFile(filepath.Join(dir, "reboot.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, a...))
	_ = f.Sync()
	_ = f.Close()
	b.logf(format, a...)
}

func (b *Board) kill(pids []int, sig syscall.Signal) {
	for _, p := range pids {
		b.signal(p, sig)
	}
}

func (b *Board) logf(format string, a ...any) {
	if b.Log != nil {
		b.Log(format, a...)
	}
}
