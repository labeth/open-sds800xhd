// Command otactl drives the board side of the supervisor by hand: the slot
// layout on p12, the launch hook, and the restart-without-watchdog check the
// core supervisor runs on this scope. It is the thin driver that exercises
// those hooks (in the simulator and on a bench); the supervisor itself is the
// core's.
//
//	otactl [-p12 DIR] bootstrap HOOK APP [VERSION]  first install: hook, slot a, confirmed
//	otactl [-p12 DIR] install [-sha256 HEX -size N] APP [VERSION]  into the other slot, on trial,
//	                                                once APP matches its digest (flags, or APP.sha256)
//	otactl [-p12 DIR] confirm | rollback | status
//	otactl [-p12 DIR] launch                        run the launch hook as the boot does
//	otactl [-p12 DIR] restart                       stop the running app, launch again
//	otactl [-p12 DIR] watch [GRACE]                 health check: confirm, or roll back and relaunch
//	otactl [-p12 DIR] supervise [GRACE]             keep the app running, bounded: 3 starts a boot, 3 reboots
//	otactl [-p12 DIR] takeover                      stop the vendor processes (no hardware)
//	otactl [-p12 DIR] vendor [reboot]               next power-up runs the vendor firmware (open/disable)
//	otactl [-p12 DIR] open                          next power-up runs the open firmware again
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-sds800xhd/app/hdguard"
	"github.com/labeth/open-sds800xhd/ota/internal/board"
	"github.com/labeth/open-sds800xhd/ota/internal/slots"
)

func main() {
	p12 := flag.String("p12", board.P12, "p12's root")
	flag.Parse()
	// Every write of the board side goes through the write guard, rooted at
	// the p12 this run works on; a vendor area is refused as a root.
	if _, err := hdguard.FS.SetRoot(*p12); err != nil {
		say("%v", err)
		os.Exit(1)
	}
	b := board.New(func(f string, a ...any) { say(f, a...) })
	b.P12 = *p12
	args := flag.Args()
	if len(args) == 0 {
		usage()
	}
	if err := run(b, args[0], args[1:]); err != nil {
		say("%s: %v", args[0], err)
		os.Exit(1)
	}
}

func run(b *board.Board, cmd string, args []string) error {
	l := b.Slots()
	switch cmd {
	case "bootstrap":
		if len(args) < 2 {
			usage()
		}
		app, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		hook, err := os.ReadFile(args[0])
		if err != nil {
			return err
		}
		if err := hdguard.FS.MkdirAll(l.Dir); err != nil {
			return err
		}
		if err := l.Bootstrap(app, arg(args, 2, "bootstrap")); err != nil {
			return err
		}
		if err := slots.WriteFile(filepath.Join(b.P12, "extra.sh"), hook, 0o755); err != nil {
			return err
		}
		say("bootstrap: slot a installed and current, hook in place")
	case "install":
		fs := flag.NewFlagSet("install", flag.ContinueOnError)
		sum := fs.String("sha256", "", "the image's SHA-256 (hex), as built")
		size := fs.Int64("size", 0, "the image's size in bytes, as built")
		if err := fs.Parse(args); err != nil {
			return err
		}
		args = fs.Args()
		if len(args) < 1 {
			usage()
		}
		want, err := imageDigest(args[0], *sum, *size)
		if err != nil {
			return err
		}
		app, err := os.ReadFile(args[0])
		if err != nil {
			return err
		}
		s, err := l.Install(app, arg(args, 1, filepath.Base(args[0])), want)
		if err != nil {
			return err
		}
		say("install: slot %s installed, current, on trial", s)
	case "confirm":
		if err := b.Confirmed(); err != nil {
			return err
		}
		say("confirm: slot %s confirmed", l.Status().Current)
	case "rollback":
		s, err := l.Rollback()
		if err != nil {
			return err
		}
		say("rollback: slot %s current", s)
	case "status":
		st := l.Status()
		say("status: current %s previous %s trial %v starts %d disabled %v versions %v",
			st.Current, st.Previous, st.Trial, st.Starts, st.Disabled, st.Versions)
	case "launch":
		return launch(b)
	case "restart":
		if pid := b.AppPID(); pid != 0 {
			say("restart: stopping app %d", pid)
			stop(b, pid)
		}
		return launch(b)
	case "watch":
		grace := 15 * time.Second
		if len(args) > 0 {
			d, err := time.ParseDuration(args[0])
			if err != nil {
				return err
			}
			grace = d
		}
		return watch(b, grace)
	case "supervise":
		grace := 15 * time.Second
		if len(args) > 0 {
			d, err := time.ParseDuration(args[0])
			if err != nil {
				return err
			}
			grace = d
		}
		return supervise(b, grace)
	case "takeover":
		return b.Takeover(context.Background())
	case "vendor":
		if err := b.SetBootTarget(hal.BootVendor, "otactl vendor"); err != nil {
			return err
		}
		if arg(args, 0, "") == "reboot" {
			say("vendor: next boot runs the vendor firmware; rebooting")
			return b.RebootNow("otactl vendor reboot")
		}
		say("vendor: next boot runs the vendor firmware; 'otactl vendor reboot' reboots now, 'otactl open' undoes it")
	case "open":
		if err := b.SetBootTarget(hal.BootOpen, "otactl open"); err != nil {
			return err
		}
		say("open: next boot runs the open firmware")
	default:
		usage()
	}
	return nil
}

// imageDigest is the digest an update image must match: -sha256 and -size,
// or else the manifest beside the image (IMAGE.sha256: "<hex SHA-256>
// <size>", written where the image was built). An image without one is
// refused: nothing could tell a damaged copy from a good one.
func imageDigest(image, sum string, size int64) (slots.Digest, error) {
	if sum != "" || size != 0 {
		return slots.ParseDigest(sum, size)
	}
	m, err := os.ReadFile(image + ".sha256") //nolint:gosec // G304: the manifest beside the image the operator named
	if err != nil {
		return slots.Digest{}, fmt.Errorf("no digest for %s: give -sha256 and -size, or the manifest %s.sha256 (%w)", image, image, err)
	}
	f := strings.Fields(string(m))
	if len(f) < 2 {
		return slots.Digest{}, fmt.Errorf("manifest %s.sha256: want \"<hex SHA-256> <size>\"", image)
	}
	n, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil {
		return slots.Digest{}, fmt.Errorf("manifest %s.sha256: size %q", image, f[1])
	}
	return slots.ParseDigest(f[0], n)
}

// launch runs the launch hook as S99extra does, once the current slot's
// app matches the digest recorded at its install: a damaged app is not
// started; the previous slot is made current when its own app is intact,
// otherwise nothing is started (the vendor firmware runs at the next boot).
func launch(b *board.Board) error {
	l := b.Slots()
	if err := l.Verify(l.Status().Current); err != nil {
		prev := l.Status().Previous
		if prev == "" || l.Verify(prev) != nil {
			return fmt.Errorf("%w: nothing started", err)
		}
		s, rerr := l.Rollback()
		if rerr != nil {
			return fmt.Errorf("%w: rollback: %w", err, rerr)
		}
		say("launch: %v: back to slot %s", err, s)
	}
	out, err := exec.CommandContext(context.Background(), "/bin/sh", filepath.Join(b.P12, "extra.sh")).CombinedOutput() //nolint:gosec // G204: the launch hook at its fixed place on p12
	if len(out) > 0 {
		say("launch: %s", out)
	}
	if err != nil {
		return err
	}
	st := b.Slots().Status()
	say("launch: hook ran; current slot %s (trial %v, starts %d)", st.Current, st.Trial, st.Starts)
	return nil
}

// watch is the software health check of a scope without a watchdog: the
// current slot's app must run, healthy, for grace. A healthy app is
// confirmed; an app that dies or goes stale is stopped, its slot rolled back
// when it is on trial, and the hook launched again.
func watch(b *board.Board, grace time.Duration) error {
	deadline := time.Now().Add(grace)
	pid := 0
	for time.Now().Before(deadline) && pid == 0 {
		pid = b.AppPID()
		time.Sleep(200 * time.Millisecond)
	}
	healthy := pid != 0
	for healthy && time.Now().Before(deadline) {
		healthy = b.Healthy(pid, time.Now())
		time.Sleep(500 * time.Millisecond)
	}
	st := b.Slots().Status()
	if healthy {
		if err := b.Confirmed(); err != nil {
			return err
		}
		say("watch: slot %s healthy for %v (pid %d): confirmed", st.Current, grace, pid)
		return nil
	}
	say("watch: slot %s not healthy (pid %d)", st.Current, pid)
	if pid != 0 && b.Alive(pid) {
		stop(b, pid)
	}
	if st.Trial {
		s, err := b.Slots().Rollback()
		if err != nil {
			return err
		}
		say("watch: rolled back to slot %s", s)
	}
	return launch(b)
}

// supervise runs the bounded supervisor (board.Supervisor) until SIGTERM
// or SIGINT.
func supervise(b *board.Board, grace time.Duration) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	s := &board.Supervisor{
		B: b, Grace: grace,
		Launch: func() error { return launch(b) },
		Stop:   func(pid int) { stop(b, pid) },
		Done:   func() bool { return ctx.Err() != nil },
		Sleep: func(d time.Duration) {
			select {
			case <-ctx.Done():
			case <-time.After(d):
			}
		},
		Logf: say,
	}
	return s.Run()
}

func stop(b *board.Board, pid int) {
	_ = syscall.Kill(pid, syscall.SIGTERM)
	for range 50 {
		if !b.Alive(pid) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

func arg(a []string, i int, def string) string {
	if i < len(a) {
		return a[i]
	}
	return def
}

// simMark is the simulator's device-log marker node; absent on a unit.
const simMark = "/dev/simd_mark"

// say prints a line and, in the simulator, marks it in the device log.
func say(format string, a ...any) {
	s := fmt.Sprintf(format, a...)
	fmt.Println("otactl:", s)
	if hdguard.FS.CheckDevice(simMark) != nil {
		return
	}
	if f, err := os.OpenFile(simMark, os.O_WRONLY, 0); err == nil {
		_, _ = f.WriteString("ota: " + s + "\n")
		_ = f.Close()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: otactl [-p12 DIR] bootstrap|install|confirm|rollback|status|launch|restart|watch|supervise|takeover|vendor|open ...")
	os.Exit(2)
}
