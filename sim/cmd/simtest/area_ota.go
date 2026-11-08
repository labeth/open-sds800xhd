package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/labeth/open-sds800xhd/sim/harness"
)

// The ota area: the board side of the supervisor on the simulated p12 over
// two boots of one unit (Keep). First boot: the install stick's layout, the
// hook starts slot a, the health check confirms it, an update that dies is
// rolled back and slot a relaunched, and a second broken update is left
// started once and unconfirmed. Second boot: the disable switch starts
// nothing; then the hook goes back to slot a by itself and the health check
// confirms it. There is no watchdog to exercise on this scope, and no
// reboot: each boot is a simulator run.
func init() {
	register(area{
		name: "ota", title: "OTA A/B: install, health, rollback",
		runs: func(e *env) []harness.Spec {
			files := map[string]string{
				"otactl":   filepath.Join(e.root, "build", "otactl"),
				"app":      e.app,
				"broken":   filepath.Join(e.root, "sim", "guest", "broken-app.sh"),
				"extra.sh": e.hook,
			}
			// the update's manifest, as its build writes it
			if b, err := os.ReadFile(files["broken"]); err == nil {
				dir := filepath.Join(e.root, "build", "ota")
				if os.MkdirAll(dir, 0o755) == nil { //nolint:gosec // G301: a build output directory of the repository
					if m, err := writeManifest(dir, "broken", b); err == nil {
						files["broken.sha256"] = m
					}
				}
			}
			name := e.prefix + "ota"
			return []harness.Spec{
				{Name: name, Secs: 150, Guest: filepath.Join(e.root, "sim", "guest", "ota-install.sh"), Files: files},
				{Name: name, Secs: 90, Keep: true, Guest: filepath.Join(e.root, "sim", "guest", "ota-reboot.sh"), Files: files},
				{
					Name: name, Secs: 90, Keep: true, Guest: filepath.Join(e.root, "sim", "guest", "ota-key.sh"), Files: files,
					Scenario: []string{"@4 keyraw 3 4"},
				},
			}
		},
		check: func(_ *env, runs []*harness.Run) []harness.Result {
			if len(runs) < 3 {
				return []harness.Result{harness.Fail("ota", "runs", "three boots needed (with -reuse only the last is on disk)")}
			}
			first, second, third := runs[0], runs[1], runs[2]
			var res []harness.Result
			has := func(r *harness.Run, c, want string) {
				if strings.Contains(r.Console, want) {
					res = append(res, harness.Pass("ota", c, want))
				} else {
					res = append(res, harness.Fail("ota", c, "console lacks %q", want))
				}
			}
			has(first, "first-install", "bootstrap: slot a installed and current")
			has(first, "slot-a-confirmed", "watch: slot a healthy")
			has(first, "update-installed", "install: slot b installed, current, on trial")
			has(first, "update-unhealthy", "watch: slot b not healthy")
			has(first, "health-rollback", "watch: rolled back to slot a")
			has(first, "update-left-on-trial", "status: current b previous a trial true starts 1")
			if n := len(first.Log.Marks("app: takeover")); n >= 2 {
				res = append(res, harness.Pass("ota", "app-relaunched", "slot a's app took the unit over twice"))
			} else {
				res = append(res, harness.Fail("ota", "app-relaunched", "%d takeovers, want 2 (start and after the rollback)", n))
			}

			before, seen := 0, false
			for _, m := range second.Log.Marks("") {
				if m == "ota: disabled boot done" {
					seen = true
					break
				}
				if m == "app: takeover" {
					before++
				}
			}
			switch {
			case !seen:
				res = append(res, harness.Fail("ota", "disable-switch", "no mark of the disabled boot"))
			case before > 0:
				res = append(res, harness.Fail("ota", "disable-switch", "the app started although open/disable existed"))
			default:
				res = append(res, harness.Pass("ota", "disable-switch", "no app started with open/disable"))
			}
			has(second, "vendor-command", "vendor: next boot runs the vendor firmware")
			has(second, "vendor-status", "disabled true")
			has(second, "open-command", "open: next boot runs the open firmware")
			has(second, "boot-rollback-confirmed", "watch: slot a healthy")
			has(second, "boot-rollback-status", "status: current a previous  trial false")
			lg, _ := os.ReadFile(second.File("usr/open/log/launch.log"))
			if strings.Contains(string(lg), "slot b unconfirmed after 1 start(s): back to slot a") {
				res = append(res, harness.Pass("ota", "hook-rollback", "the launch hook went back to slot a"))
			} else {
				res = append(res, harness.Fail("ota", "hook-rollback", "launch.log lacks the hook's rollback:\n%s", lg))
			}
			res = append(res, keyHeld(third)...)
			return res
		},
	})
}

// keyHeld checks the third boot: with the Acquire key held the hook started
// no app, logged why and wrote no disable file.
// TRLC-LINKS: REQ-HD-002
func keyHeld(r *harness.Run) []harness.Result {
	var res []harness.Result
	seen, started := false, 0
	for _, m := range r.Log.Marks("") {
		if m == "ota: key-held boot done" {
			seen = true
			break
		}
		if m == "app: takeover" {
			started++
		}
	}
	switch {
	case !seen:
		res = append(res, harness.Fail("ota", "key-held", "no mark of the key-held boot"))
	case started > 0:
		res = append(res, harness.Fail("ota", "key-held", "the app started although Acquire was held"))
	default:
		res = append(res, harness.Pass("ota", "key-held", "no app started with Acquire held"))
	}
	lg, _ := os.ReadFile(r.File("usr/open/log/launch.log"))
	if strings.Contains(string(lg), "Acquire held at power-up") {
		res = append(res, harness.Pass("ota", "key-held-logged", "launch.log names the held key"))
	} else {
		res = append(res, harness.Fail("ota", "key-held-logged", "launch.log lacks the held key:\n%s", lg))
	}
	if strings.Contains(r.Console, "disabled false") {
		res = append(res, harness.Pass("ota", "key-held-no-disable", "the held key wrote no disable file"))
	} else {
		res = append(res, harness.Fail("ota", "key-held-no-disable", "status after the key-held boot is not \"disabled false\""))
	}
	return res
}
