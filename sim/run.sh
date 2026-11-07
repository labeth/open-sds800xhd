#!/bin/bash
# Runs the app in the tier-1 simulator of the SDS800X HD private reference
# workspace (its sim/README.md): the vendor boot up to the kernel modules
# (hook "modules", what the vendor application would find), then the launch
# hook ota/boot/extra.sh, which starts build/app from open/ on the simulated
# p12. Outputs land in the workspace's analysis/testenv/runs/NAME/; the app's
# log is usr/open/log/app.log there.
#
# Usage: sim/run.sh [-n NAME] [-t SECS] [-- SIMD flags are taken from $SIMD_ARGS]
#   SDS800XHD_WS   the workspace (default ~/ws/sds800xhd)
#   SCENARIO       a scenario file of timed control/SCPI commands (sim/README.md)
set -euo pipefail
here=$(cd "$(dirname "$0")/.." && pwd)
WS=${SDS800XHD_WS:-$HOME/ws/sds800xhd}
NAME=open-app
SECS=120
while [ $# -gt 0 ]; do
  case "$1" in
    -n) NAME=$2; shift 2 ;;
    -t) SECS=$2; shift 2 ;;
    *) echo "usage: sim/run.sh [-n NAME] [-t SECS]" >&2; exit 2 ;;
  esac
done
make -C "$here" app >/dev/null
[ -n "${SCENARIO:-}" ] && export SCENARIO=$(cd "$(dirname "$SCENARIO")" && pwd)/$(basename "$SCENARIO")
fw=$(mktemp -d)
trap 'rm -rf "$fw"' EXIT
mkdir -p "$fw/open"
cp "$here/ota/boot/extra.sh" "$fw/extra.sh"
cp "$here/build/app" "$fw/open/app"
FWDIR=$fw flock "$WS/analysis/.sim.lock" "$WS/sim/run/tier1.sh" -n "$NAME" -t "$SECS" -p \
  -R "$here/sim/guest-boot.sh" -H modules
echo "log: $WS/analysis/testenv/runs/$NAME/usr/open/log/app.log"
