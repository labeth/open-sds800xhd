// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// compat_acq -- the subset of the vendor ACQ plane (spec 02) the board's
// front end, ADC bring-up and the unit's kernel modules use against the open
// image (spec 20 §10.4). Word-indexed (byte address / 4), 16-bit data in bits
// 15:0. Every other index reads 0 and ignores writes.
// TRLC-LINKS: REQ-HD-015
module compat_acq #(parameter [15:0] ABI_MAJOR = 16'd1, parameter [15:0] ABI_MINOR = 16'd0,
                    parameter [7:0] IMAGE_INDEX = 8'd0) (
  input  wire        clk,
  input  wire        rst,
  input  wire        wr,
  input  wire [15:0] wr_addr,
  input  wire [31:0] wr_data,
  input  wire        rd,
  input  wire [15:0] rd_addr,
  output reg  [31:0] rd_data,
  // ADC capture control (spec 12 §6.4)
  output reg  [1:0]  mmcm_reset = 2'b00,     // 0x01F W bits 1:0, a pulse 1 then 0 by software
  input  wire        mmcm0_locked,           // 0x01F R bit 8
  input  wire        core_alive,             // 0x01F R bit 12 (core clock running, ADC1 FIFO aligned)
  input  wire        mmcm1_locked,           // 0x01F R bit 13
  output reg         iserdes_reset = 1'b1,   // 0x04F
  output reg         phase_apply = 1'b0,     // 0x07A 0 -> 1 (either interface form)
  output reg  [2:0]  phase = 3'd0,           // 0x07B
  input  wire [1:0]  data_ready,             // 0x07C
  output reg  [1:0]  err_clear = 2'b00,      // 0x07D bits 0/1 rising
  output reg         phase_cal = 1'b0,       // 0x07D bit 8
  input  wire [1:0]  adc_error,              // 0x07D R bits 5:4
  output reg         pll_sync = 1'b0,        // 0x1FA bit 8
  output reg         adc_type = 1'b0,        // 0x1FB
  input  wire        rec_ready,              // 0x021 bit 0
  // offset DACs (spec 02 §7): codes CH1..CH4 and a pulse when one changes
  output reg  [15:0] dac_ctrl = 16'd0,       // 0x141
  output reg  [63:0] dac_code = 64'd0,       // {CH4, CH3, CH2, CH1}
  output reg  [3:0]  dac_write = 4'd0,
  // interrupt acknowledge (0x160 read) and the scope ID (0x180-0x185)
  output reg         irq_take = 1'b0,
  output reg         dna_read = 1'b0,
  input  wire [95:0] dna,
  input  wire        dna_ready,
  // mean engine (0x1AE-0x1B6, spec 10 §5)
  output reg  [15:0] mean_n = 16'd0,
  output reg  [1:0]  mean_ch = 2'd0,
  output reg         mean_start = 1'b0,
  input  wire        mean_valid,
  input  wire [3:0]  mean_overflow,
  input  wire [47:0] mean_sum,
  input  wire [31:0] mean_count
);
  wire [13:0] wi = wr_addr[15:2];
  wire [13:0] ri = rd_addr[15:2];
  wire [15:0] d = wr_data[15:0];
  // previous values of the strobe registers' bits (the strobes act on 0 -> 1);
  // 0x142-0x145 are accepted and have no effect
  reg r7a0 = 1'b0, r7a4 = 1'b0, r180 = 1'b0, r1ae = 1'b0;
  reg [1:0] r7d = 2'b00;
  always @(posedge clk) begin
    phase_apply <= 1'b0; err_clear <= 2'b00; dac_write <= 4'd0; irq_take <= 1'b0;
    dna_read <= 1'b0; mean_start <= 1'b0;
    if (rst) begin
      mmcm_reset <= 2'b00; iserdes_reset <= 1'b1; phase <= 3'd0; phase_cal <= 1'b0; pll_sync <= 1'b0;
      r7a0 <= 1'b0; r7a4 <= 1'b0; r7d <= 2'b00; r180 <= 1'b0; r1ae <= 1'b0;
    end else begin
      if (wr) case (wi)
        14'h01F: mmcm_reset <= d[1:0];
        14'h04F: iserdes_reset <= d[0];
        14'h07A: begin phase_apply <= (d[0] & ~r7a0) | (d[4] & ~r7a4); r7a0 <= d[0]; r7a4 <= d[4]; end
        14'h07B: phase <= d[2:0];
        14'h07D: begin err_clear <= d[1:0] & ~r7d; phase_cal <= d[8]; r7d <= d[1:0]; end
        14'h1FA: pll_sync <= d[8];
        14'h1FB: adc_type <= d[0];
        14'h141: dac_ctrl <= d;
        14'h146: begin dac_code[15:0]  <= d; dac_write[0] <= 1'b1; end
        14'h148: begin dac_code[31:16] <= d; dac_write[1] <= 1'b1; end
        14'h149: begin dac_code[47:32] <= d; dac_write[2] <= 1'b1; end
        14'h147: begin dac_code[63:48] <= d; dac_write[3] <= 1'b1; end
        14'h180: begin dna_read <= d[0] & ~r180; r180 <= d[0]; end
        14'h1AE: begin mean_start <= d[0] & ~r1ae; r1ae <= d[0]; end
        14'h1AF: mean_n <= d;
        14'h1B0: mean_ch <= d[1:0];
        default: ;
      endcase
      if (rd && ri == 14'h160) irq_take <= 1'b1;
    end
  end
  always @(*) begin
    rd_data = 32'd0;
    case (ri)
      14'h000: rd_data[15:0] = ABI_MAJOR;
      14'h001: rd_data[15:0] = ABI_MINOR;
      14'h002: rd_data[15:0] = {8'h4F, IMAGE_INDEX};
      14'h01F: rd_data[15:0] = {2'b00, mmcm1_locked, core_alive, 3'b000, mmcm0_locked, 8'd0};
      14'h021: rd_data[15:0] = {15'd0, rec_ready};
      14'h07C: rd_data[15:0] = {14'd0, data_ready};
      14'h07D: rd_data[15:0] = {10'd0, adc_error, 4'd0};
      14'h181: rd_data[15:0] = {15'd0, dna_ready};
      14'h182: rd_data[15:0] = dna[15:0];
      14'h183: rd_data[15:0] = dna[31:16];
      14'h184: rd_data[15:0] = dna[47:32];
      14'h185: rd_data[15:0] = dna[63:48];
      14'h1B1: rd_data[15:0] = {mean_overflow, 11'd0, mean_valid};
      14'h1B2: rd_data[15:0] = mean_sum[15:0];
      14'h1B3: rd_data[15:0] = mean_sum[31:16];
      14'h1B4: rd_data[15:0] = mean_sum[47:32];
      14'h1B5: rd_data[15:0] = mean_count[15:0];
      14'h1B6: rd_data[15:0] = mean_count[31:16];
      default: ;
    endcase
  end
endmodule
