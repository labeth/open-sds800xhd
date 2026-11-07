package trigger

import (
	"testing"

	"github.com/labeth/open-sds800xhd/app/internal/mmio"
	"github.com/labeth/open-sds800xhd/app/internal/mmio/mmiotest"
)

func newTest() (*Trigger, *mmiotest.Bus, *mmiotest.Bus) {
	ab, mb := mmiotest.New(), mmiotest.New()
	t := New(mmio.NewPlane("acq", ab), mmio.NewPlane("map", mb), 4)
	t.RestoreDefaults()
	return t, ab, mb
}

func reg(t *testing.T, b *mmiotest.Bus, idx int, want uint16) {
	t.Helper()
	got, ok := b.Last(uint32(idx) * 4)
	if !ok || uint16(got) != want {
		t.Errorf("reg %#03x = %#x (written %v), want %#x", idx, got, ok, want)
	}
}

func oneVolt() [4]Chan {
	var c [4]Chan
	for i := range c {
		c[i] = Chan{VoltsDiv: 1, Class: 17}
	}
	return c
}

// Level examples of spec 05 §3.2, §3.3, §5.6.
func TestLevelExamples(t *testing.T) {
	for _, c := range []struct {
		v, vdiv float64
		class   int
		s       Dir
		want    uint16
	}{
		{0.1, 0.01, 11, Rising, 0xF34},    // clamped to +246 rows, h 124
		{-0.15, 1, 17, Rising, 0x77A},     // the video sync-tip level
		{-0.025, 0.5, 17, Falling, 0x826}, // −3 rows
		{0.5, 1, 17, Falling, 0x92E},
		{0.05, 1, 17, Falling, 0x856},
	} {
		h := NoiseValue(c.class, false)
		if got := LevelCode(LevelPx(c.v, c.vdiv, 0, CoupDC), c.s, h); got != c.want {
			t.Errorf("%g V at %g V/div: %#x, want %#x", c.v, c.vdiv, got, c.want)
		}
	}
	for _, c := range []struct {
		class  int
		reject bool
		want   int
	}{{17, false, 62}, {17, true, 158}, {11, false, 124}, {10, true, 158}, {9, false, 240}, {7, true, 240}} {
		if got := NoiseValue(c.class, c.reject); got != c.want {
			t.Errorf("class %d reject %v: h %d, want %d", c.class, c.reject, got, c.want)
		}
	}
}

// Video examples of spec 05 §5.6.
func TestVideoWords(t *testing.T) {
	for _, c := range []struct {
		v    VideoSet
		want uint16
	}{
		{VideoSet{Standard: NTSC, Field: 1, Line: 2}, 0x0103},
		{VideoSet{Standard: NTSC, Field: 1, Line: 10}, 0x1005},
		{VideoSet{Standard: NTSC, Field: 2, Line: 4}, 0x1106},
		{VideoSet{Standard: PAL, Field: 1, Line: 4}, 0x0137},
		{VideoSet{Standard: P720L60, Field: 2, Line: 2}, 0x02ED},
		{VideoSet{Standard: P1080L50, Field: 1, Line: 2}, 0x0464},
		{VideoSet{Standard: Custom, Fields: 4, Interlace: 1, Lines: 300, Field: 3, Line: 10}, 0x3009},
	} {
		if got := VideoLine(c.v); got != c.want {
			t.Errorf("%+v: %#x, want %#x", c.v, got, c.want)
		}
	}
	if got := VideoConfig(VideoSet{Standard: Custom, Lines: 300, Rate: 0, Interlace: 1}); got != 0x03FF {
		t.Errorf("custom 300 lines: %#x, want the period clamped to 0x3FF", got)
	}
	if got := VideoConfig(VideoSet{Standard: Custom, Lines: 800, Rate: 1, Interlace: 2}); got != 0x128B {
		t.Errorf("custom 800 lines 30 Hz interlace 2: %#x, want 0x128B", got)
	}
	if got := VideoConfig(VideoSet{Standard: PAL}); got != 0x13E8 {
		t.Errorf("PAL: %#x", got)
	}
}

// Time and holdoff examples of spec 05 §5.3 and §6.
func TestTimes(t *testing.T) {
	for _, c := range []struct {
		s    float64
		want uint64
	}{{3.7e-9, 4}, {1.23456789e-6, 1235}, {5.0000009e-6, 5000}} {
		if got := NS(c.s); got != c.want {
			t.Errorf("%g s: %d ns, want %d", c.s, got, c.want)
		}
	}
	for _, c := range []struct {
		s    float64
		want uint64
	}{{1.0000003e-6, 250}, {8e-9, 2}, {30, 7_500_000_000}} {
		if got := HoldoffTicks(c.s); got != c.want {
			t.Errorf("holdoff %g s: %d ticks, want %d", c.s, got, c.want)
		}
	}
	tr, ab, _ := newTest()
	tr.setTB(NS(19.5))
	reg(t, ab, 0x014, 0x6300)
	reg(t, ab, 0x015, 0x8A4A)
	reg(t, ab, 0x072, 0x0400)
}

// Serial examples of spec 05 §10.
func TestSerialExamples(t *testing.T) {
	x := SPISet{N: 8}
	copy(x.Bits[:], []State{High, Low, High, DontCare, Low, Low, High, High})
	d, m := SPIPattern(&x)
	if d[0] != 0xC5 || m[0] != 0xF7 {
		t.Errorf("SPI LSB first: %#x/%#x, want 0xC5/0xF7", d[0], m[0])
	}
	x.MSBFirst = true
	if d, m = SPIPattern(&x); d[0] != 0xA3 || m[0] != 0xEF {
		t.Errorf("SPI MSB first: %#x/%#x, want 0xA3/0xEF", d[0], m[0])
	}
	for _, c := range []struct {
		v, bits    int
		data, mask uint16
		clamped    int
	}{{165, 7, 0x80, 0, 128}, {128, 5, 0x20, 0, 32}, {32, 8, 0x20, 0xFF, 32}, {256, 8, 0, 0, 256}} {
		v, d, m := UARTValue(c.v, c.bits)
		if v != c.clamped || d != c.data || m != c.mask {
			t.Errorf("UART %d at %d bits: %d %#x/%#x, want %d %#x/%#x", c.v, c.bits, v, d, m, c.clamped, c.data, c.mask)
		}
	}
	if w := BaudWord(19200); w != 0x0032_54E6 {
		t.Errorf("19200 Bd: %#x", w)
	}
	// I2C: R/W read, then EEPROM keeps the R/W code (§10.4).
	tr, _, mb := newTest()
	s := Default()
	s.Type, s.Sweep = Serial, Normal
	s.Serial = SerialSet{Protocol: I2C, I2C: I2CSet{SDA: Line{Source: 1}, Condition: 5, Address: 0x50, RW: 1, Data: NoBytes}}
	tr.Program(s, oneVolt())
	reg(t, mb, 0x047, 0x0C50)
	s.Serial.I2C.Condition = 4
	tr.Program(s, oneVolt())
	reg(t, mb, 0x048, 0x1E14)
	reg(t, mb, 0x047, 0x0C50)
	// 10 MBd and above: the short bit timing.
	s.Serial = SerialSet{Protocol: UART, UART: UARTSet{Baud: 12_500_000, Bits: 8, Value: 256}}
	tr.Program(s, oneVolt())
	n := 1_000_000_000 / (4 * 12_500_000)
	reg(t, mb, 0x053, uint16(1|(n-1)<<5|((n-1)>>1)<<1))
}

// The start-up restore leaves the values of spec 05 §7.
func TestRestore(t *testing.T) {
	_, ab, mb := newTest()
	for idx, v := range map[int]uint16{0x017: 0x13E0, 0x016: 0x1902, 0x0AC: 0x2222, 0x080: 0x1001, 0x087: 1, 0x111: 0} {
		reg(t, ab, idx, v)
	}
	for idx, v := range map[int]uint16{0x030: 1, 0x032: 0x40, 0x045: 0x32, 0x046: 0x54E6, 0x053: 0x12A} {
		reg(t, mb, idx, v)
	}
}

// Force sets the Force row and Restore the selected mode (§7).
func TestForce(t *testing.T) {
	tr, ab, _ := newTest()
	s := Default()
	s.HoldoffS = 1e-6
	tr.Program(s, oneVolt())
	tr.ForceOnce()
	reg(t, ab, 0x00A, 0x0001)
	reg(t, ab, 0x00C, 1)
	reg(t, ab, 0x02A, 1)
	tr.Restore()
	reg(t, ab, 0x00A, 0x0101)
	reg(t, ab, 0x00E, 0x1000)
}

// An inverted source takes the opposite slope and its level negated (§3.3).
func TestInvertedSource(t *testing.T) {
	tr, ab, _ := newTest()
	ch := oneVolt()
	ch[0].Invert = true
	s := Default()
	s.Level = 0.5
	tr.Program(s, ch)
	reg(t, ab, 0x0B5, 2)
	reg(t, ab, 0x0E0, uint16(2048-240+62))
}
