# ENGMODEL-OWNER-UNIT: FU-HD-PL-BUILD
# ps_bd.tcl -- the PS block design of the open image (spec 20 §4, spec 12
# §3-§4): the Zynq UltraScale+ PS with the PL-facing configuration the vendor
# FSBL already programs (the image keeps the vendor boot chain, so the
# generated psu_init is not used on the unit). Sourced by build.tcl, or run
# alone to check the configuration:
#   vivado -mode batch -source ps_bd.tcl -tclargs <outdir>
# PL clocks: pl_clk0 100 MHz (registers), pl_clk1 300 MHz (HP ports).
# Ports: HPM0_LPD 32-bit; HP0 128 R/W, HP1 64 write-only use, HP2 64, HP3 32
# (the FSBL's AFIFM widths). IRQ0/IRQ1 to the PL. EMIO GPIO 96 (0/1 = ADC chip
# selects, 95 = pl_resetn0), GEM0 over EMIO (MII), TTC0 wave-out 0 over EMIO.
proc open_pl_ps_bd {name} {
  create_bd_design $name
  set ps [create_bd_cell -type ip -vlnv xilinx.com:ip:zynq_ultra_ps_e ps]
  set_property -dict [list \
    CONFIG.PSU__USE__M_AXI_GP0 {0} \
    CONFIG.PSU__USE__M_AXI_GP1 {0} \
    CONFIG.PSU__USE__M_AXI_GP2 {1} \
    CONFIG.PSU__MAXIGP2__DATA_WIDTH {32} \
    CONFIG.PSU__USE__S_AXI_GP2 {1} \
    CONFIG.PSU__SAXIGP2__DATA_WIDTH {128} \
    CONFIG.PSU__USE__S_AXI_GP3 {1} \
    CONFIG.PSU__SAXIGP3__DATA_WIDTH {64} \
    CONFIG.PSU__USE__S_AXI_GP4 {1} \
    CONFIG.PSU__SAXIGP4__DATA_WIDTH {64} \
    CONFIG.PSU__USE__S_AXI_GP5 {1} \
    CONFIG.PSU__SAXIGP5__DATA_WIDTH {32} \
    CONFIG.PSU__FPGA_PL0_ENABLE {1} \
    CONFIG.PSU__CRL_APB__PL0_REF_CTRL__FREQMHZ {100} \
    CONFIG.PSU__FPGA_PL1_ENABLE {1} \
    CONFIG.PSU__CRL_APB__PL1_REF_CTRL__FREQMHZ {300} \
    CONFIG.PSU__USE__IRQ0 {1} \
    CONFIG.PSU__USE__IRQ1 {1} \
    CONFIG.PSU__NUM_FABRIC_RESETS {1} \
    CONFIG.PSU__GPIO_EMIO__PERIPHERAL__ENABLE {1} \
    CONFIG.PSU__GPIO_EMIO__PERIPHERAL__IO {95} \
    CONFIG.PSU__ENET0__PERIPHERAL__ENABLE {1} \
    CONFIG.PSU__ENET0__PERIPHERAL__IO {EMIO} \
    CONFIG.PSU__TTC0__PERIPHERAL__ENABLE {1} \
    CONFIG.PSU__TTC0__WAVEOUT__ENABLE {1} \
    CONFIG.PSU__TTC0__WAVEOUT__IO {EMIO} \
  ] $ps
  # every PS interface the PL uses becomes a port of the design; the PL top
  # (open_pl_top) connects them
  foreach intf {M_AXI_HPM0_LPD S_AXI_HP0_FPD S_AXI_HP1_FPD S_AXI_HP2_FPD S_AXI_HP3_FPD GPIO_0 GMII_ENET0 MDIO_ENET0} {
    if {[llength [get_bd_intf_pins -quiet ps/$intf]]} { make_bd_intf_pins_external [get_bd_intf_pins ps/$intf] }
  }
  foreach pin {pl_clk0 pl_clk1 pl_resetn0 pl_ps_irq0 pl_ps_irq1 maxihpm0_lpd_aclk saxihp0_fpd_aclk saxihp1_fpd_aclk
               saxihp2_fpd_aclk saxihp3_fpd_aclk emio_ttc0_wave_o} {
    if {[llength [get_bd_pins -quiet ps/$pin]]} { make_bd_pins_external [get_bd_pins ps/$pin] }
  }
  # the PL masters reach DDR only (the record and stream rings, udmabuf0, the
  # framebuffers); the register window is 0x8000_0000 + 512 MiB (spec 02)
  assign_bd_address
  foreach seg [get_bd_addr_segs -of_objects [get_bd_addr_spaces -quiet *_FPD_0] -quiet] {
    if {[string match *OCM* $seg]} { exclude_bd_addr_seg $seg }
  }
  set lpd [get_bd_addr_segs -quiet -of_objects [get_bd_addr_spaces ps/Data] -filter {NAME =~ *M_AXI_HPM0_LPD*}]
  if {[llength $lpd]} { set_property offset 0x80000000 $lpd; set_property range 512M $lpd }
  validate_bd_design
  save_bd_design
}
if {[info exists argv0] && [file tail $argv0] eq "ps_bd.tcl" || ([info exists argv] && [llength $argv] == 1 && ![info exists ::open_pl_build])} {
  set out [lindex $argv 0]
  file mkdir $out
  create_project -in_memory -part xczu2cg-sfvc784-2-e
  set_property target_language Verilog [current_project]
  set_property BD_FLOW_MODE 1 [current_project] -quiet
  file mkdir $out/bd
  create_bd_design -dir $out/bd tmp ; close_bd_design tmp
  open_pl_ps_bd open_pl_ps
  report_property [get_bd_cells ps] -file $out/ps_props.txt
  puts "ps_bd: block design valid"
}
