//go:build simboard

package boardtest

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-sds800xhd/app/internal/frontend"
	"github.com/labeth/open-sds800xhd/app/internal/platform"
)

// TestCalibration runs with the factory DAC file on p9 and a self-cal file
// on p12 (the calibration area seeds them): the front end must report the
// vendor calibration, and the offset-DAC code of a setting must use the
// factory slope: code = trunc(centre − slope·V_off) (spec 06 §5).
func TestCalibration(t *testing.T) {
	c := begin(t, "calibration/source")
	src := board.FrontEnd().Calibration()
	c.check(src.Source == "vendor" && src.Valid, "calibration %+v", src)

	n := board.Profile().Channels
	fh, err := os.Open(filepath.Join(platform.FactoryDir, frontend.FactoryDACFile))
	if !c.noErr(err, "factory file on p9") {
		return
	}
	slopes, lines, err := frontend.ParseFactoryDAC(fh, n)
	_ = fh.Close() // read only: nothing to flush
	c.check(err == nil && lines == 4*n*3, "%d factory lines, %v", lines, err)

	uh, err := os.Open(filepath.Join(frontend.UserCalDir, frontend.UserCalFile))
	if !c.noErr(err, "self-cal file on p12") {
		return
	}
	part := platform.ADC12D1000
	if ident.ADCPart == platform.ADC12DL1500.String() {
		part = platform.ADC12DL1500
	}
	cals, err := frontend.ParseUserCal(uh, n, part)
	_ = uh.Close() // read only: nothing to flush
	if !c.noErr(err, "self-cal file parses") {
		return
	}

	// 1 V/div, offset 0.5 V on C1 alone at 10 µs/div (2 GSa/s, rate mode
	// 0): group X10, attenuator A1X. The step is the one the self-cal gains
	// select by spec 06 §6.2 step 4 (the X10 group scans steps 0 … 10).
	c = begin(t, "calibration/dac-slope")
	p, err := board.Planner().Plan(hal.Request{Channels: []int{0}, TimePerDivS: 10e-6})
	if !c.noErr(err, "plan") {
		return
	}
	m := 2
	if p.SampleS < 0.6e-9 {
		m = 0
	}
	step := 10
	for s := range 11 {
		g, _ := cals[0].Entry(1, 0, s, m)
		if 1000 < g {
			step = s
			if s > 0 {
				gp, _ := cals[0].Entry(1, 0, s-1, m)
				if math.Abs(float64(gp)-1000) < math.Abs(float64(g)-1000) {
					step = s - 1
				}
			}
			break
		}
	}
	_, centre := cals[0].Entry(1, 0, step, m)
	if _, err := only(0, hal.Channel{VoltsDiv: 1, OffsetV: 0.5, Probe: 1, Coupling: hal.DC}); !c.noErr(err, "set") {
		return
	}
	if _, err := acquireOne(0, 10e-6); !c.noErr(err, "record") {
		return
	}
	code := uint16(float64(centre) - float64(slopes[0][0][1])*0.5)
	c.check(true, "step %d, rate mode %d, centre %d, factory slope %g: DAC %#x", step, m, centre, slopes[0][0][1], code)
	c.expect(map[string]uint16{"acq:0x146": code})
	_, _ = only(0, hal.Channel{VoltsDiv: 1, Probe: 1, Coupling: hal.DC})
}
