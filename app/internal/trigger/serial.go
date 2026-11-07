package trigger

// Protocol is a serial trigger protocol, by the API enumeration of spec 05.
type Protocol int

// The protocols with trigger hardware (spec 05 §10).
const (
	I2C Protocol = iota
	SPI
	UART
	CAN
	LIN
)

// protocolCode is M 0x030[11:8] of each protocol.
var protocolCode = [5]uint16{I2C: 0, SPI: 1, UART: 2, CAN: 5, LIN: 3}

// Line is one bus line: its source channel and threshold.
type Line struct {
	Source int
	Level  float64 // volts at the tip
}

// AnyByte is a data byte that matches anything (spec 05 §10.3).
const AnyByte = 256

// Bytes is up to two data bytes; AnyByte where none is given.
type Bytes [2]int

// NoBytes is two don't-care bytes.
var NoBytes = Bytes{AnyByte, AnyByte}

// I2CSet is an I2C trigger (§10.4). Conditions: 0 start, 1 stop, 2
// restart, 3 no-ack, 4 EEPROM, 5 7-bit address and data, 6 10-bit address
// and data, 7 data length.
type I2CSet struct {
	SCL, SDA   Line
	Condition  int
	Address    int // 0x80 (7-bit) or 0x400 (10-bit): any
	RW         int // 0 write, 1 read, 2 any
	Data       Bytes
	Compare    int // 0 =, 1 >, 2 <
	DataLength int
	Addr10     bool
}

// SPISet is an SPI trigger (§10.5). Select: 0 active-low CS, 1 active-high
// CS, 2 clock idle timeout.
type SPISet struct {
	Clock, Data, CS Line
	Select          int
	IdleS           float64
	LatchFalling    bool
	MSBFirst        bool
	N               int       // pattern length in bits, ≤ 96
	Bits            [96]State // first-sent bit first
}

// UARTSet is a UART trigger (§10.6). Conditions: 0 start, 1 stop, 2 data,
// 3 error. Parity: 0 none, 1 odd, 2 even, 3 mark, 4 space. Stop: 0 one,
// 1 one and a half, 2 two.
type UARTSet struct {
	Line      Line
	Condition int
	MSBFirst  bool
	Compare   int
	Value     int // 2^Bits or more: any
	Baud      int
	Bits      int
	Parity    int
	Stop      int
	IdleHigh  bool
}

// CANSet is a CAN trigger (§10.6). Conditions: 0 start, 1 remote, 2 ID, 3
// ID and data.
type CANSet struct {
	Line      Line
	Condition int
	ID        int // 0x800 (11-bit) or 0x2000_0000 (29-bit): any
	Extended  bool
	Data      Bytes
	Baud      int
}

// LINSet is a LIN trigger (§10.6). Conditions: 0 break, 1 ID, 2 ID and
// data, 3 data error.
type LINSet struct {
	Line                                  Line
	Condition                             int
	ID                                    int // 0x40: any
	Data                                  Bytes
	Baud                                  int
	ChecksumError, SyncError, ParityError bool
	Spec2                                 bool
	ErrorID, ErrorLength                  int
}

// SerialSet is the serial trigger: the protocol and its settings.
type SerialSet struct {
	Protocol Protocol
	I2C      I2CSet
	SPI      SPISet
	UART     UARTSet
	CAN      CANSet
	LIN      LINSet
}

// dataSource is a decoder source field value: channels as they are.
func dataSource(src int) uint16 {
	if src >= 0 && src < 4 {
		return uint16(src)
	}
	return 4
}

// slicer is each channel's serial threshold register pair (code, hysteresis).
var slicer = [4][2]int{{0x0E6, 0x0E5}, {0x0EC, 0x0EB}, {0x10B, 0x10A}, {0x10D, 0x10C}}

// writeSlicer writes a channel's serial threshold (§10.1): biased by half
// the current LEVEL1 hysteresis.
func (t *Trigger) writeSlicer(l Line) {
	if l.Source < 0 || l.Source >= t.nch {
		return
	}
	h := t.a.Shadow(0x0E1)
	px := t.px(l.Source, l.Level, CoupDC)
	t.a.Set(slicer[l.Source][0], uint16(2048+int(float32(px)*8.0)-int(h>>1))) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.a.Set(slicer[l.Source][1], h)
}

// lines are the protocol's analog lines.
func (x *SerialSet) lines() []Line {
	switch x.Protocol {
	case I2C:
		return []Line{x.I2C.SCL, x.I2C.SDA}
	case SPI:
		if x.SPI.Select == 2 {
			return []Line{x.SPI.Clock, x.SPI.Data}
		}
		return []Line{x.SPI.Clock, x.SPI.Data, x.SPI.CS}
	case UART:
		return []Line{x.UART.Line}
	case CAN:
		return []Line{x.CAN.Line}
	case LIN:
		// the LIN line below
	}
	return []Line{x.LIN.Line}
}

func (t *Trigger) writeSlicers() {
	for _, l := range t.s.Serial.lines() {
		t.writeSlicer(l)
	}
}

// word writes data and mask word k (0–5) of §10.3.
func (t *Trigger) word(k int, data, mask uint16) {
	t.m.Set(0x036-k, data)
	t.m.Set(0x042-k, mask)
}

func (t *Trigger) words(d, mk [6]uint16) {
	for k := range 6 {
		t.m.Set(0x036-k, d[k])
	}
	for k := range 6 {
		t.m.Set(0x042-k, mk[k])
	}
}

// BaudWord is M 0x045:0x046 (§10.2).
func BaudWord(baud int) uint32 { return uint32(uint64(baud) << 32 / 25_000_000) } //nolint:gosec // G115: the board refuses bit rates above 20 Mbit/s, so the word is below 2^32

func (t *Trigger) bitTiming(baud int) {
	m := t.m
	w := BaudWord(baud)
	m.Set(0x045, uint16(w>>16))
	m.Set(0x046, uint16(w)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	if baud < 10_000_000 {
		m.Field(0x053, 0, 0, 0)
		m.Field(0x053, 9, 5, 9)
		m.Field(0x053, 4, 1, 5)
		return
	}
	n := 1_000_000_000 / (4 * baud)
	m.Field(0x053, 0, 0, 1)
	m.Field(0x053, 9, 5, uint16(n-1))      //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	m.Field(0x053, 4, 1, uint16((n-1)>>1)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
}

// twoBytes is the data-byte rule of I2C, CAN and LIN: both bytes, one, or
// none given.
func twoBytes(b Bytes) (data, mask uint16, n int) {
	g1, g2 := b[0] >= 0 && b[0] < AnyByte, b[1] >= 0 && b[1] < AnyByte
	switch {
	case g1 && g2:
		return uint16(b[0]<<8 | b[1]), 0xFFFF, 2 //nolint:gosec // G115: g1 and g2 hold both bytes to 0 … 255
	case g1:
		return uint16(b[0]), 0xFF, 1 //nolint:gosec // G115: g1 holds the byte to 0 … 255
	case g2:
		return uint16(b[1]), 0xFF, 1 //nolint:gosec // G115: g2 holds the byte to 0 … 255
	}
	return 0, 0, 0
}

// writeSerial programs the decoder (M block) and the slicers.
func (t *Trigger) writeSerial() {
	x := &t.s.Serial
	t.m.Field(0x030, 11, 8, protocolCode[x.Protocol])
	switch x.Protocol {
	case I2C:
		t.writeI2C(&x.I2C)
	case SPI:
		t.writeSPI(&x.SPI)
	case UART:
		t.writeUART(&x.UART)
	case CAN:
		t.writeCAN(&x.CAN)
	case LIN:
		t.writeLIN(&x.LIN)
	}
}

func (t *Trigger) writeI2C(x *I2CSet) {
	m := t.m
	m.Field(0x030, 12, 12, 0)
	m.Field(0x044, 9, 5, dataSource(x.SCL.Source))
	m.Field(0x030, 4, 0, dataSource(x.SDA.Source))
	switch c := x.Condition; c {
	case 0, 1, 2:
		m.Field(0x048, 2, 0, uint16(c))
		m.Field(0x048, 5, 4, 1)
	case 3:
		m.Field(0x048, 2, 0, 3)
		m.Field(0x048, 5, 4, 3)
	case 4:
		m.Field(0x048, 2, 0, 4)
		m.Field(0x048, 5, 4, 1)
		m.Field(0x047, 12, 12, 0)
		m.Field(0x030, 7, 5, uint16(x.Compare)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		m.Field(0x047, 9, 0, 0x50)
		m.Field(0x048, 15, 6, 0x78)
		dontCare := x.Data[0] < 0 || x.Data[0] >= AnyByte
		d := uint16(x.Data[0]) //nolint:gosec // G115: a data byte; the negative don't-care value is replaced below
		if x.Data[0] < 0 {
			d = AnyByte
		}
		t.word(0, d, pick(dontCare, 0, 0xFF))
		m.Set(0x043, pick(dontCare, 0, 1))
	case 5, 6:
		m.Field(0x048, 2, 0, 4)
		m.Field(0x048, 5, 4, 1)
		m.Field(0x030, 7, 5, 0)
		m.Field(0x047, 9, 0, uint16(x.Address)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		mask := uint16(0x7F)
		if c == 6 {
			m.Field(0x047, 12, 12, 1)
			mask = pick(x.Address == 0x400, 0, 0x3FF)
		} else {
			m.Field(0x047, 12, 12, 0)
			if x.Address == 0x80 {
				mask = 0
			}
		}
		m.Field(0x048, 15, 6, mask)
		m.Set(0x032, 0)
		d, mk, n := twoBytes(x.Data)
		t.word(0, d, mk)
		m.Set(0x043, uint16(n)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		m.Field(0x049, 14, 4, 0)
		m.Field(0x047, 11, 10, [3]uint16{0, 3, 1}[max(0, min(2, x.RW))])
	case 7:
		m.Field(0x048, 2, 0, 5)
		m.Field(0x048, 5, 4, 1)
		m.Field(0x048, 15, 6, 0)
		m.Field(0x030, 7, 5, 0)
		t.word(0, uint16(x.DataLength), 0) //nolint:gosec // G115: an I2C data length is 1 … 12 bytes
		m.Field(0x047, 12, 12, b2u(x.Addr10))
		m.Field(0x047, 11, 10, 1)
	}
	t.writeSlicers()
}

// SPIPattern is the data and mask words of an SPI pattern (§10.5).
func SPIPattern(x *SPISet) (data, mask [6]uint16) {
	n := max(0, min(96, x.N))
	for i := range n {
		k := i
		if x.MSBFirst {
			k = n - 1 - i
		}
		w, bit := k/16, uint(k%16)
		switch x.Bits[i] {
		case High:
			data[w] |= 1 << bit
			mask[w] |= 1 << bit
		case Low:
			mask[w] |= 1 << bit
		case DontCare:
			// neither data nor mask: the bit matches anything
		}
	}
	return data, mask
}

func (t *Trigger) writeSPI(x *SPISet) {
	m := t.m
	m.Field(0x044, 9, 5, dataSource(x.Clock.Source))
	m.Field(0x030, 4, 0, dataSource(x.Data.Source))
	idle := uint32(0x7FFFFF >> 3)
	switch x.Select {
	case 0:
		m.Field(0x04F, 1, 0, 0)
		m.Field(0x044, 4, 0, dataSource(x.CS.Source))
	case 1:
		m.Field(0x04F, 1, 0, 3)
		m.Field(0x044, 4, 0, dataSource(x.CS.Source))
	default:
		m.Field(0x04F, 1, 0, 1)
		idle = uint32(NS(x.IdleS) >> 3) //nolint:gosec // G115: the board refuses idle times beyond 8.39 ms, 20 bits of 8 ns
	}
	m.Field(0x049, 15, 15, 1)
	m.Field(0x049, 3, 0, uint16(idle>>16)&0xF)
	m.Set(0x04E, uint16(idle)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	m.Field(0x030, 7, 5, 0)
	t.words(SPIPattern(x))
	m.Field(0x030, 12, 12, 0)
	m.Set(0x043, uint16(x.N)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	m.Field(0x04F, 3, 3, b2u(x.LatchFalling))
	m.Field(0x04F, 2, 2, 0)
	t.writeSlicers()
}

// UARTValue is the data value clamp of §10.6: the value clamped to 2^bits
// (any), and its data and mask words.
func UARTValue(v, bits int) (clamped int, data, mask uint16) {
	lim := 1 << max(1, bits)
	v = min(v, lim)
	if v < 0 {
		v = lim
	}
	if v < lim {
		mask = uint16(lim - 1) //nolint:gosec // G115: lim is 2^bits for a UART frame of at most 9 bits
	}
	return v, uint16(v & 0xFF), mask
}

// uartParity is M 0x050 bits 6:5 and 14:13 of each parity.
var uartParity = [5][2]uint16{{1, 0}, {0, 0}, {3, 0}, {0, 2}, {0, 3}}

func (t *Trigger) writeUART(x *UARTSet) {
	m := t.m
	m.Field(0x030, 4, 0, dataSource(x.Line.Source))
	m.Field(0x050, 1, 0, uint16(x.Condition)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	m.Field(0x051, 12, 12, b2u(!x.MSBFirst))
	if x.Condition == 2 {
		m.Field(0x030, 7, 5, uint16(x.Compare)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
		m.Field(0x049, 14, 4, 0)
		_, d, mk := UARTValue(x.Value, x.Bits)
		t.word(0, d, mk)
	}
	t.bitTiming(x.Baud)
	m.Field(0x050, 10, 7, uint16(x.Bits)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	par := max(0, min(4, x.Parity))
	if x.Condition == 2 && par >= 3 {
		par = 7 - par // mark and space swap for the data condition
	}
	m.Field(0x050, 6, 5, uartParity[par][0])
	m.Field(0x050, 14, 13, uartParity[par][1])
	m.Field(0x050, 4, 3, [3]uint16{0, 1, 3}[max(0, min(2, x.Stop))])
	m.Field(0x050, 2, 2, b2u(x.IdleHigh))
	m.Set(0x043, uint16(x.Bits)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	t.writeSlicers()
}

// canIDWords are the pattern words of a CAN identifier: data and mask, the
// mask empty for the any-ID value (0x800 standard, 0x2000_0000 extended).
func canIDWords(x *CANSet) (d, mk [6]uint16) {
	if !x.Extended {
		d[5] = uint16(x.ID << 2) //nolint:gosec // G115: a standard CAN identifier is 11 bits, 0x800 for any
		if x.ID != 0x800 {
			mk[5] = 0x1FFC
		}
		return d, mk
	}
	d[4], d[5] = uint16(x.ID), uint16(x.ID>>16) //nolint:gosec // G115: the two words of a 29-bit CAN identifier
	if x.ID != 0x2000_0000 {
		mk[4], mk[5] = 0xFFFF, 0x1FFF
	}
	return d, mk
}

func (t *Trigger) writeCAN(x *CANSet) {
	m := t.m
	m.Field(0x030, 4, 0, dataSource(x.Line.Source))
	m.Field(0x051, 2, 0, uint16(x.Condition)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	if x.Condition >= 1 && x.Condition <= 3 {
		d, mk := canIDWords(x)
		length := 0
		if x.Condition == 3 {
			var n int
			d[0], mk[0], n = twoBytes(x.Data)
			length = 16
			if n == 1 {
				length = 8
			}
		}
		m.Field(0x030, 7, 5, 0)
		m.Field(0x049, 14, 4, 0)
		t.words(d, mk)
		m.Set(0x043, uint16(length))
		m.Field(0x051, 4, 3, pick(x.Extended, 3, 0))
	}
	t.bitTiming(x.Baud)
	t.writeSlicers()
}

func (t *Trigger) writeLIN(x *LINSet) {
	m := t.m
	m.Field(0x030, 4, 0, dataSource(x.Line.Source))
	m.Field(0x052, 1, 0, uint16(x.Condition)) //nolint:gosec // G115: a 16-bit register takes the value's low 16 bits
	m.Field(0x052, 5, 5, b2u(x.ChecksumError))
	m.Field(0x052, 4, 4, b2u(x.SyncError))
	m.Field(0x052, 3, 3, b2u(x.ParityError))
	m.Field(0x052, 2, 2, b2u(x.Spec2))
	if x.Condition != 0 {
		var d, mk [6]uint16
		length := 0
		switch x.Condition {
		case 1, 2:
			d[4] = uint16(x.ID) //nolint:gosec // G115: a LIN identifier is 6 bits, 0x40 for any
			if x.ID != 0x40 {
				mk[4] = 0x3F
			}
			if x.Condition == 2 {
				var n int
				d[0], mk[0], n = twoBytes(x.Data)
				length = n
			}
		case 3:
			d[4], mk[4] = uint16(x.ErrorID), 0x3F //nolint:gosec // G115: a LIN identifier is 6 bits
			length = x.ErrorLength
		}
		m.Field(0x030, 7, 5, 0)
		t.words(d, mk)
		m.Set(0x043, uint16(length))
	}
	t.bitTiming(x.Baud)
	t.writeSlicers()
}

// serialRestore is the decoder's start-up state (spec 05 §7, M rows).
var serialRestore = [][2]uint16{
	{0x30, 0x0001},
	{0x31, 0},
	{0x32, 0x0040},
	{0x33, 0},
	{0x34, 0},
	{0x35, 0},
	{0x36, 0},
	{0x3D, 0},
	{0x3E, 0},
	{0x3F, 0},
	{0x40, 0},
	{0x41, 0},
	{0x42, 0},
	{0x43, 0},
	{0x44, 0},
	{0x45, 0x0032},
	{0x46, 0x54E6},
	{0x47, 0x0480},
	{0x48, 0x0010},
	{0x49, 0x800F},
	{0x4E, 0xFFFF},
	{0x4F, 0},
	{0x50, 0x0420},
	{0x51, 0x1000},
	{0x52, 0x003C},
	{0x53, 0x012A},
}
