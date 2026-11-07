// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// prim_dna -- the 96-bit device DNA. A read pulse loads the DNA shift
// register and shifts it out (96 clocks); ready rises with the whole value in
// dna, bit 0 shifted first. clk at most 200 MHz (DNA_PORTE2). The model
// returns SIM_DNA.
// TRLC-LINKS: REQ-HD-015
module prim_dna #(parameter [95:0] SIM_DNA = 96'h0123_4567_89AB_CDEF_0F1E_2D3C) (
  input  wire        clk,
  input  wire        read,
  output reg  [95:0] dna = 96'd0,
  output reg         ready = 1'b0
);
  wire dout;
  reg  load = 1'b0, shift = 1'b0;
`ifdef SIM
  reg [95:0] sr = 96'd0;
  always @(posedge clk) if (load) sr <= SIM_DNA; else if (shift) sr <= {1'b0, sr[95:1]};
  assign dout = sr[0];
`else
  DNA_PORTE2 u_dna (.CLK(clk), .DIN(1'b0), .READ(load), .SHIFT(shift), .DOUT(dout));
`endif
  reg [6:0] n = 7'd0;
  always @(posedge clk) begin
    load <= 1'b0;
    if (read) begin load <= 1'b1; shift <= 1'b0; ready <= 1'b0; n <= 7'd0; end
    else if (load) shift <= 1'b1;
    else if (shift) begin
      dna <= {dout, dna[95:1]};
      n <= n + 1'b1;
      if (n == 7'd95) begin shift <= 1'b0; ready <= 1'b1; end
    end
  end
endmodule
