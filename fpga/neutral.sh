#!/usr/bin/env bash
# ENGMODEL-OWNER-UNIT: FU-HD-PL-BUILD
# TRLC-LINKS: REQ-HD-015
# neutral.sh -- the neutrality check for the RTL blocks
# that are candidates for open-dso-core: each synthesises with yosys's generic
# `synth` (to the coarse cell level; memories stay memories) from its own
# sources alone, with no vendor primitive, no unknown
# module and no SIM define; `check -assert` fails on combinational loops,
# multiple drivers and undriven nets. The candidates are the blocks with no
# board-specific logic.
set -u
FPGA="$(cd "$(dirname "$0")" && pwd)"
OUT="$FPGA/build/neutral"; mkdir -p "$OUT"
# top : sources
CANDIDATES=(
  "trig_core:rtl/trig/trig_core.v rtl/trig/trig_cmp.v rtl/trig/trig_interval.v rtl/trig/trig_nth.v rtl/trig/trig_accept.v rtl/trig/trig_interp.v"
  "uart_trigger:rtl/proto/uart_trigger.v"
  "spi_trigger:rtl/proto/spi_trigger.v"
  "i2c_trigger:rtl/proto/i2c_trigger.v"
  "stream_framer:rtl/stream/stream_framer.v"
  "stack_accum:rtl/stack/stack_accum.v"
  "axil_slave:rtl/regs/axil_slave.v"
)
fail=0
for c in "${CANDIDATES[@]}"; do
  top="${c%%:*}"; srcs="${c#*:}"; files=""
  for s in $srcs; do files="$files $FPGA/$s"; done
  if yosys -q -l "$OUT/$top.log" -p "read_verilog $files; hierarchy -check -top $top; synth -top $top -run :fine; memory -nomap; opt -full; check -assert; stat" >/dev/null 2>&1 \
     && ! grep -qiE '(BUFG|MMCM|IDELAY|ISERDES|DNA_PORT|IBUFDS|altera|altsync|RAMB|DSP48)' "$OUT/$top.log"; then
    cells="$(grep -oE '^ +[0-9]+ +cells$' "$OUT/$top.log" | tail -1 | grep -oE '[0-9]+')"
    echo "[PASS] $top: generic coarse synthesis, ${cells:-?} cells"
  else
    echo "[FAIL] $top"; grep -E 'ERROR|Warning|Found' "$OUT/$top.log" | head -8; fail=1
  fi
done
exit $fail
