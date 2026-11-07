// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// prim_reset_sync -- asynchronous assert, synchronous release. rst_out is
// held for STAGES clocks after rst_in releases. Active high in and out.
// TRLC-LINKS: REQ-HD-015
module prim_reset_sync #(parameter integer STAGES = 3) (
  input  wire clk,
  input  wire rst_in,
  output wire rst_out
);
  (* ASYNC_REG = "TRUE" *) reg [STAGES-1:0] chain = {STAGES{1'b1}};
  always @(posedge clk or posedge rst_in)
    if (rst_in) chain <= {STAGES{1'b1}};
    else chain <= {chain[STAGES-2:0], 1'b0};
  assign rst_out = chain[STAGES-1];
endmodule
