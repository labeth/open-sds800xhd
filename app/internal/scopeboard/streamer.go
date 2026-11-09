package scopeboard

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-sds800xhd/app/internal/acq"
)

// The roll stream (spec 03 §6.4) as the HAL's Streamer, payload Codes.
//
// Start commits the staged roll plan and starts the PL's stream; a reader
// goroutine then owns the readout DMA, polling the stream FIFO and filling
// a ring of blocks. While the stream runs, Readout and Arm are refused
// (they would share the DMA). A block holds the same number of samples of
// every streamed channel, channel after channel in the order of
// StreamBlock.Formats: Words[c·n : (c+1)·n] is the c-th channel's n
// samples. Blocks that find no free ring slot are dropped and counted in
// Faults, never lost silently.

const (
	ringSlots    = 8
	blockSamples = 4096 // per channel
	rollPoll     = 20 * time.Millisecond
)

type streamer struct {
	b     *Board
	g     acq.RollGeometry
	chans []int
	// sampleS is the time between the stream's samples: the acquisition's
	// interval times the compression factor (the compressed read keeps
	// every c_f-th word).
	sampleS float64
	free    chan int
	filled  chan hal.StreamBlock
	words   [ringSlots][]uint16
	stop    chan struct{}
	done    chan struct{}

	mu     sync.Mutex
	faults hal.StreamFaults
	err    error
}

type streamerFacet Board

func (s *streamerFacet) Kinds() []hal.Payload {
	if b := (*Board)(s); b.open != nil {
		return b.openKinds()
	}
	return []hal.Payload{hal.PayloadCodes}
}

// Start starts the roll stream of the staged plan (Plan.Mode RollMode).
func (s *streamerFacet) Start(spec hal.StreamSpec) error {
	b := (*Board)(s)
	if b.open != nil {
		return b.openStreamStart(spec)
	}
	if spec.Payload != hal.PayloadCodes {
		return hal.Refuse("stream", "the roll stream carries sample codes only: no bits or decoded events on this PL")
	}
	if !b.staged.Roll {
		return hal.Refuse("stream", "configure a roll plan (50 ms/div or slower) first")
	}
	if b.stream != nil {
		return hal.Refuse("stream", "already streaming")
	}
	var chans []int
	for c := range b.id.Channels {
		if b.staged.Ch[c].On {
			chans = append(chans, c)
		}
	}
	if len(chans) == 0 {
		return hal.Refuse("stream", "no channel is on")
	}
	for _, c := range spec.Channels {
		if c < 0 || c >= b.id.Channels || !b.staged.Ch[c].On {
			return hal.Refuse("stream", fmt.Sprintf("C%d is not acquired", c+1))
		}
	}
	b.eng.Stop()
	b.eng.Commit(b.staged)
	g, err := b.eng.RollStart()
	if err != nil {
		return err
	}
	st := &streamer{
		b: b, g: g, chans: chans, free: make(chan int, ringSlots),
		sampleS: float64(max(g.Factor, 1)) / (b.eng.Timebase().R * 1e6),
		filled:  make(chan hal.StreamBlock, ringSlots), stop: make(chan struct{}), done: make(chan struct{}),
	}
	for i := range ringSlots {
		st.words[i] = make([]uint16, blockSamples*len(chans))
		st.free <- i
	}
	b.stream, b.state, b.stored = st, hal.Armed, false
	go st.run()
	return nil
}

func (st *streamer) run() {
	defer close(st.done)
	fifo := make([][]uint16, 4)
	var first uint64
	parser := acq.NewRollParser(st.g)
	tick := time.NewTicker(rollPoll)
	defer tick.Stop()
	for {
		select {
		case <-st.stop:
			return
		case <-tick.C:
		}
		data, err := st.b.eng.RollPoll(st.g)
		if err != nil {
			st.mu.Lock()
			st.faults.Stalls++
			st.err = err
			st.mu.Unlock()
			continue
		}
		if data == nil {
			continue
		}
		parser.Feed(data, func(ch int, w []uint16) { fifo[ch] = append(fifo[ch], w...) })
		for {
			n := blockSamples
			for _, c := range st.chans {
				n = min(n, len(fifo[c]))
			}
			if n < blockSamples {
				break
			}
			select {
			case slot := <-st.free:
				words := st.words[slot][:n*len(st.chans)]
				for i, c := range st.chans {
					copy(words[i*n:(i+1)*n], fifo[c][:n])
				}
				fm := make([]hal.CodeFormat, len(st.chans))
				for i := range fm {
					fm[i] = format12
				}
				st.filled <- hal.StreamBlock{Payload: hal.PayloadCodes, Formats: fm, Words: words, First: first, Ring: slot, SampleS: st.sampleS}
			default:
				st.mu.Lock()
				st.faults.LostBlocks++
				st.mu.Unlock()
			}
			first += uint64(n)
			for _, c := range st.chans {
				fifo[c] = append(fifo[c][:0], fifo[c][n:]...)
			}
		}
	}
}

// Lease blocks for the next filled block.
func (s *streamerFacet) Lease(ctx context.Context) (hal.StreamBlock, error) {
	if o := (*Board)(s).open; o != nil {
		if o.stream == nil {
			return hal.StreamBlock{}, errNoStream
		}
		return o.stream.lease(ctx)
	}
	st := (*Board)(s).stream
	if st == nil {
		return hal.StreamBlock{}, errors.New("stream: not started")
	}
	select {
	case blk := <-st.filled:
		return blk, nil
	case <-st.done:
		return hal.StreamBlock{}, errors.New("stream: stopped")
	case <-ctx.Done():
		return hal.StreamBlock{}, ctx.Err()
	}
}

// Release returns a block's ring slot.
func (s *streamerFacet) Release(blk hal.StreamBlock) {
	if o := (*Board)(s).open; o != nil {
		if o.stream != nil {
			o.stream.release(blk)
		}
		return
	}
	if st := (*Board)(s).stream; st != nil && blk.Ring >= 0 && blk.Ring < ringSlots {
		st.free <- blk.Ring
	}
}

// Stop ends the stream; the reader goroutine has exited when it returns.
func (s *streamerFacet) Stop() error {
	b := (*Board)(s)
	if b.open != nil {
		return b.openStreamStop()
	}
	st := b.stream
	if st == nil {
		return nil
	}
	close(st.stop)
	<-st.done
	b.eng.RollStop()
	b.stream, b.state, b.rerun = nil, hal.Idle, true
	return nil
}

func (s *streamerFacet) Faults() hal.StreamFaults {
	if o := (*Board)(s).open; o != nil {
		if o.stream == nil {
			return hal.StreamFaults{}
		}
		o.stream.mu.Lock()
		defer o.stream.mu.Unlock()
		return o.stream.faults
	}
	st := (*Board)(s).stream
	if st == nil {
		return hal.StreamFaults{}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.faults
}
