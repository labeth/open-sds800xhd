// ENGMODEL-OWNER-UNIT: FU-HD-PL-TESTS
`timescale 1ns/1ps
// Self-checking: trig_core against a sequential reference model. Random
// noisy multi-level waveforms on four channels (fast edges, narrow pulses
// several to a clock, runts, long gaps) are sampled at every route (8, 4 and
// 2 lanes a clock); each of the vendor trigger types of spec 20 §7.4 is set up
// as the board would and the times of every accepted trigger must equal the
// model's, sample for sample. Directed checks follow: holdoff, auto, force,
// the crossing interpolation and the protocol match path.
// TRLC-LINKS: REQ-HD-015
module tb_trig_core;
  localparam integer L = 8;
  reg clk = 0; always #2 clk = ~clk;
  reg rst = 1, en = 0;
  reg [4*L*12-1:0] xs = 0; reg [L-1:0] vmask = 8'hFF; reg [2:0] tpl = 1;
  reg [4*48-1:0] thr;
  reg [3:0] engine = 1; reg [31:0] om = 0, cm = 0, am = 0; reg [8:0] qm = 0, qr = 0;
  reg [2:0] cond = 0; reg reach = 0, always_armed = 0; reg [47:0] ta = 0, tb = 0, hold = 0, autoc = 0;
  reg [15:0] nth = 1; reg [3:0] pcare = 0, plevel = 0; reg por = 0, pinv = 0;
  reg hold_en = 0, auto_en = 0, interp_en = 0, ifall = 0, icmp = 0; reg [1:0] ich = 0;
  reg serial = 0, armed = 1, frc = 0;
  wire trig, tauto, tforced, fvalid; wire [47:0] ttime; wire [4:0] frac; wire [31:0] ncand, nacc;
  trig_core #(.L(L)) dut (.clk(clk), .rst(rst), .en(en), .xs(xs), .vmask(vmask), .tpl(tpl), .thresholds(thr),
    .engine(engine), .open_mask(om), .close_mask(cm), .abort_mask(am), .qual_mask(qm), .qual_req(qr), .cond(cond),
    .reach(reach), .always_armed(always_armed), .ta(ta), .tb(tb), .nth(nth), .pat_care(pcare), .pat_level(plevel),
    .pat_or(por), .pat_invert(pinv), .holdoff_en(hold_en), .holdoff(hold), .auto_en(auto_en), .auto_clocks(autoc),
    .interp_en(interp_en), .interp_ch(ich), .interp_cmp(icmp), .interp_falling(ifall), .serial_match(serial),
    .armed(armed), .force_pulse(frc), .trig(trig), .trig_time(ttime), .trig_auto(tauto), .trig_forced(tforced),
    .frac(frac), .frac_valid(fvalid), .candidates(ncand), .accepted(nacc));
  integer errors = 0;
  // ---------------------------------------------------------------- waveforms
  integer seed = 1;
  integer seg_end [0:3], seg_start [0:3], lvl_from [0:3], lvl_to [0:3], rise_t [0:3];
  task wave_reset; integer c; begin
    for (c = 0; c < 4; c = c + 1) begin seg_end[c] = 0; seg_start[c] = 0; lvl_from[c] = 1000; lvl_to[c] = 1000; rise_t[c] = 1; end
  end endtask
  function integer urand(input integer n); urand = ($random(seed) & 32'h7FFFFFFF) % n; endfunction
  task wave_next(input integer c); integer r; begin
    seg_start[c] = seg_end[c];
    r = urand(100);
    // durations: mostly short (several edges a clock at 2 GSa/s), some long
    seg_end[c] = seg_start[c] + ((r < 40) ? 1 + urand(12) : (r < 85) ? 10 + urand(120) : 200 + urand(1500));
    lvl_from[c] = lvl_to[c];
    r = urand(100);
    lvl_to[c] = (r < 42) ? 3000 : (r < 84) ? 1000 : (r < 92) ? 2250 : 2750;  // highs, lows, runts, mid-highs
    rise_t[c] = 1 + urand(6);
  end endtask
  function [11:0] wave_at(input integer c, input integer t); integer v, dt; begin
    dt = t - seg_start[c];
    v = (dt >= rise_t[c]) ? lvl_to[c] : lvl_from[c] + (lvl_to[c] - lvl_from[c]) * dt / rise_t[c];
    v = v + urand(41) - 20;
    wave_at = v < 0 ? 0 : v > 4095 ? 4095 : v;
  end endfunction
  // ---------------------------------------------------------------- reference model
  reg [7:0] ms;                   // Schmitt states, index 2c (A) / 2c+1 (B)
  reg mpat; reg m_armed, m_fired, m_have_prev, m_have_acc; integer m_open_t, m_prev_t, m_cnt;
  reg [63:0] m_next_ok;
  integer exp_t [0:4095], got_t [0:4095]; integer n_exp = 0, n_got = 0;
  task model_reset; begin
    ms = 8'd0; mpat = 0; m_armed = 0; m_fired = 0; m_have_prev = 0; m_have_acc = 0; m_cnt = 0; m_next_ok = 0;
  end endtask
  function mok(input integer w); begin
    case (cond)
      0: mok = 1; 1: mok = w < ta; 2: mok = w > tb; 3: mok = w > ta && w < tb; 4: mok = w < ta || w > tb; default: mok = 0;
    endcase
  end endfunction
  // one sample set of all channels at time t: returns 1 if a candidate fires
  function model_sample(input integer t, input [47:0] x0, input [47:0] x1, input [47:0] x2, input [47:0] x3);
    reg [31:0] ev; reg [8:0] sv; reg [7:0] ns; reg np, term, acc, op, cl, ab, q, cand; integer c, w; reg [11:0] x;
    begin
      ns = ms;
      for (c = 0; c < 4; c = c + 1) begin
        x = c == 0 ? x0[11:0] : c == 1 ? x1[11:0] : c == 2 ? x2[11:0] : x3[11:0];
        if (x >= thr[48*c +: 12]) ns[2*c] = 1; else if (x <= thr[48*c+12 +: 12]) ns[2*c] = 0;
        if (x >= thr[48*c+24 +: 12]) ns[2*c+1] = 1; else if (x <= thr[48*c+36 +: 12]) ns[2*c+1] = 0;
      end
      for (c = 0; c < 4; c = c + 1) begin
        ev[4*c] = !ms[2*c] && ns[2*c]; ev[4*c+1] = ms[2*c] && !ns[2*c];
        ev[4*c+2] = !ms[2*c+1] && ns[2*c+1]; ev[4*c+3] = ms[2*c+1] && !ns[2*c+1];
        sv[c] = ns[2*c]; sv[4+c] = ns[2*c+1];
      end
      if (pcare == 0) np = 0;
      else begin
        acc = !por;
        for (c = 0; c < 4; c = c + 1) if (pcare[c]) begin term = ns[2*c] == plevel[c]; acc = por ? acc | term : acc & term; end
        np = acc ^ pinv;
      end
      ev[16] = np && !mpat; ev[17] = !np && mpat; ev[31:18] = 0;
      sv[8] = np;
      ms = ns; mpat = np;
      op = |(ev & om); cl = |(ev & cm); ab = |(ev & am);
      q = &(~qm | ~(sv ^ qr));
      cand = 0;
      if (engine == 1) begin
        if (ab) m_armed = 0;
        w = t - m_open_t;
        if (reach) begin
          if (m_armed && !m_fired && w > tb) begin cand = 1; m_fired = 1; end
        end else if (cl && q && (always_armed || (m_armed && mok(w)))) cand = 1;
        if (cl && q) m_armed = 0;
        if (op) begin m_armed = 1; m_open_t = t; m_fired = 0; end
      end else if (engine == 2 && cl) begin
        if (!m_have_prev || t - m_prev_t > tb) m_cnt = 1;
        else if (m_cnt <= nth) m_cnt = m_cnt + 1;
        if (m_cnt == nth) cand = 1;
        m_have_prev = 1; m_prev_t = t;
      end
      model_sample = cand;
    end
  endfunction
  // ---------------------------------------------------------------- drive
  integer n_clk = 0, nv;
  task drive_clock; integer i, c, t; reg cand, taken; reg [47:0] xl [0:3]; begin
    nv = 8 / tpl; taken = 0;
    for (i = 0; i < L; i = i + 1) begin
      if (i < nv) begin
        t = 8 * n_clk + i * tpl;
        for (c = 0; c < 4; c = c + 1) begin
          while (t >= seg_end[c]) wave_next(c);
          xs[96*c + 12*i +: 12] = wave_at(c, t);
          xl[c] = xs[96*c + 12*i +: 12];
        end
        cand = model_sample(t, xl[0], xl[1], xl[2], xl[3]);
        if (cand && !taken && armed && (!hold_en || !m_have_acc || t >= m_next_ok)) begin
          taken = 1; m_have_acc = 1; m_next_ok = t + hold;
          if (n_exp < 4096) exp_t[n_exp] = t; n_exp = n_exp + 1;
        end
      end else for (c = 0; c < 4; c = c + 1) xs[96*c + 12*i +: 12] = 12'hABC;  // invalid lanes carry junk
    end
    n_clk = n_clk + 1;
  end endtask
  integer t_limit = 0;
  always @(posedge clk) if (trig && en && ttime < t_limit) begin
    if (n_got < 4096) got_t[n_got] = ttime; n_got = n_got + 1;
  end
  // ---------------------------------------------------------------- scenarios
  integer total_trigs = 0, sc = 0;
  task run(input [8*28-1:0] name, input integer clocks); integer k, m, bad; begin
    @(negedge clk); en = 0; rst = 1;
    @(negedge clk); @(negedge clk);
    vmask = (tpl == 1) ? 8'hFF : (tpl == 2) ? 8'h0F : 8'h03;
    n_clk = 0; n_exp = 0; n_got = 0; wave_reset; model_reset; t_limit = 8 * clocks;
    rst = 0; en = 1;
    for (k = 0; k < clocks; k = k + 1) begin drive_clock; @(negedge clk); end
    repeat (8) @(negedge clk);     // pipeline drain: triggers of the last clocks
    bad = (n_exp != n_got);
    m = n_exp < n_got ? n_exp : n_got;
    for (k = 0; k < m && k < 4096; k = k + 1) if (exp_t[k] != got_t[k]) begin
      if (!bad) $display("FAIL %0s tpl %0d: trigger %0d at %0d, model %0d", name, tpl, k, got_t[k], exp_t[k]);
      bad = 1;
    end
    if (n_exp == 0) begin $display("FAIL %0s tpl %0d: no trigger in the model (scenario too weak)", name, tpl); bad = 1; end
    if (bad) begin errors = errors + 1; $display("FAIL %0s tpl %0d: %0d triggers, model %0d", name, tpl, n_got, n_exp); end
    total_trigs = total_trigs + n_got; sc = sc + 1;
  end endtask
  // event bit helpers: channel c, comparator A/B
  function [31:0] AR(input integer c); AR = 32'd1 << (4*c); endfunction
  function [31:0] AF(input integer c); AF = 32'd1 << (4*c+1); endfunction
  function [31:0] BR(input integer c); BR = 32'd1 << (4*c+2); endfunction
  function [31:0] BF(input integer c); BF = 32'd1 << (4*c+3); endfunction
  task defaults; begin
    engine = 1; om = 0; cm = 0; am = 0; qm = 0; qr = 0; cond = 0; reach = 0; always_armed = 0;
    ta = 0; tb = 0; nth = 1; pcare = 0; plevel = 0; por = 0; pinv = 0; hold_en = 0; hold = 0; auto_en = 0;
  end endtask
  integer r, rr; integer cyc = 2500;
  initial begin
    // A: band 1900..2100 around 2000; B: band 2400..2600 around 2500 (runts reach 2250)
    for (r = 0; r < 4; r = r + 1) thr[48*r +: 48] = {12'd2400, 12'd2600, 12'd1900, 12'd2100};
    for (rr = 0; rr < 3; rr = rr + 1) begin
      tpl = (rr == 0) ? 1 : (rr == 1) ? 2 : 4;
      defaults; cm = AR(0); always_armed = 1;                          run("edge rising", cyc);
      defaults; cm = AF(1); always_armed = 1;                          run("edge falling", cyc);
      defaults; cm = AR(2) | AF(2); always_armed = 1;                  run("edge either", cyc);
      defaults; cm = AF(0) | BR(0); always_armed = 1;                  run("window exit", cyc);
      defaults; om = AR(0); cm = AF(0); cond = 1; ta = 20;             run("pulse+ less", cyc);
      defaults; om = AF(0); cm = AR(0); cond = 2; tb = 60;             run("pulse- greater", cyc);
      defaults; om = AR(1); cm = AF(1); cond = 3; ta = 6; tb = 40;     run("pulse+ inside", cyc);
      defaults; om = AR(1); cm = AF(1); cond = 4; ta = 6; tb = 40;     run("pulse+ outside", cyc);
      defaults; om = AR(0); cm = AR(0); cond = 3; ta = 30; tb = 200;   run("interval rising", cyc);
      defaults; om = AR(3) | AF(3); cm = AR(3) | AF(3); cond = 1; ta = 16; run("interval either", cyc);
      defaults; om = AR(0); cm = BR(0); am = AF(0); cond = 1; ta = 5;  run("slope rising less", cyc);
      defaults; om = BF(0); cm = AF(0); am = BR(0); cond = 2; tb = 3;  run("slope falling greater", cyc);
      defaults; om = AR(1); cm = AF(1); am = BR(1); cond = 0;          run("runt positive", cyc);
      defaults; om = BF(1); cm = BR(1); am = AF(1); cond = 1; ta = 300; run("runt negative less", cyc);
      defaults; om = AR(2) | AF(2); cond = 2; reach = 1; tb = 400;     run("dropout edge", cyc);
      defaults; om = AR(2); am = AF(2); cond = 2; reach = 1; tb = 300; run("dropout state", cyc);
      defaults; pcare = 4'b0011; plevel = 4'b0001; cm = 32'd1 << 16; always_armed = 1; run("pattern and", cyc);
      defaults; pcare = 4'b0111; plevel = 4'b0000; por = 1; pinv = 1; om = 32'd1 << 16; cm = 32'd1 << 17;
               cond = 2; tb = 50; run("pattern nor longer", cyc);
      defaults; cm = AR(0); qm = 9'b0_0000_0010; qr = 9'b0_0000_0010; always_armed = 1; run("qualified state", cyc);
      defaults; om = AR(1); am = AF(1); cm = AR(0); cond = 2; tb = 80; run("qualified state-delay", cyc);
      defaults; om = AR(1) | AF(1); cm = AR(0); cond = 1; ta = 48;    run("setup violation", cyc);
      defaults; om = AR(0); cm = AR(1) | AF(1); cond = 1; ta = 24;    run("hold violation", cyc);
      defaults; pcare = 4'b0110; plevel = 4'b0110; om = 32'd1 << 16; cm = BF(0); cond = 3; ta = 4; tb = 500; run("delay", cyc);
      defaults; engine = 2; cm = AR(0); nth = 3; tb = 300;             run("nth edge", cyc);
      defaults; engine = 2; cm = AR(3) | AF(3); nth = 1; tb = 150;     run("nth edge either", cyc);
      defaults; cm = AR(0) | AF(0); always_armed = 1; hold_en = 1; hold = 333; run("edge with holdoff", cyc);
    end
    directed;
    if (errors == 0) $display("PASS %0d scenarios at 3 routes, %0d triggers equal to the model; holdoff, auto, force, interpolation, serial", sc, total_trigs);
    else $display("FAIL %0d errors", errors);
    $finish;
  end
  // ---------------------------------------------------------------- directed checks
  integer t0, nf, ramp; reg seen;
  task directed; integer k; begin
    // auto: no events at all (flat), auto every autoc clocks
    tpl = 1; vmask = 8'hFF; defaults; cm = AR(0); always_armed = 1; auto_en = 1; autoc = 50;
    @(negedge clk); en = 0; rst = 1; @(negedge clk); rst = 0; en = 1; xs = {32{12'd1000}};
    n_got = 0; nf = 0;
    for (k = 0; k < 400; k = k + 1) begin @(negedge clk); if (trig) begin nf = nf + 1; if (!tauto) begin errors = errors + 1; $display("FAIL auto flag"); end end end
    if (nf < 6 || nf > 8) begin errors = errors + 1; $display("FAIL auto triggers %0d in 400 clocks at 50", nf); end
    // not armed: nothing, a force waits for armed
    auto_en = 0; armed = 0; @(negedge clk) frc = 1; @(negedge clk) frc = 0;
    nf = 0; for (k = 0; k < 20; k = k + 1) begin @(negedge clk); if (trig) nf = nf + 1; end
    if (nf != 0) begin errors = errors + 1; $display("FAIL trigger while not armed"); end
    armed = 1; seen = 0; for (k = 0; k < 10; k = k + 1) begin @(negedge clk); if (trig && tforced) seen = 1; end
    if (!seen) begin errors = errors + 1; $display("FAIL force not taken once armed"); end
    // interpolation: a ramp of 100 codes a sample through level 2100: 2000 -> 2100 gives frac 16 * 100/100 -> 15;
    // shift so the samples are 2050, 2150: frac = 16 * 50 / 100 = 8
    defaults; cm = AR(0); always_armed = 1; interp_en = 1; ich = 0; icmp = 0; ifall = 0;
    @(negedge clk); en = 0; rst = 1; @(negedge clk); rst = 0; en = 1;
    ramp = 1250;
    seen = 0;
    for (k = 0; k < 40; k = k + 1) begin
      for (r = 0; r < 8; r = r + 1) begin xs[12*r +: 12] = (ramp > 4000) ? 4000 : ramp; ramp = ramp + 100; end
      @(negedge clk); if (fvalid) begin seen = 1; if (frac != 5'd8) begin errors = errors + 1; $display("FAIL frac %0d want 8", frac); end end end
    if (!seen) begin errors = errors + 1; $display("FAIL no interpolation result"); end
    // serial: a match pulse becomes a trigger at the clock's last lane
    defaults; engine = 4; tpl = 2; vmask = 8'h0F;
    @(negedge clk); en = 0; rst = 1; @(negedge clk); rst = 0; en = 1; xs = {32{12'd1000}};
    repeat (10) @(negedge clk); serial = 1; t0 = 8 * 10; @(negedge clk); serial = 0;
    seen = 0; for (k = 0; k < 10; k = k + 1) begin @(negedge clk); if (trig) begin seen = 1;
      if (ttime != t0 + 6) begin errors = errors + 1; $display("FAIL serial trigger at %0d want %0d", ttime, t0 + 6); end end end
    if (!seen) begin errors = errors + 1; $display("FAIL serial match did not trigger"); end
  end endtask
  initial begin #50000000; $display("FAIL timeout"); $finish; end
endmodule
