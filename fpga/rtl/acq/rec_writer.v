// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// rec_writer -- the record writer of spec 20 §6.3: per channel an
// asynchronous FIFO of {address, P12 group} (cclk -> mclk, 512 groups,
// high-water mark at 500), drained round-robin onto S_AXI_HP0's write
// channels, one INCR burst of three 128-bit beats per group, up to eight
// bursts outstanding. written counts the groups whose write response came
// back (the acquisition's DONE waits for it). A response other than OKAY
// raises axi_err.
// TRLC-LINKS: REQ-HD-015
module rec_writer (
  input  wire          cclk,
  input  wire          crst,
  input  wire [3:0]    push,
  input  wire [1663:0] push_data,
  output wire [3:0]    hiwater,
  input  wire          mclk,
  input  wire          mrst,
  output reg  [31:0]   m_awaddr = 32'd0,
  output wire [7:0]    m_awlen,
  output reg           m_awvalid = 1'b0,
  input  wire          m_awready,
  output reg  [127:0]  m_wdata = 128'd0,
  output reg           m_wlast = 1'b0,
  output reg           m_wvalid = 1'b0,
  input  wire          m_wready,
  input  wire [1:0]    m_bresp,
  input  wire          m_bvalid,
  output wire          m_bready,
  output reg  [31:0]   written = 32'd0,
  output reg           axi_err = 1'b0
);
  assign m_awlen = 8'd2;
  assign m_bready = 1'b1;
  wire [3:0] empty, rvalid;
  wire [1663:0] rdata;
  reg  [3:0] ren = 4'd0;
  genvar c;
  generate for (c = 0; c < 4; c = c + 1) begin : g_fifo
    wire [9:0] wl;
    prim_async_fifo #(.DW(416), .AW(9)) u_fifo (.wr_clk(cclk), .wr_rst(crst), .wr_en(push[c]),
      .wr_data(push_data[416*c +: 416]), .wr_full(), .wr_level(wl), .rd_clk(mclk), .rd_rst(mrst), .rd_en(ren[c]),
      .rd_data(rdata[416*c +: 416]), .rd_valid(rvalid[c]), .rd_empty(empty[c]), .rd_level());
    assign hiwater[c] = wl >= 10'd500;
  end endgenerate
  // One group is prefetched from the FIFOs while the previous one's beats go
  // out, so the port sends a group every three clocks (100 M groups/s at
  // 300 MHz, 3.2 G words/s; 2 GSa/s needs 62.5 M groups/s).
  reg sending = 1'b0;
  reg [1:0] rr = 2'd0, pch = 2'd0;
  reg pre = 1'b0, nv = 1'b0;            // a FIFO read is in flight; a group waits in n*
  reg [415:0] nxt = 416'd0;
  reg [383:0] grp = 384'd0;
  reg [1:0] beat = 2'd0;
  reg [3:0] outst = 4'd0;
  reg aw_done = 1'b0;
  wire b_ok = m_bvalid;
  wire aw_fire = m_awvalid && m_awready;
  wire w_fire = m_wvalid && m_wready;
  integer k; reg found; reg [1:0] pick;
  always @(*) begin
    found = 1'b0; pick = rr;
    for (k = 0; k < 4; k = k + 1)
      if (!found && !empty[(rr + k[1:0])]) begin found = 1'b1; pick = rr + k[1:0]; end
  end
  // the current group is done when its address went and its last beat goes now (or went)
  wire last_out = sending && (aw_done || aw_fire) && ((w_fire && beat == 2'd2) || !m_wvalid);
  reg take; reg [415:0] src;
  always @(posedge mclk) begin
    ren <= 4'd0;
    if (mrst) begin
      sending <= 1'b0; pre <= 1'b0; nv <= 1'b0; m_awvalid <= 1'b0; m_wvalid <= 1'b0; outst <= 4'd0;
      written <= 32'd0; axi_err <= 1'b0;
    end else begin
      if (b_ok) begin written <= written + 32'd1; if (m_bresp != 2'b00) axi_err <= 1'b1; end
      outst <= outst + (aw_fire ? 4'd1 : 4'd0) - (b_ok ? 4'd1 : 4'd0);
      // the beats of the current group
      if (aw_fire) begin m_awvalid <= 1'b0; aw_done <= 1'b1; end
      if (w_fire) begin
        if (beat == 2'd2) m_wvalid <= 1'b0;
        else begin
          beat <= beat + 2'd1;
          m_wdata <= grp[128*({6'd0, beat} + 8'd1) +: 128];
          m_wlast <= (beat == 2'd1);
        end
      end
      if (last_out) sending <= 1'b0;
      // the next group: from the prefetch register, or straight from the FIFO
      take = 1'b0; src = nxt;
      if ((!sending || last_out) && outst < 4'd8) begin
        if (nv) begin take = 1'b1; src = nxt; nv <= 1'b0; end
        else if (pre && rvalid[pch]) begin take = 1'b1; src = rdata[416*pch +: 416]; pre <= 1'b0; end
      end
      if (!take && pre && rvalid[pch]) begin nxt <= rdata[416*pch +: 416]; nv <= 1'b1; pre <= 1'b0; end
      if (take) begin
        {m_awaddr, grp} <= src;
        m_awvalid <= 1'b1; aw_done <= 1'b0; sending <= 1'b1;
        m_wdata <= src[127:0]; m_wlast <= 1'b0; m_wvalid <= 1'b1; beat <= 2'd0;
      end
      // keep one FIFO read in flight while there is room for its group
      if (!pre && found && (!nv || take) && !(pre && rvalid[pch])) begin
        ren[pick] <= 1'b1; pch <= pick; rr <= pick + 2'd1; pre <= 1'b1;
      end
    end
  end
endmodule
