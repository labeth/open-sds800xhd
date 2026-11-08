#!/bin/sh
# Fault lane: p12 is a small device (a tmpfs of a few MiB more than one app
# binary). The firmware is installed, the rest of the device filled, and the
# app started as the boot starts it; an update into the full device must
# fail without touching the current slot. The host checks the app over its
# ports meanwhile; this script waits for the host's stop.
. /sim/fw/l4-lib.sh
$O bootstrap $F/extra.sh $F/app l4-1 >/dev/null
say "storage-full installed: $(layout)"
dd if=/dev/zero of="$P/filler" bs=65536 2>/dev/null
say "storage-full filled: $(df -k $P | tail -1)"
say "storage-full boot $(boot)"
out=$($O install $F/app2 l4-2 2>&1)
rc=$?
say "storage-full update rc $rc ${out#otactl: } layout [$(layout)] slot-a $(which_app $P/open/a/app)"
usleep 2000000
say "storage-full after-update runs $(which_app "$(running)")"
while [ ! -e /sim/fw/stop ]; do sleep 1; done
say "storage-full alive-at-end $(which_app "$(running)") log $(wc -c <$P/open/log/app.log 2>/dev/null)"
