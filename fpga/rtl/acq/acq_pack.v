// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// acq_pack -- the P12 packer of one channel (spec 20 §6.1): record words
// (0 … 8 a clock, the upper 12 bits of each 16-bit word) into groups of 32
// words in 384 bits, word k of a group at bits [12k + 11 : 12k]. Each
// completed group comes out with its index (groups since clear). flush
// emits a partial group padded with zeros. take selects n consecutive
// words of the clock starting at word k0. Latency: 1 clock.
// TRLC-LINKS: REQ-HD-015
module acq_pack (
  input  wire          clk,
  input  wire          clear,
  input  wire [127:0]  words,
  input  wire [3:0]    k0,
  input  wire [3:0]    n,
  input  wire          flush,
  output reg           gv = 1'b0,
  output reg  [383:0]  gdata = 384'd0,
  output reg  [31:0]   gidx = 32'd0
);
  reg [479:0] buf_q = 480'd0;
  reg [5:0]   cnt = 6'd0;
  reg [31:0]  ng = 32'd0;
  reg [479:0] b; reg [6:0] c;
  integer i;
  always @(*) begin
    b = buf_q; c = {1'b0, cnt};
    for (i = 0; i < 8; i = i + 1) if (i < n) begin
      b[12*c +: 12] = words[16*(k0 + i[3:0]) + 4 +: 12];   // the code, without the word's low nibble
      c = c + 7'd1;
    end
  end
  always @(posedge clk) begin
    gv <= 1'b0;
    if (clear) begin
      buf_q <= 480'd0; cnt <= 6'd0; ng <= 32'd0;
    end else if (c >= 7'd32) begin
      gv <= 1'b1; gdata <= b[383:0]; gidx <= ng; ng <= ng + 32'd1;
      buf_q <= {384'd0, b[479:384]}; cnt <= c[5:0] - 6'd32;
    end else if (flush && c != 7'd0) begin
      gv <= 1'b1; gdata <= b[383:0]; gidx <= ng; ng <= ng + 32'd1;
      buf_q <= 480'd0; cnt <= 6'd0;
    end else begin
      buf_q <= b; cnt <= c[5:0];
    end
  end
endmodule
