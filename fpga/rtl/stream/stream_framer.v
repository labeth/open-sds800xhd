// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// stream_framer -- gap-free stream blocks (spec 20 §8.1). Items (64 bits,
// at most one a clock) fill one of two block buffers; a full buffer goes out
// as beats + 2 beats of 64 bits:
//   beat 0  [15:0] 0x5354, [23:16] payload, [31:24] channel mask, [63:32] seq
//   beat 1  [47:0] time of the block's first item, bit 48: blocks were lost
//           before this one
//   beat 2+ the items
// to ring slot s (the written blocks counted mod blocks), byte address
// base + s * (beats + 2) * 8 + 8 * beat. A block is never written in part:
//   - a block whose first item finds both buffers busy is dropped whole (it
//     still takes a sequence number);
//   - a full buffer that finds the ring full (no slot free of an unconsumed
//     or in-flight block) is dropped;
// either way the items are counted in lost, the sequence number jumps and the
// next written block carries the lost flag: never a silent gap (G8).
// head counts the blocks whose last beat left; the ring writer publishes a
// block to the PS only after its write responses. One clock domain;
// configuration is latched while !en. Buffers are block RAM (registered reads).
// TRLC-LINKS: REQ-HD-015
module stream_framer #(parameter integer AW = 9) (   // up to 2^AW items a block
  input  wire         clk,
  input  wire         rst,
  input  wire         en,
  input  wire [1:0]   payload,
  input  wire [3:0]   mask,
  input  wire [AW:0]  beats,          // items a block, 1 .. 2^AW
  input  wire [15:0]  blocks,         // ring slots, >= 1
  input  wire [31:0]  base,
  input  wire [31:0]  tail,
  input  wire         in_valid,
  input  wire [63:0]  in_item,
  input  wire [47:0]  in_time,
  output wire         out_valid,
  input  wire         out_ready,
  output wire [63:0]  out_data,
  output wire [31:0]  out_addr,
  output wire         out_last,
  output reg  [31:0]  head = 32'd0,
  output reg  [31:0]  seq = 32'd0,     // blocks formed (written or dropped)
  output reg  [31:0]  lost = 32'd0,    // items dropped
  output reg          overflow = 1'b0  // sticky: something was dropped
);
  // ---- block buffers: two halves of one memory, half = buffer index
  reg [63:0] mem [0:(2<<AW)-1];
  reg [63:0] mem_q = 64'd0;
  // ---- fill side
  reg fill = 1'b0;
  reg [AW:0] count = 0;
  reg dropping = 1'b0;
  reg [1:0] busy = 2'b00;
  reg [47:0] t_first0 = 0, t_first1 = 0;
  reg [31:0] s_of0 = 0, s_of1 = 0;
  // ---- out side
  localparam O_IDLE = 3'd0, O_H0 = 3'd1, O_H1 = 3'd2, O_ITEMS = 3'd3, O_DONE = 3'd4;
  reg [2:0] ost = O_IDLE;
  reg ob = 1'b0;
  reg [AW:0] k = 0;
  reg [32:0] last_w = {33{1'b1}};       // seq of the last written block (-1: none)
  reg [31:0] slot_base = 0;
  reg [15:0] slot = 0;
  // slots taken: blocks started (head lags them by the blocks still in flight)
  reg [31:0] alloc = 32'd0;
  wire ring_full = (alloc - tail) >= {16'd0, blocks};
  wire [31:0] block_bytes = ({{(31-AW){1'b0}}, beats} + 32'd2) << 3;
  // output FIFO (4 beats): {last, addr, data}
  reg [96:0] q [0:3];
  reg [2:0] q_n = 0; reg [1:0] q_r = 0, q_w = 0;
  reg rd_pending = 1'b0;                 // an item read whose data lands next clock
  reg [31:0] rd_pending_addr = 0; reg rd_pending_last = 1'b0;
  assign out_valid = q_n != 0;
  assign {out_last, out_addr, out_data} = q[q_r];
  wire pop = out_valid && out_ready;
  wire room = (q_n + rd_pending) < 3'd3;   // space for one more beat after what is in flight
  // decisions of this clock
  wire start_blk = in_valid && count == 0;
  wire drop_ob = (ost == O_IDLE) && busy[ob] && ring_full;
  wire free_ob = drop_ob || (ost == O_DONE && !rd_pending);
  wire [1:0] freed = free_ob ? (ob ? 2'b10 : 2'b01) : 2'b00;
  wire drop_cur = start_blk ? (busy[fill] && !freed[fill]) : dropping;
  wire blk_done = in_valid && (count + 1'b1 == beats);
  reg push; reg [96:0] push_d;
  always @(posedge clk) begin
    if (rst || !en) begin
      fill <= 1'b0; count <= 0; dropping <= 1'b0; busy <= 2'b00; seq <= 32'd0;
      ost <= O_IDLE; ob <= 1'b0; last_w <= {33{1'b1}}; head <= 32'd0; alloc <= 32'd0; lost <= 32'd0;
      slot <= 16'd0; slot_base <= base; q_n <= 0; q_r <= 0; q_w <= 0; rd_pending <= 1'b0;
      if (rst) overflow <= 1'b0;
    end else begin
      // ---- fill
      if (in_valid) begin
        if (start_blk) begin
          dropping <= drop_cur;
          if (!drop_cur) begin
            if (fill) begin t_first1 <= in_time; s_of1 <= seq; end else begin t_first0 <= in_time; s_of0 <= seq; end
          end
        end
        if (!drop_cur) mem[{fill, count[AW-1:0]}] <= in_item;
        if (blk_done) begin
          count <= 0; seq <= seq + 1'b1;
          if (!drop_cur) fill <= ~fill;
        end else count <= count + 1'b1;
      end
      busy <= (busy & ~freed) | ((blk_done && !drop_cur) ? (fill ? 2'b10 : 2'b01) : 2'b00);
      lost <= lost + ((blk_done && drop_cur) ? {{(31-AW){1'b0}}, beats} : 32'd0) + (drop_ob ? {{(31-AW){1'b0}}, beats} : 32'd0);
      // ---- out: beats into the FIFO; item data arrives a clock after its read
      push = 1'b0; push_d = 97'd0;
      if (rd_pending) begin push = 1'b1; push_d = {rd_pending_last, rd_pending_addr, mem_q}; end
      rd_pending <= 1'b0;
      case (ost)
        O_IDLE: if (busy[ob]) begin
          if (ring_full) ob <= ~ob;
          else if (!push && q_n < 3'd3) begin
            push = 1'b1; push_d = {1'b0, slot_base, (ob ? s_of1 : s_of0), 4'd0, mask, 6'd0, payload, 16'h5354};
            ost <= O_H0; alloc <= alloc + 1'b1;
          end
        end
        O_H0: if (!push && q_n < 3'd3) begin
          // lost flag: the sequence jumped since the last written block
          push = 1'b1; push_d = {1'b0, slot_base + 32'd8, 15'd0, ({1'b0, ob ? s_of1 : s_of0} != last_w + 1'b1),
                                 (ob ? t_first1 : t_first0)};
          last_w <= {1'b0, ob ? s_of1 : s_of0}; ost <= O_H1; k <= 0;
        end
        O_H1, O_ITEMS: if (room) begin
          mem_q <= mem[{ob, k[AW-1:0]}]; rd_pending <= 1'b1;
          rd_pending_addr <= slot_base + 32'd16 + {{(28-AW){1'b0}}, k, 3'd0}; rd_pending_last <= (k + 1'b1 == beats);
          k <= k + 1'b1;
          ost <= (k + 1'b1 == beats) ? O_DONE : O_ITEMS;
        end
        O_DONE: if (!rd_pending) begin
          ost <= O_IDLE; ob <= ~ob;
          if (slot + 1'b1 == blocks) begin slot <= 16'd0; slot_base <= base; end
          else begin slot <= slot + 1'b1; slot_base <= slot_base + block_bytes; end
        end
        default: ost <= O_IDLE;
      endcase
      if ((blk_done && drop_cur) || drop_ob) overflow <= 1'b1;
      if (push) begin q[q_w] <= push_d; q_w <= q_w + 1'b1; end
      if (pop) q_r <= q_r + 1'b1;
      if (pop && out_last) head <= head + 1'b1;
      q_n <= q_n + push - pop;
    end
  end
endmodule
