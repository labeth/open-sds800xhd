package main

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/labeth/open-sds800xhd/sim/harness"
)

// The fault-injection areas (simtest -l4, test-bench layer L4): the device
// model's fault knobs (simd "fault ...": DMA, interrupts, bus errors, a PL
// reload, a clock lock loss, keys), power cuts during an install or an
// update, damaged images and storage, each against the behaviour the specs
// and SAFETY.txt require. Every case is named "<fault>/<aspect>"; the report
// adds a table of the faults with what is required of each.
var l4Areas []area

func l4register(a area) { l4Areas = append(l4Areas, a) }

// l4Required is what each fault requires, with where the requirement comes
// from, for the report's table.
var l4Required = map[string]string{
	"dma-timeout":     "readout failure reported, acquisition resumes, no app restart (spec 03 §4.4, spec 14 §3)",
	"dma-error":       "an errored transfer (DMASR error bits) is detected and reported, not published as a record; acquisition resumes (spec 14 §8, PG021)",
	"irq-lost":        "a lost completion interrupt is a timed-out read: reported, acquisition resumes (spec 14 §3)",
	"pll-unlock":      "lost clock lock reported (spec 06 §9.2 lock check); the app keeps serving and recovers when lock returns",
	"pl-reload":       "the app keeps serving; a reconfigured PL needs its setup redone (spec 13 §3.3)",
	"key-stuck":       "a held key does not wedge the UI: SCPI and web answer, other input acts after release (spec 08)",
	"key-chatter":     "a bouncing key does not wedge the UI (spec 08)",
	"knob-spin":       "a spinning encoder does not wedge the UI; its steps stay bounded (spec 08)",
	"bus-error":       "an app ended by a bus error is relaunched by the supervisor; the slot stays current and confirmed (SAFETY.txt, spec 01 §12.3)",
	"bus-error-loop":  "a fault that kills every start does not crash-loop: bounded starts, then the vendor firmware (SAFETY.txt: 3 reboots then open/disable)",
	"corrupt-image":   "a corrupt update is refused before activation, or at worst rolled back by the trial (spec 01 §8, SAFETY.txt)",
	"update-cut":      "power lost at any point of an update boots a complete, known-good slot (spec 01 §12.4, slots: pointer last)",
	"install-cut":     "power lost at any point of the install stick leaves a unit that boots the vendor firmware or ours, never bricked; the stick's re-run completes it (spec 01 §12.4)",
	"reboot-cut":      "a power cut and a reboot from the flash state boots (spec 01 §12.4, SAFETY.txt)",
	"corrupt-p12":     "damaged layout files, hook, settings or a damaged current app leave a bootable unit (SAFETY.txt: vendor or ours)",
	"storage-full":    "a full p12 does not crash the app; an update into it fails cleanly and leaves the current slot (spec 01 §8)",
	"storage-ro":      "a read-only p12 does not crash the app; updates are refused; the unit stays recoverable (SAFETY.txt key route)",
	"write-guard":     "no write reaches a path outside p12, p12's vendor update trees, p9, a block device, or a forbidden /dev/mem window (SAFETY.txt rules 1-6)",
	"guard-symlinks":  "symlinks planted in p12 cannot carry a write outside it (SAFETY.txt: after resolving .. and symlinks)",
	"forbidden-mmio":  "no /dev/mem write by our code lands in the CSU, eFuse, BBRAM or SD/eMMC controllers (SAFETY.txt rule 3)",
	"forbidden-paths": "no open for writing of a block device, eFuse, BBRAM or nvmem node (SAFETY.txt rules 1, 3)",
}

// l4Files are the guest files every fault run carries.
func l4Files(e *env) map[string]string {
	return map[string]string{
		"otactl":   filepath.Join(e.root, "build", "otactl"),
		"app":      e.app,
		"extra.sh": e.hook,
	}
}

// l4Spec is a fault run of a guest script with the fault lane's files: the
// update images (l4Images), the shared script and extra (name → host path).
func l4Spec(e *env, name, script string, secs int, extra map[string]string) harness.Spec {
	files, err := l4Images(e)
	if err != nil {
		// the run cannot start without its images: the guest finds none
		// and the checks report the cases missing
		files = l4Files(e)
	}
	files["l4-lib.sh"] = l4Guest(e, "l4-lib.sh")
	maps.Copy(files, extra)
	return harness.Spec{Name: e.prefix + name, Secs: secs, SimdArgs: signals, Guest: l4Guest(e, script), Files: files}
}

// l4Guest is the path of a fault lane guest script.
func l4Guest(e *env, name string) string { return filepath.Join(e.root, "sim", "guest", name) }

// faultCounts parses the device model's "fault" reply: how often each kind
// fired.
func faultCounts(reply string) map[string]int {
	m := map[string]int{}
	_, rest, ok := strings.Cut(reply, "fired [")
	if !ok {
		return m
	}
	rest = strings.TrimSuffix(strings.TrimSpace(rest), "]")
	// kinds hold spaces ("dma timeout=4 irq dmadata_irq=3")
	for _, kv := range countRE.FindAllStringSubmatch(rest, -1) {
		n, _ := strconv.Atoi(kv[2])
		m[strings.TrimSpace(kv[1])] += n
	}
	return m
}

var countRE = regexp.MustCompile(`([^=]+?)=(\d+)`)

// seqOf is the published-frame counter of a status document.
func seqOf(st map[string]any) float64 {
	if v, ok := st["seq"].(float64); ok {
		return v
	}
	if r, ok := st["run"].(map[string]any); ok {
		if v, ok := r["seq"].(float64); ok {
			return v
		}
	}
	v, _ := st["published"].(float64)
	return v
}

// flowing waits up to d for the published-frame counter to move.
func (u *uiRig) flowing(d time.Duration) (bool, float64) {
	s0 := seqOf(u.status())
	for end := time.Now().Add(d); time.Now().Before(end) && u.ctx.Err() == nil; time.Sleep(500 * time.Millisecond) {
		if s := seqOf(u.status()); s > s0 {
			return true, s - s0
		}
	}
	return false, 0
}

// answers reports whether the web status and SCPI *IDN? both answer, and
// how long the slower took.
func (u *uiRig) answers() (bool, time.Duration) {
	t0 := time.Now()
	web := len(u.status()) > 0
	id, err := scpiQuery(u.ctx, "*IDN?")
	return web && err == nil && id != "", time.Since(t0)
}

// appLog is the app's log on the run's p12.
func appLog(r *harness.Run) string {
	b, _ := os.ReadFile(r.File("usr/open/log/app.log"))
	return string(b)
}

// logHas is a check that the app's log has a line matching re.
func logHas(area, c string, r *harness.Run, re, what string) harness.Result {
	m := regexp.MustCompile(re).FindString(appLog(r))
	if m != "" {
		return harness.Pass(area, c, fmt.Sprintf("%s: %q", what, m))
	}
	return harness.Fail(area, c, "the app's log has no line for %s (/%s/)", what, re)
}

// evMMIO is the device log's register access event.
const evMMIO = "mmio"

// forbiddenWindows are the ZynqMP blocks no /dev/mem write may reach
// (SAFETY.txt rule 3; ota/fsguard's physical windows).
var forbiddenWindows = []struct {
	lo, n uint64
	name  string
}{
	{0xFF16_0000, 0x1_0000, "SD0/eMMC host controller"},
	{0xFF17_0000, 0x1_0000, "SD1/eMMC host controller"},
	{0xFFCA_0000, 0x1_0000, "CSU"},
	{0xFFCC_0000, 0x1_0000, "eFuse controller"},
	{0xFFCD_0000, 0x1_0000, "BBRAM key controller"},
}

// forbiddenPath is a device path no write may open (SAFETY.txt rules 1, 3).
var forbiddenPath = regexp.MustCompile(`^/dev/(mmcblk|mtd|nvme)|efuse|bbram|nvmem|/dev/block/`)

// guardChecks are the write guard's invariants over one run's device log,
// after the vendor bring-up the run script does (the mark "hook modules
// done"): no MMIO write into a forbidden window, no forbidden device opened
// for writing.
func guardChecks(area string, r *harness.Run) []harness.Result {
	evs := r.Log.Window("hook modules done", "\x00")
	if evs == nil {
		evs = r.Log.Events
	}
	var mm, paths []string
	for i := range evs {
		e := &evs[i]
		switch e.Ev {
		case evMMIO:
			if e.Op != "w" {
				continue
			}
			a := e.Address()
			for _, w := range forbiddenWindows {
				if a >= w.lo && a < w.lo+w.n {
					mm = append(mm, fmt.Sprintf("%#x (%s)", a, w.name))
				}
			}
		case "open":
			fl, _ := strconv.ParseUint(strings.TrimPrefix(e.Flags, "0x"), 16, 32)
			if fl&3 != 0 && forbiddenPath.MatchString(e.Path) {
				paths = append(paths, e.Path+" "+e.Flags)
			}
		}
	}
	var res []harness.Result
	if len(mm) == 0 {
		res = append(res, harness.Pass(area, "forbidden-mmio/"+r.Name, fmt.Sprintf("%d events after the bring-up, no write in a forbidden window", len(evs))))
	} else {
		res = append(res, harness.Fail(area, "forbidden-mmio/"+r.Name, "writes in forbidden windows: %s", strings.Join(first(mm, 5), ", ")))
	}
	if len(paths) == 0 {
		res = append(res, harness.Pass(area, "forbidden-paths/"+r.Name, "no block, eFuse, BBRAM or nvmem node opened for writing"))
	} else {
		res = append(res, harness.Fail(area, "forbidden-paths/"+r.Name, "opened for writing: %s", strings.Join(first(paths, 5), ", ")))
	}
	return res
}

func first(s []string, n int) []string {
	if len(s) > n {
		return append(s[:n:n], fmt.Sprintf("… %d more", len(s)-n))
	}
	return s
}

// renderL4 is the fault table: per fault (a case's name up to "/"), what is
// required, what was observed (the cases' details) and the verdict.
func renderL4(results []harness.Result) string {
	type row struct {
		pass, fail int
		obs        []string
	}
	rows := map[string]*row{}
	var order []string
	for _, r := range results {
		f, _, _ := strings.Cut(r.Case, "/")
		if l4Required[f] == "" {
			continue
		}
		w := rows[f]
		if w == nil {
			w = &row{}
			rows[f] = w
			order = append(order, f)
		}
		if r.Pass {
			w.pass++
		} else {
			w.fail++
			w.obs = append(w.obs, "FAIL "+r.Case+": "+r.Detail)
		}
	}
	var b strings.Builder
	b.WriteString("\nFAULT TABLE\n")
	tw := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "FAULT\tREQUIRED (SOURCE)\tPASS\tFAIL\tVERDICT")
	for _, f := range order {
		w := rows[f]
		v := passWord
		if w.fail > 0 {
			v = "FAIL"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\n", f, l4Required[f], w.pass, w.fail, v)
	}
	_ = tw.Flush()
	return b.String()
}
