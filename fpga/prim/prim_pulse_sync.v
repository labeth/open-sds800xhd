// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// prim_pulse_sync -- one source-clock pulse becomes one destination-clock
// pulse. Source pulses must be at least three destination clocks plus three
// source clocks apart (the toggle's round trip); busy is high while a pulse
// is in flight, and a pulse given while busy is counted in dropped (sim
// check) rather than lost silently.
// TRLC-LINKS: REQ-HD-015
module prim_pulse_sync (
  input  wire src_clk,
  input  wire src_pulse,
  output wire src_busy,
  input  wire dst_clk,
  output wire dst_pulse
);
  reg toggle = 1'b0;
  always @(posedge src_clk) if (src_pulse) toggle <= ~toggle;
  wire t_dst;
  prim_sync #(.STAGES(2)) s_fwd (.clk(dst_clk), .d(toggle), .q(t_dst));
  reg t_dst_q = 1'b0;
  always @(posedge dst_clk) t_dst_q <= t_dst;
  assign dst_pulse = t_dst ^ t_dst_q;
  wire t_back;
  prim_sync #(.STAGES(2)) s_back (.clk(src_clk), .d(t_dst_q), .q(t_back));
  assign src_busy = toggle ^ t_back;
`ifdef SIM
  integer dropped = 0;
  always @(posedge src_clk) if (src_pulse && src_busy) dropped = dropped + 1;
`endif
endmodule
