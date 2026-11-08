package panel

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-sds800xhd/app/internal/mmio"
)

// Device nodes of the panel (spec 08 §1, §7).
const (
	KeyDevice   = "/dev/siglent_kb"
	TouchCtl    = "/dev/siglent_touch"
	inputList   = "/proc/bus/input/devices"
	touchDevice = "goodix-ts"
)

// Panel is the front panel. Events are fed by two reader goroutines of
// the panel, one blocked in read(2) on the key device and one on the touch
// screen's event node; they touch no registers (the kernel driver reads
// the key matrix), except for the beeper's click.
type Panel struct {
	events chan hal.InputEvent
	leds   func(uint32)
	beeper *Beeper
	sound  atomic.Bool

	mu      sync.Mutex
	word    uint32 // the LED word written last
	dropped atomic.Uint64
	records atomic.Uint64
	touches atomic.Uint64
}

// New returns the panel; leds writes the LED word (the map engine's), beeper
// may be nil.
func New(leds func(uint32), beeper *Beeper) *Panel {
	p := &Panel{events: make(chan hal.InputEvent, 64), leds: leds, beeper: beeper}
	p.sound.Store(true)
	return p
}

// Events is the input event stream.
func (p *Panel) Events() <-chan hal.InputEvent { return p.events }

// SetSound switches the key and touch clicks (the Sound setting, on by
// default).
func (p *Panel) SetSound(on bool) { p.sound.Store(on) }

// SetLamps writes the LED word of the named lamps (spec 08 §6.2). Names
// this panel has no lamp for light nothing.
func (p *Panel) SetLamps(on []string) error {
	w, _ := LampWord(on)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.word = w
	p.leds(w)
	return nil
}

// Word is the LED word written last.
func (p *Panel) Word() uint32 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.word
}

// Stats are the panel's counters: records read, touches, events dropped
// because nobody took them.
func (p *Panel) Stats() (records, touches, dropped uint64) {
	return p.records.Load(), p.touches.Load(), p.dropped.Load()
}

func (p *Panel) send(ev hal.InputEvent) {
	select {
	case p.events <- ev:
	default:
		p.dropped.Add(1)
	}
}

// sendWait queues an event that must not be lost (a finger's start or
// end), waiting a moment for room.
func (p *Panel) sendWait(ev hal.InputEvent) {
	select {
	case p.events <- ev:
	case <-time.After(100 * time.Millisecond):
		p.dropped.Add(1)
	}
}

// Beep sounds the beeper for d whether or not clicks are on (an alert);
// false without a beeper.
func (p *Panel) Beep(d time.Duration) bool {
	if p.beeper == nil {
		return false
	}
	p.beeper.Beep(d)
	return true
}

func (p *Panel) click() {
	if p.beeper != nil && p.sound.Load() {
		p.beeper.Beep(Click)
	}
}

// Start starts the readers. The key matrix must be configured first (the
// knob period, MAP 0x16D, is written by the map engine's start-up): the
// driver drops every key event that arrives while no reader is blocked in
// read(2), so the reader goes back to read at once (spec 08 §3.1).
// TRLC-LINKS: REQ-HD-008
func (p *Panel) Start(log func(string, ...any)) error {
	fd, err := syscall.Open(KeyDevice, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", KeyDevice, err)
	}
	go p.readKeys(fd, log)
	node, err := touchNode()
	if err != nil {
		log("touch screen: %v", err)
		return nil
	}
	tfd, err := syscall.Open(node, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		log("touch screen: open %s: %v", node, err)
		return nil
	}
	go p.readTouch(tfd, log)
	return nil
}

func (p *Panel) readKeys(fd int, log func(string, ...any)) {
	var d decoder
	var buf [4]byte
	for {
		n, err := syscall.Read(fd, buf[:])
		if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil || n != 4 {
			log("%s: read %d bytes: %v", KeyDevice, n, err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		p.records.Add(1)
		ev, ok := d.decode(buf)
		if !ok {
			continue
		}
		if ev.Kind == hal.KeyEvent && ev.Down && !strings.HasSuffix(ev.Key, "-push") {
			p.click() // button presses click; knobs do not (spec 08 §8.2)
		}
		p.send(ev)
	}
}

// touchNode finds the touch screen's event node in the input device list.
func touchNode() (string, error) {
	b, err := os.ReadFile(inputList)
	if err != nil {
		return "", err
	}
	return parseInputDevices(string(b), touchDevice)
}

// parseInputDevices returns the event node of the named device in a
// /proc/bus/input/devices listing.
func parseInputDevices(list, name string) (string, error) {
	match := false
	for l := range strings.SplitSeq(list, "\n") {
		switch {
		case strings.HasPrefix(l, "N: Name="):
			match = strings.Trim(strings.TrimPrefix(l, "N: Name="), `"`) == name
		case match && strings.HasPrefix(l, "H: Handlers="):
			for h := range strings.FieldsSeq(strings.TrimPrefix(l, "H: Handlers=")) {
				if strings.HasPrefix(h, "event") {
					return "/dev/input/" + h, nil
				}
			}
		}
	}
	return "", fmt.Errorf("no %s input device", name)
}

// maxContacts is how many fingers the touch reader follows; the GT9xx
// reports up to 5 or 10 (the unit's number is unverified, spec 08 §7).
const maxContacts = 10

// moveMinPx is how far a contact moves before a move is reported: the
// controller's jitter does not flood the UI model.
const moveMinPx = 2

// touchState follows the Goodix driver's multi-touch reports (spec 08 §7)
// and turns each contact's start, moves and end into events, one contact
// per slot: protocol B (ABS_MT_SLOT, ABS_MT_TRACKING_ID per slot), protocol
// A (each contact's positions closed by SYN_MT_REPORT) and the single-touch
// BTN_TOUCH with ABS_X/ABS_Y.
type touchState struct {
	slotted bool // the driver reports slots (protocol B)
	slot    int
	b       [maxContacts]struct {
		x, y int
		id   int // tracking id, -1 lifted
	}
	// protocol A and single touch, within one report
	ax, ay   int
	axy      bool // a contact's position since the last SYN_MT_REPORT
	a        []image.Point
	mtReport bool // SYN_MT_REPORT seen in this report
	btn      bool // BTN_TOUCH
	x, y     int  // the single-touch position
	// the contacts as last reported
	prev [maxContacts]struct {
		on   bool
		x, y int
	}
}

// The input event codes used.
const (
	evSyn, evKey, evAbs = 0, 1, 3
	synReport, synMT    = 0, 2
	absX, absY          = 0x00, 0x01
	absMTSlot           = 0x2f
	absMTX, absMTY      = 0x35, 0x36
	absMTTrackID        = 0x39
	btnTouch            = 0x14A
)

// newTouchState is a reader with no contact down.
func newTouchState() *touchState {
	s := &touchState{}
	for i := range s.b {
		s.b[i].id = -1
	}
	return s
}

// feed takes one struct input_event (type, code, value); at a report's end
// it returns the events of the contacts that started, moved or ended.
func (s *touchState) feed(typ, code uint16, val int32) []hal.InputEvent {
	v := int(val)
	switch {
	case typ == evAbs && code == absMTSlot:
		s.slotted, s.slot = true, min(max(v, 0), maxContacts-1)
	case typ == evAbs && code == absMTTrackID && s.slotted:
		s.b[s.slot].id = v
	case typ == evAbs && code == absMTX:
		s.b[s.slot].x, s.ax, s.axy = v, v, true
	case typ == evAbs && code == absMTY:
		s.b[s.slot].y, s.ay, s.axy = v, v, true
	case typ == evAbs && code == absX:
		s.x = v
	case typ == evAbs && code == absY:
		s.y = v
	case typ == evKey && code == btnTouch:
		s.btn = val != 0
	case typ == evSyn && code == synMT: // protocol A from here
		s.slotted = false
		if s.axy {
			s.a = append(s.a, image.Pt(s.ax, s.ay))
		}
		s.axy, s.mtReport = false, true
	case typ == evSyn && code == synReport:
		return s.report()
	}
	return nil
}

// report ends a report: the contacts now against the last report's.
func (s *touchState) report() []hal.InputEvent {
	var cur [maxContacts]struct {
		on   bool
		x, y int
	}
	switch {
	case s.slotted:
		for i, c := range s.b {
			cur[i].on, cur[i].x, cur[i].y = c.id >= 0, c.x, c.y
		}
	case s.mtReport:
		if s.axy {
			s.a = append(s.a, image.Pt(s.ax, s.ay))
		}
		for i, p := range s.a {
			if i < maxContacts {
				cur[i].on, cur[i].x, cur[i].y = s.btn || len(s.a) > 0, p.X, p.Y
			}
		}
	default: // single touch: BTN_TOUCH and the last position
		if s.axy {
			s.x, s.y = s.ax, s.ay
		}
		cur[0].on, cur[0].x, cur[0].y = s.btn, s.x, s.y
	}
	s.a, s.axy, s.mtReport = s.a[:0], false, false
	var out []hal.InputEvent
	for i := range cur {
		c, p := cur[i], s.prev[i]
		ev := hal.InputEvent{Source: hal.FromTouch, Kind: hal.TouchEvent, Contact: i, TouchX: c.x, TouchY: c.y, Down: true}
		switch {
		case c.on && !p.on:
		case !c.on && p.on:
			ev.TouchX, ev.TouchY, ev.Down = p.x, p.y, false
		case c.on && max(abs(c.x-p.x), abs(c.y-p.y)) >= moveMinPx:
			ev.Gesture = hal.TouchMove
		default:
			if c.on { // a jitter: the last reported place stays
				cur[i].x, cur[i].y = p.x, p.y
			}
			continue
		}
		out = append(out, ev)
	}
	s.prev = cur
	return out
}

// abs is |v|.
func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func (p *Panel) readTouch(fd int, log func(string, ...any)) {
	s := newTouchState()
	var buf [24]byte // struct input_event on arm64: timeval, type, code, value
	for {
		n, err := syscall.Read(fd, buf[:])
		if errors.Is(err, syscall.EINTR) || errors.Is(err, syscall.EAGAIN) {
			continue
		}
		if err != nil || n != len(buf) {
			log("touch screen: read %d bytes: %v", n, err)
			return
		}
		le := binary.LittleEndian
		for _, ev := range s.feed(le.Uint16(buf[16:]), le.Uint16(buf[18:]), int32(le.Uint32(buf[20:]))) { //nolint:gosec // G115: an evdev value is a signed 32-bit field
			if ev.Gesture == hal.TouchMove {
				p.send(ev) // a move may be dropped: the next one carries the place
				continue
			}
			p.touches.Add(1)
			if ev.Down {
				p.click()
			}
			p.sendWait(ev)
		}
	}
}

// SetTouch suspends or resumes the touch controller (spec 08 §7).
func SetTouch(on bool) error {
	fd, err := syscall.Open(TouchCtl, syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = syscall.Close(fd) }()
	req := uintptr(0x900)
	if on {
		req = 0x901
	}
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, 0); e != 0 {
		return e
	}
	return nil
}

// TTC0 counter 1 registers (spec 08 §8).
const (
	ttcClock1    = 0x00
	ttcCounter1  = 0x0C
	ttcInterval1 = 0x24
	ttcMatch1    = 0x30
)

// The beeps of spec 08 §8.2.
const (
	Click     = 1700 * time.Microsecond
	FailAlarm = 50 * time.Millisecond
	LongAlarm = 120 * time.Millisecond
)

// Beeper is the buzzer on TTC0 counter 1.
type Beeper struct {
	mu  sync.Mutex
	ttc mmio.Bus
	on  bool
}

// NewBeeper initialises the beeper's timer (spec 08 §8.1) and leaves it
// silent.
func NewBeeper(ttc mmio.Bus) *Beeper {
	b := &Beeper{ttc: ttc}
	b.field(1<<0, 1)
	ttc.Write32(ttcClock1, 0)
	b.field(1<<5, 0)
	b.field(1<<3, 1<<3)
	b.field(1<<1, 1<<1)
	ttc.Write32(ttcInterval1, 27770)
	ttc.Write32(ttcMatch1, 13885)
	b.field(1<<0, 0)
	b.field(1<<0, 1)
	return b
}

// field is a read-modify-write of Counter_Control_1 (a PS timer whose
// other bits must be kept).
func (b *Beeper) field(mask, v uint32) {
	b.ttc.Write32(ttcCounter1, b.ttc.Read32(ttcCounter1)&^mask|v&mask)
}

// Beep sounds for d, from any goroutine; the stop runs on a timer.
func (b *Beeper) Beep(d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.on {
		return
	}
	b.on = true
	b.field(1, 0)
	time.AfterFunc(d, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.field(1, 1)
		b.on = false
	})
}
