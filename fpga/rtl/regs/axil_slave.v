// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// axil_slave -- AXI4-Lite slave to a simple register bus. Every access
// answers OKAY (spec 20 §4.3: an error response would be an external abort in
// the PS); a register file that does not decode an address reads 0 and ignores
// the write. One access at a time; writes take precedence over reads. A read
// presents rd for one clock and samples rd_data on the first following
// clock with rd_hold low (a register file that must fetch a snapshot from
// another clock domain holds it; it bounds the wait itself).
// TRLC-LINKS: REQ-HD-015
module axil_slave #(parameter integer AW = 16) (
  input  wire          clk,
  input  wire          rst,
  input  wire [AW-1:0] s_awaddr,
  input  wire          s_awvalid,
  output wire          s_awready,
  input  wire [31:0]   s_wdata,
  input  wire [3:0]    s_wstrb,
  input  wire          s_wvalid,
  output wire          s_wready,
  output wire [1:0]    s_bresp,
  output reg           s_bvalid = 1'b0,
  input  wire          s_bready,
  input  wire [AW-1:0] s_araddr,
  input  wire          s_arvalid,
  output wire          s_arready,
  output reg  [31:0]   s_rdata = 32'd0,
  output wire [1:0]    s_rresp,
  output reg           s_rvalid = 1'b0,
  input  wire          s_rready,
  // register bus
  output reg           wr = 1'b0,
  output reg  [AW-1:0] wr_addr = {AW{1'b0}},
  output reg  [31:0]   wr_data = 32'd0,
  output reg  [3:0]    wr_strb = 4'd0,
  output reg           rd = 1'b0,
  output reg  [AW-1:0] rd_addr = {AW{1'b0}},
  input  wire [31:0]   rd_data,
  input  wire          rd_hold
);
  assign s_bresp = 2'b00;
  assign s_rresp = 2'b00;
  localparam IDLE = 2'd0, WRESP = 2'd1, RWAIT = 2'd2, RRESP = 2'd3;
  reg [1:0] st = IDLE;
  reg have_aw = 1'b0, have_w = 1'b0;
  // Ready while idle; a handshake is valid && ready at a clock edge.
  assign s_awready = (st == IDLE) && !have_aw && !rst;
  assign s_wready  = (st == IDLE) && !have_w && !rst;
  assign s_arready = (st == IDLE) && !have_aw && !have_w && !s_awvalid && !s_wvalid && !rst;
  always @(posedge clk) begin
    wr <= 1'b0; rd <= 1'b0;
    if (rst) begin
      st <= IDLE; have_aw <= 1'b0; have_w <= 1'b0; s_bvalid <= 1'b0; s_rvalid <= 1'b0;
    end else case (st)
      IDLE: begin
        if (s_awvalid && s_awready) begin wr_addr <= s_awaddr; have_aw <= 1'b1; end
        if (s_wvalid && s_wready) begin wr_data <= s_wdata; wr_strb <= s_wstrb; have_w <= 1'b1; end
        if (have_aw && have_w) begin
          wr <= 1'b1; have_aw <= 1'b0; have_w <= 1'b0; s_bvalid <= 1'b1; st <= WRESP;
        end else if (s_arvalid && s_arready) begin
          rd_addr <= s_araddr; rd <= 1'b1; st <= RWAIT;
        end
      end
      WRESP: if (s_bready) begin s_bvalid <= 1'b0; st <= IDLE; end
      RWAIT: if (!rd_hold) begin s_rdata <= rd_data; s_rvalid <= 1'b1; st <= RRESP; end
      RRESP: if (s_rready) begin s_rvalid <= 1'b0; st <= IDLE; end
    endcase
  end
endmodule
