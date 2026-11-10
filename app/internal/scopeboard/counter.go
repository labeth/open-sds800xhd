package scopeboard

import (
	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-dso-core/sample"
	"github.com/labeth/open-sds800xhd/app/internal/counter"
)

// counterFacet is the PL's frequency counter and totalizer (spec 15 §4).
type counterFacet Board

// Read advances the counter's measurement cycle and returns the latest
// frequency and the totalizer count. The first Read starts the counter.
// Valid is false until a gate has completed. OWNER.
func (c *counterFacet) Read() (sample.Counters, error) {
	b := (*Board)(c)
	if b.regs == nil {
		return sample.Counters{}, hal.Refuse("counter", "the board is not brought up")
	}
	if b.open != nil {
		return b.openCounter()
	}
	source := int((b.regs.ACQ.Shadow(0x00E) >> 4) & 7)
	if b.ctr == nil || b.ctrSource != source {
		b.ctrSource = source
		b.ctr = counter.New(b.regs.ACQ)
		b.ctr.Start()
		return sample.Counters{}, nil
	}
	f, ok := b.ctr.Poll()
	return sample.Counters{FreqHz: f, Total: b.ctr.Total(), Valid: ok}, nil
}

// Why the other optional capabilities are absent. The HAL has no place for
// the reason yet (the profile names none); these are the texts a refusal
// of a feature that needs them carries.
const (
	// LogicProbeAbsent: the SLA1016 is a USB-TMC device whose command
	// protocol is not specified (spec 15 §2).
	LogicProbeAbsent = "the SLA1016 logic probe's USB protocol is not known (spec 15 §2)"
	// GeneratorAbsent: there is no on-board generator; WGEN drives an
	// external SAG1021I or SDG, which this firmware does not drive (spec 15 §1).
	GeneratorAbsent = "no on-board generator, and external generators are not driven (spec 15 §1)"
)

// Source names the counter's actual mux, not a trigger change waiting for
// the next arm. Pattern and serial triggers can leave a different source.
func (c *counterFacet) Source() hal.Source {
	b := (*Board)(c)
	source := -1
	if b.open != nil {
		source = b.staged.Trig.Source
		if source < 0 || source > 3 {
			source = 0
		}
	} else if b.regs != nil {
		source = int((b.regs.ACQ.Shadow(0x00E) >> 4) & 7)
	}
	return hal.Source{Kind: hal.Analog, Index: source}
}
