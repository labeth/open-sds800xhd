# ENGMODEL-OWNER-UNIT: FU-HD-PL-BUILD
# build.tcl -- the open image's Vivado project and build (spec 20 §13), batch:
#   vivado -mode batch -source fpga/vivado/build.tcl -tclargs <check|bitstream> [outdir]
# check: create the project (PS block design, RTL, constraints) and run
#   synthesis of the whole top, no bitstream.
# bitstream: also implement and write the .bin for fpga_manager (spec 12 §2),
#   refused while verified.tcl leaves a needed board fact open.
# The top level (open_pl_top) instantiates capture, display and panel blocks
# that wait for the board facts U3-U10; until it exists both modes stop
# after the block design.
set mode [expr {[llength $argv] > 0 ? [lindex $argv 0] : "check"}]
set here [file normalize [file dirname [info script]]]
set fpga [file normalize $here/..]
set out  [expr {[llength $argv] > 1 ? [file normalize [lindex $argv 1]] : "$fpga/build/vivado"}]
set part xczu2cg-sfvc784-2-e
set ::open_pl_build 1
source $here/verified.tcl
if {$mode eq "bitstream"} {
  set open {}
  foreach {k v} [array get ::verified] { if {!$v} { lappend open $k } }
  if {[llength $open]} {
    puts "build.tcl: refusing a bitstream for the unit: board facts open: [lsort -dictionary $open] (spec 20 §14)"
    exit 2
  }
}
file mkdir $out
create_project -force open_pl $out -part $part
set_property target_language Verilog [current_project]
source $here/ps_bd.tcl
open_pl_ps_bd open_pl_ps
make_wrapper -files [get_files open_pl_ps.bd] -top
add_files -norecurse [glob $out/open_pl.gen/sources_1/bd/open_pl_ps/hdl/open_pl_ps_wrapper.v]
# generated register include and the RTL
# Vivado's LD_LIBRARY_PATH carries an older libstdc++ than the system node needs
exec env -u LD_LIBRARY_PATH node $fpga/tools/regmap.mjs --out $fpga/build >@ stdout
set_property include_dirs [list $fpga/build $fpga/rtl] [current_fileset]
foreach f [glob -nocomplain $fpga/prim/*.v $fpga/rtl/*/*.v] { add_files -norecurse $f }
add_files -fileset constrs_1 -norecurse [list $here/constraints/pins.xdc $here/constraints/timing.xdc $here/constraints/bitstream.xdc]
set top_file [glob -nocomplain $fpga/rtl/top/open_pl_top.v]
if {$top_file eq ""} {
  puts "build.tcl: the block design is ready; open_pl_top waits for the board facts U3-U10 (spec 20 §14)"
  exit 0
}
set_property top open_pl_top [current_fileset]
# the build identity: the git commit into USR_ACCESS and ID_BUILD
set commit [string range [exec git -C $fpga rev-parse HEAD] 0 7]
set_property generic "BUILD=32'h$commit" [current_fileset]
launch_runs synth_1 -jobs 4
wait_on_run synth_1
open_run synth_1
report_utilization -file $out/synth_util.rpt
if {$mode eq "check"} { exit 0 }
set_property BITSTREAM.CONFIG.USR_ACCESS 0x$commit [current_design]
launch_runs impl_1 -to_step write_bitstream -jobs 4
wait_on_run impl_1
open_run impl_1
report_timing_summary -file $out/timing.rpt
report_utilization -file $out/util.rpt
set wns [get_property SLACK [get_timing_paths -max_paths 1 -setup]]
if {$wns < 0} { puts "build.tcl: timing fails (WNS $wns ns); not published"; exit 1 }
write_bitstream -force -bin_file $out/open_pl.bit
# The image's manifest: the board facts it was built against. The board's
# PL image gate (app/internal/plimage) loads the image only when every fact
# is verified and the sha256 matches.
set facts {}
set all 1
foreach k [lsort -dictionary [array names ::verified]] {
  if {!$::verified($k)} { set all 0 }
  lappend facts "\"$k\": [expr {$::verified($k) ? "true" : "false"}]"
}
set sha [lindex [exec sha256sum $out/open_pl.bin] 0]
set fh [open $out/open_pl.json w]
puts $fh "{\"image\": \"full\", \"sha256\": \"$sha\", \"build\": \"$commit\", \"board_facts\": {[join $facts {, }]}, \"board_facts_verified\": [expr {$all ? "true" : "false"}]}"
close $fh
puts "build.tcl: $out/open_pl.bin, $out/open_pl.json (WNS $wns ns, Vivado [version -short], $part)"
