// Package board holds the register blocks of the SDS800X HD's programmable
// logic and the PS peripherals the app drives, and the hardware half of the
// takeover from the vendor application.
//
// The PL must be configured and clocked before any of these windows is
// accessed: an access to an unconfigured PL stalls the AXI interconnect and
// hangs the CPU (spec 01 §13). On the stage-1 path the vendor boot scripts
// have done both before the app starts, and the app never reloads the PL.
package board

import (
	"errors"
	"time"

	"github.com/labeth/open-sds800xhd/app/internal/mmio"
)

// Board is the mapped register blocks.
type Board struct {
	ACQ, MAP *mmio.Plane
	AXIDMA   mmio.Bus // record readout DMA (S2MM)
	UIVDMA   mmio.Bus // UI framebuffer → LCD
	TTC0     mmio.Bus // beeper timer

	windows []*mmio.Window
}

// Open maps every block. Mapping does not access the bus.
func Open() (*Board, error) {
	b := &Board{}
	var ws [5]*mmio.Window
	for i, phys := range []uint64{mmio.PhysACQ, mmio.PhysMAP, mmio.PhysAXIDMA, mmio.PhysUIVDMA, mmio.PhysTTC0} {
		w, err := mmio.Map(phys)
		if err != nil {
			_ = b.Close()
			return nil, err
		}
		ws[i] = w
		b.windows = append(b.windows, w)
	}
	b.ACQ = mmio.NewPlane("acq", ws[0])
	b.MAP = mmio.NewPlane("map", ws[1])
	b.AXIDMA, b.UIVDMA, b.TTC0 = ws[2], ws[3], ws[4]
	return b, nil
}

// Close unmaps every block.
func (b *Board) Close() error {
	var errs []error
	for _, w := range b.windows {
		errs = append(errs, w.Close())
	}
	b.windows = nil
	return errors.Join(errs...)
}

// AXI DMA S2MM registers (spec 02 §5.2).
const (
	S2MMCR    = 0x30
	S2MMSR    = 0x34
	S2MMDA    = 0x48
	S2MMDAMSB = 0x4C
	S2MMLen   = 0x58
)

// UI VDMA MM2S registers (spec 13 §5.1).
const (
	mm2sVSize = 0x50
	mm2sStart = 0x5C
	uiLines   = 0x258 // 600 lines; the VSIZE write latches the start address
)

// FB0 is the physical address of /dev/fb0's memory, the frame the app
// draws its UI into (spec 01 §4, spec 07 §2).
const FB0 = 0x1FC0_0000

// ttcCounterControl1 is TTC0 Counter_Control_1; bit 0 disables the
// counter, which silences the beeper (spec 08 §8).
const ttcCounterControl1 = 0x0C

// Quiesce runs steps 4–6 of the takeover (spec 01 §12.2) once the vendor
// processes are gone: the beeper is silenced, acquisition and the sample DMA
// are halted, and the UI layer shows /dev/fb0. Nothing the vendor left is
// relied on afterwards; the start-up re-initialises everything.
func (b *Board) Quiesce() {
	// 4. Beeper off: a read-modify-write of a PS peripheral, which has no
	// shadow of ours yet.
	b.TTC0.Write32(ttcCounterControl1, b.TTC0.Read32(ttcCounterControl1)|1)

	// 5. Stop request (the other bits of 0x019 are rewritten by the
	// start-up), DMA reset, then clear its status.
	b.ACQ.Set(0x019, 0x0008)
	b.AXIDMA.Write32(S2MMCR, 0x4)
	time.Sleep(20 * time.Microsecond)
	b.AXIDMA.Write32(S2MMSR, 0xFFFF_FFFF)

	// 6. The UI layer scans out /dev/fb0.
	b.UIVDMA.Write32(mm2sStart, FB0)
	b.UIVDMA.Write32(mm2sVSize, uiLines)
}
