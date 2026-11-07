package plimage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

// The facts the gate requires are exactly the ones the build's gate
// (fpga/vivado/verified.tcl) lists.
// TRLC-LINKS: REQ-HD-016
func TestRequiredFactsMatchVerifiedTcl(t *testing.T) {
	b, err := os.ReadFile("../../../fpga/vivado/verified.tcl")
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, m := range regexp.MustCompile(`(?m)^\s+(U\d+)\s+[01]\s*$`).FindAllStringSubmatch(string(b), -1) {
		keys = append(keys, m[1])
	}
	if !slices.Equal(keys, RequiredFacts) {
		t.Fatalf("verified.tcl lists %v, the gate requires %v", keys, RequiredFacts)
	}
}

func allFacts(v bool) map[string]bool {
	m := map[string]bool{}
	for _, f := range RequiredFacts {
		m[f] = v
	}
	return m
}

type fakeSysfs struct {
	writes []string
	state  string
}

func (f *fakeSysfs) WriteAttr(p, v string) error {
	f.writes = append(f.writes, p+"="+v)
	return nil
}

func (f *fakeSysfs) ReadAttr(string) (string, error) { return f.state, nil }

func image(t *testing.T, m Manifest, bin []byte) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "open_pl.bin")
	if err := os.WriteFile(p, bin, 0o644); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "open_pl.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TRLC-LINKS: REQ-HD-016
func TestLoadRefusesUnverified(t *testing.T) {
	bin := []byte("bitstream")
	sum := sha256.Sum256(bin)
	good := Manifest{Image: "full", SHA256: hex.EncodeToString(sum[:]), BoardFacts: allFacts(true), BoardFactsVerified: true}
	oneOpen := good
	oneOpen.BoardFacts = allFacts(true)
	oneOpen.BoardFacts["U9"] = false
	missing := good
	missing.BoardFacts = allFacts(true)
	delete(missing.BoardFacts, "U3")
	noFlag := good
	noFlag.BoardFactsVerified = false
	wrongSum := good
	wrongSum.SHA256 = "00"
	for name, m := range map[string]Manifest{
		"one fact open": oneOpen, "fact missing": missing, "no flag": noFlag,
		"wrong sha256": wrongSum, "empty": {},
	} {
		fs := &fakeSysfs{state: "operating"}
		if err := Load(fs, image(t, m, bin)); !errors.Is(err, ErrUnverified) {
			t.Errorf("%s: %v", name, err)
		}
		if len(fs.writes) > 0 {
			t.Errorf("%s: fpga_manager written: %v", name, fs.writes)
		}
	}
	fs := &fakeSysfs{state: "operating"}
	noManifest := filepath.Join(t.TempDir(), "open_pl.bin")
	_ = os.WriteFile(noManifest, bin, 0o644)
	if err := Load(fs, noManifest); !errors.Is(err, ErrUnverified) || len(fs.writes) > 0 {
		t.Errorf("no manifest: %v, writes %v", err, fs.writes)
	}

	p := image(t, good, bin)
	if err := Load(fs, p); err != nil {
		t.Fatal(err)
	}
	want := []string{firmwarePath + "=" + filepath.Dir(p), fpgaFlags + "=0", fpgaFirmware + "=open_pl.bin"}
	if !slices.Equal(fs.writes, want) {
		t.Errorf("writes %v, want %v", fs.writes, want)
	}
	if err := Load(&fakeSysfs{state: "unknown"}, p); err == nil {
		t.Error("a PL not operating after the load was accepted")
	}
}

// TestCosimManifestRefused: the co-simulation's manifest (fpga/cosim) can
// never pass the gate: it declares no board fact verified, so nothing a
// co-simulation needs could make a unit load an image.
func TestCosimManifestRefused(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "fpga", "cosim", "open_pl_cosim.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if err := Check(m, []byte("anything")); !errors.Is(err, ErrUnverified) {
		t.Fatalf("the co-simulation manifest passed the gate: %v", err)
	}
}
