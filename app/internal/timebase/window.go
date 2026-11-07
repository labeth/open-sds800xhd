package timebase

// Readout is one record readout window of spec 03 §4.3: the registers that
// select it and where the screen samples land in what the DMA delivers.
type Readout struct {
	O, L, N int // offset (Δ units), length (4-sample units), samples transferred per channel
	Lead    int // λ, interpolated samples
	Comp    int // 0x0D5 compression rate r (screen-record reads)
	Factor  int // compression factor c_f
	Screen  int // n, screen samples covered
	First   int // k, the first screen sample
	Piece   bool
	// Pieces: piece reads only. Piece 0 transfers Head + PieceSize samples,
	// every further piece PieceSize samples.
	Pieces, Head int
}

// PieceSize is M of spec 03 §4.3.
const PieceSize = 5_000_000

// ReadoutWindow is the screen-record window (k = 0, n = S) for the frame's
// position and the trigger fine term t = ⌊φ/I⌋.
func (h Set) ReadoutWindow(pos Position, t int) Readout { return h.Window(pos, t, 0, int(h.S), false) }

// Window is the window of n screen samples from screen sample k ≥ 0 (spec 03
// §4.3, window arithmetic). A screen-record read (piece false) compresses
// by c_f = 2r once n ≥ 2·M; a piece read is uncompressed and moves the
// record in pieces of M samples, of which the caller reads only the pieces
// it needs.
func (h Set) Window(pos Position, t, k, n int, piece bool) Readout {
	w := Readout{Factor: 1, Screen: n, First: k, Piece: piece}
	if !piece {
		if x := float64(n) / PieceSize; x > 1 {
			if r := int(x / 2); r > 0 && r < 0x1000 {
				w.Comp, w.Factor = r, 2*r
			}
		}
	}
	if n/PieceSize >= 2 && n%PieceSize != 0 && w.Comp == 0 && !piece {
		n = n / PieceSize * PieceSize
		w.Screen = n
	}
	dI := h.Delta * h.I
	G := 100/h.Delta + 1
	e := int(pos.P-float64(pos.PIdx*dI)) - pos.TrigRemainder + k%h.Delta
	O := k/h.Delta - G + pos.PIdx
	lead := h.I*G*h.Delta + e
	if O >= 2 {
		O--
		lead += h.I * h.Delta
	}
	w.O, w.Lead = O, lead
	if piece {
		// L is that of the record (k = 0, n = S) and the stream runs on
		// piece after piece (spec 03 §4.3, long records).
		w.L = (lead/h.I+int(h.S)+300+t+G*h.Delta)/4 + 20
		w.Head = (lead/h.I + t + 150 + 7) &^ 7
		w.Pieces = max(1, (lead/h.I+t+n-w.Head+PieceSize-1)/PieceSize)
		w.N = w.Head + w.Pieces*PieceSize
		return w
	}
	w.L = (lead/h.I+n+300+t+G*h.Delta)/4 + 20
	w.N = min(lead/h.I+n+150+t, 4*w.L) / w.Factor
	return w
}

// ScreenAt is the buffer index of screen sample First in what the window's
// transfer delivered, in delivered samples.
func (w Readout) ScreenAt(i, t int) int { return (w.Lead/i + t) / w.Factor }

// Roll is the roll timebase set of spec 04 §7.
type Roll struct {
	P, Lr  int // roll point count (14-bit) and its 4-sample length
	Scan   int // 0x010[11:0] and 0x005: P/u
	Offset int // roll full offset o (0x050/0x051)
}

// RollSet computes the roll timebase set of h.
func (h Set) RollSet() Roll {
	P := int(h.S) / 1000
	if P%4 != 0 {
		P = (4 * P) & 0x3FFF
	}
	r := Roll{P: P, Lr: (P + 3) / 4, Scan: P / h.U}
	half := (h.Delta*h.D - int(h.S)) / 2
	o := half / h.Delta
	if half-o*h.Delta > 32 {
		o++
	}
	r.Offset = o
	return r
}
