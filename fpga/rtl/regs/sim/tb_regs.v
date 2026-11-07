// ENGMODEL-OWNER-UNIT: FU-HD-PL-TESTS
`timescale 1ns/1ps
// Self-checking: the open plane and the compat ACQ/MAP planes over AXI-Lite.
// Identity (magic, ABI, manifest from the generated include, geometry), every
// response OKAY, unmapped reads 0, read/write masks and reset values,
// strobes, sticky faults, the interrupt take/acknowledge path through the
// compat 0x160 read, uptime latching; compat version marker, ADC controls,
// DNA scope ID, offset-DAC writes, keys, LED latch and the key interrupt.
// TRLC-LINKS: REQ-HD-015
module tb_regs;
`include "open_pl_regs.vh"
`include "regs/opl_sts.vh"
  reg clk = 0; always #5 clk = ~clk;
  reg rst = 1;
`include "regs/sim/axil_bfm.vh"
  // one AXI-Lite master, address bit 15:14 selects the plane in this bench:
  // 0 open, 1 compat ACQ, 2 compat MAP
  wire wr, rd; wire [15:0] wa, ra; wire [31:0] wd; wire [3:0] ws;
  reg  [31:0] rdat;
  axil_slave #(.AW(16)) u_axil (.clk(clk), .rst(rst),
    .s_awaddr(m_awaddr), .s_awvalid(m_awvalid), .s_awready(m_awready), .s_wdata(m_wdata), .s_wstrb(m_wstrb),
    .s_wvalid(m_wvalid), .s_wready(m_wready), .s_bresp(m_bresp), .s_bvalid(m_bvalid), .s_bready(m_bready),
    .s_araddr(m_araddr), .s_arvalid(m_arvalid), .s_arready(m_arready), .s_rdata(m_rdata), .s_rresp(m_rresp),
    .s_rvalid(m_rvalid), .s_rready(m_rready),
    .wr(wr), .wr_addr(wa), .wr_data(wd), .wr_strb(ws), .rd(rd), .rd_addr(ra), .rd_data(rdat), .rd_hold(1'b0));
  wire [1:0] wsel = wa[15:14], rsel = ra[15:14];
  wire [15:0] wa12 = {4'd0, wa[11:0]}, ra12 = {4'd0, ra[11:0]};
  wire [31:0] rd_open, rd_acq, rd_map;
  always @(*) case (rsel) 2'd0: rdat = rd_open; 2'd1: rdat = rd_acq; 2'd2: rdat = rd_map; default: rdat = 0; endcase
  wire [1024*32-1:0] cfg;
  reg  [32*32-1:0] sts = 0;
  wire [31:0] acq_pulse, stack_pulse, ro_pulse, ctr_pulse; wire tail_wr;
  reg [4:0] fault_set = 0; reg [3:0] cause = 0;
  wire irq, irq_take;
  wire [95:0] dna = 96'hFEDC_BA98_7654_3210_0011_2233;
  opl_regs #(.BUILD(32'hC0FFEE01)) u_open (.clk(clk), .rst(rst), .wr(wr && wsel == 0), .wr_addr(wa12), .wr_data(wd),
    .wr_strb(ws), .rd(rd && rsel == 0), .rd_addr(ra12), .rd_data(rd_open), .cfg(cfg), .sts(sts), .dna(dna),
    .acq_ctrl_pulse(acq_pulse), .stack_ctrl_pulse(stack_pulse), .readout_ctrl_pulse(ro_pulse),
    .counter_clear_pulse(ctr_pulse), .stream_tail_written(tail_wr), .fault_set(fault_set), .irq_cause_set(cause),
    .irq_take(irq_take), .irq(irq));
  wire [1:0] mmcm_reset; wire iserdes_reset, phase_apply, phase_cal, pll_sync, adc_type, dna_read, mean_start;
  wire [2:0] phase; wire [1:0] err_clear, mean_ch; wire [15:0] dac_ctrl, mean_n; wire [63:0] dac_code; wire [3:0] dac_write;
  reg dna_ready = 0;
  always @(posedge clk) if (dna_read) dna_ready <= 1;
  compat_acq u_acq (.clk(clk), .rst(rst), .wr(wr && wsel == 1), .wr_addr(wa12), .wr_data(wd), .rd(rd && rsel == 1),
    .rd_addr(ra12), .rd_data(rd_acq), .mmcm_reset(mmcm_reset), .mmcm0_locked(1'b1), .core_alive(1'b1),
    .mmcm1_locked(1'b0), .iserdes_reset(iserdes_reset), .phase_apply(phase_apply), .phase(phase),
    .data_ready(2'b01), .err_clear(err_clear), .phase_cal(phase_cal), .adc_error(2'b10), .pll_sync(pll_sync),
    .adc_type(adc_type), .rec_ready(1'b1), .dac_ctrl(dac_ctrl), .dac_code(dac_code), .dac_write(dac_write),
    .irq_take(irq_take), .dna_read(dna_read), .dna(dna), .dna_ready(dna_ready), .mean_n(mean_n), .mean_ch(mean_ch),
    .mean_start(mean_start), .mean_valid(1'b1), .mean_overflow(4'b0010), .mean_sum(48'h0000_1234_5678),
    .mean_count(32'h0001_0000));
  wire lcd_reset, lcd_enable, key_irq, led_latch; wire [15:0] backlight, knob; wire [31:0] led_word;
  reg key_event = 0;
  compat_map u_map (.clk(clk), .rst(rst), .wr(wr && wsel == 2), .wr_addr(wa12), .wr_data(wd), .rd(rd && rsel == 2),
    .rd_addr(ra12), .rd_data(rd_map), .lcd_reset(lcd_reset), .lcd_enable(lcd_enable), .backlight(backlight),
    .keys(64'hFFFF_FFFF_FFFE_FFFF), .encoder(8'h2A), .key_event(key_event), .key_irq(key_irq), .led_word(led_word),
    .led_latch(led_latch), .knob_period(knob));
  // pulse watchers
  integer arms = 0, phases = 0, dacs = 0, latches = 0;
  always @(posedge clk) begin
    if (acq_pulse[0]) arms = arms + 1;
    if (phase_apply) phases = phases + 1;
    if (dac_write[1]) dacs = dacs + 1;
    if (led_latch) latches = latches + 1;
  end
  integer errors = 0;
  reg [31:0] v;
  task check(input [15:0] a, input [31:0] want, input [8*24-1:0] what);
    begin axil_read(a, v); if (v !== want) begin errors = errors + 1; $display("FAIL %0s: 0x%h read 0x%h want 0x%h", what, a, v, want); end end
  endtask
  localparam ACQ = 16'h4000, MAP = 16'h8000;
  initial begin
    repeat (4) @(posedge clk); rst = 0;
    // identity
    check(OPL_ID_MAGIC, 32'h4F504C31, "magic");
    check(OPL_ID_ABI, {OPL_ABI_MAJOR, OPL_ABI_MINOR}, "abi");
    check(OPL_ID_IMAGE, "lluf", "image");
    check(OPL_ID_BUILD, 32'hC0FFEE01, "build");
    check(OPL_ID_MANIFEST_LO, OPL_MANIFEST[31:0], "manifest lo");
    check(OPL_ID_MANIFEST_HI, OPL_MANIFEST[63:32], "manifest hi");
    check(OPL_ID_GEOMETRY, 32'h01F4_0408, "geometry");
    check(OPL_ID_DNA2, 32'hFEDC_BA98, "dna");
    // scratch, masks, resets, unmapped
    axil_write(OPL_ID_SCRATCH, 32'hDEADBEEF); check(OPL_ID_SCRATCH, 32'hDEADBEEF, "scratch");
    check(OPL_ACQ_POST, 32'd1000, "post reset");
    check(OPL_TRIG_CLOSE, 32'd1, "close reset");
    check(OPL_TRIG_CMP2_A_LO, 32'h7C0, "cmp reset");
    axil_write(OPL_ACQ_ROUTE, 32'hFFFF_FFFF); check(OPL_ACQ_ROUTE, 32'h0000_00F3, "route mask");
    m_wstrb = 4'b0001; axil_write(OPL_ID_SCRATCH, 32'h0000_0011); m_wstrb = 4'hF;
    check(OPL_ID_SCRATCH, 32'hDEADBE11, "byte strobe");
    axil_write(OPL_ID_MAGIC, 32'h0); check(OPL_ID_MAGIC, 32'h4F504C31, "magic is read only");
    check(16'h0FFC, 32'h0, "unmapped");
    check(16'h0F00, 32'h0, "unmapped 2");
    axil_write(16'h0F00, 32'h1234); check(16'h0F00, 32'h0, "unmapped write ignored");
    // status slots
    sts[32*STS_ACQ_TRIG_POS +: 32] = 32'h0BAD_F00D; check(OPL_ACQ_TRIG_POS, 32'h0BAD_F00D, "status slot");
    // strobes
    axil_write(OPL_ACQ_CTRL, 32'h1); axil_write(OPL_ACQ_CTRL, 32'h1); repeat (3) @(posedge clk);
    check(OPL_ACQ_CTRL, 32'h0, "strobe reads 0");
    if (arms != 2) begin errors = errors + 1; $display("FAIL arm strobes %0d", arms); end
    // faults
    @(posedge clk) fault_set <= 5'b00101; @(posedge clk) fault_set <= 0;
    check(OPL_SYS_FAULT, 32'h5, "fault sticky");
    axil_write(OPL_SYS_FAULT, 32'h1); check(OPL_SYS_FAULT, 32'h4, "fault w1c");
    // interrupts: a cause with the enable off raises nothing
    @(posedge clk) cause <= 4'b0001; @(posedge clk) cause <= 0;
    @(posedge clk) if (irq) begin errors = errors + 1; $display("FAIL irq while disabled"); end
    axil_write(OPL_SYS_IRQ_EN, 32'h1);
    @(posedge clk) if (!irq) begin errors = errors + 1; $display("FAIL irq not raised"); end
    check(ACQ | 16'h0580, 32'h0, "0x160 read");
    repeat (2) @(posedge clk); if (irq) begin errors = errors + 1; $display("FAIL irq not taken by 0x160 read"); end
    check(OPL_SYS_IRQ_TAKEN, 32'h1, "taken");
    check(OPL_SYS_IRQ_TAKEN, 32'h0, "taken read clears");
    // uptime: HI latched by the LO read
    axil_read(OPL_ID_UPTIME_LO, v); if (v == 0) begin errors = errors + 1; $display("FAIL uptime"); end
    check(OPL_ID_UPTIME_HI, 32'h0, "uptime hi");
    // compat ACQ
    check(ACQ | 16'h0008, 32'h4F00, "open marker");
    check(ACQ | 16'h007C, 32'h1100, "0x01F status");
    axil_write(ACQ | 16'h007C, 32'h1); if (mmcm_reset != 2'b01) begin errors = errors + 1; $display("FAIL mmcm reset"); end
    axil_write(ACQ | 16'h013C, 32'h0); if (iserdes_reset) begin errors = errors + 1; $display("FAIL iserdes release"); end
    axil_write(ACQ | 16'h01EC, 32'h5); axil_write(ACQ | 16'h01E8, 32'h0); axil_write(ACQ | 16'h01E8, 32'h1); repeat (3) @(posedge clk);
    if (phase != 3'd5 || phases != 1) begin errors = errors + 1; $display("FAIL phase apply %0d %0d", phase, phases); end
    check(ACQ | 16'h01F4, 32'h0020, "adc errors");
    axil_write(ACQ | 16'h0520, 32'h1234); repeat (3) @(posedge clk); if (dacs != 1 || dac_code[31:16] != 16'h1234) begin errors = errors + 1; $display("FAIL dac CH2"); end
    axil_write(ACQ | 16'h0600, 32'h0); axil_write(ACQ | 16'h0600, 32'h1);
    check(ACQ | 16'h0604, 32'h1, "scope id ready");
    check(ACQ | 16'h0614, 32'h7654, "scope id 0x185");
    check(ACQ | 16'h06C8, 32'h5678, "mean sum 0x1B2");
    check(ACQ | 16'h06C4, 32'h2001, "mean valid/overflow");
    check(ACQ | 16'h0400, 32'h0, "unmapped compat");
    // compat MAP
    check(MAP | 16'h0584, 32'hFFFE, "keys word 1");
    check(MAP | 16'h05A4, 32'h2A, "encoder");
    axil_write(MAP | 16'h05A8, 32'hBEEF); axil_write(MAP | 16'h05BC, 32'h0001);
    axil_write(MAP | 16'h05B0, 32'h0); axil_write(MAP | 16'h05B0, 32'h1); repeat (3) @(posedge clk);
    if (latches != 1 || led_word != 32'h0001_BEEF) begin errors = errors + 1; $display("FAIL led latch %h", led_word); end
    @(posedge clk) key_event <= 1; @(posedge clk) key_event <= 0;
    @(posedge clk) if (!key_irq) begin errors = errors + 1; $display("FAIL key irq"); end
    check(MAP | 16'h05B8, 32'h0, "key ack");
    @(posedge clk) if (key_irq) begin errors = errors + 1; $display("FAIL key irq not acknowledged"); end
    axil_write(MAP | 16'h0534, 32'd1099); if (backlight != 16'd1099) begin errors = errors + 1; $display("FAIL backlight"); end
    errors = errors + bfm_errors;
    if (errors == 0) $display("PASS identity, masks, resets, strobes, faults, irq take via 0x160, compat ACQ/MAP");
    else $display("FAIL %0d errors", errors);
    $finish;
  end
  initial begin #2000000; $display("FAIL timeout"); $finish; end
endmodule
