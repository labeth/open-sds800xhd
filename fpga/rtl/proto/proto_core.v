// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// proto_core -- the protocol trigger block (spec 20 §7.7): two line slicers
// on selected channels (Schmitt comparators with their own thresholds, one
// level a clock: the state after the clock's last valid lane) and the
// protocol engines. One engine runs at a time; the others are held in reset.
//   engine 1 UART on line A (bit time in clocks), 2 SPI (A clock, B data,
//   idle gap in clocks), 3 I2C (A SCL, B SDA).
// match pulses on a protocol match (the trigger core's serial input);
// event_* is the decoded event stream (start 1, data 2, end 3, error 4).
// TRLC-LINKS: REQ-HD-015
module proto_core #(parameter integer L = 8) (
  input  wire          clk,
  input  wire          rst,
  input  wire          en,
  input  wire [4*L*12-1:0] xs,
  input  wire [L-1:0]  vmask,
  input  wire [3:0]    engine,
  input  wire [1:0]    a_ch,
  input  wire [1:0]    b_ch,
  input  wire          inv_a,
  input  wire          inv_b,
  input  wire [23:0]   slice_a,      // {lo, hi} 12 bits each
  input  wire [23:0]   slice_b,
  input  wire [23:0]   timing,
  input  wire          cpol,
  input  wire          cpha,
  input  wire          msb,
  input  wire [6:0]    address,
  input  wire          any_address,
  input  wire [1:0]    direction,
  input  wire [31:0]   pattern,
  input  wire [2:0]    pattern_len,
  output reg           match = 1'b0,
  output reg           event_valid = 1'b0,
  output reg  [7:0]    event_kind = 8'd0,
  output reg  [7:0]    event_data = 8'd0,
  output reg  [31:0]   match_count = 32'd0,
  output reg  [31:0]   event_count = 32'd0,
  output reg  [31:0]   error_count = 32'd0
);
  localparam ENG_UART = 4'd1, ENG_SPI = 4'd2, ENG_I2C = 4'd3;
  wire run_rst = rst || !en;
  wire [L-1:0] sa, sb;
  trig_cmp #(.L(L)) u_sa (.clk(clk), .rst(run_rst), .x(xs[L*12*a_ch +: L*12]), .v(vmask),
    .hi(slice_a[11:0]), .lo(slice_a[23:12]), .state(sa), .rise(), .fall());
  trig_cmp #(.L(L)) u_sb (.clk(clk), .rst(run_rst), .x(xs[L*12*b_ch +: L*12]), .v(vmask),
    .hi(slice_b[11:0]), .lo(slice_b[23:12]), .state(sb), .rise(), .fall());
  // the state after the last valid lane: lanes are filled from 0, so the
  // highest set bit of vmask (delayed with the comparators)
  reg [L-1:0] v1 = 0, v2 = 0;
  always @(posedge clk) begin v1 <= vmask; v2 <= v1; end
  reg la, lb;
  integer i;
  always @(*) begin
    la = sa[0]; lb = sb[0];
    for (i = 0; i < L; i = i + 1) if (v2[i]) begin la = sa[i]; lb = sb[i]; end
  end
  reg line_a = 1'b1, line_b = 1'b1;
  always @(posedge clk) begin line_a <= la ^ inv_a; line_b <= lb ^ inv_b; end
  wire u_match, u_valid, u_err; wire [7:0] u_data;
  uart_trigger u_uart (.clk(clk), .reset(run_rst), .enable(engine == ENG_UART), .tick(1'b1), .line(line_a),
    .inverted(1'b0), .bit_ticks(timing), .pattern(pattern), .pattern_len(pattern_len),
    .match(u_match), .byte_valid(u_valid), .framing_error(u_err), .byte_data(u_data));
  wire s_match, s_valid; wire [7:0] s_kind, s_data;
  spi_trigger u_spi (.clk(clk), .reset(run_rst), .enable(engine == ENG_SPI), .tick(1'b1), .sck(line_a), .data(line_b),
    .cpol(cpol), .cpha(cpha), .msb(msb), .gap_ticks(timing), .pattern(pattern), .pattern_len(pattern_len),
    .match(s_match), .event_valid(s_valid), .event_kind(s_kind), .event_data(s_data));
  wire i_match, i_valid; wire [7:0] i_kind, i_data;
  i2c_trigger u_i2c (.clk(clk), .reset(run_rst), .enable(engine == ENG_I2C), .tick(1'b1), .scl(line_a), .sda(line_b),
    .address(address), .any_address(any_address), .direction(direction), .pattern(pattern), .pattern_len(pattern_len),
    .match(i_match), .event_valid(i_valid), .event_kind(i_kind), .event_data(i_data), .event_meta(), .selected());
  always @(posedge clk) begin
    match <= 1'b0; event_valid <= 1'b0;
    if (run_rst) begin
      match_count <= 32'd0; event_count <= 32'd0; error_count <= 32'd0;
    end else begin
      case (engine)
        ENG_UART: begin
          match <= u_match; event_valid <= u_valid | u_err;
          event_kind <= u_err ? 8'd4 : 8'd2; event_data <= u_data;
        end
        ENG_SPI: begin match <= s_match; event_valid <= s_valid; event_kind <= s_kind; event_data <= s_data; end
        ENG_I2C: begin match <= i_match; event_valid <= i_valid; event_kind <= i_kind; event_data <= i_data; end
        default: ;
      endcase
      if (match) match_count <= match_count + 1'b1;
      if (event_valid) begin
        event_count <= event_count + 1'b1;
        if (event_kind == 8'd4) error_count <= error_count + 1'b1;
      end
    end
  end
endmodule
