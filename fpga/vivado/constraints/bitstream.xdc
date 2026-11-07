# ENGMODEL-OWNER-UNIT: FU-HD-PL-BUILD
# bitstream.xdc -- configuration options (spec 12 §2): a full, unencrypted,
# uncompressed image for fpga_manager with flags 0. USR_ACCESS carries the
# build identity (the git commit, also ID_BUILD), never a timestamp; build.tcl
# sets it.
set_property BITSTREAM.GENERAL.COMPRESS FALSE [current_design]
set_property BITSTREAM.ENCRYPTION.ENCRYPT NO [current_design]
set_property BITSTREAM.CONFIG.UNUSEDPIN PULLNONE [current_design]
