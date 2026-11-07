package display

import (
	"image"
	"testing"

	"github.com/labeth/open-sds800xhd/app/internal/mmio"
	"github.com/labeth/open-sds800xhd/app/internal/mmio/mmiotest"
)

// Spec 07 §2.1: little-endian 0xAARRGGBB words, 4096-byte lines.
func TestPixelLayout(t *testing.T) {
	mem := make([]byte, Stride*H)
	fb := NewFB(mem)
	Put(fb.Back(), 1023, 599, ARGB(0xFF, 0x12, 0x34, 0x56))
	Put(fb.Back(), 0, 1, ARGB(0x80, 1, 2, 3))
	if err := fb.Present(); err != nil {
		t.Fatal(err)
	}
	if got := mem[599*4096+1023*4 : 599*4096+1024*4]; got[0] != 0x56 || got[1] != 0x34 || got[2] != 0x12 || got[3] != 0xFF {
		t.Errorf("bytes %x, want B G R A = 56 34 12 ff", got)
	}
	if At(mem, 0, 1) != 0x80010203 {
		t.Errorf("At = %#x", At(mem, 0, 1))
	}
	if fb.Stride() != 4096 {
		t.Error("stride")
	}
}

func TestInfoCheck(t *testing.T) {
	good := fbInfo{xres: 1024, yres: 600, bpp: 32, red: 16, green: 8, blue: 0, tr: 24, lineLength: 4096}
	if err := good.check(); err != nil {
		t.Error(err)
	}
	bad := good
	bad.bpp, bad.red = 16, 11
	if bad.check() == nil {
		t.Error("RGB565 fb0 accepted")
	}
}

func TestFromRGB565(t *testing.T) {
	src := make([]byte, 2048*H)
	put := func(x, y int, v uint16) { src[y*2048+2*x], src[y*2048+2*x+1] = byte(v), byte(v>>8) }
	put(0, 0, 0xF800) // red
	put(5, 7, 0x07E0) // green
	put(1023, 599, 0x001F)
	dst := image.NewRGBA(image.Rect(0, 0, W, H))
	FromRGB565(dst, src, 2048)
	for _, c := range []struct {
		x, y    int
		r, g, b uint8
	}{{0, 0, 255, 0, 0}, {5, 7, 0, 255, 0}, {1023, 599, 0, 0, 255}, {1, 0, 0, 0, 0}} {
		p := dst.RGBAAt(c.x, c.y)
		if p.R != c.r || p.G != c.g || p.B != c.b || p.A != 255 {
			t.Errorf("(%d,%d) = %v", c.x, c.y, p)
		}
	}
}

// Spec 07 §4.2: the capture path start, in order.
func TestStartPath(t *testing.T) {
	bus := mmiotest.New()
	mp := mmio.NewPlane("map", mmiotest.New())
	c := NewCapturer(bus, mp, "")
	c.startPath()
	w := func(off, v uint32) mmiotest.Write { return mmiotest.Write{Off: off, V: v} }
	want := []mmiotest.Write{w(0x30, 4), w(0x30, 0x18003), w(0xAC, 0x1F800000), w(0xA8, 0x800), w(0xA4, 0x800), w(0xA0, 600)}
	if len(bus.Writes) != len(want) {
		t.Fatalf("writes %v", bus.Writes)
	}
	for i, w := range want {
		if bus.Writes[i] != w {
			t.Errorf("write %d = %+v, want %+v", i, bus.Writes[i], w)
		}
	}
	if mp.Shadow(0x158) != 1 || mp.Shadow(0x1B0) != 1 {
		t.Error("PL capture stream not started")
	}
	if !c.running() {
		t.Error("not running after start")
	}
}
