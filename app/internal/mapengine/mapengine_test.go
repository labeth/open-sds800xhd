package mapengine

import (
	"slices"
	"testing"

	"github.com/labeth/open-sds800xhd/app/internal/mmio"
	"github.com/labeth/open-sds800xhd/app/internal/mmio/mmiotest"
	"github.com/labeth/open-sds800xhd/app/internal/timebase"
)

type rig struct {
	m      *Engine
	mp     *mmio.Plane
	writes []write
}

type write struct {
	idx int
	v   uint16
}

func newRig(channels int) *rig {
	r := &rig{}
	r.mp = mmio.NewPlane("map", mmiotest.New())
	acq := mmio.NewPlane("acq", mmiotest.New())
	r.mp.SetTrace(func(_ string, idx int, v uint16) { r.writes = append(r.writes, write{idx, v}) })
	r.m = New(r.mp, acq, channels)
	return r
}

// seq is the values written to idx, in order.
func (r *rig) seq(idx int) []uint16 {
	var out []uint16
	for _, w := range r.writes {
		if w.idx == idx {
			out = append(out, w.v)
		}
	}
	return out
}

func (r *rig) check(t *testing.T, want map[int]uint16) {
	t.Helper()
	for idx, v := range want {
		if got := r.mp.Shadow(idx); got != v {
			t.Errorf("MAP %#03x = %#x, want %#x", idx, got, v)
		}
	}
}

// Spec 07 §7.2: persistence types and the INF → OFF example.
func TestPersistence(t *testing.T) {
	r := newRig(4)
	r.m.SetPersistence(PersistInfinite)
	r.check(t, map[int]uint16{0x080: 0, 0x140: 3, 0x144: 3})
	r.writes = nil
	r.m.SetPersistence(PersistOff)
	if got, want := r.seq(0x140), []uint16{0, 2, 0}; !slices.Equal(got, want) {
		t.Errorf("0x140 writes %#x, want %#x", got, want)
	}
	r.check(t, map[int]uint16{0x080: 3})
	if len(r.seq(0x144)) != 0 {
		t.Error("0x144 written for Off")
	}
	r.m.SetPersistence(Persist10s)
	r.check(t, map[int]uint16{0x080: 0, 0x140: 1, 0x144: 10})
}

func TestPersistenceOf(t *testing.T) {
	for s, want := range map[float64]Persistence{0: PersistOff, -1: PersistInfinite, 1: Persist1s, 5: Persist5s, 10: Persist10s, 30: Persist30s} {
		if p, ok := PersistenceOf(s); !ok || p != want {
			t.Errorf("PersistenceOf(%v) = %v %v", s, p, ok)
		}
		if p, _ := PersistenceOf(s); p.Seconds() != s && s >= 0 {
			t.Errorf("%v.Seconds() = %v", p, p.Seconds())
		}
	}
	for _, s := range []float64{0.1, 2, 60} {
		if _, ok := PersistenceOf(s); ok {
			t.Errorf("PersistenceOf(%v) accepted", s)
		}
	}
}

// Spec 07 §7.1: intensity, colour grade, grid and transparency defaults.
func TestAppearance(t *testing.T) {
	r := newRig(4)
	r.m.Appearance(DefaultDisplay())
	r.check(t, map[int]uint16{0x0E7: 0, 0x0FE: 0x42, 0x0D0: 0x3333, 0x0D1: 0x33, 0x14D: 0x44B, 0x0FA: 0x99})
	d := DefaultDisplay()
	d.ColorGrade, d.IntensityPct = true, 100
	r.m.Appearance(d)
	r.check(t, map[int]uint16{0x0E7: 1, 0x0FE: 116})
}

func TestBacklight(t *testing.T) {
	r := newRig(4)
	r.m.Backlight(50, true)
	r.check(t, map[int]uint16{0x14D: 1099, 0x016: 1})
	r.m.Backlight(100, true)
	r.check(t, map[int]uint16{0x14D: 1999})
	r.m.Backlight(0, false)
	r.check(t, map[int]uint16{0x016: 0, 0x14D: 1999})
}

// Spec 07 §7.8 example: 2 GSa/s, 500 ns/div, S = 10 000, I = 1.
func TestTimebaseSet(t *testing.T) {
	r := newRig(4)
	r.m.TimebaseSets(timebase.Set{S: 10000, I: 1})
	r.check(t, map[int]uint16{
		0x19D: 10, 0x060: 250, 0x19F: 1, 0x19B: 500, 0x19A: 250, 0x1A1: 0, 0x1A3: 1,
		0x1A7: 2, 0x1AB: 1, 0x1AE: 0, 0x1AF: 1000, 0x153: 1000, 0x1BD: 0, 0x1BE: 0x1DF,
	})
	// Four normal transactions (spec 07 §7.3): set 3 ends 0x1031, 0x30.
	w := r.seq(0x190)
	if got := w[len(w)-2:]; !slices.Equal(got, []uint16{0x1031, 0x30}) {
		t.Errorf("last transaction words %#x", got)
	}
}

// Spec 07 §7.7 XY-only examples: 200 ns/div (S = 2000) and 1 µs/div
// (S = 10 000) at 1 GSa/s.
func TestXYSets(t *testing.T) {
	r := newRig(2)
	r.m.SetXY(true, false)
	r.m.TimebaseSets(timebase.Set{S: 2000, I: 1})
	r.check(t, map[int]uint16{0x19B: 0x190, 0x19D: 4, 0x19F: 3, 0x1A1: 0x10, 0x199: 0x7D0, 0x153: 0x320})
	r.m.TimebaseSets(timebase.Set{S: 10000, I: 1})
	r.check(t, map[int]uint16{0x19B: 0x1F4, 0x19A: 0xFA, 0x19D: 0xA, 0x1A7: 4, 0x153: 0x3E8, 0x199: 0x9C4})
	r.m.XYMapping(true)
	r.check(t, map[int]uint16{0x0C4: 0x1E0, 0x0C3: 0x1E0, 0x0C5: 0x138})
}

// TestYTXYSets: the YT+XY display keeps the main sets normal and writes the
// XY variant into sets 4 + n (spec 07 §7.7).
func TestYTXYSets(t *testing.T) {
	r := newRig(2)
	r.m.SetXY(false, true)
	r.m.TimebaseSets(timebase.Set{S: 2000, I: 1})
	// The last set written is 5, the XY variant.
	r.check(t, map[int]uint16{0x19B: 0x190, 0x199: 0x7D0, 0x153: 0x320, 0x190: 0x50})
	r.m.XYMapping(false)
	r.check(t, map[int]uint16{0x0C4: 1000, 0x0C3: 1000, 0x0C5: 0xA0})
}

// Spec 07 §7.4, §7.8, §7.9: zoom 200 ns/div under 1 ms/div at 1 GSa/s
// (S = 1e7, S_z = 2000, I_z = 1, ppp_z = 2).
func TestZoom(t *testing.T) {
	r := newRig(4)
	r.m.SetZoom(true)
	r.check(t, map[int]uint16{0x10A: 1, 0x090: 1, 0x091: 460, 0x092: 115, 0x093: 20, 0x08F: 0})
	r.m.TimebaseSets(timebase.Set{S: 1e7, I: 1})
	r.check(t, map[int]uint16{0x1BD: 0x168, 0x1BE: 0x1DF})
	r.writes = nil
	r.m.ZoomSets(1e7, 2000, 1, 0)
	r.check(t, map[int]uint16{
		0x061: 250, 0x19B: 200, 0x19A: 2, 0x19D: 2, 0x1A7: 1, 0x154: 400,
		0x1AA: 1, 0x14C: 0, 0x13F: 0x10, 0x1BD: 0x168,
	})
	// Sets 4..7: the first transaction selects set 4.
	if w := r.seq(0x190); len(w) < 2 || w[1] != 0x41 {
		t.Errorf("zoom transaction words %#x, want set 4", w)
	}
	r.m.SetZoom(false)
	r.m.TimebaseSets(timebase.Set{S: 1e7, I: 1})
	r.check(t, map[int]uint16{0x10A: 0, 0x091: 480, 0x092: 480, 0x093: 0, 0x1BD: 0})
}

// Spec 07 §7.1: ranks follow the selection order; off channels last.
func TestChannelRanks(t *testing.T) {
	r := newRig(4)
	r.m.Channels([]bool{true, false, false, false}, []int{0})
	r.check(t, map[int]uint16{0x131: 0, 0x132: 3, 0x133: 2, 0x134: 1, 0x106: 1, 0x107: 0})
	r.m.Channels([]bool{true, true, true, true}, []int{3, 2, 1, 0})
	r.check(t, map[int]uint16{0x131: 3, 0x132: 2, 0x133: 1, 0x134: 0})
}

// Spec 08 §6.2: the LED word in two halves with the latch.
func TestLEDs(t *testing.T) {
	r := newRig(4)
	r.m.LEDs(0x26002)
	got := []write{}
	got = append(got, r.writes...)
	want := []write{{0x16C, 0}, {0x16A, 0x6002}, {0x16C, 1}, {0x16C, 0}, {0x16F, 2}, {0x16C, 1}}
	if !slices.Equal(got, want) {
		t.Errorf("LED writes %v, want %v", got, want)
	}
}

// TestStoredSets: the stored-record example of spec 07 §7.8 (1 ch,
// 200 ns/div record viewed at 1 µs/div): set 0's values, its closing words
// and the empty gain/offset transaction, and the DPO value for ppp 20.
func TestStoredSets(t *testing.T) {
	r := newRig(4)
	r.m.StoredSets(timebase.StoredMapSet(4000, 20000, 1, 200e-9, 1e-6, 0, 0), 1, true, 1)
	r.check(t, map[int]uint16{
		0x19B: 0x190, 0x19A: 0xC8, 0x1A7: 1, 0x1AE: 0x190, 0x1AF: 0x258, 0x153: 0x320,
		0x13E: 5, 0x189: 1, 0x1BD: 0, 0x1BE: 0x1DF,
	})
	words := r.seq(0x190)
	want := []uint16{0x1, 0x1001, 0x0, 0x1, 0x8001, 0x1, 0x0}
	if len(words) < len(want) || !slices.Equal(words[:len(want)], want) {
		t.Errorf("0x190 words %#x, want first %#x", words, want)
	}
}
