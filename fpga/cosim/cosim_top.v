// ENGMODEL-OWNER-UNIT: FU-HD-PL-TESTS
`timescale 1ns/1ps
// cosim_top -- opl_core as the co-simulation drives it (fpga/cosim): the
// core's ports unchanged, plus read-only probes into it that the harness
// logs and samples by (the committed route, the trigger path's run enable,
// accepted triggers with their time and fraction). Simulation only.
// TRLC-LINKS: REQ-HD-015
module cosim_top (
  input  wire          aclk,
  input  wire          mclk,
  input  wire          cclk,
  input  wire          aresetn,
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
  input  wire [47:0]   adc0_i,
  input  wire [47:0]   adc0_q,
  input  wire [47:0]   adc1_i,
  input  wire [47:0]   adc1_q,
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
  output wire [63:0]   dma_tdata,
  output wire          dma_tvalid,
  output wire          dma_tlast,
  input  wire          dma_tready,
  output wire          irq,
  // probes
  output wire [7:0]    dbg_route,
  output wire          dbg_en,
  output wire          dbg_trig,
  output wire [47:0]   dbg_trig_time,
  output wire          dbg_trig_auto,
  output wire          dbg_trig_forced,
  output wire          dbg_frac_valid,
  output wire [4:0]    dbg_frac,
  output wire [2:0]    dbg_acq_state
);
  // the co-simulation's image name, "cosm": no unit build carries it
  opl_core #(.IMAGE(32'h6D736F63)) u_core (.*);
  assign dbg_route = u_core.k_route[7:0];
  assign dbg_en = u_core.en;
  assign dbg_trig = u_core.trig;
  assign dbg_trig_time = u_core.t_time;
  assign dbg_trig_auto = u_core.t_auto;
  assign dbg_trig_forced = u_core.t_forced;
  assign dbg_frac_valid = u_core.frac_v;
  assign dbg_frac = u_core.frac;
  assign dbg_acq_state = u_core.a_state;
endmodule
