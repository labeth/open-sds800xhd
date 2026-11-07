#!/usr/bin/env bash
# ENGMODEL-OWNER-UNIT: FU-HD-PL-BUILD
# TRLC-LINKS: REQ-HD-015
# build.sh -- Verilate the open PL core (fpga/rtl, top cosim_top) with the
# co-simulation harness (cosim.cpp) into fpga/build/cosim/opl_cosim.
# Verilator 5 (behavioural primitive models: -DSIM).
set -eu
FPGA="$(cd "$(dirname "$0")/.." && pwd)"
GEN="$FPGA/build"
OUT="${COSIM_OUT:-$GEN/cosim}"
node "$FPGA/tools/regmap.mjs" --out "$GEN" >/dev/null
dirs=$(find "$FPGA/prim" "$FPGA/rtl" -name '*.v' -not -path '*/sim/*' -printf '%h\n' | sort -u | sed 's/^/-y /')
mkdir -p "$OUT"
verilator --cc --exe --build -j "${COSIM_JOBS:-$(nproc)}" -O3 --x-assign fast --x-initial fast --noassert \
  -Wno-fatal -Wno-lint -Wno-style -Wno-TIMESCALEMOD -DSIM -I"$GEN" -I"$FPGA/rtl" $dirs \
  --top-module cosim_top --Mdir "$OUT/obj" -o "$OUT/opl_cosim" \
  -CFLAGS "-O2 -std=c++17" "$FPGA/cosim/cosim_top.v" "$FPGA/cosim/cosim.cpp" >"$OUT/build.log" 2>&1 \
  || { tail -30 "$OUT/build.log"; exit 1; }
echo "$OUT/opl_cosim"
