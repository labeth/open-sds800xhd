// ENGMODEL-OWNER-UNIT: FU-HD-PL-TESTS
`timescale 1ns/1ps
// The SDS1000CML+'s unit test of uart_trigger (fpga/images/general/sim), unchanged.
// TRLC-LINKS: REQ-HD-015
module tb_uart_trigger;
 reg clk=0;always #5 clk=~clk;
 reg reset=1,enable=1,tick=1,line=1,inverted=0;
 reg sparse=0;
 always @(negedge clk)if(sparse)tick<=!tick;else tick<=1;
 reg [23:0] bit_ticks=16;
 reg [31:0] pattern=32'h4869;
 reg [2:0] pattern_len=2;
 wire matched,valid,error;wire [7:0] data;
 uart_trigger dut(clk,reset,enable,tick,line,inverted,bit_ticks,pattern,pattern_len,matched,valid,error,data);
 integer match_count=0,bytes=0,errors=0;
 always @(negedge clk) begin
  if(matched) match_count=match_count+1;
  if(valid) bytes=bytes+1;
  if(error) errors=errors+1;
 end
 task level(input v);integer j;begin
  @(negedge clk);line=v ^ inverted;
  for(j=0;j<bit_ticks;j=j+1)begin @(posedge clk);while(!tick)@(posedge clk);end
 end endtask
 task send(input [7:0] v,input good_stop);integer i;begin
  level(0);for(i=0;i<8;i=i+1) level(v[i]);level(good_stop);
 end endtask
 task restart;begin
  @(negedge clk);reset=1;repeat(2) @(posedge clk);
  @(negedge clk);reset=0;level(1);level(1);
 end endtask
 integer period,spacing;
 initial begin
  for(spacing=0;spacing<2;spacing=spacing+1)begin
  sparse=spacing;
  for(period=4;period<=32;period=period+1)begin
  bit_ticks=period;match_count=0;bytes=0;errors=0;inverted=0;
  restart;send(8'h48,1);send(8'h69,1);level(1);
  if(match_count!=1 || bytes!=2) $fatal(1,"matching bytes failed");
  send(8'hde,1);send(8'had,1);level(1);
  if(match_count!=1) $fatal(1,"absent pattern matched");
  send(8'h48,1);send(8'hff,0);level(1);send(8'h69,1);level(1);
  if(match_count!=1 || errors!=1) $fatal(1,"framing error bridged a match");
  inverted=1;restart;send(8'h48,1);send(8'h69,1);level(1);
  if(match_count!=2) $fatal(1,"inverted UART did not match");
  restart;send(8'h48,1);restart;send(8'h69,1);level(1);
  if(match_count!=2) $fatal(1,"match crossed reset");
  end
  end
  $display("PASS UART matching, rejection, framing error, polarity, reset");$finish;
 end
 initial begin #4000000;$fatal(1,"timeout");end
endmodule
