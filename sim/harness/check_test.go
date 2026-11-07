package harness

import "testing"

func TestHexMatch(t *testing.T) {
	for _, c := range []struct {
		p, tx string
		ok    bool
	}{
		{"0005x7", "000507", true},
		{"0005x7", "0005f7", true},
		{"0005x7", "000508", false},
		{"ffx8", "FFF8", true},
		{"ff", "fff8", false},
	} {
		if HexMatch(c.p, c.tx) != c.ok {
			t.Errorf("%s %s", c.p, c.tx)
		}
	}
}
