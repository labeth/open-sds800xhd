package census

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"sync/atomic"
	"unsafe"
)

// recordSize is the size of the identity record (spec 09 §1.2).
const recordSize = 0xCF8

// DecodeIdentityRecord undoes the encoding of the identity record on p9 and
// checks its checksum (spec 09 §1.2).
func DecodeIdentityRecord(f []byte) ([]byte, error) {
	n := len(f)
	if n != recordSize {
		return nil, fmt.Errorf("identity record: %d bytes, want %d", n, recordSize)
	}
	h := n / 2
	t := bytes.Clone(f)
	for i := range n / 4 {
		t[i], t[h-1-i] = t[h-1-i], t[i]
		t[h+i], t[n-1-i] = t[n-1-i], t[h+i]
	}
	r := make([]byte, n)
	copy(r[:h], t[h:])
	copy(r[h:], t[:h])
	for i := h; i < n; i++ {
		r[i] = ^r[i]
	}
	for p, k := 1, 2; p < n; p, k = p+k, k+1 {
		r[p] = ^r[p]
	}
	sum := binary.LittleEndian.Uint32(r[0:4])
	for _, b := range r[4:] {
		sum += uint32(b)
	}
	if sum != 0 {
		return r, fmt.Errorf("identity record: checksum %#x", sum)
	}
	return r, nil
}

// EncodeIdentityRecord is the inverse of DecodeIdentityRecord; it sets the
// checksum. It exists for test fixtures.
func EncodeIdentityRecord(rec []byte) []byte {
	n := len(rec)
	r := bytes.Clone(rec)
	var sum uint32
	for _, b := range r[4:] {
		sum += uint32(b)
	}
	binary.LittleEndian.PutUint32(r[0:4], -sum)
	for p, k := 1, 2; p < n; p, k = p+k, k+1 {
		r[p] = ^r[p]
	}
	h := n / 2
	for i := h; i < n; i++ {
		r[i] = ^r[i]
	}
	t := make([]byte, n)
	copy(t[h:], r[:h])
	copy(t[:h], r[h:])
	for i := range n / 4 {
		t[i], t[h-1-i] = t[h-1-i], t[i]
		t[h+i], t[n-1-i] = t[n-1-i], t[h+i]
	}
	return t
}

// modelOf returns the first model name of the record's first model-name
// table (ten 15-byte slots at 0xA8); which slot a unit selects is not known
// (spec 09 §1.2 Open).
func modelOf(r []byte) string {
	for s := range 10 {
		if m := cString(r[0xA8+15*s : 0xA8+15*s+15]); strings.HasPrefix(m, "SDS") {
			return m
		}
	}
	return ""
}

// BandwidthOf derives the bandwidth from the model number (spec 09 §1.2):
// SDS80xX 70 MHz, SDS81xX 100 MHz, SDS82xX 200 MHz; 0 for anything else.
func BandwidthOf(m string) int {
	if len(m) < 6 || !strings.HasPrefix(m, "SDS8") {
		return 0
	}
	switch m[4] {
	case '0':
		return 70
	case '1':
		return 100
	case '2':
		return 200
	}
	return 0
}

func cString(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return strings.TrimSpace(string(b))
}

// load32 is one aligned 32-bit load from a register mapping.
func load32(m []byte, off int) uint32 {
	return atomic.LoadUint32((*uint32)(unsafe.Pointer(&m[off]))) //nolint:gosec // G103: a register in the page mapped from /dev/mem
}
