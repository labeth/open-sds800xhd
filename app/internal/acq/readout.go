package acq

import (
	"encoding/binary"
	"errors"
	"time"

	"github.com/labeth/open-sds800xhd/app/internal/timebase"
)

// Selection is a stored frame (or one segment of a sequence) selected for
// readout: the frame-selection steps of spec 03 §4.3 have run.
type Selection struct {
	Segment int
	Phi, T  int    // trigger fine value φ and t = ⌊φ/I⌋
	StampUs uint64 // the 48-bit frame timestamp, µs (spec 03 §6.3)
	resume  bool   // a history pause to undo at the end
}

// ErrNoRecord is a readout without a stored record (spec 03 §4.2).
var ErrNoRecord = errors.New("no record in the record memory")

// BeginRead runs the frame selection of spec 03 §4.3 steps 0–2 and 4 for
// segment seg (0 outside sequence mode; spec 03 §6.2 for k > 0). The
// acquisition must be halted. Every BeginRead is closed by EndRead.
func (e *Engine) BeginRead(seg int) (Selection, error) {
	a := e.acq
	sel := Selection{Segment: seg}
	// 0. History pause, if not paused already.
	if a.Shadow(0x0F8)&0x100 == 0 {
		a.Field(0x0F8, 8, 8, 1)
		sel.resume = true
	}
	// 1., 2. Freeze the frame, select the segment.
	a.Set(0x01E, 1)
	cnt := a.Shadow(0x018) & 0x7FF
	a.Set(0x018, 0x1000|cnt)
	a.Set(0x04D, uint16(seg))               //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	a.Field(0x05B, 8, 8, uint16(seg>>16)&1) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	// 4. Trigger interpolation value and timestamp.
	sel.Phi, sel.StampUs = e.selectSegment(cnt)
	sel.T = sel.Phi / e.h.I
	return sel, nil
}

// selectSegment is the read-start pulse of the selected segment and the
// reads it latches: the fine value φ and the timestamp.
func (e *Engine) selectSegment(cnt uint16) (phi int, stamp uint64) {
	a := e.acq
	a.Set(0x018, 0x3000|cnt, 0x1000|cnt)
	a.Poll(0x0A4, 1, true, 100000, time.Microsecond)
	aa, a6 := a.HW(0x0AA), a.HW(0x0A6)
	hi, mid, lo := a.HW(0x0E8), a.HW(0x01D), a.HW(0x01C)
	return fineValue(e.h, a6, aa), uint64(hi)<<32 | uint64(mid)<<16 | uint64(lo)
}

// EndRead is step 10 of spec 03 §4.3.
func (e *Engine) EndRead(sel Selection) {
	a := e.acq
	a.Set(0x0CA, 0)
	a.Set(0x018, a.Shadow(0x018)&0x7FF)
	a.Set(0x01E, 0)
	e.mp.Set(0x148, 1)
	if sel.resume {
		a.Field(0x0F8, 8, 8, 0)
	}
}

// Window is what one channel pass delivered.
type Window struct {
	timebase.Readout
	Words     []uint16 // the window's samples, first screen sample first
	TriggerAt float64  // index in Words of the trigger, sub-sample
	// Buf is the whole transfer and At the index of Words[0] in it: words
	// that come in groups aligned to the transfer (peak-detect pairs) are
	// regrouped from there.
	Buf []uint16
	At  int
}

// ReadWindow reads n screen samples from screen sample k of channel ch of
// the selected frame into dst's storage (grown when short): spec 03 §4.3
// steps 3 and 5–9. A screen-record read (piece false) is compressed by the
// PL once n ≥ 2·M; a piece read is uncompressed and reads only the pieces
// that hold the window. yield runs between DMA blocks.
func (e *Engine) ReadWindow(sel Selection, ch, k, n int, piece bool, dst []uint16, yield func()) (Window, error) {
	if e.dma == nil {
		return Window{}, errors.New("readout DMA unavailable")
	}
	a := e.acq
	h, s := e.h, e.cur
	pos := h.Position(e.posInput(&s, ch))
	w := h.Window(pos, sel.T, k, n, piece)
	// 3. Compression (screen-record reads), then the compressor reset.
	if !piece {
		a.Field(0x0D3, 8, 8, 1)
		a.Set(0x0D5, uint16(w.Comp)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	}
	a.Set(0x0D4, 0, 1, 0)
	// 5.–8. Read mode, buffer, channel, window, read start.
	a.Field(0x0D3, 1, 0, 1)
	a.Set(0x0C9, 0, 1)
	a.Set(0x0CA, 1)
	a.Set(0x0C1, uint16(ch))                  //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	a.Set32(0x0C3, 0x0C4, uint32(w.O), 0x7F)  //nolint:gosec // G115: the register pair takes the value's low 32 bits
	a.Set32(0x0C5, 0x0C6, uint32(w.L), 0x3FF) //nolint:gosec // G115: the register pair takes the value's low 32 bits
	a.Set(0x0C2, 0, 1)
	// 9. Transfer: the screen starts at buffer index λ/I + t.
	at := w.ScreenAt(h.I, sel.T)
	want := at + (w.Screen+w.Factor-1)/w.Factor
	// A piece read moves whole pieces, as the PL streams them.
	total := w.N
	if cap(dst) < total {
		dst = make([]uint16, total)
	}
	dst = dst[:total]
	var err error
	if piece {
		err = e.pieces(w, dst, yield)
	} else {
		err = e.dma.words(dst, yield)
	}
	if err != nil {
		return Window{}, err
	}
	lo := min(at, len(dst))
	hi := min(want, len(dst))
	out := Window{Readout: w, Words: dst[lo:hi], Buf: dst, At: lo}
	// The trigger is screen sample S/2 − 2 − R·τ (spec 03 §4.3, time axis).
	out.TriggerAt = (float64(h.S)/2 - 2 - h.R*s.DelayS*1e6 - float64(k)) / float64(w.Factor)
	return out, nil
}

// pieces moves a piece read: piece 0 is Head + M samples, every further
// piece M samples after a compressor reset pulse (spec 03 §4.3).
func (e *Engine) pieces(w timebase.Readout, dst []uint16, yield func()) error {
	first := min(len(dst), w.Head+timebase.PieceSize)
	if err := e.dma.words(dst[:first], yield); err != nil {
		return err
	}
	for done := first; done < len(dst); {
		e.acq.Set(0x0D4, 0, 1, 0)
		k := min(len(dst)-done, timebase.PieceSize)
		if err := e.dma.words(dst[done:done+k], yield); err != nil {
			return err
		}
		done += k
	}
	return nil
}

// words moves len(dst) 16-bit little-endian samples in blocks of at most
// maxBlock bytes, decoding them straight into dst.
func (d *DMA) words(dst []uint16, yield func()) error {
	for done := 0; done < len(dst); {
		k := min(len(dst)-done, maxBlock/2)
		if err := d.block(nil, 2*k); err != nil {
			return err
		}
		for i := range k {
			dst[done+i] = binary.LittleEndian.Uint16(d.buf[2*i:])
		}
		done += k
		if yield != nil && done < len(dst) {
			yield()
		}
	}
	return nil
}

// StoredRecord reports whether the record memory holds a record to read
// (spec 03 §4.2).
func (e *Engine) StoredRecord() bool {
	a := e.acq
	return a.HW(0x02F)&0x8000 != 0 || a.HW(0x026) != 0 || a.HW(0x05B)&0x200 != 0 ||
		a.HW(0x120)&0x100 != 0 || a.HW(0x122) != 0 || a.HW(0x121)&1 != 0
}

// Scale is channel ch's word scale as applied now, for a frame read now.
func (e *Engine) Scale(ch int) Scale { return scaleOf(e.cur.Ch[ch], e.FE.Derived(ch)) }
