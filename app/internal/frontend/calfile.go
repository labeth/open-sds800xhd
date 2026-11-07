package frontend

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/labeth/open-sds800xhd/app/internal/platform"
)

// The calibration files of spec 10 §1: the factory offset-DAC file on p9
// and the user self-calibration file on p12.
const (
	FactoryDACFile = "acq_self_cal_factory_dac.txt"
	UserCalFile    = "acq_self_cal_usr.txt"
	// UserCalDir is where the user self-calibration lives on p12.
	UserCalDir = platform.P12 + "/usr"
)

// factoryReadMax is how much of the factory file is read (spec 10 §3).
const factoryReadMax = 10000

// dacNames and groupNames are the key fields of the factory DAC file, in
// the order of the attenuator d and the group g (spec 10 §3).
var (
	dacNames   = [4]string{"DAC1X", "DAC0.5X", "DAC0.25X", "DAC0.1X"}
	groupNames = [3]string{"X100", "X10", "X1"}
)

// quoted returns the values between the quotes of a line, split at ',' and
// ';' (the positional rule of spec 10 §1), and the key before the '='.
func quoted(line string) (key string, vals []string, ok bool) {
	i := strings.IndexByte(line, '"')
	j := strings.LastIndexByte(line, '"')
	if i < 0 || j <= i {
		return "", nil, false
	}
	key = strings.TrimSuffix(strings.TrimSpace(line[:i]), "=")
	for f := range strings.FieldsFuncSeq(line[i+1:j], func(r rune) bool { return r == ',' || r == ';' }) {
		vals = append(vals, strings.TrimSpace(f))
	}
	return key, vals, true
}

// ParseFactoryDAC reads the factory offset-DAC file (spec 10 §3) for a unit
// with nch channels: for each 1 MΩ line a least-squares line code = a·V + b
// through its five (code, millivolt) points gives the slope a in codes per
// volt. It returns slope[c][d][g]; a line that is missing or does not fit
// leaves that entry at the default.
func ParseFactoryDAC(r io.Reader, nch int) (slopes [4][4][3]float32, n int, err error) {
	for c := range slopes {
		slopes[c] = defaultSlope
	}
	b, err := io.ReadAll(io.LimitReader(r, factoryReadMax))
	if err != nil {
		return slopes, 0, err
	}
	for line := range strings.Lines(string(b)) {
		key, vals, ok := quoted(line)
		if !ok {
			continue
		}
		f := strings.Split(key, ",")
		if len(f) != 4 || f[1] != "1M" || !strings.HasPrefix(f[3], "CH") {
			continue
		}
		d := index(dacNames[:], f[0])
		g := index(groupNames[:], f[2])
		k, kerr := strconv.Atoi(f[3][2:])
		if d < 0 || g < 0 || kerr != nil || k < 1 || k > nch {
			continue
		}
		a, ok := fitSlope(vals)
		if !ok {
			continue
		}
		slopes[k-1][d][g] = a
		n++
	}
	if n == 0 {
		return slopes, 0, errors.New("no 1M line")
	}
	return slopes, n, nil
}

// fitSlope is the least-squares slope of code over volts (val/1000) of the
// "code,val" pairs.
func fitSlope(vals []string) (float32, bool) {
	if len(vals) < 4 || len(vals)%2 != 0 {
		return 0, false
	}
	var sx, sy, sxx, sxy float64
	n := float64(len(vals) / 2)
	for i := 0; i < len(vals); i += 2 {
		y, err1 := strconv.ParseFloat(vals[i], 64)
		x, err2 := strconv.ParseFloat(vals[i+1], 64)
		if err1 != nil || err2 != nil {
			return 0, false
		}
		x /= 1000
		sx, sy, sxx, sxy = sx+x, sy+y, sxx+x*x, sxy+x*y
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 0, false
	}
	a := (n*sxy - sx*sy) / den
	if a <= 0 || math.IsInf(a, 0) || math.IsNaN(a) {
		return 0, false
	}
	return float32(a), true
}

func index(names []string, s string) int {
	for i, n := range names {
		if n == s {
			return i
		}
	}
	return -1
}

// UserCal is the content of the user self-calibration file of one channel:
// the full-scale table (columns single, unite) and the VGA table.
type UserCal struct {
	FS  [][3]float64
	VGA [3][3][21]vgaEntry
}

// Entry is the VGA table entry of group g, DAC attenuator d, step s and
// rate mode m (0 at 2 GSa/s, 2 below): mV/div and offset-DAC centre.
func (u *UserCal) Entry(g, d, s, m int) (gainMV float32, centre int32) {
	e := u.VGA[g][d][s]
	return e.gain[m], e.centre[m]
}

// vgaName is the file's name of VGA step s: the VGA bits 4:0 (spec 10 §4.1).
func vgaName(s int) int {
	if s <= 10 {
		return s
	}
	return s + 5
}

var (
	userGroups = [3]string{"_100X", "_10X", "_1X"}
	userDACs   = [3]string{"DX1_", "DXP5_", "DXP25_"}
)

// ParseUserCal reads the user self-calibration file (spec 10 §4) of a unit
// with nch channels and the converter part. The file is positional: the
// version line, then per channel two full-scale lines and 189 VGA lines.
// Any deviation rejects the whole file (the defaults stay).
func ParseUserCal(r io.Reader, nch int, part platform.ADCPart) ([]UserCal, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	next := func() (string, bool) {
		for sc.Scan() {
			if l := strings.TrimSpace(sc.Text()); l != "" {
				return l, true
			}
		}
		return "", false
	}
	if l, ok := next(); !ok || l != "VER:4" {
		return nil, errors.New("version mismatch")
	}
	nfs := fsFor(part).n
	out := make([]UserCal, nch)
	for c := range nch {
		u := &out[c]
		u.FS = make([][3]float64, nfs)
		for col, want := range []string{"_UNITE_ADC_FULLSCALE_M", "_PART_ADC_FULLSCALE_M"} {
			l, _ := next()
			key, vals, ok := quoted(l)
			if !ok || key != fmt.Sprintf("CH%d%s", c+1, want) || len(vals) != nfs {
				return nil, fmt.Errorf("CH%d full-scale line %d: %q", c+1, col+1, trunc(l))
			}
			for j, v := range vals {
				x, err := strconv.ParseUint(v, 10, 32)
				if err != nil {
					return nil, fmt.Errorf("CH%d full scale: %w", c+1, err)
				}
				u.FS[j][col] = float64(x) / 1e6
			}
		}
		for j := range u.FS {
			u.FS[j][2] = u.FS[j][1]
		}
		for g := range 3 {
			for d := range 3 {
				for s := range 21 {
					l, _ := next()
					key, vals, ok := quoted(l)
					want := fmt.Sprintf("FULL_%sC%d%s_V%d", userDACs[d], c+1, userGroups[g], vgaName(s))
					if !ok || key != want || len(vals) != 6 {
						return nil, fmt.Errorf("want %s, have %q", want, trunc(l))
					}
					var n [6]uint64
					for i, v := range vals {
						x, err := strconv.ParseUint(v, 10, 32)
						if err != nil {
							return nil, fmt.Errorf("%s: %w", want, err)
						}
						n[i] = x
					}
					// Column 1 (rate mode 1) is never measured: ignored on recall.
					e := &u.VGA[g][d][s]
					e.gain = [3]float32{float32(n[0]) / 1000, float32(n[4]) / 1000, float32(n[4]) / 1000}
					e.centre = [3]int32{int32(n[1]), int32(n[5]), int32(n[5])} //nolint:gosec // G115: the centres are 16-bit ADC codes in the calibration file
				}
			}
		}
	}
	return out, nil
}

// WriteUserCal writes the user self-calibration file in the layout of spec
// 10 §4.1 (column 1 written 0,0), so a recall reads back what was written.
func WriteUserCal(w io.Writer, cals []UserCal) error {
	bw := bufio.NewWriter(w)
	// bufio.Writer keeps the first write error and Flush returns it.
	_, _ = fmt.Fprintln(bw, "VER:4")
	for c, u := range cals {
		for col, key := range []string{"_UNITE_ADC_FULLSCALE_M", "_PART_ADC_FULLSCALE_M"} {
			vs := make([]string, len(u.FS))
			for j := range u.FS {
				vs[j] = strconv.FormatUint(uint64(float32(u.FS[j][col])*1e6), 10)
			}
			_, _ = fmt.Fprintf(bw, "CH%d%s=\"%s\"\n", c+1, key, strings.Join(vs, ","))
		}
		for g := range 3 {
			for d := range 3 {
				for s := range 21 {
					e := u.VGA[g][d][s]
					_, _ = fmt.Fprintf(bw, "FULL_%sC%d%s_V%d=\"%d,%d,0,0,%d,%d\"\n", userDACs[d], c+1, userGroups[g], vgaName(s),
						uint32(e.gain[0]*1000), e.centre[0], uint32(e.gain[2]*1000), e.centre[2])
				}
			}
		}
	}
	return bw.Flush()
}

func trunc(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}

// CalStatus says which calibration data the front end runs on.
type CalStatus struct {
	Factory, User bool   // the factory DAC file and the user self-cal file were applied
	SelfCal       bool   // the user tables come from a self-calibration this process ran
	Detail        string // why a file was not applied
}

// LoadCalibration recalls the factory DAC file from factoryDir and the
// user self-calibration from userDir (spec 10 §7 steps 2 and 4); a missing
// or rejected file keeps the defaults for its part.
func (f *FrontEnd) LoadCalibration(factoryDir, userDir string, nch int) CalStatus {
	var st CalStatus
	var why []string
	if fh, err := os.Open(filepath.Join(factoryDir, FactoryDACFile)); err == nil { //nolint:gosec // G304: the factory calibration file in the directory the board names
		slopes, _, perr := ParseFactoryDAC(fh, nch)
		_ = fh.Close() // read only: nothing to flush
		if perr == nil {
			for c := range f.cal {
				f.cal[c].slope = slopes[c]
			}
			st.Factory = true
		} else {
			why = append(why, "factory DAC file: "+perr.Error())
		}
	} else {
		why = append(why, "no factory DAC file")
	}
	if fh, err := os.Open(filepath.Join(userDir, UserCalFile)); err == nil { //nolint:gosec // G304: the user calibration file in the directory the board names
		cals, perr := ParseUserCal(fh, nch, f.part)
		_ = fh.Close() // read only: nothing to flush
		if perr == nil {
			for c, u := range cals {
				f.cal[c].fs = u.FS
				f.cal[c].vga = u.VGA
			}
			st.User = true
		} else {
			why = append(why, "self-cal file: "+perr.Error())
		}
	} else {
		why = append(why, "no self-cal file")
	}
	st.Detail = strings.Join(why, "; ")
	f.calStatus = st
	return st
}

// CalStatus returns what the last LoadCalibration applied.
func (f *FrontEnd) CalStatus() CalStatus { return f.calStatus }

// Centre is channel c's offset-DAC centre code for its last setup: the
// calibration entry the DAC code was computed from.
func (f *FrontEnd) Centre(c int, fullRate bool) int32 {
	d := f.derived[c]
	m := rateUnite
	if fullRate {
		m = rateSingle
	}
	return f.cal[c].vga[d.Group][min(d.Att, 2)][d.Step].centre[m]
}
