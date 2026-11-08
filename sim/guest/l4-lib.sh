# Shared by the fault lane's guest scripts (sourced): the unit's p12, the
# tools, and the "boot" the checks use: the launch hook run as the vendor
# rcS runs it, then which app the unit ended up running.
P=/usr/bin/siglent/usr
O=/sim/fw/otactl
F=/sim/fw

# say prints a result line ("l4: ...") on the console and marks it.
say() {
	echo "l4: $*"
	echo "l4: $*" >/dev/simd_mark 2>/dev/null
}

# running prints the path of the running app binary, or "none".
running() {
	for p in $(pidof app 2>/dev/null); do
		set -- $(tr '\0' ' ' <"/proc/$p/cmdline" 2>/dev/null)
		[ -n "$1" ] && { echo "$1"; return; }
	done
	echo none
}

# which names a binary by its content: app (the first version), app2 (the
# update), or "damaged" for anything else.
which_app() {
	case "$1" in none) echo none; return ;; esac
	if cmp -s "$1" "$F/app"; then echo app; elif cmp -s "$1" "$F/app2"; then echo app2; else echo damaged; fi
}

# stop_app ends every running app.
stop_app() {
	for p in $(pidof app 2>/dev/null); do kill "$p" 2>/dev/null; done
	for _ in 1 2 3 4 5 6 7 8 9 10; do
		[ -z "$(pidof app 2>/dev/null)" ] && return
		usleep 200000
	done
	for p in $(pidof app 2>/dev/null); do kill -9 "$p" 2>/dev/null; done
}

# boot runs the launch hook as S99extra does, if p12 holds it, and reports
# what runs after it: "vendor" when nothing of ours was started.
boot() {
	if [ ! -f "$P/extra.sh" ]; then
		echo "vendor (no hook)"
		return
	fi
	/bin/sh "$P/extra.sh" >/dev/null 2>&1
	rc=$?
	usleep "${BOOT_WAIT_US:-3000000}"
	r=$(running)
	if [ "$r" = none ]; then
		echo "vendor (hook rc $rc, no app)"
	else
		echo "$(which_app "$r") from $r (hook rc $rc)"
	fi
}

# layout prints the slot files in one line.
layout() {
	s=""
	for f in current previous trial disable; do
		[ -f "$P/open/$f" ] && s="$s $f=$(tr -d '\n' <"$P/open/$f" | head -c 16)"
	done
	for f in $(cd "$P" 2>/dev/null && ls extra.sh* open/*/app* open/*.new open/installed-* 2>/dev/null); do
		s="$s $f:$(wc -c <"$P/$f")"
	done
	echo "${s# }"
}

# tear models what a power cut does to a file still being written: every
# temporary file (*.new) of the durable writes is cut to half its length.
tear() {
	for f in $(find "$P" -name '*.new' 2>/dev/null); do
		n=$(wc -c <"$f")
		truncate -s $((n / 2)) "$f"
		echo "torn $f $n"
	done
}
