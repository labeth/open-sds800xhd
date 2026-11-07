package frontend

import "testing"

func TestPickPhase(t *testing.T) {
	g := func(s string) (b [8]bool) {
		for i := range 8 {
			b[i] = s[i] == '1'
		}
		return b
	}
	for _, c := range []struct {
		good string
		want int
	}{
		{"11111111", 7},
		{"00111110", 4},
		{"01111000", 2},
		{"11000011", 0}, // a = 1, b = 6 − 8: s = −1 → 0
		{"11100001", 0},
	} {
		if got := PickPhase(g(c.good)); got != c.want {
			t.Errorf("%s: %d, want %d", c.good, got, c.want)
		}
	}
}
