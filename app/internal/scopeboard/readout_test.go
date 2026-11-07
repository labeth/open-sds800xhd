package scopeboard

import (
	"slices"
	"testing"
)

func TestReducePairs(t *testing.T) {
	words := []uint16{9, 1, 8, 2, 7, 3, 6, 4, 5, 5}
	tests := []struct {
		name     string
		points   int
		max, min []uint16
		bucket   int
	}{
		{"every pair", 0, []uint16{9, 8, 7, 6, 5}, []uint16{1, 2, 3, 4, 5}, 1},
		{"as many points as pairs", 5, []uint16{9, 8, 7, 6, 5}, []uint16{1, 2, 3, 4, 5}, 1},
		{"two pairs per point", 3, []uint16{9, 7, 5}, []uint16{1, 3, 5}, 2},
		{"one point", 1, []uint16{9}, []uint16{1}, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mx, mn, b := reducePairs(words, tt.points, nil, nil)
			if !slices.Equal(mx, tt.max) || !slices.Equal(mn, tt.min) || b != tt.bucket {
				t.Errorf("got max %v min %v bucket %d, want %v %v %d", mx, mn, b, tt.max, tt.min, tt.bucket)
			}
		})
	}
}

func TestReducePairsOddTail(t *testing.T) {
	// A trailing word without its partner is not a pair.
	mx, mn, _ := reducePairs([]uint16{4, 2, 7}, 0, make([]uint16, 8), nil)
	if !slices.Equal(mx, []uint16{4}) || !slices.Equal(mn, []uint16{2}) {
		t.Errorf("got %v %v", mx, mn)
	}
}
