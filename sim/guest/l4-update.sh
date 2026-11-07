#!/bin/sh
# Fault lane: damaged update images, power cuts at chosen points of an
# update, and a damaged p12, each followed by the boot the unit would do.
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

# A damaged p12: garbage pointers, a cut hook, unreadable settings, a
# damaged current app.
$O bootstrap $F/extra.sh $F/app l4-1 >/dev/null
printf 'x\000zz' >"$P/open/current"
say "corrupt-p12 current-garbage boot $(boot)"
stop_app
$O bootstrap $F/extra.sh $F/app l4-1 >/dev/null
echo "a notanumber" >"$P/open/trial"
say "corrupt-p12 trial-garbage boot $(boot) after [$(layout)]"
stop_app
$O bootstrap $F/extra.sh $F/app l4-1 >/dev/null
echo "q 7" >"$P/open/previous"
echo "a 5" >"$P/open/trial"
say "corrupt-p12 previous-garbage boot $(boot) after [$(layout)]"
stop_app
$O bootstrap $F/extra.sh $F/app l4-1 >/dev/null
n=$(wc -c <"$F/extra.sh")
head -c $((n / 2)) "$F/extra.sh" >"$P/extra.sh"
say "corrupt-p12 hook-cut boot $(boot)"
stop_app
$O bootstrap $F/extra.sh $F/app l4-1 >/dev/null
mkdir -p "$P/open/state"
head -c 3000 /sim/fw/app >"$P/open/state/scope-settings.json"
r=$(boot)
usleep 8000000
say "corrupt-p12 settings-garbage boot $r alive-after-8s $(which_app "$(running)")"
stop_app
$O bootstrap $F/extra.sh $F/app l4-1 >/dev/null
head -c 4000000 "$F/app" >"$P/open/a/app.cut" && mv "$P/open/a/app.cut" "$P/open/a/app" && chmod 755 "$P/open/a/app"
r=$(boot)
out=$($O watch 6s 2>&1 | tail -1)
say "corrupt-p12 current-app-damaged boot $r watch ${out#otactl: } runs $(which_app "$(running)") layout $(layout)"
stop_app
say "update scenario done"
