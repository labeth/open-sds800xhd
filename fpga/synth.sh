#!/usr/bin/env bash
# ENGMODEL-OWNER-UNIT: FU-HD-PL-BUILD
# TRLC-LINKS: REQ-HD-015
# synth.sh -- Vivado out-of-context synthesis + place + route of each RTL block
# at its clock (fpga/vivado/ooc.tcl), and a utilisation/timing summary in
# fpga/build/synth/summary.md. Needs Vivado in PATH or VIVADO_SETTINGS
# (default ~/tools/Xilinx/2026.1/Vivado/settings64.sh). No bitstream: the
# board facts the top level needs are UNVERIFIED (spec 20 §14).
#   fpga/synth.sh [block-filter]
set -u
FPGA="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$FPGA/.." && pwd)"
GEN="$FPGA/build"; OUT="$GEN/synth"
filter="${1:-}"
if ! command -v vivado >/dev/null; then
  s="${VIVADO_SETTINGS:-$HOME/tools/Xilinx/2026.1/Vivado/settings64.sh}"
  [ -f "$s" ] || { echo "synth.sh: no vivado (set VIVADO_SETTINGS)"; exit 2; }
  # shellcheck disable=SC1090
  source "$s"
fi
node "$FPGA/tools/regmap.mjs" --out "$GEN" >/dev/null || exit 1
mkdir -p "$OUT"
R="$FPGA/rtl"; P="$FPGA/prim"
# block : clock period (ns) : sources (the clock is the domain the block runs in, spec 20 §3)
BLOCKS=(
  "trig_core:4.0:$R/trig/trig_core.v $R/trig/trig_cmp.v $R/trig/trig_interval.v $R/trig/trig_nth.v $R/trig/trig_accept.v $R/trig/trig_interp.v"
  "proto_core:4.0:$R/proto/proto_core.v $R/trig/trig_cmp.v $R/proto/uart_trigger.v $R/proto/spi_trigger.v $R/proto/i2c_trigger.v"
  "stream_framer:3.333:$R/stream/stream_framer.v"
  "stack_accum:4.0:$R/stack/stack_accum.v"
  "opl_regs:10.0:$R/regs/opl_regs.v"
  "compat_acq:10.0:$R/regs/compat_acq.v"
  "compat_map:10.0:$R/regs/compat_map.v"
  "axil_slave:10.0:$R/regs/axil_slave.v"
)
jobs_max="${SYNTH_JOBS:-3}"
ran=()
for b in "${BLOCKS[@]}"; do
  top="${b%%:*}"; rest="${b#*:}"; per="${rest%%:*}"; srcs="${rest#*:}"
  [ -n "$filter" ] && [[ "$top" != *"$filter"* ]] && continue
  ran+=("$top")
  rm -rf "$OUT/$top"; mkdir -p "$OUT/$top"
  ( cd "$OUT/$top" && vivado -mode batch -nojournal -log vivado.log -source "$FPGA/vivado/ooc.tcl" \
      -tclargs "$top" "$per" "$OUT/$top" "$GEN:$R" $srcs >/dev/null 2>&1 ) &
  while [ "$(jobs -rp | wc -l)" -ge "$jobs_max" ]; do wait -n; done
done
wait
fail=0
{
  echo "| block | clock | WNS (ns) | Fmax est. (MHz) | LUTs | FFs | BRAM36 | DSP |"
  echo "|---|---|---|---|---|---|---|---|"
  # every block with a result, the ones of this run and earlier ones
  for b in "${BLOCKS[@]}"; do
    top="${b%%:*}"; s="$OUT/$top/summary.txt"
    if [ ! -f "$s" ]; then
      [[ " ${ran[*]} " == *" $top "* ]] && { echo "| $top | — | FAILED (see $OUT/$top/vivado.log) | | | | | |"; fail=1; }
      continue
    fi
    eval "$(tr ' ' '\n' < "$s" | grep = )"
    fmax="$(awk -v p="$period_ns" -v w="$wns_ns" 'BEGIN{printf "%.0f", 1000/(p-w)}')"
    echo "| $top | $(awk -v p="$period_ns" 'BEGIN{printf "%.0f MHz", 1000/p}') | $wns_ns | $fmax | $luts | $ffs | $bram36 | $dsp |"
    awk -v w="$wns_ns" 'BEGIN{exit !(w < 0)}' && fail=1
  done
  echo
  first="$(ls "$OUT"/*/summary.txt 2>/dev/null | head -1)"
  [ -n "$first" ] && echo "Vivado $(grep -oE 'vivado=[^ ]+' "$first" | cut -d= -f2), part $(grep -oE 'part=[^ ]+' "$first" | cut -d= -f2), out-of-context, post-route."
} | tee "$OUT/summary.md"
exit $fail
