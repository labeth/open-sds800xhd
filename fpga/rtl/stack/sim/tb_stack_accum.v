// ENGMODEL-OWNER-UNIT: FU-HD-PL-TESTS
`timescale 1ns/1ps
// Self-checking: stack_accum against a reference array. Records of random
// length (some past the last bin), random trigger fractions and phase counts
// 1, 3, 4 and 16, back to back and with gaps, words on every clock or with
// holes; then every bin is read back through the host port and compared.
// TRLC-LINKS: REQ-HD-015
module tb_stack_accum;
  localparam integer AW = 9, NB = 1 << AW;
  reg clk = 0; always #2 clk = ~clk;
  reg rst = 1, clear = 0, start = 0, rs = 0, wv = 0, re = 0;
  reg [15:0] n = 0; reg [4:0] P = 1; reg [3:0] phi = 0; reg [11:0] word = 0; reg [AW-1:0] ra = 0;
  wire busy, running, done, beyond; wire [15:0] recs, rc; wire [31:0] rsum;
  stack_accum #(.AW(AW)) dut (.clk(clk), .rst(rst), .clear(clear), .start(start), .n(n), .phases(P), .rec_start(rs),
    .phi(phi), .word_valid(wv), .word(word), .rec_end(re), .busy(busy), .running(running), .records(recs),
    .done(done), .beyond(beyond), .rd_addr(ra), .rd_sum(rsum), .rd_count(rc));
  reg [31:0] msum [0:NB-1]; reg [15:0] mcnt [0:NB-1];
  integer errors = 0, seed = 5, i, r, k, len, b, pp, dones = 0, round, gaps;
  always @(posedge clk) if (done) dones = dones + 1;
  integer Ps [0:3];
  initial begin
    Ps[0] = 1; Ps[1] = 3; Ps[2] = 4; Ps[3] = 16;
    repeat (3) @(negedge clk); rst = 0;
    for (round = 0; round < 4; round = round + 1) begin
      P = Ps[round];
      @(negedge clk) clear = 1; @(negedge clk) clear = 0;
      while (busy) @(negedge clk);
      for (i = 0; i < NB; i = i + 1) begin msum[i] = 0; mcnt[i] = 0; end
      n = 20; @(negedge clk) start = 1; @(negedge clk) start = 0;
      for (r = 0; r < 20; r = r + 1) begin
        phi = $random(seed); rs = 1; @(negedge clk); rs = 0;
        pp = P - 1 - ((phi * P) >> 4);
        len = (r == 7) ? NB / P + 9 : 20 + ($random(seed) & 31) + (NB / P) / 2;
        for (k = 0; k < len; k = k + 1) begin
          gaps = (round & 1) ? (($random(seed) & 3) == 0) : 0;
          while (gaps) begin @(negedge clk); gaps = 0; end
          word = $random(seed); wv = 1;
          b = k * P + pp;
          if (b < NB) begin msum[b] = msum[b] + word; mcnt[b] = mcnt[b] + 1; end
          @(negedge clk); wv = 0;
        end
        re = 1; @(negedge clk); re = 0;
        if (round >= 2) repeat ($random(seed) & 3) @(negedge clk);
      end
      repeat (5) @(negedge clk);
      if (running || recs != 20) begin errors = errors + 1; $display("FAIL P %0d: running %0d records %0d", P, running, recs); end
      if (!beyond) begin errors = errors + 1; $display("FAIL P %0d: long record not flagged", P); end
      for (i = 0; i < NB; i = i + 1) begin
        ra = i; @(negedge clk); @(negedge clk);
        if (rsum !== msum[i] || rc !== mcnt[i]) begin
          if (errors < 10) $display("FAIL P %0d bin %0d: sum %0d count %0d, model %0d %0d", P, i, rsum, rc, msum[i], mcnt[i]);
          errors = errors + 1;
        end
      end
    end
    if (dones != 4) begin errors = errors + 1; $display("FAIL done pulses %0d", dones); end
    if (errors == 0) $display("PASS 4 x 20 records at P = 1, 3, 4, 16, every bin equal to the model, overflow flagged");
    $finish;
  end
endmodule
