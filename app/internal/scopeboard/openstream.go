package scopeboard

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-sds800xhd/app/internal/openpl"
	"github.com/labeth/open-sds800xhd/app/internal/trigger"
)

// The open image's gap-free stream (spec 20 §8.1): the PL frames blocks
// into a ring in DDR and publishes them by STREAM_HEAD; Lease reads the
// next block from the ring mapped read-only, Release advances STREAM_TAIL.
// A block the PL had to drop (ring full) shows as a jump of the block
// sequence and the lost flag; both are counted in Faults, and the items
// lost are STREAM_LOST. No board goroutine: the ring is the buffer.
type openStream struct {
	pl     *openpl.PL
	ring   *openpl.Ring
	cfg    openpl.StreamConfig
	chans  []int
	tail   uint32 // blocks consumed
	leased uint32 // blocks leased
	next   uint32 // expected block sequence number
	mu     sync.Mutex
	faults hal.StreamFaults
}

const (
	openRingBeats = 510 // items a block: 4 KiB slots
	openStreamMax = 4   // record words a sample-core clock the stream path carries (one item a clock)
)

func (b *Board) openKinds() []hal.Payload {
	k := []hal.Payload{hal.PayloadCodes}
	if b.open.pl.Has(openpl.CapUART) || b.open.pl.Has(openpl.CapSPI) || b.open.pl.Has(openpl.CapI2C) {
		k = append(k, hal.PayloadEvents)
	}
	return k
}

// openStreamStart commits the staged plan's route (and, for events, the
// staged protocol trigger's engine) and starts the ring.
func (b *Board) openStreamStart(spec hal.StreamSpec) error {
	o := b.open
	if o.stream != nil {
		return hal.Refuse("stream", "already streaming")
	}
	if b.state == hal.Armed {
		o.pl.Stop()
	}
	cfg := openpl.StreamConfig{Blocks: openpl.RingBytes / (8 * (openRingBeats + 2)), Beats: openRingBeats}
	var chans []int
	switch spec.Payload { //nolint:exhaustive // default: the open image streams codes and events
	case hal.PayloadCodes:
		cfg.Payload = openpl.PayloadCodes
		for _, c := range spec.Channels {
			if c < 0 || c >= b.id.Channels || !b.staged.Ch[c].On {
				return hal.Refuse("stream", fmt.Sprintf("C%d is not acquired", c+1))
			}
		}
		for c := range b.id.Channels {
			if b.staged.Ch[c].On {
				chans = append(chans, c)
				cfg.Mask |= 1 << c
			}
		}
		if len(chans) == 0 {
			return hal.Refuse("stream", "no channel is on")
		}
		route := openpl.RouteFor(cfg.Mask)
		cfg.Decim = max(1, int(math.Round(spec.SampleS*openpl.RouteRate(route))))
		lanes := 8 / openpl.TicksPerLane(route)
		if len(chans)*lanes > openStreamMax*cfg.Decim {
			return hal.Refuse("stream", fmt.Sprintf("%d channels at %.4g Sa/s exceed the stream path's %d words a 4 ns clock",
				len(chans), openpl.RouteRate(route)/float64(cfg.Decim), openStreamMax))
		}
	case hal.PayloadEvents:
		cfg.Payload = openpl.PayloadEvents
		if b.staged.Trig.Type != trigger.Serial {
			return hal.Refuse("stream", "decoded events come from the protocol engine: program a serial trigger first")
		}
	default:
		return hal.Refuse("stream", "the open image streams codes and decoded events")
	}
	if err := b.openCommit(); err != nil {
		return err
	}
	ring, err := openpl.MapRing()
	if err != nil {
		return err
	}
	st := &openStream{pl: o.pl, ring: ring, cfg: cfg, chans: chans}
	o.pl.StartStream(cfg)
	o.stream = st
	b.state, b.stored = hal.Armed, false
	return nil
}

func (b *Board) openStreamStop() error {
	o := b.open
	st := o.stream
	if st == nil {
		return nil
	}
	o.pl.StopStream()
	err := st.ring.Close()
	o.stream = nil
	b.state, b.rerun = hal.Idle, true
	return err
}

// lease waits for the next published block.
func (st *openStream) lease(ctx context.Context) (hal.StreamBlock, error) {
	for {
		s := st.pl.ReadStream()
		if s.Head-st.leased > 0 && s.Head != st.leased {
			break
		}
		select {
		case <-ctx.Done():
			return hal.StreamBlock{}, ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
	slot := int(st.leased % uint32(st.cfg.Blocks)) //nolint:gosec // G115: ring slots
	blk, err := openpl.ParseBlock(st.ring.Slot(st.cfg, slot), st.cfg.Beats)
	if err != nil {
		return hal.StreamBlock{}, err
	}
	st.leased++
	st.mu.Lock()
	if blk.Seq != st.next || blk.Lost {
		st.faults.SeqJumps++
		st.faults.LostBlocks += uint64(blk.Seq - st.next)
	}
	st.next = blk.Seq + 1
	st.mu.Unlock()
	out := hal.StreamBlock{Payload: hal.PayloadCodes, First: uint64(blk.Seq) * uint64(st.cfg.Beats), Ring: slot} //nolint:gosec // G115: items a block, ≤ 512
	if st.cfg.Payload == openpl.PayloadEvents {
		out.Payload = hal.PayloadEvents
		for _, it := range blk.Items {
			out.Events = append(out.Events, hal.Event{At: it & (1<<48 - 1), Kind: uint8(it >> 56), Value: uint32(it >> 48 & 0xFF)})
		}
		return out, nil
	}
	nch := len(st.chans)
	per := 4 * (len(blk.Items) / nch)
	words := make([]uint16, per*nch)
	for i, it := range blk.Items[:per/4*nch] {
		c, k := i%nch, i/nch
		for j := range 4 {
			words[c*per+4*k+j] = uint16(it >> (16 * j)) //nolint:gosec // G115: a 16-bit word of the item
		}
	}
	out.Words = words
	for range st.chans {
		out.Formats = append(out.Formats, format12)
	}
	return out, nil
}

func (st *openStream) release(_ hal.StreamBlock) {
	st.tail++
	st.pl.SetTail(st.tail)
}

// OpenStreamLost is the PL's count of items lost to whole-block drops.
func (b *Board) OpenStreamLost() uint32 {
	if b.open == nil {
		return 0
	}
	return b.open.pl.ReadStream().Lost
}

var errNoStream = errors.New("stream: not started")
