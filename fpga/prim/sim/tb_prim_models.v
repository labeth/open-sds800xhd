// ENGMODEL-OWNER-UNIT: FU-HD-PL-TESTS
`timescale 1ns/1ps
// Self-checking: the behavioural models of the vendor-primitive wrappers
// meet their contracts (DNA value and ready, LVDS lane bit order, MMCM lock,
// gating and phase-step handshake).
// TRLC-LINKS: REQ-HD-015
module tb_prim_models;
  integer errors = 0;
  reg clk = 0; always #5 clk = ~clk;
  reg rd = 0; wire [95:0] dna; wire ready;
  prim_dna #(.SIM_DNA(96'hA5A5_0000_1234_5678_9ABC_DEF0)) u_dna (.clk(clk), .read(rd), .dna(dna), .ready(ready));
  // LVDS lane: a 1 Gb/s nbit stream on a 500 MHz DDR clock
  reg bclk = 0; always #1 bclk = ~bclk;
  reg dclk = 0; always @(posedge bclk) dclk <= ~dclk;
  reg pad = 0; reg [31:0] pattern = 32'hC3A5_0F96; integer nbit = 0;
  always @(posedge bclk or negedge bclk) begin #0.5 pad = pattern[nbit % 32]; nbit = nbit + 1; end
  wire [3:0] q;
  prim_lvds_lane u_lane (.pad_p(pad), .pad_n(~pad), .clk(bclk), .clk_b(~bclk), .clkdiv(dclk), .rst(1'b0), .q(q));
  // MMCM model
  reg mrst = 1, step = 0; wire done, cb, cd, lk;
  prim_mmcm #(.LOCK_CYCLES(20)) u_mmcm (.clk_in(bclk), .rst(mrst), .ps_clk(clk), .ps_step(step), .ps_inc(1'b1),
    .ps_done(done), .clk_bit(cb), .clk_div(cd), .locked(lk));
  reg [63:0] seen = 0; integer groups = 0, k, matched = 0;
  always @(posedge dclk) if (nbit > 16) begin
    // reassemble the received bits, oldest first, and look for the pattern
    seen = {q, seen[63:4]};
    groups = groups + 1;
    if (groups > 16) for (k = 0; k < 32; k = k + 1) if (seen[k +: 32] == pattern) matched = 1;
  end
  integer dones = 0;
  always @(posedge clk) if (done) dones = dones + 1;
  initial begin
    repeat (3) @(posedge clk); rd <= 1; @(posedge clk); rd <= 0;
    wait (ready); @(posedge clk);
    if (dna !== 96'hA5A5_0000_1234_5678_9ABC_DEF0) begin errors = errors + 1; $display("FAIL dna %h", dna); end
    if (cb !== 1'b0 || lk) begin errors = errors + 1; $display("FAIL mmcm output not gated in reset"); end
    mrst = 0; #100;
    if (!lk) begin errors = errors + 1; $display("FAIL mmcm did not lock"); end
    @(posedge clk) step <= 1; @(posedge clk) step <= 0; repeat (20) @(posedge clk);
    if (dones != 1) begin errors = errors + 1; $display("FAIL phase step done %0d", dones); end
    #200;
    if (!matched) begin errors = errors + 1; $display("FAIL lane bits out of order"); end
    if (errors == 0) $display("PASS dna, lane bit order, mmcm lock/gating/phase step");
    $finish;
  end
endmodule
