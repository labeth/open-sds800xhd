#!/bin/sh
# Fault lane, the boot after a power cut: the end of the vendor rcS runs the
# launch hook from p12 if it is there; then what runs, and the health
# check's verdict.
. /sim/fw/l4-lib.sh
BOOT_WAIT_US=6000000
say "reboot-cut boot $(boot) layout [$(layout)]"
out=$($O watch 8s 2>&1 | tail -1)
say "reboot-cut watch ${out#otactl: } runs $(which_app "$(running)") layout [$(layout)]"
stop_app
