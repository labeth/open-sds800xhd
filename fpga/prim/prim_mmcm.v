// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// prim_mmcm -- ADC data-clock manager: from the 500 MHz DCLK, clk_bit
// (500 MHz) and clk_div (250 MHz) with a dynamic phase shift. A ps_step pulse
// moves both outputs by one fine step in the direction ps_inc; ps_done pulses
// when the step is applied. Outputs are gated low and locked is low while rst
// is high and until the MMCM locks. The VCO runs at 1000 MHz (M 2, D 1), inside
// the -2 speed grade's range. The model divides clk_in and locks after
// LOCK_CYCLES input clocks; its phase steps only count.
// TRLC-LINKS: REQ-HD-015
module prim_mmcm #(parameter real CLKIN_PERIOD_NS = 2.0, parameter integer LOCK_CYCLES = 64) (
  input  wire clk_in,
  input  wire rst,
  input  wire ps_clk,
  input  wire ps_step,
  input  wire ps_inc,
  output wire ps_done,
  output wire clk_bit,
  output wire clk_div,
  output wire locked
);
`ifdef SIM
  reg [15:0] n = 16'd0;
  reg lk = 1'b0, div = 1'b0;
  always @(posedge clk_in or posedge rst)
    if (rst) begin n <= 16'd0; lk <= 1'b0; end
    else if (n == LOCK_CYCLES[15:0]) lk <= 1'b1;
    else n <= n + 1'b1;
  always @(posedge clk_in) div <= ~div;
  assign clk_bit = clk_in & lk;
  assign clk_div = div & lk;
  assign locked = lk;
  reg [3:0] ps_wait = 4'd0;
  reg done_r = 1'b0;
  integer phase = 0;
  always @(posedge ps_clk) begin
    done_r <= 1'b0;
    if (ps_step) begin ps_wait <= 4'd12; phase = phase + (ps_inc ? 1 : -1); end
    else if (ps_wait == 4'd1) begin ps_wait <= 4'd0; done_r <= 1'b1; end
    else if (ps_wait != 4'd0) ps_wait <= ps_wait - 1'b1;
  end
  assign ps_done = done_r;
`else
  wire fb, c0, c1;
  MMCME4_ADV #(.CLKIN1_PERIOD(CLKIN_PERIOD_NS), .DIVCLK_DIVIDE(1), .CLKFBOUT_MULT_F(2.0),
               .CLKOUT0_DIVIDE_F(2.0), .CLKOUT1_DIVIDE(4), .CLKOUT0_USE_FINE_PS("TRUE"),
               .CLKOUT1_USE_FINE_PS("TRUE"), .STARTUP_WAIT("FALSE"))
    u_mmcm (.CLKIN1(clk_in), .CLKIN2(1'b0), .CLKINSEL(1'b1), .CLKFBIN(fb), .CLKFBOUT(fb),
            .CLKOUT0(c0), .CLKOUT1(c1), .RST(rst), .PWRDWN(1'b0), .LOCKED(locked),
            .PSCLK(ps_clk), .PSEN(ps_step), .PSINCDEC(ps_inc), .PSDONE(ps_done),
            .DCLK(1'b0), .DEN(1'b0), .DWE(1'b0), .DADDR(7'd0), .DI(16'd0), .DO(), .DRDY(),
            .CDDCREQ(1'b0), .CDDCDONE(), .CLKFBOUTB(), .CLKOUT0B(), .CLKOUT1B(), .CLKOUT2(),
            .CLKOUT2B(), .CLKOUT3(), .CLKOUT3B(), .CLKOUT4(), .CLKOUT5(), .CLKOUT6(),
            .CLKINSTOPPED(), .CLKFBSTOPPED());
  BUFGCE u_b0 (.I(c0), .CE(locked), .O(clk_bit));
  BUFGCE u_b1 (.I(c1), .CE(locked), .O(clk_div));
`endif
endmodule
