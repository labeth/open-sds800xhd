//go:build simboard

package boardtest

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-sds800xhd/app/internal/platform"
	"github.com/labeth/open-sds800xhd/app/internal/scopeboard"
)

// board is brought up once for every test of the binary.
var (
	board  *scopeboard.Board
	ident  hal.Identity
	logger *platform.Log
)

func TestMain(m *testing.M) {
	var err error
	logger, _ = platform.OpenLog("boardtest")
	board = scopeboard.New(logger)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	ident, err = board.Platform().BringUp(ctx)
	cancel()
	if err != nil {
		fmt.Println("RESULT platform/bring-up FAIL", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// tcase is one reported case.
type tcase struct {
	t  *testing.T
	id string
}

// begin marks the start of case AREA/NAME in the simulator's log.
func begin(t *testing.T, id string) *tcase {
	t.Helper()
	platform.Mark("case " + id)
	return &tcase{t: t, id: id}
}

// check reports one assertion of the guest.
func (c *tcase) check(ok bool, format string, a ...any) bool {
	c.t.Helper()
	d := fmt.Sprintf(format, a...)
	v := "PASS"
	if !ok {
		v = "FAIL"
		c.t.Errorf("%s: %s", c.id, d)
	}
	fmt.Printf("RESULT %s %s %s\n", c.id, v, d)
	return ok
}

// noErr reports err as the case's result.
func (c *tcase) noErr(err error, what string) bool {
	c.t.Helper()
	if err != nil {
		return c.check(false, "%s: %v", what, err)
	}
	return c.check(true, "%s", what)
}

// refused reports whether err is a *Refusal, and its reason.
func (c *tcase) refused(err error, what string) bool {
	c.t.Helper()
	r, ok := hal.AsRefusal(err)
	if !ok {
		return c.check(false, "%s: want a refusal, got %v", what, err)
	}
	return c.check(true, "%s refused: %s", what, r.Reason)
}

// expect asks the harness to check the last value written to each register
// in this case's window. Keys are "acq:0x0E" or "map:0x30".
func (c *tcase) expect(regs map[string]uint16) {
	keys := make([]string, 0, len(regs))
	for k := range regs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, " %s=%#x", k, regs[k])
	}
	fmt.Printf("EXPECT %s%s\n", c.id, b.String())
}

// expectSPI asks the harness to find an SPI frame to chip in the window.
func (c *tcase) expectSPI(chip string, tx []byte) {
	fmt.Printf("EXPECTSPI %s %s %x\n", c.id, chip, tx)
}
