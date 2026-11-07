// Package spi is the front-end SPI bus (PS SPI1): clock chip, the two ADCs,
// the relay shift register and the four VGAs share one controller with one
// chip select each (spec 06 §2).
package spi

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

// Chip selects of SPI1 (spec 06 §2).
const (
	CSPLL   = 0
	CSADC0  = 1
	CSADC1  = 2 // absent on 2-channel models: never opened there (spec 06 §9.1)
	CSRelay = 3 // 74HC595 chain, 48 bits
	CSVGA1  = 4 // the VGA of channel c is CSVGA1 + c
)

const (
	iocWrMode   = 0x40016B01 // SPI_IOC_WR_MODE
	iocMessage1 = 0x40206B00 // SPI_IOC_MESSAGE(1)
)

// iocTransfer is struct spi_ioc_transfer (32 bytes).
type iocTransfer struct {
	txBuf, rxBuf            uint64
	length, speedHz         uint32
	delayUsecs              uint16
	bitsPerWord, csChange   uint8
	txNbits, rxNbits        uint8
	wordDelayUsecs, padding uint8
}

// Bus serialises every access to the shared controller. A frame on a chip
// select is one write(2) of its bytes; reads (ADC12DL1500 registers) are one
// full-duplex message.
type Bus struct {
	mu  sync.Mutex
	fds map[int]int
	log func(string, ...any)
}

// New returns a bus that opens /dev/spidev1.<cs> on first use; errors go to
// log (an SPI frame that fails is logged, not retried).
func New(log func(string, ...any)) *Bus { return &Bus{fds: map[int]int{}, log: log} }

func (b *Bus) fd(cs int) (int, error) {
	if fd, ok := b.fds[cs]; ok {
		return fd, nil
	}
	fd, err := syscall.Open(fmt.Sprintf("/dev/spidev1.%d", cs), syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	mode := uint8(0)
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), iocWrMode, uintptr(unsafe.Pointer(&mode))); e != 0 { //nolint:gosec // G103: the system call takes its argument by address
		_ = syscall.Close(fd)
		return -1, fmt.Errorf("spidev1.%d mode: %w", cs, e)
	}
	b.fds[cs] = fd
	return fd, nil
}

// Write sends one frame on chip select cs.
func (b *Bus) Write(cs int, tx ...byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	fd, err := b.fd(cs)
	if err == nil {
		_, err = syscall.Write(fd, tx)
	}
	if err != nil {
		b.log("spi cs%d: %v", cs, err)
	}
}

// Xfer is one full-duplex frame; it returns the bytes clocked in.
func (b *Bus) Xfer(cs int, tx []byte) []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	rx := make([]byte, len(tx))
	fd, err := b.fd(cs)
	if err != nil {
		b.log("spi cs%d: %v", cs, err)
		return rx
	}
	t := iocTransfer{
		txBuf: uint64(uintptr(unsafe.Pointer(&tx[0]))), rxBuf: uint64(uintptr(unsafe.Pointer(&rx[0]))), //nolint:gosec // G103: the spidev transfer names its buffers by address; the buffers outlive the ioctl
		length: uint32(len(tx)), speedHz: 1_000_000, delayUsecs: 1, bitsPerWord: 8, //nolint:gosec // G115: a transfer of a few bytes
	}
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), iocMessage1, uintptr(unsafe.Pointer(&t))); e != 0 { //nolint:gosec // G103: the system call takes its argument by address
		b.log("spi cs%d message: %v", cs, e)
	}
	runtime.KeepAlive(tx)
	return rx
}
