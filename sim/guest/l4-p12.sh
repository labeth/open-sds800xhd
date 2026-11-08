#!/bin/sh
# Fault lane: a damaged p12 (garbage slot pointers, a cut hook, unreadable
# settings, a damaged current app), each followed by the boot the unit
# would do.
. /sim/fw/l4-lib.sh
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
say "p12 scenario done"
