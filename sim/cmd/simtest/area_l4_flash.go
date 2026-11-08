package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/labeth/open-sds800xhd/sim/harness"
)

// The flash areas: damaged update images, power cuts at chosen points of
// an update and of the install stick's copy, a power cut followed by a
// reboot from the flash state, and a damaged p12. The guest scripts print
// one "l4: ..." line per case; the checks read them.
func init() {
	l4register(area{
		name: "l4-update", title: "faults: damaged update images, power cuts during an update; a damaged p12",
		runs: func(e *env) []harness.Spec {
			return []harness.Spec{l4Spec(e, "update", "l4-update.sh", 290, nil), l4Spec(e, "p12", "l4-p12.sh", 200, nil)}
		},
		check: func(_ *env, runs []*harness.Run) []harness.Result {
			if len(runs) != 2 {
				return []harness.Result{harness.Fail("l4-update", "update-cut/runs", "%d runs, want 2", len(runs))}
			}
			return append(updateChecks(runs[0]), p12Checks(runs[1])...)
		},
	})
	l4register(area{
		name: "l4-install", title: "faults: power cuts during the install stick's copy, the stick's re-run",
		runs: func(e *env) []harness.Spec {
			// the test binary starts in about 150 ms under the emulator:
			// the cut points cluster past that
			var specs []harness.Spec
			for _, c := range []string{"empty 0 120 150 170 190 220 260 400", "installed 120 150 170 190 220 260 400"} {
				base := strings.Fields(c)[0]
				conf := filepath.Join(e.root, "build", "l4", "install-"+base+".conf")
				_ = os.MkdirAll(filepath.Dir(conf), 0o755)    //nolint:gosec // G301: a build output directory of the repository
				_ = os.WriteFile(conf, []byte(c+"\n"), 0o644) //nolint:gosec // G306: a guest input, not a secret
				specs = append(specs, l4Spec(e, "install-"+base, "l4-install.sh", 290,
					map[string]string{"installtest": filepath.Join(e.root, "build", "installtest"), "l4-install.conf": conf}))
			}
			return specs
		},
		check: func(_ *env, runs []*harness.Run) []harness.Result {
			var res []harness.Result
			for _, r := range runs {
				res = append(res, installChecks(r)...)
			}
			return res
		},
	})
	l4register(area{
		name: "l4-reboot", title: "faults: power cut during an update and during the install, then a boot from the flash state",
		runs: func(e *env) []harness.Spec {
			var specs []harness.Spec
			for _, cut := range []string{"update 230", "install 160"} {
				what := strings.Fields(cut)[0]
				conf := filepath.Join(e.root, "build", "l4", "cut-"+what+".conf")
				_ = os.MkdirAll(filepath.Dir(conf), 0o755)      //nolint:gosec // G301: a build output directory of the repository
				_ = os.WriteFile(conf, []byte(cut+"\n"), 0o644) //nolint:gosec // G306: a guest input, not a secret
				extra := map[string]string{"installtest": filepath.Join(e.root, "build", "installtest"), "l4-cut.conf": conf}
				cutRun := l4Spec(e, "reboot-"+what, "l4-cut.sh", 60, extra)
				bootRun := l4Spec(e, "reboot-"+what, "l4-boot.sh", 60, extra)
				bootRun.Keep = true
				specs = append(specs, cutRun, bootRun)
			}
			return specs
		},
		check: func(_ *env, runs []*harness.Run) []harness.Result { return rebootChecks(runs) },
	})
}

// l4Images are the update images the flash areas install, made from the app
// binary in build/l4: app2 (the app with bytes appended: a different,
// runnable version), app.trunc (cut to a quarter), app.hdr (the ELF header's
// machine field changed) and app.flip (one byte flipped in every MiB past
// the headers).
func l4Images(e *env) (map[string]string, error) {
	dir := filepath.Join(e.root, "build", "l4")
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: a build output directory of the repository
		return nil, err
	}
	app, err := os.ReadFile(e.app)
	if err != nil {
		return nil, err
	}
	if len(app) < 1<<20 {
		return nil, fmt.Errorf("%s: %d bytes, not an app build", e.app, len(app))
	}
	trunc := app[:len(app)/4]
	hdr := append([]byte(nil), app...)
	hdr[18] ^= 0xFF // e_machine
	flip := append([]byte(nil), app...)
	for i := 1 << 20; i < len(flip); i += 1 << 20 {
		flip[i] ^= 0x5A
	}
	images := map[string][]byte{
		"app2":      append(append([]byte(nil), app...), []byte("\nl4 second version\n")...),
		"app.trunc": trunc, "app.hdr": hdr, "app.flip": flip,
	}
	files := l4Files(e)
	for name, b := range images {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o755); err != nil { //nolint:gosec // G306: a test image, executable as the app is
			return nil, err
		}
		files[name] = p
		// The damaged images carry the manifest of the image they were
		// built as, the app: what a copy damaged on its way would arrive with.
		built := b
		if strings.HasPrefix(name, "app.") {
			built = app
		}
		m, err := writeManifest(dir, name, built)
		if err != nil {
			return nil, err
		}
		files[name+".sha256"] = m
	}
	m, err := writeManifest(dir, "app", app)
	if err != nil {
		return nil, err
	}
	files["app.sha256"] = m
	return files, nil
}

// writeManifest writes dir/name.sha256, the update manifest otactl install
// checks an image against: "<hex SHA-256> <size>" of built.
func writeManifest(dir, name string, built []byte) (string, error) {
	sum := sha256.Sum256(built)
	p := filepath.Join(dir, name+".sha256")
	return p, os.WriteFile(p, fmt.Appendf(nil, "%s %d\n", hex.EncodeToString(sum[:]), len(built)), 0o644) //nolint:gosec // G306: a guest input, not a secret
}

// l4Lines are a run's "l4: <prefix> ..." console lines, prefix removed.
func l4Lines(r *harness.Run, prefix string) []string {
	return r.Lines("l4: " + prefix + " ")
}

var (
	knownBoot = regexp.MustCompile(`^(app|app2) from `)
	vendBoot  = regexp.MustCompile(`^vendor `)
)

// bootOf is the "boot ..." part of a line (up to the next keyword).
func bootOf(line, key string) string {
	_, b, ok := strings.Cut(line, key+" ")
	if !ok {
		return ""
	}
	for _, stop := range []string{" after [", " rerun ", " alive-after-", " watch ", " layout "} {
		if i := strings.Index(b, stop); i >= 0 {
			b = b[:i]
		}
	}
	return b
}

func updateChecks(r *harness.Run) []harness.Result {
	const a = "l4-update"
	var res []harness.Result
	if !strings.Contains(r.Console, "l4: update scenario done") {
		res = append(res, harness.Fail(a, "update-cut/done", "the scenario did not finish (time limit?)"))
	}
	for _, l := range l4Lines(r, "corrupt-image") {
		f := strings.Fields(l)
		if len(f) < 3 {
			continue
		}
		img := f[0]
		switch f[1] {
		case "install":
			refused := !strings.HasPrefix(f[3], "0")
			if refused {
				res = append(res, harness.Pass(a, "corrupt-image/"+img+"-refused", l))
			} else {
				res = append(res, harness.Fail(a, "corrupt-image/"+img+"-refused", "accepted and made current on trial: %s", l))
			}
		case "after-trial":
			if strings.HasPrefix(f[3], "app ") || f[3] == "app" {
				res = append(res, harness.Pass(a, "corrupt-image/"+img+"-rolled-back", "the trial went back to slot a: "+l))
			} else {
				res = append(res, harness.Fail(a, "corrupt-image/"+img+"-rolled-back", "after the trial the unit runs %s: %s", f[3], l))
			}
		}
	}
	n := 0
	for _, l := range l4Lines(r, "update-cut") {
		n++
		d := strings.Fields(l)[0]
		b := bootOf(l, "boot")
		if knownBoot.MatchString(b) {
			res = append(res, harness.Pass(a, "update-cut/"+d, l))
		} else {
			res = append(res, harness.Fail(a, "update-cut/"+d, "the boot after the cut runs %q: %s", b, l))
		}
	}
	if n == 0 {
		res = append(res, harness.Fail(a, "update-cut/runs", "no power cut ran"))
	}
	return append(res, guardChecks(a, r)...)
}

func p12Checks(r *harness.Run) []harness.Result {
	const a = "l4-update"
	var res []harness.Result
	if !strings.Contains(r.Console, "l4: p12 scenario done") {
		res = append(res, harness.Fail(a, "corrupt-p12/done", "the scenario did not finish (time limit?)"))
	}
	for _, l := range l4Lines(r, "corrupt-p12") {
		c := strings.Fields(l)[0]
		b := bootOf(l, "boot")
		ok := knownBoot.MatchString(b) || vendBoot.MatchString(b)
		switch c {
		case "settings-garbage":
			ok = ok && strings.HasSuffix(l, "alive-after-8s app")
		case "current-app-damaged":
			// a damaged confirmed app: ours cannot run; the unit must be
			// left to the vendor firmware, not to a damaged binary
			ok = vendBoot.MatchString(b) && !strings.Contains(l, "runs damaged")
		}
		if ok {
			res = append(res, harness.Pass(a, "corrupt-p12/"+c, l))
		} else {
			res = append(res, harness.Fail(a, "corrupt-p12/"+c, "%s", l))
		}
	}
	return append(res, guardChecks(a, r)...)
}

func installChecks(r *harness.Run) []harness.Result {
	const a = "l4-install"
	var res []harness.Result
	if !strings.Contains(r.Console, "l4: install scenario done") {
		res = append(res, harness.Fail(a, "install-cut/done", "the scenario did not finish (time limit?)"))
	}
	n := 0
	for _, l := range l4Lines(r, "install-cut") {
		n++
		f := strings.Fields(l)
		c := "install-cut/" + strings.TrimPrefix(f[0], "base=") + "-" + strings.TrimPrefix(f[1], "d=")
		b := bootOf(l, "boot")
		_, again, _ := strings.Cut(l, " then ")
		switch {
		case !knownBoot.MatchString(b) && !vendBoot.MatchString(b):
			res = append(res, harness.Fail(a, c, "the boot without the stick runs %q: %s", b, l))
		case !strings.HasPrefix(again, "app2 from "):
			res = append(res, harness.Fail(a, c, "the stick's re-run did not complete the install (%q): %s", again, l))
		default:
			res = append(res, harness.Pass(a, c, l))
		}
	}
	if n == 0 {
		res = append(res, harness.Fail(a, "install-cut/runs", "no power cut ran"))
	}
	return append(res, guardChecks(a, r)...)
}

func rebootChecks(runs []*harness.Run) []harness.Result {
	const a = "l4-reboot"
	var res []harness.Result
	if len(runs) != 4 {
		return []harness.Result{harness.Fail(a, "reboot-cut/runs", "%d runs, want 4 (with -reuse only the last of each pair is on disk)", len(runs))}
	}
	for i := 0; i < 4; i += 2 {
		cut, boot := runs[i], runs[i+1]
		what := cut.Name[strings.LastIndex(cut.Name, "-")+1:]
		c := "reboot-cut/" + what
		cl := l4Lines(cut, "power")
		bl := l4Lines(boot, "reboot-cut")
		if len(cl) == 0 || len(bl) == 0 {
			res = append(res, harness.Fail(a, c, "cut %q, boot %q", cl, bl))
			continue
		}
		b := bootOf(bl[0], "boot")
		ok := knownBoot.MatchString(b) || (what == "install" && vendBoot.MatchString(b))
		detail := fmt.Sprintf("%s; %s", cl[0], strings.Join(bl, "; "))
		if ok {
			res = append(res, harness.Pass(a, c, detail))
		} else {
			res = append(res, harness.Fail(a, c, "after the cut the boot runs %q: %s", b, detail))
		}
		res = append(res, guardChecks(a, boot)...)
	}
	return res
}
