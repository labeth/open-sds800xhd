// Package display is the SDS800X HD's UI layer and its screen capture
// (spec 07 §2–§5): /dev/fb0, an ARGB8888 1024×600 framebuffer the PL mixes
// over the waveform layer the map engine draws, and the PL's capture of the
// mixed picture, RGB565 in /dev/fb1.
package display

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/labeth/open-sds800xhd/app/internal/mmio"
)

// The panel and the UI layer's format (spec 07 §1, §2.1).
const (
	W      = 1024 // pixels per line
	H      = 600  // lines
	Stride = 4096 // bytes per fb0 line
)

// ARGB packs a straight-alpha pixel as fb0 holds it: a little-endian
// 0xAARRGGBB word. Alpha 0 shows the waveform layer, 0xFF the UI (spec 07
// §3.2).
func ARGB(a, r, g, b uint8) uint32 {
	return uint32(a)<<24 | uint32(r)<<16 | uint32(g)<<8 | uint32(b)
}

// Put stores pixel (x, y) in an fb0-format buffer.
func Put(buf []byte, x, y int, p uint32) {
	binary.LittleEndian.PutUint32(buf[y*Stride+4*x:], p)
}

// At loads pixel (x, y) of an fb0-format buffer.
func At(buf []byte, x, y int) uint32 {
	return binary.LittleEndian.Uint32(buf[y*Stride+4*x:])
}

// FB is the UI layer: a back buffer the renderer draws into and the
// mapped /dev/fb0 that Present copies it to. Back and Present belong to the
// render goroutine.
type FB struct {
	back []byte
	mem  []byte // the mapped framebuffer, Stride·H bytes
}

const (
	fbioGetVScreenInfo = 0x4600
	fbioGetFScreenInfo = 0x4602
)

func ioctl(fd int, req uintptr, arg unsafe.Pointer) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(arg)); e != 0 {
		return e
	}
	return nil
}

// fbInfo is what the framebuffer driver reports (struct fb_var_screeninfo
// and fb_fix_screeninfo, 64-bit layout).
type fbInfo struct {
	xres, yres, bpp      uint32
	red, green, blue, tr uint32 // bit offsets
	lineLength           uint32
}

func readInfo(fd int) (fbInfo, error) {
	var v [160]byte
	if err := ioctl(fd, fbioGetVScreenInfo, unsafe.Pointer(&v[0])); err != nil { //nolint:gosec // G103: the ioctl fills the buffer by address
		return fbInfo{}, fmt.Errorf("FBIOGET_VSCREENINFO: %w", err)
	}
	var f [80]byte
	if err := ioctl(fd, fbioGetFScreenInfo, unsafe.Pointer(&f[0])); err != nil { //nolint:gosec // G103: the ioctl fills the buffer by address
		return fbInfo{}, fmt.Errorf("FBIOGET_FSCREENINFO: %w", err)
	}
	le := binary.LittleEndian
	return fbInfo{
		xres: le.Uint32(v[0:]), yres: le.Uint32(v[4:]), bpp: le.Uint32(v[24:]),
		red: le.Uint32(v[32:]), green: le.Uint32(v[44:]), blue: le.Uint32(v[56:]), tr: le.Uint32(v[68:]),
		lineLength: le.Uint32(f[48:]),
	}, nil
}

// check refuses a framebuffer whose layout is not spec 07 §2.1's.
func (i fbInfo) check() error {
	switch {
	case i.bpp != 32 || i.red != 16 || i.green != 8 || i.blue != 0 || i.tr != 24:
		return fmt.Errorf("fb0 is %d bpp with RGBA offsets %d/%d/%d/%d, want ARGB8888", i.bpp, i.red, i.green, i.blue, i.tr)
	case i.xres < W || i.yres < H || i.lineLength != Stride:
		return fmt.Errorf("fb0 is %dx%d with %d-byte lines, want %dx%d with %d", i.xres, i.yres, i.lineLength, W, H, Stride)
	}
	return nil
}

// OpenFB maps the UI framebuffer at path (/dev/fb0).
func OpenFB(path string) (*FB, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = syscall.Close(fd) }()
	info, err := readInfo(fd)
	if err != nil {
		return nil, err
	}
	if err := info.check(); err != nil {
		return nil, err
	}
	mem, err := syscall.Mmap(fd, 0, Stride*H, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("mmap %s: %w", path, err)
	}
	return &FB{back: make([]byte, Stride*H), mem: mem}, nil
}

// NewFB returns a framebuffer over mem (Stride·H bytes) without a device:
// for tests and for a board whose framebuffer is plain memory.
func NewFB(mem []byte) *FB { return &FB{back: make([]byte, Stride*H), mem: mem} }

// Back is the back buffer, fb0's format.
func (f *FB) Back() []byte { return f.back }

// Stride is the bytes per line.
func (f *FB) Stride() int { return Stride }

// Present copies the back buffer to the framebuffer the PL scans out.
func (f *FB) Present() error {
	if len(f.mem) < len(f.back) {
		return errors.New("fb0: not mapped")
	}
	copy(f.mem, f.back)
	return nil
}

// Capture registers of the UI VDMA, S2MM side (spec 07 §4.2, PG020).
const (
	s2mmCR     = 0x30
	s2mmVSize  = 0xA0
	s2mmHSize  = 0xA4
	s2mmStride = 0xA8
	s2mmStart  = 0xAC

	captureBuf = 0x1F80_0000 // /dev/fb1
)

// Capturer reads the PL's capture of the mixed picture (spec 07 §5): the
// UI VDMA writes the mixer output to /dev/fb1 as RGB565, 1024×600.
type Capturer struct {
	mu   sync.Mutex
	vdma mmio.Bus
	mp   *mmio.Plane
	path string
	mem  []byte
	irq  int // /dev/siglent_vnc, -1 when it did not open
}

// FrameIRQ is the capture path's per-frame interrupt (spec 14 §6).
const FrameIRQ = "/dev/siglent_vnc"

// NewCapturer returns the capture over the UI VDMA, the MAP plane and the
// capture framebuffer at path (/dev/fb1).
func NewCapturer(vdma mmio.Bus, mp *mmio.Plane, path string) *Capturer {
	c := &Capturer{vdma: vdma, mp: mp, path: path, irq: -1}
	if fd, err := syscall.Open(FrameIRQ, syscall.O_RDONLY|syscall.O_CLOEXEC, 0); err == nil {
		c.irq = fd
	}
	return c
}

// waitFrame waits for the capture of a whole frame that started after the
// call: the frame interrupt's ready flag may be left from an older frame,
// and the frame in flight may have begun earlier, so two interrupts are
// waited for (poll clears the flag, spec 14 §6). Without the interrupt node
// it sleeps for two frames of the slowest capture rate.
func (c *Capturer) waitFrame() {
	if c.irq < 0 {
		time.Sleep(250 * time.Millisecond)
		return
	}
	for range 2 {
		pollPri(c.irq, 500*time.Millisecond)
	}
}

// pollPri waits for POLLPRI on fd at most d.
func pollPri(fd int, d time.Duration) bool {
	const pollPRI = 0x2
	pfd := struct {
		fd             int32
		events, revent int16
	}{int32(fd), pollPRI, 0} //nolint:gosec // G115: a file descriptor of this process fits pollfd's int32
	ts := syscall.NsecToTimespec(int64(d))
	n, _, e := syscall.Syscall6(syscall.SYS_PPOLL, uintptr(unsafe.Pointer(&pfd)), 1, uintptr(unsafe.Pointer(&ts)), 0, 0, 0) //nolint:gosec // G103: the system call takes its argument by address
	return e == 0 && n == 1 && pfd.revent&pollPRI != 0
}

// running reports whether the S2MM channel runs into the capture buffer.
func (c *Capturer) running() bool {
	return c.vdma.Read32(s2mmCR)&1 != 0 && c.vdma.Read32(s2mmStart) == captureBuf
}

// startPath is the capture start of spec 07 §4.2 (the vendor boot normally
// leaves it running).
func (c *Capturer) startPath() {
	c.vdma.Write32(s2mmCR, 0x4)
	c.vdma.Write32(s2mmCR, 0x0001_8003)
	c.vdma.Write32(s2mmStart, captureBuf)
	c.vdma.Write32(s2mmStride, 0x800)
	c.vdma.Write32(s2mmHSize, 0x800)
	c.vdma.Write32(s2mmVSize, H)
	c.mp.Set(0x158, 0, 1)
	c.mp.Set(0x1B0, 1)
}

// Capture copies the latest captured picture into dst (W×H).
func (c *Capturer) Capture(dst *image.RGBA) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if dst.Rect.Dx() < W || dst.Rect.Dy() < H {
		return fmt.Errorf("capture: destination %v smaller than %dx%d", dst.Rect, W, H)
	}
	if !c.running() {
		c.startPath()
	}
	c.waitFrame()
	if c.mem == nil {
		fd, err := syscall.Open(c.path, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("open %s: %w", c.path, err)
		}
		mem, err := syscall.Mmap(fd, 0, 0x800*H, syscall.PROT_READ, syscall.MAP_SHARED)
		_ = syscall.Close(fd) // the mapping holds its own reference
		if err != nil {
			return fmt.Errorf("mmap %s: %w", c.path, err)
		}
		c.mem = mem
	}
	stride := int(c.vdma.Read32(s2mmStride) & 0xFFFF)
	if stride < 2*W || stride*H > len(c.mem) {
		stride = 2 * W
	}
	FromRGB565(dst, c.mem, stride)
	return nil
}

// FromRGB565 converts a W×H RGB565 picture with stride bytes per line into
// dst, opaque.
func FromRGB565(dst *image.RGBA, src []byte, stride int) {
	o := dst.Rect.Min
	for y := range H {
		row := src[y*stride:]
		for x := range W {
			v := uint16(row[2*x]) | uint16(row[2*x+1])<<8
			r, g, b := uint8(v>>11)&0x1F, uint8(v>>5)&0x3F, uint8(v)&0x1F //nolint:gosec // G115: the masked fields of an RGB565 pixel
			dst.SetRGBA(o.X+x, o.Y+y, color.RGBA{r<<3 | r>>2, g<<2 | g>>4, b<<3 | b>>2, 0xFF})
		}
	}
}
