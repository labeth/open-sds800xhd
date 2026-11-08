#!/bin/sh
# Launch hook of the open firmware (spec 01 §12.2–§12.3). The vendor rcS runs
# it synchronously as its last start script, so it must return at once: it
# picks the slot, starts that slot's app in the background with its standard
# streams detached, and exits. It lives at the root of p12
# (/usr/bin/siglent/usr/extra.sh) and is replaced only by the install stick,
# never by an update, so the slot layout it reads is a fixed contract
# (ota/internal/slots):
#
#   open/current   "a" or "b"          open/previous   the slot to go back to
#   open/trial     "<slot> <starts>"   while the current slot is unconfirmed
#   open/<slot>/app                    open/disable    leave the unit to the vendor
#
# A slot on trial gets one start; if it is still unconfirmed at the next
# start (the supervisor confirms a healthy app), the hook goes back to the
# previous slot. A trial start the hook cannot count (p12 read-only) is not
# made: the previous slot starts instead, or with none the vendor firmware. Disable switch: open/disable (every boot until removed), or
# the Acquire key held at power-up (this boot only). Deleting this script
# returns the unit to the vendor firmware at the next boot.
#
# Nothing of ours on p12 is a symlink, so one there was planted or is
# damage, and a write through it could land anywhere (p9 included): the hook
# tests every path it writes with [ -L ] first and writes nothing through a
# link, and with open/ itself a link it does nothing at all.
dir=$(dirname "$0")
open=$dir/open
[ -L "$open" ] && exit 0
[ -e "$open/disable" ] && exit 0

# plain NAME... succeeds when no open/NAME, nor any directory of it below
# open/, is a symlink.
plain() {
	for name in "$@"; do
		p=$open
		rest=$name
		while [ -n "$rest" ]; do
			part=${rest%%/*}
			[ "$part" = "$rest" ] && rest= || rest=${rest#*/}
			p=$p/$part
			[ -L "$p" ] && return 1
		done
	done
	return 0
}

# log writes a line to open/log/launch.log, or nowhere when a link is in
# the way.
log() {
	plain log log/launch.log || return 0
	mkdir -p "$open/log" 2>/dev/null && plain log && echo "$(date +%s) $*" >>"$open/log/launch.log"
}

# Key held at power-up: Acquire (MAP idx 0x162 = 0x80040588 bit 3, active
# low; spec 08 §2.2) leaves this boot to the vendor firmware, without writing
# the disable file, so the next boot runs ours again. In the vendor firmware Acquire
# only opens its menu (spec 08 §4.4), so the vendor firmware seeing it held
# changes no setting; it is a plain key, not a knob, and on the key-test
# screen. The vendor boot configured the PL before S99extra runs (spec 01
# §12.1) and reading the key words has no side effect (spec 01 §12.3).
keys=$(devmem 0x80040588 32 2>/dev/null)
case "$keys" in
0x* | 0X*)
	if [ $((keys & 0x8)) -eq 0 ]; then
		log "Acquire held at power-up: vendor firmware for this boot"
		exit 0
	fi
	;;
esac

# put NAME VALUE writes open/NAME durably: temporary file, sync, rename, sync.
put() {
	plain "$1" "$1.new" || return 1
	echo "$2" >"$open/$1.new" && sync && mv -f "$open/$1.new" "$open/$1" && sync
}

slot=$(cat "$open/current" 2>/dev/null)
case "$slot" in a | b) ;; *) slot=a ;; esac

if [ -f "$open/trial" ] && read -r tslot starts <"$open/trial" && [ "$tslot" = "$slot" ]; then
	case "$starts" in '' | *[!0-9]*) starts=0 ;; esac
	prev=$(cat "$open/previous" 2>/dev/null)
	if [ "$starts" -ge 1 ] && { [ "$prev" = a ] || [ "$prev" = b ]; } && [ "$prev" != "$slot" ] && [ -x "$open/$prev/app" ]; then
		log "slot $slot unconfirmed after $starts start(s): back to slot $prev"
		put current "$prev"
		plain trial && rm -f "$open/trial" && sync
		slot=$prev
	elif ! put trial "$slot $((starts + 1))"; then
		# The start cannot be counted (p12 read-only or full): a slot on
		# trial would then start on every boot without ever going back.
		# Start the previous slot instead, or nothing (the vendor firmware).
		if { [ "$prev" = a ] || [ "$prev" = b ]; } && [ "$prev" != "$slot" ] && [ -x "$open/$prev/app" ]; then
			log "slot $slot on trial and its start cannot be counted: slot $prev for this boot"
			slot=$prev
		else
			log "slot $slot on trial and its start cannot be counted: vendor firmware for this boot"
			exit 0
		fi
	fi
fi

app=$open/$slot/app
plain "$slot/app" || app=
[ -n "$app" ] && [ -x "$app" ] || { plain app && app=$open/app; } # a single binary, before slots were installed
[ -n "$app" ] && [ -x "$app" ] || exit 0
log "starting slot $slot: $app"
OTA_DIR=$open OTA_SLOT=$slot OTA_HEALTH_PATH=$open/health "$app" </dev/null >/dev/null 2>&1 &
exit 0
