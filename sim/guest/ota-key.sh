#!/bin/sh
# Third boot of the OTA scenario: the Acquire key is held at power-up (the
# scenario holds MAP 0x80040588 bit 3 low), so the launch hook leaves this
# boot to the vendor firmware without writing the disable file.
P=/usr/bin/siglent/usr
O=/sim/fw/otactl
i=0
while [ $i -lt 60 ]; do
	k=$(devmem 0x80040588 32 2>/dev/null)
	case "$k" in 0x* | 0X*) [ $((k & 0x8)) -eq 0 ] && break ;; esac
	sleep 1
	i=$((i + 1))
done
echo "ota: key word $k" > /dev/simd_mark
/bin/sh $P/extra.sh
sleep 3
echo "ota: key-held boot done" > /dev/simd_mark
$O status
