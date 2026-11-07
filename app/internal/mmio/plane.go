package mmio

import (
	"sync"
	"time"
)

// NumRegs is the number of registers of one plane (idx 0x000 … 0x1FF).
const NumRegs = 0x200

// Plane is one 16-bit register plane (ACQ or MAP) with a software shadow of
// every value written (spec 02 §1.2):
//
//   - writes go to hardware unconditionally (the write rule): re-writing an
//     unchanged value is harmless for ordinary registers and required for
//     strobes;
//   - read-modify-write uses the shadow, never the hardware read value,
//     because several indices carry status bits in the read direction;
//   - hardware reads (HW, the polls) are for status registers only;
//   - all accesses to a plane are serialised behind one lock, so
//     select-then-access sequences can hold it (Locked).
type Plane struct {
	Name string

	mu     sync.Mutex
	bus    Bus
	shadow [NumRegs]uint16
	trace  func(name string, idx int, v uint16)
}

// NewPlane returns a plane on bus. The shadow starts at 0 (spec 02 §1.2:
// assume 0 / unknown; the firmware initialises every register it uses).
func NewPlane(name string, bus Bus) *Plane { return &Plane{Name: name, bus: bus} }

// SetTrace installs a function called on every write (tests).
func (p *Plane) SetTrace(f func(name string, idx int, v uint16)) {
	p.mu.Lock()
	p.trace = f
	p.mu.Unlock()
}

func (p *Plane) write(idx int, v uint16) {
	p.shadow[idx] = v
	p.bus.Write32(uint32(idx)*4, uint32(v)) //nolint:gosec // G115: idx is below NumRegs and a register's value is 16 bits
	if p.trace != nil {
		p.trace(p.Name, idx, v)
	}
}

// Set writes v to idx. Several values are written in order (a strobe or
// pulse: Set(i, 0, 1, 0)).
// TRLC-LINKS: REQ-HD-003
func (p *Plane) Set(idx int, vs ...uint16) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, v := range vs {
		p.write(idx, v)
	}
}

// Field sets bits hi:lo of idx to v, read-modify-write on the shadow.
func (p *Plane) Field(idx, hi, lo int, v uint16) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.field(idx, hi, lo, v)
}

func (p *Plane) field(idx, hi, lo int, v uint16) {
	mask := uint16((1<<(hi-lo+1))-1) << lo
	p.write(idx, p.shadow[idx]&^mask|(v<<lo)&mask)
}

// Set32 writes a wider quantity low word first: bits 15:0 to lo, bits 31:16
// masked with hiMask to hi (spec 02 §1.1).
func (p *Plane) Set32(lo, hi int, v uint32, hiMask uint16) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.write(lo, uint16(v)) //nolint:gosec // G115: the low word of a 32-bit register pair
	p.write(hi, uint16(v>>16)&hiMask)
}

// Shadow returns the last value written to idx (0 if never written).
func (p *Plane) Shadow(idx int) uint16 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.shadow[idx]
}

// HW reads idx from hardware: status direction only.
func (p *Plane) HW(idx int) uint16 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return uint16(p.bus.Read32(uint32(idx) * 4)) //nolint:gosec // G115: a plane's registers are 16 bits wide in 32-bit bus slots
}

// Poll is the poll primitive of spec 02 §1.3: read idx from hardware until
// (value & mask != 0) == set, sleeping between reads, at most n+1 reads.
// It reports whether the condition was met; callers decide what a timeout
// means.
func (p *Plane) Poll(idx int, mask uint16, set bool, n int, sleep time.Duration) bool {
	for i := 0; i <= n; i++ {
		if (p.HW(idx)&mask != 0) == set {
			return true
		}
		time.Sleep(sleep)
	}
	return false
}

// Locked runs f with the plane lock held, for select-then-access sequences
// (configuration banks, readout setup). f must use the Tx methods only.
func (p *Plane) Locked(f func(t *Tx)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(&Tx{p: p})
}

// Tx is the plane while Locked holds its lock.
type Tx struct{ p *Plane }

// Set writes v (several values in order) to idx.
func (t *Tx) Set(idx int, vs ...uint16) {
	for _, v := range vs {
		t.p.write(idx, v)
	}
}

// Field sets bits hi:lo of idx to v on the shadow.
func (t *Tx) Field(idx, hi, lo int, v uint16) { t.p.field(idx, hi, lo, v) }

// Set32 writes a wider quantity low word first.
func (t *Tx) Set32(lo, hi int, v uint32, hiMask uint16) {
	t.p.write(lo, uint16(v)) //nolint:gosec // G115: the low word of a 32-bit register pair
	t.p.write(hi, uint16(v>>16)&hiMask)
}

// Shadow returns the last value written to idx.
func (t *Tx) Shadow(idx int) uint16 { return t.p.shadow[idx] }

// HW reads idx from hardware.
func (t *Tx) HW(idx int) uint16 { return uint16(t.p.bus.Read32(uint32(idx) * 4)) } //nolint:gosec // G115: a plane's registers are 16 bits wide in 32-bit bus slots
