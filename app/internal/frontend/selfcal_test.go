package frontend

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/labeth/open-sds800xhd/app/hdguard"
	"github.com/labeth/open-sds800xhd/app/internal/platform"
)

// lineProber is a channel whose mean code is a straight line in the DAC
// code: m = CENTER + (centre − dac)·k, clamped to the 16-bit-scaled range.
type lineProber struct {
	centre [4]float64
	k      [4]float64
	dac    [4]int
	means  int
}

func (p *lineProber) SetDAC(c, code int) { p.dac[c] = code }
func (p *lineProber) Wait(int)           {}
func (p *lineProber) Mean(c int) (float64, error) {
	p.means++
	m := calCenter + (p.centre[c]-float64(p.dac[c]))*p.k[c]
	code := math.Max(0, math.Min(4095, math.Round(m/16)))
	return code * 16, nil
}

// kFor is the slope k of a channel at vdiv V/div with an offset DAC of
// slope codes per volt: 7680 codes per division.
func kFor(vdiv, slope float64) float64 { return calKDiv / (vdiv * slope) }

func TestFullSearch(t *testing.T) {
	tests := []struct {
		name   string
		centre float64
		vdiv   float64
		fail   bool
	}{
		{"mid-scale 50 mV/div", 0x8012, 0.05, false},
		{"off-centre 10 mV/div", 0x7A00, 0.01, false},
		{"fine 1 mV/div", 0x8100, 0.001, false},
		{"centre beyond the bracket", 0xC000, 0.05, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const slope = 1948.4
			p := &lineProber{centre: [4]float64{tt.centre}, k: [4]float64{kFor(tt.vdiv, slope)}}
			var out [4]centreResult
			if err := fullSearch(context.Background(), p, []int{0}, [4]float32{slope}, &out); err != nil {
				t.Fatal(err)
			}
			r := out[0]
			if tt.fail {
				if r.failed == "" {
					t.Fatalf("want a failure, have centre %#x", r.centre)
				}
				return
			}
			if r.failed != "" {
				t.Fatal(r.failed)
			}
			if math.Abs(float64(r.centre)-tt.centre) > 2 {
				t.Errorf("centre %#x, want %#x", r.centre, int(tt.centre))
			}
			if math.Abs(float64(r.vdiv)/tt.vdiv-1) > 0.02 {
				t.Errorf("V/div %g, want %g", r.vdiv, tt.vdiv)
			}
			if r.p2.dac <= r.p1.dac || r.p1.m <= calCenter || r.p2.m >= calCenter {
				t.Errorf("probes %+v %+v: P1 above and P2 below the centre", r.p1, r.p2)
			}
		})
	}
}

// TestQuickSearch: the quick search with ratio r from a reference full
// search measures a gain r times the reference (spec 10 §5.4 step 4), and
// the step-5 variant with the kept probes gives the same centre.
func TestQuickSearch(t *testing.T) {
	const slope = 1948.4
	p := &lineProber{centre: [4]float64{0x8012, 0x8020}, k: [4]float64{kFor(0.05, slope), kFor(0.05, slope)}}
	var ref [4]centreResult
	ctx := context.Background()
	if err := fullSearch(ctx, p, []int{0, 1}, [4]float32{slope, slope}, &ref); err != nil {
		t.Fatal(err)
	}
	for _, r := range []float32{0.75, 1, 1.25} {
		p.k[0], p.k[1] = kFor(0.05*float64(r), slope), kFor(0.05*float64(r), slope)
		var plus, minus [4]int
		for c := range 2 {
			plus[c], minus[c] = quickProbes(r, ref[c].c0, ref[c].p1, ref[c].p2)
		}
		before := p.means
		var out [4]centreResult
		if err := quickSearch(ctx, p, []int{0, 1}, plus, minus, [4]float32{slope, slope}, &out); err != nil {
			t.Fatal(err)
		}
		if p.means-before < 6 || p.means-before > 6*4 {
			t.Errorf("r %g: %d means, want three per channel", r, p.means-before)
		}
		for c := range 2 {
			if out[c].failed != "" {
				t.Fatal(out[c].failed)
			}
			if math.Abs(float64(out[c].centre)-p.centre[c]) > 2 {
				t.Errorf("r %g C%d: centre %#x", r, c+1, out[c].centre)
			}
			if got := out[c].vdiv / ref[c].vdiv; math.Abs(float64(got/r)-1) > 0.02 {
				t.Errorf("r %g C%d: ratio %g", r, c+1, got)
			}
		}
	}
}

// TestQuickArithmetic pins the float32/truncation rules of spec 10 §5.4
// step 4 on hand-computed values.
func TestQuickArithmetic(t *testing.T) {
	p1 := calPoint{dac: 0x7F00, m: calCenter + calDelta}
	p2 := calPoint{dac: 0x8124, m: calCenter - calDelta}
	plus, minus := quickProbes(1, 0x8012, p1, p2)
	if plus != 0x7F00 || minus != 0x8124 {
		t.Errorf("r 1: probes %#x %#x, want the reference probes", plus, minus)
	}
	plus, minus = quickProbes(0.5, 0x8012, p1, p2)
	if plus != 0x8012-0x89 || minus != 0x8012+0x89 {
		t.Errorf("r 0.5: probes %#x %#x", plus, minus)
	}
	x, ok := quickCode(0x7F00, 0x8124, calCenter+calDelta, calCenter-calDelta)
	if !ok || x != 0x8012 {
		t.Errorf("x %#x %v, want 0x8012", x, ok)
	}
	if _, ok := quickCode(1, 2, 100, 100); ok {
		t.Error("equal means accepted")
	}
	c, v := quickResult(0x8012, calCenter+160, 0x7F00, 0x8124, calCenter+calDelta, calCenter-calDelta, 1948.4)
	// k = (c₊ − c₋)/(m₊ − m₋) = −548/46080: x − k·160 + 0.5 = 0x8012 + 2.4.
	if c != 0x8014 {
		t.Errorf("centre %#x, want 0x8014", c)
	}
	if want := float32(0x224) / (2 * calDelta) * calKDiv / 1948.4; math.Abs(float64(v-want)) > 1e-6 {
		t.Errorf("V/div %g, want %g", v, want)
	}
}

// modelFE is a front end on a fake bus whose PL mean engine reads the
// channel's 0 V input through the analog state the front end set: VGA
// step, DAC attenuator, full-scale code and offset DAC.
func modelFE(t *testing.T, part platform.ADCPart, overflow int) (*FrontEnd, *int) {
	t.Helper()
	fe, _, bus := newTestFE(part)
	fe.sleep = func(time.Duration) {}
	centre := [4]float64{0x8012, 0x8010, 0x8016, 0x8012}
	starts := new(int)
	geo := fsFor(part)
	mean := func() float64 {
		v, _ := bus.Last(0x1B0 * 4)
		c := int(v & 3)
		bits := fe.vga[c] & 0x1F
		s := int(bits)
		if bits >= 0x10 {
			s = int(bits) - 0x10 + 11
		}
		nib := fe.relay >> (32 + 4*c) & 0xF
		d := map[uint64]int{0x8: 0, 0x0: 1, 0x1: 2, 0x2: 3}[nib]
		var code int
		if part == platform.ADC12D1000 {
			code = int(fe.ADC.reg[c/2][[2]int{0x3, 0xB}[1-c%2]])
		} else {
			code = 0x4000
		}
		F := float64(geo.f0) + float64(code-geo.codeMin)/float64(geo.codeMax-geo.codeMin)*(1.25-float64(geo.f0))
		vdiv := float64(g0[s]) / 1000 * F
		pos := (centre[c] - float64(fe.dac[c])) / float64(defaultSlope[d][groupX1]) / vdiv
		return math.Max(0, math.Min(4095, 2048+480*pos))
	}
	bus.Status[0x1B1*4] = func() uint32 {
		v, _ := bus.Last(0x1B0 * 4)
		if overflow >= 0 && int(v&3) == overflow {
			return 1 | 1<<(12+overflow)
		}
		return 1
	}
	bus.Status[0x1B5*4] = func() uint32 { return 1000 }
	bus.Status[0x1B6*4] = func() uint32 { return 0 }
	sum := func(sh uint) func() uint32 {
		return func() uint32 { return uint32(uint64(math.Round(mean()*1000)) >> sh & 0xFFFF) }
	}
	bus.Status[0x1B2*4], bus.Status[0x1B3*4], bus.Status[0x1B4*4] = sum(0), sum(16), sum(32)
	bus.Status[0x1AE*4] = func() uint32 { *starts++; return 1 }
	return fe, starts
}

// guardedDir is a temporary directory the write guard is rooted at for one
// test.
func guardedDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	restore, err := hdguard.FS.SetRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	return dir
}

func TestSelfCal(t *testing.T) {
	fe, _ := modelFE(t, platform.ADC12D1000, -1)
	dir := guardedDir(t)
	var masks []int
	var last float64
	res, err := fe.SelfCal(context.Background(), 4, SelfCalEnv{
		Channels: func(mask int) (bool, error) { masks = append(masks, mask); return mask&(mask-1) == 0, nil },
		Progress: func(x float64) {
			if x < last {
				t.Errorf("progress went back from %g to %g", last, x)
			}
			last = x
		},
		Dir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Failed) != 0 {
		t.Fatalf("failed: %v", res.Failed)
	}
	if want := []int{0xF, 1, 2, 4, 8, 0xF, 1, 2, 4, 8}; !equalInts(masks, want) {
		t.Errorf("channel sets %v, want %v", masks, want)
	}
	if last < 0.99 {
		t.Errorf("progress ended at %g", last)
	}
	raw, err := os.ReadFile(filepath.Join(dir, UserCalFile))
	if err != nil {
		t.Fatal(err)
	}
	cals, err := ParseUserCal(bytes.NewReader(raw), 4, platform.ADC12D1000)
	if err != nil {
		t.Fatal(err)
	}
	want := [4]int32{0x8012, 0x8010, 0x8016, 0x8012}
	for c, u := range cals {
		for g := range 3 {
			for d := range 3 {
				for s := range 21 {
					for _, m := range []int{rateSingle, rateUnite} {
						gain, centre := u.Entry(g, d, s, m)
						if centre < want[c]-2 || centre > want[c]+2 {
							t.Fatalf("C%d g%d d%d s%d m%d: centre %#x", c+1, g, d, s, m, centre)
						}
						// The self-cal's own slopes (no factory file)
						// against the model's driver slopes.
						exp := float64(g0[s]) * float64(defaultSlope[d][groupX1]) / float64(100*selfCalK[d])
						if g != groupX1 {
							exp *= 10
							if g == groupX100 {
								exp *= 10
							}
						}
						if math.Abs(float64(gain)/exp-1) > 0.03 {
							t.Fatalf("C%d g%d d%d s%d m%d: gain %g mV/div, want %g", c+1, g, d, s, m, gain, exp)
						}
					}
				}
			}
		}
		mid := u.FS[20]
		if math.Abs(mid[0]-1) > 0.005 || math.Abs(mid[1]-1) > 0.005 {
			t.Errorf("C%d: full scale at the default code %v, want 1", c+1, mid)
		}
		for j := 1; j < len(u.FS); j++ {
			if u.FS[j][0] <= u.FS[j-1][0] || u.FS[j][1] <= u.FS[j-1][1] {
				t.Fatalf("C%d: full scale not increasing at %d: %v %v", c+1, j, u.FS[j-1], u.FS[j])
			}
		}
	}
	if st := fe.CalStatus(); !st.User || !st.SelfCal {
		t.Errorf("status %+v after the self-cal", st)
	}
	if fe.selfCal {
		t.Error("the self-cal flag stayed set")
	}
}

// TestSelfCalOverflow: a channel whose mean engine overflows fails and
// keeps its tables; the others are calibrated.
func TestSelfCalOverflow(t *testing.T) {
	fe, _ := modelFE(t, platform.ADC12D1000, 2)
	before := fe.cal[2]
	res, err := fe.SelfCal(context.Background(), 4, SelfCalEnv{
		Channels: func(int) (bool, error) { return false, nil }, Dir: guardedDir(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Failed) != 1 {
		t.Fatalf("failed %v, want C3 alone", res.Failed)
	}
	if fe.cal[2].vga != before.vga {
		t.Error("the failed channel's table changed")
	}
}

// TestSelfCalNoEngine: a mean engine that never reports valid stops the
// self-calibration with ErrMeanEngine; a cancelled context stops it too.
func TestSelfCalNoEngine(t *testing.T) {
	fe, _, bus := newTestFE(platform.ADC12D1000)
	fe.sleep = func(time.Duration) {}
	bus.Status[0x1B1*4] = func() uint32 { return 0 }
	env := SelfCalEnv{Channels: func(int) (bool, error) { return false, nil }, Dir: t.TempDir()}
	if _, err := fe.SelfCal(context.Background(), 2, env); !errors.Is(err, ErrMeanEngine) {
		t.Errorf("err %v, want ErrMeanEngine", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fe.SelfCal(ctx, 2, env); !errors.Is(err, context.Canceled) {
		t.Errorf("err %v, want cancelled", err)
	}
	if _, err := os.Stat(filepath.Join(env.Dir, UserCalFile)); err == nil {
		t.Error("a file was written by a self-cal that did not finish")
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
