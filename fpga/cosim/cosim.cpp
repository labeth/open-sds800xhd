// ENGMODEL-OWNER-UNIT: FU-HD-PL-TESTS
// opl_cosim -- the open PL (fpga/rtl, top opl_core through cosim_top),
// Verilated, as a process another simulator drives: the device model of
// the tier-1 simulator forwards the PL's register windows to it, gives it
// the PS DDR as shared memory and the ADC samples on request, and turns its
// interrupt lines into the kernel modules' interrupt nodes. Around the core
// the harness stands for what the full image adds from Xilinx IP or from
// board facts still open: the HP ports' DDR slaves, the AXI DMA (PG021,
// S2MM direct register mode) and the capture front (samples handed in at
// the ISERDES outputs after the lane map).
//
// Clocks: aclk 100 MHz, cclk 250 MHz, mclk 300 MHz, exact ratios (time
// unit 1/3 ns). One tick is 0.5 ns; cclk cycle j carries ticks 8j … 8j+7.
//
// Protocol. File descriptors 3 (ctl), 4 (sig) are stream sockets, 5 the
// DDR memfd (1 GiB, physical address = offset). Little endian.
//   ctl, the driver asks, the co-sim answers (one request at a time):
//     'R' u32 addr                 -> u32 value
//     'W' u32 addr, u32 value      -> u8 0
//     'Z'                          -> u8 0   PL reset (pl_resetn0 pulse, e.g. a reload)
//     'T'                          -> u64 cclk cycles, u64 wall ns
//     'X' u16 len, text            -> u16 len, text   knobs (see knob())
//   sig, the co-sim asks or tells:
//     'S' u64 first tick, u32 n    <- n u16 codes per channel, channel-major (C1 first)
//     'I' u8 line, u8 level           an interrupt line changed (0 AXI DMA, 5 open plane)
//     'E' u16 len, text               a log event (JSON), the driver writes it to its log
// Addresses: 0x8001_xxxx the AXI DMA model; 0x8003_xxxx, 0x8010_xxxx,
// 0x8011_xxxx the core over AXI-Lite (offset from 0x8000_0000); anything
// else reads 0.
//
// --selftest runs a short self-checking scenario with an internal sine and
// no driver (the CI smoke): edge trigger, record, readout through the DMA.
#include "Vcosim_top.h"
#include "verilated.h"

#include <poll.h>
#include <sys/mman.h>
#include <sys/socket.h>
#include <unistd.h>
#include <time.h>

#include <cerrno>
#include <cstdarg>
#include <cmath>
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <deque>
#include <string>
#include <vector>

static const uint64_t kDDR = 1ull << 30;

static uint64_t wall_ns() {
  timespec ts;
  clock_gettime(CLOCK_MONOTONIC, &ts);
  return uint64_t(ts.tv_sec) * 1000000000ull + uint64_t(ts.tv_nsec);
}

static bool read_full(int fd, void* p, size_t n) {
  uint8_t* b = static_cast<uint8_t*>(p);
  while (n) {
    ssize_t r = read(fd, b, n);
    if (r < 0 && errno == EINTR) continue;
    if (r <= 0) return false;
    b += r;
    n -= size_t(r);
  }
  return true;
}
static bool write_full(int fd, const void* p, size_t n) {
  const uint8_t* b = static_cast<const uint8_t*>(p);
  while (n) {
    ssize_t r = write(fd, b, n);
    if (r < 0 && errno == EINTR) continue;
    if (r <= 0) return false;
    b += r;
    n -= size_t(r);
  }
  return true;
}

// wide Verilator ports are arrays of 32-bit words
template <typename W> static void put_bits(W& w, int lsb, int width, uint64_t v) {
  for (int i = 0; i < width; i++) {
    int b = lsb + i;
    uint32_t m = 1u << (b & 31);
    if ((v >> i) & 1) w[b >> 5] |= m; else w[b >> 5] &= ~m;
  }
}
static uint64_t pack48(const uint16_t* s) {   // four 12-bit samples, oldest in bits 11:0
  uint64_t v = 0;
  for (int i = 0; i < 4; i++) v |= uint64_t(s[i] & 0xFFF) << (12 * i);
  return v;
}

struct Sim {
  Vcosim_top* top = nullptr;
  uint8_t* ddr = nullptr;
  int ctl = -1, sig = -1;
  FILE* log = nullptr;
  bool selftest = false;
  // time in 1/3 ns; next edges
  uint64_t now = 0, na = 15, nc = 6, nm = 5;
  uint64_t ccyc = 0, mcyc = 0, acyc = 0;
  uint64_t en_base = 0;     // tick of the trigger path's t = 0
  bool en_prev = false;
  // samples: ticks [blk0, blk0 + blkn) for four channels
  std::vector<uint16_t> blk;
  uint64_t blk0 = 0, blkn = 0;
  // ---- AXI-Lite master
  enum { L_IDLE, L_WR, L_RD } lst = L_IDLE;
  bool l_aw = false, l_w = false, l_done = false;
  uint32_t l_rdata = 0;
  // ---- HP0 / HP1 slaves
  struct Burst { uint64_t addr; int beats; int got; uint64_t due; };
  std::deque<Burst> q_aw0, q_aw1, r0;
  std::deque<uint64_t> b0, b1;           // response due cycles
  int r0_beat = 0;
  uint64_t stall_hp0w = 0, stall_hp1 = 0, stall_dma = 0;   // knob: cycles of ready low
  int lat_r = 12, lat_b = 4;
  // ---- AXI DMA (PG021 S2MM, direct register mode)
  uint32_t dma_cr = 0, dma_sr = 1, dma_da = 0, dma_len = 0, dma_off = 0;
  bool dma_run = false;
  bool irq_dma = false, irq_opl = false;
  uint64_t trigs = 0, dmas = 0;
  uint64_t start_ns = 0;

  void ev(const char* fmt, ...) __attribute__((format(printf, 2, 3)));

  // ---------------------------------------------------------------- samples
  uint16_t internal(int c, uint64_t tick) {
    // self-test signals: C1 a 1 MHz sine of ±1500 codes, the others mid-scale ramps
    double t = double(tick) * 0.5e-9;
    if (c == 0) return uint16_t(std::lround(2048.0 + 1500.0 * std::sin(2 * M_PI * 1e6 * t + 0.3)));
    return uint16_t(2048 + ((tick / 64 + c * 100) % 400));
  }
  void fetch(uint64_t t0, uint32_t n) {
    blk.assign(size_t(n) * 4, 2048);
    blk0 = t0; blkn = n;
    if (selftest || sig < 0) {
      for (int c = 0; c < 4; c++)
        for (uint32_t k = 0; k < n; k++) blk[size_t(c) * n + k] = internal(c, t0 + k);
      return;
    }
    uint8_t req[13];
    req[0] = 'S';
    memcpy(req + 1, &t0, 8);
    memcpy(req + 9, &n, 4);
    if (!write_full(sig, req, sizeof req) || !read_full(sig, blk.data(), blk.size() * 2)) {
      fprintf(stderr, "opl_cosim: sample request failed\n");
      exit(1);
    }
  }
  uint16_t code(int c, uint64_t tick) {
    if (tick < blk0 || tick >= blk0 + blkn) fetch(tick - tick % 8, 8 * 4096);
    return blk[size_t(c) * blkn + (tick - blk0)];
  }
  void set_adc(uint64_t cyc) {
    uint64_t t = 8 * cyc;
    int route = top->dbg_route & 3, mask = (top->dbg_route >> 4) & 15;
    int des = mask & 1 ? 0 : mask & 2 ? 1 : mask & 4 ? 2 : mask & 8 ? 3 : 0;
    uint16_t s[4][4];   // ADC0 I, ADC0 Q, ADC1 I, ADC1 Q
    for (int a = 0; a < 2; a++) {
      int ci = 2 * a, cq = 2 * a + 1;
      for (int l = 0; l < 4; l++) {
        if (route == 0 && (des == ci || des == cq)) {
          s[2 * a][l] = code(des, t + 2 * l);
          s[2 * a + 1][l] = code(des, t + 2 * l + 1);
        } else {
          s[2 * a][l] = code(ci, t + 2 * l);
          s[2 * a + 1][l] = code(cq, t + 2 * l);
        }
      }
    }
    top->adc0_i = pack48(s[0]);
    top->adc0_q = pack48(s[1]);
    top->adc1_i = pack48(s[2]);
    top->adc1_q = pack48(s[3]);
  }

  // ---------------------------------------------------------------- clocks
  void init() {
    top->aclk = top->cclk = top->mclk = 0;
    top->aresetn = 0;
    top->s_bready = 1; top->s_rready = 1;
    top->hp0_awready = 1; top->hp0_wready = 0; top->hp0_bvalid = 0; top->hp0_bresp = 0;
    top->hp0_arready = 1; top->hp0_rvalid = 0; top->hp0_rlast = 0; top->hp0_rresp = 0;
    top->hp1_awready = 1; top->hp1_wready = 0; top->hp1_bvalid = 0; top->hp1_bresp = 0;
    top->dma_tready = 0;
    set_adc(0);
    top->eval();
    for (int i = 0; i < 400; i++) step();
    top->aresetn = 1;
    top->eval();
    start_ns = wall_ns();
  }
  void reset_pl() {
    top->aresetn = 0;
    for (int i = 0; i < 400; i++) step();
    q_aw0.clear(); q_aw1.clear(); r0.clear(); b0.clear(); b1.clear(); r0_beat = 0;
    lst = L_IDLE;
    top->s_awvalid = top->s_wvalid = top->s_arvalid = 0;
    top->aresetn = 1;
    for (int i = 0; i < 40; i++) step();
    ev("{\"ev\":\"reset\",\"ccyc\":%llu}", (unsigned long long)ccyc);
  }
  void step() {
    uint64_t t = std::min(na, std::min(nc, nm));
    now = t;
    bool pa = false, pc = false, pm = false;
    // decide on the values before the edges
    if (na == t && !top->aclk) pa = true;
    if (nc == t && !top->cclk) pc = true;
    if (nm == t && !top->mclk) pm = true;
    bool a_aw = false, a_w = false, a_b = false, a_ar = false, a_r = false;
    uint32_t a_rdata = 0;
    if (pa) {
      a_aw = top->s_awvalid && top->s_awready;
      a_w = top->s_wvalid && top->s_wready;
      a_b = top->s_bvalid && top->s_bready;
      a_ar = top->s_arvalid && top->s_arready;
      a_r = top->s_rvalid && top->s_rready;
      a_rdata = top->s_rdata;
    }
    struct { bool aw, w, ar, wlast, w1aw, w1, t, tlast; uint64_t awaddr, araddr, w1addr; int arlen, awlen; } m{};
    uint32_t wbeat[4] = {0, 0, 0, 0};
    uint64_t w1data = 0, tdata = 0;
    if (pm) {
      m.aw = top->hp0_awvalid && top->hp0_awready;
      m.awaddr = top->hp0_awaddr; m.awlen = top->hp0_awlen;
      m.w = top->hp0_wvalid && top->hp0_wready;
      m.wlast = top->hp0_wlast;
      for (int i = 0; i < 4; i++) wbeat[i] = top->hp0_wdata[i];
      m.ar = top->hp0_arvalid && top->hp0_arready;
      m.araddr = top->hp0_araddr; m.arlen = top->hp0_arlen;
      m.w1aw = top->hp1_awvalid && top->hp1_awready;
      m.w1addr = top->hp1_awaddr;
      m.w1 = top->hp1_wvalid && top->hp1_wready;
      w1data = top->hp1_wdata;
      m.t = top->dma_tvalid && top->dma_tready;
      m.tlast = top->dma_tlast;
      tdata = top->dma_tdata;
    }
    if (na == t) { top->aclk = !top->aclk; na += 15; }
    if (nc == t) { top->cclk = !top->cclk; nc += 6; }
    if (nm == t) { top->mclk = !top->mclk; nm += 5; }
    top->eval();
    if (pa) {
      acyc++;
      if (lst == L_WR) {
        if (a_aw) { top->s_awvalid = 0; l_aw = true; }
        if (a_w) { top->s_wvalid = 0; l_w = true; }
        if (a_b) { l_done = true; lst = L_IDLE; }
      } else if (lst == L_RD) {
        if (a_ar) top->s_arvalid = 0;
        if (a_r) { l_rdata = a_rdata; l_done = true; lst = L_IDLE; }
      }
    }
    if (pc) {
      ccyc++;
      bool en = top->dbg_en;
      if (en && !en_prev) {
        en_base = 8 * (ccyc - 1);
        ev("{\"ev\":\"commit\",\"ccyc\":%llu,\"tick0\":%llu}", (unsigned long long)ccyc, (unsigned long long)en_base);
      }
      en_prev = en;
      if (top->dbg_trig) {
        trigs++;
        uint64_t tt = top->dbg_trig_time;
        ev("{\"ev\":\"trig\",\"n\":%llu,\"t\":%llu,\"tick\":%llu,\"auto\":%d,\"forced\":%d,\"ccyc\":%llu}",
           (unsigned long long)trigs, (unsigned long long)tt, (unsigned long long)(tt + en_base),
           top->dbg_trig_auto, top->dbg_trig_forced, (unsigned long long)ccyc);
      }
      if (top->dbg_frac_valid) ev("{\"ev\":\"frac\",\"n\":%llu,\"frac\":%d}", (unsigned long long)trigs, top->dbg_frac);
      set_adc(ccyc);
    }
    if (pm) { mcyc++; mem_edge(m.aw, m.awaddr, m.awlen, m.w, wbeat, m.wlast, m.ar, m.araddr, m.arlen,
                                m.w1aw, m.w1addr, m.w1, w1data, m.t, m.tlast, tdata); }
    top->eval();
    irq_edges();
  }
  void irq_edges() {
    bool d = (dma_sr & 0x1000) && (dma_cr & 0x1000);
    bool o = top->irq;
    if (d != irq_dma) { irq_dma = d; if (d) send_irq(0, 1); }
    if (o != irq_opl) { irq_opl = o; if (o) send_irq(5, 1); }
  }
  void send_irq(uint8_t line, uint8_t lvl) {
    if (sig < 0) return;
    uint8_t m[3] = {'I', line, lvl};
    write_full(sig, m, 3);
  }

  // ---------------------------------------------------------------- memory side
  void mem_edge(bool aw, uint64_t awaddr, int awlen, bool w, const uint32_t* wb, bool wlast, bool ar,
                uint64_t araddr, int arlen, bool w1aw, uint64_t w1addr, bool w1, uint64_t w1data, bool t,
                bool tlast, uint64_t tdata) {
    // HP0 writes: bursts in order, beats after their address
    if (aw) q_aw0.push_back({awaddr, awlen + 1, 0, 0});
    if (w && !q_aw0.empty()) {
      Burst& b = q_aw0.front();
      uint64_t a = b.addr + 16ull * uint64_t(b.got);
      if (a + 16 <= kDDR) memcpy(ddr + a, wb, 16);
      b.got++;
      if (b.got == b.beats || wlast) { q_aw0.pop_front(); b0.push_back(mcyc + uint64_t(lat_b)); }
    }
    top->hp0_bvalid = 0;
    if (!b0.empty() && b0.front() <= mcyc) { top->hp0_bvalid = 1; b0.pop_front(); }
    if (stall_hp0w) { stall_hp0w--; top->hp0_wready = 0; top->hp0_awready = 0; }
    else { top->hp0_awready = q_aw0.size() < 16; top->hp0_wready = !q_aw0.empty() || aw; }
    // HP0 reads: data after a fixed latency, beats back to back
    if (ar) r0.push_back({araddr, arlen + 1, 0, mcyc + uint64_t(lat_r)});
    top->hp0_rvalid = 0; top->hp0_rlast = 0;
    if (!r0.empty() && r0.front().due <= mcyc) {
      Burst& b = r0.front();
      uint64_t a = b.addr + 16ull * uint64_t(b.got);
      for (int i = 0; i < 4; i++) {
        uint32_t v = 0;
        if (a + 16 <= kDDR) memcpy(&v, ddr + a + 4 * i, 4);
        top->hp0_rdata[i] = v;
      }
      top->hp0_rvalid = 1;
      b.got++;
      if (b.got == b.beats) { top->hp0_rlast = 1; r0.pop_front(); }
    }
    top->hp0_arready = r0.size() < 8;
    // HP1 single-beat writes
    if (w1aw) q_aw1.push_back({w1addr, 1, 0, 0});
    if (w1 && !q_aw1.empty()) {
      uint64_t a = q_aw1.front().addr;
      if (a + 8 <= kDDR) memcpy(ddr + a, &w1data, 8);
      q_aw1.pop_front();
      b1.push_back(mcyc + uint64_t(lat_b));
    }
    top->hp1_bvalid = 0;
    if (!b1.empty() && b1.front() <= mcyc) { top->hp1_bvalid = 1; b1.pop_front(); }
    if (stall_hp1) { stall_hp1--; top->hp1_wready = 0; top->hp1_awready = 0; }
    else { top->hp1_awready = q_aw1.size() < 16; top->hp1_wready = !q_aw1.empty() || w1aw; }
    // AXI DMA S2MM
    if (t && dma_run) {
      if (dma_off + 8 <= dma_len) {
        uint64_t a = uint64_t(dma_da) + dma_off;
        if (a + 8 <= kDDR) memcpy(ddr + a, &tdata, 8);
      }
      dma_off += 8;
      if (tlast || dma_off >= dma_len) {
        dma_run = false;
        dma_sr |= 0x1002;
        dmas++;
        ev("{\"ev\":\"dma\",\"n\":%llu,\"da\":%u,\"bytes\":%u,\"len\":%u,\"tlast\":%d}", (unsigned long long)dmas,
           dma_da, dma_off, dma_len, tlast ? 1 : 0);
      }
    }
    if (stall_dma) { stall_dma--; top->dma_tready = 0; }
    else top->dma_tready = dma_run;
  }
  uint32_t dma_read(uint32_t off) {
    switch (off) {
      case 0x30: return dma_cr;
      case 0x34: return (dma_sr & ~3u) | ((dma_cr & 1) ? 0 : 1) | (dma_run ? 0 : 2);
      case 0x48: return dma_da;
      case 0x58: return dma_run ? dma_len : dma_off;
      default: return 0;
    }
  }
  void dma_write(uint32_t off, uint32_t v) {
    switch (off) {
      case 0x30:
        if (v & 4) { dma_cr = 0; dma_sr = 1; dma_run = false; dma_off = 0; }
        else dma_cr = v;
        break;
      case 0x34: dma_sr &= ~(v & 0x7000); break;
      case 0x48: dma_da = v; break;
      case 0x58:
        dma_len = v & 0x3FFFFFF;
        if ((dma_cr & 1) && dma_len) { dma_run = true; dma_off = 0; dma_sr &= ~2u; }
        break;
      default: break;
    }
    irq_edges();
  }

  // ---------------------------------------------------------------- registers
  uint32_t lite(bool wr, uint32_t addr, uint32_t v) {
    uint32_t off = addr - 0x80000000u;
    l_done = false; l_aw = l_w = false;
    if (wr) {
      lst = L_WR;
      top->s_awaddr = off; top->s_awvalid = 1; top->s_wdata = v; top->s_wstrb = 0xF; top->s_wvalid = 1;
    } else {
      lst = L_RD;
      top->s_araddr = off; top->s_arvalid = 1;
    }
    top->eval();
    for (uint64_t guard = 0; !l_done; guard++) {
      step();
      if (guard > 100000) { fprintf(stderr, "opl_cosim: AXI-Lite access %08x hung\n", addr); exit(1); }
    }
    return l_rdata;
  }
  uint32_t reg_read(uint32_t addr) {
    uint32_t w = addr & 0xFFFF0000u;
    if (w == 0x80010000u) return dma_read(addr & 0xFFFF);
    if (w == 0x80030000u || w == 0x80100000u || w == 0x80110000u) return lite(false, addr & ~3u, 0);
    return 0;
  }
  void reg_write(uint32_t addr, uint32_t v) {
    uint32_t w = addr & 0xFFFF0000u;
    if (w == 0x80010000u) { dma_write(addr & 0xFFFF, v); return; }
    if (w == 0x80030000u || w == 0x80100000u || w == 0x80110000u) lite(true, addr & ~3u, v);
  }
  std::string knob(const std::string& s) {
    unsigned long long n = 0;
    char what[32] = {0};
    if (sscanf(s.c_str(), "stall %31s %llu", what, &n) == 2) {
      if (!strcmp(what, "hp0w")) stall_hp0w = n;
      else if (!strcmp(what, "hp1")) stall_hp1 = n;
      else if (!strcmp(what, "dma")) stall_dma = n;
      else return "unknown stall " + std::string(what);
      ev("{\"ev\":\"knob\",\"stall\":\"%s\",\"cycles\":%llu}", what, n);
      return "ok";
    }
    if (sscanf(s.c_str(), "run %llu", &n) == 1) {   // advance n cclk cycles now
      uint64_t end = ccyc + n;
      while (ccyc < end) step();
      return "ok";
    }
    if (s == "stats") {
      char b[256];
      double secs = double(wall_ns() - start_ns) * 1e-9;
      snprintf(b, sizeof b, "ccyc %llu sim_ms %.3f wall_s %.1f ratio %.1f trigs %llu dmas %llu",
               (unsigned long long)ccyc, double(ccyc) * 4e-6, secs, secs / (double(ccyc) * 4e-9 + 1e-12),
               (unsigned long long)trigs, (unsigned long long)dmas);
      return b;
    }
    return "unknown knob";
  }
  void serve() {
    pollfd p{ctl, POLLIN, 0};
    uint64_t last_stat = wall_ns();
    for (;;) {
      for (int i = 0; i < 256; i++) step();
      if (poll(&p, 1, 0) > 0) {
        if (!handle()) return;
        // serve back-to-back requests without running the clocks in between
        while (poll(&p, 1, 0) > 0) if (!handle()) return;
      }
      uint64_t w = wall_ns();
      if (w - last_stat > 10000000000ull) {
        last_stat = w;
        ev("{\"ev\":\"stats\",\"ccyc\":%llu,\"sim_us\":%.1f,\"wall_s\":%.1f}", (unsigned long long)ccyc,
           double(ccyc) * 4e-3, double(w - start_ns) * 1e-9);
      }
    }
  }
  bool handle() {
    uint8_t op;
    if (!read_full(ctl, &op, 1)) return false;
    uint32_t a = 0, v = 0;
    switch (op) {
      case 'R':
        if (!read_full(ctl, &a, 4)) return false;
        v = reg_read(a);
        return write_full(ctl, &v, 4);
      case 'W': {
        if (!read_full(ctl, &a, 4) || !read_full(ctl, &v, 4)) return false;
        reg_write(a, v);
        uint8_t z = 0;
        return write_full(ctl, &z, 1);
      }
      case 'Z': {
        reset_pl();
        uint8_t z = 0;
        return write_full(ctl, &z, 1);
      }
      case 'T': {
        uint64_t r[2] = {ccyc, wall_ns() - start_ns};
        return write_full(ctl, r, 16);
      }
      case 'X': {
        uint16_t n;
        if (!read_full(ctl, &n, 2)) return false;
        std::string s(n, '\0');
        if (!read_full(ctl, s.data(), n)) return false;
        std::string r = knob(s);
        uint16_t rn = uint16_t(r.size());
        return write_full(ctl, &rn, 2) && write_full(ctl, r.data(), rn);
      }
      default:
        fprintf(stderr, "opl_cosim: unknown request %02x\n", op);
        return false;
    }
  }
  int run_selftest();
};

void Sim::ev(const char* fmt, ...) {
  char b[512];
  va_list ap;
  va_start(ap, fmt);
  int n = vsnprintf(b, sizeof b, fmt, ap);
  va_end(ap);
  if (n < 0) return;
  if (n >= int(sizeof b)) n = sizeof b - 1;
  if (log) { fprintf(log, "%s\n", b); fflush(log); }
  if (sig >= 0) {
    uint8_t h[3] = {'E', uint8_t(n & 0xFF), uint8_t(n >> 8)};
    write_full(sig, h, 3);
    write_full(sig, b, size_t(n));
  }
}

// ---------------------------------------------------------------- self-test
int Sim::run_selftest() {
  int fails = 0;
  auto check = [&](bool ok, const char* what, long long want, long long got) {
    printf("%s %-44s expected %-12lld measured %lld\n", ok ? "PASS" : "FAIL", what, want, got);
    if (!ok) fails++;
  };
  const uint32_t O = 0x80100000u;
  check(reg_read(0x80030008) >> 8 == 0x4F, "compat ACQ 0x002 bits 15:8 (open image)", 0x4F, reg_read(0x80030008) >> 8);
  check(reg_read(O) == 0x4F504C31, "ID_MAGIC", 0x4F504C31, reg_read(O));
  reg_write(0x8003013C, 1);  // compat 0x04F: ISERDES reset, then released
  reg_write(0x8003013C, 0);
  for (int i = 0; i < 50; i++) step();
  check((reg_read(0x8003007C) >> 12 & 1) == 1, "compat 0x01F bit 12 (core alive)", 1, reg_read(0x8003007C) >> 12 & 1);
  // edge trigger, C1 rising through mid-scale, 2 GSa/s, interpolated
  const uint32_t L = 2048, PRE = 600, POST = 400, GROUPS = 64;
  reg_write(O + 0x208, 0x10);                  // route 0, C1
  reg_write(O + 0x20C, 1);                     // D = 1
  reg_write(O + 0x230, PRE);
  reg_write(O + 0x234, POST);
  reg_write(O + 0x238, 1);
  reg_write(O + 0x240, 0x20000000);
  reg_write(O + 0x244, GROUPS);
  reg_write(O + 0x450, L);                     // CMP0 A hi
  reg_write(O + 0x454, L - 64);                // CMP0 A lo
  reg_write(O + 0x408, 1);                     // CLOSE = C1 A rise
  reg_write(O + 0x418, 0x20);                  // any, always armed
  reg_write(O + 0x400, 1 | (1 << 10));         // interval engine, interpolate C1 A rising
  reg_write(O + 0x114, 1);                     // IRQ enable acq_done
  reg_write(O + 0x200, 1);                     // ARM
  int st = 0;
  for (int i = 0; i < 4000 && st != 4; i++) { for (int k = 0; k < 64; k++) step(); st = reg_read(O + 0x204) & 7; }
  check(st == 4, "ACQ_STATUS.state after arm (DONE)", 4, st);
  uint32_t pos = reg_read(O + 0x258), frac = reg_read(O + 0x260), words = reg_read(O + 0x268);
  check(irq_opl, "open-plane interrupt (acq_done)", 1, irq_opl);
  check(words == PRE + POST + 1 || words > PRE + POST, "ACQ_WORDS covers PRE + POST + 1", PRE + POST + 1, words);
  // readout of the window around the trigger through the DMA
  uint32_t first = (pos + GROUPS * 32 - PRE) % (GROUPS * 32), count = PRE + POST + 1;
  dma_write(0x30, 4);
  dma_write(0x48, 0x1E800000);
  dma_write(0x30, 0x5001);
  dma_write(0x58, (count * 2 + 7) & ~7u);
  reg_write(O + 0xC04, 0);
  reg_write(O + 0xC08, first);
  reg_write(O + 0xC0C, count);
  reg_write(O + 0xC10, 0x100);
  reg_write(O + 0xC00, 1);
  for (int i = 0; i < 20000 && dma_run; i++) step();
  check(!dma_run && (dma_sr & 0x1000), "DMA complete (IOC)", 1, !dma_run);
  const uint16_t* w = reinterpret_cast<const uint16_t*>(ddr + 0x1E800000);
  int x0 = w[PRE - 1] >> 4, x1 = w[PRE] >> 4;
  check(x0 < int(L) && x1 >= int(L), "trigger word: x[-1] < L <= x[0]", L, x1);
  int want = std::min(16, (int(L) - x0) * 16 / std::max(1, x1 - x0));
  check((frac >> 8 & 1) && int(frac & 31) == want, "TRIG_FRAC (1/16 sample, 0..16)", want, frac & 31);
  // a deep record at 2 GSa/s: the record writer keeps up (no group lost)
  reg_write(O + 0x230, 200000);
  reg_write(O + 0x234, 200000);
  reg_write(O + 0x244, 12600);
  reg_write(O + 0x200, 1);
  st = 0;
  for (int i = 0; i < 200000 && st != 4; i++) { for (int k = 0; k < 64; k++) step(); st = reg_read(O + 0x204) & 7; }
  check(st == 4, "deep record (400001 words at 2 GSa/s) DONE", 4, st);
  check(reg_read(O + 0x248) == 0, "REC_LOST after the deep record (groups)", 0, reg_read(O + 0x248));
  check((reg_read(O + 0x110) & 1) == 0, "SYS_FAULT rec_overflow", 0, reg_read(O + 0x110) & 1);
  printf("selftest: %s (%llu cclk cycles, %.1f s)\n", fails ? "FAILURES" : "all passed", (unsigned long long)ccyc,
         double(wall_ns() - start_ns) * 1e-9);
  return fails ? 1 : 0;
}

int main(int argc, char** argv) {
  Verilated::commandArgs(argc, argv);
  Sim s;
  const char* logp = nullptr;
  for (int i = 1; i < argc; i++) {
    if (!strcmp(argv[i], "--selftest")) s.selftest = true;
    else if (!strcmp(argv[i], "--log") && i + 1 < argc) logp = argv[++i];
  }
  if (logp) s.log = fopen(logp, "w");
  int memfd = -1;
  if (!s.selftest) { s.ctl = 3; s.sig = 4; memfd = 5; }
  if (memfd >= 0) {
    void* p = mmap(nullptr, kDDR, PROT_READ | PROT_WRITE, MAP_SHARED, memfd, 0);
    if (p == MAP_FAILED) { perror("opl_cosim: mmap DDR"); return 1; }
    s.ddr = static_cast<uint8_t*>(p);
  } else {
    void* p = mmap(nullptr, kDDR, PROT_READ | PROT_WRITE, MAP_PRIVATE | MAP_ANONYMOUS | MAP_NORESERVE, -1, 0);
    if (p == MAP_FAILED) { perror("opl_cosim: mmap"); return 1; }
    s.ddr = static_cast<uint8_t*>(p);
  }
  s.top = new Vcosim_top;
  s.init();
  int rc = 0;
  if (s.selftest) rc = s.run_selftest();
  else s.serve();
  s.top->final();
  delete s.top;
  return rc;
}
