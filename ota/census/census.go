// Package census is the SDS800X HD's read-only census: what the unit is and
// which of its parts are there, found without changing anything. It is the
// board's half of the diagnostics (hal.Census, offered by the supervisor
// board as hal.CensusBoard), run by the supervisor on demand and by the
// census stick (first contact with a unit) before any of our code has
// written to it.
//
// Read-only means: no file on the eMMC is opened for writing, no PL register
// is written, and no PL register with a read side effect is read (spec 02
// §10, spec 08 §1). The PL is read only when fpga_manager reports it
// operating and the UI VDMA answers its version register (spec 13 §3.3):
// an access to an unconfigured PL hangs the CPU (spec 01 §13). The only
// writes are the sysfs exports that make the hw-id straps readable as GPIO
// inputs (spec 13 §4.1), and only when a strap is not exported yet.
package census

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/labeth/open-sds800xhd/ota/fsguard"
)

// The verdicts a part can get.
const (
	Present = "present"
	Absent  = "absent"
	Partial = "partial"
	Other   = "other"
)

// Verdict is one part's judgement.
type Verdict struct {
	Part    string `json:"part"`
	Verdict string `json:"verdict"`
	Detail  string `json:"detail"`
}

// Identity is what the census learns of the unit's identity (spec 09 §1,
// spec 13 §4.1).
type Identity struct {
	HWID         uint8  `json:"hwid"`
	HWIDBits     int    `json:"hwid_bits"` // straps read; 8 when the byte is complete
	Channels     int    `json:"channels"`
	ADCPart      string `json:"adc_part"`
	ClockChip    string `json:"clock_chip"` // the clock-chip variant the straps select: "original" or "new"
	PCB          int    `json:"pcb"`
	BOM          int    `json:"bom"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model"`
	ModelSource  string `json:"model_source"`
	BandwidthMHz int    `json:"bandwidth_mhz"`
}

// Report is the census's result.
type Report struct {
	Identity  Identity  `json:"identity"`
	Verdicts  []Verdict `json:"verdicts"`
	SoCIDCode uint32    `json:"soc_idcode,omitempty"`
	PLVersion []uint16  `json:"pl_version,omitempty"`
}

// Verdict returns the verdict of part, or false.
func (r *Report) Verdict(part string) (Verdict, bool) {
	for _, v := range r.Verdicts {
		if v.Part == part {
			return v, true
		}
	}
	return Verdict{}, false
}

// WriteJSON writes the report, indented.
func (r *Report) WriteJSON(w io.Writer) error {
	e := json.NewEncoder(w)
	e.SetIndent("", "  ")
	return e.Encode(r)
}

// Mem reads 32-bit physical registers.
type Mem interface {
	Read32(phys uint64) (uint32, error)
}

// Unit is where the census looks: Root prefixes every path ("" on the unit,
// a fixture tree in tests) and Mem reads registers (nil: /dev/mem).
type Unit struct {
	Root string
	Mem  Mem
}

// Paths on the unit (spec 01 §3, §8).
const (
	factoryDir = "/usr/bin/siglent/firmdata0" // p9
	userDir    = "/usr/bin/siglent/usr"       // p12
)

// Physical registers the census reads (all reads without side effects).
const (
	csuIDCode   = 0xFFCA0040 // PS: CSU IDCODE (spec 13 §2)
	csuVersion  = 0xFFCA0044
	vdmaVersion = 0x8000002C // PL: UI VDMA VERSION, the PL presence check (spec 13 §3.3)
	acqVersion  = 0x80030000 // PL: ACQ 0x000–0x002, the PL version words (spec 02 §3)
)

// Run takes the census.
func Run(ctx context.Context, u Unit) (*Report, error) {
	if u.Mem == nil {
		u.Mem = devMem{}
	}
	r := &Report{}
	steps := []func(*Report){u.identity, u.partitions, u.soc, u.pl, u.framebuffer}
	for _, step := range steps {
		if err := ctx.Err(); err != nil {
			return r, err
		}
		step(r)
	}
	return r, nil
}

func (u Unit) path(p string) string { return filepath.Join(u.Root, p) }

// readAttr reads a small file with one read: sysfs and device nodes are not
// given to the runtime poller.
func (u Unit) readAttr(p string) (string, error) {
	fd, err := syscall.Open(u.path(p), syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = syscall.Close(fd) }()
	var b [4096]byte
	n, err := syscall.Read(fd, b[:])
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b[:max(n, 0)])), nil
}

func (u Unit) writeAttr(p, v string) error {
	if err := fsguard.CheckDevice(p); err != nil {
		return err
	}
	fd, err := syscall.Open(u.path(p), syscall.O_WRONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = syscall.Close(fd) }()
	_, err = syscall.Write(fd, []byte(v))
	return err
}

// strap reads hw-id bit i (sysfs GPIO 334 + i), exporting it as an input
// only when it is not exported yet.
func (u Unit) strap(i int) (int, error) {
	n := strconv.Itoa(334 + i)
	dir := "/sys/class/gpio/gpio" + n
	if _, err := u.readAttr(dir + "/value"); err != nil {
		if err := u.writeAttr("/sys/class/gpio/export", n); err != nil {
			return 0, err
		}
		_ = u.writeAttr(dir+"/direction", "in")
	}
	s, err := u.readAttr(dir + "/value")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(s)
}

func (u Unit) identity(r *Report) {
	id := &r.Identity
	var missing []int
	for i := range 8 {
		v, err := u.strap(i)
		if err != nil {
			missing = append(missing, i)
			continue
		}
		id.HWIDBits++
		if v != 0 {
			id.HWID |= 1 << i
		}
	}
	h := id.HWID
	id.Channels, id.ADCPart, id.ClockChip = 4, "ADC12D1000", "original"
	id.PCB, id.BOM = int(h&0xF), int(h>>4)
	if h&0x80 != 0 {
		id.Channels = 2
	}
	if h&0x10 != 0 {
		id.ADCPart = "ADC12DL1500"
	}
	if h&0x30 == 0x30 {
		id.ClockChip = "new"
	}
	switch id.HWIDBits {
	case 8:
		r.add("hw-id", Present, fmt.Sprintf("%#02x: %d channels, %s, %s clock chip, pcb %d, bom %d",
			h, id.Channels, id.ADCPart, id.ClockChip, id.PCB, id.BOM))
	case 0:
		r.add("hw-id", Absent, "no strap readable as sysfs GPIO 334–341")
	default:
		r.add("hw-id", Partial, fmt.Sprintf("straps %v unreadable; the byte reads %#02x with them 0", missing, h))
	}

	files := []string{fmt.Sprintf("sys_cfg_%dch.cfg", id.Channels), "sys_cfg.cfg"}
	for _, f := range files {
		b, err := os.ReadFile(u.path(filepath.Join(factoryDir, f)))
		if err != nil {
			continue
		}
		rec, err := DecodeIdentityRecord(b)
		if err != nil {
			r.add("identity-record", Other, f+": "+err.Error())
			continue
		}
		if m := modelOf(rec); m != "" && BandwidthOf(m) != 0 {
			id.Model, id.ModelSource, id.BandwidthMHz = m, f, BandwidthOf(m)
			id.Manufacturer = cString(rec[0x08:0x28])
			r.add("identity-record", Present, fmt.Sprintf("%s: %s %s, %d MHz", f, id.Manufacturer, m, id.BandwidthMHz))
			break
		}
		r.add("identity-record", Other, f+": no SDS8 model in the model table")
	}
	if id.Model == "" {
		id.Model, id.ModelSource, id.BandwidthMHz = fmt.Sprintf("SDS80%dX HD", id.Channels), "fallback", 70
		if _, ok := r.Verdict("identity-record"); !ok {
			r.add("identity-record", Absent, "no identity record on p9; model from the straps, 70 MHz")
		}
	}
	if len(id.Model) > 5 && id.Model[5] != byte('0'+id.Channels) { //nolint:gosec // G115: the straps give 2 or 4 channels
		r.add("model-vs-straps", Other, fmt.Sprintf("model %s but the straps say %d channels", id.Model, id.Channels))
	}
}

func (u Unit) partitions(r *Report) {
	b, err := u.readAttr("/proc/partitions")
	if err != nil {
		r.add("emmc", Other, "no /proc/partitions: "+err.Error())
	} else {
		var parts []string
		size := map[string]int64{}
		for l := range strings.SplitSeq(b, "\n") {
			f := strings.Fields(l)
			if len(f) == 4 && strings.HasPrefix(f[3], "mmcblk0") {
				parts = append(parts, f[3])
				size[f[3]], _ = strconv.ParseInt(f[2], 10, 64)
			}
		}
		switch {
		case len(parts) == 0:
			r.add("emmc", Absent, "no mmcblk0 in /proc/partitions")
		case size["mmcblk0p9"] == 0 || size["mmcblk0p12"] == 0:
			r.add("emmc", Partial, "partitions "+strings.Join(parts, " ")+": p9 or p12 missing")
		default:
			r.add("emmc", Present, fmt.Sprintf("%d partitions; p9 %d KiB, p12 %d KiB", len(parts)-1,
				size["mmcblk0p9"], size["mmcblk0p12"]))
		}
	}
	m, err := u.readAttr("/proc/mounts")
	if err != nil {
		r.add("mounts", Other, "no /proc/mounts: "+err.Error())
		return
	}
	mounts := map[string][]string{}
	for l := range strings.SplitSeq(m, "\n") {
		if f := strings.Fields(l); len(f) >= 4 {
			mounts[f[1]] = f
		}
	}
	check := func(part, dir, wantMode string) {
		f, ok := mounts[dir]
		if !ok {
			r.add(part, Absent, dir+" not mounted")
			return
		}
		mode := "rw"
		if strings.HasPrefix(f[3], "ro") || strings.Contains(f[3], ",ro") {
			mode = "ro"
		}
		v := Present
		if mode != wantMode {
			v = Other
		}
		r.add(part, v, fmt.Sprintf("%s on %s (%s, %s)", f[0], dir, f[2], mode))
	}
	check("p9", factoryDir, "ro")
	check("p12", userDir, "rw")
}

func (u Unit) soc(r *Report) {
	id, err := u.Mem.Read32(csuIDCode)
	if err != nil {
		r.add("soc", Other, "CSU IDCODE unreadable: "+err.Error())
		return
	}
	ver, _ := u.Mem.Read32(csuVersion)
	r.SoCIDCode = id
	switch id & 0x0FFF_FFFF {
	case 0x04711093:
		r.add("soc", Present, fmt.Sprintf("XCZU2 (IDCODE %#08x, version %#x)", id, ver))
	case 0x04710093:
		r.add("soc", Present, fmt.Sprintf("XCZU3 (IDCODE %#08x, version %#x)", id, ver))
	default:
		r.add("soc", Other, fmt.Sprintf("IDCODE %#08x is neither ZU2 nor ZU3", id))
	}
}

func (u Unit) pl(r *Report) {
	st, err := u.readAttr("/sys/class/fpga_manager/fpga0/state")
	if err != nil {
		r.add("pl", Other, "fpga_manager state unreadable; the PL is not read")
		return
	}
	if st != "operating" {
		r.add("pl", Absent, "fpga_manager state "+st+"; the PL is not read")
		return
	}
	v, err := u.Mem.Read32(vdmaVersion)
	if err != nil || v == 0 || v == 0xFFFF_FFFF {
		r.add("pl", Absent, fmt.Sprintf("fpga_manager operating but the UI VDMA version reads %#x (%v)", v, err))
		return
	}
	for i := range 3 {
		w, err := u.Mem.Read32(acqVersion + 4*uint64(i))
		if err != nil {
			break
		}
		r.PLVersion = append(r.PLVersion, uint16(w)) //nolint:gosec // G115: the PL version registers are 16 bits wide in 32-bit bus slots
	}
	d := fmt.Sprintf("operating, UI VDMA version %#x", v)
	if len(r.PLVersion) == 3 {
		d += fmt.Sprintf(", PL version %04x-%04x-%04x", r.PLVersion[2], r.PLVersion[1], r.PLVersion[0])
	}
	r.add("pl", Present, d)
}

func (u Unit) framebuffer(r *Report) {
	var found []string
	for i := range 4 {
		dir := fmt.Sprintf("/sys/class/graphics/fb%d", i)
		bpp, err := u.readAttr(dir + "/bits_per_pixel")
		if err != nil {
			continue
		}
		vs, _ := u.readAttr(dir + "/virtual_size")
		found = append(found, fmt.Sprintf("fb%d %s bpp %s", i, bpp, vs))
	}
	_, devErr := os.Stat(u.path("/dev/fb0"))
	switch {
	case len(found) > 0:
		r.add("framebuffer", Present, strings.Join(found, "; "))
	case devErr == nil:
		r.add("framebuffer", Partial, "/dev/fb0 exists but sysfs graphics reports no framebuffer")
	default:
		r.add("framebuffer", Absent, "no /dev/fb0 and no sysfs framebuffer")
	}
}

func (r *Report) add(part, verdict, detail string) {
	r.Verdicts = append(r.Verdicts, Verdict{part, verdict, detail})
}

// devMem reads physical registers through /dev/mem, one page mapped per
// read and unmapped again.
type devMem struct{}

func (devMem) Read32(phys uint64) (uint32, error) {
	fd, err := syscall.Open("/dev/mem", syscall.O_RDONLY|syscall.O_SYNC|syscall.O_CLOEXEC, 0)
	if err != nil {
		return 0, err
	}
	defer func() { _ = syscall.Close(fd) }()
	const page = 4096
	base := phys &^ (page - 1)
	m, err := syscall.Mmap(fd, int64(base), page, syscall.PROT_READ, syscall.MAP_SHARED) //nolint:gosec // G115: physical addresses on this SoC are 32-bit
	if err != nil {
		return 0, err
	}
	defer func() { _ = syscall.Munmap(m) }()
	return load32(m, int(phys-base)), nil //nolint:gosec // G115: an offset within one page
}
