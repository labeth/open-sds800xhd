//go:build simguest

package install

import (
	"os"
	"testing"

	"github.com/labeth/open-sds800xhd/ota/fsguard"
)

// TestGuestPayload runs the install stick's copy on the simulated unit's
// p12, for the simulator's fault lane (sim/guest/l4-install.sh), which cuts
// the power under it at chosen points: the payload tree the script builds
// in /tmp/l4-payload, marker open/installed-2. It is the code oinit runs
// once its mount checks passed; the stick's mounts are not part of the
// simulator. L4_GUEST=1 enables it.
func TestGuestPayload(t *testing.T) {
	if os.Getenv("L4_GUEST") != "1" {
		t.Skip("runs only in the simulator's fault lane (L4_GUEST=1)")
	}
	const src, p12, mark = "/tmp/l4-payload", fsguard.P12, "open/installed-2"
	if _, err := fsguard.SetRoot(p12); err != nil {
		t.Fatal(err)
	}
	if err := Payload(src, p12, mark, "l4-2"); err != nil {
		t.Fatal(err)
	}
}
