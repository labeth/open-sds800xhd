package timebase

import "testing"

// The readout examples of spec 03 §4.3 (τ = 0, σ = 0).
func TestReadoutWindowExamples(t *testing.T) {
	for _, c := range []struct {
		name              string
		mask, enum, depth int
		t                 int
		delta, D, S       int
		O, L, N, lead     int
	}{
		{"1ch 2G 200ns 10k", 1, 9, 0, 6, 160, 32, 4000, 1, 1228, 4524, 368},
		{"2ch 1G 200ns 10k", 3, 9, 0, 6, 80, 32, 2000, 1, 678, 2324, 168},
		{"1ch 1G 1us 10k", 1, 11, 0, 6, 80, 136, 10000, 2, 2698, 10404, 248},
		{"1ch 100M 5us 10k", 1, 13, 0, 6, 40, 160, 5000, 12, 1423, 5344, 188},
		{"1ch 100M 100us 100k", 1, 17, 1, 6, 40, 2536, 100000, 13, 25168, 100324, 168},
		{"1ch 2G 2us 10M", 1, 12, 3, 6, 160, 256, 40000, 1, 10208, 40444, 288},
		{"1ch 100M 10us 10k", 1, 14, 0, 6, 40, 280, 10000, 10, 2668, 10324, 168},
		{"4ch 500M 500ns 10k", 0xF, 10, 0, 3, 40, 72, 2500, 0, 790, 2811, 316},
	} {
		h := Compute(Config{Enum: c.enum, Depth: c.depth, Mask: c.mask})
		if h.Delta != c.delta || h.D != c.D || int(h.S) != c.S {
			t.Errorf("%s: Δ=%d D=%d S=%v, want %d %d %d", c.name, h.Delta, h.D, h.S, c.delta, c.D, c.S)
			continue
		}
		w := h.ReadoutWindow(h.Position(PosInput{}), c.t)
		if w.O != c.O || w.L != c.L || w.N != c.N || w.Lead != c.lead {
			t.Errorf("%s: O=%d L=%d N=%d λ=%d, want %d %d %d %d", c.name, w.O, w.L, w.N, w.Lead, c.O, c.L, c.N, c.lead)
		}
	}
}

// The timebase example of spec 03 §3.7: 1 ch, 2 GSa/s, 1 µs/div.
func TestComputeExample(t *testing.T) {
	h := Compute(Config{Enum: 11, Depth: 1, Mask: 1})
	if h.R != 2000 || h.S != 20000 || h.Delta != 160 || h.I != 1 || h.D != 0x88 {
		t.Fatalf("set = %+v", h)
	}
	if n := Sections(int(float32(h.I) * h.S / 1000)); n != 5000 {
		t.Fatalf("N = %d", n)
	}
}

// The trigger position example of spec 03 §4.3: 1 ch 2 GSa/s 2 µs/div, PRE = 2556.
func TestPositionExample(t *testing.T) {
	h := Compute(Config{Enum: 12, Depth: 3, Mask: 1})
	if p := h.Position(PosInput{}); p.Pre != 2556 {
		t.Fatalf("PRE = %d, want 2556", p.Pre)
	}
}

// The roll examples of spec 04 §7.
func TestRollSetExamples(t *testing.T) {
	for _, c := range []struct {
		depth        int
		P, scan, lr  int
		D, offset, R int
	}{
		{3, 1000, 0x1F4, 0xFA, 0, 0x1C, 1},
		{0, 40, 0x14, 0xA, 0x118, 15, 0},
	} {
		h := Compute(Config{Enum: 26, Depth: c.depth, Mask: 1, Roll: true})
		r := h.RollSet()
		if r.P != c.P || r.Scan != c.scan || r.Lr != c.lr || r.Offset != c.offset {
			t.Errorf("depth %d: %+v (S %v, Δ %d, D %#x)", c.depth, r, h.S, h.Delta, h.D)
		}
		if c.D != 0 && h.D != c.D {
			t.Errorf("depth %d: D = %#x, want %#x", c.depth, h.D, c.D)
		}
	}
}

// A piece read covers the window from piece 0 in pieces of M samples.
func TestPieceWindow(t *testing.T) {
	h := Compute(Config{Enum: 20, Depth: 3, Mask: 1}) // 1 ch 10M 1 ms/div
	w := h.Window(h.Position(PosInput{}), 6, 0, int(h.S), true)
	if w.Comp != 0 || w.Factor != 1 || w.Pieces != 2 || w.Head%8 != 0 || w.N != w.Head+2*PieceSize {
		t.Fatalf("%+v (S %v)", w, h.S)
	}
	if h.Delta != 80 || w.O != 0x270 || w.L != 0x26_2670 {
		t.Errorf("piece window O %#x L %#x (Δ %d), want 0x270 0x262670 (spec 03 §4.3)", w.O, w.L, h.Delta)
	}
	if w6 := h.Window(h.Position(PosInput{}), 6, 6_000_000, 1000, true); w6.O != 0x1_2768 || w6.L != w.L {
		t.Errorf("STARt 6e6: O %#x L %#x, want 0x12768 and the record's L", w6.O, w6.L)
	}
	c := h.ReadoutWindow(h.Position(PosInput{}), 6)
	if c.Comp != 1 || c.Factor != 2 {
		t.Fatalf("screen read of 1e7 samples: %+v", c)
	}
}
