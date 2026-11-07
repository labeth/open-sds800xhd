// Package platform is the scope's place in the vendor system: the paths it
// may use, its log, the identity of the unit, and the takeover from the
// vendor application (spec 01 §12, spec 09 §1).
package platform

// Paths of the vendor system the app uses (spec 01 §3, §8, §12). Only p12 is
// writable; the app keeps all of its files under OpenDir.
const (
	AppPartition = "/usr/bin/siglent"           // p10, read-only
	P12          = "/usr/bin/siglent/usr"       // p12, the only writable partition
	FactoryDir   = "/usr/bin/siglent/firmdata0" // p9, factory data, read-only
	OpenDir      = P12 + "/open"                // everything of ours on p12
	LogDir       = OpenDir + "/log"
	DisableFile  = OpenDir + "/disable" // the launch hook's disable switch
)

// Vendor programs the takeover stops (spec 01 §12.2), by their paths.
const (
	vendorApp     = AppPartition + "/main.app"
	vendorVNC     = AppPartition + "/vncserver"
	vendorDrivers = AppPartition + "/drivers/"
	displayScript = "insmod_after_app.sh"
)
