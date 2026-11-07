package acq

import (
	"errors"
	"fmt"
	"syscall"
	"time"
	"unsafe"

	"github.com/labeth/open-sds800xhd/app/internal/board"
	"github.com/labeth/open-sds800xhd/app/internal/mmio"
)

// The sample-data region and its limits (spec 03 §4.4, spec 14 §7).
const (
	rawPhys    = 0x1E80_0000 // the rawdata pool the AXI DMA writes
	rawMapSize = 0x70_0000   // the 7 MiB window of /dev/udmabuf0
	maxBlock   = 7_000_000   // bytes per DMA block
	dmaTimeout = 3 * time.Second
)

// DMA is the record readout path: the AXI DMA (S2MM), the uncached
// udmabuf0 window and the completion interrupt node.
type DMA struct {
	acq *mmio.Plane
	reg mmio.Bus
	buf []byte
	irq int
}

// OpenDMA opens the readout path. udmabuf0 is opened O_SYNC, which gives an
// uncached mapping (the device tree sets sync-mode 1), so no cache
// invalidate is needed after a transfer (spec 03 §4.4 option 1).
func OpenDMA(b *board.Board) (*DMA, error) {
	fd, err := syscall.Open("/dev/udmabuf0", syscall.O_RDWR|syscall.O_SYNC|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("udmabuf0: %w", err)
	}
	buf, err := syscall.Mmap(fd, 0, rawMapSize, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	_ = syscall.Close(fd) // the mapping holds its own reference
	if err != nil {
		return nil, fmt.Errorf("mmap udmabuf0: %w", err)
	}
	irq, err := syscall.Open("/dev/dmadata_irq", syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		_ = syscall.Munmap(buf)
		return nil, fmt.Errorf("dmadata_irq: %w", err)
	}
	return &DMA{acq: b.ACQ, reg: b.AXIDMA, buf: buf, irq: irq}, nil
}

// Close releases the readout path.
func (d *DMA) Close() error {
	return errors.Join(syscall.Munmap(d.buf), syscall.Close(d.irq))
}

type pollFd struct {
	fd      int32
	events  int16
	revents int16
}

// ready waits up to t for the completion interrupt. It uses ppoll: the
// interrupt node reports POLLPRI|POLLRDNORM once per interrupt (spec 14 §3),
// and the Go poller is not used on device nodes.
func (d *DMA) ready(t time.Duration) bool {
	fds := [1]pollFd{{fd: int32(d.irq), events: 0x2 | 0x1 | 0x40}} //nolint:gosec // G115: a file descriptor of this process fits pollfd's int32
	ts := syscall.NsecToTimespec(int64(t))
	n, _, e := syscall.Syscall6(syscall.SYS_PPOLL, uintptr(unsafe.Pointer(&fds[0])), 1, //nolint:gosec // G103: the system call takes its argument by address
		uintptr(unsafe.Pointer(&ts)), 0, 0, 0) //nolint:gosec // G103: the system call takes its argument by address
	return e == 0 && n > 0 && fds[0].revents != 0
}

func (d *DMA) drain() {
	for range 16 {
		if !d.ready(0) {
			return
		}
	}
}

func (d *DMA) reset() {
	d.reg.Write32(board.S2MMCR, d.reg.Read32(board.S2MMCR)|4)
	time.Sleep(20 * time.Microsecond)
}

// block moves one block of n bytes into dst, or leaves them in the
// window for the caller when dst is nil (spec 03 §4.4). A timeout is a
// failed read: the DMA is reset and the stale completion drained.
func (d *DMA) block(dst []byte, n int) error {
	w8 := uint32((n + 7) / 8) //nolint:gosec // G115: a DMA block is at most maxBlock (7 MB)
	return d.Transfer(dst, n, func() {
		d.acq.Set(0x0CF, uint16(w8)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		d.acq.Set(0x0D0, uint16(w8>>16)&0xFF)
		d.acq.Set(0x0C8, 0, 1)
	})
}

// Transfer programs the S2MM for one block of n bytes into the rawdata
// window, calls start to make the PL send it (the vendor image's read
// strobe, or the open image's readout start), waits for the completion and
// copies the block into dst (nil: it stays in the window).
// TRLC-LINKS: REQ-HD-005
func (d *DMA) Transfer(dst []byte, n int, start func()) error {
	if n <= 0 || n > maxBlock {
		return fmt.Errorf("DMA block of %d bytes: 1 … %d", n, maxBlock)
	}
	n8 := (n + 7) &^ 7
	d.drain()
	d.reg.Write32(board.S2MMCR, d.reg.Read32(board.S2MMCR)&^1)
	d.reset()
	d.reg.Write32(board.S2MMDAMSB, 0)
	d.reg.Write32(board.S2MMDA, rawPhys)
	d.reg.Write32(board.S2MMCR, d.reg.Read32(board.S2MMCR)&^0x10|0x5001)
	d.reg.Write32(board.S2MMLen, uint32(n8)) //nolint:gosec // G115: a DMA block is at most maxBlock (7 MB)
	start()
	if !d.ready(dmaTimeout) {
		sr := d.reg.Read32(board.S2MMSR)
		d.reset()
		d.drain()
		return fmt.Errorf("DMA block of %d bytes timed out, status %#x", n8, sr)
	}
	if dst != nil {
		copy(dst, d.buf[:n])
	}
	return nil
}

// MaxBlock is the largest block one Transfer moves.
const MaxBlock = maxBlock
