#!/bin/sh
# Fault lane: the write guard against a p12 an attacker or a damaged file
# system left with symlinks out of it, and against vendor roots: the update,
# the app's logs, settings and health token, and otactl's writes must all be
# refused where they would leave p12's root or enter its vendor trees.
. /sim/fw/l4-lib.sh
$O bootstrap $F/extra.sh $F/app l4-1 >/dev/null
# slot b → p9 (factory data); the app's log and state directories → p9 and
# /tmp; the update trees the vendor boot flashes from
rm -rf "$P/open/b" "$P/open/log" "$P/open/state"
ln -s /usr/bin/siglent/firmdata0 "$P/open/b"
ln -s /usr/bin/siglent/firmdata0 "$P/open/log"
ln -s /tmp/l4-escape "$P/open/state"
mkdir -p /tmp/l4-escape
out=$($O install $F/app2 l4-2 2>&1); say "guard-symlinks update-into-p9 rc $? ${out#otactl: }"
rm -f "$P/open/b"; ln -s ../updatefile "$P/open/b"
out=$($O install $F/app2 l4-2 2>&1); say "guard-symlinks update-into-updatefile rc $? ${out#otactl: }"
rm -f "$P/open/b"
out=$($O -p12 /usr/bin/siglent bootstrap $F/extra.sh $F/app x 2>&1); say "write-guard root-vendor rc $? ${out#otactl: }"
out=$($O -p12 $P/updatefile bootstrap $F/extra.sh $F/app x 2>&1); say "write-guard root-updatefile rc $? ${out#otactl: }"
out=$($O -p12 $P/usr/upgrade bootstrap $F/extra.sh $F/app x 2>&1); say "write-guard root-upgrade rc $? ${out#otactl: }"
out=$($O -p12 /usr/bin/siglent/firmdata0 status 2>&1); say "write-guard root-p9 rc $? ${out#otactl: }"
say "guard-symlinks app boot $(boot)"
usleep 15000000
say "guard-symlinks app alive $(which_app "$(running)")"
stop_app
say "guard-symlinks escape [$(ls -A /tmp/l4-escape 2>/dev/null | tr '\n' ' ')] p9 [$(ls -A /usr/bin/siglent/firmdata0 | tr '\n' ' ')] updatefile [$(ls -A $P/updatefile $P/usr/upgrade 2>/dev/null | tr '\n' ' ')]"
say "guard scenario done"
