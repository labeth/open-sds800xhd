// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// stack_accum -- phase-binned stacking for super-resolution (spec 20 §8.2).
// Each record arrives as rec_start (with its trigger fraction phi in 1/16
// sample) followed by its words; word i goes to bin i*P + p,
// p = P - 1 - floor(phi*P/16), so records of different trigger phase
// interleave on a grid P times finer than the sample period. Per bin a 32-bit
// sum of the 12-bit codes and a 16-bit count (saturating; a saturated count
// stops that bin's sum too, so sum/count stays the mean). Words past the last
// bin are ignored (and flagged). Read-modify-write in 4 clocks with
// forwarding, so every word may hit the bin just written.
//   clear: zero every bin (NB clocks, busy), then idle.
//   start: count records; done pulses after the n-th record's last word.
//   host port (while no record is being taken): rd_addr -> rd_sum/rd_count
//   two clocks later.
// TRLC-LINKS: REQ-HD-015
module stack_accum #(parameter integer AW = 13) (
  input  wire          clk,
  input  wire          rst,
  input  wire          clear,
  input  wire          start,
  input  wire [15:0]   n,
  input  wire [4:0]    phases,        // P, 1..16
  input  wire          rec_start,
  input  wire [3:0]    phi,
  input  wire          word_valid,
  input  wire [11:0]   word,
  input  wire          rec_end,
  output reg           busy = 1'b0,   // clearing
  output reg           running = 1'b0,
  output reg  [15:0]   records = 16'd0,
  output reg           done = 1'b0,
  output reg           beyond = 1'b0,
  input  wire [AW-1:0] rd_addr,
  output wire [31:0]   rd_sum,
  output wire [15:0]   rd_count
);
  localparam integer NB = 1 << AW;
  reg [47:0] mem [0:NB-1];            // {count, sum}
  // ---- bin index of the next word
  reg [AW+4:0] bin = 0;               // wide enough to see "past the end"
  wire [8:0] phi_p = phi * phases;    // < 16 * 16
  wire [4:0] p_of = phases - 5'd1 - phi_p[8:4];
  // ---- pipeline: S0 read address, S1 block-RAM output register, S2 update
  // and write. The value S2 sees was read two edges before its write, so the
  // updates written in the two clocks before are forwarded (newest first).
  reg v1 = 0, v2 = 0, v3 = 0, v4 = 0;
  reg [AW-1:0] a1 = 0, a2 = 0, a3 = 0, a4 = 0;
  reg [11:0] w1 = 0, w2 = 0;
  reg [47:0] q0 = 0, q1 = 0;          // block RAM data, its output register
  reg [47:0] r3 = 0, r4 = 0;          // updates written one and two clocks ago
  reg [AW:0] clr = 0;
  wire in_range = bin[AW+4:AW] == 5'd0;
  assign {rd_count, rd_sum} = q1;     // the host port shares the read port
  wire take = running && word_valid && in_range;
  wire [47:0] cur2 = (v3 && a3 == a2) ? r3 : (v4 && a4 == a2) ? r4 : q1;
  wire sat = cur2[47:32] == 16'hFFFF;
  wire [47:0] new2 = sat ? cur2 : {cur2[47:32] + 16'd1, cur2[31:0] + {20'd0, w2}};
  // the single write port: clearing writes zeros, otherwise S2's update
  wire we = busy || v2;
  wire [AW-1:0] waddr = busy ? clr[AW-1:0] : a2;
  wire [47:0] wdata = busy ? 48'd0 : new2;
  always @(posedge clk) begin
    done <= 1'b0;
    // memory: one read port with its output register, one write port
    q0 <= mem[take ? bin[AW-1:0] : rd_addr];
    q1 <= q0;
    if (we) mem[waddr] <= wdata;
    if (rst) begin
      busy <= 1'b0; running <= 1'b0; records <= 16'd0; v1 <= 1'b0; v2 <= 1'b0; v3 <= 1'b0; v4 <= 1'b0;
      beyond <= 1'b0;
    end else begin
      v1 <= take; a1 <= bin[AW-1:0]; w1 <= word;
      v2 <= v1; a2 <= a1; w2 <= w1;
      v3 <= v2; a3 <= a2; r3 <= new2;
      v4 <= v3; a4 <= a3; r4 <= r3;
      if (clear) begin busy <= 1'b1; clr <= 0; running <= 1'b0; beyond <= 1'b0; end
      else if (busy) begin clr <= clr + 1'b1; if (clr[AW-1:0] == {AW{1'b1}}) busy <= 1'b0; end
      if (start && !busy && !clear) begin running <= 1'b1; records <= 16'd0; end
      if (running) begin
        if (rec_start) bin <= {{(AW){1'b0}}, p_of};
        else if (word_valid) begin
          if (!in_range) beyond <= 1'b1;
          else bin <= bin + {{AW{1'b0}}, phases};
        end
        if (rec_end) begin
          records <= records + 1'b1;
          if (records + 1'b1 == n) begin running <= 1'b0; done <= 1'b1; end
        end
      end
    end
  end
endmodule
