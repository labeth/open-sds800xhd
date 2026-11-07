// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// acq_fsm -- the acquisition state machine and record packing of spec 20
// §6.1–§6.3 (cclk). It runs on the record path, which carries the samples
// DLY clocks behind the trigger engine: a trigger is known before its
// sample reaches the decimator, so the trigger word, the end of the record
// and the pre-trigger are exact to the word.
//   IDLE   reset, STOP/CLEAR, after DONE
//   PRE    ARM (it implies COMMIT: the record path restarts and the segment
//          begins with the restart's first words) or the next segment;
//          ACQ_PRE words appended -> ARMED
//   ARMED  the trigger engine is armed; its accepted trigger -> POST
//   POST   ACQ_POST words after the trigger word -> the next segment, or
//          FLUSH after the last
//   FLUSH  the packers' last groups out and written (the writer's count
//          crosses back), the interpolator's fraction in -> DONE (acq_done)
//   DONE   the record is in memory; ARM or CLEAR leave it
// Each channel's ring for segment s is REC_GROUPS groups of 32 words at
// REC_BASE + (s·N_ch + slot)·REC_GROUPS·48, slot = the channel's place in
// mask; words go round it while pre-triggering and linearly after the
// trigger. A group whose FIFO is above its high-water mark is lost and
// counted (REC_LOST, overflow, the rec_overflow fault).
// Reported state: IDLE 0, PRE 1, ARMED 2, POST 3 (also while flushing), DONE 4.
// TRLC-LINKS: REQ-HD-015
module acq_fsm (
  input  wire          clk,
  input  wire          rst,
  input  wire          arm,
  input  wire          stop,
  input  wire          force_in,
  input  wire          clear,
  input  wire [3:0]    mask,
  input  wire [1:0]    mode,
  input  wire [31:0]   pre,
  input  wire [31:0]   post,
  input  wire [15:0]   segments,
  input  wire [31:0]   rec_base,
  input  wire [27:0]   rec_groups,
  input  wire [2:0]    tpl,
  // the decimator (record path)
  input  wire          d_first,
  input  wire [3:0]    d_nw,
  input  wire [511:0]  d_words,
  input  wire [47:0]   d_wfirst,
  input  wire [383:0]  d_grp,
  input  wire [223:0]  d_ph,
  input  wire [47:0]   d_t,
  // the trigger engine
  output wire          armed,
  output wire          force_out,
  input  wire          trig,
  input  wire [47:0]   trig_time,
  input  wire          trig_auto,
  input  wire          trig_forced,
  input  wire          interp_en,
  input  wire [4:0]    frac,
  input  wire          frac_valid,
  // the record FIFOs (cclk side): {addr, group}
  output reg  [3:0]    push = 4'd0,
  output reg  [1663:0] push_data = 1664'd0,
  input  wire [3:0]    hiwater,
  input  wire [31:0]   groups_written,
  // status
  output reg  [2:0]    state_out = 3'd0,
  output reg  [31:0]   status = 32'd0,
  output reg  [31:0]   rec_lost = 32'd0,
  output reg  [47:0]   t_trig = 48'd0,
  output reg  [31:0]   trig_pos = 32'd0,
  output reg  [31:0]   trig_phase = 32'd0,
  output reg  [4:0]    trig_frac = 5'd0,
  output reg           frac_ok = 1'b0,
  output reg  [2:0]    trig_flags = 3'd0,  // {overflow, forced, auto}
  output reg  [31:0]   words_out = 32'd0,
  input  wire [9:0]    seg_sel,
  output reg  [47:0]   seg_time = 48'd0,
  output reg  [31:0]   seg_pos = 32'd0,
  output reg  [6:0]    seg_flags = 7'd0,   // {forced, auto, frac}
  input  wire [9:0]    seg_sel2,           // a second read port (the stacker's phase)
  output reg  [6:0]    seg_flags2 = 7'd0,
  output reg           done = 1'b0,
  output reg           overflow_pulse = 1'b0
);
  localparam S_IDLE = 3'd0, S_PRE = 3'd1, S_ARMED = 3'd2, S_POST = 3'd3, S_DONE = 3'd4,
             S_SEGEND = 3'd5, S_FLUSH = 3'd6;
  reg [2:0]  st = S_IDLE;
  reg        arm_pend = 1'b0, stopping = 1'b0, triggered = 1'b0, located = 1'b0;
  reg        f_auto = 1'b0, f_forced = 1'b0, f_ovf = 1'b0, frac_wait = 1'b0;
  reg [47:0] w_seg0 = 48'd0, w_next = 48'd0, w_hi = 48'd0, pend_t = 48'd0;
  reg [31:0] wseg = 32'd0, rp = 32'd0;
  reg [15:0] seg = 16'd0;
  reg [3:0]  flushn = 4'd0;
  reg [31:0] pushed = 32'd0;
  reg [31:0] wr_base = 32'd0;         // the writer's count when this domain left reset
  reg [27:0] rg = 28'd0;
  reg [31:0] rg_off = 32'd0;
  reg [127:0] slot_base = 128'd0;     // per channel: byte offset of its ring in this segment
  wire [31:0] rw = {rec_groups[26:0], 5'd0};
  assign armed = (st == S_ARMED);
  assign force_out = force_in && (st == S_PRE || st == S_ARMED);
  // ---- segment table
  reg [47:0] tab_time [0:1023];
  reg [31:0] tab_pos  [0:1023];
  reg [6:0]  tab_flag [0:1023];
  always @(posedge clk) begin
    seg_time <= tab_time[seg_sel]; seg_pos <= tab_pos[seg_sel]; seg_flags <= tab_flag[seg_sel];
    seg_flags2 <= tab_flag[seg_sel2];
  end
  // ---- where the pending trigger sits in this record-path clock
  reg [2:0]  sh; reg [47:0] dt; reg [2:0] lane; reg here;
  reg [47:0] wt_c; reg [31:0] ph_c;
  always @(*) begin
    sh = (tpl == 3'd4) ? 3'd2 : (tpl == 3'd2) ? 3'd1 : 3'd0;
    dt = pend_t - d_t;
    here = (st == S_POST) && !located && (pend_t < d_t || dt < 48'd8);
    lane = (pend_t < d_t) ? 3'd0 : dt[2:0] >> sh;
    wt_c = d_grp[48*lane +: 48];
    if (mode == 2'd2) wt_c = {wt_c[46:0], 1'b0};
    ph_c = {4'd0, d_ph[28*lane +: 28]} << sh;
  end
  // ---- the words of this clock that belong to the segment
  reg [47:0] lo, hi, wlast; reg [3:0] k0, n; reg in_seg;
  always @(*) begin
    in_seg = (st == S_PRE || st == S_ARMED || st == S_POST);
    hi = here ? wt_c + {16'd0, post} : (triggered && located) ? w_hi : {48{1'b1}};
    lo = (arm_pend && d_first) ? d_wfirst : w_seg0;
    wlast = d_wfirst + {44'd0, d_nw} - 48'd1;
    k0 = 4'd0; n = 4'd0;
    if (in_seg && d_nw != 4'd0 && wlast >= lo && d_wfirst <= hi) begin
      k0 = (d_wfirst >= lo) ? 4'd0 : lo[3:0] - d_wfirst[3:0];
      n = ((wlast <= hi) ? d_nw : (hi[3:0] - d_wfirst[3:0] + 4'd1)) - k0;
    end
  end
  wire seg_next = (st == S_SEGEND) && flushn == 4'd0 && !frac_wait;
  wire pk_clear = rst || arm || seg_next;
  // the trigger word's ring position from the next word's (rp at w_next)
  function [31:0] ring_pos(input [31:0] r, input [31:0] dd, input [31:0] w);
    reg [31:0] s;
    begin
      s = r + dd;
      if (dd[31]) ring_pos = (r < (32'd0 - dd)) ? s + w : s;
      else ring_pos = (s >= w) ? s - w : s;
    end
  endfunction
  wire seg_end = in_seg && triggered && (here || located) && (d_wfirst + {44'd0, k0} + {44'd0, n} > hi);
  // ---- packers
  wire [3:0] gv; wire [1535:0] gd;
  genvar c;
  generate for (c = 0; c < 4; c = c + 1) begin : g_pack
    acq_pack u_pack (.clk(clk), .clear(pk_clear),
      .words(d_words[128*c +: 128]), .k0(k0), .n(mask[c] ? n : 4'd0), .flush(flushn != 4'd0),
      .gv(gv[c]), .gdata(gd[384*c +: 384]), .gidx());
  end endgenerate
  // slot of each channel in the mask
  function [1:0] slot_of(input [3:0] m, input integer ch);
    integer j; reg [1:0] s;
    begin s = 2'd0; for (j = 0; j < 4; j = j + 1) if (j < ch && m[j]) s = s + 2'd1; slot_of = s; end
  endfunction
  function [2:0] nch_of(input [3:0] m); nch_of = {2'd0, m[0]} + {2'd0, m[1]} + {2'd0, m[2]} + {2'd0, m[3]}; endfunction
  wire [31:0] seg_bytes = rec_groups * 32'd48;
  integer j;
  reg [31:0] np; reg any_g;
  always @(posedge clk) begin
    done <= 1'b0; overflow_pulse <= 1'b0; push <= 4'd0;
    if (frac_valid && frac_wait) begin trig_frac <= frac; frac_ok <= 1'b1; frac_wait <= 1'b0; end
    // groups out of the packers: ring position, FIFO push or loss
    np = pushed; any_g = 1'b0;
    for (j = 0; j < 4; j = j + 1) if (gv[j] && mask[j]) begin
      any_g = 1'b1;
      if (hiwater[j]) begin
        rec_lost <= rec_lost + 32'd1; f_ovf <= 1'b1; overflow_pulse <= 1'b1;
      end else begin
        push[j] <= 1'b1;
        push_data[416*j +: 416] <= {rec_base + slot_base[32*j +: 32] + rg_off, gd[384*j +: 384]};
        np = np + 32'd1;
      end
    end
    pushed <= np;
    if (any_g) begin
      if (rg + 28'd1 >= rec_groups) begin rg <= 28'd0; rg_off <= 32'd0; end
      else begin rg <= rg + 28'd1; rg_off <= rg_off + 32'd48; end
    end
    if (flushn != 4'd0) flushn <= flushn - 4'd1;
    if (rst || clear) begin
      st <= S_IDLE; arm_pend <= 1'b0; stopping <= 1'b0; triggered <= 1'b0; located <= 1'b0;
      f_auto <= 1'b0; f_forced <= 1'b0; f_ovf <= 1'b0; frac_ok <= 1'b0; frac_wait <= 1'b0;
      flushn <= 4'd0; seg <= 16'd0;
      if (rst) begin rec_lost <= 32'd0; pushed <= 32'd0; wr_base <= groups_written; end
    end else begin
      if (arm) begin
        arm_pend <= 1'b1; st <= S_PRE; seg <= 16'd0; stopping <= 1'b0; f_ovf <= 1'b0;
        triggered <= 1'b0; located <= 1'b0; wseg <= 32'd0; rp <= 32'd0; rg <= 28'd0; rg_off <= 32'd0;
        w_seg0 <= {48{1'b1}}; frac_ok <= 1'b0; frac_wait <= 1'b0; f_auto <= 1'b0; f_forced <= 1'b0;
        for (j = 0; j < 4; j = j + 1) slot_base[32*j +: 32] <= {30'd0, slot_of(mask, j)} * seg_bytes;
      end else begin
        if (stop) begin
          if (st == S_PRE || st == S_ARMED) st <= S_IDLE;
          else if (st == S_POST) stopping <= 1'b1;
        end
        // the restart's first words begin the first segment
        if (arm_pend && d_first) begin arm_pend <= 1'b0; w_seg0 <= d_wfirst; w_next <= d_wfirst; end
        if (in_seg && !(stop && st != S_POST)) begin
          wseg <= wseg + {28'd0, n};
          rp <= (rp + {28'd0, n} >= rw) ? rp + {28'd0, n} - rw : rp + {28'd0, n};
          if (here) begin
            located <= 1'b1; w_hi <= wt_c + {16'd0, post}; trig_phase <= ph_c;
            // the trigger word's ring position: rp is the next word's
            trig_pos <= ring_pos(rp, wt_c[31:0] - w_next[31:0], rw);
          end
          if (n != 4'd0) w_next <= d_wfirst + {44'd0, k0} + {44'd0, n};
          if (st == S_PRE && !arm_pend && wseg + {28'd0, n} >= pre) st <= S_ARMED;
          if (st == S_ARMED && trig) begin
            st <= S_POST; triggered <= 1'b1; pend_t <= trig_time; t_trig <= trig_time;
            f_auto <= trig_auto; f_forced <= trig_forced;
            frac_ok <= 1'b0; frac_wait <= interp_en && !trig_auto && !trig_forced;
          end
          if (seg_end) begin st <= S_SEGEND; flushn <= 4'd3; end
        end
        case (st)
          S_SEGEND: if (seg_next) begin
            tab_time[seg[9:0]] <= t_trig; tab_pos[seg[9:0]] <= trig_pos;
            tab_flag[seg[9:0]] <= {f_forced, f_auto, frac_ok ? trig_frac : 5'd0};
            words_out <= wseg;
            if (seg + 16'd1 < segments && !stopping) begin
              seg <= seg + 16'd1; st <= S_PRE; triggered <= 1'b0; located <= 1'b0; wseg <= 32'd0; rp <= 32'd0;
              rg <= 28'd0; rg_off <= 32'd0; w_seg0 <= w_hi + 48'd1; w_next <= w_hi + 48'd1;
              for (j = 0; j < 4; j = j + 1)
                slot_base[32*j +: 32] <= (({16'd0, seg} + 32'd1) * {29'd0, nch_of(mask)} + {30'd0, slot_of(mask, j)}) * seg_bytes;
            end else st <= S_FLUSH;
          end
          S_FLUSH: if (groups_written - wr_base == pushed && !any_g) begin st <= S_DONE; done <= 1'b1; end
          default: ;
        endcase
      end
    end
  end
  always @(*) begin
    case (st)
      S_IDLE: state_out = 3'd0;
      S_PRE: state_out = 3'd1;
      S_ARMED: state_out = 3'd2;
      S_DONE: state_out = 3'd4;
      default: state_out = 3'd3;
    endcase
    status = {seg, 7'd0, stopping, f_ovf, f_forced, f_auto, triggered, 1'b0, state_out};
    trig_flags = {f_ovf, f_forced, f_auto};
  end
endmodule
