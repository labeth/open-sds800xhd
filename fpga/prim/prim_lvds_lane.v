// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// prim_lvds_lane -- one ADC data lane: differential input buffer, fixed input
// delay and a 1:4 DDR deserializer. q[0] is the oldest bit of the four received
// in one clkdiv cycle (the bit after the rising clk edge that follows the
// previous group). Latency after rst release is fixed for a given build.
// DIFF_TERM and IOSTANDARD come from the XDC (spec 20 §14 U5); DELAY_PS is
// the fixed IDELAYE3 delay (lane group A 24 taps, group B 0 in the vendor
// image, spec 12 §6.2).
// TRLC-LINKS: REQ-HD-015
module prim_lvds_lane #(parameter integer DELAY_PS = 0, parameter real REFCLK_MHZ = 500.0) (
  input  wire       pad_p,
  input  wire       pad_n,
  input  wire       clk,      // bit clock, DDR (DCLK, 500 MHz)
  input  wire       clk_b,    // inverted bit clock (ISERDESE3 CLK_B)
  input  wire       clkdiv,   // clk / 2
  input  wire       rst,
  output wire [3:0] q
);
`ifdef SIM
  // Model: sample the P pad on both edges of clk, emit four bits per clkdiv.
  reg [3:0] sr = 4'd0;
  reg [3:0] q_r = 4'd0;
  always @(posedge clk) sr <= {pad_p, sr[3:1]};
  always @(negedge clk) sr <= {pad_p, sr[3:1]};
  always @(posedge clkdiv) q_r <= rst ? 4'd0 : sr;
  assign q = q_r;
`else
  wire din, ddly;
  IBUFDS u_ibuf (.I(pad_p), .IB(pad_n), .O(din));
  IDELAYE3 #(.DELAY_FORMAT("TIME"), .DELAY_TYPE("FIXED"), .DELAY_VALUE(DELAY_PS),
             .REFCLK_FREQUENCY(REFCLK_MHZ), .UPDATE_MODE("ASYNC"), .SIM_DEVICE("ULTRASCALE_PLUS"))
    u_dly (.IDATAIN(din), .DATAIN(1'b0), .DATAOUT(ddly), .CLK(1'b0), .CE(1'b0), .INC(1'b0),
           .LOAD(1'b0), .CNTVALUEIN(9'd0), .CNTVALUEOUT(), .CASC_IN(1'b0), .CASC_RETURN(1'b0),
           .CASC_OUT(), .EN_VTC(1'b1), .RST(rst));
  wire [7:0] qq;
  ISERDESE3 #(.DATA_WIDTH(4), .FIFO_ENABLE("FALSE"), .FIFO_SYNC_MODE("FALSE"), .SIM_DEVICE("ULTRASCALE_PLUS"))
    u_ser (.D(ddly), .CLK(clk), .CLK_B(clk_b), .CLKDIV(clkdiv), .RST(rst), .FIFO_RD_CLK(1'b0),
           .FIFO_RD_EN(1'b0), .FIFO_EMPTY(), .INTERNAL_DIVCLK(), .Q(qq));
  assign q = qq[3:0];
`endif
endmodule
