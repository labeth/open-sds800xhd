#!/bin/sh
# Stands in for the end of the vendor rcS inside the tier-1 simulator: runs
# the launch hook as S99extra would and then stays alive, as init does, so
# the container keeps running while the app works, until the host asks the
# run to end early by creating the file stop beside this script (the run's
# fw/ directory, read-only here, writable on the host).
/sim/fw/extra.sh
echo "launch hook returned $?" > /dev/simd_mark 2>/dev/null
while [ ! -e /sim/fw/stop ]; do sleep 1; done
echo "stop asked by the host" > /dev/simd_mark 2>/dev/null
