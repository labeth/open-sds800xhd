// Command app is the SDS800X HD scope application. The launch hook on p12
// starts it in the background after the vendor boot (spec 01 §12.2); it takes
// the hardware over from the vendor application and runs the instrument. It
// has no other way to run: tools and tests are separate binaries.
package main

import (
	"os"

	"github.com/labeth/open-sds800xhd/app/internal/app"
)

func main() {
	os.Exit(app.Run())
}
