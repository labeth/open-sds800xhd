// ENGMODEL-OWNER-UNIT: FU-HD-PL-TESTS
// AXI4-Lite master tasks for testbenches. The including module declares clk
// and connects the m_* signals to the slave. Signals change 1 ns after a
// rising edge; ready is sampled at the falling edge, so a handshake is the
// next rising edge with valid and ready both high (no race with the slave).
reg  [15:0] m_awaddr = 0; reg m_awvalid = 0; wire m_awready;
reg  [31:0] m_wdata = 0; reg [3:0] m_wstrb = 4'hF; reg m_wvalid = 0; wire m_wready;
wire [1:0]  m_bresp; wire m_bvalid; reg m_bready = 0;
reg  [15:0] m_araddr = 0; reg m_arvalid = 0; wire m_arready;
wire [31:0] m_rdata; wire [1:0] m_rresp; wire m_rvalid; reg m_rready = 0;
integer bfm_errors = 0;
task axil_write(input [15:0] a, input [31:0] v);
  integer t; reg aw_hs, w_hs;
  begin
    @(posedge clk); #1 m_awaddr = a; m_awvalid = 1; m_wdata = v; m_wvalid = 1; m_bready = 1;
    t = 0;
    while (m_awvalid || m_wvalid) begin
      @(negedge clk); aw_hs = m_awvalid && m_awready; w_hs = m_wvalid && m_wready;
      @(posedge clk); #1 if (aw_hs) m_awvalid = 0; if (w_hs) m_wvalid = 0;
      t = t + 1; if (t > 64) begin $display("FAIL axil write 0x%h stalled", a); bfm_errors = bfm_errors + 1; m_awvalid = 0; m_wvalid = 0; end
    end
    t = 0;
    while (!m_bvalid && t < 64) begin @(posedge clk); #1 t = t + 1; end
    if (!m_bvalid) begin $display("FAIL axil write 0x%h no response", a); bfm_errors = bfm_errors + 1; end
    else if (m_bresp != 2'b00) begin $display("FAIL axil write 0x%h resp %0d", a, m_bresp); bfm_errors = bfm_errors + 1; end
    @(posedge clk); #1 m_bready = 0;
  end
endtask
task axil_read(input [15:0] a, output [31:0] v);
  integer t; reg hs;
  begin
    @(posedge clk); #1 m_araddr = a; m_arvalid = 1; m_rready = 1;
    t = 0;
    while (m_arvalid) begin
      @(negedge clk); hs = m_arready;
      @(posedge clk); #1 if (hs) m_arvalid = 0;
      t = t + 1; if (t > 64) begin $display("FAIL axil read 0x%h stalled", a); bfm_errors = bfm_errors + 1; m_arvalid = 0; end
    end
    t = 0;
    while (!m_rvalid && t < 64) begin @(posedge clk); #1 t = t + 1; end
    v = m_rdata;
    if (!m_rvalid) begin $display("FAIL axil read 0x%h no data", a); bfm_errors = bfm_errors + 1; end
    else if (m_rresp != 2'b00) begin $display("FAIL axil read 0x%h resp %0d", a, m_rresp); bfm_errors = bfm_errors + 1; end
    @(posedge clk); #1 m_rready = 0;
  end
endtask
