package trigger

// Standard is a video standard (spec 05 §5.6).
type Standard int

// The video standards.
const (
	NTSC Standard = iota
	PAL
	P720L50
	P720L60
	P1080L50
	P1080L60
	I1080L50
	I1080L60
	Custom
)

// VideoSet is a video trigger's standard and line selection.
type VideoSet struct {
	Standard Standard
	Line     int  // 1-based UI line
	Field    int  // 1-based UI field
	AnyLine  bool // sync on every line
	// Custom: lines per frame, frame rate code (0 25 Hz, 1 30 Hz, 2 50 Hz,
	// 3 60 Hz), interlace 1/2/4/8, fields 1/2/4/8.
	Lines, Rate, Interlace, Fields int
}

// linePeriod is the line period in 64 ns units of the built-in standards.
var linePeriod = [Custom]int{992, 1000, 416, 347, 277, 231, 555, 462}

// VideoConfig is A 0x017: the active-field code over the line period,
// clamped to 10 bits and composed whole (§5.6).
func VideoConfig(v VideoSet) uint16 {
	var period int
	var af uint16
	if v.Standard == Custom {
		rate := [4]int{25, 30, 50, 60}[max(0, min(3, v.Rate))]
		period = 15_625_000 / rate / max(1, v.Lines)
		af = interlaceCode(v.Interlace)
	} else {
		period = linePeriod[v.Standard]
		switch v.Standard {
		case NTSC, PAL, I1080L50, I1080L60:
			af = 1
		case P720L50, P720L60, P1080L50, P1080L60, Custom:
			// progressive: no field flag
		}
	}
	return af<<12 | uint16(min(period, 1023)) //nolint:gosec // G115: the line period is positive and at most 1023
}

func interlaceCode(i int) uint16 {
	switch i {
	case 2:
		return 1
	case 4:
		return 2
	case 8:
		return 3
	}
	return 0
}

// geometry is adj, F and M(f) of the §5.6 table for UI field f.
func geometry(v VideoSet, f int) (adj, fields, lines int) {
	two := 0
	if f == 2 {
		two = 1
	}
	switch v.Standard {
	case NTSC:
		return 4, 2, 262 + two
	case PAL:
		return 4, 2, 312 + two
	case P720L50, P720L60:
		return 2, 1, 750
	case P1080L50, P1080L60:
		return 2, 1, 1125
	case I1080L50, I1080L60:
		return 1 + two, 2, 562 + two
	case Custom:
		// the user's geometry below
	}
	il := max(1, v.Interlace)
	return 0, max(1, v.Fields), (v.Lines + il - 1) / il
}

// VideoLine is A 0x016: the FPGA field and line of the UI line and field,
// and the any-line bit (§5.6).
func VideoLine(v VideoSet) uint16 {
	_, n, _ := geometry(v, 1)
	f := max(1, min(n, v.Field))
	adj, n, m := geometry(v, f)
	var line, field int
	if v.Line > adj {
		line = v.Line - adj - 1
		if f < n {
			field = f
		}
	} else {
		line = m + v.Line - adj - 1
		field = f - 1
	}
	return uint16(field<<12|line) | b2u(v.AnyLine)<<11 //nolint:gosec // G115: the field and line fit the video trigger word's 4 and 11 bits
}

func (t *Trigger) writeVideo() {
	t.a.Set(0x017, VideoConfig(t.s.Video))
	t.a.Set(0x016, VideoLine(t.s.Video))
}
