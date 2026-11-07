// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// trig_core -- the trigger engine of spec 20 §7 on the four channel streams:
// two comparators per channel, the pattern, the 32 event vectors and 9 state
// vectors of §7.3, the interval and Nth-edge engines, the protocol match,
// holdoff/auto/force and the crossing interpolator.
//
// Samples: xs holds channel c's lane i at [96c + 12i +: 12]; vmask marks the
// valid lanes (the same for every channel: 8, 4 or 2 low lanes by route) and
// tpl the ticks per lane (1, 2, 4). The lanes of one clock span 8 ticks; t of
// the clock's lane 0 counts from 0 at the first enabled clock.
// Pipeline: comparators 2, events/engines 2, acceptance 1: a trigger comes
// out 5 clocks after its samples went in; its time is the samples' time.
// Configuration must be stable while en is high (latched while idle).
// TRLC-LINKS: REQ-HD-015
module trig_core #(parameter integer L = 8) (
  input  wire          clk,
  input  wire          rst,
  input  wire          en,
  input  wire [4*L*12-1:0] xs,
  input  wire [L-1:0]  vmask,
  input  wire [2:0]    tpl,
  // comparators: channel c at [48c +: 48] = {b_lo, b_hi, a_lo, a_hi}
  input  wire [4*48-1:0] thresholds,
  input  wire [3:0]    engine,          // 0 off, 1 interval, 2 nth, 4 serial
  input  wire [31:0]   open_mask,
  input  wire [31:0]   close_mask,
  input  wire [31:0]   abort_mask,
  input  wire [8:0]    qual_mask,
  input  wire [8:0]    qual_req,
  input  wire [2:0]    cond,
  input  wire          reach,
  input  wire          always_armed,
  input  wire [47:0]   ta,
  input  wire [47:0]   tb,
  input  wire [15:0]   nth,
  input  wire [3:0]    pat_care,
  input  wire [3:0]    pat_level,
  input  wire          pat_or,
  input  wire          pat_invert,
  input  wire          holdoff_en,
  input  wire [47:0]   holdoff,
  input  wire          auto_en,
  input  wire [47:0]   auto_clocks,
  input  wire          interp_en,
  input  wire [1:0]    interp_ch,
  input  wire          interp_cmp,
  input  wire          interp_falling,
  input  wire          serial_match,    // protocol engine, clock resolution
  input  wire          armed,
  input  wire          force_pulse,
  output wire          trig,
  output wire [47:0]   trig_time,
  output wire          trig_auto,
  output wire          trig_forced,
  output wire [4:0]    frac,
  output wire          frac_valid,
  output wire [31:0]   candidates,
  output wire [31:0]   accepted,
  // the event vectors of §7.3 per lane (lane i at [32i +: 32]), the time of
  // their clock's lane 0, for the counter (§7.9)
  output reg  [32*L-1:0] ev_lanes = {32*L{1'b0}},
  output reg  [47:0]   ev_time = 48'd0
);
  localparam [3:0] ENG_INTERVAL = 4'd1, ENG_NTH = 4'd2, ENG_SERIAL = 4'd4;
  wire run_rst = rst || !en;
  // ---- time of each stage's clock
  reg [47:0] t0 = 0, t1 = 0, t2 = 0, t3 = 0, t4 = 0;
  always @(posedge clk) begin
    t0 <= run_rst ? 48'd0 : t0 + 48'd8;
    t1 <= t0; t2 <= t1; t3 <= t2; t4 <= t3;
  end
  // ---- comparators (S0 -> S2)
  wire [L-1:0] st [0:7], ri [0:7], fa [0:7];   // index 2c = A, 2c+1 = B
  genvar c;
  generate for (c = 0; c < 4; c = c + 1) begin : g_ch
    trig_cmp #(.L(L)) u_a (.clk(clk), .rst(run_rst), .x(xs[96*c +: 96]), .v(vmask),
      .hi(thresholds[48*c +: 12]), .lo(thresholds[48*c+12 +: 12]), .state(st[2*c]), .rise(ri[2*c]), .fall(fa[2*c]));
    trig_cmp #(.L(L)) u_b (.clk(clk), .rst(run_rst), .x(xs[96*c +: 96]), .v(vmask),
      .hi(thresholds[48*c+24 +: 12]), .lo(thresholds[48*c+36 +: 12]), .state(st[2*c+1]), .rise(ri[2*c+1]), .fall(fa[2*c+1]));
  end endgenerate
  reg [L-1:0] v1 = 0, v2 = 0;
  always @(posedge clk) begin v1 <= vmask; v2 <= v1; end
  // ---- pattern and the event/state vectors (S2, combinational)
  reg [L-1:0] pat, pat_rise, pat_fall;
  reg pat_carry = 1'b0;
  reg term, acc, prev;
  integer i, k;
  always @(*) begin
    prev = pat_carry;
    for (i = 0; i < L; i = i + 1) begin
      acc = pat_or ? 1'b0 : 1'b1; term = 1'b0;
      for (k = 0; k < 4; k = k + 1) if (pat_care[k]) begin
        term = st[2*k][i] == pat_level[k];
        acc = pat_or ? (acc | term) : (acc & term);
      end
      if (pat_care == 4'd0) acc = 1'b0;
      pat[i] = v2[i] ? (acc ^ pat_invert) : prev;
      pat_rise[i] = v2[i] && pat[i] && !prev;
      pat_fall[i] = v2[i] && !pat[i] && prev;
      prev = pat[i];
    end
  end
  always @(posedge clk) pat_carry <= run_rst ? 1'b0 : prev;
  // serial match: delayed to the events' stage, so it takes the time of the
  // clock it arrived in, at that clock's last valid lane
  reg ser1 = 1'b0, ser2 = 1'b0;
  always @(posedge clk) begin ser1 <= serial_match && !run_rst; ser2 <= ser1; end
  reg [L-1:0] last_lane;
  always @(*) for (i = 0; i < L; i = i + 1) last_lane[i] = v2[i] && (i == L - 1 || !v2[i+1 < L ? i+1 : i]);
  reg [L-1:0] ev_open, ev_close, ev_abort, qual;
  reg [31:0] ev;
  reg [32*L-1:0] ev_all;
  reg [8:0] sv;
  always @(*) begin
    for (i = 0; i < L; i = i + 1) begin
      for (k = 0; k < 4; k = k + 1) begin
        ev[4*k+0] = ri[2*k][i]; ev[4*k+1] = fa[2*k][i];
        ev[4*k+2] = ri[2*k+1][i]; ev[4*k+3] = fa[2*k+1][i];
        sv[k] = st[2*k][i]; sv[4+k] = st[2*k+1][i];
      end
      ev[16] = pat_rise[i]; ev[17] = pat_fall[i]; ev[18] = ser2 && last_lane[i];
      ev[31:19] = 13'd0;
      sv[8] = pat[i];
      ev_all[32*i +: 32] = v2[i] ? ev : 32'd0;
      ev_open[i] = |(ev & open_mask);
      ev_close[i] = |(ev & close_mask);
      ev_abort[i] = |(ev & abort_mask);
      qual[i] = &(~qual_mask | ~(sv ^ qual_req));
    end
  end
  always @(posedge clk) begin ev_lanes <= run_rst ? {32*L{1'b0}} : ev_all; ev_time <= t2; end
  // ---- engines (S2 -> S3 -> S4)
  wire [L-1:0] iv_fire;
  trig_interval #(.L(L)) u_iv (.clk(clk), .rst(run_rst), .en(engine == ENG_INTERVAL), .open(ev_open), .close(ev_close),
    .cancel(ev_abort), .qual(qual), .v(v2), .tpl(tpl), .cond(cond), .reach(reach), .always_armed(always_armed),
    .ta(ta), .tb(tb), .fire(iv_fire), .judge(), .fresh());
  // Nth edge: the gaps between close events against the idle time tb
  wire [L-1:0] gap_judge, gap_fresh, nth_fire;
  trig_interval #(.L(L)) u_gap (.clk(clk), .rst(run_rst), .en(engine == ENG_NTH), .open(ev_close), .close(ev_close),
    .cancel({L{1'b0}}), .qual({L{1'b1}}), .v(v2), .tpl(tpl), .cond(3'd2), .reach(1'b0), .always_armed(1'b0),
    .ta(48'd0), .tb(tb), .fire(), .judge(gap_judge), .fresh(gap_fresh));
  reg [L-1:0] close3 = 0;
  always @(posedge clk) close3 <= ev_close;
  trig_nth #(.L(L)) u_nth (.clk(clk), .rst(run_rst), .en(engine == ENG_NTH), .edges(close3),
    .restart(gap_judge | gap_fresh), .n(nth), .fire(nth_fire));
  reg [L-1:0] iv4 = 0, ser3 = 0, ser4 = 0;
  always @(posedge clk) begin
    iv4 <= iv_fire;
    ser3 <= (engine == ENG_SERIAL) ? ({L{ser2}} & last_lane) : {L{1'b0}};
    ser4 <= ser3;
  end
  wire [L-1:0] cand = (engine == ENG_INTERVAL) ? iv4 : (engine == ENG_NTH) ? nth_fire :
                      (engine == ENG_SERIAL) ? ser4 : {L{1'b0}};
  // ---- acceptance (S4 -> S5)
  wire [2:0] lane;
  trig_accept #(.L(L)) u_acc (.clk(clk), .rst(run_rst), .armed(armed && en), .cand(cand), .tpl(tpl), .t_clk(t4),
    .holdoff_en(holdoff_en), .holdoff(holdoff), .auto_en(auto_en), .auto_clocks(auto_clocks),
    .force_pulse(force_pulse), .trig(trig), .trig_time(trig_time), .trig_lane(lane), .trig_auto(trig_auto),
    .trig_forced(trig_forced), .candidates(candidates), .accepted(accepted));
  // ---- interpolation: the interp channel's samples delayed to S5, and the
  // last valid sample of the clock before
  wire [L*12-1:0] xi = xs[96*interp_ch +: 96];
  reg [L*12-1:0] xd1 = 0, xd2 = 0, xd3 = 0, xd4 = 0, xd5 = 0, xd6 = 0;
  reg [L-1:0] vd3 = 0, vd4 = 0, vd5 = 0;
  always @(posedge clk) begin
    xd1 <= xi; xd2 <= xd1; xd3 <= xd2; xd4 <= xd3; xd5 <= xd4; xd6 <= xd5;
    vd3 <= v2; vd4 <= vd3; vd5 <= vd4;
  end
  reg [11:0] s_prev, s_cur;
  integer nv;
  always @(*) begin
    nv = 0;
    for (i = 0; i < L; i = i + 1) if (vd5[i]) nv = i;
    s_cur = xd5[12*lane +: 12];
    s_prev = (lane == 0) ? xd6[12*nv +: 12] : xd5[12*(lane - 1) +: 12];
  end
  wire [47:0] thr = thresholds[48*interp_ch +: 48];
  wire [11:0] level = interp_cmp ? (interp_falling ? thr[47:36] : thr[35:24]) : (interp_falling ? thr[23:12] : thr[11:0]);
  trig_interp u_interp (.clk(clk), .start(trig && !trig_auto && !trig_forced && interp_en), .falling(interp_falling),
    .x0(s_prev), .x1(s_cur), .level(level), .frac(frac), .valid(frac_valid));
endmodule
