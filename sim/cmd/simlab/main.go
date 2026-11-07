// Command simlab is the virtual bench for the SDS800X HD simulator: it keeps
// a simulator run of the app going (or attaches to one) and serves a page
// with the scope's front panel and live screen, a signal generator on its
// inputs, and the firmware's own web UI.
//
// Usage (from the repository root, after make app; make simlab does both):
//
//	simlab [-addr 127.0.0.1:8090] [-secs 300] [-name simlab] [-attach RUN]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/labeth/open-sds800xhd/sim/harness"
	"github.com/labeth/open-sds800xhd/sim/lab"
)

// defaultSignals are the inputs a new bench starts with: a 1 kHz square on
// C1, a 1 MHz sine on C2, C3 and C4 at 0 V.
const defaultSignals = "C1=square,1e3,2,0;C2=sine,1e6,1,0;C3=dc,0,0,0;C4=dc,0,0,0"

func main() {
	addr := flag.String("addr", "127.0.0.1:8090", "listen address of the bench page")
	secs := flag.Int("secs", 300, "length of one simulator run (the simulator caps it at 300 s; simlab starts the next)")
	name := flag.String("name", "simlab", "simulator run name (runs/NAME in the simulator workspace)")
	attach := flag.String("attach", "", "attach to a running run by name or directory instead of starting one (e.g. one sim/run.sh started)")
	sigs := flag.String("signals", defaultSignals, "inputs of the first run: Cn=kind,freq,vpp,offset[,key=value...];…")
	noRestart := flag.Bool("no-restart", false, "do not start a new run when one ends (use the page's Restart)")
	root := flag.String("root", ".", "the open-sds800xhd repository (build/app, ota/boot/extra.sh, sim/guest-boot.sh)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	start := map[string]string{}
	for p := range strings.SplitSeq(*sigs, ";") {
		if c, s, ok := strings.Cut(strings.TrimSpace(p), "="); ok {
			start[strings.ToUpper(c)] = s
		}
	}
	run := lab.NewRunner(start)
	tmp, err := os.MkdirTemp("", "simlab")
	if err != nil {
		stop()
		log.Fatal(err) //nolint:gocritic // exitAfterDefer: stop already ran
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	done := make(chan struct{})
	if *attach != "" {
		dir := *attach
		if !strings.Contains(dir, "/") {
			dir = filepath.Join(harness.Workspace(), "analysis", "testenv", "runs", dir)
		}
		go func() { defer close(done); run.Attach(ctx, dir) }()
	} else {
		r, err := filepath.Abs(*root)
		if err != nil {
			log.Fatal(err)
		}
		app := filepath.Join(r, "build", "app")
		if _, err := os.Stat(app); err != nil {
			log.Fatalf("simlab: %v (run make app, or make simlab)", err)
		}
		spec := harness.Spec{
			Name: *name, Secs: *secs, Ports: true, Keep: true,
			App: app, Hook: filepath.Join(r, "ota", "boot", "extra.sh"), GuestBoot: filepath.Join(r, "sim", "guest-boot.sh"),
		}
		go func() { defer close(done); run.Loop(ctx, spec, !*noRestart) }()
	}

	srv := lab.NewServer(run, tmp)
	go srv.Serve(ctx)
	l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	fmt.Printf("simlab: http://%s/  (firmware web UI %s once the run is up; Ctrl-C stops)\n", l.Addr(), srv.WebURL)
	go func() {
		if err := hs.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Print(err)
			stop()
		}
	}()
	<-ctx.Done()
	stop() // a second Ctrl-C ends simlab at once
	fmt.Println("simlab: stopping the simulator run…")
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = hs.Shutdown(sctx) // the page's long polls end with the process
	cancel()
	select {
	case <-done:
	case <-time.After(90 * time.Second):
		fmt.Println("simlab: the run did not end in time")
	}
}
