// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// trig_accept -- turns candidate fires into the trigger the acquisition takes
// (spec 20 §7.6). Only while armed: the first candidate lane at or after the
// holdoff end (holdoff measured from the previous accepted trigger), else an
// auto trigger once no trigger was accepted for auto_clocks while armed, else
// a pending force. Auto and forced triggers sit at lane 0 of their clock.
// t_clk is the time of lane 0 of the candidates' clock. Latency: 1 clock.
// TRLC-LINKS: REQ-HD-015
module trig_accept #(parameter integer L = 8) (
  input  wire         clk,
  input  wire         rst,
  input  wire         armed,
  input  wire [L-1:0] cand,
  input  wire [2:0]   tpl,
  input  wire [47:0]  t_clk,
  input  wire         holdoff_en,
  input  wire [47:0]  holdoff,
  input  wire         auto_en,
  input  wire [47:0]  auto_clocks,
  input  wire         force_pulse,
  output reg          trig = 1'b0,
  output reg  [47:0]  trig_time = 48'd0,
  output reg  [2:0]   trig_lane = 3'd0,
  output reg          trig_auto = 1'b0,
  output reg          trig_forced = 1'b0,
  output reg  [31:0]  candidates = 32'd0,
  output reg  [31:0]  accepted = 32'd0
);
  reg [47:0] next_ok = 48'd0;     // first tick a trigger may be accepted at
  reg have_prev = 1'b0, force_pend = 1'b0, armed_q = 1'b0;
  reg [47:0] quiet = 48'd0;
  // ticks from this clock's lane 0 to the holdoff end (0: the whole clock is free)
  wire [48:0] rem_s = {1'b0, next_ok} - {1'b0, t_clk};
  wire hold_all_clear = !holdoff_en || !have_prev || rem_s[48] || rem_s == 0;
  wire hold_all_block = holdoff_en && have_prev && !rem_s[48] && rem_s > 49'd7 * tpl;
  reg [L-1:0] ok;
  reg found; reg [2:0] lane;
  integer i;
  always @(*) begin
    found = 1'b0; lane = 3'd0;
    for (i = 0; i < L; i = i + 1) begin
      ok[i] = cand[i] && (hold_all_clear || (!hold_all_block && i * tpl >= rem_s[5:0]));
      if (ok[i] && !found) begin found = 1'b1; lane = i[2:0]; end
    end
  end
  always @(posedge clk) begin
    trig <= 1'b0; trig_auto <= 1'b0; trig_forced <= 1'b0;
    armed_q <= armed;
    if (force_pulse) force_pend <= 1'b1;
    if (rst) begin
      have_prev <= 1'b0; force_pend <= 1'b0; quiet <= 48'd0; candidates <= 32'd0; accepted <= 32'd0;
    end else begin
      if (|cand) candidates <= candidates + 1'b1;
      if (!armed || !armed_q) quiet <= 48'd0;
      else quiet <= quiet + 1'b1;
      if (armed && (found || (auto_en && quiet >= auto_clocks) || force_pend)) begin
        trig <= 1'b1;
        trig_lane <= found ? lane : 3'd0;
        trig_time <= t_clk + (found ? lane * tpl : 48'd0);
        trig_auto <= !found && !force_pend;
        trig_forced <= !found && force_pend;
        force_pend <= 1'b0;
        next_ok <= t_clk + (found ? lane * tpl : 48'd0) + holdoff;
        have_prev <= 1'b1;
        quiet <= 48'd0;
        accepted <= accepted + 1'b1;
      end
    end
  end
endmodule
