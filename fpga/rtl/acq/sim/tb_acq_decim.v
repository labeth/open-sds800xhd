// ENGMODEL-OWNER-UNIT: FU-HD-PL-TESTS
`timescale 1ns/1ps
// Self-checking: acq_decim against a sequential model, every route (8, 4, 2
// valid lanes), D = 1, 2, 3, 4, 7, 8, 16, 33 and the three modes (box at a
// non-power-of-two D picks; peak at D = 1 picks). Checks every word in order
// and, per lane, the group index and position the acquisition places
// triggers by.
// TRLC-LINKS: REQ-HD-015
module tb_acq_decim;
  reg clk = 0; always #2 clk = ~clk;
  reg restart = 1;
  reg [383:0] xs = 0; reg [7:0] v = 8'hFF; reg [2:0] tpl = 1; reg [47:0] t = 0;
  reg [27:0] d = 1; reg [1:0] mode = 0;
  wire [3:0] nw; wire [511:0] words; wire [47:0] wfirst; wire [383:0] wt, grp; wire [223:0] ph; wire [47:0] t_out;
  wire [7:0] v_out;
  acq_decim dut (.clk(clk), .restart(restart), .xs(xs), .v(v), .tpl(tpl), .t_clk(t), .d(d), .mode(mode), .nw(nw),
    .words(words), .wfirst(wfirst), .wt(wt), .grp(grp), .ph(ph), .t_out(t_out), .v_out(v_out));
  // model state: the expected words of channel 0 and 2 in a queue
  reg [15:0] q0 [0:65535]; reg [15:0] q2 [0:65535];
  integer qw, qr, errors, checked, cnt, g, k, sum0, sum2, mx0, mn0, mx2, mn2, l;
  reg [11:0] s0, s2;
  reg [47:0] lg [0:7]; reg [27:0] lp [0:7];
  function integer eff(input integer m, input integer dd);
    eff = (m == 1 && (dd & (dd - 1)) != 0) ? 0 : (m == 2 && dd < 2) ? 0 : m;
  endfunction
  function integer lg2(input integer dd); integer i; begin lg2 = 0; for (i = 0; i < 28; i = i + 1) if (dd == (1 << i)) lg2 = i; end endfunction
  task model_clock;
    integer m;
    begin
      m = eff(mode, d);
      for (l = 0; l < 8; l = l + 1) if (v[l]) begin
        s0 = xs[12*l +: 12]; s2 = xs[192 + 12*l +: 12];
        if (cnt == 0) begin
          g = g + 1; sum0 = 0; sum2 = 0; mx0 = 0; mn0 = 4095; mx2 = 0; mn2 = 4095;
          if (m == 0) begin q0[qw] = {s0, 4'd0}; q2[qw] = {s2, 4'd0}; qw = qw + 1; end
        end
        lg[l] = g - 1; lp[l] = cnt;
        sum0 = sum0 + s0; sum2 = sum2 + s2;
        if (s0 > mx0) mx0 = s0; if (s0 < mn0) mn0 = s0; if (s2 > mx2) mx2 = s2; if (s2 < mn2) mn2 = s2;
        cnt = cnt + 1;
        if (cnt == d) begin
          cnt = 0;
          if (m == 1) begin q0[qw] = (sum0 * 16) >> lg2(d); q2[qw] = (sum2 * 16) >> lg2(d); qw = qw + 1; end
          if (m == 2) begin
            q0[qw] = {mx0[11:0], 4'd0}; q2[qw] = {mx2[11:0], 4'd0}; qw = qw + 1;
            q0[qw] = {mn0[11:0], 4'd0}; q2[qw] = {mn2[11:0], 4'd0}; qw = qw + 1;
          end
        end
      end
    end
  endtask
  reg [47:0] mg [0:7]; reg [27:0] mp [0:7]; reg [7:0] mv;
  integer i, r, dd, mm, n;
  initial begin
    errors = 0; checked = 0;
    for (r = 0; r < 3; r = r + 1)
      for (dd = 0; dd < 8; dd = dd + 1)
        for (mm = 0; mm < 3; mm = mm + 1) begin
          d = (dd == 0) ? 1 : (dd == 1) ? 2 : (dd == 2) ? 3 : (dd == 3) ? 4 : (dd == 4) ? 7 : (dd == 5) ? 8 : (dd == 6) ? 16 : 33;
          mode = mm; v = (r == 0) ? 8'hFF : (r == 1) ? 8'h0F : 8'h03; tpl = 1 << r;
          restart = 1; @(posedge clk); @(posedge clk); #0.1;
          qw = 0; qr = 0; cnt = 0; g = 0; restart = 0;
          for (n = 0; n < 200; n = n + 1) begin
            for (i = 0; i < 32; i = i + 1) xs[12*i +: 12] = $random;
            model_clock;
            for (i = 0; i < 8; i = i + 1) begin mg[i] = lg[i]; mp[i] = lp[i]; end
            mv = v;
            @(posedge clk); #0.1;
            // the outputs of the clock just taken
            if (wfirst != qr) begin errors = errors + 1; if (errors < 10) $display("FAIL wfirst %0d, model %0d (D %0d mode %0d)", wfirst, qr, d, mode); end
            for (i = 0; i < nw; i = i + 1) begin
              if (words[16*i +: 16] !== q0[qr] || words[256 + 16*i +: 16] !== q2[qr]) begin
                errors = errors + 1;
                if (errors < 10) $display("FAIL word %0d: %h/%h, model %h/%h (D %0d mode %0d route %0d)", qr, words[16*i +: 16],
                  words[256 + 16*i +: 16], q0[qr], q2[qr], d, mode, r);
              end
              qr = qr + 1; checked = checked + 1;
            end
            for (i = 0; i < 8; i = i + 1) if (mv[i] && (grp[48*i +: 48] != mg[i] || ph[28*i +: 28] != mp[i])) begin
              errors = errors + 1;
              if (errors < 10) $display("FAIL lane %0d group %0d pos %0d, model %0d %0d", i, grp[48*i +: 48], ph[28*i +: 28], mg[i], mp[i]);
            end
          end
          if (qr != qw && qr + 2 < qw) begin errors = errors + 1; $display("FAIL %0d words out, model %0d", qr, qw); end
        end
    if (errors == 0) $display("PASS %0d words at 3 routes x 8 decimations x 3 modes equal to the model, groups and positions per lane", checked);
    else $display("FAIL %0d errors", errors);
    $finish;
  end
endmodule
