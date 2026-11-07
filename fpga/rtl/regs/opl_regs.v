// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// opl_regs -- the open plane's register file (spec 20 §10), in the aclk
// domain. Addresses, read/write masks and reset values come from the
// generated include (fpga/regs/open-pl.yml); the decode is the lists below.
//   rw   stored here, masked to the fields' bits, presented on cfg
//        (word w of the plane at cfg[32w +: 32]); consumers in other clock
//        domains take them through the commit crossing (spec 20 §3.1).
//   r    the identity block (constants, uptime, DNA) or a status slot of sts.
//   w1   strobes: the written word appears for one clock on *_pulse.
//   w1c  SYS_FAULT: sticky bits set by fault_set, cleared by writing 1.
//   rc   SYS_IRQ_TAKEN.
// rd_data is combinational on rd_addr; read side effects happen on rd.
// TRLC-LINKS: REQ-HD-015
module opl_regs #(
  parameter [31:0] BUILD = 32'h0,
  parameter [31:0] IMAGE = 32'h6C6C7566,   // "full"
  parameter integer NW   = 1024           // 4 KiB of registers
) (
  input  wire          clk,
  input  wire          rst,
  input  wire          wr,
  input  wire [15:0]   wr_addr,
  input  wire [31:0]   wr_data,
  input  wire [3:0]    wr_strb,
  input  wire          rd,
  input  wire [15:0]   rd_addr,
  output reg  [31:0]   rd_data,
  output wire [NW*32-1:0] cfg,
  input  wire [32*32-1:0] sts,
  input  wire [95:0]   dna,
  output reg  [31:0]   acq_ctrl_pulse = 32'd0,
  output reg  [31:0]   stack_ctrl_pulse = 32'd0,
  output reg  [31:0]   readout_ctrl_pulse = 32'd0,
  output reg  [31:0]   counter_clear_pulse = 32'd0,
  output reg           stream_tail_written = 1'b0,
  input  wire [4:0]    fault_set,
  input  wire [3:0]    irq_cause_set,
  input  wire          irq_take,           // compat ACQ 0x160 read
  output wire          irq
);
`include "open_pl_regs.vh"
`include "regs/opl_sts.vh"
  // ---- the read/write registers: mask and reset by address
  function [63:0] rw_info(input [15:0] a);   // {mask, reset}
    case (a)
      OPL_ID_SCRATCH: rw_info = {OPL_ID_SCRATCH_RW, OPL_ID_SCRATCH_RESET & OPL_ID_SCRATCH_RW};
      OPL_SYS_CTRL: rw_info = {OPL_SYS_CTRL_RW, OPL_SYS_CTRL_RESET & OPL_SYS_CTRL_RW};
      OPL_SYS_IRQ_EN: rw_info = {OPL_SYS_IRQ_EN_RW, OPL_SYS_IRQ_EN_RESET & OPL_SYS_IRQ_EN_RW};
      OPL_ACQ_ROUTE: rw_info = {OPL_ACQ_ROUTE_RW, OPL_ACQ_ROUTE_RESET & OPL_ACQ_ROUTE_RW};
      OPL_ACQ_DECIM: rw_info = {OPL_ACQ_DECIM_RW, OPL_ACQ_DECIM_RESET & OPL_ACQ_DECIM_RW};
      OPL_ACQ_MODE: rw_info = {OPL_ACQ_MODE_RW, OPL_ACQ_MODE_RESET & OPL_ACQ_MODE_RW};
      OPL_ACQ_INVERT: rw_info = {OPL_ACQ_INVERT_RW, OPL_ACQ_INVERT_RESET & OPL_ACQ_INVERT_RW};
      OPL_ACQ_GNDSUB: rw_info = {OPL_ACQ_GNDSUB_RW, OPL_ACQ_GNDSUB_RESET & OPL_ACQ_GNDSUB_RW};
      OPL_ACQ_GNDCODE01: rw_info = {OPL_ACQ_GNDCODE01_RW, OPL_ACQ_GNDCODE01_RESET & OPL_ACQ_GNDCODE01_RW};
      OPL_ACQ_GNDCODE23: rw_info = {OPL_ACQ_GNDCODE23_RW, OPL_ACQ_GNDCODE23_RESET & OPL_ACQ_GNDCODE23_RW};
      OPL_ACQ_SKEW: rw_info = {OPL_ACQ_SKEW_RW, OPL_ACQ_SKEW_RESET & OPL_ACQ_SKEW_RW};
      OPL_ACQ_PRE: rw_info = {OPL_ACQ_PRE_RW, OPL_ACQ_PRE_RESET & OPL_ACQ_PRE_RW};
      OPL_ACQ_POST: rw_info = {OPL_ACQ_POST_RW, OPL_ACQ_POST_RESET & OPL_ACQ_POST_RW};
      OPL_ACQ_SEGMENTS: rw_info = {OPL_ACQ_SEGMENTS_RW, OPL_ACQ_SEGMENTS_RESET & OPL_ACQ_SEGMENTS_RW};
      OPL_ACQ_REC_BASE: rw_info = {OPL_ACQ_REC_BASE_RW, OPL_ACQ_REC_BASE_RESET & OPL_ACQ_REC_BASE_RW};
      OPL_ACQ_REC_GROUPS: rw_info = {OPL_ACQ_REC_GROUPS_RW, OPL_ACQ_REC_GROUPS_RESET & OPL_ACQ_REC_GROUPS_RW};
      OPL_ACQ_SEG_SEL: rw_info = {OPL_ACQ_SEG_SEL_RW, OPL_ACQ_SEG_SEL_RESET & OPL_ACQ_SEG_SEL_RW};
      OPL_TRIG_CTRL: rw_info = {OPL_TRIG_CTRL_RW, OPL_TRIG_CTRL_RESET & OPL_TRIG_CTRL_RW};
      OPL_TRIG_OPEN: rw_info = {OPL_TRIG_OPEN_RW, OPL_TRIG_OPEN_RESET & OPL_TRIG_OPEN_RW};
      OPL_TRIG_CLOSE: rw_info = {OPL_TRIG_CLOSE_RW, OPL_TRIG_CLOSE_RESET & OPL_TRIG_CLOSE_RW};
      OPL_TRIG_ABORT: rw_info = {OPL_TRIG_ABORT_RW, OPL_TRIG_ABORT_RESET & OPL_TRIG_ABORT_RW};
      OPL_TRIG_QUAL_MASK: rw_info = {OPL_TRIG_QUAL_MASK_RW, OPL_TRIG_QUAL_MASK_RESET & OPL_TRIG_QUAL_MASK_RW};
      OPL_TRIG_QUAL_REQ: rw_info = {OPL_TRIG_QUAL_REQ_RW, OPL_TRIG_QUAL_REQ_RESET & OPL_TRIG_QUAL_REQ_RW};
      OPL_TRIG_COND: rw_info = {OPL_TRIG_COND_RW, OPL_TRIG_COND_RESET & OPL_TRIG_COND_RW};
      OPL_TRIG_TA_LO: rw_info = {OPL_TRIG_TA_LO_RW, OPL_TRIG_TA_LO_RESET & OPL_TRIG_TA_LO_RW};
      OPL_TRIG_TA_HI: rw_info = {OPL_TRIG_TA_HI_RW, OPL_TRIG_TA_HI_RESET & OPL_TRIG_TA_HI_RW};
      OPL_TRIG_TB_LO: rw_info = {OPL_TRIG_TB_LO_RW, OPL_TRIG_TB_LO_RESET & OPL_TRIG_TB_LO_RW};
      OPL_TRIG_TB_HI: rw_info = {OPL_TRIG_TB_HI_RW, OPL_TRIG_TB_HI_RESET & OPL_TRIG_TB_HI_RW};
      OPL_TRIG_HOLDOFF_LO: rw_info = {OPL_TRIG_HOLDOFF_LO_RW, OPL_TRIG_HOLDOFF_LO_RESET & OPL_TRIG_HOLDOFF_LO_RW};
      OPL_TRIG_HOLDOFF_HI: rw_info = {OPL_TRIG_HOLDOFF_HI_RW, OPL_TRIG_HOLDOFF_HI_RESET & OPL_TRIG_HOLDOFF_HI_RW};
      OPL_TRIG_AUTO_LO: rw_info = {OPL_TRIG_AUTO_LO_RW, OPL_TRIG_AUTO_LO_RESET & OPL_TRIG_AUTO_LO_RW};
      OPL_TRIG_AUTO_HI: rw_info = {OPL_TRIG_AUTO_HI_RW, OPL_TRIG_AUTO_HI_RESET & OPL_TRIG_AUTO_HI_RW};
      OPL_TRIG_NTH: rw_info = {OPL_TRIG_NTH_RW, OPL_TRIG_NTH_RESET & OPL_TRIG_NTH_RW};
      OPL_TRIG_PATTERN: rw_info = {OPL_TRIG_PATTERN_RW, OPL_TRIG_PATTERN_RESET & OPL_TRIG_PATTERN_RW};
      OPL_TRIG_VIDEO: rw_info = {OPL_TRIG_VIDEO_RW, OPL_TRIG_VIDEO_RESET & OPL_TRIG_VIDEO_RW};
      OPL_TRIG_CMP0_A_HI: rw_info = {OPL_TRIG_CMP0_A_HI_RW, OPL_TRIG_CMP0_A_HI_RESET & OPL_TRIG_CMP0_A_HI_RW};
      OPL_TRIG_CMP0_A_LO: rw_info = {OPL_TRIG_CMP0_A_LO_RW, OPL_TRIG_CMP0_A_LO_RESET & OPL_TRIG_CMP0_A_LO_RW};
      OPL_TRIG_CMP0_B_HI: rw_info = {OPL_TRIG_CMP0_B_HI_RW, OPL_TRIG_CMP0_B_HI_RESET & OPL_TRIG_CMP0_B_HI_RW};
      OPL_TRIG_CMP0_B_LO: rw_info = {OPL_TRIG_CMP0_B_LO_RW, OPL_TRIG_CMP0_B_LO_RESET & OPL_TRIG_CMP0_B_LO_RW};
      OPL_TRIG_CMP1_A_HI: rw_info = {OPL_TRIG_CMP1_A_HI_RW, OPL_TRIG_CMP1_A_HI_RESET & OPL_TRIG_CMP1_A_HI_RW};
      OPL_TRIG_CMP1_A_LO: rw_info = {OPL_TRIG_CMP1_A_LO_RW, OPL_TRIG_CMP1_A_LO_RESET & OPL_TRIG_CMP1_A_LO_RW};
      OPL_TRIG_CMP1_B_HI: rw_info = {OPL_TRIG_CMP1_B_HI_RW, OPL_TRIG_CMP1_B_HI_RESET & OPL_TRIG_CMP1_B_HI_RW};
      OPL_TRIG_CMP1_B_LO: rw_info = {OPL_TRIG_CMP1_B_LO_RW, OPL_TRIG_CMP1_B_LO_RESET & OPL_TRIG_CMP1_B_LO_RW};
      OPL_TRIG_CMP2_A_HI: rw_info = {OPL_TRIG_CMP2_A_HI_RW, OPL_TRIG_CMP2_A_HI_RESET & OPL_TRIG_CMP2_A_HI_RW};
      OPL_TRIG_CMP2_A_LO: rw_info = {OPL_TRIG_CMP2_A_LO_RW, OPL_TRIG_CMP2_A_LO_RESET & OPL_TRIG_CMP2_A_LO_RW};
      OPL_TRIG_CMP2_B_HI: rw_info = {OPL_TRIG_CMP2_B_HI_RW, OPL_TRIG_CMP2_B_HI_RESET & OPL_TRIG_CMP2_B_HI_RW};
      OPL_TRIG_CMP2_B_LO: rw_info = {OPL_TRIG_CMP2_B_LO_RW, OPL_TRIG_CMP2_B_LO_RESET & OPL_TRIG_CMP2_B_LO_RW};
      OPL_TRIG_CMP3_A_HI: rw_info = {OPL_TRIG_CMP3_A_HI_RW, OPL_TRIG_CMP3_A_HI_RESET & OPL_TRIG_CMP3_A_HI_RW};
      OPL_TRIG_CMP3_A_LO: rw_info = {OPL_TRIG_CMP3_A_LO_RW, OPL_TRIG_CMP3_A_LO_RESET & OPL_TRIG_CMP3_A_LO_RW};
      OPL_TRIG_CMP3_B_HI: rw_info = {OPL_TRIG_CMP3_B_HI_RW, OPL_TRIG_CMP3_B_HI_RESET & OPL_TRIG_CMP3_B_HI_RW};
      OPL_TRIG_CMP3_B_LO: rw_info = {OPL_TRIG_CMP3_B_LO_RW, OPL_TRIG_CMP3_B_LO_RESET & OPL_TRIG_CMP3_B_LO_RW};
      OPL_PROTO_CTRL: rw_info = {OPL_PROTO_CTRL_RW, OPL_PROTO_CTRL_RESET & OPL_PROTO_CTRL_RW};
      OPL_PROTO_LINES: rw_info = {OPL_PROTO_LINES_RW, OPL_PROTO_LINES_RESET & OPL_PROTO_LINES_RW};
      OPL_PROTO_SLICE_A: rw_info = {OPL_PROTO_SLICE_A_RW, OPL_PROTO_SLICE_A_RESET & OPL_PROTO_SLICE_A_RW};
      OPL_PROTO_SLICE_B: rw_info = {OPL_PROTO_SLICE_B_RW, OPL_PROTO_SLICE_B_RESET & OPL_PROTO_SLICE_B_RW};
      OPL_PROTO_TIMING: rw_info = {OPL_PROTO_TIMING_RW, OPL_PROTO_TIMING_RESET & OPL_PROTO_TIMING_RW};
      OPL_PROTO_OPTS: rw_info = {OPL_PROTO_OPTS_RW, OPL_PROTO_OPTS_RESET & OPL_PROTO_OPTS_RW};
      OPL_PROTO_PATTERN: rw_info = {OPL_PROTO_PATTERN_RW, OPL_PROTO_PATTERN_RESET & OPL_PROTO_PATTERN_RW};
      OPL_PROTO_PATTERN_LEN: rw_info = {OPL_PROTO_PATTERN_LEN_RW, OPL_PROTO_PATTERN_LEN_RESET & OPL_PROTO_PATTERN_LEN_RW};
      OPL_STREAM_CTRL: rw_info = {OPL_STREAM_CTRL_RW, OPL_STREAM_CTRL_RESET & OPL_STREAM_CTRL_RW};
      OPL_STREAM_DECIM: rw_info = {OPL_STREAM_DECIM_RW, OPL_STREAM_DECIM_RESET & OPL_STREAM_DECIM_RW};
      OPL_STREAM_BASE: rw_info = {OPL_STREAM_BASE_RW, OPL_STREAM_BASE_RESET & OPL_STREAM_BASE_RW};
      OPL_STREAM_BLOCKS: rw_info = {OPL_STREAM_BLOCKS_RW, OPL_STREAM_BLOCKS_RESET & OPL_STREAM_BLOCKS_RW};
      OPL_STREAM_BEATS: rw_info = {OPL_STREAM_BEATS_RW, OPL_STREAM_BEATS_RESET & OPL_STREAM_BEATS_RW};
      OPL_STREAM_TAIL: rw_info = {OPL_STREAM_TAIL_RW, OPL_STREAM_TAIL_RESET & OPL_STREAM_TAIL_RW};
      OPL_STACK_N: rw_info = {OPL_STACK_N_RW, OPL_STACK_N_RESET & OPL_STACK_N_RW};
      OPL_STACK_PHASES: rw_info = {OPL_STACK_PHASES_RW, OPL_STACK_PHASES_RESET & OPL_STACK_PHASES_RW};
      OPL_READOUT_SRC: rw_info = {OPL_READOUT_SRC_RW, OPL_READOUT_SRC_RESET & OPL_READOUT_SRC_RW};
      OPL_READOUT_START: rw_info = {OPL_READOUT_START_RW, OPL_READOUT_START_RESET & OPL_READOUT_START_RW};
      OPL_READOUT_COUNT: rw_info = {OPL_READOUT_COUNT_RW, OPL_READOUT_COUNT_RESET & OPL_READOUT_COUNT_RW};
      OPL_READOUT_MODE: rw_info = {OPL_READOUT_MODE_RW, OPL_READOUT_MODE_RESET & OPL_READOUT_MODE_RW};
      OPL_COUNTER_CTRL: rw_info = {OPL_COUNTER_CTRL_RW, OPL_COUNTER_CTRL_RESET & OPL_COUNTER_CTRL_RW};
      OPL_COUNTER_GATE: rw_info = {OPL_COUNTER_GATE_RW, OPL_COUNTER_GATE_RESET & OPL_COUNTER_GATE_RW};
      default: rw_info = 64'd0;
    endcase
  endfunction
  wire [31:0] strb_mask = {{8{wr_strb[3]}}, {8{wr_strb[2]}}, {8{wr_strb[1]}}, {8{wr_strb[0]}}};
  reg [31:0] word [0:NW-1];
  genvar g;
  generate for (g = 0; g < NW; g = g + 1) begin : g_cfg
    localparam [63:0] INFO = rw_info(g * 4);
    if (INFO[63:32] != 32'd0) begin : g_rw
      initial word[g] = INFO[31:0];
      always @(posedge clk)
        if (rst) word[g] <= INFO[31:0];
        else if (wr && wr_addr[15:2] == g) word[g] <= (word[g] & ~(INFO[63:32] & strb_mask)) | (wr_data & INFO[63:32] & strb_mask);
      assign cfg[32*g +: 32] = word[g];
    end else begin : g_none
      assign cfg[32*g +: 32] = 32'd0;
    end
  end endgenerate
  wire [31:0] cfg_rd = cfg[32*rd_addr[11:2] +: 32];
  // ---- strobes, faults, interrupts, uptime
  reg [4:0] fault = 5'd0;
  reg [3:0] pending = 4'd0, taken = 4'd0;
  reg [63:0] uptime = 64'd0;
  reg [31:0] uptime_hi = 32'd0;
  wire [3:0] irq_en = cfg[32*(OPL_SYS_IRQ_EN/4) +: 4];
  assign irq = |(pending & irq_en);
  wire take = irq_take || (wr && wr_addr == OPL_SYS_IRQ_ACK && wr_data[0]);
  always @(posedge clk) begin
    acq_ctrl_pulse <= 32'd0; stack_ctrl_pulse <= 32'd0; readout_ctrl_pulse <= 32'd0;
    counter_clear_pulse <= 32'd0; stream_tail_written <= 1'b0;
    uptime <= uptime + 1'b1;
    if (rst) begin
      fault <= 5'd0; pending <= 4'd0; taken <= 4'd0;
    end else begin
      if (wr) case (wr_addr)
        OPL_ACQ_CTRL:      acq_ctrl_pulse <= wr_data;
        OPL_STACK_CTRL:    stack_ctrl_pulse <= wr_data;
        OPL_READOUT_CTRL:  readout_ctrl_pulse <= wr_data;
        OPL_COUNTER_CLEAR: counter_clear_pulse <= wr_data;
        OPL_STREAM_TAIL:   stream_tail_written <= 1'b1;
        default: ;
      endcase
      fault <= (fault & ~((wr && wr_addr == OPL_SYS_FAULT) ? wr_data[4:0] : 5'd0)) | fault_set;
      if (take) begin
        taken <= taken | pending | irq_cause_set;
        pending <= 4'd0;
      end else begin
        pending <= pending | irq_cause_set;
        if (rd && rd_addr == OPL_SYS_IRQ_TAKEN) taken <= 4'd0;
      end
      if (rd && rd_addr == OPL_ID_UPTIME_LO) uptime_hi <= uptime[63:32];
    end
  end
  // ---- read mux
  function [31:0] st(input integer slot); st = sts[32*slot +: 32]; endfunction
  always @(*) begin
    case (rd_addr)
      OPL_ID_MAGIC:       rd_data = OPL_ID_MAGIC_RESET;
      OPL_ID_ABI:         rd_data = {OPL_ABI_MAJOR, OPL_ABI_MINOR};
      OPL_ID_IMAGE:       rd_data = IMAGE;
      OPL_ID_BUILD:       rd_data = BUILD;
      OPL_ID_MANIFEST_LO: rd_data = OPL_MANIFEST[31:0];
      OPL_ID_MANIFEST_HI: rd_data = OPL_MANIFEST[63:32];
      OPL_ID_CAPS:        rd_data = OPL_ID_CAPS_RESET;
      OPL_ID_GEOMETRY:    rd_data = OPL_ID_GEOMETRY_RESET;
      OPL_ID_UPTIME_LO:   rd_data = uptime[31:0];
      OPL_ID_UPTIME_HI:   rd_data = uptime_hi;
      OPL_ID_DNA0:        rd_data = dna[31:0];
      OPL_ID_DNA1:        rd_data = dna[63:32];
      OPL_ID_DNA2:        rd_data = dna[95:64];
      OPL_SYS_STATUS:     rd_data = st(STS_SYS_STATUS);
      OPL_SYS_CCLK_KHZ:   rd_data = st(STS_SYS_CCLK_KHZ);
      OPL_SYS_ADC1_KHZ:   rd_data = st(STS_SYS_ADC1_KHZ);
      OPL_SYS_FAULT:      rd_data = {27'd0, fault};
      OPL_SYS_IRQ_STATUS: rd_data = {28'd0, pending};
      OPL_SYS_IRQ_TAKEN:  rd_data = {28'd0, taken};
      OPL_ACQ_STATUS:     rd_data = st(STS_ACQ_STATUS);
      OPL_ACQ_REC_LOST:   rd_data = st(STS_ACQ_REC_LOST);
      OPL_ACQ_TRIG_TIME_LO: rd_data = st(STS_ACQ_TRIG_TIME_LO);
      OPL_ACQ_TRIG_TIME_HI: rd_data = st(STS_ACQ_TRIG_TIME_HI);
      OPL_ACQ_TRIG_POS:   rd_data = st(STS_ACQ_TRIG_POS);
      OPL_ACQ_TRIG_PHASE: rd_data = st(STS_ACQ_TRIG_PHASE);
      OPL_ACQ_TRIG_FRAC:  rd_data = st(STS_ACQ_TRIG_FRAC);
      OPL_ACQ_TRIG_FLAGS: rd_data = st(STS_ACQ_TRIG_FLAGS);
      OPL_ACQ_WORDS:      rd_data = st(STS_ACQ_WORDS);
      OPL_ACQ_SEG_TIME_LO: rd_data = st(STS_ACQ_SEG_TIME_LO);
      OPL_ACQ_SEG_TIME_HI: rd_data = st(STS_ACQ_SEG_TIME_HI);
      OPL_ACQ_SEG_POS:    rd_data = st(STS_ACQ_SEG_POS);
      OPL_ACQ_SEG_FLAGS:  rd_data = st(STS_ACQ_SEG_FLAGS);
      OPL_TRIG_CANDIDATES: rd_data = st(STS_TRIG_CANDIDATES);
      OPL_TRIG_ACCEPTED:  rd_data = st(STS_TRIG_ACCEPTED);
      OPL_PROTO_MATCHES:  rd_data = st(STS_PROTO_MATCHES);
      OPL_PROTO_EVENTS:   rd_data = st(STS_PROTO_EVENTS);
      OPL_PROTO_ERRORS:   rd_data = st(STS_PROTO_ERRORS);
      OPL_STREAM_HEAD:    rd_data = st(STS_STREAM_HEAD);
      OPL_STREAM_SEQ:     rd_data = st(STS_STREAM_SEQ);
      OPL_STREAM_LOST:    rd_data = st(STS_STREAM_LOST);
      OPL_STREAM_STATUS:  rd_data = st(STS_STREAM_STATUS);
      OPL_STACK_STATUS:   rd_data = st(STS_STACK_STATUS);
      OPL_READOUT_STATUS: rd_data = st(STS_READOUT_STATUS);
      OPL_COUNTER_COUNT:  rd_data = st(STS_COUNTER_COUNT);
      OPL_COUNTER_PERIOD: rd_data = st(STS_COUNTER_PERIOD);
      OPL_COUNTER_STATUS: rd_data = st(STS_COUNTER_STATUS);
      default:            rd_data = (rd_addr < 16'h1000) ? cfg_rd : 32'd0;
    endcase
  end
endmodule
