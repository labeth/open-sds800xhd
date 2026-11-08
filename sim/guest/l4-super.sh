#!/bin/sh
# Fault lane: the open firmware installed as the install stick leaves it
# (hook, slot a confirmed), started as the boot starts it, and kept running
# by otactl's bounded supervisor (a dead or stale app is started again, a
# slot on trial rolled back; MaxStarts starts in a row without a healthy app
# end in a counted reboot). The host injects the faults over the device
# model's control socket and checks the app over its ports; this script ends
# when the host asks (the file stop beside it).
O=/sim/fw/otactl
mark() { echo "$*" > /dev/simd_mark 2>/dev/null; }
$O bootstrap /sim/fw/extra.sh /sim/fw/app l4-1
$O launch
$O supervise 6s &
sup=$!
while [ ! -e /sim/fw/stop ]; do
	sleep 1
done
kill "$sup" 2>/dev/null
wait "$sup" 2>/dev/null
mark "l4: supervisor stopped"
