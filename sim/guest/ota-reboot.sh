#!/bin/sh
# Second boot of the OTA scenario on the p12 the first boot left: with the
# disable switch (otactl vendor) the hook starts nothing; without it (otactl
# open) the hook finds slot b unconfirmed after its one start, goes back to
# slot a and starts it, and the health check confirms it.
P=/usr/bin/siglent/usr
O=/sim/fw/otactl
$O vendor
$O status
/bin/sh $P/extra.sh
sleep 2
echo "ota: disabled boot done" > /dev/simd_mark
$O open
/bin/sh $P/extra.sh
$O watch 20s
$O status
echo "ota: reboot scenario done" > /dev/simd_mark
