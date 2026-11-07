# ENGMODEL-OWNER-UNIT: FU-HD-PL-BUILD
# ooc.tcl -- out-of-context synthesis, placement and routing of one RTL block
# at its clock, for utilisation and timing (spec 20 §13). Batch mode:
#   vivado -mode batch -source ooc.tcl -tclargs <top> <period_ns> <outdir> <incdirs> <files...>
# The part is the one spec 12 §1 gives (ZU2 vs ZU3 and the temperature grade
# are UNVERIFIED, spec 20 §14 U1). Every port of the block is a clock or an
# unconstrained I/O of the out-of-context module: the numbers are the block's
# own paths, register to register.
set top    [lindex $argv 0]
set period [lindex $argv 1]
set out    [lindex $argv 2]
set incs   [split [lindex $argv 3] ":"]
set files  [lrange $argv 4 end]
set part   xczu2cg-sfvc784-2-e
file mkdir $out
set_part $part
foreach f $files { read_verilog -sv $f }
synth_design -top $top -part $part -mode out_of_context -include_dirs $incs -flatten_hierarchy rebuilt
create_clock -name clk -period $period [get_ports clk]
report_utilization -file $out/synth_util.rpt
opt_design
place_design
phys_opt_design
route_design
report_utilization -file $out/util.rpt
report_timing_summary -max_paths 5 -file $out/timing.rpt
report_timing -max_paths 1 -sort_by group -file $out/worst.rpt
set wns [get_property SLACK [get_timing_paths -max_paths 1 -setup]]
set u [report_utilization -return_string]
proc used {u name} {
  foreach line [split $u "\n"] {
    if {[regexp "^\\|\\s*${name}\\s*\\|\\s*(\[0-9.\]+)\\s*\\|" $line -> n]} { return $n }
  }
  return 0
}
set fh [open $out/summary.txt w]
puts $fh [format "%s period_ns=%s wns_ns=%s luts=%s ffs=%s bram36=%s dsp=%s vivado=%s part=%s" \
  $top $period $wns [used $u "CLB LUTs"] [used $u "CLB Registers"] [used $u "Block RAM Tile"] [used $u "DSPs"] \
  [version -short] $part]
close $fh
