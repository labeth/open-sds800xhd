// Package boardtest drives the board through the HAL inside the tier-1
// simulator, the way the core engine will. Its tests are built only with the
// simboard tag, into a test binary the parity harness (sim/cmd/simtest) runs
// in place of the vendor application; on the host they do not exist.
//
// Each case marks its start in the simulator's log ("case AREA/NAME") and
// reports on the console: RESULT lines for what the guest checked itself,
// EXPECT lines for register values the harness checks in the case's window
// of the log, EXPECTSPI lines for front-end SPI frames.
package boardtest
