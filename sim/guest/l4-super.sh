#!/bin/sh
# Fault lane: the open firmware installed as the install stick leaves it
# (hook, slot a confirmed), started as the boot starts it, and then watched by
# otactl's health check in a loop, standing in for a supervisor that runs on
# the unit: a dead or stale app is relaunched (and a slot on trial rolled
# back). The host injects the faults over the device model's control socket
# and checks the app over its ports; this script ends when the host asks
# (the file stop beside it).
O=/sim/fw/otactl
mark() { echo "$*" > /dev/simd_mark 2>/dev/null; }
$O bootstrap /sim/fw/extra.sh /sim/fw/app l4-1
$O launch
while [ ! -e /sim/fw/stop ]; do
	$O watch 6s
done
mark "l4: supervisor stopped"
