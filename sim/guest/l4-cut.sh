#!/bin/sh
# Fault lane, first boot of a power-cut pair: the open firmware installed
# and confirmed, then an update (L4_CUT=update) or the install stick's copy
# (L4_CUT=install, on an empty p12) is started and the power is cut D ms
# later (L4_CUT_MS): every process of the unit is killed at once. The next
# run boots from what p12 holds (l4-boot.sh).
. /sim/fw/l4-lib.sh
what=$(cat $F/l4-cut.conf 2>/dev/null)
mode=${what%% *}
ms=${what##* }
case "$mode" in
update)
	$O bootstrap $F/extra.sh $F/app l4-1 >/dev/null
	$O launch >/dev/null
	$O watch 6s >/dev/null
	$O install $F/app2 l4-2 >/dev/null 2>&1 &
	;;
install)
	rm -rf "$P"/* 2>/dev/null
	S=/tmp/l4-payload
	mkdir -p $S/open/a
	cp $F/extra.sh $S/extra.sh
	cp $F/app2 $S/open/a/app
	echo a >$S/open/current
	L4_GUEST=1 $F/installtest -test.run '^TestGuestPayload$' >/dev/null 2>&1 &
	;;
esac
usleep $((ms * 1000))
# the power goes: every process but this one and init, at once (before
# anything slow: the report below takes seconds under the emulator)
kill -9 -1 2>/dev/null
say "power cut: $mode after $ms ms, layout $(layout)"
say "reboot-cut first boot ends"
