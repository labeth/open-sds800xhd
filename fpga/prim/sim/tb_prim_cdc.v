// ENGMODEL-OWNER-UNIT: FU-HD-PL-TESTS
`timescale 1ns/1ps
// Self-checking: the CDC primitives between unrelated clocks (100 and
// 250 MHz with a drifting phase): every pulse arrives once, bus words arrive
// whole, the async FIFO neither loses nor duplicates a word, the reset
// synchronizer holds for its stages.
// TRLC-LINKS: REQ-HD-015
module tb_prim_cdc;
  reg aclk = 0, cclk = 0;
  always #5.000 aclk = ~aclk;
  always #1.937 cclk = ~cclk;
  integer errors = 0;
  // ---- pulse sync aclk -> cclk
  reg sp = 0; wire busy, dp;
  prim_pulse_sync u_ps (.src_clk(aclk), .src_pulse(sp), .src_busy(busy), .dst_clk(cclk), .dst_pulse(dp));
  integer sent = 0, got = 0;
  always @(posedge cclk) if (dp) got = got + 1;
  // ---- bus sync cclk -> aclk: a counter, sampled; every received word must be one the source held
  reg [31:0] cnt = 0; always @(posedge cclk) cnt <= cnt + 1;
  wire rdy; wire [31:0] bw; wire bv;
  prim_bus_sync #(.W(32)) u_bs (.src_clk(cclk), .src_load(1'b1), .src_data({cnt[15:0], ~cnt[15:0]}),
                                .src_ready(rdy), .dst_clk(aclk), .dst_data(bw), .dst_valid(bv));
  integer words = 0;
  always @(posedge aclk) if (bv) begin
    words = words + 1;
    if (bw[31:16] != ~bw[15:0]) begin errors = errors + 1; $display("FAIL bus word torn %h", bw); end
  end
  // ---- async FIFO cclk -> aclk with bursts and stalls
  reg wen = 0; reg [15:0] wd = 0; wire full; wire [4:0] wl;
  reg ren = 0; wire [15:0] rd; wire rv, empty; wire [4:0] rl;
  prim_async_fifo #(.DW(16), .AW(4)) u_f (.wr_clk(cclk), .wr_rst(1'b0), .wr_en(wen), .wr_data(wd), .wr_full(full),
    .wr_level(wl), .rd_clk(aclk), .rd_rst(1'b0), .rd_en(ren), .rd_data(rd), .rd_valid(rv), .rd_empty(empty), .rd_level(rl));
  reg [15:0] want = 0; integer popped = 0;
  integer seed = 7;
  always @(posedge cclk) begin
    if (wen && !full) wd <= wd + 1;
    wen <= ($random(seed) & 3) != 0;
    if (wl > 16) begin errors = errors + 1; $display("FAIL fifo level"); end
  end
  always @(posedge aclk) begin
    ren <= ($random(seed) & 1);
    if (rv) begin
      if (rd !== want) begin errors = errors + 1; $display("FAIL fifo word %0d expected %0d", rd, want); end
      want <= want + 1; popped = popped + 1;
    end
  end
  // ---- reset sync
  reg rin = 1; wire rout;
  prim_reset_sync #(.STAGES(3)) u_rs (.clk(cclk), .rst_in(rin), .rst_out(rout));
  integer i, held = 0;
  initial begin
    repeat (5) @(posedge cclk);
    @(negedge cclk) rin = 0;
    while (rout) begin @(posedge cclk); #0.01 held = held + 1; end
    if (held < 2 || held > 4) begin errors = errors + 1; $display("FAIL reset held %0d clocks", held); end
    @(negedge cclk) rin = 1; #0.1 if (!rout) begin errors = errors + 1; $display("FAIL reset not asynchronous"); end
    @(negedge cclk) rin = 0;
    for (i = 0; i < 200; i = i + 1) begin
      @(posedge aclk); sp <= 1; sent = sent + 1; @(posedge aclk); sp <= 0;
      #1 while (busy) @(posedge aclk);
      repeat (i % 3) @(posedge aclk);
    end
    repeat (40) @(posedge aclk);
    if (got != sent) begin errors = errors + 1; $display("FAIL pulses sent %0d got %0d", sent, got); end
    if (u_ps.dropped != 0) begin errors = errors + 1; $display("FAIL pulses dropped"); end
    if (words < 100) begin errors = errors + 1; $display("FAIL bus words %0d", words); end
    if (popped < 300) begin errors = errors + 1; $display("FAIL fifo popped %0d", popped); end
    if (errors == 0) $display("PASS pulses %0d, bus words %0d whole, fifo %0d words in order, reset sync", got, words, popped);
    $finish;
  end
endmodule
