// Package mmiotest is a register-block fake for tests: it records every write
// and answers reads from a programmable status table.
package mmiotest

import (
	"slices"
	"sync"
)

// Write is one recorded store.
type Write struct {
	Off uint32
	V   uint32
}

// Bus is a fake mmio.Bus. Reads return Status[off] if set, else the last
// value written.
type Bus struct {
	mu     sync.Mutex
	Writes []Write
	Status map[uint32]func() uint32
	mem    map[uint32]uint32
}

// New returns an empty fake bus.
func New() *Bus { return &Bus{Status: map[uint32]func() uint32{}, mem: map[uint32]uint32{}} }

// Read32 implements mmio.Bus.
func (b *Bus) Read32(off uint32) uint32 {
	b.mu.Lock()
	f := b.Status[off]
	v := b.mem[off]
	b.mu.Unlock()
	if f != nil {
		return f()
	}
	return v
}

// Write32 implements mmio.Bus.
func (b *Bus) Write32(off, v uint32) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Writes = append(b.Writes, Write{off, v})
	b.mem[off] = v
}

// Last returns the last value written at off and whether one was.
func (b *Bus) Last(off uint32) (uint32, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, v := range slices.Backward(b.Writes) {
		if v.Off == off {
			return v.V, true
		}
	}
	return 0, false
}
