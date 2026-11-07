// Package mmio is register access to the programmable logic and the PS
// peripherals through /dev/mem (spec 02 §1).
//
// The PL is reached through 64 KiB windows in the AXI HPM range at
// 0x8000_0000. The two register planes (ACQ, MAP) hold 16-bit registers at
// base + 4·idx; the AXI DMA and the VDMAs are plain 32-bit register blocks.
// Every access is one aligned 32-bit load or store, never merged, split or
// elided: many registers are strobes whose effect is the write itself.
package mmio

import (
	"fmt"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/labeth/open-sds800xhd/ota/fsguard"
)

// Physical bases of the blocks the firmware uses (spec 02 §2).
const (
	PhysUIVDMA   = 0x8000_0000 // UI VDMA: UI framebuffer → LCD, screen capture
	PhysAXIDMA   = 0x8001_0000 // AXI DMA (S2MM): record readout
	PhysWaveVDMA = 0x8002_0000 // waveform VDMA
	PhysACQ      = 0x8003_0000 // ACQ register plane
	PhysMAP      = 0x8004_0000 // MAP register plane
	PhysTTC0     = 0xFF11_0000 // PS triple-timer counter 0 (beeper PWM)

	// WindowSize is the size of one mapping; every block fits in one.
	WindowSize = 0x1_0000
)

// Bus is a block of 32-bit registers addressed by byte offset.
type Bus interface {
	Read32(off uint32) uint32
	Write32(off, v uint32)
}

// Window is one /dev/mem mapping of a register block.
type Window struct {
	Phys uint64
	mem  []byte
}

// Map maps the 64 KiB block at phys. Each window opens /dev/mem itself, so
// windows do not share one file descriptor (the test environment serialises
// accesses per descriptor).
// The write guard refuses a window over the eFuse, BBRAM, CSU or SD/eMMC
// controllers.
// TRLC-LINKS: REQ-HD-016
func Map(phys uint64) (*Window, error) {
	if err := fsguard.CheckPhys(phys, WindowSize); err != nil {
		return nil, err
	}
	fd, err := syscall.Open("/dev/mem", syscall.O_RDWR|syscall.O_SYNC|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/mem: %w", err)
	}
	defer func() { _ = syscall.Close(fd) }()
	mem, err := syscall.Mmap(fd, int64(phys), WindowSize, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED) //nolint:gosec // G115: physical addresses on this SoC are 32-bit
	if err != nil {
		return nil, fmt.Errorf("mmap %#x: %w", phys, err)
	}
	return &Window{Phys: phys, mem: mem}, nil
}

// Close unmaps the window.
func (w *Window) Close() error {
	if w.mem == nil {
		return nil
	}
	err := syscall.Munmap(w.mem)
	w.mem = nil
	return err
}

func (w *Window) ptr(off uint32) *uint32 {
	if off&3 != 0 || off >= WindowSize {
		panic(fmt.Sprintf("mmio: bad offset %#x in window %#x", off, w.Phys))
	}
	return (*uint32)(unsafe.Pointer(&w.mem[off])) //nolint:gosec // G103: a register in the block mapped from /dev/mem
}

// Read32 is one aligned 32-bit load.
func (w *Window) Read32(off uint32) uint32 { return atomic.LoadUint32(w.ptr(off)) }

// Write32 is one aligned 32-bit store.
func (w *Window) Write32(off, v uint32) { atomic.StoreUint32(w.ptr(off), v) }
