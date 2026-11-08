package scopeboard

import (
	"fmt"
	"testing"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-sds800xhd/app/internal/panel"
)

// The key table is the board's translation: with each channel selected,
// every key's press and release and every knob's turn reach the UI model
// as the table says, and a channel key selects as the board does.
func TestKeyTableIsTheBoards(t *testing.T) {
	tab := KeyTable()
	if err := tab.CheckProfile(inputSpec()); err != nil {
		t.Fatal(err)
	}
	for sel := 1; sel <= 4; sel++ {
		for _, k := range panel.Keys() {
			n := &names{sel: sel - 1}
			var board []hal.InputEvent
			for _, down := range []bool{true, false} {
				if ev, ok := n.translate(hal.InputEvent{Source: hal.FromPanel, Kind: hal.KeyEvent, Key: k, Down: down}); ok {
					board = append(board, ev)
				}
			}
			e, ok := tab.Key(k)
			if !ok {
				t.Fatalf("key %s is not in the table", k)
			}
			got, after := e.Press(sel)
			if fmt.Sprint(got) != fmt.Sprint(board) || after != n.sel+1 {
				t.Errorf("C%d %s: the table sends %+v and selects C%d, the board %+v and C%d", sel, k, got, after, board, n.sel+1)
			}
		}
		for _, k := range panel.Knobs() {
			n := &names{sel: sel - 1}
			for _, steps := range []int{1, -1, 7, -255} {
				ev, _ := n.translate(hal.InputEvent{Source: hal.FromPanel, Kind: hal.KnobEvent, Knob: k, Steps: steps})
				e, _ := tab.Knob(k)
				if got := e.Turn(steps, sel); len(got) != 1 || got[0] != ev {
					t.Errorf("C%d %s %+d: the table sends %+v, the board %+v", sel, k, steps, got, ev)
				}
			}
		}
	}
}
