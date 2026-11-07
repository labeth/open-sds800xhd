// Command census takes the read-only census of the unit (package census)
// and reports it: one line per part on stdout, a last line "CENSUS <json>",
// and with -o the indented JSON report in a file (the census stick writes it
// to the stick). It writes nothing else: run it before any of our code has
// been installed, beside the vendor firmware or from the census stick.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/labeth/open-sds800xhd/ota/census"
	"github.com/labeth/open-sds800xhd/ota/fsguard"
)

func main() { os.Exit(run()) }

// run is the census, its exit status the result: 1 when the census or the
// report failed.
func run() int {
	out := flag.String("o", "", "write the JSON report to this file")
	root := flag.String("root", "", "look under this directory instead of / (fixtures)")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := census.Run(ctx, census.Unit{Root: *root})
	if err != nil {
		fmt.Fprintln(os.Stderr, "census:", err)
	}
	for _, v := range r.Verdicts {
		fmt.Printf("census: %-16s %-8s %s\n", v.Part, v.Verdict, v.Detail)
	}
	b, _ := json.Marshal(r)
	fmt.Printf("CENSUS %s\n", b)
	if *out != "" {
		f, ferr := createOffUnit(*out)
		if ferr == nil {
			ferr = r.WriteJSON(f)
			ferr = errors.Join(ferr, f.Sync(), f.Close())
		}
		if ferr != nil {
			fmt.Fprintln(os.Stderr, "census:", ferr)
			return 1
		}
	}
	if err != nil {
		return 1
	}
	return 0
}

// createOffUnit creates the report file only off the unit's eMMC (a stick,
// tmpfs): the census writes nothing to the eMMC.
func createOffUnit(path string) (*os.File, error) {
	m, err := os.Open("/proc/mounts")
	if err != nil {
		return nil, err
	}
	defer func() { _ = m.Close() }() // read only: nothing to flush
	if err := fsguard.CheckOffUnit(path, m); err != nil {
		return nil, err
	}
	return os.Create(path) //nolint:gosec // G304: the path passed the write guard
}
