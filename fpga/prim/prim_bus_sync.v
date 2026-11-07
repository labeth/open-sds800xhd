// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// prim_bus_sync -- moves a W-bit word between clock domains whole: the
// destination sees the old or the new word, never a mix. src_load captures
// src_data when ready (src_ready high); dst_valid pulses once the word is
// stable in dst_data. One transfer per handshake round trip.
// TRLC-LINKS: REQ-HD-015
module prim_bus_sync #(parameter integer W = 32) (
  input  wire         src_clk,
  input  wire         src_load,
  input  wire [W-1:0] src_data,
  output wire         src_ready,
  input  wire         dst_clk,
  output reg  [W-1:0] dst_data = {W{1'b0}},
  output reg          dst_valid = 1'b0
);
  reg [W-1:0] hold = {W{1'b0}};
  reg req = 1'b0;
  wire ack_src;
  assign src_ready = (req == ack_src);
  always @(posedge src_clk)
    if (src_load && src_ready) begin hold <= src_data; req <= ~req; end
  wire req_dst;
  prim_sync #(.STAGES(2)) s_req (.clk(dst_clk), .d(req), .q(req_dst));
  reg ack = 1'b0;
  always @(posedge dst_clk) begin
    dst_valid <= 1'b0;
    if (req_dst != ack) begin dst_data <= hold; ack <= req_dst; dst_valid <= 1'b1; end
  end
  prim_sync #(.STAGES(2)) s_ack (.clk(src_clk), .d(ack), .q(ack_src));
endmodule
