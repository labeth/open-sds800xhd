// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// acq_route -- the route stage of spec 20 §5.2: the two ADCs' I/Q words
// (4 I and 4 Q samples per clock each, oldest in bits 11:0) become the four
// channel streams the trigger, record, stream and mean paths share
// (channel c's lane i at xs[96c + 12i +: 12], valid lanes vmask, ticks per
// lane tpl), then invert and GND substitute per channel.
//
// Channel-to-core mapping (UNVERIFIED, spec 20 §14 U14; the bring-up fills
// it from ADC test patterns): CH1/CH2 on ADC0, CH3/CH4 on ADC1, the even
// channel on the I core and the odd one on Q. Route 0 (one channel at
// 2 GSa/s): the lowest channel of mask, its ADC in DES with I first
// (I0 Q0 I1 Q1 ...). Route 1: four lanes per channel from its core. Route 2:
// every second sample of the core, two lanes. Latency: 1 clock.
// TRLC-LINKS: REQ-HD-015
module acq_route (
  input  wire          clk,
  input  wire [47:0]   adc0_i,
  input  wire [47:0]   adc0_q,
  input  wire [47:0]   adc1_i,
  input  wire [47:0]   adc1_q,
  input  wire [1:0]    route,
  input  wire [3:0]    mask,
  input  wire [3:0]    invert,
  input  wire [3:0]    gndsub,
  input  wire [47:0]   gndcode,       // channel c at [12c +: 12]
  output reg  [383:0]  xs = 384'd0,
  output reg  [7:0]    vmask = 8'hFF,
  output reg  [2:0]    tpl = 3'd1
);
  reg [1:0] des;
  always @(*) begin
    des = 2'd0;
    if (mask[0]) des = 2'd0; else if (mask[1]) des = 2'd1; else if (mask[2]) des = 2'd2; else if (mask[3]) des = 2'd3;
  end
  reg [47:0] core, ci, cq;
  reg [11:0] s;
  integer c, l;
  always @(posedge clk) begin
    case (route)
      2'd0: begin vmask <= 8'hFF; tpl <= 3'd1; end
      2'd1: begin vmask <= 8'h0F; tpl <= 3'd2; end
      default: begin vmask <= 8'h03; tpl <= 3'd4; end
    endcase
    for (c = 0; c < 4; c = c + 1) begin
      ci = (c < 2) ? adc0_i : adc1_i;
      cq = (c < 2) ? adc0_q : adc1_q;
      core = c[0] ? cq : ci;
      for (l = 0; l < 8; l = l + 1) begin
        if (route == 2'd0 && c == {30'd0, des}) s = l[0] ? cq[12*(l/2) +: 12] : ci[12*(l/2) +: 12];
        else if (route == 2'd2) s = (l < 2) ? core[24*l +: 12] : 12'd0;
        else s = (l < 4) ? core[12*l +: 12] : 12'd0;
        if (invert[c]) s = 12'hFFF - s;
        if (gndsub[c]) s = gndcode[12*c +: 12];
        xs[96*c + 12*l +: 12] <= s;
      end
    end
  end
endmodule
