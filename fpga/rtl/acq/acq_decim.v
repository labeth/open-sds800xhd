// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// acq_decim -- the decimator/reducer of spec 20 §5.3 for the four channel
// streams at once (they share the route, so the group phase is common):
// groups of D consecutive valid samples, D any integer 1 … 2^28 − 1.
//   mode 0 pick: one word per group, the group's first sample (code << 4);
//   mode 1 box:  the group mean in Q12.4 (sum · 16 / D) when D is a power of
//                two, pick otherwise;
//   mode 2 peak: two words per group, maximum then minimum (D ≥ 2; D = 1
//                picks).
// Group g yields word g (pick, box) or words 2g, 2g + 1 (peak); words and
// groups count from 0 at the first valid sample after restart. Per clock
// the outputs give the words completed in that clock (nw, oldest first, the
// first one's index wfirst), each word's group start time, and per input
// lane the group it belongs to and its position in that group (what the
// acquisition needs to place a trigger in the record, spec 20 §6.2).
// t_clk is the time of the input clock's lane 0. Latency: 1 clock.
// The lane loop is a combinational chain: functional, not timing-closed.
// TRLC-LINKS: REQ-HD-015
module acq_decim (
  input  wire          clk,
  input  wire          restart,
  input  wire [383:0]  xs,
  input  wire [7:0]    v,
  input  wire [2:0]    tpl,
  input  wire [47:0]   t_clk,
  input  wire [27:0]   d,
  input  wire [1:0]    mode,
  output reg  [3:0]    nw = 4'd0,
  output reg  [511:0]  words = 512'd0,   // channel c, word k at [128c + 16k +: 16]
  output reg  [47:0]   wfirst = 48'd0,
  output reg  [383:0]  wt = 384'd0,      // word k's group start time at [48k +: 48]
  output reg  [383:0]  grp = 384'd0,     // lane l's group at [48l +: 48]
  output reg  [223:0]  ph = 224'd0,      // lane l's position in its group at [28l +: 28]
  output reg  [47:0]   t_out = 48'd0,
  output reg  [7:0]    v_out = 8'd0
);
  // the box shift: log2(d) when d is a power of two (continuous assignments:
  // a simulator evaluates them at time zero, an always block only on a change)
  function [4:0] log2d(input [27:0] x);
    integer j;
    begin log2d = 5'd0; for (j = 0; j < 28; j = j + 1) if (x[j]) log2d = j[4:0]; end
  endfunction
  wire [4:0] k2 = log2d(d);
  wire pow2 = (d != 28'd0) && ((d & (d - 28'd1)) == 28'd0);
  wire [1:0] m = (mode == 2'd1 && !pow2) ? 2'd0 : (mode == 2'd2 && d < 28'd2) ? 2'd0 : mode;
  // running state between clocks
  reg [27:0]  cnt = 28'd0;          // position of the next sample in its group
  reg [47:0]  g = 48'd0;            // groups started
  reg [47:0]  wn = 48'd0;           // words completed
  reg [47:0]  gt = 48'd0;           // start time of the open group
  reg [159:0] acc = 160'd0;         // per channel: sum of the open group (40 bits)
  reg [47:0]  mx = 48'd0, mn = {4{12'hFFF}};
  // combinational lane chain
  reg [27:0]  c_cnt; reg [47:0] c_g, c_gt; reg [159:0] c_acc; reg [47:0] c_mx, c_mn;
  reg [3:0]   c_nw; reg [511:0] c_words; reg [383:0] c_wt, c_grp; reg [223:0] c_ph;
  reg [11:0]  x; reg [39:0] a; reg [39:0] q;
  integer l, c, k;
  always @(*) begin
    c_cnt = cnt; c_g = g; c_gt = gt; c_acc = acc; c_mx = mx; c_mn = mn;
    c_nw = 4'd0; c_words = 512'd0; c_wt = 384'd0; c_grp = 384'd0; c_ph = 224'd0;
    x = 12'd0; a = 40'd0; q = 40'd0; k = 0;
    for (l = 0; l < 8; l = l + 1) if (v[l]) begin
      if (c_cnt == 28'd0) begin
        c_g = c_g + 48'd1;
        c_gt = t_clk + {45'd0, tpl} * l;
        for (c = 0; c < 4; c = c + 1) begin
          c_acc[40*c +: 40] = 40'd0; c_mx[12*c +: 12] = 12'd0; c_mn[12*c +: 12] = 12'hFFF;
        end
        if (m == 2'd0) begin
          for (c = 0; c < 4; c = c + 1) c_words[128*c + 16*c_nw +: 16] = {xs[96*c + 12*l +: 12], 4'd0};
          c_wt[48*c_nw +: 48] = c_gt;
          c_nw = c_nw + 4'd1;
        end
      end
      c_grp[48*l +: 48] = c_g - 48'd1;
      c_ph[28*l +: 28] = c_cnt;
      for (c = 0; c < 4; c = c + 1) begin
        x = xs[96*c + 12*l +: 12];
        c_acc[40*c +: 40] = c_acc[40*c +: 40] + {28'd0, x};
        if (x > c_mx[12*c +: 12]) c_mx[12*c +: 12] = x;
        if (x < c_mn[12*c +: 12]) c_mn[12*c +: 12] = x;
      end
      c_cnt = c_cnt + 28'd1;
      if (c_cnt == d || d == 28'd0) begin
        c_cnt = 28'd0;
        if (m == 2'd1) begin
          for (c = 0; c < 4; c = c + 1) begin
            a = c_acc[40*c +: 40];
            q = (a << 4) >> k2;
            c_words[128*c + 16*c_nw +: 16] = q[15:0];  // Q12.4: the quotient fits 16 bits
          end
          c_wt[48*c_nw +: 48] = c_gt;
          c_nw = c_nw + 4'd1;
        end else if (m == 2'd2) begin
          k = {28'd0, c_nw};
          for (c = 0; c < 4; c = c + 1) begin
            c_words[128*c + 16*k +: 16] = {c_mx[12*c +: 12], 4'd0};
            c_words[128*c + 16*(k + 1) +: 16] = {c_mn[12*c +: 12], 4'd0};
          end
          c_wt[48*k +: 48] = c_gt;
          c_wt[48*(k + 1) +: 48] = c_gt;
          c_nw = c_nw + 4'd2;
        end
      end
    end
  end
  always @(posedge clk) begin
    t_out <= t_clk; v_out <= restart ? 8'd0 : v;
    if (restart) begin
      cnt <= 28'd0; g <= 48'd0; wn <= 48'd0; gt <= 48'd0; acc <= 160'd0; mx <= 48'd0; mn <= {4{12'hFFF}};
      nw <= 4'd0; wfirst <= 48'd0;
    end else begin
      cnt <= c_cnt; g <= c_g; gt <= c_gt; acc <= c_acc; mx <= c_mx; mn <= c_mn;
      nw <= c_nw; words <= c_words; wt <= c_wt; grp <= c_grp; ph <= c_ph;
      wfirst <= wn; wn <= wn + {44'd0, c_nw};
    end
  end
endmodule
