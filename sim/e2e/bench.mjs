// bench.mjs — what every simlab e2e suite shares: simlab's API (its run
// state, the generator, keys and knobs), the app's SCPI socket, a headless
// Chromium that keeps WebGL, and the run guard that tells a simulator
// restart apart from a failure.
//
// A simulator run lasts at most 300 s and simlab starts the next one in
// place, so the scope is down for a while every few minutes. Suites never
// sleep their way past that: they wait for simlab's run state to read
// "serving" (the run's app answers its web API and SCPI) and note the run's
// generation; a step that sees the generation move was cut by a restart,
// which the runner retries on the next run instead of counting as a failure.
import { existsSync, mkdirSync, readFileSync } from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

export const HERE = path.dirname(fileURLToPath(import.meta.url));
export const REPO = path.resolve(HERE, "../..");
// The core checkout the app builds against (app/go.mod's replace).
export const CORE = process.env.CORE_DIR || path.resolve(REPO, "../open-dso-core");
export const LAB = process.env.SIMLAB_URL || "http://127.0.0.1:8090";
export const SCOPE = process.env.SCOPE_URL || "http://127.0.0.1:8080";
export const SCPI_ADDR = { host: "127.0.0.1", port: +(process.env.SCPI_PORT || 5025) };

// RunRestarted is thrown when the simulator run a step started on is gone.
export class RunRestarted extends Error {
  constructor(from, to) { super(`the simulator run restarted (run ${from} → ${to})`); this.restart = true; }
}

export const delay = (ms) => new Promise((r) => setTimeout(r, ms));

// ---- simlab's API -----------------------------------------------------------
export async function labGet(p, { timeout = 10000 } = {}) {
  const r = await fetch(LAB + p, { signal: AbortSignal.timeout(timeout) });
  if (!r.ok) throw new Error(`${p}: ${r.status} ${await r.text()}`);
  return r.json();
}
export async function labPost(p, body, { timeout = 30000 } = {}) {
  const r = await fetch(LAB + p, {
    method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body),
    signal: AbortSignal.timeout(timeout),
  });
  const j = await r.json().catch(() => ({ ok: false, err: r.statusText }));
  if (!j.ok) throw new Error(`${p} ${JSON.stringify(body)}: ${j.err}`);
  return j;
}
export const labState = () => labGet("/api/state");

// until polls fn (which returns a value, or null/false/undefined for "not
// yet") every `every` ms until it returns a value or `timeout` ms pass.
export async function until(fn, timeout, what, every = 250) {
  const end = Date.now() + timeout;
  let last;
  for (;;) {
    try { last = await fn(); } catch (e) { if (e.restart) throw e; last = undefined; }
    if (last != null && last !== false) return last;
    if (Date.now() > end) throw new Error(`timed out after ${timeout} ms: ${what}`);
    await delay(every);
  }
}

// waitServing waits until simlab's run serves and has at least minLeft
// seconds before its time limit. A run with less left is restarted (a
// fresh one starts at once if the simulator is free; otherwise it queues
// behind the other runs sharing it), so a suite never starts on a run
// about to end. Returns the state; its run.runs is the generation a guard
// follows. The wait is long by design: other agents' runs hold the shared
// simulator lock for up to 300 s each.
export async function waitServing({ minLeft = 0, timeout = 30 * 60e3, log = () => {} } = {}) {
  const t0 = Date.now();
  let restarted = 0;
  for (;;) {
    const st = await labState().catch(() => null);
    const run = st?.run;
    if (run && run.serving && (!minLeft || !run.secs || run.left >= minLeft)) {
      log(`run ${run.runs} serving (${run.left}s left) after ${((Date.now() - t0) / 1000).toFixed(0)} s`);
      return st;
    }
    if (run && run.serving && run.secs && run.left < minLeft && restarted !== run.runs) {
      log(`run ${run.runs} has ${run.left}s left, below ${minLeft}s: restarting it`);
      restarted = run.runs;
      await labPost("/api/restart", {});
      await until(async () => (await labState()).run.runs !== run.runs, 120000, "the restart never began a new run");
    }
    if (Date.now() - t0 > timeout) throw new Error(`simlab never served: ${JSON.stringify(run)}`);
    await delay(500);
  }
}

// Guard follows one run: check() throws RunRestarted once simlab no longer
// serves that run. A suite checks it when a step fails, so a step cut by
// a restart is told apart from a real failure.
export class Guard {
  constructor(st) { this.runs = st.run.runs; }
  async check() {
    const st = await labState().catch(() => null);
    if (!st) return; // simlab itself unreachable: not a restart, the step's own error stands
    if (st.run.runs !== this.runs || !st.run.serving) throw new RunRestarted(this.runs, st.run.serving ? st.run.runs : `${st.run.runs} (${st.run.state}, not serving)`);
  }
}

export const signal = (ch, spec) => labPost("/api/signal", { ch, spec });
export const bus = (b) => labPost("/api/bus", b);
export const key = (k) => labPost("/api/key", { key: k });
export const knob = (k, steps) => labPost("/api/knob", { knob: k, steps });

// ---- SCPI on the app's raw socket ----------------------------------------------
// scpi sends one line and, for a query, returns the reply line.
export function scpi(line, { timeout = 8000 } = {}) {
  return new Promise((resolve, reject) => {
    const s = net.createConnection(SCPI_ADDR);
    let buf = "";
    const done = (err, v) => { clearTimeout(t); s.destroy(); err ? reject(err) : resolve(v); };
    const t = setTimeout(() => done(new Error(`SCPI ${line}: no reply in ${timeout} ms`)), timeout);
    s.on("error", (e) => done(new Error(`SCPI ${line}: ${e.message}`)));
    s.on("connect", () => {
      s.write(line + "\n");
      if (!line.includes("?")) { s.end(); setTimeout(() => done(null, ""), 50); }
    });
    s.on("data", (d) => { buf += d; const i = buf.indexOf("\n"); if (i >= 0) done(null, buf.slice(0, i).trim()); });
  });
}
// scpiNum reads a query's last number ("C1:VDIV 2.00E-01V" → 0.2).
export async function scpiNum(q) {
  const r = await scpi(q);
  const m = r.match(/[-+]?\d*\.?\d+(?:[eE][-+]?\d+)?(?=\s*[A-Za-z%]*\s*$)/);
  return m ? parseFloat(m[0]) : NaN;
}

// resetScope brings the scope to its defaults (*RST) before a suite: the
// simulated p12 keeps the scope's settings across runs, so a suite would
// otherwise start from whatever the last one (or a fuzz run) left.
export async function resetScope() {
  await scpi("*RST");
  await until(async () => (await scpi("*OPC?")).trim() === "1", 30000, "*OPC? after *RST");
}

// ---- the app's own API (for harness state only, never for a result) -------
export async function appGet(p, { timeout = 8000 } = {}) {
  const r = await fetch(SCOPE + p, { signal: AbortSignal.timeout(timeout) });
  if (!r.ok) throw new Error(`${p}: ${r.status}`);
  return r.json();
}

// ---- the browser ---------------------------------------------------------------
// findPlaywright is the core harness's locator.
export async function playwright() {
  const { findPlaywright } = await import(pathToFileURL(path.join(CORE, "webclient/scope_po.mjs")).href);
  const p = findPlaywright();
  if (!p) return null;
  if (!process.env.PLAYWRIGHT_BROWSERS_PATH && process.env.HOME)
    process.env.PLAYWRIGHT_BROWSERS_PATH = path.join(process.env.HOME, ".cache/ms-playwright");
  return (await import(p)).default;
}

// launchChromium starts headless Chromium for a WebGL page: its scratch
// directory on a real disk (Chromium fails in odd ways on a small tmpfs)
// and EGL GL, whose context survives where SwiftShader's GL drops it.
export async function launchChromium() {
  const pw = await playwright();
  if (!pw) return null;
  const tmp = process.env.E2E_TMP || path.join(REPO, "build", "e2e-tmp");
  mkdirSync(tmp, { recursive: true });
  process.env.TMPDIR = tmp;
  return pw.chromium.launch({ headless: true, args: ["--no-sandbox", "--use-gl=egl", "--ignore-gpu-blocklist"] });
}

// ---- reporting -----------------------------------------------------------------
// load is the host's 1-minute load average and its CPUs, logged with every run.
export function load() {
  const [l1] = os.loadavg();
  return { load1: +l1.toFixed(2), cpus: os.cpus().length, nice: os.getPriority() };
}

export function readJSONL(p) {
  if (!existsSync(p)) return [];
  return readFileSync(p, "utf8").split("\n").filter(Boolean).map((l) => JSON.parse(l));
}
