// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// mean_engine -- the self-calibration mean engine of spec 20 §6.5 with the
// compat semantics of spec 02 §7 / spec 10 §5 (cclk): a start sums every
// valid sample of each processed channel stream over N · 1024 samples per
// channel (48-bit sum, 32-bit count), then valid; a sample at either end of
// the code range sets the channel's overflow flag (the mean is then not to
// be trusted, spec 10 §5.5).
// TRLC-LINKS: REQ-HD-015
module mean_engine (
  input  wire          clk,
  input  wire          rst,
  input  wire          start,
  input  wire [15:0]   n,
  input  wire [383:0]  xs,
  input  wire [7:0]    v,
  output reg           valid = 1'b0,
  output reg  [3:0]    overflow = 4'd0,
  output reg  [191:0]  sum = 192'd0,      // channel c at [48c +: 48]
  output reg  [31:0]   count = 32'd0
);
  reg run = 1'b0;
  reg [31:0] target = 32'd0;
  reg [191:0] s; reg [3:0] o; reg [31:0] k; reg [11:0] x;
  integer c, l;
  always @(posedge clk) begin
    if (rst) begin
      run <= 1'b0; valid <= 1'b0;
    end else if (start) begin
      run <= 1'b1; valid <= 1'b0; sum <= 192'd0; count <= 32'd0; overflow <= 4'd0;
      target <= {6'd0, n, 10'd0};
    end else if (run) begin
      s = sum; o = overflow; k = count;
      for (l = 0; l < 8; l = l + 1) if (v[l] && k < target) begin
        for (c = 0; c < 4; c = c + 1) begin
          x = xs[96*c + 12*l +: 12];
          s[48*c +: 48] = s[48*c +: 48] + {36'd0, x};
          if (x == 12'd0 || x == 12'hFFF) o[c] = 1'b1;
        end
        k = k + 32'd1;
      end
      sum <= s; overflow <= o; count <= k;
      if (k >= target) begin run <= 1'b0; valid <= 1'b1; end
    end
  end
endmodule
