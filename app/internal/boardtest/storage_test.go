//go:build simboard

package boardtest

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestStorage checks the settings store and the setup slots on p12: a
// value written is read back, a value past the budget is refused and not
// stored, and the captures directory is empty without a USB stick.
func TestStorage(t *testing.T) {
	s := board.Storage()
	c := begin(t, "storage/settings")
	kv := s.Settings()
	val := []byte(`{"tdiv":2e-7}`)
	c.noErr(kv.Put("boardtest", val), "put")
	got, ok := kv.Get("boardtest")
	c.check(ok && bytes.Equal(got, val), "read back %q", got)
	_, ok = kv.Get("never-written")
	c.check(!ok, "a key never written is absent")

	c = begin(t, "storage/budget")
	err := kv.Put("too-big", make([]byte, kv.Budget()+1))
	c.refused(err, fmt.Sprintf("%d bytes past the %d-byte budget", kv.Budget()+1, kv.Budget()))
	_, ok = kv.Get("too-big")
	c.check(!ok, "the refused value is not stored")

	c = begin(t, "storage/setups")
	st, ok := s.Setups()
	if c.check(ok, "setup slots present") {
		c.noErr(st.Put("1", []byte("setup one")), "slot 1")
		got, _ := st.Get("1")
		c.check(string(got) == "setup one", "slot 1 reads %q", got)
		c.refused(st.Put("0", nil), "slot 0")
		c.refused(st.Put("21", nil), "slot 21")
	}

	c = begin(t, "storage/captures")
	dir, err := s.Captures()
	c.check(err == nil && dir == "", "no USB stick in the simulator: captures dir %q, %v", dir, err)
	c.check(s.Calibration().Source != "", "calibration source %q", s.Calibration().Source)
}

// TestStorageWrite and TestStorageRead are the two runs of the restart
// check: the first writes a token, the second (same p12) reads it. The
// harness compares the token the first printed with what the second read.
func TestStorageWrite(t *testing.T) {
	c := begin(t, "storage/restart-write")
	tok := fmt.Sprintf("token-%d-%d", time.Now().UnixNano(), os.Getpid())
	if c.noErr(board.Storage().Settings().Put("restart", []byte(tok)), "write the token") {
		fmt.Printf("TOKEN written %s\n", tok)
	}
}

func TestStorageRead(t *testing.T) {
	c := begin(t, "storage/restart-read")
	v, ok := board.Storage().Settings().Get("restart")
	c.check(ok && strings.HasPrefix(string(v), "token-"), "token after the restart: %q", v)
	fmt.Printf("TOKEN read %s\n", v)
}
