#!/bin/sh
# Runs the census binary in place of the vendor application, as the census
# stick would beside a configured PL, and marks its window in the log.
echo "census start" > /dev/simd_mark
/sim/fw/census -o /tmp/census.json
echo "census exit $?" > /dev/simd_mark
