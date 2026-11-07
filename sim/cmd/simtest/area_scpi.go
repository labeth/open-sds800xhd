package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/labeth/open-sds800xhd/sim/harness"
)

// The SCPI area runs the app (not the board tests): its host interface over
// the raw socket, with the replies checked against what the injected
// signals and the commanded settings imply.
var scpiChecks = []struct{ line, reply string }{
	{"*IDN?", `^Siglent Technologies,SDS8\d4X HD,[0-9A-F]{16},`},
	{":CHAN1:SCAL?;:TIM:SCAL?", `^1\.00E\+00;2\.00E-07$`},
	{":MEAS:SIMP:VAL? PKPK;:MEAS:SIMP:VAL? FREQ", `^(1\.9\d|2\.0\d)E\+00;5\.00E\+06$`},
	{":TIM:SCAL?;:MEAS:SIMP:VAL? FREQ;:MEAS:SIMP:VAL? DUTY", `^2\.00E-06;5\.00E\+06;(4\.9\d|5\.0\d)E\+01$`},
	{":MEAS:SIMP:VAL? MAX;:MEAS:SIMP:VAL? MIN;:MEAS:SIMP:VAL? PKPK", `^(9\.\d\dE-01|1\.0\dE\+00);-(9\.\d\dE-01|1\.0\dE\+00);(1\.9\d|2\.0\d)E\+00$`},
	{":TRIG:STAT?", `^Stop$`},
	{"FOO;:SYST:ERR?", `^-113,"Undefined header`},
	// The legacy forms of spec 11 §6.3.2, read back after settings.
	{"C1:VDIV?;:TDIV?", `^C1:VDIV 1\.00E\+00V;2\.00E-06S$`},
	{":TRIG:EDGE:SOUR?;SLOP?;LEV?;COUP?", `^C1;RISing;0\.00E\+00;DC$`},
	{":ACQ:SRAT?;MDEP?;TYPE?", `^\d\.\d\dE\+\d\d;10M;NORMal$`},
	{":WAV:SOUR?;WIDT?;BYT?;MAXP?", `^C1;WORD;LSB;5000000$`},
	{":TRIG:STAT?;:TRIG:MODE?", `^(Trig'd|Auto|Arm);AUTO$`},
	{"*STB?;*ESR?;:SYST:ERR?", `^36;32;-113,"Undefined header;BAR"$`},
	{"C1:CPL?", `^D1M$`},
	{":TIM:SCAL?", `^2\.000000E-06$`},
	{":TRIG:MODE?", `^AUTO$`},
	{":CHAN2:SWIT?;COUP?;:CHAN2:BWL?", `^ON;AC;20M$`},
	{":SYST:SELFC?", `^DONE$`},
}

// scpiScenario is the run's scenario: settings, then the queries above.
var scpiScenario = []string{
	"@15 scpi *IDN?", "@16 scpi :CHAN1:SCAL?;:TIM:SCAL?",
	"@17 scpi :MEAS:SIMP:VAL? PKPK;:MEAS:SIMP:VAL? FREQ", "@18 scpi :TIM:SCAL 2E-6",
	"@21 scpi :TIM:SCAL?;:MEAS:SIMP:VAL? FREQ;:MEAS:SIMP:VAL? DUTY",
	"@22 scpi :CHAN1:SCAL 0.5;:CHAN1:OFFS 0.5",
	"@25 scpi :MEAS:SIMP:VAL? MAX;:MEAS:SIMP:VAL? MIN;:MEAS:SIMP:VAL? PKPK",
	"@30 scpi :TRIG:MODE SINGLE", "@33 scpi :TRIG:STAT?", "@34 scpi FOO;:SYST:ERR?",
	"@35 scpi :TRIG:MODE AUTO;:CHAN1:SCAL 1;:CHAN1:OFFS 0;:TIM:SCAL 2E-6",
	"@37 scpi C1:VDIV?;:TDIV?", "@37 scpi :TRIG:EDGE:SOUR?;SLOP?;LEV?;COUP?",
	"@38 scpi :ACQ:SRAT?;MDEP?;TYPE?",
	"@39 scpi :WAV:SOUR C1;:WAV:WIDT WORD;:WAV:STAR 0;:WAV:POIN 0",
	"@40 scpi :WAV:SOUR?;WIDT?;BYT?;MAXP?", "@40 scpi :WAV:PRE?", "@41 scpi :WAV:DATA?", "@42 scpi :TRIG:STAT?;:TRIG:MODE?",
	"@43 scpi *CLS;*ESE 32;BAR", "@43 scpi *STB?;*ESR?;:SYST:ERR?",
	"@44 scpi CHDR OFF", "@44 scpi C1:CPL?", "@45 scpi CHDR SHORT;:FORM:DATA SING", "@45 scpi :TIM:SCAL?",
	"@46 scpi :FORM:DATA CUST,3;:TRIG:MODE FTRIG", "@47 scpi :TRIG:MODE?",
	"@48 scpi :CHAN2:SWIT ON;:CHAN2:COUP AC;:CHAN2:BWL 20M", "@50 scpi :CHAN2:SWIT?;COUP?;:CHAN2:BWL?", "@51 scpi :SYST:SELFC?",
}

func init() {
	register(area{
		name: "scpi", title: "SCPI: commons, status, tree and legacy forms, measurements, waveform transfer",
		runs: func(e *env) []harness.Spec {
			return []harness.Spec{{
				Name: e.prefix + "scpi", Secs: 60, Ports: true, SimdArgs: signals,
				App: e.app, Hook: e.hook, GuestBoot: e.guestBoot,
				Scenario: scpiScenario,
			}}
		},
		check: func(_ *env, runs []*harness.Run) []harness.Result {
			var out []harness.Result
			replies := harness.SCPIReplies(runs[0])
			for _, c := range scpiChecks {
				got, ok := replies[c.line]
				switch {
				case !ok:
					out = append(out, harness.Fail("scpi", c.line, "no reply"))
				case regexp.MustCompile(c.reply).MatchString(strings.TrimSpace(got)):
					out = append(out, harness.Pass("scpi", c.line, got))
				default:
					out = append(out, harness.Fail("scpi", c.line, "reply %q, want %s", got, c.reply))
				}
			}
			return append(out, checkWaveform(replies)...)
		},
	})
}

var reSaved = regexp.MustCompile(`saved (\S+)`)

// block reads the definite block a scenario line's reply was saved with.
func block(reply string) ([]byte, error) {
	m := reSaved.FindStringSubmatch(reply)
	if m == nil {
		return nil, fmt.Errorf("no saved block in %q", reply)
	}
	b, err := os.ReadFile(m[1])
	if err != nil {
		return nil, err
	}
	i := bytes.IndexByte(b, '#')
	if i < 0 || i+2 > len(b) {
		return nil, errors.New("no block header")
	}
	nd := int(b[i+1] - '0')
	if i+2+nd > len(b) {
		return nil, errors.New("short block header")
	}
	n, err := strconv.Atoi(string(b[i+2 : i+2+nd]))
	if err != nil || i+2+nd+n > len(b) {
		return nil, fmt.Errorf("block length %d of %d bytes: %w", n, len(b), err)
	}
	return b[i+2+nd : i+2+nd+n], nil
}

// checkWaveform decodes :WAV:PRE? and :WAV:DATA? (spec 11 §7.2–§7.4): the
// WORD codes scaled by the descriptor are the injected ±1 V square.
func checkWaveform(replies map[string]string) []harness.Result {
	const pre, data = ":WAV:PRE?", ":WAV:DATA?"
	desc, err := block(replies[pre])
	if err != nil || len(desc) != 346 || string(desc[:8]) != "WAVEDESC" {
		return []harness.Result{harness.Fail("scpi", pre, "descriptor: %v (%d bytes)", err, len(desc))}
	}
	le := binary.LittleEndian
	f32 := func(o int) float64 { return float64(math.Float32frombits(le.Uint32(desc[o:]))) }
	gain, off, cpd, count := f32(156), f32(160), f32(164), int(le.Uint32(desc[116:]))
	var res []harness.Result
	if le.Uint16(desc[32:]) != 1 || cpd != 7680 || math.Abs(gain-1) > 1e-6 || count <= 0 {
		res = append(res, harness.Fail("scpi", pre, "WORD %d, gain %g, codes/div %g, count %d", le.Uint16(desc[32:]), gain, cpd, count))
	} else {
		res = append(res, harness.Pass("scpi", pre, fmt.Sprintf("WORD, %d points, %g V/div, 7680 codes/div", count, gain)))
	}
	words, err := block(replies[data])
	if err != nil || len(words) == 0 || len(words)%2 != 0 {
		return append(res, harness.Fail("scpi", data, "data: %v (%d bytes)", err, len(words)))
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for i := 0; i+1 < len(words); i += 2 {
		v := float64(int16(le.Uint16(words[i:])))*gain/cpd - off //nolint:gosec // G115: a word sample is a signed 16-bit code
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	if math.Abs(hi-1) > 0.05 || math.Abs(lo+1) > 0.05 {
		return append(res, harness.Fail("scpi", data, "decoded %.3f … %.3f V, want ±1 V", lo, hi))
	}
	return append(res, harness.Pass("scpi", data, fmt.Sprintf("%d words decode to %.3f … %.3f V", len(words)/2, lo, hi)))
}
