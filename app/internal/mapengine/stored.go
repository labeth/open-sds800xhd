package mapengine

import "github.com/labeth/open-sds800xhd/app/internal/timebase"

// StoredSets writes the map side of a stored-record redraw (spec 07 §7.8
// stored-record set, spec 03 §3.7 step 4): per channel of the model a
// normal transaction with the stored set, each followed by the empty
// gain/offset transaction; the zoom sets of a held record carry only their
// code window; then the DPO value for the stored points per pixel (spec 07
// §7.9). Quick preview is off on this firmware: 0x110 ← 0, 0x111 is not
// written, 0x125 keeps the display type.
func (m *Engine) StoredSets(sm timebase.StoredMap, iv int, vectors bool, slope int) {
	mp := m.mp
	for n := range m.channels {
		m.begin(n)
		if sm.PPP%10 == 0 {
			mp.Set(0x19D, 10)
		} else {
			mp.Set(0x19D, uint16(sm.PPP)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		}
		mp.Set(0x060, 250)
		mp.Field(0x19F, 3, 0, uint16(timebase.ReadTimes(sm.PPP))) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		mp.Set(0x19B, uint16(max(1, sm.P/10)))                    //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		mp.Set(0x19A, uint16(sm.P/20))                            //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		mp.Set(0x1A1, uint16(timebase.MaskBase(sm.PPP)))          //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		if sm.PPP < 11 || sm.PPP == 25 || sm.PPP == 125 {
			mp.Set(0x1A3, 1)
		} else {
			mp.Set(0x1A3, uint16(sm.PPP/10)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		}
		mp.Set(0x1A7, uint16(sm.Seg)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		lx := 1
		if sm.PPP >= sm.P && sm.P > 0 {
			lx = sm.PPP / sm.P
		}
		mp.Field(0x1AB, 11, 0, uint16(lx))
		mp.Set(0x1AE, uint16(sm.Start)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		mp.Set(0x1AF, uint16(sm.End))   //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		mp.Set(0x1B5, 0)
		mp.Set(0x1B6, 0)
		mp.Set(0x1B7, 0)
		mp.Set(0x153, uint16(sm.P/5)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		mp.Set(0x110, 0)
		mp.Set(0x125, b2u(vectors))
		m.codeWindow()
		m.end()
		m.gainOffset(n)
	}
	if m.zoom {
		for n := range m.channels {
			m.begin(4 + n)
			mp.Set(0x1BD, 0)
			mp.Set(0x1BE, 0x167)
			m.end()
		}
	}
	mp.Set(0x189, 1)
	if x, ud, a, ok := timebase.DPO(1, sm.PPP, iv&0xFF, slope, false, false); ok {
		mp.Set(0x1A9, uint16(x))  //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		mp.Set(0x13A, uint16(ud)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		mp.Set(0x13E, uint16(a))  //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	}
}

// gainOffset is the empty gain/offset transaction of set n (spec 07 §7.3):
// steps 1–3, the valid pulse on bit 15, step 6.
func (m *Engine) gainOffset(n int) {
	m.begin(n)
	w := m.mp.Shadow(0x190)
	m.set190(w | 0x8000)
	m.set190(w & 0x7FFF)
	m.set190(w & 0x00FE)
}
