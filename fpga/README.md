# Open PL

The open programmable-logic design for the SDS800X HD (stage 2: our own bitstream instead of the vendor's). The
design is [`../specs/20-open-pl.md`](../specs/20-open-pl.md); the register ABI is
[`regs/open-pl.yml`](regs/open-pl.yml).

**Stage: simulation and out-of-context synthesis.** No bitstream exists for the unit: the top level waits for the
board facts spec 20 §14 lists (ADC lane map, DCLK balls, LCD timing, panel interface, DDR range), and
`vivado/build.tcl bitstream` refuses to build while [`vivado/verified.tcl`](vivado/verified.tcl) leaves one open.

## Layout

| Path | What |
|---|---|
| `regs/open-pl.yml` | the register ABI, one-to-one with SystemRDL |
| `tools/regmap.mjs` | generates the Verilog include, Go constants, SystemRDL and the manifest hash (Node ≥ 22, no dependencies) |
| `prim/` | vendor-primitive wrappers with behavioural models (`SIM`): synchronizers, async FIFO, DNA, LVDS lane, MMCM |
| `rtl/regs/` | AXI-Lite slave (always OKAY), the open-plane register file, the compat ACQ and MAP planes |
| `rtl/trig/` | trigger engine: comparators, interval engine, Nth edge, holdoff/auto/force, interpolator |
| `rtl/proto/` | protocol triggers: the SDS1000CML+'s UART, SPI and I2C engines behind line slicers |
| `rtl/stream/` | stream framer: whole blocks, sequence numbers, every drop counted and flagged |
| `rtl/stack/` | phase-binned stacking accumulator for super-resolution |
| `rtl/acq/` | route stage, decimator/reducer, P12 packer, acquisition state machine, record writer (HP0), readout engine, mean engine |
| `rtl/top/` | `opl_core`: everything from the ISERDES outputs to the PS ports, with the clock-domain crossings of spec 20 §3.1 (the capture, display, panel and Xilinx IP sit around it) |
| `cosim/` | the co-simulation: `opl_core` Verilated (`cosim_top.v`, `cosim.cpp`, `build.sh`) as the PL of the tier-1 simulator |
| `*/sim/tb_*.v` | self-checking testbenches |
| `vivado/` | out-of-context flow (`ooc.tcl`), the PS block design (`ps_bd.tcl`), the project build (`build.tcl`), the board-fact gate (`verified.tcl`) and the XDC (`constraints/`) |

Generated files and every tool output go to `fpga/build/` (ignored by git).

## Checks

```sh
make fpga-sim       # iverilog testbenches (parallel; SIM_JOBS=n)
make fpga-lint      # verilator --lint-only -Wall per module; waivers with reasons in lint.vlt
make fpga-neutral   # yosys generic synthesis of the blocks meant for open-dso-core: no vendor primitive
make fpga-synth     # Vivado out-of-context synthesis + place + route per block at its clock:
                    # fpga/build/synth/summary.md (VIVADO_SETTINGS names settings64.sh)
```

CI runs the first three (`rtl-sim`) and the co-simulation smoke; Vivado never runs in CI.

## Co-simulation

`opl_core` is Verilated with a harness (`cosim/cosim.cpp`) that stands for what surrounds it in the full image: the
DDR slaves of the HP ports, the Xilinx AXI DMA (PG021, S2MM direct register mode) and the capture (samples handed in
at the ISERDES outputs after the lane map, whose channel-to-core mapping is the U14 assumption of `acq_route`).
The tier-1 simulator of the private reference workspace runs it as its PL (`simd -pl open -cosim
fpga/build/cosim/opl_cosim`): it forwards the compat ACQ plane, the open plane, the stacker window and the AXI DMA's
registers to it, shares the PS DDR with it (1 GiB memfd; the record and stream rings at `0x2000_0000`–`0x3FFF_FFFF`),
answers its sample requests from its signal generators through the decoded front end, and turns its interrupt
lines into the kernel modules' nodes. The firmware then runs stage 2 unchanged: the board finds the open image
(compat ACQ 0x002 = 0x4Fxx), checks ID_MAGIC, the ABI and the manifest, and drives acquisition, trigger, readout,
stream, segments, counter and stacker through the open plane.

```sh
make cosim-smoke   # the Verilated core's self-test alone (CI)
make cosim-test    # every stage-2 area in the simulator (local: needs the private reference workspace) -> build/cosim-test.txt
make cosim-test AREA=trigger,stream
```

Clocks are exact (aclk 100, cclk 250, mclk 300 MHz; time unit 1/3 ns); one tick is 0.5 ns and cclk cycle j carries
ticks 8j … 8j+7 of the injected signals. Simulated time runs as fast as the model goes, about 250 000–300 000 cclk
cycles (1–1.2 ms) a wall second on one core: 1 s of PL time is 15 minutes, so the firmware's wall-clock timeouts are
long against PL time and the board's gates are chosen to be short in PL time (the counter's 1 ms gate). Register
accesses are served between batches of 256 clock edges. The wire protocol is in `cosim.cpp`'s header.

**Never on a unit.** The co-simulation loads nothing: the simulator's PL is the open design from power-up, and a
bitstream load through `fpga_manager` only resets it. `cosim/open_pl_cosim.json` is the co-simulation's manifest; it
declares no board fact verified, so `app/internal/plimage` refuses it like any image built before the facts are
settled (`TestCosimManifestRefused`), and its image name `cosm` is not one a unit build produces.
