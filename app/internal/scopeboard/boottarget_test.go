package scopeboard

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labeth/open-dso-core/boottarget"
	"github.com/labeth/open-dso-core/hal"
	"github.com/labeth/open-sds800xhd/app/internal/platform"
)

// :SYSTem:BOOT and /api/boot write and remove the disable file the launch
// hook honours, through the write guard.
// TRLC-LINKS: REQ-HD-002, REQ-HD-016
func TestBootTargetExtension(t *testing.T) {
	if bt, ok := (&Board{}).BootTarget(); !ok || bt != bootSwitch(platform.OpenDir) {
		t.Fatalf("the board's boot target %+v: want the disable file in %s", bt, platform.OpenDir)
	}
	dir := filepath.Join(guardedDir(t), "open")
	x := boottarget.Extension(bootSwitch(dir))
	h := x.SCPI[0]
	disabled := func() bool { _, err := os.Stat(filepath.Join(dir, "disable")); return err == nil }
	query := func() string { b, _ := h.Query(""); return string(b) }
	if q := query(); q != "OPEN" {
		t.Fatalf("fresh: %s", q)
	}
	if err := h.Command("VENDor"); err != nil {
		t.Fatal(err)
	}
	if !disabled() || query() != "VENDOR" {
		t.Fatal("VENDOR did not write the disable file")
	}
	if err := h.Command("OPEN"); err != nil || disabled() {
		t.Fatalf("OPEN: %v, disabled %v", err, disabled())
	}
	if _, ok := hal.AsRefusal(h.Command("ON")); !ok {
		t.Error("a bad target not refused")
	}

	mux := http.NewServeMux()
	for _, r := range x.Routes {
		mux.Handle(r.Pattern, r.Handler)
	}
	do := func(method, target, body string) (int, string) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body)))
		return w.Code, strings.TrimSpace(w.Body.String())
	}
	if c, b := do("POST", "/api/boot", "vendor"); c != 200 || b != `{"next":"vendor"}` || !disabled() {
		t.Fatalf("POST vendor: %d %s", c, b)
	}
	if c, b := do("GET", "/api/boot", ""); c != 200 || b != `{"next":"vendor"}` {
		t.Fatalf("GET: %d %s", c, b)
	}
	if c, b := do("POST", "/api/boot?next=open", ""); c != 200 || b != `{"next":"open"}` || disabled() {
		t.Fatalf("POST open: %d %s", c, b)
	}
	if c, _ := do("POST", "/api/boot", "x"); c != 400 {
		t.Fatalf("POST x: %d", c)
	}
}
