// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// From the SDS1000CML+'s open RTL (fpga/images/general/spi_trigger.v, same
// project, MIT); unchanged in behaviour, the output ports declared one a line
// (yosys does not parse a list of initialised output regs). Here tick is
// every cclk (the line slicer's level a clock, spec 20 §7.7).
// Two-wire SPI byte decoder. No chip-select channel is available: gap_ticks
// defines framing, configured to 1.5 clock periods like the repository decoder.
// Inputs are synchronized electrical levels; configuration is held while enabled.
// TRLC-LINKS: REQ-HD-015
module spi_trigger(
 input wire clk,reset,enable,tick,sck,data,cpol,cpha,msb,
 input wire [23:0] gap_ticks,
 input wire [31:0] pattern,input wire [2:0] pattern_len,
 output reg match=0,
 output reg event_valid=0,
 output reg [7:0] event_kind=0,
 output reg [7:0] event_data=0
);
 reg primed=0,previous=0,active=0,framed=0;
 reg valid_config=0;
 always @(posedge clk)valid_config<=gap_ticks>=3 && pattern_len<=4;
 reg [23:0] idle=0;
 reg [2:0] bits=0,count=0;
 reg [7:0] shift=0;
 reg [31:0] history=0;
 wire sample_edge=primed && sck!=previous && sck==(cpol==cpha);
 wire [7:0] received=msb ? {shift[6:0],data} : {data,shift[7:1]};
 wire [31:0] next_history={history[23:0],received};
 wire hit=pattern_len==0 ||
  (pattern_len==1 && next_history[7:0]==pattern[7:0]) ||
  (pattern_len==2 && count>=1 && next_history[15:0]==pattern[15:0]) ||
  (pattern_len==3 && count>=2 && next_history[23:0]==pattern[23:0]) ||
  (pattern_len==4 && count>=3 && next_history==pattern);
 always @(posedge clk)begin
  match<=0;event_valid<=0;
  if(reset || !enable || !valid_config)begin
   primed<=0;previous<=cpol;active<=0;framed<=0;idle<=0;bits<=0;count<=0;history<=0;shift<=0;
  end else if(tick)begin
   primed<=1;previous<=sck;
   // With no CS input, an epoch beginning mid-byte has unknown alignment.
   // Observe an idle gap before admitting the first frame or trigger suffix.
   if(primed && idle<gap_ticks)idle<=idle+1'b1;
   if(primed && idle>=gap_ticks)framed<=1;
   if(sample_edge)idle<=0;
   if(active && idle>=gap_ticks)begin
    active<=0;bits<=0;count<=0;history<=0;shift<=0;
    event_valid<=1;event_kind<=bits==0 ? 8'd3 : 8'd4;event_data<=0;
   end
   if(sample_edge && (framed || idle>=gap_ticks))begin
    idle<=0;active<=1;
    if(!active || idle>=gap_ticks)begin
     // First sampled bit of a frame. A stale partial byte and suffix must
     // never contribute to the new transaction's trigger predicate.
     shift<=msb ? {7'd0,data} : {data,7'd0};bits<=1;count<=0;history<=0;
     event_valid<=1;event_kind<=1;event_data<=0;
    end else begin
     shift<=received;bits<=bits+1'b1;
     if(bits==7)begin
      event_valid<=1;event_kind<=2;event_data<=received;
      history<=next_history;if(count<4)count<=count+1'b1;
      match<=hit;
     end
    end
   end
  end
 end
endmodule
