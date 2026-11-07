# Repo-level conveniences over the app module.
#
#   make test   - the app's unit tests on the host
#   make app    - the static arm64 device binary, build/app
#   make boardtest - the in-guest board tests, build/boardtest (simulator only)
#   make ota    - the static arm64 ota binaries: build/otactl, build/census,
#                 build/oinit (the install stick's /init)
#   make sim-test  - the simulator parity harness: every HAL area in the
#                    tier-1 simulator, pass/fail per area (build/sim-test.txt)
#   make cosim-test - the open-PL co-simulation lane: the open PL's RTL,
#                    Verilated (fpga/cosim), as the simulator's PL; every
#                    stage-2 area with expected/measured tables
#                    (build/cosim-test.txt; needs the private reference workspace's simulator)
#   make cosim-smoke - the Verilated open PL's self-test alone (no simulator)
#   make l4-test   - the fault lane: the device model's fault knobs (DMA,
#                    interrupts, bus errors, PL reload, clock lock, keys),
#                    power cuts during an install or an update, damaged
#                    images and storage, the write guard, and the operator
#                    fuzzer on the web UI (build/l4-test.txt, with a fault
#                    table; needs the private reference workspace's simulator)
#   make simlab    - the virtual bench: the simulator running the app, and
#                    a page (http://127.0.0.1:8090/) with the scope's front
#                    panel, live screen, a signal generator on its inputs and
#                    the firmware's web UI; Ctrl-C stops (sim/README.md)
#   make fpga-sim     - the open PL's iverilog testbenches (fpga/sim.sh)
#   make fpga-lint    - verilator --lint-only -Wall on every PL module
#   make fpga-neutral - yosys generic synthesis of the shareable PL blocks
#   make fpga-synth   - Vivado out-of-context synthesis, place and route per
#                       block with a utilisation/timing summary (needs Vivado)
#
#   make demo      - the browser demo as a static site in build/demo/ (the
#                    firmware as WebAssembly against a simulated board, built
#                    by open-dso-core from demo/profile.yaml and demo/demo.json);
#                    serve the directory on localhost or https to try it
#   make demo-native - the same demo as a local web server (bench page at
#                    http://127.0.0.1:8090/demo/, web UI at /); Ctrl-C stops
#
# The app module requires open-dso-core next to this repository
# (app/go.mod's replace) until the core is released and pinned by tag; the
# demo is built by that checkout too (CORE names another).

GO ?= go
CORE ?= $(abspath ../open-dso-core)
DEMO_OUT ?= $(abspath build/demo)

.PHONY: test app vet boardtest ota sim-test l4-test simlab cosim-test cosim-smoke cosim-build fpga-sim fpga-lint fpga-neutral fpga-synth demo demo-native

demo:
	rm -rf $(DEMO_OUT)
	cd $(CORE) && $(GO) run ./cmd/dsodemo-site -profile $(abspath demo/profile.yaml) -demo $(abspath demo/demo.json) -out $(DEMO_OUT)

demo-native:
	cd $(CORE) && $(GO) run ./cmd/dsodemo -profile $(abspath demo/profile.yaml) -demo $(abspath demo/demo.json)

test:
	cd app && $(GO) test ./...
	cd ota && $(GO) test ./...
	cd sim && $(GO) test ./...

vet:
	cd app && $(GO) vet ./...
	cd app && GOOS=linux GOARCH=arm64 $(GO) vet ./...
	cd ota && $(GO) vet ./...
	cd ota && GOOS=linux GOARCH=arm64 $(GO) vet ./...

app:
	mkdir -p build
	cd app && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -o ../build/app ./cmd/app

ota:
	mkdir -p build
	cd ota && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -o ../build/ ./cmd/otactl ./cmd/census ./cmd/oinit

boardtest:
	mkdir -p build
	cd app && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) test -c -tags simboard -trimpath -o ../build/boardtest ./internal/boardtest

# AREA=trigger,readout limits the run; REUSE=1 re-checks the last runs.
sim-test: app boardtest ota
	cd sim && $(GO) build -o ../build/simtest ./cmd/simtest
	./build/simtest $(if $(AREA),-area $(AREA)) $(if $(REUSE),-reuse)

cosim-build:
	fpga/cosim/build.sh

cosim-smoke: cosim-build
	fpga/build/cosim/opl_cosim --selftest

# AREA=trigger,stream limits the run; REUSE=1 re-checks the last runs.
cosim-test: app boardtest ota cosim-build
	cd sim && $(GO) build -o ../build/simtest ./cmd/simtest
	./build/simtest -cosim $(if $(AREA),-area $(AREA)) $(if $(REUSE),-reuse)

# AREA=l4-acq,l4-fuzz limits the run; REUSE=1 re-checks the last runs;
# L4_FUZZ_SEEDS=1-20 and L4_FUZZ_ITERS=60 size the fuzz lane.
l4-test: app ota
	cd ota && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) test -c -tags simguest -trimpath -o ../build/installtest ./internal/install
	cd sim && $(GO) build -o ../build/simtest ./cmd/simtest
	./build/simtest -l4 $(if $(AREA),-area $(AREA)) $(if $(REUSE),-reuse)

# SIMLAB_ARGS passes simlab flags, e.g. SIMLAB_ARGS="-addr 127.0.0.1:9000".
simlab: app
	cd sim && $(GO) build -o ../build/simlab ./cmd/simlab
	./build/simlab $(SIMLAB_ARGS)

fpga-sim:
	fpga/sim.sh

fpga-lint:
	fpga/lint.sh

fpga-neutral:
	fpga/neutral.sh

fpga-synth:
	fpga/synth.sh
