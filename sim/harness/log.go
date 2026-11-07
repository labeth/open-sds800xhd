// Package harness runs the app and the in-guest board tests in the tier-1
// simulator of the private reference workspace and checks what reached the
// simulated hardware: the device model's JSONL log of every register access,
// SPI frame and control command, the console of the guest, and the files the
// guest left on its partitions.
package harness

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Event is one line of the simulator's log, the fields the checks use.
type Event struct {
	Ev   string          `json:"ev"`
	Seq  int64           `json:"seq"`
	T    float64         `json:"t"`
	Exe  string          `json:"exe"`
	Text string          `json:"text"` // mark
	Cmd  string          `json:"cmd"`  // ctl
	Op   string          `json:"op"`   // mmio r/w, spi write/read/msg
	Addr string          `json:"addr"` // mmio physical address
	Blk  string          `json:"blk"`  // mmio block
	Val  json.RawMessage `json:"val"`
	Chip string          `json:"chip"` // spi
	Bus  int             `json:"bus"`
	CS   int             `json:"cs"`
	Tx   string          `json:"tx"`
	Kind string          `json:"kind"`
}

// Address returns an mmio event's physical address.
func (e *Event) Address() uint64 {
	v, _ := strconv.ParseUint(strings.TrimPrefix(e.Addr, "0x"), 16, 64)
	return v
}

// Value returns the event's val field as a number (hex strings or numbers).
func (e *Event) Value() uint64 {
	s := strings.Trim(string(e.Val), `"`)
	if strings.HasPrefix(s, "0x") {
		v, _ := strconv.ParseUint(s[2:], 16, 64)
		return v
	}
	v, _ := strconv.ParseUint(s, 10, 64)
	return v
}

// Plane bases (spec 02 §2): register n of a plane is at base + 4·n.
const (
	BaseACQ = 0x8003_0000
	BaseMAP = 0x8004_0000
)

// Reg names a register of a plane: "acq" or "map" and its index.
type Reg struct {
	Plane string
	Index int
}

func (r Reg) String() string { return fmt.Sprintf("%s:%#03x", r.Plane, r.Index) }

// ParseReg parses "acq:0x0E" or "map:0x30".
func ParseReg(s string) (Reg, error) {
	p, i, ok := strings.Cut(s, ":")
	if !ok || (p != "acq" && p != "map") {
		return Reg{}, fmt.Errorf("bad register %q", s)
	}
	n, err := strconv.ParseUint(strings.TrimPrefix(i, "0x"), 16, 16)
	if err != nil {
		return Reg{}, fmt.Errorf("bad register %q", s)
	}
	return Reg{p, int(n)}, nil
}

// RegOf returns the plane register an mmio event touches.
func RegOf(e *Event) (Reg, bool) {
	a := e.Address()
	switch {
	case a >= BaseACQ && a < BaseACQ+0x1_0000:
		return Reg{"acq", int(a-BaseACQ) / 4}, true
	case a >= BaseMAP && a < BaseMAP+0x1_0000:
		return Reg{"map", int(a-BaseMAP) / 4}, true
	}
	return Reg{}, false
}

// Log is a run's events in order.
type Log struct{ Events []Event }

// ReadLog reads a JSONL log. Events of other kinds than the checks use are
// kept with their common fields only.
func ReadLog(path string) (*Log, error) {
	f, err := os.Open(path) //nolint:gosec // G304: a run's log in the simulator workspace
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // read only: nothing to flush
	l := &Log{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		switch e.Ev {
		case "mmio", "spi", "mark", "ctl", "shot", "key", "touch", "dma", "fb", "irq":
			l.Events = append(l.Events, e)
		}
	}
	return l, sc.Err()
}

// Window is the events between the mark whose text equals from (or has it
// as prefix when it ends in "*") and the next mark with the prefix next;
// the rest of the log if no such mark follows.
func (l *Log) Window(from, next string) []Event {
	start := -1
	for i, e := range l.Events {
		if e.Ev != "mark" {
			continue
		}
		if start < 0 {
			if e.Text == from {
				start = i + 1
			}
			continue
		}
		if strings.HasPrefix(e.Text, next) {
			return l.Events[start:i]
		}
	}
	if start < 0 {
		return nil
	}
	return l.Events[start:]
}

// Marks returns the texts of every mark with the prefix.
func (l *Log) Marks(prefix string) []string {
	var out []string
	for _, e := range l.Events {
		if e.Ev == "mark" && strings.HasPrefix(e.Text, prefix) {
			out = append(out, e.Text)
		}
	}
	return out
}

// Writes are the plane register writes in events, in order.
func Writes(events []Event) []RegWrite {
	var out []RegWrite
	for i := range events {
		e := &events[i]
		if e.Ev != "mmio" || e.Op != "w" {
			continue
		}
		if r, ok := RegOf(e); ok {
			out = append(out, RegWrite{r, uint16(e.Value())}) //nolint:gosec // G115: the device model logs 16-bit register writes
		}
	}
	return out
}

// RegWrite is one register write.
type RegWrite struct {
	Reg Reg
	Val uint16
}

// Final is the last written value of every plane register in events.
func Final(events []Event) map[Reg]uint16 {
	m := map[Reg]uint16{}
	for _, w := range Writes(events) {
		m[w.Reg] = w.Val
	}
	return m
}

// SPIWrites are the transmitted frames per chip in events, in order.
func SPIWrites(events []Event) map[string][]string {
	m := map[string][]string{}
	for _, e := range events {
		if e.Ev == "spi" && (e.Op == "write" || e.Op == "msg") && e.Tx != "" {
			m[e.Chip] = append(m[e.Chip], e.Tx)
		}
	}
	return m
}

// SortedRegs returns the keys of m in plane and index order.
func SortedRegs(m map[Reg]uint16) []Reg {
	rs := make([]Reg, 0, len(m))
	for r := range m {
		rs = append(rs, r)
	}
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].Plane != rs[j].Plane {
			return rs[i].Plane < rs[j].Plane
		}
		return rs[i].Index < rs[j].Index
	})
	return rs
}

// FinalThrough is the last written value of every plane register from the
// start of the log through the end of the window Window(from, next) gives:
// the state the hardware holds when the window ends. ok is false without
// the window's mark.
func (l *Log) FinalThrough(from, next string) (m map[Reg]uint16, ok bool) {
	end := -1
	for i, e := range l.Events {
		if e.Ev != "mark" {
			continue
		}
		if end < 0 && !ok {
			if e.Text == from {
				ok = true
			}
			continue
		}
		if ok && strings.HasPrefix(e.Text, next) {
			end = i
			break
		}
	}
	if !ok {
		return nil, false
	}
	if end < 0 {
		end = len(l.Events)
	}
	return Final(l.Events[:end]), true
}
