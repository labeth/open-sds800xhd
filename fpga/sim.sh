#!/usr/bin/env bash
# ENGMODEL-OWNER-UNIT: FU-HD-PL-BUILD
# TRLC-LINKS: REQ-HD-015
# sim.sh -- generate the register include and run every self-checking iverilog
# testbench (fpga/**/sim/tb_*.v). A testbench passes when it compiles, ends
# without $fatal and prints no line starting FAIL. Exits non-zero on any
# failure. Usage: fpga/sim.sh [name-filter]
set -u
FPGA="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$FPGA/.." && pwd)"
GEN="$FPGA/build"
OUT="$GEN/sim${1:+-$1}"
LIB="$OUT/lib"
filter="${1:-}"
rm -rf "$OUT"; mkdir -p "$LIB"
node "$FPGA/tools/regmap.mjs" --out "$GEN" >/dev/null || { echo "[FAIL] regmap"; exit 1; }
# iverilog finds a library module by file name: link each module name to the
# file that defines it (several modules may share a file).
for f in $(find "$FPGA/prim" "$FPGA/rtl" -name '*.v' -not -path '*/sim/*'); do
  for m in $(grep -oE '^\s*module\s+\w+' "$f" | awk '{print $2}'); do
    [ -e "$LIB/$m.v" ] && { echo "[FAIL] module $m defined twice"; exit 1; }
    ln -s "$f" "$LIB/$m.v"
  done
done
# one testbench: compile, run, judge; the verdict line goes to $OUT/<tb>.verdict
run_tb() {
  local tb="$1" n; n="$(basename "$tb" .v)"
  if ! iverilog -g2012 -Wall -Wno-timescale -Wno-implicit-dimensions -Wno-sensitivity-entire-array -DSIM -I "$GEN" -I "$FPGA/rtl" -y "$LIB" -o "$OUT/$n.vvp" "$tb" >"$OUT/$n.log" 2>&1; then
    { echo "[FAIL] $n (compile)"; sed -n 1,15p "$OUT/$n.log"; } >"$OUT/$n.verdict"; return
  fi
  if grep -qE 'implicit definition' "$OUT/$n.log"; then
    { echo "[FAIL] $n (implicit net)"; grep -E 'implicit definition' "$OUT/$n.log" | head -5; } >"$OUT/$n.verdict"; return
  fi
  if timeout 1800 vvp -N "$OUT/$n.vvp" >>"$OUT/$n.log" 2>&1 && ! grep -qE '^FAIL|FATAL|ERROR' "$OUT/$n.log" && grep -qE '^PASS' "$OUT/$n.log"; then
    echo "[PASS] $n: $(grep -E '^PASS' "$OUT/$n.log" | tail -1 | cut -c6-)" >"$OUT/$n.verdict"
  else
    { echo "[FAIL] $n"; grep -E '^FAIL|FATAL|ERROR' "$OUT/$n.log" | head -8; } >"$OUT/$n.verdict"
  fi
}
jobs_max="${SIM_JOBS:-$(nproc)}"
tbs=()
for tb in $(find "$FPGA" -path '*/sim/tb_*.v' | sort); do
  n="$(basename "$tb" .v)"
  [ -n "$filter" ] && [[ "$n" != *"$filter"* ]] && continue
  tbs+=("$tb")
  run_tb "$tb" &
  while [ "$(jobs -rp | wc -l)" -ge "$jobs_max" ]; do wait -n; done
done
wait
fail=0
for tb in "${tbs[@]}"; do
  n="$(basename "$tb" .v)"
  cat "$OUT/$n.verdict"
  grep -q '^\[PASS\]' "$OUT/$n.verdict" || fail=1
done
echo "sim.sh: ${#tbs[@]} testbenches, $([ $fail = 0 ] && echo all passed || echo FAILURES)"
exit $fail
