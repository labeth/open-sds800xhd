// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// stream_source -- the items of the gap-free stream (spec 20 §8.1), cclk:
//   payload 1 codes:  the channel streams picked every D-th sample (STREAM_
//                     DECIM), four 16-bit words of one channel an item
//                     (oldest in bits 15:0); several channels alternate item
//                     by item in mask order; an item's time is its first
//                     word's sample time
//   payload 3 events: one decoded protocol event an item: [47:0] time in
//                     ticks, [55:48] data, [63:56] kind (1 start, 2 data,
//                     3 end, 4 error)
// Items queue in a 32-entry buffer and leave one a clock towards the
// framer's clock domain; an item that finds the buffer full is counted in
// lost and raises overflow (the board keeps the item rate at or below one
// a clock, so this is a fault, never a mode). Bits payloads are not built.
// TRLC-LINKS: REQ-HD-015
module stream_source (
  input  wire          clk,
  input  wire          rst,
  input  wire          en,
  input  wire [1:0]    payload,
  input  wire [3:0]    mask,
  input  wire [27:0]   d,
  input  wire [383:0]  xs,
  input  wire [7:0]    v,
  input  wire [2:0]    tpl,
  input  wire [47:0]   t_clk,
  input  wire          ev_valid,
  input  wire [7:0]    ev_kind,
  input  wire [7:0]    ev_data,
  output reg           out_valid = 1'b0,
  output reg  [111:0]  out_data = 112'd0,   // {time, item}
  input  wire          out_full,
  output reg  [31:0]   lost = 32'd0,
  output reg           overflow = 1'b0
);
  wire [3:0] nw; wire [511:0] words; wire [383:0] wt;
  acq_decim u_dec (.clk(clk), .restart(!en), .xs(xs), .v(v), .tpl(tpl), .t_clk(t_clk), .d(d), .mode(2'd0),
    .nw(nw), .words(words), .wfirst(), .wt(wt), .grp(), .ph(), .t_out(), .v_out());
  reg [47:0] t1 = 48'd0;
  always @(posedge clk) t1 <= t_clk;
  reg [255:0] acc = 256'd0;          // per channel the item being filled
  reg [1:0]   an = 2'd0;
  reg [47:0]  at = 48'd0;            // time of the item's first word
  reg [111:0] q [0:31];
  reg [5:0]   qw = 6'd0, qr = 6'd0;
  reg [255:0] c_acc; reg [2:0] c_an; reg [47:0] c_at; reg [5:0] c_qw; reg [31:0] c_lost; reg c_ovf;
  integer i, c;
  always @(posedge clk) begin
    if (rst || !en) begin
      an <= 2'd0; qw <= 6'd0; qr <= 6'd0; out_valid <= 1'b0;
      if (rst) begin lost <= 32'd0; overflow <= 1'b0; end
    end else begin
      c_acc = acc; c_an = {1'b0, an}; c_at = at; c_qw = qw; c_lost = lost; c_ovf = overflow;
      if (payload == 2'd1) begin
        for (i = 0; i < 8; i = i + 1) if (i < nw) begin
          if (c_an == 3'd0) c_at = wt[48*i +: 48];
          for (c = 0; c < 4; c = c + 1) c_acc[64*c + 16*c_an +: 16] = words[128*c + 16*i +: 16];
          c_an = c_an + 3'd1;
          if (c_an == 3'd4) begin
            c_an = 3'd0;
            for (c = 0; c < 4; c = c + 1) if (mask[c]) begin
              if (c_qw - qr >= 6'd32) begin c_lost = c_lost + 32'd1; c_ovf = 1'b1; end
              else begin q[c_qw[4:0]] <= {c_at, c_acc[64*c +: 64]}; c_qw = c_qw + 6'd1; end
            end
          end
        end
      end else if (payload == 2'd3 && ev_valid) begin
        if (c_qw - qr >= 6'd32) begin c_lost = c_lost + 32'd1; c_ovf = 1'b1; end
        else begin q[c_qw[4:0]] <= {t1, ev_kind, ev_data, t1}; c_qw = c_qw + 6'd1; end
      end
      acc <= c_acc; an <= c_an[1:0]; at <= c_at; lost <= c_lost; overflow <= c_ovf;
      out_valid <= 1'b0;
      if (qr != qw && !out_full) begin out_valid <= 1'b1; out_data <= q[qr[4:0]]; qr <= qr + 6'd1; end
      qw <= c_qw;
    end
  end
endmodule
