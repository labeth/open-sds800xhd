package mapengine

import "github.com/labeth/open-sds800xhd/app/internal/timebase"

// RollSets writes the roll map set in place of the main set of every
// channel of the model (spec 07 §7.8, roll map set), for nOn enabled
// channels.
func (m *Engine) RollSets(h timebase.Set, nOn int) {
	ppp := int(h.S) / 1000 & 0xFFFF
	u1 := ppp / 10
	u2 := u1
	if ppp%4 != 0 {
		u2 = 4 * u1
	}
	mp := m.mp
	for n := range m.channels {
		m.begin(n)
		mp.Set(0x10A, 0)
		mp.Set(0x120, 1)
		mp.Set(0x124, 0x16)
		mp.Set(0x1A3, uint16(u1))
		mp.Set(0x18F, 0)
		mp.Set(0x19B, uint16(u2))
		mp.Set(0x19A, uint16(u2/2+1))
		mp.Set(0x1A1, 0)
		mp.Set(0x19F, 1)
		mp.Set(0x19D, 10)
		mp.Set(0x1AB, 1)
		mp.Set(0x1A7, 1)
		mp.Set(0x1AE, 0)
		mp.Set(0x1AF, 0x3FF)
		mp.Field(0x184, 0, 0, 1)
		mp.Set(0x1B1, uint16(nOn)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		mp.Set(0x153, uint16(ppp/5))
		mp.Set(0x110, 0)
		m.end()
	}
}

// RollStop ends the roll map sets (spec 07 §7.8, roll stop).
func (m *Engine) RollStop() { m.mp.Set(0x120, 0) }

// SequenceZone switches the map side of sequence mode (spec 03 §6.2):
// the sequence zone 0x1BF and the segment count 0x1BB/0x1BC.
func (m *Engine) SequenceZone(on bool, count int) {
	m.mp.Set(0x1BB, uint16(count))               //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	m.mp.Field(0x1BC, 0, 0, uint16(count>>16)&1) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	m.mp.Set(0x1BF, b2u(on))
}

// SequenceHeld marks a captured sequence as shown (MAP 0x1BA bit 0).
func (m *Engine) SequenceHeld(on bool) { m.mp.Field(0x1BA, 0, 0, b2u(on)) }
