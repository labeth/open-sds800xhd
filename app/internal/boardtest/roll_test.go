//go:build simboard

package boardtest

import (
	"context"
	"testing"
	"time"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-dso-core/sample"
)

// TestRoll streams roll mode through the Streamer (spec 03 §6.4), both
// framings.
func TestRoll(t *testing.T) {
	s, ok := board.Streamer()
	c := begin(t, "roll/refusals")
	if !c.check(ok, "the board streams") {
		return
	}
	_, err := board.Planner().Plan(hal.Request{Channels: []int{0}, TimePerDivS: 1e-3, Mode: hal.RollMode})
	c.refused(err, "roll at 1 ms/div")
	c.refused(s.Start(hal.StreamSpec{Payload: hal.PayloadEvents}), "a decoded-event stream")

	for _, x := range []struct {
		name   string
		on     []int
		depth  int
		expect map[string]uint16
	}{
		// 1 ch 10k 100 ms/div: P 40, uncompressed (spec 04 §7, 03 §6.4).
		{"roll/uncompressed", []int{1}, 10_000, map[string]uint16{
			"acq:0x010": 0x0014, "acq:0x005": 0x14,
			"acq:0x092": 0xA, "acq:0x094": 0xA, "acq:0x050": 15, "acq:0x1ee": 0xA, "acq:0x0d5": 0, "acq:0x0ca": 1,
		}},
		// 1 ch 10M 100 ms/div: P 1000, r 12.
		{"roll/compressed", []int{1}, 10_000_000, map[string]uint16{
			"acq:0x010": 0x01F4, "acq:0x005": 0x1F4,
			"acq:0x092": 0xFA, "acq:0x050": 0x1C, "acq:0x0d5": 0xC, "acq:0x024": 1, "map:0x120": 1, "map:0x153": 0xC8,
		}},
		{"roll/two-channels", []int{0, 1}, 10_000, nil},
	} {
		c = begin(t, x.name)
		if _, err := setup(t, x.on, hal.Request{TimePerDivS: 100e-3, Depth: x.depth, Mode: hal.RollMode}); !c.noErr(err, "stage roll at 100 ms/div") {
			continue
		}
		if !c.noErr(s.Start(hal.StreamSpec{Payload: hal.PayloadCodes, Channels: x.on}), "start the stream") {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		var got int
		var first uint64
		for i := range 3 {
			blk, err := s.Lease(ctx)
			if !c.noErr(err, "lease a block") {
				break
			}
			if i == 0 {
				first = blk.First
			}
			n := len(blk.Words) / len(blk.Formats)
			c.check(len(blk.Formats) == len(x.on) && n > 0, "block of %d channel(s) × %d samples from %d", len(blk.Formats), n, blk.First)
			tr := sample.Trace{
				Format: blk.Formats[0], Codes16: blk.Words[(len(x.on)-1)*n : len(x.on)*n], Valid: n,
				Scale: sample.Scale{PerCode: 1.0 / 7680, Zero: 0x8000},
			}
			if x.depth < 1_000_000 {
				lo, hi := volts(&tr)
				c.check(lo >= -0.55 && hi <= 0.55 && hi-lo > 0.2, "C2 (1 Vpp sine) streams within ±0.5 V: %.3f … %.3f V", lo, hi)
			}
			// The compressed framing is not checked by value: the device
			// model lays compressed blocks out at the stride B of 90 bytes,
			// the spec as a stream of 64-bit words; both are unverified
			// (spec 03 §6.4).

			got++
			s.Release(blk)
		}
		cancel()
		c.check(got == 3 && first == 0, "three blocks leased, the first from sample 0")
		f := s.Faults()
		c.check(f.LostBlocks == 0 && f.SeqJumps == 0, "no lost blocks or jumps (%+v)", f)
		c.refused(board.Acquirer().Arm(), "arming a roll plan")
		if x.expect != nil {
			c.expect(x.expect)
		}
		c = begin(t, x.name+"-stop")
		c.noErr(s.Stop(), "stop the stream")
		c.expect(map[string]uint16{"acq:0x0ca": 0, "acq:0x024": 0, "acq:0x0d5": 0, "map:0x120": 0})
	}
}
