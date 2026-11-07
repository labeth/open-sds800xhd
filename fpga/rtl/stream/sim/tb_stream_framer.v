// ENGMODEL-OWNER-UNIT: FU-HD-PL-TESTS
`timescale 1ns/1ps
// Self-checking: stream_framer with random item arrival, random write-side
// stalls and a consumer that lags, so blocks are dropped both ways (both
// buffers busy; ring full). Every consumed block must be whole and in place:
// its items are exactly the input items seq*beats ... seq*beats+beats-1, its
// time that of its first item, its lost flag set exactly when the sequence
// jumped, and lost counts the items of every jump.
// TRLC-LINKS: REQ-HD-015
module tb_stream_framer;
  reg clk = 0; always #2 clk = ~clk;
  reg rst = 1, en = 0;
  localparam integer BEATS = 13, BLOCKS = 5;
  localparam [31:0] BASE = 32'h3C00_0000;
  reg [31:0] tail = 0;
  reg in_valid = 0; reg [63:0] item = 0;
  wire ov, ol; wire [63:0] od; wire [31:0] oa, head, seq, lost; wire ovf;
  reg ready = 0;
  stream_framer #(.AW(4)) dut (.clk(clk), .rst(rst), .en(en), .payload(2'd1), .mask(4'b0101), .beats(5'd13),
    .blocks(BLOCKS), .base(BASE), .tail(tail), .in_valid(in_valid), .in_item(item), .in_time(item[47:0] * 3),
    .out_valid(ov), .out_ready(ready), .out_data(od), .out_addr(oa), .out_last(ol), .head(head), .seq(seq),
    .lost(lost), .overflow(ovf));
  reg [63:0] ring [0:BLOCKS*(BEATS+2)-1];
  integer errors = 0, seed = 11, consumed = 0, jumps = 0; integer prev_seq = -1;
  integer phase = 0;   // 0 fast consumer, 1 slow consumer, 2 stalled writer, 3 drain
  always @(posedge clk) begin
    if (ov && ready) begin
      if (oa < BASE || oa >= BASE + BLOCKS*(BEATS+2)*8 || oa[2:0]) begin errors = errors + 1; $display("FAIL address %h", oa); end
      else ring[(oa - BASE) / 8] = od;
    end
  end
  always @(negedge clk) begin
    ready <= (phase == 2) ? (($random(seed) & 15) == 0) : (($random(seed) & 3) != 0);
    if (in_valid) item <= item + 1;
    in_valid <= en && !rst && (phase < 3) && (($random(seed) & 7) != 0);
  end
  // the consumer
  integer s, j, sl, b, sq;
  reg [63:0] h0, h1;
  always @(negedge clk) if (en && head != tail && (phase == 0 || phase == 3 || ($random(seed) & 63) == 0)) begin
    sl = tail % BLOCKS; b = sl * (BEATS + 2);
    h0 = ring[b]; h1 = ring[b + 1]; sq = h0[63:32];
    if (h0[15:0] != 16'h5354 || h0[23:16] != 1 || h0[31:24] != 8'h05) begin errors = errors + 1; $display("FAIL header %h", h0); end
    if (sq <= prev_seq) begin errors = errors + 1; $display("FAIL seq %0d after %0d", sq, prev_seq); end
    if (h1[48] != (sq != prev_seq + 1)) begin errors = errors + 1; $display("FAIL lost flag %0d at seq %0d after %0d", h1[48], sq, prev_seq); end
    if (sq != prev_seq + 1) jumps = jumps + (sq - prev_seq - 1);
    if (h1[47:0] != sq * BEATS * 3) begin errors = errors + 1; $display("FAIL time %0d seq %0d", h1[47:0], sq); end
    for (j = 0; j < BEATS; j = j + 1) if (ring[b + 2 + j] != sq * BEATS + j) begin
      errors = errors + 1; $display("FAIL seq %0d item %0d = %0d", sq, j, ring[b + 2 + j]); end
    prev_seq = sq; consumed = consumed + 1;
    tail <= tail + 1;
  end
  initial begin
    repeat (3) @(negedge clk); rst = 0; en = 1;
    phase = 0; repeat (3000) @(negedge clk);
    phase = 1; repeat (6000) @(negedge clk);
    phase = 2; repeat (3000) @(negedge clk);
    phase = 0; repeat (2000) @(negedge clk);
    phase = 3; repeat (3000) @(negedge clk);
    if (head != tail) begin errors = errors + 1; $display("FAIL not drained: head %0d tail %0d", head, tail); end
    if (lost != (seq - consumed) * BEATS) begin errors = errors + 1; $display("FAIL lost %0d, seq %0d consumed %0d", lost, seq, consumed); end
    if (jumps != seq - consumed - (seq - 1 - prev_seq)) begin errors = errors + 1; $display("FAIL jumps %0d", jumps); end
    if (consumed < 100 || jumps < 10 || !ovf) begin errors = errors + 1; $display("FAIL weak run: %0d blocks, %0d dropped", consumed, jumps); end
    if (errors == 0) $display("PASS %0d blocks whole and in place, %0d dropped whole and flagged, lost = %0d items", consumed, jumps, lost);
    $finish;
  end
endmodule
