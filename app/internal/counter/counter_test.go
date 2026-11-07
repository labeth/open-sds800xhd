package counter

import (
	"testing"

	"github.com/labeth/open-sds800xhd/app/internal/mmio"
	"github.com/labeth/open-sds800xhd/app/internal/mmio/mmiotest"
)

func TestFormulas(t *testing.T) {
	// The simulator's model: count = ⌊f·(n − 1)/1000⌋ (spec 15 §4).
	for _, f := range []float64{5e6, 1.25e6, 25e3} {
		count := uint32(f * (AutoGate - 1) / 1000)
		if got := AutoFreq(count, AutoGate); got != f {
			t.Errorf("auto %g: %g", f, got)
		}
	}
	if got := PeriodFreq(1000, 40_000_000); got != 25e3 {
		t.Errorf("period: %g", got)
	}
	for _, c := range []struct {
		f float64
		n int
	}{{5, 1}, {50, 5}, {500, 10}, {5e3, 100}, {5e4, 1000}, {5e5, 10000}, {5e6, 1}} {
		if got := PeriodGate(c.f); got != c.n {
			t.Errorf("gate %g: %d, want %d", c.f, got, c.n)
		}
	}
}

func reg(n int) uint32 { return uint32(n) * 4 }

// TestCycle runs the measurement cycle against a fake plane: a slow signal
// goes from the auto gate to the period gate.
func TestCycle(t *testing.T) {
	bus := mmiotest.New()
	c := New(mmio.NewPlane("acq", bus))
	c.Start()
	if v, _ := bus.Last(reg(regAutoGate)); v != AutoGate {
		t.Errorf("auto gate %d", v)
	}
	done := uint32(0)
	bus.Status[reg(regGateDone)] = func() uint32 { return done }
	if _, ok := c.Poll(); ok {
		t.Fatal("valid before a gate")
	}
	// 25 kHz: auto count 22475 → below 1 MHz, so N = 1000 goes to 0x063.
	bus.Status[reg(regAutoLo)] = func() uint32 { return 22475 }
	bus.Status[reg(regAutoHi)] = func() uint32 { return 0 }
	done = autoDone
	if _, ok := c.Poll(); ok {
		t.Fatal("valid after the auto gate of a slow signal")
	}
	if v, _ := bus.Last(reg(regPeriodN)); v != 1000 {
		t.Errorf("period gate %d", v)
	}
	bus.Status[reg(regPeriodLo)] = func() uint32 { return 40_000_000 & 0xFFFF }
	bus.Status[reg(regPeriodHi)] = func() uint32 { return 40_000_000 >> 16 }
	done = periodDone
	if f, ok := c.Poll(); !ok || f != 25e3 {
		t.Errorf("period result %g %v", f, ok)
	}
	// Totalizer: lock pulse, then 56 bits from the four registers.
	bus.Status[reg(regTotal0)] = func() uint32 { return 0x3333 }
	bus.Status[reg(regTotal0+1)] = func() uint32 { return 0x2222 }
	bus.Status[reg(regTotal0+2)] = func() uint32 { return 0x1111 }
	bus.Status[reg(regTotal0+3)] = func() uint32 { return 0xAB55 }
	if got := c.Total(); got != 0x55_1111_2222_3333 {
		t.Errorf("total %#x", got)
	}
	var tot []uint32
	for _, w := range bus.Writes {
		if w.Off == reg(regTotalizer) {
			tot = append(tot, w.V)
		}
	}
	if n := len(tot); n < 2 || tot[n-2] != 0x104 || tot[n-1] != 0x004 {
		t.Errorf("lock pulse %#x", tot)
	}
}
