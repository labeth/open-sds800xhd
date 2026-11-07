#!/usr/bin/env bash
# ENGMODEL-OWNER-UNIT: FU-HD-PL-BUILD
# TRLC-LINKS: REQ-HD-015
# lint.sh -- verilator --lint-only -Wall on every module under fpga/prim and
# fpga/rtl as its own top (simulation models of the primitives: -DSIM; the
# vendor branches are checked by Vivado). Waivers with reasons: fpga/lint.vlt.
set -u
FPGA="$(cd "$(dirname "$0")" && pwd)"
GEN="$FPGA/build"
node "$FPGA/tools/regmap.mjs" --out "$GEN" >/dev/null || exit 1
dirs=$(find "$FPGA/prim" "$FPGA/rtl" -name '*.v' -not -path '*/sim/*' -printf '%h\n' | sort -u | sed 's/^/-y /')
fail=0 n=0
for f in $(find "$FPGA/prim" "$FPGA/rtl" -name '*.v' -not -path '*/sim/*' | sort); do
  for m in $(grep -oE '^\s*module\s+\w+' "$f" | awk '{print $2}'); do
    n=$((n+1))
    out="$(verilator --lint-only -Wall -Wno-fatal -DSIM -I"$GEN" -I"$FPGA/rtl" $dirs "$FPGA/lint.vlt" --top-module "$m" "$f" 2>&1 | grep -E '^%(Warning|Error)')"
    if [ -n "$out" ]; then echo "[FAIL] $m"; echo "$out" | head -20; fail=1; else echo "[PASS] $m"; fi
  done
done
echo "lint.sh: $n modules, $([ $fail = 0 ] && echo clean || echo FINDINGS)"
exit $fail
