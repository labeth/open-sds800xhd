// ENGMODEL-OWNER-UNIT: FU-HD-PL-TESTS
`timescale 1ns/1ps
// Self-checking: proto_core from sampled analog channels (route 1: 4 lanes a
// clock, noisy codes about 1000/3000, slicer band 1900..2100) through the
// line slicers into the UART, SPI and I2C engines: decoded bytes, matches,
// misses and the counters.
// TRLC-LINKS: REQ-HD-015
module tb_proto_core;
  localparam integer L = 8;
  reg clk = 0; always #2 clk = ~clk;
  reg rst = 1, en = 0;
  reg [3:0] engine = 1; reg [1:0] a_ch = 0, b_ch = 1;
  reg [23:0] timing = 40; reg cpol = 0, cpha = 0, msb = 1; reg [6:0] addr = 7'h50; reg any = 0; reg [1:0] dir = 0;
  reg [31:0] pattern = 32'h4869; reg [2:0] plen = 2;
  reg [3:0] line = 4'b1111;           // logic level of each channel
  reg [4*L*12-1:0] xs;
  integer seed = 3, i, c;
  always @(negedge clk) for (c = 0; c < 4; c = c + 1) for (i = 0; i < L; i = i + 1)
    xs[96*c + 12*i +: 12] = (i < 4) ? ((line[c] ? 3000 : 1000) + ($random(seed) % 30)) : 12'hFFF;
  wire match, ev; wire [7:0] kind, data; wire [31:0] nm, ne, nerr;
  proto_core #(.L(L)) dut (.clk(clk), .rst(rst), .en(en), .xs(xs), .vmask(8'h0F), .engine(engine), .a_ch(a_ch),
    .b_ch(b_ch), .inv_a(1'b0), .inv_b(1'b0), .slice_a({12'd1900, 12'd2100}), .slice_b({12'd1900, 12'd2100}),
    .timing(timing), .cpol(cpol), .cpha(cpha), .msb(msb), .address(addr), .any_address(any), .direction(dir),
    .pattern(pattern), .pattern_len(plen), .match(match), .event_valid(ev), .event_kind(kind), .event_data(data),
    .match_count(nm), .event_count(ne), .error_count(nerr));
  integer hits = 0, nbytes = 0, errors = 0; reg [7:0] got [0:15];
  always @(posedge clk) begin
    if (match) hits = hits + 1;
    if (ev && kind == 2) begin got[nbytes % 16] = data; nbytes = nbytes + 1; end
  end
  task hold(input integer n); repeat (n) @(negedge clk); endtask
  task restart(input [3:0] e); begin
    @(negedge clk) en = 0; rst = 1; engine = e; hold(3); rst = 0; en = 1; hold(4 * timing);
    hits = 0; nbytes = 0;
  end endtask
  task uart(input [7:0] b); integer k; begin
    line[a_ch] = 0; hold(timing);
    for (k = 0; k < 8; k = k + 1) begin line[a_ch] = b[k]; hold(timing); end
    line[a_ch] = 1; hold(timing);
  end endtask
  task spi(input [7:0] b); integer k; begin
    for (k = 0; k < 8; k = k + 1) begin line[b_ch] = b[7-k]; hold(10); line[a_ch] = 1; hold(10); line[a_ch] = 0; end
  end endtask
  task i2c_start; begin line[1] = 1; line[0] = 1; hold(10); line[1] = 0; hold(10); line[0] = 0; hold(5); end endtask
  task i2c_stop; begin line[1] = 0; hold(5); line[0] = 1; hold(10); line[1] = 1; hold(10); end endtask
  task i2c_byte(input [7:0] b); integer k; begin
    for (k = 0; k < 9; k = k + 1) begin
      line[1] = (k < 8) ? b[7-k] : 1'b0;   // ninth bit: the receiver's ACK
      hold(5); line[0] = 1; hold(10); line[0] = 0; hold(5);
    end
  end endtask
  task check(input integer want_hits, input integer want_bytes, input [8*20-1:0] what); begin
    if (hits != want_hits || nbytes != want_bytes) begin
      errors = errors + 1; $display("FAIL %0s: %0d matches (want %0d), %0d bytes (want %0d)", what, hits, want_hits, nbytes, want_bytes);
    end
  end endtask
  initial begin
    // UART "Hi" on C1
    timing = 40; pattern = 32'h4869; plen = 2; a_ch = 0; line = 4'b1111;
    restart(1); uart(8'h48); uart(8'h69); hold(200); check(1, 2, "uart Hi");
    if (got[0] != 8'h48 || got[1] != 8'h69) begin errors = errors + 1; $display("FAIL uart bytes %h %h", got[0], got[1]); end
    uart(8'h48); uart(8'h68); hold(200); check(1, 4, "uart miss");
    // SPI on C2 (clock) / C3 (data), mode 0, MSB first, gap 30 clocks
    a_ch = 1; b_ch = 2; timing = 30; pattern = 32'h48A5; plen = 2; line = 4'b0000;
    restart(2); spi(8'h48); spi(8'hA5); hold(100); check(1, 2, "spi 48 A5");
    spi(8'h48); hold(100); spi(8'hA5); hold(100); check(1, 4, "spi gap isolates");
    // I2C on C1 (SCL) / C2 (SDA): write to 0x50, data 12 34
    a_ch = 0; b_ch = 1; timing = 30; addr = 7'h50; dir = 0; pattern = 32'h1234; plen = 2; line = 4'b0011;
    restart(3); i2c_start; i2c_byte(8'hA0); i2c_byte(8'h12); i2c_byte(8'h34); i2c_stop; hold(50);
    check(1, 3, "i2c 50 w 12 34");
    i2c_start; i2c_byte(8'hA2); i2c_byte(8'h12); i2c_byte(8'h34); i2c_stop; hold(50);
    check(1, 6, "i2c other address");
    if (nm != 1 || ne < 6) begin errors = errors + 1; $display("FAIL counters %0d %0d", nm, ne); end
    if (errors == 0) $display("PASS UART, SPI and I2C from sampled channels through the slicers: bytes, matches, misses, counters");
    $finish;
  end
  initial begin #20000000; $display("FAIL timeout"); $finish; end
endmodule
