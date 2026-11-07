package openpl

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestRegsGenerated: regs_gen.go is the generator's output for the
// register YAML (fpga/tools/regmap.mjs), so the board's constants and the
// image's manifest cannot drift apart.
func TestRegsGenerated(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out := t.TempDir()
	tool := filepath.Join("..", "..", "..", "fpga", "tools", "regmap.mjs")
	if b, err := exec.CommandContext(t.Context(), node, tool, "--out", out).CombinedOutput(); err != nil {
		t.Fatalf("regmap: %v\n%s", err, b)
	}
	want, err := os.ReadFile(filepath.Join(out, "openpl", "regs.go"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("regs_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("regs_gen.go differs from fpga/tools/regmap.mjs's output: copy fpga/build/openpl/regs.go after a change of fpga/regs/open-pl.yml")
	}
}
