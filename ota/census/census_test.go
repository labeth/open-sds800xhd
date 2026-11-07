package census

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type fakeMem map[uint64]uint32

func (m fakeMem) Read32(p uint64) (uint32, error) {
	v, ok := m[p]
	if !ok {
		return 0, errors.New("not mapped")
	}
	return v, nil
}

// unit builds a fixture tree: straps, p9 identity record, partitions,
// mounts, fpga_manager and a framebuffer.
func unit(t *testing.T, hwid uint8, model, plState string) Unit {
	t.Helper()
	root := t.TempDir()
	put := func(p, s string) {
		p = filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 8 {
		put("sys/class/gpio/gpio"+strconv.Itoa(334+i)+"/value", strconv.Itoa(int(hwid>>i&1))+"\n")
	}
	if model != "" {
		rec := make([]byte, recordSize)
		copy(rec[0x08:], "Siglent Technologies")
		copy(rec[0xA8:], model)
		put("usr/bin/siglent/firmdata0/sys_cfg.cfg", string(EncodeIdentityRecord(rec)))
	}
	put("proc/partitions", "major minor  #blocks  name\n\n 179        0    7634944 mmcblk0\n 179        9      51200 mmcblk0p9\n 179       12    1048576 mmcblk0p12\n")
	put("proc/mounts", "/dev/mmcblk0p9 /usr/bin/siglent/firmdata0 ext4 ro,relatime 0 0\n/dev/mmcblk0p12 /usr/bin/siglent/usr ext4 rw,relatime 0 0\n")
	if plState != "" {
		put("sys/class/fpga_manager/fpga0/state", plState+"\n")
	}
	put("sys/class/graphics/fb0/bits_per_pixel", "32\n")
	put("sys/class/graphics/fb0/virtual_size", "1024,600\n")
	return Unit{Root: root, Mem: fakeMem{
		csuIDCode: 0x14711093, csuVersion: 3, vdmaVersion: 0x62000050,
		acqVersion: 0x1111, acqVersion + 4: 0x2222, acqVersion + 8: 0x3333,
	}}
}

func verdict(t *testing.T, r *Report, part string) Verdict {
	t.Helper()
	v, ok := r.Verdict(part)
	if !ok {
		t.Fatalf("no verdict for %s in %+v", part, r.Verdicts)
	}
	return v
}

func TestCensusFullUnit(t *testing.T) {
	r, err := Run(context.Background(), unit(t, 0x00, "SDS824X HD", "operating"))
	if err != nil {
		t.Fatal(err)
	}
	id := r.Identity
	if id.Channels != 4 || id.Model != "SDS824X HD" || id.BandwidthMHz != 200 || id.ModelSource != "sys_cfg.cfg" {
		t.Errorf("identity %+v", id)
	}
	if id.Manufacturer != "Siglent Technologies" {
		t.Errorf("manufacturer %q", id.Manufacturer)
	}
	for _, p := range []string{"hw-id", "identity-record", "emmc", "p9", "p12", "soc", "pl", "framebuffer"} {
		if v := verdict(t, r, p); v.Verdict != Present {
			t.Errorf("%s: %+v", p, v)
		}
	}
	if !strings.Contains(verdict(t, r, "soc").Detail, "XCZU2") {
		t.Errorf("soc %+v", verdict(t, r, "soc"))
	}
	if len(r.PLVersion) != 3 || r.PLVersion[2] != 0x3333 {
		t.Errorf("PL version %x", r.PLVersion)
	}
	if _, ok := r.Verdict("model-vs-straps"); ok {
		t.Error("a 4-channel model on 4-channel straps flagged")
	}
}

func TestCensusVariants(t *testing.T) {
	for _, c := range []struct {
		hwid           uint8
		ch             int
		adc, clock     string
		model          string
		wantModelFlags bool
	}{
		{0x80, 2, "ADC12D1000", "original", "", false},
		{0x30, 4, "ADC12DL1500", "new", "SDS814X HD", false},
		{0xB0, 2, "ADC12DL1500", "new", "SDS824X HD", true},
	} {
		r, err := Run(context.Background(), unit(t, c.hwid, c.model, "operating"))
		if err != nil {
			t.Fatal(err)
		}
		id := r.Identity
		if id.Channels != c.ch || id.ADCPart != c.adc || id.ClockChip != c.clock {
			t.Errorf("hwid %#x: %+v", c.hwid, id)
		}
		if c.model == "" && (id.ModelSource != "fallback" || id.Model != "SDS802X HD" || id.BandwidthMHz != 70) {
			t.Errorf("hwid %#x fallback: %+v", c.hwid, id)
		}
		if _, ok := r.Verdict("model-vs-straps"); ok != c.wantModelFlags {
			t.Errorf("hwid %#x model %s: model-vs-straps flagged %v", c.hwid, c.model, ok)
		}
	}
}

// An unconfigured PL is never read: the census must not touch a PL address
// unless fpga_manager reports it operating.
func TestCensusLeavesUnconfiguredPLAlone(t *testing.T) {
	for _, st := range []string{"", "unknown", "write error"} {
		u := unit(t, 0, "SDS824X HD", st)
		reads := 0
		fm, ok := u.Mem.(fakeMem)
		if !ok {
			t.Fatalf("unit memory is %T", u.Mem)
		}
		u.Mem = memCounter{fm, &reads}
		r, err := Run(context.Background(), u)
		if err != nil {
			t.Fatal(err)
		}
		if v := verdict(t, r, "pl"); v.Verdict == Present {
			t.Errorf("state %q: %+v", st, v)
		}
		if reads != 2 { // the two CSU (PS) registers only
			t.Errorf("state %q: %d register reads, want the 2 PS reads", st, reads)
		}
	}
}

type memCounter struct {
	m fakeMem
	n *int
}

func (c memCounter) Read32(p uint64) (uint32, error) {
	if p >= 0x8000_0000 && p < 0x9000_0000 {
		panic("PL read with an unconfigured PL")
	}
	*c.n++
	return c.m.Read32(p)
}

func TestIdentityRecordRoundTrip(t *testing.T) {
	rec := make([]byte, recordSize)
	for i := range rec {
		rec[i] = byte(i * 7)
	}
	got, err := DecodeIdentityRecord(EncodeIdentityRecord(rec))
	if err != nil {
		t.Fatal(err)
	}
	if string(got[4:]) != string(rec[4:]) {
		t.Fatal("round trip changed the record")
	}
	bad := EncodeIdentityRecord(rec)
	bad[100] ^= 1
	if _, err := DecodeIdentityRecord(bad); err == nil {
		t.Fatal("a corrupted record passed the checksum")
	}
}

func TestCensusEmptyUnit(t *testing.T) {
	r, err := Run(context.Background(), Unit{Root: t.TempDir(), Mem: fakeMem{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"hw-id", "framebuffer"} {
		if v := verdict(t, r, p); v.Verdict != Absent {
			t.Errorf("%s: %+v", p, v)
		}
	}
	if r.Identity.Model != "SDS804X HD" {
		t.Errorf("model %q", r.Identity.Model)
	}
}
