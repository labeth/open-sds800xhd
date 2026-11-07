// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// trig_interp -- the crossing's position between the two samples around a
// threshold (spec 20 §7.5): frac = floor(16 * (level - x0) / (x1 - x0)),
// 0..16, for a rising crossing x0 < level <= x1 (16: the level is x1 itself, the
// crossing on the trigger sample) (a falling one
// passes x0, x1 and level negated). The crossing is frac/16 of a sample
// period after sample x0. Restoring division, one bit a clock: valid 6
// clocks after start.
// TRLC-LINKS: REQ-HD-015
module trig_interp (
  input  wire        clk,
  input  wire        start,
  input  wire        falling,
  input  wire [11:0] x0,
  input  wire [11:0] x1,
  input  wire [11:0] level,
  output reg  [4:0]  frac = 5'd0,
  output reg         valid = 1'b0
);
  reg [12:0] den = 0, rem = 0;
  reg [2:0] q = 0;
  reg [2:0] step = 0;
  reg busy = 1'b0;
  wire [12:0] a0 = falling ? {1'b0, ~x0} : {1'b0, x0};
  wire [12:0] a1 = falling ? {1'b0, ~x1} : {1'b0, x1};
  wire [12:0] lv = falling ? {1'b0, ~level} : {1'b0, level};
  wire [13:0] trial = {rem, 1'b0} - {1'b0, den};
  always @(posedge clk) begin
    valid <= 1'b0;
    if (start) begin
      busy <= 1'b1; step <= 3'd0; q <= 3'd0;
      den <= (a1 > a0) ? a1 - a0 : 13'd1;
      rem <= (lv > a0) ? ((lv - a0 >= ((a1 > a0) ? a1 - a0 : 13'd1)) ? 13'h1FFF : lv - a0) : 13'd0;
    end else if (busy) begin
      if (rem == 13'h1FFF) begin frac <= 5'd16; valid <= 1'b1; busy <= 1'b0; end
      else begin
        if (!trial[13]) begin rem <= trial[12:0]; q <= {q[1:0], 1'b1}; end
        else begin rem <= {rem[11:0], 1'b0}; q <= {q[1:0], 1'b0}; end
        step <= step + 1'b1;
        if (step == 3'd3) begin
          frac <= !trial[13] ? {1'b0, q[2:0], 1'b1} : {1'b0, q[2:0], 1'b0};
          valid <= 1'b1; busy <= 1'b0;
        end
      end
    end
  end
endmodule
