package scpi

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-dso-core/measure"
	"github.com/labeth/open-dso-core/sample"
	core "github.com/labeth/open-dso-core/scpi"
)

// fake is an instrument holding its state and one frame.
type fake struct {
	s      core.State
	f      *sample.Frame
	run    core.RunState
	forced int
	cal    string
}

func (x *fake) State() core.State { return x.s.Clone() }
func (x *fake) Change(fn func(*core.State) error) error {
	s := x.s.Clone()
	if err := fn(&s); err != nil {
		return err
	}
	x.s = s
	return nil
}
func (x *fake) WithFrame(fn func(*sample.Frame)) { fn(x.f) }
func (x *fake) Acquisition() core.RunState {
	if !x.s.Running {
		return core.Stopped
	}
	return x.run
}
func (x *fake) Force() error { x.forced++; return nil }

// square is a 12-bit left-aligned ±1 V square at 1 V/div (7680 words a
// division about 0x8000), period 20 samples.
func square(n int) []uint16 {
	w := make([]uint16, n)
	for i := range w {
		w[i] = 0x8000 - 7680
		if i%20 < 10 {
			w[i] = 0x8000 + 7680
		}
	}
	return w
}

func measureQ8(t sample.Trace, sampleS float64) *measure.Result {
	vpc := t.Scale.PerCode * 256
	return measure.ComputeQ8(t.Codes16, vpc, (t.Scale.Zero-0x8000)*t.Scale.PerCode, sampleS)
}

func newEngine(t *testing.T) (*core.Engine, *fake) {
	t.Helper()
	x := &fake{s: Defaults(4), run: core.Triggered, cal: "DONE"}
	w := square(1000)
	x.f = &sample.Frame{SampleS: 1e-9, TriggerAt: 500, Traces: []sample.Trace{{
		Ch: 0, Format: sample.CodeFormat{Bits: 12, Storage: sample.U16, Shift: 4, Mid: 0x8000},
		Codes16: w, Valid: len(w), Scale: sample.Scale{PerCode: 1.0 / 7680, Zero: 0x8000}, CodesPerDiv: 7680,
	}}}
	d, err := Dialect(Config{
		Identity: Identity{Manufacturer: "Maker Co", Model: "SDS824X HD", Serial: "SN0001", Version: ".8.0.0.1.0.0"},
		Channels: 4, Measure: measureQ8,
		SelfCal:      func() error { x.cal = "DOING"; return nil },
		SelfCalState: func() string { return x.cal },
	})
	if err != nil {
		t.Fatal(err)
	}
	return core.New(d, x), x
}

func run(t *testing.T, e *core.Engine, cases [][2]string) {
	t.Helper()
	for _, c := range cases {
		if got := string(e.Exec([]byte(c[0]))); got != c[1] {
			t.Errorf("%q: got %q, want %q", c[0], got, c[1])
		}
	}
}

// The embedded tables are the spec's appendix, byte for byte.
func TestTablesAreTheSpecs(t *testing.T) {
	for _, n := range []string{"scpi-commands.tsv", "scpi-errors.tsv"} {
		spec, err := os.ReadFile("../../../specs/appendix/" + n)
		if err != nil {
			t.Fatal(err)
		}
		mine, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(spec, mine) {
			t.Errorf("%s differs from specs/appendix (go generate ./internal/scpi)", n)
		}
	}
	if errorTexts[-113] != "Undefined header" || errorTexts[-300] == "" || len(errorTexts) < 100 {
		t.Fatalf("error texts: %d, -113 %q", len(errorTexts), errorTexts[-113])
	}
}

// The parser, error and reply rules of spec 11 §6.2–§6.3 on this dialect.
func TestMessageRules(t *testing.T) {
	e, _ := newEngine(t)
	run(t, e, [][2]string{
		{"*IDN?", "Maker Co,SDS824X HD,SN0001,.8.0.0.1.0.0\n"},
		{":CHAN1:SCAL?;OFFS?", "1.00E+00;0.00E+00\n"},
		{"*IDN?;*CLS", "Maker Co,SDS824X HD,SN0001,.8.0.0.1.0.0"},
		{"*IDN?;*CLS;*OPC?", "Maker Co,SDS824X HD,SN0001,.8.0.0.1.0.01\n"},
		{"*OPC?;*ESE 0;*OPC?;*ESE?", "11;0\n"},
		{":TIM:SCAL?;:TIM:SCAL 1E-6;:TIM:SCAL?", "2.00E-071.00E-06\n"},
		{"FOO:BAR?", ""},
		{":SYST:ERR?", "-113,\"Undefined header;FOO:BAR?\"\n"},
		{":CHAN1:SCAL", ""},
		{":CHAN1:COUP XYZ", ""},
		{":CHAN1:SCAL abc;:CHAN1:SCAL?", "1.00E+00\n"},
		{":CHAN1:INV ON,OFF", ""},
		{":CHAN5:SCAL?", ""},
		{":WAV:SOUR C5", ""},
		{":SYST:COUN?", "5\n"},
		{":SYST:ERR?;ERR?;ERR?;ERR?;ERR?", `-200,"Execution error";-224,"Illegal parameter value";-108,"Parameter not allowed";-200,"Execution error";-224,"Illegal parameter value"` + "\n"},
		{"*CLS;*ESE 32;FOO;*STB?", "36\n"},
		{":FORM:DATA SING;:TIM:SCAL?;:FORM:DATA?", "1.000000E-06;CUSTOM,7\n"},
		{":FORM:DATA CUST,6;:TIM:SCAL?;:FORM:DATA CUST,3", "1.00000E-06"},
		{"*OPT?", "0,0,0\n"},
		{":SYST:SELFC?", "DONE\n"},
		{":SYST:SELFC;:SYST:SELFC?", "DOING\n"},
	})
}

// The legacy replies follow CHDR as spec 11 §6.3.2's table.
func TestLegacyReplies(t *testing.T) {
	e, _ := newEngine(t)
	e.Exec([]byte(":TIM:SCAL 1E-6"))
	run(t, e, [][2]string{
		{"C1:VDIV?", "C1:VDIV 1.00E+00V\n"},
		{"C1:OFST?", "C1:OFST 0.00E+00V\n"},
		{"C1:CPL?", "C1:CPL D1M\n"},
		{"C1:BWL?", "C1:BWL OFF\n"},
		{"C1:ATTN?", "C1:ATTN 1\n"},
		{"C2:TRA?", "C2:TRA OFF\n"},
		{"C1:TRLV?", "C1:TRLV 0.00E+00V\n"},
		{"TDIV?", "1.00E-06S\n"},
		{"TRMD?", "TRMD AUTO\n"},
		{"TRSE?", "TRSE EDGE,SR,C1,HT,OFF\n"},
		{"MSIZ?", "MSIZ 10M\n"},
		{"SARA?", "SARA 1.00E+09Sa/s\n"},
		{"INR?", "INR 0\n"},
		{"ALST?", "ALST STB,0,ESR,0,INR,0,DDR,0,CMR,0,EXR,0,URR,0\n"},
		{"CHDR?", "CHDR SHORT\n"},
		{"CHDR LONG", ""},
		{"C1:VDIV?", "C1:VOLT_DIV 1.00E+00V\n"},
		{"C1:CPL?", "C1:COUPLING D1M\n"},
		{"C2:TRA?", "C2:TRACE OFF\n"},
		{"TDIV?", "1.00E-06S\n"},
		{"TRSE?", "TRSE EDGE,SR,C1,HT,OFF\n"},
		{"MSIZ?", "MEMORY_SIZE 10M\n"},
		{"SARA?", "SAMPLE_RATE 1.00E+09Sa/s\n"},
		{"CHDR?", "COMM_HEADER LONG\n"},
		{"COMM_HEADER OFF", ""},
		{"C1:VDIV?", "1.00E+00\n"},
		{"C1:CPL?", "D1M\n"},
		{"C1:TRLV?", "0.00E+00\n"},
		{"TDIV?", "1.00E-06\n"},
		{"TRSE?", "EDGE,SR,C1,HT,OFF\n"},
		{"SARA?", "1.00E+09\n"},
		{"CHDR?", "OFF\n"},
		{"CHDR SHORT;C1:VDIV 500MV", ""},
		{"C1:VDIV?", "C1:VDIV 5.00E-01V\n"},
		{"C1:VOLT_DIV 2;:C1:VDIV?", "C1:VDIV 2.00E+00V\n"},
		{"TRMD STOP;TRMD?", "TRMD STOP\n"},
		{"ARM;TRMD?", "TRMD SINGLE\n"},
		{"C2:TRLV 1", ""},
		{":SYST:ERR?", "-221,\"Settings conflict\"\n"},
	})
}

// Settings reach the instrument state in volts and seconds and read back.
func TestSettings(t *testing.T) {
	e, x := newEngine(t)
	run(t, e, [][2]string{
		{":CHAN2:SWIT ON;:CHAN2:SWIT?", "ON\n"},
		{":CHAN2:COUP AC;COUP?", "AC\n"},
		{":CHAN2:BWL 20M;BWL?", "20M\n"},
		{":CHAN2:BWL 200M;BWL?", "20M\n"},
		{":CHAN2:IMP FIFTY;IMP?", "ONEMeg\n"},
		{":CHAN2:PROB VAL,10;PROB?;SCAL?", "1.00E+01;1.00E+01\n"},
		{":CHAN2:INV ON;INV?", "ON\n"},
		{":CHAN2:SKEW 1E-9;SKEW?", "1.00E-09\n"},
		{":CHAN2:SKEW 1", ""},
		{":TIM:DEL 1E-6;DEL?", "1.00E-06\n"},
		{":ACQ:MDEP 1M;MDEP?", "1M\n"},
		{":ACQ:TYPE PEAK;TYPE?", "PEAK\n"},
		{":ACQ:TYPE AVER,16;TYPE?", "PEAK\n"},
		{":ACQ:TYPE ERES,2.0;TYPE?", "PEAK\n"},
		{":ACQ:TYPE AVER", ""},
		{":ACQ:SRAT?;POIN?", "1.00E+09;1.00E+03\n"},
		{":TRIG:MODE NORM;MODE?", "NORMal\n"},
		{":TRIG:MODE FTRIG;MODE?", "NORMal\n"},
		{":TRIG:EDGE:SLOP FALL;SLOP?", "FALLing\n"},
		{":TRIG:EDGE:COUP HFREJ;COUP?", "HFREJect\n"},
		{":TRIG:EDGE:NREJ ON;NREJ?", "ON\n"},
		{":TRIG:EDGE:LEV 10;LEV?", "4.10E+00\n"},
		{":TRIG:EDGE:SOUR C3;SOUR?", "C1\n"},
		{":TRIG:EDGE:SOUR EX;SOUR?", "C1\n"},
		{":TRIG:EDGE:SOUR C2;SOUR?", "C2\n"},
		{":TRIG:EDGE:SOUR LINE;SOUR?", "LINE\n"},
		{":TRIG:EDGE:HOLD TIME;HOLD?;HLDT?", "TIME;8.00E-08\n"},
		{":TRIG:EDGE:HLDT 1E-6;HLDT?", "1.00E-06\n"},
		{":TRIG:TYPE?", "EDGE\n"},
		{":TRIG:TYPE PULSE", ""},
		{":TRIG:STAT?", "Trig'd\n"},
		{":TRIG:STOP;:TRIG:STAT?", "Stop\n"},
		{":SYST:ERR?;ERR?;ERR?", `-222,"Data out of range";-200,"Execution error";-221,"Settings conflict"` + "\n"},
	})
	want := hal.Channel{On: true, Invert: true, VoltsDiv: 10, Probe: 10, Coupling: hal.AC, BWLimit: 20e6, Impedance: 1e6, SkewS: 1e-9}
	if got := x.s.Channels[1]; !reflect.DeepEqual(got, want) {
		t.Errorf("C2 = %+v, want %+v", got, want)
	}
	tr := x.s.Trigger
	if x.forced != 1 || x.s.DelayS != -1e-6 || x.s.Depth != 1_000_000 || x.s.Mode != hal.PeakDetect ||
		tr.Slope != hal.Falling || tr.Coupling != hal.TrigHFReject || !tr.NoiseReject || tr.HoldoffS != 1e-6 ||
		tr.Source.Kind != hal.ACLine || x.s.Sweep != hal.NormalSweep || x.s.Running {
		t.Errorf("state %+v, forced %d", x.s, x.forced)
	}
	run(t, e, [][2]string{{"*RST;:TRIG:MODE?;:CHAN2:SWIT?", "AUTO;OFF\n"}})
}

// Measurements of the last frame's source channel.
func TestMeasurements(t *testing.T) {
	e, _ := newEngine(t)
	run(t, e, [][2]string{
		{":MEAS:SIMP:SOUR?", "C1\n"},
		{":MEAS:SIMP:VAL? PKPK;VAL? FREQ;VAL? DUTY", "2.00E+00;5.00E+07;5.00E+01\n"},
		{":MEAS:SIMP:VAL? LEVELX", ""},
		{":MEAS:SIMP:SOUR C2;:MEAS:SIMP:VAL? PKPK", ""},
		{":SYST:ERR?;ERR?", `-221,"Settings conflict";-200,"Execution error"` + "\n"},
	})
}

// The waveform transfer of spec 11 §7: descriptor, blocks, encodings.
func TestWaveform(t *testing.T) {
	e, x := newEngine(t)
	x.s.TimeDivS = 100e-9
	pre := e.Exec([]byte(":WAV:STAR 3;INT 2;:WAV:PRE?"))
	if !bytes.HasPrefix(pre, []byte("#9000000346WAVEDESC")) || len(pre) != 11+346+1 || pre[len(pre)-1] != '\n' {
		t.Fatalf("preamble %q", pre[:30])
	}
	desc := pre[11:]
	le := binary.LittleEndian
	f32 := func(o int) float32 { return math.Float32frombits(le.Uint32(desc[o:])) }
	if string(desc[16:23]) != "WAVEACE" || le.Uint32(desc[116:]) != 1000 || le.Uint32(desc[132:]) != 3 ||
		le.Uint32(desc[136:]) != 2 || f32(156) != 1 || f32(164) != 7680 || le.Uint16(desc[172:]) != 16 ||
		le.Uint16(desc[324:]) != 8 || string(desc[76:85]) != "Maker SDS" || le.Uint32(desc[92:]) != 0xCDAB {
		t.Fatalf("descriptor fields wrong: % x", desc[:200])
	}
	if ho := math.Float64frombits(le.Uint64(desc[180:])); math.Abs(ho) > 1e-15 {
		t.Fatalf("HORIZ_OFFSET %g with the trigger at the centre", ho)
	}
	data := e.Exec([]byte(":WAV:STAR 0;INT 1;POIN 4;:WAV:DATA?"))
	if string(data) != "#14\x1e\x1e\x1e\x1e\n\n" {
		t.Fatalf("BYTE data %q", data)
	}
	data = e.Exec([]byte(":WAV:WIDT WORD;:WAV:DATA?"))
	if string(data) != "#18\x00\x1e\x00\x1e\x00\x1e\x00\x1e\n\n" {
		t.Fatalf("WORD data %q", data)
	}
	data = e.Exec([]byte(":WAV:BYT MSB;:WAV:STAR 10;:WAV:DATA?"))
	if string(data) != "#18\xe2\x00\xe2\x00\xe2\x00\xe2\x00\n\n" {
		t.Fatalf("WORD MSB data %q", data)
	}
	if got := string(e.Exec([]byte(":WAV:STAR 5000;:WAV:DATA?"))); got != "fp > current memory depth!\n\n" {
		t.Fatalf("start past the record %q", got)
	}
	run(t, e, [][2]string{
		{":WAV:STAR?;INT?;POIN?;WIDT?;BYT?;SOUR?;MAXP?", "5000;1;4;WORD;MSB;C1;5000000\n"},
		{":WAV:SOUR C2;:WAV:DATA?", "C2:WF DAT2,#9000000000\n\n"},
		{"WFSU SP,1,NP,2,FP,0;WFSU?", "WFSU SP,1,NP,2,FP,0\n"},
		{"C1:WF? DAT2", "C1:WF DAT2,#9000000002\x1e\x1e\n\n"},
	})
	if got := e.Exec([]byte("C1:WF? DESC")); !bytes.HasPrefix(got, []byte("C1:WF DESC,#9000000346WAVEDESC")) || len(got) != 369 {
		t.Fatalf("legacy DESC %d %q", len(got), got[:30])
	}
}

// Every header the dialect answers is in the inventory or spec 11's legacy
// table, and its coverage is reported.
func TestCoverage(t *testing.T) {
	d, err := Dialect(Config{Channels: 4})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range d.Commands {
		if !strings.HasPrefix(c.Pattern, "STATus") && !strings.HasPrefix(c.Pattern, "SYSTem") {
			n++
		}
	}
	t.Logf("%d headers bound to the instrument (plus the SYSTem/STATus subset)", n)
	if n < 100 {
		t.Fatalf("only %d headers", n)
	}
}

// Fuzzed messages never panic the dialect.
func FuzzDialect(f *testing.F) {
	for _, s := range []string{":WAV:DATA?", "C1:WF? DAT2", ":TRIG:EDGE:SOUR C9", "WFSU SP,,", ":CHAN0:SCAL?", "TRSE", ":FORM:DATA CUST,-1"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		e, _ := newEngine(t)
		e.Exec([]byte(s))
	})
}
