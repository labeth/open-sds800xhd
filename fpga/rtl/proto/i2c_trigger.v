// ENGMODEL-OWNER-UNIT: FU-HD-PL
`timescale 1ns/1ps
// From the SDS1000CML+'s open RTL (fpga/images/general/i2c_trigger.v, same
// project, MIT); unchanged in behaviour, the output ports declared one a line
// (yosys does not parse a list of initialised output regs). Here tick is
// every cclk (the line slicer's level a clock, spec 20 §7.7).
// Seven-bit I2C address/direction and up-to-four-byte data suffix matcher.
// Inputs are already synchronized and sliced to electrical logic levels.
// Each START (including repeated START) resets transaction-local history.
// TRLC-LINKS: REQ-HD-015
module i2c_trigger (
 input wire clk,reset,enable,tick,scl,sda,
 input wire [6:0] address,
 input wire any_address,
 input wire [1:0] direction, // 0 write, 1 read, 2 either
 input wire [31:0] pattern,
 input wire [2:0] pattern_len,
 output reg match=0,
 output reg event_valid=0,
 output reg [7:0] event_kind=0,
 output reg [7:0] event_data=0,
 // DATA metadata: bit0 address byte, bit1 NACK. All observed bytes are
 // streamed, independently of the trigger's address/data predicate.
 output reg [1:0] event_meta=0,
 // selected: the current transaction's address and direction match; a
 // sequence trigger over the data bytes is qualified by it.
 output reg selected=0
);
 reg prev_scl=1,prev_sda=1,active=0,address_byte=1;
 reg primed=0;
 reg [3:0] bit_index=0;
 reg [7:0] shift=0;
 reg received_address=0;
 reg [31:0] history=0;
 reg [2:0] count=0;
 wire start=prev_sda && !sda && prev_scl && scl;
 wire stop=!prev_sda && sda && prev_scl && scl;
 wire rise=!prev_scl && scl;
 wire [7:0] received={shift[6:0],sda};
 wire [31:0] next_history={history[23:0],received};
 wire address_match=(any_address || received[7:1]==address) &&
  (direction==2 || (direction==0 && !received[0]) || (direction==1 && received[0]));
 wire data_match=(pattern_len==1 && next_history[7:0]==pattern[7:0]) ||
  (pattern_len==2 && count>=1 && next_history[15:0]==pattern[15:0]) ||
  (pattern_len==3 && count>=2 && next_history[23:0]==pattern[23:0]) ||
  (pattern_len==4 && count>=3 && next_history==pattern);
 always @(posedge clk) begin
  match<=0;event_valid<=0;
  if(reset || !enable || pattern_len>4 || direction>2)begin
   active<=0;selected<=0;count<=0;history<=0;bit_index<=0;address_byte<=1;
   prev_scl<=1;prev_sda<=1;primed<=0;
  end else if(tick)begin
   prev_scl<=scl;prev_sda<=sda;
   primed<=1;
   if(!primed)begin active<=0;selected<=0;end
   else if(start)begin
    event_valid<=1;event_kind<=1;event_data<=0;event_meta<=0;
    active<=1;selected<=0;count<=0;history<=0;bit_index<=0;address_byte<=1;
   end else if(stop)begin
    if(active)begin event_valid<=1;event_kind<=3;event_data<=0;event_meta<=0;end
    active<=0;selected<=0;count<=0;history<=0;
   end
   else if(active && rise)begin
    if(bit_index==8)begin
     bit_index<=0;event_valid<=1;event_kind<=2;event_data<=shift;
     event_meta<={sda,received_address};
    end
    else begin
     shift<=received;bit_index<=bit_index+1;
     if(bit_index==7)begin
      received_address<=address_byte;
      if(address_byte)begin
       address_byte<=0;selected<=address_match;
       match<=address_match && pattern_len==0;
      end else if(selected)begin
       history<=next_history;if(count<4)count<=count+1;
       match<=data_match;
      end
     end
    end
   end
  end
 end
endmodule
