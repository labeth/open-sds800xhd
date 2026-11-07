// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// opl_core -- the open PL without the parts that wait for board facts
// (spec 20 §14): everything from the ISERDES outputs to the PS ports. The
// capture (IDELAY/ISERDES/MMCM, U3–U5), the display scan-out (U8), the
// panel and offset-DAC serializers (U9, U10), the Xilinx UI VDMA and AXI DMA
// IP and the compat MAP plane sit around it in the full image; the
// co-simulation drives its ports directly (fpga/cosim).
//
// Clock domains (spec 20 §3): aclk (registers, 100 MHz), cclk (the sample
// core, 250 MHz), mclk (the HP ports, 300 MHz). Crossings:
//   aclk -> cclk  configuration latched at COMMIT/ARM (spec 20 §3.1); the
//                 stream, stacker, counter and segment-select words are
//                 quasi-static (written while their block is off) and are
//                 taken at the block's enable or start; strobes cross as one
//                 accumulated word through a bus synchronizer
//   aclk -> mclk  readout start and the stream tail through bus synchronizers;
//                 the readout window is taken at its start
//   c/mclk -> aclk  every register read fetches a fresh snapshot of both
//                 domains' status (request and answer through bus
//                 synchronizers; the AXI-Lite read waits for it, at most 48
//                 aclk cycles, then answers with the last snapshot); interrupt
//                 causes and faults cross as pulses
//   cclk <-> mclk record groups, stream items and stacker words through
//                 asynchronous FIFOs; the writer's group count back through a
//                 bus synchronizer
// The record path runs DLY clocks behind the trigger engine (acq_fsm).
// AXI-Lite addresses are offsets from 0x8000_0000: compat ACQ 0x03xxxx,
// open plane 0x10xxxx, stacker bins 0x11xxxx; anything else reads 0.
// TRLC-LINKS: REQ-HD-015
module opl_core #(
  parameter [31:0] BUILD = 32'h0,
  parameter [31:0] IMAGE = 32'h6C6C7566,   // ID_IMAGE, four characters, first in bits 7:0 ("full")
  parameter integer DLY = 10
) (
  input  wire          aclk,
  input  wire          mclk,
  input  wire          cclk,
  input  wire          aresetn,
  // HPM0_LPD (AXI-Lite)
  input  wire [23:0]   s_awaddr,
  input  wire          s_awvalid,
  output wire          s_awready,
  input  wire [31:0]   s_wdata,
  input  wire [3:0]    s_wstrb,
  input  wire          s_wvalid,
  output wire          s_wready,
  output wire [1:0]    s_bresp,
  output wire          s_bvalid,
  input  wire          s_bready,
  input  wire [23:0]   s_araddr,
  input  wire          s_arvalid,
  output wire          s_arready,
  output wire [31:0]   s_rdata,
  output wire [1:0]    s_rresp,
  output wire          s_rvalid,
  input  wire          s_rready,
  // the ISERDES outputs after the lane map, four samples a clock per core
  input  wire [47:0]   adc0_i,
  input  wire [47:0]   adc0_q,
  input  wire [47:0]   adc1_i,
  input  wire [47:0]   adc1_q,
  // S_AXI_HP0 (128): record writer and readout reader
  output wire [31:0]   hp0_awaddr,
  output wire [7:0]    hp0_awlen,
  output wire          hp0_awvalid,
  input  wire          hp0_awready,
  output wire [127:0]  hp0_wdata,
  output wire          hp0_wlast,
  output wire          hp0_wvalid,
  input  wire          hp0_wready,
  input  wire [1:0]    hp0_bresp,
  input  wire          hp0_bvalid,
  output wire          hp0_bready,
  output wire [31:0]   hp0_araddr,
  output wire [7:0]    hp0_arlen,
  output wire          hp0_arvalid,
  input  wire          hp0_arready,
  input  wire [127:0]  hp0_rdata,
  input  wire [1:0]    hp0_rresp,
  input  wire          hp0_rlast,
  input  wire          hp0_rvalid,
  output wire          hp0_rready,
  // S_AXI_HP1 (64, write only): stream ring
  output wire [31:0]   hp1_awaddr,
  output wire [7:0]    hp1_awlen,
  output wire          hp1_awvalid,
  input  wire          hp1_awready,
  output wire [63:0]   hp1_wdata,
  output wire          hp1_wlast,
  output wire          hp1_wvalid,
  input  wire          hp1_wready,
  input  wire [1:0]    hp1_bresp,
  input  wire          hp1_bvalid,
  output wire          hp1_bready,
  // readout stream to the AXI DMA's S2MM (mclk)
  output wire [63:0]   dma_tdata,
  output wire          dma_tvalid,
  output wire          dma_tlast,
  input  wire          dma_tready,
  // pl_ps_irq0[5]: the open-plane interrupt (spec 20 §4.4)
  output wire          irq
);
`include "open_pl_regs.vh"
`include "regs/opl_sts.vh"
  // ======================================================================
  // resets
  wire arst;
  prim_reset_sync u_arst (.clk(aclk), .rst_in(!aresetn), .rst_out(arst));
  wire mrst;
  prim_reset_sync u_mrst (.clk(mclk), .rst_in(!aresetn), .rst_out(mrst));
  wire core_hold;                       // aclk: SYS_CTRL core reset or the ISERDES reset (compat 0x04F)
  wire crst;
  prim_reset_sync u_crst (.clk(cclk), .rst_in(!aresetn || core_hold), .rst_out(crst));

  // ======================================================================
  // aclk: AXI-Lite, register planes
  wire        wr, rd; wire [23:0] wa, ra; wire [31:0] wd; wire [3:0] ws;
  reg  [31:0] rdat;
  wire        hold;
  axil_slave #(.AW(24)) u_axil (.clk(aclk), .rst(arst),
    .s_awaddr(s_awaddr), .s_awvalid(s_awvalid), .s_awready(s_awready), .s_wdata(s_wdata), .s_wstrb(s_wstrb),
    .s_wvalid(s_wvalid), .s_wready(s_wready), .s_bresp(s_bresp), .s_bvalid(s_bvalid), .s_bready(s_bready),
    .s_araddr(s_araddr), .s_arvalid(s_arvalid), .s_arready(s_arready), .s_rdata(s_rdata), .s_rresp(s_rresp),
    .s_rvalid(s_rvalid), .s_rready(s_rready),
    .wr(wr), .wr_addr(wa), .wr_data(wd), .wr_strb(ws), .rd(rd), .rd_addr(ra), .rd_data(rdat), .rd_hold(hold));
  wire w_open = wa[23:16] == 8'h10, r_open = ra[23:16] == 8'h10;
  wire w_acq = wa[23:16] == 8'h03, r_acq = ra[23:16] == 8'h03;
  wire r_stk = ra[23:16] == 8'h11;
  localparam integer NW = 1024;
  wire [NW*32-1:0] cfg;
  reg  [32*32-1:0] sts = 0;
  wire [31:0] acq_pulse, stack_pulse, ro_pulse, ctr_pulse; wire tail_wr;
  wire [4:0] fault_set; wire [3:0] cause_set;
  wire irq_take;
  wire [95:0] dna; wire dna_ready; wire dna_read;
  wire [31:0] rd_open, rd_acq;
  opl_regs #(.BUILD(BUILD), .IMAGE(IMAGE)) u_open (.clk(aclk), .rst(arst), .wr(wr && w_open), .wr_addr(wa[15:0]), .wr_data(wd),
    .wr_strb(ws), .rd(rd && r_open), .rd_addr(ra[15:0]), .rd_data(rd_open), .cfg(cfg), .sts(sts), .dna(dna),
    .acq_ctrl_pulse(acq_pulse), .stack_ctrl_pulse(stack_pulse), .readout_ctrl_pulse(ro_pulse),
    .counter_clear_pulse(ctr_pulse), .stream_tail_written(tail_wr), .fault_set(fault_set),
    .irq_cause_set(cause_set), .irq_take(irq_take), .irq(irq));
  function [31:0] cw(input [15:0] a); cw = cfg[32*(a/4) +: 32]; endfunction
  // compat ACQ
  wire iserdes_reset, mean_start_a, core_alive;
  wire [15:0] mean_n; wire [1:0] mean_ch;
  reg  mean_valid_a = 1'b0; reg [3:0] mean_ovf_a = 4'd0; reg [191:0] mean_sum_a = 192'd0; reg [31:0] mean_cnt_a = 32'd0;
  compat_acq u_compat (.clk(aclk), .rst(arst), .wr(wr && w_acq), .wr_addr(wa[15:0]), .wr_data(wd),
    .rd(rd && r_acq), .rd_addr(ra[15:0]), .rd_data(rd_acq),
    .mmcm_reset(), .mmcm0_locked(1'b1), .core_alive(core_alive), .mmcm1_locked(1'b1), .iserdes_reset(iserdes_reset),
    .phase_apply(), .phase(), .data_ready(2'b11), .err_clear(), .phase_cal(), .adc_error(2'b00), .pll_sync(),
    .adc_type(), .rec_ready(cw(OPL_ACQ_REC_BASE) != 32'd0),
    .dac_ctrl(), .dac_code(), .dac_write(), .irq_take(irq_take), .dna_read(dna_read), .dna(dna), .dna_ready(dna_ready),
    .mean_n(mean_n), .mean_ch(mean_ch), .mean_start(mean_start_a), .mean_valid(mean_valid_a),
    .mean_overflow(mean_ovf_a), .mean_sum(mean_sum_a[48*mean_ch +: 48]), .mean_count(mean_cnt_a));
  // the DNA is read once after reset and on the compat request
  reg dna_boot = 1'b1;
  always @(posedge aclk) dna_boot <= arst;
  prim_dna u_dna (.clk(aclk), .read(dna_read || (dna_boot && !arst)), .dna(dna), .ready(dna_ready));
  wire [31:0] sys_ctrl = cw(OPL_SYS_CTRL);
  assign core_hold = sys_ctrl[0] || iserdes_reset;
  // aclk copies of the other domains' state (snapshots)
  reg [31:0] src_lost_a = 32'd0, f_lost_a = 32'd0;
  reg core_alive_a = 1'b0, src_ovf_a = 1'b0;
  assign core_alive = core_alive_a;
  wire sc_v, sm_v;
  // read data: the stacker window answers from the snapshot
  reg [47:0] stk_bin_a = 48'd0;
  always @(*) begin
    if (r_open) rdat = rd_open;
    else if (r_acq) rdat = rd_acq;
    else if (r_stk) rdat = ra[2] ? {16'd0, stk_bin_a[47:32]} : stk_bin_a[31:0];
    else rdat = 32'd0;
  end

  // ---- strobes into cclk: one word, accumulated while the crossing is busy
  // bits: 0 arm, 1 stop, 2 force, 3 clear, 4 commit, 5 stack clear, 6 stack start, 7 counter clear, 8 mean start
  reg  [8:0] stb_acc = 9'd0;
  wire stb_ready; wire [8:0] stb_c; wire stb_cv;
  wire [8:0] stb_new = {mean_start_a, ctr_pulse[0], stack_pulse[1:0], acq_pulse[4:0]};
  prim_bus_sync #(.W(9)) u_stb (.src_clk(aclk), .src_load(stb_acc != 9'd0), .src_data(stb_acc), .src_ready(stb_ready),
    .dst_clk(cclk), .dst_data(stb_c), .dst_valid(stb_cv));
  always @(posedge aclk) stb_acc <= ((stb_acc != 9'd0 && stb_ready) ? 9'd0 : stb_acc) | stb_new;
  // ---- readout start and stream tail into mclk
  reg ro_acc = 1'b0;
  wire ro_ready, ro_v; wire [0:0] ro_d;
  prim_bus_sync #(.W(1)) u_ro (.src_clk(aclk), .src_load(ro_acc), .src_data(1'b1), .src_ready(ro_ready),
    .dst_clk(mclk), .dst_data(ro_d), .dst_valid(ro_v));
  always @(posedge aclk) ro_acc <= ((ro_acc && ro_ready) ? 1'b0 : ro_acc) | ro_pulse[0];
  reg tail_acc = 1'b0;
  wire tail_ready, tail_v; wire [31:0] tail_m;
  prim_bus_sync #(.W(32)) u_tail (.src_clk(aclk), .src_load(tail_acc), .src_data(cw(OPL_STREAM_TAIL)),
    .src_ready(tail_ready), .dst_clk(mclk), .dst_data(tail_m), .dst_valid(tail_v));
  always @(posedge aclk) tail_acc <= ((tail_acc && tail_ready) ? 1'b0 : tail_acc) | tail_wr;

  // ======================================================================
  // cclk: the sample core
  wire c_arm = stb_cv && stb_c[0], c_stop = stb_cv && stb_c[1], c_force = stb_cv && stb_c[2];
  wire c_clear = stb_cv && stb_c[3], c_commit = stb_cv && (stb_c[4] || stb_c[0]);
  wire c_stk_clear = stb_cv && stb_c[5], c_stk_start = stb_cv && stb_c[6];
  wire c_ctr_clear = stb_cv && stb_c[7], c_mean_start = stb_cv && stb_c[8];
  // committed configuration
  reg [31:0] k_route = 32'h10, k_decim = 32'd1, k_mode = 32'd0, k_inv = 32'd0, k_gnd = 32'd0, k_gc01 = 32'h0800_0800,
             k_gc23 = 32'h0800_0800, k_pre = 32'd0, k_post = 32'd1000, k_segs = 32'd1, k_base = 32'd0, k_groups = 32'd0;
  reg [31:0] k_tctrl = 32'd1, k_open = 32'd0, k_close = 32'd1, k_abort = 32'd0, k_qm = 32'd0, k_qr = 32'd0,
             k_cond = 32'h20, k_ta = 32'd4, k_tah = 32'd0, k_tb = 32'd6, k_tbh = 32'd0, k_ho = 32'd160, k_hoh = 32'd0,
             k_auto = 32'd25000000, k_autoh = 32'd0, k_nth = 32'd1, k_pat = 32'd0;
  reg [767:0] k_cmp = 768'd0;            // 16 threshold words (CMP0_A_HI .. CMP3_B_LO)
  reg [31:0] k_pctrl = 32'd0, k_plines = 32'h10, k_psa = 32'h07C0_0800, k_psb = 32'h07C0_0800, k_ptim = 32'd868,
             k_popt = 32'h0002_8004, k_ppat = 32'd0, k_plen = 32'd0;
  reg committed = 1'b0;
  integer q;
  always @(posedge cclk) begin
    if (crst) committed <= 1'b0;
    else if (c_commit) begin
      committed <= 1'b1;
      k_route <= cw(OPL_ACQ_ROUTE); k_decim <= cw(OPL_ACQ_DECIM); k_mode <= cw(OPL_ACQ_MODE);
      k_inv <= cw(OPL_ACQ_INVERT); k_gnd <= cw(OPL_ACQ_GNDSUB); k_gc01 <= cw(OPL_ACQ_GNDCODE01);
      k_gc23 <= cw(OPL_ACQ_GNDCODE23); k_pre <= cw(OPL_ACQ_PRE); k_post <= cw(OPL_ACQ_POST);
      k_segs <= cw(OPL_ACQ_SEGMENTS); k_base <= cw(OPL_ACQ_REC_BASE); k_groups <= cw(OPL_ACQ_REC_GROUPS);
      k_tctrl <= cw(OPL_TRIG_CTRL); k_open <= cw(OPL_TRIG_OPEN); k_close <= cw(OPL_TRIG_CLOSE);
      k_abort <= cw(OPL_TRIG_ABORT); k_qm <= cw(OPL_TRIG_QUAL_MASK); k_qr <= cw(OPL_TRIG_QUAL_REQ);
      k_cond <= cw(OPL_TRIG_COND); k_ta <= cw(OPL_TRIG_TA_LO); k_tah <= cw(OPL_TRIG_TA_HI);
      k_tb <= cw(OPL_TRIG_TB_LO); k_tbh <= cw(OPL_TRIG_TB_HI); k_ho <= cw(OPL_TRIG_HOLDOFF_LO);
      k_hoh <= cw(OPL_TRIG_HOLDOFF_HI); k_auto <= cw(OPL_TRIG_AUTO_LO); k_autoh <= cw(OPL_TRIG_AUTO_HI);
      k_nth <= cw(OPL_TRIG_NTH); k_pat <= cw(OPL_TRIG_PATTERN);
      for (q = 0; q < 16; q = q + 1) k_cmp[48*q +: 48] <= {16'd0, cw(OPL_TRIG_CMP0_A_HI + q[13:0] * 16'd4)};
      k_pctrl <= cw(OPL_PROTO_CTRL); k_plines <= cw(OPL_PROTO_LINES); k_psa <= cw(OPL_PROTO_SLICE_A);
      k_psb <= cw(OPL_PROTO_SLICE_B); k_ptim <= cw(OPL_PROTO_TIMING); k_popt <= cw(OPL_PROTO_OPTS);
      k_ppat <= cw(OPL_PROTO_PATTERN); k_plen <= cw(OPL_PROTO_PATTERN_LEN);
    end
  end
  // thresholds per channel {b_lo, b_hi, a_lo, a_hi}, 12 bits each
  reg [191:0] thr;
  integer tc;
  always @(*) for (tc = 0; tc < 4; tc = tc + 1)
    thr[48*tc +: 48] = {k_cmp[48*(4*tc+3) +: 12], k_cmp[48*(4*tc+2) +: 12], k_cmp[48*(4*tc+1) +: 12], k_cmp[48*(4*tc) +: 12]};
  // route
  wire [383:0] xs; wire [7:0] vm; wire [2:0] tpl;
  acq_route u_route (.clk(cclk), .adc0_i(adc0_i), .adc0_q(adc0_q), .adc1_i(adc1_i), .adc1_q(adc1_q),
    .route(k_route[1:0]), .mask(k_route[7:4]), .invert(k_inv[3:0]), .gndsub(k_gnd[3:0]),
    .gndcode({k_gc23[27:16], k_gc23[11:0], k_gc01[27:16], k_gc01[11:0]}), .xs(xs), .vmask(vm), .tpl(tpl));
  // the trigger path's run enable and time (cleared by COMMIT)
  reg c_commit_d = 1'b0;
  always @(posedge cclk) c_commit_d <= c_commit;
  wire en = committed && !c_commit && !c_commit_d && !crst;
  reg [47:0] tclk = 48'd0;
  always @(posedge cclk) tclk <= en ? tclk + 48'd8 : 48'd0;
  // protocol engines
  wire p_match, ev_valid; wire [7:0] ev_kind, ev_data; wire [31:0] p_matches, p_events, p_errors;
  proto_core u_proto (.clk(cclk), .rst(crst), .en(en && k_pctrl[8]), .xs(xs), .vmask(vm), .engine(k_pctrl[3:0]),
    .a_ch(k_plines[1:0]), .b_ch(k_plines[5:4]), .inv_a(k_plines[8]), .inv_b(k_plines[9]),
    .slice_a({k_psa[27:16], k_psa[11:0]}), .slice_b({k_psb[27:16], k_psb[11:0]}), .timing(k_ptim[23:0]),
    .cpol(k_popt[0]), .cpha(k_popt[1]), .msb(k_popt[2]), .address(k_popt[14:8]), .any_address(k_popt[15]),
    .direction(k_popt[17:16]), .pattern(k_ppat), .pattern_len(k_plen[2:0]),
    .match(p_match), .event_valid(ev_valid), .event_kind(ev_kind), .event_data(ev_data),
    .match_count(p_matches), .event_count(p_events), .error_count(p_errors));
  // trigger engine
  wire armed, force_out, trig, t_auto, t_forced, frac_v; wire [47:0] t_time; wire [4:0] frac;
  wire [31:0] cands, accs; wire [255:0] ev_lanes; wire [47:0] ev_time;
  trig_core #(.L(8)) u_trig (.clk(cclk), .rst(crst), .en(en), .xs(xs), .vmask(vm), .tpl(tpl), .thresholds(thr),
    .engine(k_tctrl[3:0]), .open_mask(k_open), .close_mask(k_close), .abort_mask(k_abort),
    .qual_mask(k_qm[8:0]), .qual_req(k_qr[8:0]), .cond(k_cond[2:0]), .reach(k_cond[4]), .always_armed(k_cond[5]),
    .ta({k_tah[15:0], k_ta}), .tb({k_tbh[15:0], k_tb}), .nth(k_nth[15:0]), .pat_care(k_pat[3:0]),
    .pat_level(k_pat[7:4]), .pat_or(k_pat[8]), .pat_invert(k_pat[9]), .holdoff_en(k_tctrl[8]),
    .holdoff({k_hoh[15:0], k_ho}), .auto_en(k_tctrl[9]), .auto_clocks({k_autoh[15:0], k_auto}),
    .interp_en(k_tctrl[10]), .interp_ch(k_tctrl[13:12]), .interp_cmp(k_tctrl[14]), .interp_falling(k_tctrl[15]),
    .serial_match(p_match), .armed(armed), .force_pulse(force_out), .trig(trig), .trig_time(t_time),
    .trig_auto(t_auto), .trig_forced(t_forced), .frac(frac), .frac_valid(frac_v), .candidates(cands),
    .accepted(accs), .ev_lanes(ev_lanes), .ev_time(ev_time));
  // record path: the samples DLY clocks behind
  reg [383:0] xs_d [0:DLY-1];
  reg [7:0]   vm_d [0:DLY-1];
  reg [DLY-1:0] en_d = {DLY{1'b0}};
  integer di;
  always @(posedge cclk) begin
    xs_d[0] <= xs; vm_d[0] <= vm; en_d[0] <= en;
    for (di = 1; di < DLY; di = di + 1) begin xs_d[di] <= xs_d[di-1]; vm_d[di] <= vm_d[di-1]; en_d[di] <= en_d[di-1]; end
  end
  reg [47:0] trec = 48'd0;
  always @(posedge cclk) trec <= en_d[DLY-1] ? trec + 48'd8 : 48'd0;
  wire [3:0] d_nw; wire [511:0] d_words; wire [47:0] d_wfirst, d_t; wire [383:0] d_grp; wire [223:0] d_ph;
  wire [7:0] d_v;
  acq_decim u_decim (.clk(cclk), .restart(!en_d[DLY-1]), .xs(xs_d[DLY-1]), .v(vm_d[DLY-1]), .tpl(tpl), .t_clk(trec),
    .d(k_decim[27:0]), .mode(k_mode[1:0]), .nw(d_nw), .words(d_words), .wfirst(d_wfirst), .wt(), .grp(d_grp),
    .ph(d_ph), .t_out(d_t), .v_out(d_v));
  // the decimator's first output after a restart
  reg rec_was = 1'b0, d_first = 1'b0;
  always @(posedge cclk) begin rec_was <= en_d[DLY-1]; d_first <= en_d[DLY-1] && !rec_was; end
  wire sw_empty, sw_v; wire [13:0] sw_d;
  // the stacker takes each record's phase from the segment table (one clock to look it up)
  wire [6:0] stk_seg_flags;
  reg sw_v1 = 1'b0; reg [13:0] sw_d1 = 14'd0;
  always @(posedge cclk) begin sw_v1 <= sw_v; sw_d1 <= sw_d; end
  // acquisition
  wire [3:0] push, hiwater; wire [1663:0] push_data; wire [31:0] written_c;
  wire [2:0] a_state; wire [31:0] a_status, a_lost, a_pos, a_phase, a_words, a_segpos;
  wire [47:0] a_ttime, a_segtime; wire [4:0] a_frac; wire a_fok; wire [2:0] a_flags; wire [6:0] a_segflags;
  wire a_done, a_ovf;
  reg [9:0] seg_sel_c = 10'd0;
  acq_fsm u_fsm (.clk(cclk), .rst(crst), .arm(c_arm), .stop(c_stop), .force_in(c_force), .clear(c_clear),
    .mask(k_route[7:4]), .mode(k_mode[1:0]), .pre(k_pre), .post(k_post), .segments(k_segs[15:0] == 16'd0 ? 16'd1 : k_segs[15:0]),
    .rec_base(k_base), .rec_groups(k_groups[27:0]), .tpl(tpl),
    .d_first(d_first), .d_nw(d_nw), .d_words(d_words), .d_wfirst(d_wfirst), .d_grp(d_grp), .d_ph(d_ph), .d_t(d_t),
    .armed(armed), .force_out(force_out), .trig(trig), .trig_time(t_time), .trig_auto(t_auto),
    .trig_forced(t_forced), .interp_en(k_tctrl[10]), .frac(frac), .frac_valid(frac_v),
    .push(push), .push_data(push_data), .hiwater(hiwater), .groups_written(written_c),
    .state_out(a_state), .status(a_status), .rec_lost(a_lost), .t_trig(a_ttime), .trig_pos(a_pos),
    .trig_phase(a_phase), .trig_frac(a_frac), .frac_ok(a_fok), .trig_flags(a_flags), .words_out(a_words),
    .seg_sel(seg_sel_c), .seg_time(a_segtime), .seg_pos(a_segpos), .seg_flags(a_segflags),
    .seg_sel2(sw_d[9:0]), .seg_flags2(stk_seg_flags),
    .done(a_done), .overflow_pulse(a_ovf));
  // counter
  reg [31:0] ctr_ctrl_c = 32'd0, ctr_gate_c = 32'd250000000;
  wire [31:0] ctr_count, ctr_period; wire ctr_done;
  trig_counter #(.L(8)) u_ctr (.clk(cclk), .rst(crst), .en(ctr_ctrl_c[8] && en), .totalize(ctr_ctrl_c[9]),
    .sel(ctr_ctrl_c[4:0]), .gate(ctr_gate_c), .clear(c_ctr_clear), .ev(ev_lanes), .tpl(tpl), .t(ev_time),
    .count(ctr_count), .period(ctr_period), .done(ctr_done));
  // mean engine
  reg [15:0] mean_n_c = 16'd0;
  wire m_valid; wire [3:0] m_ovf; wire [191:0] m_sum; wire [31:0] m_cnt;
  mean_engine u_mean (.clk(cclk), .rst(crst), .start(c_mean_start), .n(mean_n_c), .xs(xs), .v(vm),
    .valid(m_valid), .overflow(m_ovf), .sum(m_sum), .count(m_cnt));
  // stream source
  reg [31:0] st_ctrl_c = 32'd0, st_decim_c = 32'd1;
  wire st_en_c = st_ctrl_c[0] && en;
  wire it_full; wire it_v; wire [111:0] it_d; wire [31:0] src_lost; wire src_ovf;
  stream_source u_ssrc (.clk(cclk), .rst(crst), .en(st_en_c), .payload(st_ctrl_c[5:4]), .mask(st_ctrl_c[11:8]),
    .d(st_decim_c[27:0]), .xs(xs), .v(vm), .tpl(tpl), .t_clk(tclk), .ev_valid(ev_valid), .ev_kind(ev_kind),
    .ev_data(ev_data), .out_valid(it_v), .out_data(it_d), .out_full(it_full), .lost(src_lost), .overflow(src_ovf));
  // stacker
  reg [15:0] stk_n_c = 16'd1; reg [4:0] stk_p_c = 5'd1;
  wire stk_busy, stk_run, stk_done, stk_beyond; wire [15:0] stk_recs;
  reg [12:0] stk_raddr = 13'd0; wire [31:0] stk_sum; wire [15:0] stk_cnt;
  stack_accum #(.AW(13)) u_stack (.clk(cclk), .rst(crst), .clear(c_stk_clear), .start(c_stk_start), .n(stk_n_c),
    // the stacker's phase is 0..15: a crossing on the trigger sample (16) bins with 15
    .phases(stk_p_c), .rec_start(sw_v1 && sw_d1[13:12] == 2'd0), .phi(stk_seg_flags[4] ? 4'd15 : stk_seg_flags[3:0]),
    .word_valid(sw_v1 && sw_d1[13:12] == 2'd1), .word(sw_d1[11:0]), .rec_end(sw_v1 && sw_d1[13:12] == 2'd2),
    .busy(stk_busy), .running(stk_run), .records(stk_recs), .done(stk_done), .beyond(stk_beyond),
    .rd_addr(stk_raddr), .rd_sum(stk_sum), .rd_count(stk_cnt));
  // quasi-static configuration taken at its block's enable or start
  wire [31:0] st_ctrl_a = cw(OPL_STREAM_CTRL);
  wire st_en_s;
  prim_sync #(.STAGES(2)) u_st_en (.clk(cclk), .d(st_ctrl_a[0]), .q(st_en_s));
  wire [31:0] ctr_ctrl_a = cw(OPL_COUNTER_CTRL);
  wire [31:0] stk_n_a = cw(OPL_STACK_N), stk_p_a = cw(OPL_STACK_PHASES), seg_sel_a = cw(OPL_ACQ_SEG_SEL);
  wire [31:0] st_beats_a = cw(OPL_STREAM_BEATS), st_blocks_a = cw(OPL_STREAM_BLOCKS), rec_groups_a = cw(OPL_ACQ_REC_GROUPS);
  wire ctr_en_s;
  prim_sync #(.STAGES(2)) u_ctr_en (.clk(cclk), .d(ctr_ctrl_a[8]), .q(ctr_en_s));
  always @(posedge cclk) begin
    if (!st_ctrl_c[0]) begin st_ctrl_c <= {st_ctrl_a[31:1], st_en_s}; st_decim_c <= cw(OPL_STREAM_DECIM); end
    else if (!st_en_s) st_ctrl_c[0] <= 1'b0;
    if (!ctr_ctrl_c[8]) begin ctr_ctrl_c <= {ctr_ctrl_a[31:9], ctr_en_s, ctr_ctrl_a[7:0]}; ctr_gate_c <= cw(OPL_COUNTER_GATE); end
    else if (!ctr_en_s) ctr_ctrl_c[8] <= 1'b0;
    if (c_stk_start) begin stk_n_c <= stk_n_a[15:0] == 16'd0 ? 16'd1 : stk_n_a[15:0];
      stk_p_c <= (stk_p_a[4:0] == 5'd0 || stk_p_a[4:0] > 5'd16) ? 5'd1 : stk_p_a[4:0]; end
    if (c_mean_start) mean_n_c <= mean_n;
  end
  wire stk_run_m;
  prim_sync #(.STAGES(2)) u_stk_run (.clk(mclk), .d(stk_run), .q(stk_run_m));

  // ======================================================================
  // mclk: record writer, readout, stream framer and writer
  wire [31:0] written_m; wire wr_axi_err;
  rec_writer u_wr (.cclk(cclk), .crst(crst), .push(push), .push_data(push_data), .hiwater(hiwater),
    .mclk(mclk), .mrst(mrst), .m_awaddr(hp0_awaddr), .m_awlen(hp0_awlen), .m_awvalid(hp0_awvalid),
    .m_awready(hp0_awready), .m_wdata(hp0_wdata), .m_wlast(hp0_wlast), .m_wvalid(hp0_wvalid), .m_wready(hp0_wready),
    .m_bresp(hp0_bresp), .m_bvalid(hp0_bvalid), .m_bready(hp0_bready), .written(written_m), .axi_err(wr_axi_err));
  // the written count back to cclk, reloaded whenever the crossing is free
  wire wc_ready, wc_v;
  prim_bus_sync #(.W(32)) u_wc (.src_clk(mclk), .src_load(1'b1), .src_data(written_m), .src_ready(wc_ready),
    .dst_clk(cclk), .dst_data(written_c), .dst_valid(wc_v));
  // readout
  wire [31:0] ro_src = cw(OPL_READOUT_SRC), ro_mode = cw(OPL_READOUT_MODE), ro_route = cw(OPL_ACQ_ROUTE);
  wire sw_full; wire s_v; wire [1:0] s_k; wire [11:0] s_dat;
  wire ro_busy, ro_err, ro_done, rd_axi_err;
  readout u_ro_eng (.clk(mclk), .rst(mrst), .start(ro_v), .ch(ro_src[1:0]), .seg(ro_src[25:16]),
    .first(cw(OPL_READOUT_START)), .count(cw(OPL_READOUT_COUNT)), .mode(ro_mode[1:0]), .bucket(ro_mode[31:8]),
    .rec_base(cw(OPL_ACQ_REC_BASE)), .rec_groups(rec_groups_a[27:0]), .mask(ro_route[7:4]),
    .to_stack(stk_run_m), .m_araddr(hp0_araddr), .m_arlen(hp0_arlen), .m_arvalid(hp0_arvalid),
    .m_arready(hp0_arready), .m_rdata(hp0_rdata), .m_rresp(hp0_rresp), .m_rlast(hp0_rlast), .m_rvalid(hp0_rvalid),
    .m_rready(hp0_rready), .m_tdata(dma_tdata), .m_tvalid(dma_tvalid), .m_tlast(dma_tlast), .m_tready(dma_tready),
    .s_valid(s_v), .s_kind(s_k), .s_data(s_dat), .s_full(sw_full), .busy(ro_busy), .error(ro_err),
    .done(ro_done), .axi_err(rd_axi_err));
  wire [4:0] sw_wl;
  prim_async_fifo #(.DW(14), .AW(4)) u_swf (.wr_clk(mclk), .wr_rst(mrst), .wr_en(s_v), .wr_data({s_k, s_dat}),
    .wr_full(), .wr_level(sw_wl), .rd_clk(cclk), .rd_rst(crst), .rd_en(!sw_empty), .rd_data(sw_d), .rd_valid(sw_v),
    .rd_empty(sw_empty), .rd_level());
  assign sw_full = sw_wl >= 5'd12;
  // stream: items cclk -> mclk, framer, HP1 writer
  wire [5:0] it_wl; wire it_empty; wire it_rv; wire [111:0] it_q;
  prim_async_fifo #(.DW(112), .AW(5)) u_itf (.wr_clk(cclk), .wr_rst(crst), .wr_en(it_v), .wr_data(it_d),
    .wr_full(), .wr_level(it_wl), .rd_clk(mclk), .rd_rst(mrst), .rd_en(!it_empty), .rd_data(it_q), .rd_valid(it_rv),
    .rd_empty(it_empty), .rd_level());
  assign it_full = it_wl >= 6'd28;
  wire st_en_m;
  prim_sync #(.STAGES(2)) u_st_en_m (.clk(mclk), .d(st_ctrl_a[0]), .q(st_en_m));
  reg [31:0] tail_q = 32'd0;
  always @(posedge mclk) if (mrst || !st_en_m) tail_q <= 32'd0; else if (tail_v) tail_q <= tail_m;
  wire fo_v, fo_last, fo_ready; wire [63:0] fo_d; wire [31:0] fo_a, f_head, f_seq, f_lost; wire f_ovf;
  stream_framer #(.AW(9)) u_framer (.clk(mclk), .rst(mrst), .en(st_en_m), .payload(st_ctrl_a[5:4]),
    .mask(st_ctrl_a[11:8]), .beats(st_beats_a[9:0]), .blocks(st_blocks_a[15:0]),
    .base(cw(OPL_STREAM_BASE)), .tail(tail_q), .in_valid(it_rv && st_en_m), .in_item(it_q[63:0]),
    .in_time(it_q[111:64]), .out_valid(fo_v), .out_ready(fo_ready), .out_data(fo_d), .out_addr(fo_a),
    .out_last(fo_last), .head(f_head), .seq(f_seq), .lost(f_lost), .overflow(f_ovf));
  wire [31:0] w_head; wire blk_done, st_axi_err;
  stream_writer u_sw (.clk(mclk), .rst(mrst || !st_en_m), .in_valid(fo_v), .in_ready(fo_ready), .in_data(fo_d),
    .in_addr(fo_a), .in_last(fo_last), .m_awaddr(hp1_awaddr), .m_awlen(hp1_awlen), .m_awvalid(hp1_awvalid),
    .m_awready(hp1_awready), .m_wdata(hp1_wdata), .m_wlast(hp1_wlast), .m_wvalid(hp1_wvalid), .m_wready(hp1_wready),
    .m_bresp(hp1_bresp), .m_bvalid(hp1_bvalid), .m_bready(hp1_bready), .head(w_head), .block_done(blk_done),
    .axi_err(st_axi_err));

  // ======================================================================
  // snapshots: every register read fetches both domains' status
  localparam integer WC = 23 * 32 + 48 + 229 + 1;   // cclk status words, stacker bin, mean engine, core alive
  localparam integer WM = 5 * 32;
  reg  rq_pend = 1'b0, got_c = 1'b0, got_m = 1'b0;
  reg  [5:0] rq_age = 6'd0;
  wire rq_c_ready, rq_m_ready;
  reg  rq_load = 1'b0;
  wire rq_cv, rq_mv; wire [12:0] rq_addr_c; wire [0:0] rq_m_d;
  assign hold = rd || rq_pend;
  always @(posedge aclk) begin
    rq_load <= 1'b0;
    if (arst) begin rq_pend <= 1'b0; got_c <= 1'b0; got_m <= 1'b0; end
    else if (rd && !rq_pend) begin rq_pend <= 1'b1; got_c <= 1'b0; got_m <= 1'b0; rq_age <= 6'd0; rq_load <= 1'b1; end
    else if (rq_pend) begin
      rq_age <= rq_age + 6'd1;
      if ((got_c && got_m) || rq_age == 6'd47) rq_pend <= 1'b0;
    end
    if (sc_v) got_c <= 1'b1;
    if (sm_v) got_m <= 1'b1;
  end
  prim_bus_sync #(.W(13)) u_rq_c (.src_clk(aclk), .src_load(rq_load), .src_data(ra[15:3]), .src_ready(rq_c_ready),
    .dst_clk(cclk), .dst_data(rq_addr_c), .dst_valid(rq_cv));
  prim_bus_sync #(.W(1)) u_rq_m (.src_clk(aclk), .src_load(rq_load), .src_data(1'b1), .src_ready(rq_m_ready),
    .dst_clk(mclk), .dst_data(rq_m_d), .dst_valid(rq_mv));
  // cclk: the stacker port gets the address, the snapshot leaves three clocks later
  reg [2:0] sc_wait = 3'd0;
  reg sc_load = 1'b0;
  wire sc_ready; wire [WC-1:0] sc_q;
  always @(posedge cclk) begin
    sc_load <= 1'b0;
    if (rq_cv) begin stk_raddr <= rq_addr_c; sc_wait <= 3'd4; end
    else if (sc_wait != 3'd0) begin sc_wait <= sc_wait - 3'd1; if (sc_wait == 3'd1) sc_load <= 1'b1; end
    seg_sel_c <= seg_sel_a[9:0];
  end
  wire [WC-1:0] snap_c = {
    !crst,                                                             // core alive
    m_cnt, m_sum, m_ovf, m_valid,                                      // mean engine
    stk_cnt, stk_sum,                                                  // stacker bin
    {stk_recs, 14'd0, stk_busy, stk_run},                              // STACK_STATUS
    src_lost,                                                          // stream items lost before the framer
    {31'd0, ctr_done}, ctr_period, ctr_count,                          // COUNTER
    p_errors, p_events, p_matches,                                     // PROTO
    accs, cands,                                                       // TRIG
    {22'd0, a_segflags[6:5], 3'd0, a_segflags[4:0]}, a_segpos, {16'd0, a_segtime[47:32]}, a_segtime[31:0],
    a_words, {29'd0, a_flags}, {23'd0, a_fok, 3'd0, a_frac}, a_phase, a_pos,
    {16'd0, a_ttime[47:32]}, a_ttime[31:0], a_lost, a_status };
  prim_bus_sync #(.W(WC)) u_sc (.src_clk(cclk), .src_load(sc_load), .src_data(snap_c), .src_ready(sc_ready),
    .dst_clk(aclk), .dst_data(sc_q), .dst_valid(sc_v));
  reg sm_load = 1'b0;
  wire sm_ready; wire [WM-1:0] sm_q;
  always @(posedge mclk) sm_load <= rq_mv;
  wire [WM-1:0] snap_m = {{30'd0, ro_err, ro_busy}, {30'd0, f_ovf, st_en_m}, f_lost, f_seq, w_head};
  prim_bus_sync #(.W(WM)) u_sm (.src_clk(mclk), .src_load(sm_load), .src_data(snap_m), .src_ready(sm_ready),
    .dst_clk(aclk), .dst_data(sm_q), .dst_valid(sm_v));
  // the status slots of opl_regs
  function [31:0] cwd(input integer i); cwd = sc_q[32*i +: 32]; endfunction
  wire [31:0] khz;
  always @(posedge aclk) begin
    if (sc_v) begin
      sts[32*STS_ACQ_STATUS +: 32] <= cwd(0);
      sts[32*STS_ACQ_REC_LOST +: 32] <= cwd(1);
      sts[32*STS_ACQ_TRIG_TIME_LO +: 32] <= cwd(2);
      sts[32*STS_ACQ_TRIG_TIME_HI +: 32] <= cwd(3);
      sts[32*STS_ACQ_TRIG_POS +: 32] <= cwd(4);
      sts[32*STS_ACQ_TRIG_PHASE +: 32] <= cwd(5);
      sts[32*STS_ACQ_TRIG_FRAC +: 32] <= cwd(6);
      sts[32*STS_ACQ_TRIG_FLAGS +: 32] <= cwd(7);
      sts[32*STS_ACQ_WORDS +: 32] <= cwd(8);
      sts[32*STS_ACQ_SEG_TIME_LO +: 32] <= cwd(9);
      sts[32*STS_ACQ_SEG_TIME_HI +: 32] <= cwd(10);
      sts[32*STS_ACQ_SEG_POS +: 32] <= cwd(11);
      sts[32*STS_ACQ_SEG_FLAGS +: 32] <= cwd(12);
      sts[32*STS_TRIG_CANDIDATES +: 32] <= cwd(13);
      sts[32*STS_TRIG_ACCEPTED +: 32] <= cwd(14);
      sts[32*STS_PROTO_MATCHES +: 32] <= cwd(15);
      sts[32*STS_PROTO_EVENTS +: 32] <= cwd(16);
      sts[32*STS_PROTO_ERRORS +: 32] <= cwd(17);
      sts[32*STS_COUNTER_COUNT +: 32] <= cwd(18);
      sts[32*STS_COUNTER_PERIOD +: 32] <= cwd(19);
      sts[32*STS_COUNTER_STATUS +: 32] <= cwd(20);
      src_lost_a <= cwd(21);
      sts[32*STS_STACK_STATUS +: 32] <= cwd(22);
      stk_bin_a <= sc_q[32*23 +: 48];
      {mean_cnt_a, mean_sum_a, mean_ovf_a, mean_valid_a} <= sc_q[32*23+48 +: 229];
      core_alive_a <= sc_q[WC-1];
    end
    if (sm_v) begin
      sts[32*STS_STREAM_HEAD +: 32] <= sm_q[31:0];
      sts[32*STS_STREAM_SEQ +: 32] <= sm_q[63:32];
      f_lost_a <= sm_q[95:64];
      sts[32*STS_STREAM_STATUS +: 32] <= sm_q[127:96] | {30'd0, src_ovf_a, 1'b0};
      sts[32*STS_READOUT_STATUS +: 32] <= sm_q[159:128];
    end
    sts[32*STS_STREAM_LOST +: 32] <= f_lost_a + src_lost_a;
    sts[32*STS_SYS_STATUS +: 32] <= {28'd0, 1'b1, core_alive_a, 2'b11};
    sts[32*STS_SYS_CCLK_KHZ +: 32] <= khz;
    sts[32*STS_SYS_ADC1_KHZ +: 32] <= khz;
  end
  // ---- the sample clock measured against aclk over 1 ms
  reg [31:0] ccnt = 32'd0, cgray = 32'd0;
  always @(posedge cclk) begin ccnt <= ccnt + 32'd1; cgray <= (ccnt + 32'd1) ^ ((ccnt + 32'd1) >> 1); end
  wire [31:0] cgray_a;
  prim_sync #(.STAGES(2), .W(32)) u_cg (.clk(aclk), .d(cgray), .q(cgray_a));
  reg [31:0] cbin, cprev = 32'd0, khz_r = 32'd0; reg [16:0] win = 17'd0;
  integer gb;
  always @(*) begin cbin[31] = cgray_a[31]; for (gb = 30; gb >= 0; gb = gb - 1) cbin[gb] = cbin[gb+1] ^ cgray_a[gb]; end
  always @(posedge aclk) begin
    if (win == 17'd99999) begin win <= 17'd0; khz_r <= cbin - cprev; cprev <= cbin; end
    else win <= win + 17'd1;
  end
  assign khz = khz_r;

  // ======================================================================
  // interrupt causes and faults into aclk
  wire [3:0] cause_a; wire [4:0] fault_a;
  prim_pulse_sync u_c0 (.src_clk(cclk), .src_pulse(a_done), .src_busy(), .dst_clk(aclk), .dst_pulse(cause_a[0]));
  prim_pulse_sync u_c1 (.src_clk(mclk), .src_pulse(blk_done), .src_busy(), .dst_clk(aclk), .dst_pulse(cause_a[1]));
  prim_pulse_sync u_c2 (.src_clk(cclk), .src_pulse(stk_done), .src_busy(), .dst_clk(aclk), .dst_pulse(cause_a[2]));
  prim_pulse_sync u_c3 (.src_clk(mclk), .src_pulse(ro_done), .src_busy(), .dst_clk(aclk), .dst_pulse(cause_a[3]));
  assign cause_set = cause_a;
  reg [1:0] lvl_c = 2'd0; reg [2:0] lvl_m = 3'd0;
  always @(posedge cclk) lvl_c <= {src_ovf, 1'b0};
  always @(posedge mclk) lvl_m <= {rd_axi_err, wr_axi_err || st_axi_err, f_ovf};
  prim_pulse_sync u_f0 (.src_clk(cclk), .src_pulse(a_ovf), .src_busy(), .dst_clk(aclk), .dst_pulse(fault_a[0]));
  wire [1:0] fs_c; wire [2:0] fs_m;
  prim_sync #(.STAGES(2), .W(2)) u_fsync_c (.clk(aclk), .d(lvl_c), .q(fs_c));
  prim_sync #(.STAGES(2), .W(3)) u_fsync_m (.clk(aclk), .d(lvl_m), .q(fs_m));
  always @(posedge aclk) src_ovf_a <= fs_c[1];
  assign fault_a[1] = fs_c[1] || fs_m[0];
  assign fault_a[2] = 1'b0;
  assign fault_a[3] = fs_m[1];
  assign fault_a[4] = fs_m[2];
  assign fault_set = fault_a;
endmodule
