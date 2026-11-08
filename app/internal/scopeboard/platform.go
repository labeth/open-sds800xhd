package scopeboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-sds800xhd/app/internal/acq"
	"github.com/labeth/open-sds800xhd/app/internal/board"
	"github.com/labeth/open-sds800xhd/app/internal/platform"
	"github.com/labeth/open-sds800xhd/app/internal/spi"
)

type platformFacet Board

// ErrNotTaken is a bring-up that left the unit to the vendor firmware:
// nothing was touched, and the app should exit.
var ErrNotTaken = errors.New("unit left to the vendor firmware")

// BringUp identifies the unit, takes it over from the vendor application
// and runs the start-up (spec 01 §12.2, spec 03 §2).
// TRLC-LINKS: REQ-HD-006
func (p *platformFacet) BringUp(context.Context) (hal.Identity, error) {
	b := (*Board)(p)
	h, err := platform.ReadHWID()
	if err != nil {
		return hal.Identity{}, fmt.Errorf("%w: hw-id: %w", ErrNotTaken, err)
	}
	b.id = platform.DecodeHWID(h)
	b.id.ResolveModel(platform.FactoryDir)
	b.vendorFW = platform.VersionPrefix()
	b.log.Printf("identity: %v, vendor system %s", b.id, b.vendorFW)
	if err := platform.StopVendor(b.log); err != nil {
		return hal.Identity{}, fmt.Errorf("%w: %w", ErrNotTaken, err)
	}
	if b.regs, err = board.Open(); err != nil {
		return hal.Identity{}, err
	}
	platform.Mark("takeover")
	b.regs.Quiesce()
	b.log.Printf("takeover done")
	if err := b.openBringUp(); err != nil {
		return hal.Identity{}, err
	}
	if b.dma, err = acq.OpenDMA(b.regs); err != nil {
		b.log.Printf("readout unavailable: %v", err)
		b.dma = nil
	}
	b.eng = acq.New(b.regs, spi.New(b.log.Printf), b.id, b.dma, b.log.Printf)
	b.eng.Startup(b.staged)
	b.bringUpUI()
	b.scope = b.eng.ScopeID()
	b.rerun = true
	b.epoch = uint32(time.Now().UnixNano() >> 20) //nolint:gosec // G115: the epoch is the low 32 bits of a ~1 ms clock: a tag, not a time
	return b.identity(), nil
}

func (p *platformFacet) Handover() hal.Handover {
	return hal.Handover{StateDir: platform.OpenDir}
}

// Service has no housekeeping to do: the panel's readers block in the
// kernel, not on the bus, and the board has no extension requests.
// Service applies the front-end settings staged while nothing is armed (a
// stopped scope's channels follow its controls); the next Arm runs its
// start delay all the same. Never in the middle of a read.
func (p *platformFacet) Service() {
	b := (*Board)(p)
	if b.eng == nil || b.reading || b.state == hal.Armed {
		return
	}
	s := b.eng.Settings()
	if s.Ch == b.staged.Ch {
		return
	}
	s.Ch = b.staged.Ch
	b.eng.Commit(s)
	b.rerun = true
}

func (p *platformFacet) Worker() func() int { return nil }

func (p *platformFacet) Reboot(string) error {
	return hal.Refuse("reboot", "not implemented on this board yet")
}

func (p *platformFacet) Diag() json.RawMessage {
	b := (*Board)(p)
	d, _ := json.Marshal(map[string]any{
		"frames": b.seq, "readout_errors": b.errs.readouts, "arm_failures": b.errs.arms, "pl_reloads": b.errs.reloads,
		"scope_id": fmt.Sprintf("%016X", b.scope), "panel": b.panelDiag(),
	})
	return d
}
