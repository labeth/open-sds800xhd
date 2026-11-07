package openpl

import (
	"encoding/binary"
	"errors"
	"fmt"
	"syscall"
	"time"
)

// Stream payloads (STREAM_CTRL.payload, spec 20 §8.1).
const (
	PayloadCodes  = 1
	PayloadEvents = 3
)

// StreamConfig is a stream's ring and source.
type StreamConfig struct {
	Payload int
	Mask    uint8 // channels (codes)
	Decim   int   // the stream's decimation of the channel streams
	Blocks  int   // ring slots
	Beats   int   // items a block
}

// BlockBytes is one ring slot's size.
func (c StreamConfig) BlockBytes() int { return 8 * (c.Beats + 2) }

// Ring is the stream ring mapped read-only from the DDR region outside
// Linux (spec 20 §6.1).
type Ring struct {
	mem []byte
}

// MapRing maps the stream ring.
func MapRing() (*Ring, error) {
	fd, err := syscall.Open("/dev/mem", syscall.O_RDONLY|syscall.O_SYNC|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/mem: %w", err)
	}
	defer func() { _ = syscall.Close(fd) }()
	mem, err := syscall.Mmap(fd, RingBase, RingBytes, syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("mmap stream ring: %w", err)
	}
	return &Ring{mem: mem}, nil
}

// Close unmaps the ring.
func (r *Ring) Close() error {
	if r.mem == nil {
		return nil
	}
	err := syscall.Munmap(r.mem)
	r.mem = nil
	return err
}

// Block is one stream block as the framer wrote it.
type Block struct {
	Seq      uint32
	Payload  int
	Mask     uint8
	TimeTick uint64
	Lost     bool // blocks were dropped before this one
	Items    []uint64
}

// ParseBlock decodes ring slot raw.
func ParseBlock(raw []byte, beats int) (Block, error) {
	h0 := binary.LittleEndian.Uint64(raw)
	h1 := binary.LittleEndian.Uint64(raw[8:])
	if h0&0xFFFF != 0x5354 {
		return Block{}, fmt.Errorf("stream block: magic %#04x", h0&0xFFFF)
	}
	b := Block{Seq: uint32(h0 >> 32), Payload: int(h0 >> 16 & 0xFF), Mask: uint8(h0 >> 24), //nolint:gosec // G115: header fields
		TimeTick: h1 & (1<<48 - 1), Lost: h1>>48&1 == 1, Items: make([]uint64, beats)}
	for i := range b.Items {
		b.Items[i] = binary.LittleEndian.Uint64(raw[16+8*i:])
	}
	return b, nil
}

// StartStream configures the ring and starts the stream. The record path's
// route must be committed (the stream reads the same channel streams).
func (p *PL) StartStream(c StreamConfig) {
	p.r.Write32(StreamCtrl, 0)
	p.r.Write32(StreamDecim, uint32(max(1, c.Decim))) //nolint:gosec // G115: < 2^28
	p.r.Write32(StreamBase, RingBase)
	p.r.Write32(StreamBlocks, uint32(c.Blocks)) //nolint:gosec // G115: ≤ 65535 slots
	p.r.Write32(StreamBeats, uint32(c.Beats))   //nolint:gosec // G115: ≤ 512 items
	p.r.Write32(StreamTail, 0)
	p.r.Write32(StreamCtrl, 1|uint32(c.Payload&3)<<4|uint32(c.Mask&15)<<8)
}

// StopStream stops it.
func (p *PL) StopStream() { p.r.Write32(StreamCtrl, 0) }

// StreamState is the stream block's counters.
type StreamState struct {
	Head, Seq, Lost uint32
	Running, Over   bool
}

// ReadStream reads the stream counters.
func (p *PL) ReadStream() StreamState {
	st := p.r.Read32(StreamStatus)
	return StreamState{
		Head: p.r.Read32(StreamHead), Seq: p.r.Read32(StreamSeq), Lost: p.r.Read32(StreamLost),
		Running: st&1 == 1, Over: st>>1&1 == 1,
	}
}

// SetTail tells the PL how many blocks were consumed.
func (p *PL) SetTail(n uint32) { p.r.Write32(StreamTail, n) }

// Slot returns ring slot k's bytes.
func (r *Ring) Slot(c StreamConfig, k int) []byte {
	n := c.BlockBytes()
	return r.mem[k*n : (k+1)*n]
}

// ---- stacker

// StackStart clears the bins and starts accumulating n records over
// phases phases. The stacker ignores a start while it clears (8192 sample
// clocks), so the clearing is waited for on the status, not on a wall-clock
// time, and the start is confirmed by the running bit.
func (p *PL) StackStart(n, phases int) error {
	p.r.Write32(StackN, uint32(n&0xFFFF))
	p.r.Write32(StackPhases, uint32(phases&31))
	p.r.Write32(StackCtrl, 1)
	if !p.waitStack(func(s uint32) bool { return s&2 == 0 }) {
		return errors.New("stacker: the bins did not clear")
	}
	p.r.Write32(StackCtrl, 2)
	if !p.waitStack(func(s uint32) bool { return s&1 == 1 }) {
		return errors.New("stacker: did not start")
	}
	return nil
}

func (p *PL) waitStack(ok func(uint32) bool) bool {
	end := time.Now().Add(5 * time.Second)
	for !ok(p.r.Read32(StackStatus)) {
		if time.Now().After(end) {
			return false
		}
		time.Sleep(100 * time.Microsecond)
	}
	return true
}

// StackStatus is (running, clearing, records taken).
func (p *PL) StackStatus() (running, clearing bool, records int) {
	s := p.r.Read32(StackStatus)
	return s&1 == 1, s>>1&1 == 1, int(s >> 16)
}

// Bin reads stacker bin b: the sum of its codes and their count.
func (p *PL) Bin(b int) (sum uint32, count uint16) {
	off := uint32(8 * b) //nolint:gosec // G115: b < 8192
	return p.bins.Read32(off), uint16(p.bins.Read32(off + 4))
}

// ---- counter

// CounterStart measures event ev (spec 20 §7.3) over gates of gate cclk
// cycles, continuously.
func (p *PL) CounterStart(ev, gate int, totalize bool) {
	p.r.Write32(CounterCtrl, 0)
	p.r.Write32(CounterGate, uint32(gate)) //nolint:gosec // G115: a gate in clocks
	c := uint32(ev&31) | 1<<8
	if totalize {
		c |= 1 << 9
	}
	p.r.Write32(CounterCtrl, c)
}

// CounterRead is the last gate's events and the ticks from its first
// counted event to its last (done false before the first gate ended).
func (p *PL) CounterRead() (count, periodTicks uint32, done bool) {
	return p.r.Read32(CounterCount), p.r.Read32(CounterPeriod), p.r.Read32(CounterStatus)&1 == 1
}

// EvARise is the event bit of channel c's comparator A rising (the
// counter's input for a channel's frequency).
func EvARise(c int) int { return 4 * c }
