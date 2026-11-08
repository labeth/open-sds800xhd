package hdguard

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labeth/open-dso-core/fsguard"
)

// useRoot makes a fresh temporary directory the writable root for one test.
func useRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	restore, err := FS.SetRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	return dir
}

func refused(err error) bool { return errors.Is(err, fsguard.ErrRefused) }

// TRLC-LINKS: REQ-HD-016
func TestCheckInsideOutside(t *testing.T) {
	root := useRoot(t)
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "open"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "open", "away")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "f"), filepath.Join(root, "open", "away-file")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "open"), filepath.Join(root, "inner")); err != nil {
		t.Fatal(err)
	}
	ok := []string{
		root,
		filepath.Join(root, "extra.sh"),
		filepath.Join(root, "open", "a", "app"),
		filepath.Join(root, "open", "not", "yet", "there"),
		filepath.Join(root, "open", "..", "open", "current"),
		filepath.Join(root, "config", "settings.xml"),
	}
	for _, p := range ok {
		if err := FS.Check(p); err != nil {
			t.Errorf("%s refused: %v", p, err)
		}
	}
	bad := []string{
		"",
		"/",
		filepath.Dir(root),
		filepath.Join(root, ".."),
		filepath.Join(root, "..", "x"),
		filepath.Join(root, "open", "..", "..", "x"),
		outside,
		filepath.Join(root, "open", "away", "x"), // a symlinked directory leading out
		filepath.Join(root, "open", "away", "new", "y"), // ... with parts that do not exist yet
		filepath.Join(root, "open", "away-file"),        // a symlinked file leading out
		filepath.Join(root, "inner", "current"),         // a symlink that stays inside: no link on p12 is written through
		filepath.Join(root, "updatefile", "BOOT.bin"),
		filepath.Join(root, "updatefile", "rootfs.ext4.gz"),
		filepath.Join(root, "usr", "upgrade", "update.sh"),
		filepath.Join(root, "uboot.env"),
		filepath.Join(root, "open", "UBOOT.ENV"),
		filepath.Join(root, "open", "uEnv.txt"),
		filepath.Join(root, "open", "boot.bin.new"),
		filepath.Join(root, "huluwa_udiskEnv.txt"),
		"/dev/mmcblk0",
		"/dev/mmcblk0p1",
		"/dev/mmcblk0p9",
		"/dev/mmcblk0boot0",
		"/sys/bus/nvmem/devices/zynqmp-nvmem0/nvmem",
		"/usr/bin/siglent/firmdata0/NSP_system_info.xml",
		"/usr/bin/siglent/main.app",
		"/mnt/logo/logo.bmp",
		"/lib/firmware/x.bin",
	}
	for _, p := range bad {
		err := FS.Check(p)
		if !refused(err) {
			t.Errorf("%q not refused: %v", p, err)
		}
	}
}

// TRLC-LINKS: REQ-HD-016
func TestDefaultRootIsP12(t *testing.T) {
	if FS.Root() != P12 {
		t.Fatalf("default root %s", FS.Root())
	}
	for _, p := range []string{"/usr/bin/siglent/usr/open/current", "/usr/bin/siglent/usr/extra.sh"} {
		if err := FS.Check(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	for _, p := range []string{
		"/usr/bin/siglent/usr/../firmdata0/x", "/usr/bin/siglent/config/x", "/tmp/x",
		"/usr/bin/siglent/usr/updatefile/BOOT.bin",
	} {
		if !refused(FS.Check(p)) {
			t.Errorf("%s not refused", p)
		}
	}
}

// TRLC-LINKS: REQ-HD-016
func TestSetRootRefusesVendorAreas(t *testing.T) {
	for _, d := range []string{
		"/", "/usr", "/usr/bin/siglent", "/usr/bin/siglent/firmdata0", "/dev", "/sys/class",
		"/mnt/logo", "/etc", "/lib/firmware", "",
	} {
		if r, err := FS.SetRoot(d); err == nil {
			r()
			t.Errorf("root %q accepted", d)
		}
	}
	for _, d := range []string{P12, P12 + "/open", "/mnt", t.TempDir()} {
		r, err := FS.SetRoot(d)
		if err != nil {
			t.Errorf("root %q: %v", d, err)
			continue
		}
		if FS.Root() != d {
			t.Errorf("root %q is %q", d, FS.Root())
		}
		r()
	}
	if FS.Root() != P12 {
		t.Fatalf("restore left %s", FS.Root())
	}
}

// TRLC-LINKS: REQ-HD-016
func TestCheckDevice(t *testing.T) {
	ok := []string{
		"/sys/class/gpio/export", "/sys/class/gpio/gpio334/direction", "/dev/spidev1.0",
		"/sys/class/fpga_manager/fpga0/firmware", "/sys/class/fpga_manager/fpga0/flags",
		"/sys/module/firmware_class/parameters/path", "/dev/siglent_touch", "/dev/simd_mark",
	}
	for _, p := range ok {
		if err := FS.CheckDevice(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	bad := []string{
		"/dev/mmcblk0", "/dev/mmcblk0p1", "/dev/mmcblk0p5", "/dev/mmcblk0p9", "/dev/mmcblk0p12",
		"/dev/mmcblk0boot1", "/dev/mtd0", "/dev/sda", "/dev/disk/by-id/x",
		"/sys/bus/nvmem/devices/zynqmp-nvmem0/nvmem", "/sys/class/nvmem/x", "/dev/xilinx_efuse",
		"/sys/devices/platform/firmware:zynqmp-firmware/nvmem/efuse", "/sys/firmware/zynqmp/pggs0",
		"/sys/block/mmcblk0/force_ro", "/sys/class/block/mmcblk0p1/ro", "/dev/../dev/mmcblk0p7", "/dev/uboot.env",
	}
	for _, p := range bad {
		if !refused(FS.CheckDevice(p)) {
			t.Errorf("%s not refused", p)
		}
	}
}

// TRLC-LINKS: REQ-HD-016
func TestCheckPhys(t *testing.T) {
	for _, p := range []uint64{0x8000_0000, 0x8003_0000, 0x8004_0000, 0xFF11_0000} {
		if err := FS.CheckPhys(p, 0x1_0000); err != nil {
			t.Errorf("%#x: %v", p, err)
		}
	}
	for _, p := range []uint64{0xFFCA_0000, 0xFFCC_0000, 0xFFCC_8000, 0xFFCD_0000, 0xFF16_0000, 0xFF17_0000} {
		if !refused(FS.CheckPhys(p, 0x1_0000)) {
			t.Errorf("%#x not refused", p)
		}
	}
	if !refused(FS.CheckPhys(0xFFC0_0000, 0x10_0000)) {
		t.Error("a window over the eFuse block not refused")
	}
}

// TRLC-LINKS: REQ-HD-016
func TestWritesThroughTheGuard(t *testing.T) {
	root := useRoot(t)
	outside := t.TempDir()
	in := filepath.Join(root, "open", "current")
	if err := FS.MkdirAll(filepath.Dir(in)); err != nil {
		t.Fatal(err)
	}
	if err := FS.WriteFile(in, []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(in); string(b) != "a\n" {
		t.Fatalf("content %q", b)
	}
	out := filepath.Join(outside, "x")
	if !refused(FS.WriteFile(out, []byte("x"), 0o644)) {
		t.Error("write outside the root not refused")
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("a refused write left a file")
	}
	if _, err := os.Stat(out + ".new"); err == nil {
		t.Error("a refused write left its temporary file")
	}
	if !refused(FS.Rename(in, out)) {
		t.Error("rename out of the root not refused")
	}
	if !refused(FS.Rename(out, in)) {
		t.Error("rename from outside the root not refused")
	}
	if !refused(FS.Remove(out)) || !refused(FS.MkdirAll(filepath.Join(outside, "d"))) || !refused(FS.Mkdir(outside)) {
		t.Error("remove or mkdir outside the root not refused")
	}
	if _, err := FS.OpenFile(filepath.Join(root, "updatefile", "BOOT.bin"), os.O_WRONLY|os.O_CREATE, 0o644); !refused(err) {
		t.Errorf("open in updatefile/: %v", err)
	}
	if err := FS.Rename(in, in+".old"); err != nil {
		t.Fatal(err)
	}
	if err := FS.Remove(in + ".old"); err != nil {
		t.Fatal(err)
	}
	var re *fsguard.RefusedError
	if err := FS.Check(out); !errors.As(err, &re) || !strings.Contains(err.Error(), "outside p12's root") {
		t.Errorf("refusal %v", err)
	}
}

const unitMounts = `/dev/root / ext4 ro,relatime 0 0
devtmpfs /dev devtmpfs rw,relatime 0 0
proc /proc proc rw,relatime 0 0
tmpfs /tmp tmpfs rw,relatime 0 0
/dev/mmcblk0p10 /usr/bin/siglent ext4 ro,relatime 0 0
/dev/mmcblk0p9 /usr/bin/siglent/firmdata0 ext4 ro,relatime 0 0
/dev/mmcblk0p12 /usr/bin/siglent/usr ext4 rw,relatime 0 0
/dev/mmcblk0p8 /mnt/logo vfat rw,relatime 0 0
/dev/sda1 /mnt/usb\040stick vfat rw,relatime 0 0
`

// TRLC-LINKS: REQ-HD-016
func TestCheckInstallMounts(t *testing.T) {
	good := "rootfs / rootfs rw 0 0\ndevtmpfs /dev devtmpfs rw 0 0\n/dev/mmcblk0p12 /mnt ext4 rw,relatime 0 0\n"
	if err := FS.CheckInstallMounts(strings.NewReader(good), "/mnt"); err != nil {
		t.Fatal(err)
	}
	for name, m := range map[string]string{
		"nothing mounted": "rootfs / rootfs rw 0 0\n",
		"wrong partition": "/dev/mmcblk0p9 /mnt ext4 rw 0 0\n",
		"boot partition":  "/dev/mmcblk0p1 /mnt vfat rw 0 0\n",
		"wrong fs":        "/dev/mmcblk0p12 /mnt vfat rw 0 0\n",
		"another rw eMMC": good + "/dev/mmcblk0p5 /boot vfat rw 0 0\n",
	} {
		if !refused(FS.CheckInstallMounts(strings.NewReader(m), "/mnt")) {
			t.Errorf("%s not refused", name)
		}
	}
	if err := FS.CheckInstallMounts(strings.NewReader(good+"/dev/mmcblk0p7 /ro ext4 ro 0 0\n"), "/mnt"); err != nil {
		t.Errorf("a read-only eMMC mount refused: %v", err)
	}
}

// TRLC-LINKS: REQ-HD-013, REQ-HD-016
func TestCheckOffUnit(t *testing.T) {
	for _, p := range []string{"/tmp/census.json", "/mnt/usb stick/census.json", "/mnt/usb stick/backup/p9.img"} {
		if err := FS.CheckOffUnit(p, strings.NewReader(unitMounts)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	for _, p := range []string{
		"/usr/bin/siglent/usr/census.json", "/usr/bin/siglent/firmdata0/x", "/mnt/logo/x",
		"/etc/x", "/dev/mmcblk0p9", "/mnt/usb stick/uboot.env", "/mnt/usb stick/huluwa_udiskEnv.txt",
	} {
		if !refused(FS.CheckOffUnit(p, strings.NewReader(unitMounts))) {
			t.Errorf("%s not refused", p)
		}
	}
}

// The vendor update trees are never a root: not p12's own, not below
// another root, not through a symlink (SAFETY.txt rule 6).
// TRLC-LINKS: REQ-HD-016
func TestSetRootRefusesUpdateTrees(t *testing.T) {
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, "usr", "upgrade"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(other, "p12")
	if err := os.Symlink(filepath.Join(other, "usr", "upgrade"), link); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{
		P12 + "/updatefile", P12 + "/updatefile/x", P12 + "/usr/upgrade", P12 + "/usr/upgrade/y",
		"/mnt/updatefile", "/mnt/usr/upgrade", filepath.Join(other, "usr", "upgrade"), link,
	} {
		if r, err := FS.SetRoot(d); err == nil {
			r()
			t.Errorf("root %q accepted", d)
		}
	}
	if FS.Root() != P12 {
		t.Fatalf("root left at %s", FS.Root())
	}
}

// A symlink planted on p12 carries no write, whether it leads to p9, to an
// update tree or stays on p12 (SAFETY.txt rule 4).
// TRLC-LINKS: REQ-HD-016
func TestNoWriteThroughSymlinks(t *testing.T) {
	root := useRoot(t)
	open := filepath.Join(root, "open")
	if err := os.MkdirAll(filepath.Join(open, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, to := range map[string]string{"log": "/usr/bin/siglent/firmdata0", "b": filepath.Join(root, "updatefile"), "state": filepath.Join(open, "a")} {
		if err := os.Symlink(to, filepath.Join(open, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"log/launch.log", "log", "b/app", "state/settings", "state"} {
		if err := FS.WriteFile(filepath.Join(open, p), []byte("x"), 0o644); !refused(err) {
			t.Errorf("open/%s: %v", p, err)
		}
	}
	if err := FS.WriteFile(filepath.Join(open, "a", "app"), []byte("x"), 0o644); err != nil {
		t.Errorf("open/a/app: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(root, "open2")); err != nil {
		t.Fatal(err)
	}
	if err := FS.MkdirAll(filepath.Join(root, "open2", "a")); !refused(err) {
		t.Errorf("a symlinked open directory created through: %v", err)
	}
}
