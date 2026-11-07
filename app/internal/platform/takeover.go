package platform

import (
	"fmt"
	"slices"
	"syscall"
	"time"
)

// Services the vendor boot or the vendor application starts and the app
// does not want running (spec 01 §12.2 step 3). rpcbind stays: VXI-11
// registers with it (spec 11 §4.1).
var unwantedServices = []string{
	"udhcpc", "ntpd", "smbd", "nmbd", "avahi-daemon", "telnetd", "lighttpd", "php-cgi", "vsftpd",
}

// StopVendor runs steps 1–3 of the takeover (spec 01 §12.2): it stops the
// display-switch script, the vendor application and its VNC server, and the
// services the app does not want. It touches no hardware. An error means a
// vendor process survived SIGKILL: the caller must not touch the hardware.
// TRLC-LINKS: REQ-HD-001
func StopVendor(log *Log) error {
	// 1. The display-switch script first, then its children (the sleep):
	// killing only the sleep would make the script write the VDMA at once.
	var script, children []int
	all := procs()
	for _, p := range all {
		if mentions(p.argv, displayScript) {
			script = append(script, p.pid)
		}
	}
	for _, p := range all {
		if slices.Contains(script, p.ppid) {
			children = append(children, p.pid)
		}
	}
	if len(script) > 0 {
		log.Printf("takeover: killing %s %v and its children %v", displayScript, script, children)
		kill(script, syscall.SIGKILL)
		kill(children, syscall.SIGKILL)
	}

	// 2. The vendor application and its VNC server: SIGTERM, 5 s, SIGKILL, 5 s.
	var vendor []int
	for _, p := range procs() {
		if isProgram(p.argv, vendorApp) || isProgram(p.argv, vendorVNC) {
			vendor = append(vendor, p.pid)
		}
	}
	if len(vendor) > 0 {
		log.Printf("takeover: stopping vendor processes %v", vendor)
		kill(vendor, syscall.SIGTERM)
		if left := waitGone(vendor, 5*time.Second); len(left) > 0 {
			log.Printf("takeover: %v survived SIGTERM, killing", left)
			kill(left, syscall.SIGKILL)
			if left = waitGone(left, 5*time.Second); len(left) > 0 {
				return fmt.Errorf("takeover: vendor processes %v survived SIGKILL", left)
			}
		}
	}

	// 3. Leftovers of the vendor application and unwanted services.
	var rest []int
	for _, p := range procs() {
		if mentions(p.argv, vendorDrivers) || slices.ContainsFunc(unwantedServices, func(s string) bool {
			return isProgram(p.argv, s)
		}) {
			rest = append(rest, p.pid)
		}
	}
	if len(rest) > 0 {
		log.Printf("takeover: killing leftovers and services %v", rest)
		kill(rest, syscall.SIGKILL)
		waitGone(rest, 2*time.Second)
	}
	return nil
}
