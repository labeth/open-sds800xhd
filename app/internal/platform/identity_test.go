package platform

import (
	"encoding/binary"
	"testing"
)

func TestDecodeHWID(t *testing.T) {
	for _, c := range []struct {
		h        uint8
		ch       int
		adc      ADCPart
		newClock bool
	}{
		{0x00, 4, ADC12D1000, false},
		{0x80, 2, ADC12D1000, false},
		{0x10, 4, ADC12DL1500, false},
		{0x30, 4, ADC12DL1500, true},
		{0xB0, 2, ADC12DL1500, true},
	} {
		id := DecodeHWID(c.h)
		if id.Channels != c.ch || id.ADC != c.adc || id.NewClock != c.newClock {
			t.Errorf("%#02x: %v", c.h, id)
		}
	}
}

// encodeIdentityRecord is the inverse of DecodeIdentityRecord (a writer of
// spec 09 §1.2), used to build test records.
func encodeIdentityRecord(r []byte) []byte {
	n := len(r)
	h := n / 2
	r = append([]byte(nil), r...)
	var sum uint32
	for _, b := range r[4:] {
		sum += uint32(b)
	}
	binary.LittleEndian.PutUint32(r[0:4], -sum)
	for p, k := 1, 2; p < n; p, k = p+k, k+1 {
		r[p] = ^r[p]
	}
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

func TestIdentityRecordRoundTrip(t *testing.T) {
	r := make([]byte, identityRecordSize)
	copy(r[0xA8+15*2:], "SDS814X HD")
	f := encodeIdentityRecord(r)
	if m := ModelFromIdentityRecord(f); m != "SDS814X HD" {
		t.Fatalf("model = %q", m)
	}
	f[100] ^= 1
	if m := ModelFromIdentityRecord(f); m != "" {
		t.Fatalf("corrupt record gave model %q", m)
	}
}

func TestBandwidthOf(t *testing.T) {
	for m, bw := range map[string]int{"SDS802X HD": 70, "SDS814X HD": 100, "SDS824X HD": 200, "SDS120X": 0, "SDS8": 0} {
		if got := BandwidthOf(m); got != bw {
			t.Errorf("%s: %d, want %d", m, got, bw)
		}
	}
}
