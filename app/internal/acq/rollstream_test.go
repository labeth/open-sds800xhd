package acq

import (
	"encoding/binary"
	"slices"
	"testing"

	"github.com/labeth/open-sds800xhd/app/internal/timebase"
)

// The geometries of spec 03 §6.4: 1 ch 10k 100 ms/div (P 40,
// uncompressed, group 704) and 1 ch 10M 100 ms/div (P 1000, r 12).
func TestRollGeometry(t *testing.T) {
	g := NewRollGeometry(timebase.Compute(timebase.Config{Enum: 26, Depth: 0, Mask: 1, Roll: true}), 1)
	if g.P != 40 || g.Rate != 0 || g.L != 80 || g.B != 88 || g.Group != 704 {
		t.Errorf("10k: %+v", g)
	}
	g = NewRollGeometry(timebase.Compute(timebase.Config{Enum: 26, Depth: 3, Mask: 1, Roll: true}), 1)
	if g.P != 1000 || g.Rate != 0xC || g.Factor != 24 {
		t.Errorf("10M: %+v", g)
	}
	if g := NewRollGeometry(timebase.Compute(timebase.Config{Enum: 26, Depth: 3, Mask: 3, Roll: true}), 2); g.Rate != 0x18 {
		t.Errorf("10M 2 ch: %+v", g)
	}
}

func TestRollParserUncompressed(t *testing.T) {
	g := rollGeometryOf(40, 0, 2)
	var stream []byte
	stream = append(stream, make([]byte, 8)...) // a stale word before the first header
	stream[0] = 0xFF
	for blk := range 4 {
		hdr := make([]byte, 8)
		hdr[0] = byte(blk % 2)
		stream = append(stream, hdr...)
		for i := range g.L / 2 {
			stream = binary.LittleEndian.AppendUint16(stream, uint16(blk<<8|i))
		}
		stream = append(stream, make([]byte, g.B-8-g.L)...)
	}
	got := map[int][]uint16{}
	p := NewRollParser(g)
	// Cut the stream at an odd place: the parser carries the rest over.
	for _, part := range [][]byte{stream[:100], stream[100:]} {
		p.Feed(part, func(ch int, w []uint16) { got[ch] = append(got[ch], w...) })
	}
	if len(got[0]) != 2*g.L/2 || len(got[1]) != 2*g.L/2 || got[1][0] != 1<<8 || got[0][g.L/2] != 2<<8 {
		t.Fatalf("runs %d/%d, first %x %x", len(got[0]), len(got[1]), got[1][:1], got[0][g.L/2:g.L/2+1])
	}
}

func TestRollParserCompressed(t *testing.T) {
	g := rollGeometryOf(1000, 12, 1)
	var stream []byte
	stream = binary.LittleEndian.AppendUint64(stream, 1) // marker: C2
	stream = binary.LittleEndian.AppendUint64(stream, 0x0004_0003_0002_8001)
	stream = binary.LittleEndian.AppendUint64(stream, 0) // marker: C1
	stream = binary.LittleEndian.AppendUint64(stream, 0x0008_0007_0006_8005)
	got := map[int][]uint16{}
	p := NewRollParser(g)
	p.Feed(stream[:13], func(ch int, w []uint16) { got[ch] = append(got[ch], w...) })
	p.Feed(stream[13:], func(ch int, w []uint16) { got[ch] = append(got[ch], w...) })
	if !slices.Equal(got[1], []uint16{0x8001, 2, 3, 4}) || !slices.Equal(got[0], []uint16{0x8005, 6, 7, 8}) {
		t.Fatalf("%x", got)
	}
}
