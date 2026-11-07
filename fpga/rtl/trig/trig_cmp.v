// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// trig_cmp -- one comparator of one channel on L lanes (spec 20 §7.2): a
// Schmitt trigger per lane with explicit thresholds. The state goes 1 at the
// first valid sample >= hi and 0 at the first valid sample <= lo (hi wins if
// both hold: a misconfigured band still decides), otherwise it keeps the
// previous lane's value; the chain runs through the clock's valid lanes in
// order and carries to the next clock. rise/fall mark the lanes where the
// state changes. Invalid lanes keep the state and carry no event.
// Latency: 2 clocks from x to state/rise/fall.
// TRLC-LINKS: REQ-HD-015
module trig_cmp #(parameter integer L = 8) (
  input  wire          clk,
  input  wire          rst,
  input  wire [L*12-1:0] x,
  input  wire [L-1:0]  v,
  input  wire [11:0]   hi,
  input  wire [11:0]   lo,
  output reg  [L-1:0]  state = {L{1'b0}},
  output reg  [L-1:0]  rise = {L{1'b0}},
  output reg  [L-1:0]  fall = {L{1'b0}}
);
  reg [L-1:0] above = 0, below = 0, v1 = 0;
  integer i;
  always @(posedge clk) begin
    for (i = 0; i < L; i = i + 1) begin
      above[i] <= x[12*i +: 12] >= hi;
      below[i] <= x[12*i +: 12] <= lo;
    end
    v1 <= rst ? {L{1'b0}} : v;
  end
  reg s, p;
  reg carry = 1'b0;
  always @(posedge clk) begin
    s = carry;
    for (i = 0; i < L; i = i + 1) begin
      p = s;
      if (v1[i]) begin
        if (above[i]) s = 1'b1;
        else if (below[i]) s = 1'b0;
      end
      state[i] <= s;
      rise[i] <= v1[i] & s & ~p;
      fall[i] <= v1[i] & ~s & p;
    end
    carry <= rst ? 1'b0 : s;
  end
endmodule
