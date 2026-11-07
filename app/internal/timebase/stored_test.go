package timebase

import "testing"

// The examples of spec 04 §12 and spec 07 §7.8.
func TestStoredSetExamples(t *testing.T) {
	t.Parallel()
	rec := Compute(Config{Enum: EnumOf(200e-9, 2), Depth: 0, Mask: 1})
	if rec.R != 2000 || rec.S != 4000 || rec.Delta != 160 || rec.D != 0x20 {
		t.Fatalf("record set R %g S %g Δ %d D %#x, want 2000, 4000, 160, 0x20", rec.R, rec.S, rec.Delta, rec.D)
	}
	tests := []struct {
		name         string
		tdiv, deltaU float64
		parts, l0    int
		start, r     int
	}{
		{name: "200 ns/div record at 1 µs/div", tdiv: 1e-6, parts: 1, l0: 0x408, start: 2, r: 0x24},
		{name: "at 100 ns/div", tdiv: 100e-9, parts: 1, l0: (128 + 2000) / 4, start: 9, r: 6},
		{name: "at 100 ns/div, delay +0.1 µs", tdiv: 100e-9, deltaU: 0.1, parts: 1, l0: (128 + 2000) / 4, start: 0xA, r: 0x10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			st, ok := rec.StoredSet(StoredInput{TdivS: tt.tdiv, DelayDeltaUs: tt.deltaU})
			if !ok {
				t.Fatal("no stored set")
			}
			l0 := (128 + st.P) / 4
			if st.Parts != tt.parts || l0 != tt.l0 || st.Start != tt.start || st.R != tt.r {
				t.Errorf("parts %d L0 %#x start %d r %#x, want %d %#x %d %#x", st.Parts, l0, st.Start, st.R, tt.parts, tt.l0, tt.start, tt.r)
			}
		})
	}
	st, _ := rec.StoredSet(StoredInput{TdivS: 1e-6})
	if l1, ten := st.Iv*st.P/4, rec.Delta/4+(128+st.P)/4; l1 != 0x3E8 || ten != 0x430 {
		t.Errorf("0x094 %#x 0x10E %#x, want 0x3E8 0x430", l1, ten)
	}
}

// TestStoredSetFine: 1 ms/div 100k held and redrawn at the same scale moves
// the remainder by ρ (live 0x09E 6, 0x08E 0x10; stored 5 and 0x11, spec 04 §12).
func TestStoredSetFine(t *testing.T) {
	t.Parallel()
	rec := Compute(Config{Enum: EnumOf(1e-3, 2), Depth: 1, Mask: 1})
	if rec.Delta != 40 || rec.I != 1 {
		t.Fatalf("Δ %d I %d, want 40 1", rec.Delta, rec.I)
	}
	live := rec.Position(PosInput{})
	if live.R != 6 || live.A1 != 0x10 {
		t.Fatalf("live r %d a1 %#x, want 6 0x10", live.R, live.A1)
	}
	st, _ := rec.StoredSet(StoredInput{TdivS: rec.TimeDiv, Rho: 2})
	if st.R != 5 || st.A1 != 0x11 || st.Start != live.Start {
		t.Errorf("stored r %d a1 %#x start %d, want 5 0x11 %d", st.R, st.A1, st.Start, live.Start)
	}
}

func TestStoredMapSetExample(t *testing.T) {
	t.Parallel()
	m := StoredMapSet(4000, 20000, 1, 200e-9, 1e-6, 0, 0)
	if m.PPP != 20 || m.P/10 != 0x190 || m.P/20 != 0xC8 || m.Seg != 1 || m.Start != 0x190 || m.End != 0x258 || m.P/5 != 0x320 {
		t.Errorf("stored map set %+v, want ppp 20, 0x19B 0x190, 0x19A 0xC8, seg 1, window 0x190…0x258, 0x153 0x320", m)
	}
	if _, _, a, _ := DPO(1, m.PPP, 1, 1, false, false); a != 5 {
		t.Errorf("DPO a %d, want 5", a)
	}
}
