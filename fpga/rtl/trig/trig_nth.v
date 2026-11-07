// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// trig_nth -- Nth edge after an idle time (spec 20 §7.5). edge marks the
// counted events; restart marks those that follow an idle gap (or have no
// earlier event): from trig_interval's judge|fresh with open = close = edge
// and cond "greater than the idle time". The restart event is edge 1; the
// N-th edge since the last restart fires, once per burst. Latency: 1 clock.
// TRLC-LINKS: REQ-HD-015
module trig_nth #(parameter integer L = 8) (
  input  wire         clk,
  input  wire         rst,
  input  wire         en,
  input  wire [L-1:0] edges,
  input  wire [L-1:0] restart,
  input  wire [15:0]  n,
  output reg  [L-1:0] fire = {L{1'b0}}
);
  // need: edges still to come before the n-th (0: the n-th has passed, or
  // no restart seen yet with n > 0 counted). Per lane only small prefix counts
  // are compared, so no carry chain runs through the lanes.
  reg [15:0] need = 16'd0;
  reg [3:0] since;                 // edges since the last restart, up to lane i
  reg [3:0] total;                 // edges in the clock up to lane i (no restart seen)
  reg seen;                        // a restart at or before lane i
  reg [L-1:0] fire_n;
  wire n_small = n[15:4] == 12'd0;
  integer i;
  always @(*) begin
    since = 4'd0; total = 4'd0; seen = 1'b0; fire_n = {L{1'b0}};
    for (i = 0; i < L; i = i + 1)
      if (edges[i]) begin
        if (restart[i]) begin seen = 1'b1; since = 4'd1; end
        else if (seen) since = since + 4'd1;
        else total = total + 4'd1;
        fire_n[i] = seen ? (n_small && since == n[3:0]) : (need[15:4] == 12'd0 && need[3:0] == total);
      end
  end
  wire [15:0] need_next = seen ? ((n > {12'd0, since}) ? n - {12'd0, since} : 16'd0)
                               : ((need > {12'd0, total}) ? need - {12'd0, total} : 16'd0);
  always @(posedge clk)
    if (rst || !en) begin need <= 16'd0; fire <= {L{1'b0}}; end
    else begin need <= need_next; fire <= fire_n; end
endmodule
