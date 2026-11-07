package acq

import (
	"encoding/binary"

	"github.com/labeth/open-sds800xhd/app/internal/timebase"
)

// RollGeometry is the roll stream's block geometry (spec 03 §6.4).
type RollGeometry struct {
	P      int // roll point count
	Rate   int // 0x0D5 compression rate r_roll
	Factor int // compression factor c_f
	L, B   int // payload bytes per channel block, stride
	Ch     int // enabled channels
	Group  int // bytes per group: eight strides per channel
}

// rollFifoMax is the most the stream FIFO can report available: 4·0x7FF +
// 8·0x7FF bytes (spec 03 §6.4).
const rollFifoMax = 4*0x7FF + 8*0x7FF

// NewRollGeometry chooses the compression for nCh channels of roll set h:
// uncompressed for short roll points whose group fits the FIFO, else
// r_roll = 12·n, which gives the points of spec 03 §6.4 at P = 1000 (the
// general rule is UNVERIFIED there).
func NewRollGeometry(h timebase.Set, nCh int) RollGeometry {
	nCh = max(1, nCh)
	P := h.RollSet().P
	r := 0
	if P >= 1000 || nCh*8*(2*P+8) > rollFifoMax {
		r = 12 * nCh
	}
	return rollGeometryOf(P, r, nCh)
}

func rollGeometryOf(p, r, nCh int) RollGeometry {
	g := RollGeometry{P: p, Rate: r, Factor: 1, Ch: nCh}
	if r > 0 && r < 0x1000 {
		g.Factor = 2 * r
	}
	g.L = 2 * (p / g.Factor)
	if g.Factor == 1 && p%4 != 0 {
		g.L *= 4
	}
	switch {
	case g.Factor > 1, g.L%8 == 0:
		g.B = g.L + 8
	default:
		g.B = g.L&^7 + 16
	}
	g.Group = nCh * g.B * 8
	return g
}

// RollParser splits the roll stream into per-channel sample runs (spec 03
// §6.4, both framings). It keeps the bytes of a block or word cut by a read
// for the next read.
type RollParser struct {
	g      RollGeometry
	synced bool // uncompressed: the first header found
	cur    int  // compressed: the current channel
	carry  []byte
}

// NewRollParser returns a parser for geometry g.
func NewRollParser(g RollGeometry) *RollParser { return &RollParser{g: g} }

// Feed parses one read and calls emit with each channel run (index 0–3).
// The words passed to emit are valid only during the call.
func (p *RollParser) Feed(b []byte, emit func(ch int, words []uint16)) {
	if len(p.carry) > 0 {
		b = append(p.carry, b...)
		p.carry = p.carry[:0]
	}
	var run [4]uint16
	if p.g.Factor > 1 {
		for ; len(b) >= 8; b = b[8:] {
			v := binary.LittleEndian.Uint64(b)
			if v < 4 {
				p.cur = int(v)
				continue
			}
			run = [4]uint16{uint16(v), uint16(v >> 16), uint16(v >> 32), uint16(v >> 48)} //nolint:gosec // G115: the four 16-bit words of one 64-bit stream word
			emit(p.cur, run[:])
		}
		p.carry = append(p.carry, b...)
		return
	}
	if !p.synced {
		for len(b) >= 8 && b[0] >= 4 {
			b = b[8:]
		}
		if len(b) < 8 {
			return
		}
		p.synced = true
	}
	words := make([]uint16, p.g.L/2)
	for len(b) >= p.g.B {
		ch := int(b[0])
		for i := range words {
			words[i] = binary.LittleEndian.Uint16(b[8+2*i:])
		}
		if ch < 4 {
			emit(ch, words)
		}
		b = b[p.g.B:]
	}
	p.carry = append(p.carry, b...)
}
