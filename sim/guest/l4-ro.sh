#!/bin/sh
# Fault lane: p12 read-only (as after the kernel remounted a damaged ext4).
# The boot runs the hook; the app, an update, the health check's confirm,
# the boot-target switch and the Acquire key route are tried. The host
# checks the first boot's app over its ports before the next boot.
. /sim/fw/l4-lib.sh
touch "$P/.probe" 2>/dev/null && say "storage-ro p12 is writable (the mode did not apply)"
for n in 1 2 3; do
	say "storage-ro boot$n $(boot) layout [$(layout)]"
	if [ $n -eq 1 ]; then
		# the host checks the first boot's app over its ports; the later
		# boots stop it, so they wait for the host's checks (bounded)
		w=0
		while [ ! -e /sim/fw/checked ] && [ $w -lt 90 ]; do sleep 1; w=$((w + 1)); done
		say "storage-ro host-checked after ${w}s"
	fi
	[ $n -lt 3 ] && stop_app
done
out=$($O install $F/app 2>&1); say "storage-ro update rc $? ${out#otactl: }"
out=$($O watch 6s 2>&1 | tail -1); say "storage-ro watch ${out#otactl: }"
out=$($O vendor 2>&1); say "storage-ro vendor rc $? ${out#otactl: }"
stop_app
echo "fault key stuck 3 4" >/dev/simd_mark
usleep 500000
say "storage-ro acquire-held boot $(boot)"
echo "fault key release" >/dev/simd_mark
echo "fault clear key" >/dev/simd_mark
usleep 500000
say "storage-ro final boot $(boot)"
while [ ! -e /sim/fw/stop ]; do sleep 1; done
