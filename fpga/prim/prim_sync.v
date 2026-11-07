// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// prim_sync -- level synchronizer: STAGES flip-flops with ASYNC_REG and no
// logic between them. The XDC selects these cells by ASYNC_REG and bounds the
// path into the first stage (spec 20 §11); nothing is constrained by path name.
// q follows a stable d after STAGES destination clocks. A multi-bit d must be
// gray-coded or otherwise change one bit at a time.
// TRLC-LINKS: REQ-HD-015
module prim_sync #(parameter integer STAGES = 2, parameter integer W = 1, parameter [W-1:0] INIT = {W{1'b0}}) (
  input  wire         clk,
  input  wire [W-1:0] d,
  output wire [W-1:0] q
);
  (* ASYNC_REG = "TRUE" *) reg [W-1:0] chain [0:STAGES-1];
  integer i;
  initial for (i = 0; i < STAGES; i = i + 1) chain[i] = INIT;
  always @(posedge clk) begin
    chain[0] <= d;
    for (i = 1; i < STAGES; i = i + 1) chain[i] <= chain[i-1];
  end
  assign q = chain[STAGES-1];
endmodule
