// core.mjs — the core web client's own e2e suites (webclient/e2e in
// open-dso-core) against the simulated HD's web UI: the operator workflows,
// each source made by simlab's generator in place of the bench's FPGA, and a
// bounded run of the operator fuzzer.
//
// The workflows were written for the two-channel CML at its bench; what
// changes here is only the signal source and the skips listed in SKIP, each
// with the reason the HD or the simulator cannot do it. Nothing in the core
// is edited: the workflows, the operator primitives and the fuzzer are
// imported or run as they are.
import { spawn } from "node:child_process";
import { mkdirSync, readFileSync, existsSync } from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { CORE, SCOPE, Guard, appGet, bus, labState, resetScope, scpi, signal, until, waitServing } from "./bench.mjs";

const E2E = path.join(CORE, "webclient", "e2e");
const imp = (f) => import(pathToFileURL(path.join(E2E, f)).href);

// PRBS7 (x^7 + x^6 + 1) from seed 0x7f, packed MSB first: 16 bytes carry
// the 127-bit sequence and one bit of the next.
function prbs7Hex() {
  let s = 0x7f;
  const bits = [];
  for (let i = 0; i < 128; i++) {
    const b = ((s >> 6) ^ (s >> 5)) & 1;
    s = ((s << 1) | b) & 0x7f;
    bits.push(b);
  }
  let hex = "";
  for (let i = 0; i < 128; i += 8) hex += bits.slice(i, i + 8).reduce((a, b) => (a << 1) | b, 0).toString(16).padStart(2, "0");
  return hex;
}

const OFF = "dc,0,0,0";
// quiet sets the given inputs to 0 V (a source leaves no other bench's
// signal on the channels it does not use).
const quiet = async (...chs) => { for (const c of chs) await signal(c, OFF); };
// SOURCES are the bench's FPGA sources as the simulator's generator makes
// them (simd's siggen: a protocol frame repeats from the trigger point).
export const SOURCES = {
  // a 1 MHz 3.3 V square on C1, C2 idle
  tone1M: async () => { await signal("C1", "square,1e6,3.3,1.65"); await quiet("C2", "C3", "C4"); },
  // SPI mode 0 at 200 kHz, SCLK on C1 and MOSI on C2, "55 AA" first;
  // the CS line on C3 (the CML had no third input). A 16-byte message
  // keeps the clock running for most of each repetition
  spi: async () => {
    await bus({ kind: "spi", lines: { clk: "C1", mosi: "C2", cs: "C3" }, rate: 200e3, vpp: 3.3, offset: 1.65, opts: { data: "55aa4869c33c0ff0a55a4869c33c0ff0", mode: "0" } });
    await quiet("C4");
  },
  // UART 8N1 at 115200, "Hi" then 55 AA, on C1 and the same stream on C2
  uart: async () => {
    await bus({ kind: "uart", lines: { tx: "C1" }, rate: 115200, vpp: 3.3, offset: 1.65, opts: { data: "486955aa0d0a" } });
    const s = (await labState()).signals.C1;
    await signal("C2", s);
    await quiet("C3", "C4");
  },
  // a 2.5 kHz pulse train, 100 µs wide (25 %), with a narrow extra pulse
  // in the low part of every 16th period as the width violation
  maskviol: async () => { await signal("C1", "pulse,2500,3.3,1.65,width=1e-4,glitch=16:2e-5:3.3"); await quiet("C2", "C3", "C4"); },
  // a repetitive burst: 150 MHz for 45 periods (300 ns) then 45 off; the
  // FPGA's 50/150/250 MHz steps are one frequency here (the generator has
  // one waveform per channel)
  burst: async () => { await signal("C1", "sine,150e6,2,0,burst=45:45"); await quiet("C2", "C3", "C4"); },
  // PRBS7 NRZ data at 2 Mb/s on C1 (a UART frame of PRBS bytes back to back,
  // its start and stop bits the only fixed bits) and a 2 MHz clock on C2
  prbs2M: async () => {
    await bus({ kind: "uart", lines: { tx: "C1" }, rate: 2e6, vpp: 3.3, offset: 1.65, opts: { data: prbs7Hex(), idle: "5e-7" } });
    await signal("C2", "square,2e6,3.3,1.65");
    await quiet("C3", "C4");
  },
};

// SKIP: workflows the HD or its simulator cannot do, by id, with the reason.
// The simulator's protocol frames start at the trigger point with the
// lines idle (SPI's first clock edge 1.5 bit times later, UART's start bit
// 2 bit times later): autoset sizes its window to the bit time, so the
// window around the trigger shows flat lines where the bench's FPGA source
// triggered on an edge. The UART-based PRBS source shares it.
const IDLE_AT_TRIGGER = "the simulator's protocol frames start idle at the trigger point, so the window autoset picks shows flat lines (the bench triggered on an edge)";
export const SKIP = Object.fromEntries([
  ...Array.from({ length: 20 }, (_, i) => [`S${i + 1}`, IDLE_AT_TRIGGER]),
  ...Array.from({ length: 12 }, (_, i) => [`U${i + 1}`, IDLE_AT_TRIGGER]),
  ...Array.from({ length: 24 }, (_, i) => [`P${i + 1}`, IDLE_AT_TRIGGER]),
]);

// TIME_SCALE stretches the operator harness's waits for an observable
// state (a control's effect, a reading, autoset's end): the app runs under
// qemu, several times slower than on the unit (an autoset takes about 30 s
// against the harness's 13–16 s), and how fast the scope gets there is not
// what these workflows test. Fixed pauses inside a workflow stay as they are.
const TIME_SCALE = +(process.env.E2E_TIME_SCALE || 3);
let patched = false;
function stretch(Op) {
  if (patched || TIME_SCALE === 1) return;
  patched = true;
  const P = Op.prototype, settle = P._settle, readUntil = P.readUntil, waitMeas = P.waitMeas;
  P._settle = function (effect, timeout, msg) { return settle.call(this, effect, timeout * TIME_SCALE, msg); };
  P.readUntil = function (fn, timeout, msg) { return readUntil.call(this, fn, timeout * TIME_SCALE, msg); };
  P.waitMeas = function (ch, key, { timeout = 6000 } = {}) { return waitMeas.call(this, ch, key, { timeout: timeout * TIME_SCALE }); };
  // A workflow that clicks AUTOSET itself takes the first reading as the
  // click's effect, which on the unit comes after the autoset; here the
  // autoset runs on for seconds and the readings change under it. The
  // click's effect is the reading once the page says the autoset is done.
  const clickExpect = P.clickExpect;
  P.clickExpect = async function (id, effect, opts = {}) {
    if (id !== "autoset") return clickExpect.call(this, id, effect, opts);
    const done = async () => (await this.page.evaluate(() => typeof autosetBusy === "undefined" || !autosetBusy)) && (await effect());
    await clickExpect.call(this, id, async () => this.page.evaluate(() => typeof autosetBusy !== "undefined" && autosetBusy).then((b) => b || effect()), opts);
    return this._settle(done, (opts.timeout || 4000) + 16000, `autoset never finished with its effect (${opts.why || ""})`);
  };
}

// NEEDS: workflows that need a feature the scope's caps may say it lacks;
// a step is skipped with the caps' own reason when the feature is not ok.
const NEEDS = { M5: "mask", M6: "mask", T25: "mask", M7: "zones", B5: "zones" };
// TRIGGER_NEEDS: workflows that pick a trigger type the caps may not list.
const TRIGGER_NEEDS = { M4: "pulse" };
async function lacks(id) {
  const f = NEEDS[id], tt = TRIGGER_NEEDS[id];
  if (!f && !tt) return null;
  const caps = await appGet("/api/caps");
  if (tt) return caps.trigger?.types?.includes(tt) ? null : `the HD's caps list the trigger types ${caps.trigger?.types?.join(", ")}, not ${tt}`;
  const st = caps.features?.[f];
  return st && !st.ok ? `the HD's caps: ${f} — ${st.why || "not available"}` : null;
}

// launchOp opens the web UI as the core's operator harness does, in the
// harness's own browser (bench.mjs: EGL WebGL, a real-disk TMPDIR).
async function launchOp(browser) {
  const { Op } = await imp("operator.mjs");
  stretch(Op);
  const page = await browser.newPage({ viewport: { width: 1500, height: 950 } });
  const pageErrors = [];
  page.on("pageerror", (e) => pageErrors.push(e.message));
  await page.goto(SCOPE, { waitUntil: "domcontentloaded", timeout: 20000 });
  await page.waitForFunction(() => typeof frame !== "undefined" && frame && frame.seq > 0, null, { timeout: 25000 });
  // the HD's caps (four channels) before any workflow reads a control
  await page.waitForFunction(() => typeof capsServed !== "undefined" && capsServed, null, { timeout: 25000 });
  return new Op(browser, page, pageErrors, SCOPE);
}

// hdReset adds to the operator's reset what a four-channel scope needs:
// the scope's C1 and C2 on and C3 and C4 off, as the CML's two channels
// always were. The web UI's channel toggles only show or hide a trace on
// the page (they neither switch nor follow the scope's channel), so the
// bench sets the channels over SCPI, as it sets the generator; the page's
// C3 and C4 traces are hidden with their toggles.
async function hdReset(op) {
  for (const [c, on] of [["C1", "ON"], ["C2", "ON"], ["C3", "OFF"], ["C4", "OFF"]]) await scpi(`${c}:TRA ${on}`);
  await op.reset();
  await op.page.evaluate(() => {
    for (const id of ["tC3", "tC4"]) { const b = document.getElementById(id); if (b && b.classList.contains("on")) b.click(); }
  });
}

// operatorSuite: one step per workflow, grouped by source; a source's first
// step sets the generator.
export async function operatorSuite() {
  const { WORKFLOWS } = await imp("workflows.mjs");
  const want = (process.env.SOURCES || Object.keys(SOURCES).join(",")).split(",");
  const ids = process.env.WORKFLOWS ? process.env.WORKFLOWS.split(",") : null;
  const list = WORKFLOWS.filter((w) => want.includes(w.source) && (!ids || ids.includes(w.id)));
  let source = null;
  const steps = list.map((w) => ({
    id: w.id, name: `${w.source}: ${w.name}`,
    run: async (t) => {
      if (SKIP[w.id]) t.skip(SKIP[w.id]);
      const why = await lacks(w.id);
      if (why) t.skip(why);
      if (t.source !== w.source) { await SOURCES[w.source](); t.source = w.source; source = w.source; }
      await hdReset(t.op);
      await w.run(t.op);
      t.ok(true, "workflow done");
    },
  }));
  return {
    name: "operator", minLeft: 120, stepLeft: 60,
    steps: () => steps,
    async open(browser, ctx) { await resetScope(); return { ...ctx, browser, op: await launchOp(browser), source: null }; },
    // a new run: the scope's state is fresh and the generator as simlab
    // remembered it; the page reloads
    async reload(ctx) { await ctx.op.page.close().catch(() => {}); await resetScope(); ctx.op = await launchOp(ctx.browser); ctx.source = null; source = null; },
    async shot(ctx, file) { await ctx.op.page.screenshot({ path: file }); },
    async close(ctx) { await ctx.op.page.close(); },
  };
}

// fuzzSuite: the core's fuzz.mjs as it is, bounded (FUZZ_ITERS, default 40;
// FUZZ_SEED, default the repetition), on the tone source. One step; a
// restart during it is told apart by the run guard and the step runs again
// on the next run.
export async function fuzzSuite() {
  const iters = +(process.env.FUZZ_ITERS || 40);
  const step = {
    id: "F1", name: `fuzz ${iters} iterations`,
    run: async (t) => {
      await resetScope();
      await SOURCES.tone1M();
      const seed = +(process.env.FUZZ_SEED || t.rep);
      // absolute: the fuzzer runs in the core's e2e directory
      const out = path.resolve(t.out, `fuzz-${t.rep}-seed${seed}-${Date.now()}`);
      mkdirSync(out, { recursive: true });
      const code = await new Promise((resolve) => {
        const p = spawn(process.execPath, [path.join(E2E, "fuzz.mjs")], {
          cwd: E2E, stdio: ["ignore", "pipe", "pipe"],
          env: { ...process.env, SCOPE_URL: SCOPE, ITERS: String(iters), SEED: String(seed), OUT: out },
        });
        let tail = "";
        p.stdout.on("data", (d) => { tail = (tail + d).slice(-4000); });
        p.stderr.on("data", (d) => { tail = (tail + d).slice(-4000); });
        p.on("close", (c) => { t.info(tail.trim().split("\n").slice(-3).join(" / ")); resolve(c); });
      });
      const f = path.join(out, "findings.jsonl");
      const findings = existsSync(f) ? readFileSync(f, "utf8").split("\n").filter(Boolean).map((l) => JSON.parse(l)) : [];
      for (const x of findings) t.info(`finding iter ${x.iter} ${x.action}(${x.detail}): ${x.errs.map((e) => e.inv + " " + e.detail).join("; ").slice(0, 300)}`);
      t.ok(findings.length === 0, `seed ${seed}: ${findings.length} findings (exit ${code}) in ${out}`);
    },
  };
  return {
    name: "fuzz", minLeft: Math.min(280, 60 + iters * 4),
    steps: () => [step],
    async open(browser, ctx) { return { ...ctx }; },
    async reload() {},
    async close() {},
  };
}

export { Guard, until, waitServing };
