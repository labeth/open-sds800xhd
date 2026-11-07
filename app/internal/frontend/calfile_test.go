package frontend

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labeth/open-sds800xhd/app/internal/platform"
)

// factoryFile writes a factory DAC file (spec 10 §3) whose points lie on
// code = slope·V + 32768 with the slope given per (d, g), the same for
// every channel, plus 50R lines that must be ignored.
func factoryFile(nch int, slope func(d, g int) float64) string {
	var b strings.Builder
	for _, imp := range []string{"1M", "50R"} {
		for d, dn := range dacNames {
			for k := 1; k <= nch; k++ {
				for g, gn := range groupNames {
					fmt.Fprintf(&b, "%s,%s,%s,CH%d=\"", dn, imp, gn, k)
					for i := range 5 {
						code := 16384 + 8192*i
						s := slope(d, g)
						if imp == "50R" {
							s *= 3
						}
						mv := math.Round((float64(code) - 32768) / s * 1000)
						fmt.Fprintf(&b, "%d,%d;", code, int(mv))
					}
					b.WriteString("\"\n")
				}
			}
		}
	}
	return b.String()
}

func TestParseFactoryDAC(t *testing.T) {
	want := func(d, g int) float64 { return 19.3 * math.Pow(10, float64(2-g)) * float64(d+1) }
	slopes, n, err := ParseFactoryDAC(strings.NewReader(factoryFile(4, want)), 4)
	if err != nil || n != 48 {
		t.Fatalf("%d lines, %v", n, err)
	}
	for c := range 4 {
		for d := range 4 {
			for g := range 3 {
				if got := float64(slopes[c][d][g]); math.Abs(got/want(d, g)-1) > 1e-3 {
					t.Errorf("CH%d d%d g%d: slope %g, want %g", c+1, d, g, got, want(d, g))
				}
			}
		}
	}
	// A 2-channel unit reads only its channels; the others keep the defaults.
	slopes, n, _ = ParseFactoryDAC(strings.NewReader(factoryFile(4, want)), 2)
	if n != 24 || slopes[2] != defaultSlope {
		t.Errorf("2 channels: %d lines, CH3 %v", n, slopes[2])
	}
	if _, _, err := ParseFactoryDAC(strings.NewReader("garbage\n"), 4); err == nil {
		t.Error("a file without lines accepted")
	}
}

// userCals returns a calibration with every entry distinct.
func userCals(nch int, part platform.ADCPart) []UserCal {
	out := make([]UserCal, nch)
	for c := range out {
		d := DefaultCal(part)
		u := &out[c]
		u.FS = make([][3]float64, len(d.fs))
		for j := range d.fs {
			u.FS[j] = [3]float64{d.fs[j][0] + 0.000101*float64(c+1), d.fs[j][1] - 0.000203, 0}
			u.FS[j][2] = u.FS[j][1]
		}
		for g := range 3 {
			for a := range 3 {
				for s := range 21 {
					gn := float32(int(d.vga[g][a][s].gain[0]*1000)+c*7+s) / 1000
					u.VGA[g][a][s] = vgaEntry{
						gain:   [3]float32{gn, gn + 0.002, gn + 0.002},
						centre: [3]int32{int32(32700 + 10*g + a + s), 0, int32(32800 - s)},
					}
					u.VGA[g][a][s].centre[1] = u.VGA[g][a][s].centre[2]
				}
			}
		}
	}
	return out
}

func TestUserCalRoundTrip(t *testing.T) {
	for _, part := range []platform.ADCPart{platform.ADC12D1000, platform.ADC12DL1500} {
		in := userCals(4, part)
		var b bytes.Buffer
		if err := WriteUserCal(&b, in); err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(b.String(), "\n"); n != 1+4*191 {
			t.Errorf("%d lines", n)
		}
		out, err := ParseUserCal(bytes.NewReader(b.Bytes()), 4, part)
		if err != nil {
			t.Fatal(err)
		}
		for c := range in {
			for j := range in[c].FS {
				for col := range 2 {
					if math.Abs(out[c].FS[j][col]-in[c].FS[j][col]) > 2e-6 {
						t.Fatalf("CH%d FS[%d][%d] %g, want %g", c+1, j, col, out[c].FS[j][col], in[c].FS[j][col])
					}
				}
			}
			for g := range 3 {
				for a := range 3 {
					for s := range 21 {
						i, o := in[c].VGA[g][a][s], out[c].VGA[g][a][s]
						if math.Abs(float64(o.gain[0]-i.gain[0])) > 1e-3 || o.centre[0] != i.centre[0] || o.centre[2] != i.centre[2] {
							t.Fatalf("CH%d V[%d][%d][%d] %+v, want %+v", c+1, g, a, s, o, i)
						}
					}
				}
			}
		}
	}
}

// TestUserCalFormat checks a line in the exact form of spec 10 §4.1 and the
// rejections of §4.2.
func TestUserCalFormat(t *testing.T) {
	var b bytes.Buffer
	_ = WriteUserCal(&b, userCals(1, platform.ADC12D1000))
	lines := strings.Split(b.String(), "\n")
	if !strings.HasPrefix(lines[1], "CH1_UNITE_ADC_FULLSCALE_M=\"") || !strings.HasPrefix(lines[2], "CH1_PART_ADC_FULLSCALE_M=\"") {
		t.Errorf("full-scale lines %q %q", lines[1][:30], lines[2][:30])
	}
	if !strings.HasPrefix(lines[3], "FULL_DX1_C1_100X_V0=\"") || !strings.HasPrefix(lines[3+20], "FULL_DX1_C1_100X_V25=\"") ||
		!strings.Contains(lines[3], ",0,0,") {
		t.Errorf("VGA lines %q %q", lines[3], lines[23])
	}
	if !strings.HasPrefix(lines[3+3*21*2+3*21-1], "FULL_DXP25_C1_1X_V25=") {
		t.Errorf("last VGA line %q", lines[3+188])
	}
	for name, txt := range map[string]string{
		"version":   strings.Replace(b.String(), "VER:4", "VER:3", 1),
		"truncated": strings.Join(lines[:100], "\n"),
		"key":       strings.Replace(b.String(), "FULL_DXP5_C1_10X_V7", "FULL_DXP5_C1_10X_V8", 1),
		"negative":  strings.Replace(b.String(), "FULL_DX1_C1_1X_V3=\"", "FULL_DX1_C1_1X_V3=\"-", 1),
	} {
		if _, err := ParseUserCal(strings.NewReader(txt), 1, platform.ADC12D1000); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The example line of spec 10 §4.1 parses on its own.
	_, vals, ok := quoted(`FULL_DX1_C1_1X_V16="1078,32786,1078,32786,1078,32786"`)
	if !ok || len(vals) != 6 || vals[0] != "1078" {
		t.Errorf("%v %v", vals, ok)
	}
}

// TestLoadCalibration applies files from disk and checks they reach the
// DAC code of a channel setup (centre and slope).
func TestLoadCalibration(t *testing.T) {
	fe, _, bus := newTestFE(platform.ADC12D1000)
	dir := t.TempDir()
	if st := fe.LoadCalibration(dir, dir, 4); st.Factory || st.User || st.Detail == "" {
		t.Errorf("no files: %+v", st)
	}
	slope := func(_, g int) float64 { return 2000 * math.Pow(10, float64(g-2)) }
	_ = os.WriteFile(filepath.Join(dir, FactoryDACFile), []byte(factoryFile(4, slope)), 0o644)
	var b bytes.Buffer
	cals := userCals(4, platform.ADC12D1000)
	_ = WriteUserCal(&b, cals)
	_ = os.WriteFile(filepath.Join(dir, UserCalFile), b.Bytes(), 0o644)
	st := fe.LoadCalibration(dir, dir, 4)
	if !st.Factory || !st.User {
		t.Fatalf("%+v", st)
	}
	// 1 V/div, offset 0.1 V: X10, A1X; slope 200 codes/V; centre of the
	// step the (calibrated) gain table picks, unite column.
	fe.SetupChannel(0, Channel{On: true, VoltsDiv: 1, OffsetV: 0.1, Probe: 1, Coupling: DC}, 1, 500)
	d := fe.Derived(0)
	centre := cals[0].VGA[d.Group][d.Att][d.Step].centre[2]
	want := uint32(float64(centre) - float64(float32(200))*0.1)
	if v, _ := bus.Last(0x146 * 4); v != want || fe.Centre(0, false) != centre {
		t.Errorf("DAC %d, want %d (centre %d)", v, want, centre)
	}
}
