# ENGMODEL-OWNER-UNIT: FU-HD-PL-BUILD
# timing.xdc -- clocks, clock groups and the attribute-driven CDC constraints
# (spec 20 §3, §11). Nothing here names a hierarchical path.

# ADC data clocks: DCLK = fs/2 = 500 MHz at 1 GSa/s per core (spec 12 §6.2).
# ADC12DL1500 boards may run 750 MHz (UNVERIFIED, U4).
create_clock -name adc_a_dclk -period 2.000 [get_ports adc_a_dclk_p]
create_clock -name adc_b_dclk -period 2.000 [get_ports adc_b_dclk_p]
# MII clocks from the PHY (100 Mb/s: 25 MHz)
create_clock -name gem_tx_clk -period 40.000 [get_ports gem_tx_clk]
create_clock -name gem_rx_clk -period 40.000 [get_ports gem_rx_clk]

# pl_clk0/pl_clk1 are created by the PS IP's constraints. The domains are
# asynchronous to each other: every crossing goes through a synchronizer
# primitive (spec 20 §3.1), constrained below by attribute.
set_clock_groups -asynchronous \
  -group [get_clocks -include_generated_clocks -quiet clk_pl_0] \
  -group [get_clocks -include_generated_clocks -quiet clk_pl_1] \
  -group [get_clocks -include_generated_clocks adc_a_dclk] \
  -group [get_clocks -include_generated_clocks adc_b_dclk] \
  -group [get_clocks gem_tx_clk] -group [get_clocks gem_rx_clk]

# Synchronizers: the first ASYNC_REG stage of each chain gets a datapath-only
# bound of one destination period (clock groups would otherwise drop the
# check), and gray-coded FIFO pointers a bus skew of one period.
foreach cell [get_cells -hier -quiet -filter {ASYNC_REG == TRUE}] {
  set d [get_pins -quiet $cell/D]
  if {[llength $d] == 0} { continue }
  set src [get_cells -quiet -of_objects [get_pins -quiet -leaf -filter {DIRECTION == OUT} -of_objects [get_nets -quiet -of_objects $d]]]
  if {[llength $src] == 0 || [get_property -quiet ASYNC_REG $src] == "TRUE"} { continue }
  set_max_delay -datapath_only 2.000 -from $src -to $cell
}
# A multicycle exception, where unavoidable, is marked (* dso_mcp2 = "true" *)
# in the RTL and selected here by that property.
foreach cell [get_cells -hier -quiet -filter {dso_mcp2 == "true"}] {
  set_multicycle_path 2 -setup -to $cell
  set_multicycle_path 1 -hold -to $cell
}
