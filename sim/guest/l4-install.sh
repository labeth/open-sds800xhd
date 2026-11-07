#!/bin/sh
# Fault lane: power cuts at chosen points of the install stick's copy (the
# code oinit runs, as a test binary), on an empty p12 and over an earlier
# install, each followed by the boot without the stick and by the stick's
# own re-run (its marker is written last, so a cut install runs again).
. /sim/fw/l4-lib.sh

S=/tmp/l4-payload
mkdir -p $S/open/a
cp $F/extra.sh $S/extra.sh
cp $F/app2 $S/open/a/app
echo l4-2 >$S/open/a/version
echo a >$S/open/current

payload() { L4_GUEST=1 $F/installtest -test.run '^TestGuestPayload$' >/dev/null 2>&1; }

for base in empty installed; do
	for d in 0 30 60 100 150 220 300 450 700; do
		stop_app
		rm -rf "$P"/* "$P"/.[!.]* 2>/dev/null
		if [ $base = installed ]; then
			# an earlier install (slot a = app, marker 1) updated to slot b
			$O bootstrap $F/extra.sh $F/app l4-1 >/dev/null
			echo l4-1 >"$P/open/installed-1"
			$O install $F/app2 l4-2b >/dev/null
			$O confirm >/dev/null
		fi
		payload &
		pid=$!
		usleep $((d * 1000))
		kill -9 "$pid" 2>/dev/null
		# the test binary's process tree: nothing of it may run on
		for p in $(pidof installtest 2>/dev/null); do kill -9 "$p" 2>/dev/null; done
		wait "$pid" 2>/dev/null
		torn=$(tear | wc -l)
		marker=no
		[ -f "$P/open/installed-2" ] && marker=yes
		state=$(layout)
		b=$(boot)
		stop_app
		# the stick still in: no marker, so the install runs again
		if [ $marker = no ]; then
			payload
			rc=$?
		else
			rc=inert
		fi
		again=$(boot)
		stop_app
		say "install-cut base=$base d=$d torn=$torn marker=$marker state [$state] boot $b rerun $rc then $again"
	done
done
say "install scenario done"
