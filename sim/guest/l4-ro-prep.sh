#!/bin/sh
# Fault lane, before the read-only p12 run: slot a confirmed, an update in
# slot b on trial (not yet started), as a remount could catch it.
. /sim/fw/l4-lib.sh
$O bootstrap $F/extra.sh $F/app l4-1 >/dev/null
$O install $F/app2 l4-2 >/dev/null
say "storage-ro prepared: $(layout)"
