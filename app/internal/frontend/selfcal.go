package frontend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"time"

	"github.com/labeth/open-sds800xhd/app/internal/platform"
	"github.com/labeth/open-sds800xhd/ota/fsguard"
)

// The user self-calibration of spec 10 §5: with the inputs open, the offset
// DAC is searched for the codes that put the trace 3 divisions above and
// below the centre; the two points give the channel's centre code and its
// V/div, for the full-scale table and for every VGA step and DAC attenuator.

// Constants of spec 10 §5.1, in 16-bit-scaled sample codes.
const (
	calCenter = 32768
	calKDiv   = 7680
	calDelta  = 3 * calKDiv
	calTol    = 3072
	calAgree  = 512

	dacLo = 0x4800 // centre-search bracket
	dacHi = 0xA800

	waitUnit = 20 * time.Millisecond // the "wait w" unit
	probeW   = 3                     // the wait after every probe code
)

// selfCalK is the self-calibration's own offset-DAC slope per attenuator for
// the X100 group, used when no factory DAC file was applied (spec 10 §4.3);
// X10 is ten times and X1 a hundred times K.
var selfCalK = [4]float32{17.3, 34.6, 69.2, 173}

// ErrMeanEngine is a mean engine that never reported a result.
var ErrMeanEngine = errors.New("the PL mean engine did not report a result (ACQ 0x1B1 bit 0)")

// errOverflow is a mean-engine measurement with the overflow flag set.
var errOverflow = errors.New("mean engine overflow")

// prober is what the centre searches drive: the offset DAC of a channel,
// the settling wait and one mean measurement (spec 10 §5.2).
type prober interface {
	SetDAC(c, code int)
	Wait(w int)
	Mean(c int) (float64, error)
}

// calPoint is one probe: a DAC code and the mean it gave.
type calPoint struct {
	dac int
	m   float64
}

// centreResult is the outcome of a centre search for one channel.
type centreResult struct {
	p1, p2 calPoint // the +3 and −3 division probes
	c0     int      // the interpolated code set before the final measurement
	centre int32
	vdiv   float32 // volts per division at the BNC
	failed string  // why the channel's result is rejected (spec 10 §5.5)
}

func clampDAC(v int) int { return max(0, min(v, 0xFFFF)) }

// bisect is the bisection of spec 10 §5.4 steps 1 and 2 on the channels chs
// in parallel: each step sets code = (lo + hi) >> 1, waits, measures and
// moves the bracket until |e| ≤ TOL or hi − lo ≤ 1, e = trunc(m − target).
// A higher DAC code gives a lower sample code. A channel whose search ends
// at a bracket limit without reaching the tolerance fails.
func bisect(ctx context.Context, p prober, chs []int, target float64, lo0 [4]int, out *[4]centreResult, res *[4]calPoint) error {
	var lo, hi [4]int
	var done [4]bool
	active := 0
	for _, c := range chs {
		lo[c], hi[c] = lo0[c], dacHi
		if out[c].failed != "" {
			done[c] = true
			continue
		}
		active++
	}
	var code [4]int
	for active > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, c := range chs {
			if !done[c] {
				code[c] = (lo[c] + hi[c]) >> 1
				p.SetDAC(c, code[c])
			}
		}
		p.Wait(probeW)
		for _, c := range chs {
			if done[c] {
				continue
			}
			m, err := p.Mean(c)
			if err != nil {
				if !errors.Is(err, errOverflow) {
					return err
				}
				out[c].failed = fmt.Sprintf("C%d: %v at DAC %#x", c+1, err, code[c])
				done[c] = true
				active--
				continue
			}
			e := math.Trunc(m - target)
			if math.Abs(e) <= calTol || hi[c]-lo[c] <= 1 {
				res[c] = calPoint{code[c], m}
				if math.Abs(e) > calTol && (code[c]-dacLo <= 1 || dacHi-code[c] <= 1) {
					out[c].failed = fmt.Sprintf("C%d: the centre search ended at the DAC bracket limit %#x", c+1, code[c])
				}
				done[c] = true
				active--
				continue
			}
			if e > calTol {
				lo[c] = code[c]
			} else {
				hi[c] = code[c]
			}
		}
	}
	return nil
}

// fullSearch is the centre search of spec 10 §5.4 steps 1–3 on the
// channels chs in parallel; slope[c] is the offset-DAC slope (codes per
// volt) of the channel's attenuator and group.
func fullSearch(ctx context.Context, p prober, chs []int, slope [4]float32, out *[4]centreResult) error {
	var p1, p2 [4]calPoint
	var lo [4]int
	for _, c := range chs {
		lo[c] = dacLo
	}
	if err := bisect(ctx, p, chs, calCenter+calDelta, lo, out, &p1); err != nil {
		return err
	}
	for _, c := range chs {
		lo[c] = p1[c].dac
	}
	if err := bisect(ctx, p, chs, calCenter-calDelta, lo, out, &p2); err != nil {
		return err
	}
	var live []int
	for _, c := range chs {
		if out[c].failed != "" {
			continue
		}
		if math.Abs(p1[c].m-calCenter) < 1e-4 {
			p1[c].m = calCenter + calDelta
		}
		if math.Abs(p2[c].m-calCenter) < 1e-4 {
			p2[c].m = calCenter - calDelta
		}
		if p1[c].m == p2[c].m {
			out[c].failed = fmt.Sprintf("C%d: both probes read %.0f", c+1, p1[c].m)
			continue
		}
		out[c].p1, out[c].p2 = p1[c], p2[c]
		out[c].c0 = clampDAC(int(math.Round(interpolate(p1[c], p2[c]))))
		p.SetDAC(c, out[c].c0)
		live = append(live, c)
	}
	if len(live) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.Wait(probeW)
	for _, c := range live {
		m, err := p.Mean(c)
		if err != nil {
			if !errors.Is(err, errOverflow) {
				return err
			}
			out[c].failed = fmt.Sprintf("C%d: %v at the centre code", c+1, err)
			continue
		}
		r := &out[c]
		r.centre, r.vdiv = fullResult(r.c0, m, r.p1, r.p2, slope[c])
	}
	return nil
}

// interpolate is the DAC code for the centre on the line through P1 and P2
// (spec 10 §5.4 step 3).
func interpolate(p1, p2 calPoint) float64 {
	return float64(p1.dac) + (calCenter-p1.m)*float64(p2.dac-p1.dac)/(p2.m-p1.m)
}

// fullResult is the centre code and V/div of spec 10 §5.4 step 3 from the
// code dac set last, its mean m and the two probes.
func fullResult(dac int, m float64, p1, p2 calPoint, slope float32) (int32, float32) {
	centre := int32(math.Round(float64(dac) - (m-calCenter)*float64(p1.dac-p2.dac)/(p1.m-p2.m)))
	vdiv := float32(-(float64(p2.dac-p1.dac) / (p2.m - p1.m)) * calKDiv / float64(slope))
	return centre, vdiv
}

// quickProbes are the two probe codes of the quick centre search (spec 10
// §5.4 step 4) for full-scale ratio r from the last full search: float32
// arithmetic, truncated toward zero.
func quickProbes(r float32, c0 int, p1, p2 calPoint) (plus, minus int) {
	f0 := float32(c0)
	plus = int(f0 + r*float32(p1.dac-c0)*calDelta/float32(p1.m-calCenter))
	minus = int(f0 - r*float32(p2.dac-c0)*calDelta/float32(p2.m-calCenter))
	return plus, minus
}

// quickCode is the code x of the quick search for the mean m₊ at c₊ and m₋
// at c₋ (spec 10 §5.4 step 4); ok is false when the two means are equal.
func quickCode(cp, cm int, mp, mm float64) (int, bool) {
	fp, fm := float32(mp), float32(mm)
	if fm == fp {
		return 0, false
	}
	x := ((fm*float32(cp) - fp*float32(cm)) + calCenter*float32(cm-cp)) / (fm - fp)
	return int(x + 0.5), true
}

// quickResult is the centre and V/div of the quick search from the code x,
// its mean m and the probes (c₊, m₊), (c₋, m₋).
func quickResult(x int, m float64, cp, cm int, mp, mm float64, slope float32) (int32, float32) {
	var k float32
	if math.Abs(mp-mm) >= 1e-4 {
		k = float32(cp-cm) / float32(mp-mm)
	}
	centre := int32(float32(x) - k*(float32(m)-calCenter) + 0.5)
	vdiv := -(float32(cm-cp) / float32(mm-mp)) * calKDiv / slope
	return centre, vdiv
}

// quickSearch measures the probes plus[c] and minus[c] and the code between
// them on the channels chs in parallel (spec 10 §5.4 steps 4 and 5).
func quickSearch(ctx context.Context, p prober, chs []int, plus, minus [4]int, slope [4]float32, out *[4]centreResult) error {
	var mp, mm, mx [4]float64
	var x [4]int
	measure := func(codes *[4]int, means *[4]float64) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, c := range chs {
			if out[c].failed == "" {
				codes[c] = clampDAC(codes[c])
				p.SetDAC(c, codes[c])
			}
		}
		p.Wait(probeW)
		for _, c := range chs {
			if out[c].failed != "" {
				continue
			}
			m, err := p.Mean(c)
			if err != nil {
				if !errors.Is(err, errOverflow) {
					return err
				}
				out[c].failed = fmt.Sprintf("C%d: %v at DAC %#x", c+1, err, codes[c])
				continue
			}
			means[c] = m
		}
		return nil
	}
	if err := measure(&plus, &mp); err != nil {
		return err
	}
	if err := measure(&minus, &mm); err != nil {
		return err
	}
	for _, c := range chs {
		if out[c].failed != "" {
			continue
		}
		v, ok := quickCode(plus[c], minus[c], mp[c], mm[c])
		if !ok {
			out[c].failed = fmt.Sprintf("C%d: both quick probes read %.0f", c+1, mp[c])
			continue
		}
		x[c] = v
	}
	if err := measure(&x, &mx); err != nil {
		return err
	}
	for _, c := range chs {
		if out[c].failed == "" {
			out[c].centre, out[c].vdiv = quickResult(x[c], mx[c], plus[c], minus[c], mp[c], mm[c], slope[c])
		}
	}
	return nil
}

// SelfCalEnv is what the self-calibration needs from the acquisition owner.
type SelfCalEnv struct {
	// Channels switches the acquisition to the enabled-channel set mask
	// (route, ADC mode and the channel setups at 50 mV/div) and reports
	// whether the channels then run at the full rate, 2 GSa/s.
	Channels func(mask int) (fullRate bool, err error)
	Progress func(float64) // spec 10 §5.3 fractions; may be nil
	Dir      string        // where the user self-cal file is written (UserCalDir)
}

// SelfCalResult is the outcome of a self-calibration.
type SelfCalResult struct {
	Failed []string // one reason per failed channel; their tables were kept
	File   string   // the file written
}

// selfCalRun is one self-calibration in progress.
type selfCalRun struct {
	f       *FrontEnd
	env     SelfCalEnv
	n       int
	all     []int
	window  int
	slope   [4][3]float32 // [d][g]
	fs      [4][][3]float64
	vga     [4][3][3][21]vgaEntry
	kept    [4][3][21][2]calPoint // step 4's P1/P2 per (c, d', s)
	failed  [4]string
	fsCodes []int
	fsRatio []float32
	defFS   int
	done    float64 // the progress reported last
}

// SetDAC writes channel c's offset DAC (spec 06 §5).
func (r *selfCalRun) SetDAC(c, code int) {
	f := r.f
	f.acq.Set(dacReg[c], uint16(code)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	f.dac[c], f.dacSet[c] = code, true
}

// Wait is the self-calibration's wait of w units of 20 ms.
func (r *selfCalRun) Wait(w int) {
	if r.f.sleep != nil {
		r.f.sleep(time.Duration(w) * waitUnit)
		return
	}
	time.Sleep(time.Duration(w) * waitUnit)
}

// Mean is one channel's measurement with the PL mean engine (spec 10 §5.2):
// repeated, up to four times, until two consecutive means agree within 512.
func (r *selfCalRun) Mean(c int) (float64, error) {
	prev := math.NaN()
	var m float64
	for range 4 {
		var err error
		if m, err = r.f.meanOnce(c, r.window); err != nil {
			return 0, err
		}
		if math.Abs(m-prev) <= calAgree {
			break
		}
		prev = m
	}
	return m, nil
}

// meanOnce is one start of the PL mean engine for channel c over window n.
func (f *FrontEnd) meanOnce(c, n int) (float64, error) {
	a := f.acq
	a.Set(0x1AF, uint16(n)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	a.Set(0x1AE, 0, 1)
	if !a.Poll(0x1B1, 1, true, n+1, time.Millisecond) {
		return 0, ErrMeanEngine
	}
	a.Set(0x1B0, uint16(c)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	if a.HW(0x1B1)&(1<<(12+c)) != 0 {
		return 0, errOverflow
	}
	sum := uint64(a.HW(0x1B2)) | uint64(a.HW(0x1B3))<<16 | uint64(a.HW(0x1B4))<<32
	count := uint64(a.HW(0x1B5)) | uint64(a.HW(0x1B6))<<16
	if count == 0 {
		return 0, ErrMeanEngine
	}
	code := int64(math.Round(float64(sum)/float64(count))) & 0xFFF
	return float64(code << 4), nil
}

// setup is the self-calibration's analog setting of channel c (spec 10
// §5.3 step 2): 50 mV/div relays, full bandwidth, DAC attenuator d, VGA step
// s.
func (r *selfCalRun) setup(c, d, s int) {
	f := r.f
	f.relayBits(c, Channel{On: true, VoltsDiv: 0.05, Probe: 1, Coupling: DC})
	f.setNibble(c, dacNibble[d])
	f.sendRelay()
	bits := uint16(s) //nolint:gosec // G115: a ladder step is 0 … 20
	if s > 10 {
		bits = 0x10 + uint16(s-11) //nolint:gosec // G115: a ladder step is 0 … 20
	}
	f.vga[c] = 0x0400 | f.vgaFilter(Channel{})<<6 | bits
	f.sendVGA(c)
}

// channels switches to the channel set mask and applies the analog setting
// to its channels.
func (r *selfCalRun) channels(chs []int, d, s int) (bool, error) {
	mask := 0
	for _, c := range chs {
		mask |= 1 << c
	}
	full, err := r.env.Channels(mask)
	if err != nil {
		return false, err
	}
	for _, c := range chs {
		r.setup(c, d, s)
	}
	return full, nil
}

// progress reports x, never going back (the 0.99 before saving follows
// step 5's 0.996).
func (r *selfCalRun) progress(x float64) {
	x = max(x, r.done)
	r.done = x
	if r.env.Progress != nil {
		r.env.Progress(x)
	}
}

// live is the channels not failed so far.
func (r *selfCalRun) live(chs []int) []int {
	var out []int
	for _, c := range chs {
		if r.failed[c] == "" {
			out = append(out, c)
		}
	}
	return out
}

// note records the failures of a search.
func (r *selfCalRun) note(res *[4]centreResult) {
	for c := range r.n {
		if res[c].failed != "" && r.failed[c] == "" {
			r.failed[c] = res[c].failed
		}
	}
}

// fresh returns search results with the failed channels marked.
func (r *selfCalRun) fresh() [4]centreResult {
	var res [4]centreResult
	for c := range r.n {
		res[c].failed = r.failed[c]
	}
	return res
}

func (r *selfCalRun) setFS(chs []int, code int, full bool) {
	for _, c := range chs {
		r.f.ADC.SetFullScale(c, code, full)
	}
}

// slopes is every channel's X1 slope of attenuator d.
func (r *selfCalRun) slopes(d int) [4]float32 {
	var s [4]float32
	for c := range r.n {
		s[c] = r.slopeOf(c, d, groupX1)
	}
	return s
}

// slopeOf is the slope of channel c, attenuator d, group g used for the
// V/div arithmetic.
func (r *selfCalRun) slopeOf(c, d, g int) float32 {
	if r.f.calStatus.Factory {
		return r.f.cal[c].slope[d][g]
	}
	return r.slope[d][g]
}

// fullScale is spec 10 §5.3 step 3 for the channels chs measured together,
// into column col of the full-scale table.
func (r *selfCalRun) fullScale(ctx context.Context, chs []int, col int, lo, hi float64) error {
	full, err := r.channels(chs, att1X, 10)
	if err != nil {
		return err
	}
	r.setFS(chs, r.defFS, full)
	slope := r.slopes(att1X)
	ref := r.fresh()
	if err := fullSearch(ctx, r, r.live(chs), slope, &ref); err != nil {
		return err
	}
	r.note(&ref)
	for j := range r.fsCodes {
		chs := r.live(chs)
		r.setFS(chs, r.fsCodes[j], full)
		var plus, minus [4]int
		for _, c := range chs {
			plus[c], minus[c] = quickProbes(r.fsRatio[j], ref[c].c0, ref[c].p1, ref[c].p2)
		}
		res := r.fresh()
		if err := quickSearch(ctx, r, chs, plus, minus, slope, &res); err != nil {
			return err
		}
		r.note(&res)
		for _, c := range chs {
			if res[c].failed == "" && ref[c].vdiv != 0 {
				r.fs[c][j][col] = float64(res[c].vdiv / ref[c].vdiv)
			}
		}
		r.progress(lo + (hi-lo)*float64(j+1)/float64(len(r.fsCodes)))
	}
	r.setFS(chs, r.defFS, full)
	return nil
}

// store puts a VGA result of channel c into the X1 entry of (d, s) for rate
// mode m and derives X10 and X100 by the slope ratio (spec 10 §5.3 step 4).
func (r *selfCalRun) store(c, d, s, m int, res centreResult) {
	gX1 := res.vdiv * 1000
	for g := range 3 {
		gain := gX1
		if g != groupX1 {
			gain = gX1 * r.slopeOf(c, d, groupX1) / r.slopeOf(c, d, g)
		}
		e := &r.vga[c][g][d][s]
		e.gain[m], e.centre[m] = gain, res.centre
		if m == rateUnite {
			e.gain[1], e.centre[1] = gain, res.centre
		}
	}
}

// SelfCal runs the user self-calibration of spec 10 §5 on the first n
// channels, with the acquisition stopped and the inputs open, writes the
// user self-cal file (spec 10 §4) and applies the new tables. A channel
// whose search ends at a bracket limit or overflows keeps its previous
// tables and is reported failed (§5.5). The caller restores the
// acquisition afterwards; every channel setup must be sent again.
func (f *FrontEnd) SelfCal(ctx context.Context, n int, env SelfCalEnv) (SelfCalResult, error) {
	r := &selfCalRun{f: f, env: env, n: n}
	for c := range n {
		r.all = append(r.all, c)
	}
	for d, k := range selfCalK {
		r.slope[d] = [3]float32{k, 10 * k, 100 * k}
	}
	geo := fsFor(f.part)
	step := (geo.codeMax - geo.codeMin) / (geo.n - 1)
	rstep := (1.25 - geo.f0) / float32(geo.n-1)
	for j := range geo.n {
		r.fsCodes = append(r.fsCodes, geo.codeMin+j*step)
		r.fsRatio = append(r.fsRatio, fma32(float32(j), rstep, geo.f0))
	}
	r.defFS = 0x4000
	if f.part == platform.ADC12DL1500 {
		r.defFS = 0xA000
	}
	for c := range n {
		r.fs[c] = make([][3]float64, geo.n)
	}
	f.selfCal = true
	defer func() { f.selfCal = false }()

	// Step 3: full scale, all channels together (unite), then each alone
	// (single).
	r.window = 20
	if err := r.fullScale(ctx, r.all, 1, 0, 0.03); err != nil {
		return SelfCalResult{}, err
	}
	r.window = 5
	for i, c := range r.all {
		lo := 0.03 + 0.119*float64(i)/float64(n)
		if err := r.fullScale(ctx, []int{c}, 0, lo, lo+0.119/float64(n)); err != nil {
			return SelfCalResult{}, err
		}
	}
	for c := range n {
		for j := range r.fs[c] {
			r.fs[c][j][2] = r.fs[c][j][1]
		}
	}

	// Step 4: the VGA tables at the unite rate, all channels together.
	r.window = 20
	full, err := r.channels(r.all, att1X, 10)
	if err != nil {
		return SelfCalResult{}, err
	}
	for d := range 3 {
		slope := r.slopes(d)
		for s := range 21 {
			chs := r.live(r.all)
			for _, c := range chs {
				r.setup(c, d, s)
			}
			r.setFS(chs, r.defFS, full)
			res := r.fresh()
			if err := fullSearch(ctx, r, chs, slope, &res); err != nil {
				return SelfCalResult{}, err
			}
			r.note(&res)
			for _, c := range chs {
				if res[c].failed == "" {
					r.store(c, d, s, rateUnite, res[c])
					r.kept[c][d][s] = [2]calPoint{res[c].p1, res[c].p2}
				}
			}
			r.progress(0.314 + 0.491*float64(d*21+s+1)/63)
		}
	}

	// Step 5: the VGA tables at the single rate, each channel alone, with
	// the probe codes step 4 found.
	r.window = 5
	for i, c := range r.all {
		if r.failed[c] != "" {
			continue
		}
		full, err := r.channels([]int{c}, att1X, 10)
		if err != nil {
			return SelfCalResult{}, err
		}
		r.setFS([]int{c}, r.defFS, full)
		for d := range 3 {
			slope := r.slopes(d)
			for s := range 21 {
				if r.failed[c] != "" {
					break
				}
				r.setup(c, d, s)
				var plus, minus [4]int
				plus[c], minus[c] = r.kept[c][d][s][0].dac, r.kept[c][d][s][1].dac
				res := r.fresh()
				if err := quickSearch(ctx, r, []int{c}, plus, minus, slope, &res); err != nil {
					return SelfCalResult{}, err
				}
				r.note(&res)
				if res[c].failed == "" {
					r.store(c, d, s, rateSingle, res[c])
				}
			}
		}
		r.progress(0.805 + 0.191*float64(i+1)/float64(n))
	}

	// Step 6: apply, then write the file.
	r.progress(0.99)
	var out SelfCalResult
	cals := make([]UserCal, n)
	for c := range n {
		if r.failed[c] == "" {
			f.cal[c].fs = r.fs[c]
			f.cal[c].vga = r.vga[c]
		} else {
			out.Failed = append(out.Failed, r.failed[c])
		}
		cals[c] = UserCal{FS: f.cal[c].fs, VGA: f.cal[c].vga}
	}
	dir := env.Dir
	if dir == "" {
		dir = UserCalDir
	}
	out.File = filepath.Join(dir, UserCalFile)
	if err := writeUserCalFile(out.File, cals); err != nil {
		return out, err
	}
	if len(out.Failed) < n {
		f.calStatus.User, f.calStatus.SelfCal = true, true
	}
	r.progress(1)
	return out, nil
}

// writeUserCalFile writes the user self-cal file power-fail safe through
// the write guard: a temporary name, fsync, rename, fsync of the directory
// (spec 10 §1).
// TRLC-LINKS: REQ-HD-016
func writeUserCalFile(path string, cals []UserCal) error {
	var b bytes.Buffer
	if err := WriteUserCal(&b, cals); err != nil {
		return err
	}
	if err := fsguard.MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	return fsguard.WriteFile(path, b.Bytes(), 0o644)
}

// ForgetDAC makes the next channel setup write every offset DAC.
func (f *FrontEnd) ForgetDAC() { f.dacSet = [4]bool{} }
