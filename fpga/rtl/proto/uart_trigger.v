// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// From the SDS1000CML+'s open RTL (fpga/images/general/uart_trigger.v, same
// project, MIT); unchanged in behaviour, the output ports declared one a line
// (yosys does not parse a list of initialised output regs). Here tick is
// every cclk (the line slicer's level a clock, spec 20 §7.7).
// UART 8N1 receiver and up-to-four-byte suffix matcher. Configuration must
// remain stable while enabled; reset on every acquisition/configuration change.
// tick is the sample-valid strobe; bit_ticks is measured in those strobes.
// pattern packs chronological bytes from most to least significant within
// pattern_len bytes (e.g. 2, 32'h00004869). Length zero matches any valid byte.
// TRLC-LINKS: REQ-HD-015
module uart_trigger (
 input wire clk, reset, enable, tick, line, inverted,
 input wire [23:0] bit_ticks,
 input wire [31:0] pattern,
 input wire [2:0] pattern_len,
 output reg match=0,
 output reg byte_valid=0,
 output reg framing_error=0,
 output reg [7:0] byte_data=0
);
 localparam IDLE=0, START=1, DATA=2, STOP=3;
 reg [1:0] state=IDLE;
 reg [23:0] remaining=0;
 reg timer_done=1;
 reg configuration_valid=0;
 reg [23:0] full_reload=0,half_reload=0;
 reg [2:0] bit_index=0, count=0;
 reg [7:0] received=0;
 reg [31:0] history=0;
 reg previous=0, seen_idle=0;
 reg level=1,sample_valid=0,start_edge=0,raw_previous=0;
 always @(posedge clk)begin
  level<=line ^ inverted;sample_valid<=tick;
  start_edge<=tick && raw_previous && !(line ^ inverted);
  if(reset || !enable)begin raw_previous<=0;sample_valid<=0;start_edge<=0;end
  else if(tick)raw_previous<=line ^ inverted;
 end
 wire [31:0] next_history={history[23:0],received};
 reg [3:0] byte_equal=0;
 reg pattern_hit=0;
 always @(posedge clk) begin
  byte_equal[0]<=next_history[7:0]==pattern[7:0];
  byte_equal[1]<=next_history[15:8]==pattern[15:8];
  byte_equal[2]<=next_history[23:16]==pattern[23:16];
  byte_equal[3]<=next_history[31:24]==pattern[31:24];
  pattern_hit<=pattern_len==0 ||
   (pattern_len==1 && byte_equal[0]) ||
   (pattern_len==2 && count>=1 && (&byte_equal[1:0])) ||
   (pattern_len==3 && count>=2 && (&byte_equal[2:0])) ||
   (pattern_len==4 && count>=3 && (&byte_equal));
 end
 always @(posedge clk) begin
  // Configuration settles while reset/disabled; no wide configuration
  // comparison or subtraction belongs in the running receive-state path.
  configuration_valid<=bit_ticks>=4 && pattern_len<=4;
  full_reload<=bit_ticks-1;half_reload<=(bit_ticks>>1)-1;
  match<=0;byte_valid<=0;framing_error<=0;
  if(reset || !enable || !configuration_valid) begin
   state<=IDLE;remaining<=0;timer_done<=1;count<=0;history<=0;
   seen_idle<=0;previous<=0;
  end else if(sample_valid) begin
   previous<=level;
   // Look ahead one decrement: keep the wide zero comparison out of the
   // counter reload mux. This flag advances only with sample-valid ticks.
   timer_done<=remaining==1;
   if(!timer_done) remaining<=remaining-1;
   else begin remaining<=full_reload;timer_done<=0;end
   case(state)
    IDLE:begin
     if(level) seen_idle<=1;
     if(seen_idle && start_edge) begin
      state<=START;remaining<=half_reload;timer_done<=0;
     end
    end
    START:if(timer_done) begin
     if(level) state<=IDLE;
     else begin state<=DATA;bit_index<=0;end
    end
    DATA:if(timer_done) begin
     received[bit_index]<=level;
     if(bit_index==7) state<=STOP; else bit_index<=bit_index+1;
    end
    STOP:if(timer_done) begin
     state<=IDLE;
     if(level) begin
      byte_data<=received;byte_valid<=1;history<=next_history;
      if(count<4) count<=count+1;
      match<=pattern_hit;
     end else begin
      framing_error<=1;count<=0;history<=0;seen_idle<=0;
     end
    end
   endcase
  end
 end
endmodule
