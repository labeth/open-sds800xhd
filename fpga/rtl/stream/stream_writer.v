// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// stream_writer -- the framer's beats onto S_AXI_HP1 (spec 20 §4.1, §8.1):
// one single-beat 64-bit write per beat, up to 16 outstanding. A block is
// published (head) only when the write response of its last beat came
// back, so the PS never reads a block before it is in memory. A response
// other than OKAY raises axi_err.
// TRLC-LINKS: REQ-HD-015
module stream_writer (
  input  wire         clk,
  input  wire         rst,
  input  wire         in_valid,
  output wire         in_ready,
  input  wire [63:0]  in_data,
  input  wire [31:0]  in_addr,
  input  wire         in_last,
  output reg  [31:0]  m_awaddr = 32'd0,
  output wire [7:0]   m_awlen,
  output reg          m_awvalid = 1'b0,
  input  wire         m_awready,
  output reg  [63:0]  m_wdata = 64'd0,
  output wire         m_wlast,
  output reg          m_wvalid = 1'b0,
  input  wire         m_wready,
  input  wire [1:0]   m_bresp,
  input  wire         m_bvalid,
  output wire         m_bready,
  output reg  [31:0]  head = 32'd0,
  output reg          block_done = 1'b0,
  output reg          axi_err = 1'b0
);
  assign m_awlen = 8'd0;
  assign m_wlast = 1'b1;
  assign m_bready = 1'b1;
  reg [15:0] lastq = 16'd0;           // last flags of the outstanding writes, oldest in bit 0
  reg [4:0]  outst = 5'd0;
  wire busy = m_awvalid || m_wvalid;
  assign in_ready = !busy && outst < 5'd16;
  wire take = in_valid && in_ready;
  reg [15:0] lq; reg [4:0] on;
  always @(posedge clk) begin
    block_done <= 1'b0;
    if (rst) begin
      m_awvalid <= 1'b0; m_wvalid <= 1'b0; outst <= 5'd0; lastq <= 16'd0; head <= 32'd0; axi_err <= 1'b0;
    end else begin
      if (m_awvalid && m_awready) m_awvalid <= 1'b0;
      if (m_wvalid && m_wready) m_wvalid <= 1'b0;
      lq = lastq; on = outst;
      if (m_bvalid) begin
        if (lq[0]) begin head <= head + 32'd1; block_done <= 1'b1; end
        if (m_bresp != 2'b00) axi_err <= 1'b1;
        lq = {1'b0, lq[15:1]}; on = on - 5'd1;
      end
      if (take) begin
        m_awaddr <= in_addr; m_awvalid <= 1'b1; m_wdata <= in_data; m_wvalid <= 1'b1;
        lq[on[3:0]] = in_last; on = on + 5'd1;
      end
      lastq <= lq; outst <= on;
    end
  end
endmodule
