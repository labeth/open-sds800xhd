// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// trig_counter -- the frequency counter and totalizer of spec 20 §7.9 on
// one selected event vector of the trigger engine (§7.3), every lane
// counted. Gate mode: over each gate of GATE clocks, COUNT = events and
// PERIOD = ticks from the gate's first counted event to its last; both are
// latched at the gate's end (done) and the next gate starts at once, so
// the PS computes the frequency to the tick as (COUNT − 1) / PERIOD.
// Totalizer: COUNT runs free from the last clear. t is the time of lane 0
// of the event vectors' clock.
// TRLC-LINKS: REQ-HD-015
module trig_counter #(parameter integer L = 8) (
  input  wire          clk,
  input  wire          rst,
  input  wire          en,
  input  wire          totalize,
  input  wire [4:0]    sel,
  input  wire [31:0]   gate,
  input  wire          clear,
  input  wire [32*L-1:0] ev,        // lane i's event bits at [32i +: 32]
  input  wire [2:0]    tpl,
  input  wire [47:0]   t,
  output reg  [31:0]   count = 32'd0,
  output reg  [31:0]   period = 32'd0,
  output reg           done = 1'b0
);
  reg [31:0] n = 32'd0, gc = 32'd0;
  reg [47:0] tf = 48'd0, tl = 48'd0;
  reg have = 1'b0;
  reg [3:0] k; reg [2:0] fi, li; reg any; reg [47:0] pd;
  integer i;
  always @(*) begin
    k = 4'd0; fi = 3'd0; li = 3'd0; any = 1'b0;
    for (i = L - 1; i >= 0; i = i - 1) if (ev[32*i + {27'd0, sel}]) begin k = k + 4'd1; fi = i[2:0]; any = 1'b1; end
    for (i = 0; i < L; i = i + 1) if (ev[32*i + {27'd0, sel}]) li = i[2:0];
  end
  wire [47:0] t_first = t + {45'd0, fi} * tpl;
  wire [47:0] t_last = t + {45'd0, li} * tpl;
  always @(posedge clk) begin
    if (rst || !en || clear) begin
      n <= 32'd0; gc <= 32'd0; have <= 1'b0;
      if (rst || clear) begin count <= 32'd0; period <= 32'd0; done <= 1'b0; end
    end else if (totalize) begin
      count <= count + {28'd0, k};
    end else begin
      n <= n + {28'd0, k};
      if (any) begin if (!have) tf <= t_first; tl <= t_last; have <= 1'b1; end
      gc <= gc + 32'd1;
      if (gc + 32'd1 >= gate) begin
        count <= n + {28'd0, k};
        pd = have ? (any ? t_last - tf : tl - tf) : (any ? t_last - t_first : 48'd0);
        period <= pd[31:0];
        done <= 1'b1;
        n <= 32'd0; gc <= 32'd0; have <= 1'b0;
      end
    end
  end
endmodule
