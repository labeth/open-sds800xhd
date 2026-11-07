package mmio_test

import (
	"testing"
	"time"

	"github.com/labeth/open-sds800xhd/app/internal/mmio"
	"github.com/labeth/open-sds800xhd/app/internal/mmio/mmiotest"
)

func TestPlaneWritesUnconditionally(t *testing.T) {
	bus := mmiotest.New()
	p := mmio.NewPlane("acq", bus)
	p.Set(0x003, 0x10)
	p.Set(0x003, 0x10)
	p.Set(0x0C8, 0, 1)
	if got := len(bus.Writes); got != 4 {
		t.Fatalf("writes = %d, want 4 (equal values are written again)", got)
	}
	if w := bus.Writes[0]; w.Off != 0x003*4 || w.V != 0x10 {
		t.Fatalf("first write = %+v", w)
	}
}

func TestFieldUsesShadowNotHardware(t *testing.T) {
	bus := mmiotest.New()
	// 0x019 reads back status bit 9 set; a read-modify-write must not copy it.
	bus.Status[0x019*4] = func() uint32 { return 1 << 9 }
	p := mmio.NewPlane("acq", bus)
	p.Field(0x019, 3, 3, 1)
	if v, _ := bus.Last(0x019 * 4); v != 0x0008 {
		t.Fatalf("0x019 = %#x, want 0x0008", v)
	}
	p.Field(0x022, 14, 12, 3)
	p.Field(0x022, 9, 8, 2)
	if v := p.Shadow(0x022); v != 0x3200 {
		t.Fatalf("0x022 shadow = %#x, want 0x3200", v)
	}
}

func TestSet32SplitsLowWordFirst(t *testing.T) {
	bus := mmiotest.New()
	p := mmio.NewPlane("acq", bus)
	p.Set32(0x0C3, 0x0C4, 0x1_2768, 0x7F)
	want := []mmiotest.Write{{Off: 0x0C3 * 4, V: 0x2768}, {Off: 0x0C4 * 4, V: 0x1}}
	for i, w := range want {
		if bus.Writes[i] != w {
			t.Fatalf("write %d = %+v, want %+v", i, bus.Writes[i], w)
		}
	}
}

func TestPollCountsReads(t *testing.T) {
	bus := mmiotest.New()
	n := 0
	bus.Status[0x021*4] = func() uint32 { n++; return 0 }
	p := mmio.NewPlane("acq", bus)
	if p.Poll(0x021, 1, true, 3, 0) {
		t.Fatal("poll met a condition that never holds")
	}
	if n != 4 {
		t.Fatalf("reads = %d, want n+1 = 4", n)
	}
	n = 0
	bus.Status[0x021*4] = func() uint32 { n++; return uint32(n / 3) }
	if !p.Poll(0x021, 1, true, 10, time.Nanosecond) || n != 3 {
		t.Fatalf("poll stopped after %d reads, want 3", n)
	}
}
