// Package openpl drives the open PL design (spec 20, stage 2): the open
// register plane at 0x8010_0000 (constants generated from
// fpga/regs/open-pl.yml into regs_gen.go), the stacker bins at
// 0x8011_0000 and the record and stream rings in the DDR region outside
// Linux (spec 20 §6.1). The compat ACQ plane and the AXI DMA keep their
// stage-1 drivers (mmio, acq.DMA).
//
// A board talks to the open plane only after Detect has seen the open
// image's mark in compat ACQ 0x002, a word both images decode, so no access
// reaches an address the vendor image does not answer (spec 20 §10.2).
package openpl

import (
	"errors"
	"fmt"

	"github.com/labeth/open-sds800xhd/app/internal/mmio"
)

// Physical addresses of the open image (spec 20 §4.2, §6.1).
const (
	PhysOpen  = Base        // the open plane
	PhysBins  = 0x8011_0000 // stacker bin memory window
	RecBase   = 0x2000_0000 // record rings
	RecBytes  = 0x1C00_0000 // up to the stream ring
	RingBase  = 0x3C00_0000 // stream ring
	RingBytes = 0x0400_0000
	Magic     = 0x4F50_4C31 // "OPL1"
	// TickS is one tick, the time unit of every trigger time (spec 20 §5.2).
	TickS = 0.5e-9
	// CclkHz is the sample core's clock (spec 20 §3).
	CclkHz = 250e6
)

// ID_CAPS bits (spec 20 §10.3).
const (
	CapAcquisition = 1 << iota
	CapInterval
	CapNth
	CapVideo
	CapZone
	CapUART
	CapSPI
	CapI2C
	CapCAN
	CapSymbol
	CapSequence
	CapStream
	CapStacker
	CapReducer
	CapCounter
	CapInterp
	CapSegments
)

// ErrNotOpen is a PL that is not the open image.
var ErrNotOpen = errors.New("the PL is not the open image")

// Detect reports whether compat ACQ 0x002 carries the open image's mark
// (bits 15:8 = 0x4F; the vendor image reads 0 there, 0xBD on board-test
// images).
func Detect(acq *mmio.Plane) bool { return acq.HW(0x002)>>8 == 0x4F }

// Identity is what the image says about itself.
type Identity struct {
	ABIMajor, ABIMinor int
	Image              string // four characters, e.g. "full"
	Build              uint32
	Manifest           uint64
	Caps               uint32
	Lanes, Channels    int
	TickPS             int
	DNA                [3]uint32
}

// PL is the open image's register windows.
type PL struct {
	r, bins mmio.Bus
	win     []*mmio.Window
	ID      Identity
}

// Open maps the open plane and the bin window and checks the identity
// against the ABI this firmware was built for: the magic, the ABI major and
// the manifest hash of the register YAML. Any mismatch is an error and the
// caller must not use the image (spec 20 §12).
func Open() (*PL, error) {
	p := &PL{}
	for _, phys := range []uint64{PhysOpen, PhysBins} {
		w, err := mmio.Map(phys)
		if err != nil {
			_ = p.Close()
			return nil, err
		}
		p.win = append(p.win, w)
	}
	p.r, p.bins = p.win[0], p.win[1]
	if err := p.identify(); err != nil {
		_ = p.Close()
		return nil, err
	}
	return p, nil
}

// New is a PL on given buses (tests).
func New(r, bins mmio.Bus) (*PL, error) {
	p := &PL{r: r, bins: bins}
	return p, p.identify()
}

// Close unmaps the windows.
func (p *PL) Close() error {
	var errs []error
	for _, w := range p.win {
		errs = append(errs, w.Close())
	}
	p.win = nil
	return errors.Join(errs...)
}

func (p *PL) identify() error {
	if m := p.r.Read32(IDMagic); m != Magic {
		return fmt.Errorf("%w: ID_MAGIC %#08x", ErrNotOpen, m)
	}
	abi := p.r.Read32(IDABI)
	id := Identity{
		ABIMajor: int(abi >> 16), ABIMinor: int(abi & 0xFFFF), Build: p.r.Read32(IDBuild),
		Manifest: uint64(p.r.Read32(IDManifestHi))<<32 | uint64(p.r.Read32(IDManifestLo)), Caps: p.r.Read32(IDCaps),
	}
	im := p.r.Read32(IDImage)
	id.Image = string([]byte{byte(im), byte(im >> 8), byte(im >> 16), byte(im >> 24)}) //nolint:gosec // G115: the word's four bytes
	g := p.r.Read32(IDGeometry)
	id.Lanes, id.Channels, id.TickPS = int(g&0xFF), int(g>>8&0xFF), int(g>>16)
	for i := range id.DNA {
		id.DNA[i] = p.r.Read32(IDDna0 + uint32(4*i))
	}
	p.ID = id
	switch {
	case id.ABIMajor != ABIMajor:
		return fmt.Errorf("%w: ABI %d.%d, this firmware speaks %d.x", ErrNotOpen, id.ABIMajor, id.ABIMinor, ABIMajor)
	case id.Manifest != Manifest:
		return fmt.Errorf("%w: register manifest %016x, this firmware was built for %016x", ErrNotOpen, id.Manifest, uint64(Manifest))
	case id.TickPS != 500 || id.Lanes != 8:
		return fmt.Errorf("%w: geometry %d lanes, %d ps tick", ErrNotOpen, id.Lanes, id.TickPS)
	}
	return nil
}

// Has reports whether the image implements a feature (ID_CAPS).
func (p *PL) Has(c uint32) bool { return p.ID.Caps&c == c }

// Read32 reads an open-plane register.
func (p *PL) Read32(off uint32) uint32 { return p.r.Read32(off) }

// Write32 writes an open-plane register.
func (p *PL) Write32(off, v uint32) { p.r.Write32(off, v) }

// ClockKHz is the sample core's clock as the image measures it against
// aclk (0 while it does not run).
func (p *PL) ClockKHz() uint32 { return p.r.Read32(SysCCLKKhz) }

// Faults reads and clears the sticky faults (SYS_FAULT bits: record
// overflow, stream overflow, ADC1 slip, AXI write error, AXI read error).
func (p *PL) Faults() uint32 {
	f := p.r.Read32(SysFault)
	if f != 0 {
		p.r.Write32(SysFault, f)
	}
	return f
}
