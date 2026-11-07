#!/bin/sh
# Fault lane: damaged update images and power cuts at chosen points of an
# update, each followed by the boot the unit would do (a damaged p12 is
# l4-p12.sh: one simulator run has room for these two only).
. /sim/fw/l4-lib.sh

$O bootstrap $F/extra.sh $F/app l4-1 >/dev/null
$O launch >/dev/null
$O watch 8s
say "base $(layout)"

# Damaged images: is each refused before it is made current, and if not,
# what does the trial leave?
for img in trunc hdr flip; do
	stop_app
	$O bootstrap $F/extra.sh $F/app l4-1 >/dev/null
	rm -rf "$P/open/b" "$P/open/previous"
	out=$($O install "$F/app.$img" "bad-$img" 2>&1)
	say "corrupt-image $img install rc $? ${out#otactl: }"
	$O restart >/dev/null 2>&1
	$O watch 10s >/dev/null 2>&1
	usleep 2000000
	r=$(running)
	say "corrupt-image $img after-trial runs $(which_app "$r") layout $(layout)"
done

# Power cuts during an update: the install is killed after D ms (every
# process of the update dies at once), its half-written temporary file torn,
# then the unit boots.
for d in 0 25 50 80 120 170 230 300 400 550 750 1000; do
	stop_app
	$O bootstrap $F/extra.sh $F/app l4-1 >/dev/null
	rm -rf "$P/open/b" "$P/open/previous"
	$O install "$F/app2" l4-2 >/dev/null 2>&1 &
	pid=$!
	usleep $((d * 1000))
	kill -9 "$pid" 2>/dev/null
	wait "$pid" 2>/dev/null
	torn=$(tear | wc -l)
	state=$(layout)
	say "update-cut d=$d torn=$torn state [$state] boot $(boot) after [$(layout)]"
done
stop_app

say "update scenario done"
