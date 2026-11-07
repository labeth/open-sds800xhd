// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// trig_interval -- the time-qualifying engine (spec 20 §7.4). Per lane, in
// order: an cancel clears the open interval; a (qualified) close judges the
// interval's width w = t_close - t_open; an open (re)starts the interval.
// Lane i of a clock is at t_clk + i*tpl ticks; a clock is 8 ticks.
//   cond 0 any, 1 w < ta, 2 w > tb, 3 ta < w < tb, 4 w < ta or w > tb.
//   always_armed: a close fires without an open (edges, states, patterns).
//   reach (with cond 2): fire at the first lane where the open interval's
//   age exceeds tb, once per interval, without waiting for a close.
// Outputs per lane: fire (candidates), judge (a close of an open interval
// whose width met cond) and fresh (a close with no interval open) -- the
// Nth-edge engine counts on judge/fresh. Latency: 1 clock.
// Configuration (cond, ta, tb, tpl, flags) must be stable while enabled.
// TRLC-LINKS: REQ-HD-015
module trig_interval #(parameter integer L = 8) (
  input  wire         clk,
  input  wire         rst,
  input  wire         en,
  input  wire [L-1:0] open,
  input  wire [L-1:0] close,
  input  wire [L-1:0] cancel,
  input  wire [L-1:0] qual,
  input  wire [L-1:0] v,             // valid lanes: reach fires only at a sample
  input  wire [2:0]   tpl,           // ticks per lane: 1, 2 or 4
  input  wire [2:0]   cond,
  input  wire         reach,
  input  wire         always_armed,
  input  wire [47:0]  ta,
  input  wire [47:0]  tb,
  output reg  [L-1:0] fire = {L{1'b0}},
  output reg  [L-1:0] judge = {L{1'b0}},
  output reg  [L-1:0] fresh = {L{1'b0}}
);
  localparam [47:0] MAXT = {48{1'b1}};
  // ---- per-lane thresholds against the carried age (registered: config is static)
  // lane i, interval opened before this clock: w = age + i*tpl.
  //   w < ta  <=>  age < ta - i*tpl   (never when ta <= i*tpl)
  //   w > ta  <=>  age > ta - i*tpl   (always when ta < i*tpl)
  reg [47:0] ta_m [0:L-1], tb_m [0:L-1];
  reg [L-1:0] ta_under = 0, tb_under = 0;   // ta (tb) < i*tpl
  reg [L-1:0] ta_eq0 = 0, tb_eq0 = 0;       // ta (tb) == i*tpl
  integer i;
  always @(posedge clk)
    for (i = 0; i < L; i = i + 1) begin
      ta_m[i] <= ta - i * tpl;  tb_m[i] <= tb - i * tpl;
      ta_under[i] <= ta < i * tpl; tb_under[i] <= tb < i * tpl;
      ta_eq0[i] <= ta == i * tpl;  tb_eq0[i] <= tb == i * tpl;
    end
  reg [47:0] age = 48'd0;
  reg armed = 1'b0, fired = 1'b0;
  function ok(input [2:0] c, input lt_a, gt_a, lt_b, gt_b);
    case (c)
      3'd0: ok = 1'b1;
      3'd1: ok = lt_a;
      3'd2: ok = gt_b;
      3'd3: ok = gt_a && lt_b;
      3'd4: ok = lt_a || gt_b;
      default: ok = 1'b0;
    endcase
  endfunction
  reg a, f, has_in, lt_a, gt_a, lt_b, gt_b, cl;
  reg [2:0] j;
  reg [5:0] w_in;
  reg [L-1:0] fire_n, judge_n, fresh_n;
  always @(*) begin
    a = armed; f = fired; has_in = 1'b0; j = 3'd0; w_in = 6'd0;
    fire_n = 0; judge_n = 0; fresh_n = 0;
    for (i = 0; i < L; i = i + 1) begin
      // the width at lane i of the interval open before it
      if (has_in) begin
        w_in = ({3'd0, i[2:0]} - {3'd0, j}) * {3'd0, tpl};
        lt_a = ta > {42'd0, w_in}; gt_a = ta < {42'd0, w_in}; lt_b = tb > {42'd0, w_in}; gt_b = tb < {42'd0, w_in};
      end else begin
        lt_a = !ta_under[i] && !ta_eq0[i] && age < ta_m[i];
        // an interval open before this clock is at least one tick old: when
        // ta == i*tpl, w = age + ta > ta
        gt_a = ta_under[i] || ta_eq0[i] || age > ta_m[i];
        lt_b = !tb_under[i] && !tb_eq0[i] && age < tb_m[i];
        gt_b = tb_under[i] || tb_eq0[i] || age > tb_m[i];
      end
      if (cancel[i]) begin a = 1'b0; has_in = 1'b0; end
      cl = close[i] && qual[i];
      if (reach) begin
        if (v[i] && a && !f && gt_b) begin fire_n[i] = 1'b1; f = 1'b1; end
      end else if (cl && (always_armed || (a && ok(cond, lt_a, gt_a, lt_b, gt_b))))
        fire_n[i] = 1'b1;
      judge_n[i] = cl && a && ok(cond, lt_a, gt_a, lt_b, gt_b);
      fresh_n[i] = cl && !a;
      if (cl) begin a = 1'b0; has_in = 1'b0; end
      if (open[i]) begin a = 1'b1; has_in = 1'b1; j = i[2:0]; f = 1'b0; end
    end
  end
  always @(posedge clk) begin
    if (rst || !en) begin
      armed <= 1'b0; fired <= 1'b0; age <= 48'd0; fire <= 0; judge <= 0; fresh <= 0;
    end else begin
      fire <= fire_n; judge <= judge_n; fresh <= fresh_n;
      armed <= a; fired <= f;
      if (has_in) age <= 48'd8 - j * tpl;
      else if (age > MAXT - 48'd8) age <= MAXT;
      else age <= age + 48'd8;
    end
  end
endmodule
