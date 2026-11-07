// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// readout -- the readout engine of spec 20 §6.4 (mclk). START: the window
// (channel, segment, first ring word, word count, mode, bucket k) and the
// record geometry are taken; the engine reads the channel's ring through
// S_AXI_HP0's read channels, one three-beat burst per P12 group, wrapping
// at the ring's end, unpacks each word to the 16-bit word of spec 03 §5
// (code << 4) and reduces:
//   0 words   every word
//   1 nth     the first word of every k
//   2 minmax  per bucket of k words its maximum, then its minimum
//   3 sum     per bucket the 32-bit sum of the 16-bit words (low half first)
// (a last partial bucket is delivered too). The 16-bit results go out as
// 64-bit AXI-Stream beats to the AXI DMA's S2MM, oldest in bits 15:0, the
// last beat padded with zeros and marked tlast. While the stacker runs
// (to_stack) the words go to it instead, between a start marker carrying
// the segment and an end marker. A start word outside the ring is an error
// (nothing delivered). done pulses when the last beat or marker left.
// TRLC-LINKS: REQ-HD-015
module readout (
  input  wire          clk,
  input  wire          rst,
  input  wire          start,
  input  wire [1:0]    ch,
  input  wire [9:0]    seg,
  input  wire [31:0]   first,
  input  wire [31:0]   count,
  input  wire [1:0]    mode,
  input  wire [23:0]   bucket,
  input  wire [31:0]   rec_base,
  input  wire [27:0]   rec_groups,
  input  wire [3:0]    mask,
  input  wire          to_stack,
  output reg  [31:0]   m_araddr = 32'd0,
  output wire [7:0]    m_arlen,
  output reg           m_arvalid = 1'b0,
  input  wire          m_arready,
  input  wire [127:0]  m_rdata,
  input  wire [1:0]    m_rresp,
  input  wire          m_rlast,
  input  wire          m_rvalid,
  output wire          m_rready,
  output reg  [63:0]   m_tdata = 64'd0,
  output reg           m_tvalid = 1'b0,
  output reg           m_tlast = 1'b0,
  input  wire          m_tready,
  output reg           s_valid = 1'b0,    // to the stacker: kind 0 start (data = segment), 1 word, 2 end
  output reg  [1:0]    s_kind = 2'd0,
  output reg  [11:0]   s_data = 12'd0,
  input  wire          s_full,
  output reg           busy = 1'b0,
  output reg           error = 1'b0,
  output reg           done = 1'b0,
  output reg           axi_err = 1'b0
);
  assign m_arlen = 8'd2;
  assign m_rready = 1'b1;
  localparam R_IDLE = 3'd0, R_AR = 3'd1, R_DATA = 3'd2, R_WORDS = 3'd3, R_TAIL = 3'd4, R_LAST = 3'd5, R_END = 3'd6;
  reg [2:0]  st = R_IDLE;
  reg [31:0] base = 32'd0, rw = 32'd0, pos = 32'd0, left = 32'd0;
  reg [1:0]  md = 2'd0; reg [23:0] k = 24'd1; reg stk = 1'b0;
  reg [383:0] grp = 384'd0;
  reg [1:0]  beat = 2'd0;
  reg [23:0] bc = 24'd0;
  reg [15:0] bmax = 16'd0, bmin = 16'hFFFF; reg [31:0] bsum = 32'd0;
  reg [111:0] ub = 112'd0; reg [2:0] un = 3'd0;   // result units waiting for a beat
  function [2:0] nch(input [3:0] m); nch = {2'd0, m[0]} + {2'd0, m[1]} + {2'd0, m[2]} + {2'd0, m[3]}; endfunction
  function [2:0] slot(input [3:0] m, input [1:0] c);
    integer j; reg [2:0] s;
    begin s = 3'd0; for (j = 0; j < 4; j = j + 1) if (j < c && m[j]) s = s + 3'd1; slot = s; end
  endfunction
  // one word of the current group per clock
  wire [11:0] code = grp[12*pos[4:0] +: 12];
  wire [15:0] w16 = {code, 4'd0};
  wire last_in = (left == 32'd1);
  wire bend = (bc + 24'd1 >= k) || last_in;
  // the units this word produces
  reg [31:0] u; reg [1:0] nu;
  always @(*) begin
    u = 32'd0; nu = 2'd0;
    case (md)
      2'd0: begin u = {16'd0, w16}; nu = 2'd1; end
      2'd1: if (bc == 24'd0) begin u = {16'd0, w16}; nu = 2'd1; end
      2'd2: if (bend) begin
        u = {(bc == 24'd0 || w16 < bmin) ? w16 : bmin, (bc == 24'd0 || w16 > bmax) ? w16 : bmax}; nu = 2'd2;
      end
      default: if (bend) begin u = (bc == 24'd0 ? 32'd0 : bsum) + {16'd0, w16}; nu = 2'd2; end
    endcase
  end
  wire can_word = (st == R_WORDS) && (stk ? !s_full : (un < 3'd4));
  wire out_free = !m_tvalid || m_tready;
  reg [111:0] ub_n; reg [2:0] un_n;
  always @(posedge clk) begin
    done <= 1'b0; s_valid <= 1'b0;
    if (m_tvalid && m_tready) begin m_tvalid <= 1'b0; m_tlast <= 1'b0; end
    ub_n = ub; un_n = un;
    if (rst) begin
      st <= R_IDLE; busy <= 1'b0; m_arvalid <= 1'b0; m_tvalid <= 1'b0; un <= 3'd0; error <= 1'b0; axi_err <= 1'b0;
    end else begin
      // full beats out
      if (out_free && un_n >= 3'd4) begin
        m_tdata <= ub_n[63:0]; m_tvalid <= 1'b1; m_tlast <= (st == R_TAIL || st == R_LAST) && un_n == 3'd4;
        ub_n = {64'd0, ub_n[111:64]}; un_n = un_n - 3'd4;
      end
      case (st)
        R_IDLE: if (start) begin
          base <= rec_base + (({22'd0, seg} * {29'd0, nch(mask)} + {29'd0, slot(mask, ch)}) * ({4'd0, rec_groups} * 32'd48));
          rw <= {rec_groups[26:0], 5'd0}; pos <= first; left <= count; md <= mode; k <= (bucket == 24'd0) ? 24'd1 : bucket;
          stk <= to_stack; bc <= 24'd0; error <= 1'b0; un_n = 3'd0;
          busy <= 1'b1;
          if (first >= {rec_groups[26:0], 5'd0} || count == 32'd0 || rec_groups == 28'd0) begin
            error <= first >= {rec_groups[26:0], 5'd0} || rec_groups == 28'd0; st <= R_END;
          end else begin
            if (to_stack) begin s_valid <= 1'b1; s_kind <= 2'd0; s_data <= {2'd0, seg}; end
            st <= R_AR;
          end
        end
        R_AR: if (!m_arvalid) begin
          m_araddr <= base + {pos[31:5], 5'd0} + {pos[31:5], 4'd0}; m_arvalid <= 1'b1; beat <= 2'd0;
        end else if (m_arready) begin m_arvalid <= 1'b0; st <= R_DATA; end
        R_DATA: if (m_rvalid) begin
          grp[128*beat +: 128] <= m_rdata; beat <= beat + 2'd1;
          if (m_rresp != 2'b00) axi_err <= 1'b1;
          if (m_rlast || beat == 2'd2) st <= R_WORDS;
        end
        R_WORDS: if (can_word) begin
          if (stk) begin s_valid <= 1'b1; s_kind <= 2'd1; s_data <= code; end
          else if (nu != 2'd0) begin
            ub_n[16*un_n +: 32] = u; un_n = un_n + {1'b0, nu};
          end
          if (md == 2'd2) begin
            if (bc == 24'd0 || w16 > bmax) bmax <= w16;
            if (bc == 24'd0 || w16 < bmin) bmin <= w16;
          end
          bsum <= (bc == 24'd0 ? 32'd0 : bsum) + {16'd0, w16};
          bc <= bend ? 24'd0 : bc + 24'd1;
          left <= left - 32'd1;
          if (pos + 32'd1 >= rw) pos <= 32'd0; else pos <= pos + 32'd1;
          if (last_in) st <= stk ? R_LAST : R_TAIL;
          else if (pos[4:0] == 5'd31 || pos + 32'd1 >= rw) st <= R_AR;
        end
        R_TAIL: if (out_free && un_n < 3'd4) begin
          // the last, partial beat (padded); un_n == 0 means the full beats already went out marked
          if (un_n != 3'd0) begin
            m_tdata <= ub_n[63:0]; m_tvalid <= 1'b1; m_tlast <= 1'b1; un_n = 3'd0;
          end
          st <= R_END;
        end
        R_LAST: if (!s_full) begin s_valid <= 1'b1; s_kind <= 2'd2; s_data <= 12'd0; st <= R_END; end
        R_END: if (!m_tvalid || m_tready) begin busy <= 1'b0; done <= 1'b1; st <= R_IDLE; end
        default: st <= R_IDLE;
      endcase
      ub <= ub_n; un <= un_n;
    end
  end
endmodule
