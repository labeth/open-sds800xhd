package frontend

import (
	"time"

	"github.com/labeth/open-sds800xhd/app/internal/mmio"
	"github.com/labeth/open-sds800xhd/app/internal/platform"
	"github.com/labeth/open-sds800xhd/app/internal/spi"
)

// SPI is the front-end bus as the ADCs and the channel setup use it.
type SPI interface {
	Write(cs int, tx ...byte)
	Xfer(cs int, tx []byte) []byte
}

// DES reports whether the converters interleave both cores on one input:
// exactly one analog channel enabled and the full 2 GSa/s rate (spec 06
// §9.2).
func DES(mask int, rateMSa float64) bool {
	return rateMSa == 2000 && mask&0xF != 0 && mask&(mask-1)&0xF == 0
}

// ADC12D1000 configuration words (spec 06 §9.2).
const (
	word1000NonDES = 0x2040
	word1000DESI   = 0x2080 // DES on the I input: CH2 or CH4 alone
	word1000DESQ   = 0x20C0 // DES on the Q input: CH1 or CH3 alone
)

// init1500 is the ADC12DL1500 init block (spec 06 §9.3).
var init1500 = [][2]int{
	{0x000, 0xB0},
	{0x200, 0x00},
	{0x061, 0x00},
	{0x029, 0x60},
	{0x02A, 0x00},
	{0x030, 0x00},
	{0x031, 0xA0},
	{0x032, 0x00},
	{0x033, 0xA0},
	{0x038, 0x00},
	{0x03B, 0x00},
	{0x048, 0x00},
	{0x060, 0x01},
	{0x062, 0x01},
	{0x068, 0x61},
	{0x06B, 0x00},
	{0x070, 0x00},
	{0x09D, 0x00},
	{0x160, 0x00},
	{0x161, 0x00},
	{0x204, 0x08},
	{0x206, 0x0F},
	{0x201, 0x02},
	{0x202, 0x50},
	{0x204, 0x08},
	{0x205, 0x01},
	{0x061, 0x01},
	{0x200, 0x01},
	{0x203, 0x01},
	{0x06C, 0x00},
	{0x06C, 0x01},
}

// ADCs are the one or two converters of the board.
type ADCs struct {
	acq   *mmio.Plane
	spi   SPI
	part  platform.ADCPart
	n     int           // fitted converters: 1 on 2-channel models
	reg   [2][16]uint16 // ADC12D1000 registers are write-only: our copy
	mode  int
	phase int
	log   func(string, ...any)
}

// NewADCs returns the converters of a unit.
func NewADCs(acq *mmio.Plane, bus SPI, id platform.Identity, log func(string, ...any)) *ADCs {
	a := &ADCs{acq: acq, spi: bus, part: id.ADC, n: 2, mode: -1, phase: defaultPhase, log: log}
	if id.Channels == 2 {
		a.n = 1
	}
	return a
}

// defaultPhase is the capture phase used when training fails (spec 06 §9.4).
const defaultPhase = 4

func (a *ADCs) w1000(n, reg int, v uint16) {
	a.reg[n][reg] = v
	a.spi.Write(spi.CSADC0+n, 0x40|byte(reg&0xF)<<1, byte(v>>8), byte(v)) //nolint:gosec // G115: the bytes of a 16-bit ADC register write
}

func (a *ADCs) w1500(n, reg int, v byte) {
	a.spi.Write(spi.CSADC0+n, byte(reg>>8)&0x7F, byte(reg), v) //nolint:gosec // G115: the bytes of a 15-bit ADC register address
}

func (a *ADCs) r1500(n, reg int) byte {
	return a.spi.Xfer(spi.CSADC0+n, []byte{0x80 | byte(reg>>8)&0x7F, byte(reg), 0})[2] //nolint:gosec // G115: the bytes of a 15-bit ADC register address
}

// Init runs the chip init of every fitted converter (spec 06 §9.2, §9.3).
func (a *ADCs) Init() {
	for n := range a.n {
		if a.part == platform.ADC12D1000 {
			a.w1000(n, 0, 0x2000)
			a.w1000(n, 0, 0x2040)
			continue
		}
		for range 3 {
			for _, rv := range init1500 {
				a.w1500(n, rv[0], byte(rv[1])) //nolint:gosec // G115: an 8-bit ADC register value from the init table
			}
			ok := false
			for i := 0; i < 500 && !ok; i++ {
				if ok = a.r1500(n, 0x06A)&3 != 0; !ok {
					time.Sleep(time.Millisecond)
				}
			}
			if !ok {
				a.log("ADC%d: init calibration did not finish", n)
			}
		}
	}
}

// SetMode writes the mode of every fitted converter for the enabled-channel
// mask at the ADC rate (spec 06 §9.2, §9.3) and reports whether the mode
// changed (a change needs an internal calibration before the next arm, spec
// 06 §9.5). On ADC12DL1500 boards a change of the DES state re-applies the
// stored capture phase (spec 06 §9.4 step 7).
func (a *ADCs) SetMode(mask int, rateMSa float64) bool {
	des := DES(mask, rateMSa)
	mode := 0
	if des {
		mode = 1 + mask&0xF // one key per single channel
	}
	changed := mode != a.mode
	desChanged := a.mode < 0 || (a.mode != 0) != des
	a.mode = mode
	for n := range a.n {
		if a.part == platform.ADC12D1000 {
			w := uint16(word1000NonDES)
			switch {
			case des && mask&0x5 != 0:
				w = word1000DESQ
			case des:
				w = word1000DESI
			}
			a.w1000(n, 0, w)
			continue
		}
		a.w1500(n, 0x200, 0)
		a.w1500(n, 0x061, 0)
		r201 := a.r1500(n, 0x201)
		if des {
			if v := r201 | 1<<2; v != r201 {
				a.w1500(n, 0x201, v)
			}
			mux := byte(1) // INA
			if mask&(1<<(2*n)) != 0 {
				mux = 2 // INB: the chip's first channel (CH1, CH3)
			}
			r060 := a.r1500(n, 0x060)
			if v := r060&^3 | mux; v != r060 {
				a.w1500(n, 0x060, v)
			}
		} else if v := r201 &^ (1 << 2); v != r201 {
			a.w1500(n, 0x201, v)
		}
		a.w1500(n, 0x061, 1)
		a.w1500(n, 0x200, 1)
	}
	if a.part == platform.ADC12DL1500 && desChanged {
		a.ApplyPhase()
	}
	return changed
}

func (a *ADCs) testPattern(on bool) {
	for n := range a.n {
		if a.part == platform.ADC12D1000 {
			v := a.reg[n][0] &^ (1 << 12)
			if on {
				v |= 1 << 12
			}
			a.w1000(n, 0, v)
			continue
		}
		a.w1500(n, 0x203, 0)
		if on {
			a.w1500(n, 0x205, 0x11)
		} else {
			a.w1500(n, 0x205, 0)
		}
	}
}

// InternalCal runs the converters' internal calibration (spec 06 §9.5).
func (a *ADCs) InternalCal() {
	if a.part == platform.ADC12D1000 {
		for n := range a.n {
			v := a.reg[n][0] &^ 0x8000
			a.w1000(n, 0, v)
			time.Sleep(time.Microsecond)
			a.w1000(n, 0, v|0x8000)
			time.Sleep(time.Microsecond)
		}
		time.Sleep(52 * time.Millisecond)
		return
	}
	for n := range a.n {
		a.w1500(n, 0x06C, 0)
		a.w1500(n, 0x06C, 1)
	}
	time.Sleep(52 * time.Millisecond)
	for range 500 {
		done := true
		for n := range a.n {
			done = done && a.r1500(n, 0x06A)&3 == 3
		}
		if done {
			return
		}
		time.Sleep(time.Millisecond)
	}
	a.log("ADC internal calibration did not finish")
}

// chanADC maps a channel to its converter and input: C1 = ADC0 Q, C2 = ADC0
// I, C3 = ADC1 Q, C4 = ADC1 I (spec 06 §9.1).
func chanADC(c int) (n int, q bool) { return c / 2, c%2 == 0 }

// SetFullScale writes the full-scale code of channel c's input; at the full
// rate both inputs of the converter carry the channel (spec 06 §9.2, §9.3).
func (a *ADCs) SetFullScale(c, code int, fullRate bool) {
	n, q := chanADC(c)
	if n >= a.n {
		return
	}
	if a.part == platform.ADC12D1000 {
		v := uint16(max(0, min(code, 0x7FFF)))
		switch {
		case fullRate:
			a.w1000(n, 0xB, v)
			a.w1000(n, 0x3, v)
		case q:
			a.w1000(n, 0xB, v)
		default:
			a.w1000(n, 0x3, v)
		}
		time.Sleep(100 * time.Microsecond)
		return
	}
	code = max(code, 0x2000)
	wA := func() { a.w1500(n, 0x031, byte(code>>8)); a.w1500(n, 0x030, byte(code)) } //nolint:gosec // G115: the two bytes of a 16-bit ADC code
	wB := func() { a.w1500(n, 0x033, byte(code>>8)); a.w1500(n, 0x032, byte(code)) } //nolint:gosec // G115: the two bytes of a 16-bit ADC code
	switch {
	case fullRate:
		wB()
		wA()
	case q:
		wB()
	default:
		wA()
	}
	time.Sleep(100 * time.Microsecond)
}

// waitDataPLL pulses the ADC PLL reset and waits for the data PLL, with up
// to three resets (spec 06 §9.4).
func (a *ADCs) waitDataPLL() bool {
	for range 3 {
		a.acq.Set(0x01F, 1, 0)
		time.Sleep(10 * time.Millisecond)
		if a.acq.Poll(0x07C, 1, true, 1000, time.Millisecond) {
			return true
		}
	}
	return false
}

// TrainPhase finds the capture phase with the converters' test pattern and
// applies it (spec 06 §9.4). Nothing else may use the registers meanwhile.
func (a *ADCs) TrainPhase() {
	acq := a.acq
	a.testPattern(true)
	acq.Field(0x1FA, 8, 8, 1)
	time.Sleep(10 * time.Millisecond)
	acq.Field(0x1FA, 8, 8, 0)
	time.Sleep(10 * time.Millisecond)
	var good [8]bool
	n, aborted := 0, false
	for p := 0; p < 8 && !aborted; p++ {
		acq.Set(0x04F, 1)
		if !a.waitDataPLL() {
			acq.Set(0x04F, 0)
			aborted = true
			break
		}
		acq.Field(0x07B, 2, 0, uint16(p))
		time.Sleep(10 * time.Millisecond)
		acq.Set(0x07A, 0, 1)
		if !acq.Poll(0x01F, 1<<8, true, 5000, time.Microsecond) {
			acq.Set(0x04F, 0)
			aborted = true
			break
		}
		acq.Set(0x04F, 0)
		time.Sleep(10 * time.Millisecond)
		acq.Set(0x07D, 0, 1, 0, 2)
		time.Sleep(10 * time.Millisecond)
		e := acq.HW(0x07D)
		good[p] = e&(1<<4) == 0 && (a.n == 1 || e&(1<<5) == 0)
		if good[p] {
			n++
		}
	}
	if aborted || n < 3 {
		a.log("phase training failed (aborted=%v, %d good phases): phase %d", aborted, n, defaultPhase)
		a.phase = defaultPhase
	} else {
		a.phase = PickPhase(good)
	}
	a.testPattern(false)
	a.ApplyPhase()
}

// PickPhase is the phase choice of spec 06 §9.4 step 4: the middle of the
// good run, with wrap-around when phases 0 and 7 are both good.
func PickPhase(good [8]bool) int {
	if !good[0] || !good[7] {
		first, last := -1, -1
		for p := range 8 {
			if good[p] {
				if first < 0 {
					first = p
				}
				last = p
			}
		}
		return (first + last) / 2
	}
	aa, bb := -1, -1
	for p := range 7 {
		if good[p] && !good[p+1] {
			aa = p
			break
		}
	}
	for p := 7; p >= 1; p-- {
		if good[p] && !good[p-1] {
			bb = p - 8
			break
		}
	}
	s := aa + bb
	best := s / 2
	if s < -1 {
		best += 8
	}
	return best
}

// ApplyPhase programs the chosen capture phase (spec 06 §9.4 step 6).
func (a *ADCs) ApplyPhase() {
	acq := a.acq
	acq.Set(0x01F, 1, 0)
	acq.Set(0x04F, 1)
	time.Sleep(10 * time.Millisecond)
	acq.Field(0x07B, 2, 0, uint16(a.phase)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	time.Sleep(10 * time.Millisecond)
	acq.Set(0x07A, 0, 1)
	acq.Poll(0x01F, 1<<8, true, 5000, time.Microsecond)
	acq.Set(0x04F, 0)
}

// Part is the fitted converter type.
func (a *ADCs) Part() platform.ADCPart { return a.part }
