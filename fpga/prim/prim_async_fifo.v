// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// prim_async_fifo -- dual-clock FIFO, 2^AW words of DW bits. Gray pointers
// cross through prim_sync; full and empty are conservative (a word written is
// visible to the reader a few clocks later, a word read frees room a few clocks
// later). Read is synchronous: rd_en with !rd_empty gives rd_data on the next
// rd_clk edge with rd_valid. Levels are in words, each in its own domain.
// The memory is inferred (block RAM for large sizes); no vendor primitive.
// TRLC-LINKS: REQ-HD-015
module prim_async_fifo #(parameter integer DW = 32, parameter integer AW = 4) (
  input  wire          wr_clk,
  input  wire          wr_rst,
  input  wire          wr_en,
  input  wire [DW-1:0] wr_data,
  output wire          wr_full,
  output wire [AW:0]   wr_level,
  input  wire          rd_clk,
  input  wire          rd_rst,
  input  wire          rd_en,
  output reg  [DW-1:0] rd_data = {DW{1'b0}},
  output reg           rd_valid = 1'b0,
  output wire          rd_empty,
  output wire [AW:0]   rd_level
);
  reg [DW-1:0] mem [0:(1<<AW)-1];
  function [AW:0] bin2gray(input [AW:0] b); bin2gray = b ^ (b >> 1); endfunction
  function [AW:0] gray2bin(input [AW:0] g);
    integer k;
    begin
      gray2bin[AW] = g[AW];
      for (k = AW - 1; k >= 0; k = k - 1) gray2bin[k] = gray2bin[k+1] ^ g[k];
    end
  endfunction
  reg [AW:0] wbin = 0, wgray = 0, rbin = 0, rgray = 0;
  wire [AW:0] rgray_w, wgray_r;
  prim_sync #(.STAGES(2), .W(AW+1)) s_r2w (.clk(wr_clk), .d(rgray), .q(rgray_w));
  prim_sync #(.STAGES(2), .W(AW+1)) s_w2r (.clk(rd_clk), .d(wgray), .q(wgray_r));
  wire [AW:0] rbin_w = gray2bin(rgray_w);
  wire [AW:0] wbin_r = gray2bin(wgray_r);
  assign wr_level = wbin - rbin_w;
  assign wr_full = wr_level[AW];
  assign rd_level = wbin_r - rbin;
  assign rd_empty = (rd_level == 0);
  always @(posedge wr_clk)
    if (wr_rst) begin wbin <= 0; wgray <= 0; end
    else if (wr_en && !wr_full) begin
      mem[wbin[AW-1:0]] <= wr_data;
      wbin <= wbin + 1'b1;
      wgray <= bin2gray(wbin + 1'b1);
    end
  always @(posedge rd_clk) begin
    rd_valid <= 1'b0;
    if (rd_rst) begin rbin <= 0; rgray <= 0; end
    else if (rd_en && !rd_empty) begin
      rd_data <= mem[rbin[AW-1:0]];
      rd_valid <= 1'b1;
      rbin <= rbin + 1'b1;
      rgray <= bin2gray(rbin + 1'b1);
    end
  end
endmodule
