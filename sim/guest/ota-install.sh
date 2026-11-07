#!/bin/sh
# First boot of the OTA scenario: the install stick's layout (hook, slot a),
# the hook starts slot a, the health check confirms it; an update into
# slot b that dies is rolled back by the health check and slot a relaunched;
# a second broken update is started once and left unconfirmed, as a power
# cut would leave it, for the next boot (ota-reboot.sh).
O=/sim/fw/otactl
$O bootstrap /sim/fw/extra.sh /sim/fw/app good-1
$O launch
$O watch 20s
$O install /sim/fw/broken broken-2
$O restart
$O watch 8s
$O status
$O install /sim/fw/broken broken-3
$O restart
sleep 3
$O status
echo "ota: install scenario done" > /dev/simd_mark
