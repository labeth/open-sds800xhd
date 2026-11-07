// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// compat_map -- the subset of the vendor MAP plane (spec 02 §9, spec 07,
// spec 08) the board's display and panel code and the unit's key driver use
// against the open image (spec 20 §10.4): LCD reset/enable/backlight, keys,
// encoder, LED word, key interrupt. Word-indexed, 16-bit. The waveform-layer
// controls (0x0FA, 0x0FF) are accepted and have no effect.
// TRLC-LINKS: REQ-HD-015
module compat_map (
  input  wire        clk,
  input  wire        rst,
  input  wire        wr,
  input  wire [15:0] wr_addr,
  input  wire [31:0] wr_data,
  input  wire        rd,
  input  wire [15:0] rd_addr,
  output reg  [31:0] rd_data,
  output reg         lcd_reset = 1'b0,       // 0x015
  output reg         lcd_enable = 1'b0,      // 0x016
  output reg  [15:0] backlight = 16'h0700,   // 0x14D (the boot value, spec 02 §9)
  input  wire [63:0] keys,                   // 0x160-0x163, active low
  input  wire [7:0]  encoder,                // 0x169
  input  wire        key_event,              // a key or encoder change
  output wire        key_irq,
  output reg  [31:0] led_word = 32'd0,       // 0x16A bits 15:0, 0x16F bits 31:16
  output reg         led_latch = 1'b0,       // 0x16C 0 -> 1
  output reg  [15:0] knob_period = 16'd75    // 0x16D
);
  wire [13:0] wi = wr_addr[15:2];
  wire [13:0] ri = rd_addr[15:2];
  wire [15:0] d = wr_data[15:0];
  reg [15:0] led_lo = 16'd0, led_hi = 16'd0;
  reg r16c = 1'b0;
  reg pending = 1'b0;
  assign key_irq = pending;
  always @(posedge clk) begin
    led_latch <= 1'b0;
    if (rst) begin
      lcd_reset <= 1'b0; lcd_enable <= 1'b0; pending <= 1'b0; r16c <= 1'b0;
    end else begin
      if (wr) case (wi)
        14'h015: lcd_reset <= d[0];
        14'h016: lcd_enable <= d[0];
        14'h14D: backlight <= d;
        14'h16A: led_lo <= d;
        14'h16F: led_hi <= d;
        14'h16C: begin
          if (d[0] && !r16c) begin led_latch <= 1'b1; led_word <= {led_hi, led_lo}; end
          r16c <= d[0];
        end
        14'h16D: knob_period <= d;
        default: ;
      endcase
      pending <= key_event | (pending & ~(rd && ri == 14'h16E));
    end
  end
  always @(*) begin
    rd_data = 32'd0;
    case (ri)
      14'h160: rd_data[15:0] = keys[15:0];
      14'h161: rd_data[15:0] = keys[31:16];
      14'h162: rd_data[15:0] = keys[47:32];
      14'h163: rd_data[15:0] = keys[63:48];
      14'h169: rd_data[15:0] = {8'd0, encoder};
      default: ;
    endcase
  end
endmodule
