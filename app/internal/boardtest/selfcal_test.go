//go:build simboard

package boardtest

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labeth/open-sds800xhd/app/internal/frontend"
	"github.com/labeth/open-sds800xhd/app/internal/platform"
)

// zeroCentre is the device model's offset-DAC code of 0 V with open inputs
// (spec 16 §5.4): every centre a self-calibration finds.
const zeroCentre = 0x8012

// TestSelfCal runs the user self-calibration (spec 10 §5) with every input
// at 0 V (the selfcal area's signals) and checks the file it writes on p12
// against the device model's mean engine.
func TestSelfCal(t *testing.T) {
	c := begin(t, "selfcal/refused-while-armed")
	if _, err := acquireOne(0, 1e-6); !c.noErr(err, "a record first") {
		return
	}
	if !c.noErr(board.Acquirer().Arm(), "arm") {
		return
	}
	_, err := board.FrontEnd().SelfCal(context.Background(), nil)
	c.refused(err, "self-calibration while armed")
	c.noErr(board.Acquirer().Stop(), "stop")

	c = begin(t, "selfcal/run")
	path := filepath.Join(frontend.UserCalDir, frontend.UserCalFile)
	_ = os.Remove(path)
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 280*time.Second)
	defer cancel()
	last := -1.0
	res, err := board.FrontEnd().SelfCal(ctx, func(x float64) {
		if x-last >= 0.1 || x >= 1 {
			fmt.Printf("selfcal progress %.3f at %.1f s\n", x, time.Since(start).Seconds())
			last = x
		}
	})
	if !c.noErr(err, fmt.Sprintf("self-calibration in %.1f s", time.Since(start).Seconds())) {
		return
	}
	c.check(len(res.Failed) == 0, "no channel failed %v", res.Failed)
	c.check(res.Stored == path, "written to %s", res.Stored)
	src := board.FrontEnd().Calibration()
	c.check(src.Source == "open-selfcal" && src.Author == "open" && src.Valid && res.Source == src, "calibration source %+v, reported %+v", src, res.Source)

	c = begin(t, "selfcal/file")
	raw, err := os.ReadFile(path)
	if !c.noErr(err, "the self-cal file on p12") {
		return
	}
	n := board.Profile().Channels
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	c.check(lines[0] == "VER:4" && len(lines) == 1+n*191, "VER %q, %d lines (want 1 + %d·191)", lines[0], len(lines), n)
	part := platform.ADC12D1000
	if ident.ADCPart == platform.ADC12DL1500.String() {
		part = platform.ADC12DL1500
	}
	cals, err := frontend.ParseUserCal(bytes.NewReader(raw), n, part)
	if !c.noErr(err, "the file parses back") {
		return
	}
	bad, bracket, gains, minGain := 0, 0, 0, float32(1e9)
	for _, u := range cals {
		for g := range 3 {
			for d := range 3 {
				for s := range 21 {
					for _, m := range []int{0, 2} {
						gain, centre := u.Entry(g, d, s, m)
						if centre < zeroCentre-2 || centre > zeroCentre+2 {
							bad++
						}
						if centre == 0x4800 || centre == 0xA800 {
							bracket++
						}
						gains++
						minGain = min(minGain, gain)
					}
				}
			}
		}
	}
	c.check(bad == 0, "%d centre codes, %d off %#x (±2)", gains, bad, zeroCentre)
	c.check(bracket == 0, "no centre at the 0x4800/0xA800 bracket")
	c.check(minGain > 0, "%d gains, the smallest %g mV/div", gains, minGain)
	mono, mids := true, []string{}
	for ch, u := range cals {
		for col := range 2 {
			for j := 1; j < len(u.FS); j++ {
				mono = mono && u.FS[j][col] > u.FS[j-1][col]
			}
			mid := u.FS[len(u.FS)/2][col]
			mono = mono && mid > 0.98 && mid < 1.02
			mids = append(mids, fmt.Sprintf("C%d:%.4f", ch+1, mid))
		}
	}
	c.check(mono, "full-scale lines increasing, middle 1.0 ±2 %%: %v", mids)

	c = begin(t, "selfcal/record-after")
	_, err = acquireOne(0, 1e-6)
	c.noErr(err, "a record with the new tables")
	_ = board.Acquirer().Stop()
}
