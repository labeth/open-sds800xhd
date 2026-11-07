package main

import "github.com/labeth/open-sds800xhd/sim/harness"

// signals is the input every board area runs with unless it says otherwise:
// C1 a 5 MHz 2 Vpp square, C2 a 1 MHz 1 Vpp sine.
const signals = "-signals C1=square,5e6,2,0;C2=sine,1e6,1,0"

func init() {
	register(boardArea("readout", "records: windows, zoom, compression, pieces", "Readout", 240, signals, nil, nil))
	register(boardArea("peak", "records: peak detect max/min pairs", "Peak", 120, signals, nil, nil))
	register(boardArea("segments", "records: sequence segments and timestamps", "Segments", 120, signals, nil, nil))
	// Roll samples at 10 kSa/s: slow inputs, so the stream shows their swing.
	const slow = "-signals C1=square,3,2,0;C2=sine,7,1,0"
	roll := boardArea("roll", "streams: roll through the Streamer", "Roll", 120, slow, nil, nil)
	inner := roll.runs
	roll.runs = func(e *env) []harness.Spec {
		s := inner(e)
		// The model does not compute the stream FIFO level (ACQ 0x0CB, MAP
		// 0x047): pin both full, so every poll finds groups to read.
		s[0].Script = map[uint64]uint32{harness.BaseACQ + 4*0x0CB: 0x7FF, harness.BaseMAP + 4*0x047: 0x7FF}
		return s
	}
	register(roll)
}
