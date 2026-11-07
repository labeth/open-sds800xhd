// ENGMODEL-OWNER-UNIT: FU-HD-PL-TESTS
`timescale 1ns/1ps
// The SDS1000CML+'s unit test of spi_trigger (fpga/images/general/sim), unchanged.
// TRLC-LINKS: REQ-HD-015
module tb_spi_trigger;
 reg clk=0;always #5 clk=~clk;
 reg reset=1,sck=0,data=0,cpol=0,cpha=0,msb=1;
 wire match,ev;wire [7:0] kind,value;
 integer hits=0,bytes=0,starts=0,ends=0,errors=0,mode,order;
 spi_trigger dut(clk,reset,1'b1,1'b1,sck,data,cpol,cpha,msb,24'd18,32'h48a5,3'd2,match,ev,kind,value);
 always @(negedge clk)begin
  if(match)hits=hits+1;
  if(ev)case(kind)
   1:starts=starts+1;
   2:begin
    if(value !== (bytes%2==0 ? 8'h48 : 8'ha5))$fatal(1,"byte %0d got %h",bytes,value);
    bytes=bytes+1;
   end
   3:ends=ends+1;
   4:errors=errors+1;
  endcase
 end
 task hold;begin repeat(6)@(negedge clk);end endtask
 task send(input [7:0] word);integer i;reg bitvalue;begin
  for(i=0;i<8;i=i+1)begin
   bitvalue=msb?word[7-i]:word[i];
   if(!cpha)data=bitvalue;
   hold;sck=!cpol;if(cpha)data=bitvalue;
   hold;sck=cpol;
  end
 end endtask
 initial begin
  for(mode=0;mode<4;mode=mode+1)for(order=0;order<2;order=order+1)begin
   reset=1;cpol=mode/2;cpha=mode%2;msb=order;sck=cpol;hold;
   reset=0;repeat(25)@(negedge clk);hits=0;bytes=0;starts=0;ends=0;errors=0;
   send(8'h48);send(8'ha5);repeat(25)@(negedge clk);
   if(hits!=1 || bytes!=2 || starts!=1 || ends!=1 || errors!=0)$fatal(1,"mode %0d order %0d",mode,order);
   // Reset mid-message: no START, DATA or trigger before a real idle gap.
   reset=1;hold;reset=0;
   send(8'hde);send(8'had);
   if(hits!=1 || bytes!=2 || starts!=1)$fatal(1,"accepted unframed startup bytes");
   repeat(25)@(negedge clk);
   // The same suffix separated by idle must not match.
   send(8'h48);repeat(25)@(negedge clk);send(8'ha5);repeat(25)@(negedge clk);
   if(hits!=1 || bytes!=4 || starts!=3 || ends!=3)$fatal(1,"pattern crossed gap");
  end
  $display("PASS SPI four modes, both bit orders, byte events, suffix and gap isolation");$finish;
 end
 initial begin #100000;$fatal(1,"timeout");end
endmodule
