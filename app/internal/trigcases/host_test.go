package trigcases

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"testing"
)

var update = flag.Bool("update", false, "rewrite host.json")

// host.json is Hosts as the simulator's harness reads it; every setup
// names a golden.
func TestHostFile(t *testing.T) {
	if len(Hosts()) != len(hostSetups) {
		t.Fatalf("%d of %d host setups name a golden", len(Hosts()), len(hostSetups))
	}
	b, err := json.MarshalIndent(Hosts(), "", " ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	if *update {
		if err := os.WriteFile("host.json", b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile("host.json")
	if err != nil || !bytes.Equal(got, b) {
		t.Fatalf("host.json is not Hosts (go test ./internal/trigcases -update): %v", err)
	}
}
