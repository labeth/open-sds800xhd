package platform

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ADCPart is the converter fitted to the board (spec 06 §9.1).
type ADCPart int

// The two ADC parts. The numbers are the value of ACQ 0x1FB (spec 02 §3).
const (
	ADC12D1000  ADCPart = 0
	ADC12DL1500 ADCPart = 1
)

func (p ADCPart) String() string {
	if p == ADC12DL1500 {
		return "ADC12DL1500"
	}
	return "ADC12D1000"
}

// Identity is what the app learns about the unit before it touches the
// hardware (spec 02 §3, spec 09 §1).
type Identity struct {
	HWID         uint8   // GPIO straps 334…341, bit n = GPIO 334+n
	Channels     int     // 2 or 4, from HWID bit 7
	ADC          ADCPart // HWID bit 4
	NewClock     bool    // HWID bits 5:4 both set: the newer clock-chip sequence
	PCB, BOM     int     // HWID bits 3:0 and 7:4
	Manufacturer string  // from the identity record
	Model        string  // e.g. "SDS824X HD"
	ModelSource  string  // where Model came from
	BandwidthMHz int     // 70, 100 or 200, from the model number
}

func (id Identity) String() string {
	return fmt.Sprintf("hwid=%#02x channels=%d adc=%s newclock=%v pcb=%d bom=%d model=%q (%s) bandwidth=%dMHz",
		id.HWID, id.Channels, id.ADC, id.NewClock, id.PCB, id.BOM, id.Model, id.ModelSource, id.BandwidthMHz)
}

// hwidGPIO is the sysfs number of HWID bit 0 (spec 02 §3).
const hwidGPIO = 334

// ReadHWID reads the eight strap lines.
func ReadHWID() (uint8, error) {
	var h uint8
	for i := range 8 {
		v, err := GPIOIn(hwidGPIO + i)
		if err != nil {
			return 0, fmt.Errorf("hw-id bit %d: %w", i, err)
		}
		if v != 0 {
			h |= 1 << i
		}
	}
	return h, nil
}

// DecodeHWID fills the strap-derived fields of an identity (spec 02 §3).
func DecodeHWID(h uint8) Identity {
	id := Identity{HWID: h, Channels: 4, PCB: int(h & 0xF), BOM: int(h >> 4), NewClock: h&0x30 == 0x30}
	if h&0x80 != 0 {
		id.Channels = 2
	}
	if h&0x10 != 0 {
		id.ADC = ADC12DL1500
	}
	return id
}

// ResolveModel sets Model and BandwidthMHz in the resolution order of spec 09
// §1.2: the identity record on p9, else the conservative fallback (70 MHz,
// the channel count from the straps). The fallback is our choice; a
// placeholder model is never reported.
func (id *Identity) ResolveModel(factoryDir string) {
	names := []string{fmt.Sprintf("sys_cfg_%dch.cfg", id.Channels), "sys_cfg.cfg"}
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(factoryDir, n)) //nolint:gosec // G304: a factory configuration file of a fixed name
		if err != nil {
			continue
		}
		if m := ModelFromIdentityRecord(b); m != "" && BandwidthOf(m) != 0 {
			id.Model, id.ModelSource = m, n
			id.Manufacturer = ManufacturerFromIdentityRecord(b)
			id.BandwidthMHz = BandwidthOf(m)
			return
		}
	}
	id.Model = fmt.Sprintf("SDS80%dX HD", id.Channels)
	id.ModelSource = "fallback"
	id.BandwidthMHz = 70
}

// BandwidthOf derives the bandwidth from a model number: SDS80xX → 70 MHz,
// SDS81xX → 100 MHz, SDS82xX → 200 MHz; 0 if m is not such a model.
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

// identityRecordSize is the size of the identity record (spec 09 §1.2).
const identityRecordSize = 0xCF8

// DecodeIdentityRecord undoes the encoding of the identity record and checks
// its checksum (spec 09 §1.2).
func DecodeIdentityRecord(f []byte) ([]byte, error) {
	n := len(f)
	if n != identityRecordSize {
		return nil, fmt.Errorf("identity record: %d bytes, want %d", n, identityRecordSize)
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

// ModelFromIdentityRecord returns the first model name in the record's first
// model-name table (ten 15-byte slots at 0xA8). Which slot a real unit
// selects is not known (spec 09 §1.2 Open); the first non-empty one is
// taken.
func ModelFromIdentityRecord(f []byte) string {
	r, err := DecodeIdentityRecord(f)
	if err != nil {
		return ""
	}
	for s := range 10 {
		b := r[0xA8+15*s : 0xA8+15*s+15]
		if i := bytes.IndexByte(b, 0); i >= 0 {
			b = b[:i]
		}
		if m := strings.TrimSpace(string(b)); strings.HasPrefix(m, "SDS") {
			return m
		}
	}
	return ""
}

// ManufacturerFromIdentityRecord returns the manufacturer string at 0x08 of
// the record (spec 09 §1.2), or "".
func ManufacturerFromIdentityRecord(f []byte) string {
	r, err := DecodeIdentityRecord(f)
	if err != nil {
		return ""
	}
	b := r[0x08:0x28]
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return strings.TrimSpace(string(b))
}

// VersionPrefix is the three system components of the instrument version
// (spec 11 §6.6): the boot, rootfs and kernel versions, from /proc/cmdline,
// /etc/version and /proc/version.
func VersionPrefix() string {
	field := func(path, key, stop string) string {
		b, err := os.ReadFile(path) //nolint:gosec // G304: one of the fixed system files below
		if err != nil {
			return ""
		}
		s := string(b)
		i := strings.Index(s, key)
		if i < 0 {
			return ""
		}
		s = s[i+len(key):]
		if j := strings.IndexAny(s, stop); j >= 0 {
			s = s[:j]
		}
		return s
	}
	boot := field("/proc/cmdline", "boot_version=", " \"\n")
	rootfs := field("/etc/version", "rootfs_version=", " \n")
	kernel := field("/proc/version", "siglent_version=", "@ \n")
	if kernel == "" {
		kernel = "0"
	}
	return boot + "." + rootfs + "." + kernel
}
