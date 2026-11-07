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
# previous slot. Disable switch: open/disable (every boot until removed), or
# the Acquire key held at power-up (this boot only). Deleting this script
# returns the unit to the vendor firmware at the next boot.
dir=$(dirname "$0")
open=$dir/open
[ -e "$open/disable" ] && exit 0

log() {
	mkdir -p "$open/log" 2>/dev/null && echo "$(date +%s) $*" >>"$open/log/launch.log"
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
		rm -f "$open/trial" && sync
		slot=$prev
	else
		put trial "$slot $((starts + 1))"
	fi
fi

app=$open/$slot/app
[ -x "$app" ] || app=$open/app # a single binary, before slots were installed
[ -x "$app" ] || exit 0
log "starting slot $slot: $app"
OTA_DIR=$open OTA_SLOT=$slot OTA_HEALTH_PATH=$open/health "$app" </dev/null >/dev/null 2>&1 &
exit 0
