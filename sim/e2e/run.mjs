// run.mjs — runs simlab's e2e suites against a running simlab (make simlab,
// or make simlab-e2e, which starts one) and records every repetition.
//
//   node sim/e2e/run.mjs [simlab|operator|fuzz ...]
//
// Environment (harness flags, none reach the device):
//   REPEAT=N      repetitions of each suite (default 1)
//   OUT=DIR       results: DIR/results.jsonl, one line per repetition and
//                 step, and DIR/<suite>-<n>/ screenshots (default build/e2e)
//   NAIVE=1       the suites as first written: no wait for "serving", no
//                 time-left floor, no retry of a step a restart cut (to
//                 measure what those waits fix)
//   SOURCES=a,b   operator: the sources to run (default all the simulator
//                 can make); WORKFLOWS=T1,T2: only these workflows;
//                 E2E_TIME_SCALE: the operator's wait stretch (default 3)
//   FUZZ_ITERS, FUZZ_SEED: the fuzz run's size and seed
//   SCOPE_URL, SIMLAB_URL, CORE_DIR: see bench.mjs
import { spawn } from "node:child_process";
import { appendFileSync, mkdirSync } from "node:fs";
import path from "node:path";
import { REPO, LAB, SCOPE, Guard, RunRestarted, labPost, labState, launchChromium, load, until, waitServing, delay } from "./bench.mjs";

const OUT = process.env.OUT || path.join(REPO, "build", "e2e");
const REPEAT = +(process.env.REPEAT || 1);
const NAIVE = process.env.NAIVE === "1";
const suites = process.argv.slice(2).length ? process.argv.slice(2) : ["simlab"];
mkdirSync(OUT, { recursive: true });
const results = path.join(OUT, "results.jsonl");
const log = (...a) => console.log(new Date().toISOString().slice(11, 19), ...a);
const record = (o) => appendFileSync(results, JSON.stringify({ at: new Date().toISOString(), naive: NAIVE, ...o }) + "\n");

// A suite is { name, minLeft, open(browser) → ctx, steps, close(ctx) }: steps run
// in order on one page; ctx.reload() brings the page back after a restart.
async function suiteOf(name) {
  switch (name) {
    case "simlab": return (await import("./simlab.mjs")).suite();
    case "operator": return (await import("./core.mjs")).operatorSuite();
    case "fuzz": return (await import("./core.mjs")).fuzzSuite();
  }
  throw new Error(`no suite ${name} (simlab, operator, fuzz)`);
}

async function ready(minLeft) {
  if (NAIVE) {
    // as first written: go once simlab says a run is up
    const st = await until(async () => { const s = await labState(); return ["running", "attached"].includes(s.run.state) ? s : null; }, 30 * 60e3, "no run");
    return st;
  }
  return waitServing({ minLeft, log: (m) => log("  " + m) });
}

// runStep runs one step with its checks; a step that fails on a run that
// ended under it is retried once on the next run (NAIVE: never).
async function runStep(suite, ctx, step, rep) {
  // the run the suite is on must still serve before a step starts: the
  // ports the steps talk to belong to whichever run holds the simulator,
  // and while ours is down they may be another run's
  if (!NAIVE && ctx.guard) {
    try {
      await ctx.guard.check();
      // a step is not started on a run that ends before it can finish:
      // the run is ended now (freeing the simulator) and the step waits for
      // the next
      const st = await labState();
      if (suite.stepLeft && st.run.secs && st.run.left < suite.stepLeft) {
        log(`  run ${st.run.runs} has ${st.run.left}s left, below a step's ${suite.stepLeft}s: restarting it`);
        await labPost("/api/restart", {});
        await until(async () => (await labState()).run.runs !== st.run.runs, 120000, "the restart never began a new run");
        throw new RunRestarted(st.run.runs, "next");
      }
    } catch (g) {
      if (!g.restart) throw g;
      log(`  ${g.message}: waiting for the next run before ${step.id}`);
      record({ suite: suite.name, rep, step: step.id, name: step.name, attempt: 0, outcome: "restart", restart: g.message, ms: 0, load: load().load1, failed: [], checks: [] });
      ctx.guard = new Guard(await ready(suite.minLeft));
      await suite.reload(ctx);
    }
  }
  for (let attempt = 1; ; attempt++) {
    const checks = [];
    // the step's handle: ctx's fields, and what the step sets on it
    // (source, guard) kept for the next step
    const t = Object.assign(Object.create(ctx), {
      ok: (c, m) => { checks.push({ kind: "ok", pass: !!c, msg: m }); },
      known: (name, c, m) => { checks.push({ kind: "known", name, pass: !!c, msg: m }); },
      info: (m) => { checks.push({ kind: "info", msg: m }); },
      skip: (why) => { const e = new Error(why); e.skip = true; throw e; },
    });
    const t0 = Date.now();
    let err = null, outcome;
    const l0 = load();
    try {
      await step.run(t);
      outcome = checks.some((c) => c.kind === "ok" && !c.pass) ? "fail" : "pass";
    } catch (e) {
      err = e;
      outcome = e.skip ? "skip" : "fail";
    }
    for (const k of ["source", "guard", "restarting"]) if (Object.hasOwn(t, k)) ctx[k] = t[k];
    // a failure on a run that is gone is the restart's, not the step's
    let restart = false;
    if (outcome === "fail" && !ctx.restarting && ctx.guard) {
      try { await ctx.guard.check(); } catch (g) { if (g.restart) restart = g.message; else throw g; }
    }
    if (err && err.restart) restart = err.message;
    const failed = checks.filter((c) => c.kind === "ok" && !c.pass).map((c) => c.msg);
    if (err && !err.skip) failed.push(err.message.split("\n")[0]);
    const rec = { suite: suite.name, rep, step: step.id, name: step.name, attempt, outcome, restart: restart || undefined, ms: Date.now() - t0, load: l0.load1, nice: l0.nice, run: ctx.guard?.runs, failed, checks, skip: err?.skip ? err.message : undefined };
    if (outcome === "fail" && suite.shot) await suite.shot(ctx, path.join(OUT, `${suite.name}-${rep}-${step.id}-${attempt}.png`)).catch(() => {});
    log(`  [${step.id}] ${outcome}${restart ? " (" + restart + ")" : ""} ${((Date.now() - t0) / 1000).toFixed(1)}s ${failed.join(" | ").slice(0, 300)}`);
    for (const c of checks.filter((c) => c.kind === "known")) log(`      known ${c.name}: ${c.pass ? "not seen" : "SEEN"} — ${c.msg}`);
    ctx.restarting = false;
    if (restart && !NAIVE && attempt < 3) {
      rec.outcome = "restart";
      record(rec);
      const st = await ready(suite.minLeft);
      ctx.guard = new Guard(st);
      await suite.reload(ctx);
      continue;
    }
    record(rec);
    return rec;
  }
}

// ensureSimlab uses a running simlab, or starts one (build/simlab, run
// name e2e-simlab) for this invocation and stops it at the end.
async function ensureSimlab() {
  if (await labState().then(() => true, () => false)) return null;
  const bin = path.join(REPO, "build", "simlab");
  log(`starting ${bin} (no simlab at ${LAB})`);
  const p = spawn(bin, ["-name", process.env.SIMLAB_RUN || "e2e-simlab", "-addr", new URL(LAB).host], { cwd: REPO, stdio: ["ignore", "inherit", "inherit"] });
  await until(() => labState().then(() => true, () => false), 30000, "simlab never answered");
  return p;
}

const simlab = await ensureSimlab();
const stopSimlab = async () => {
  if (!simlab) return;
  simlab.kill("SIGINT");
  await new Promise((r) => { simlab.on("close", r); setTimeout(r, 90000); });
};
const browser = await launchChromium();
// a stop ends the runner, not just its browser: the steps after it would
// still drive the bench
for (const sig of ["SIGINT", "SIGTERM"]) {
  process.on(sig, () => {
    // at once: an awaited close would let the loop start the next step
    log(`${sig}: stopping`);
    closing = true;
    simlab?.kill("SIGINT");
    process.exit(130); // Playwright ends its browser with the process
  });
}
let closing = false;
browser?.on("disconnected", () => { if (!closing) { log("the browser went away: stopping"); process.exit(1); } });
if (!browser) { console.log("SKIP: playwright not installed"); await stopSimlab(); process.exit(0); }
let anyFail = false;
for (const name of suites) {
  const suite = await suiteOf(name);
  for (let rep = 1; rep <= REPEAT; rep++) {
    const l0 = load();
    log(`== ${name} ${rep}/${REPEAT} (load ${l0.load1} on ${l0.cpus} CPUs, nice ${l0.nice}${NAIVE ? ", naive" : ""})`);
    const t0 = Date.now();
    const st = await ready(suite.minLeft);
    const ctx = await suite.open(browser, { guard: NAIVE ? null : new Guard(st), rep, out: OUT });
    if (NAIVE) ctx.guard = new Guard(st); // only to classify, never to retry
    const recs = [];
    for (const step of suite.steps(ctx)) recs.push(await runStep(suite, ctx, step, rep));
    await suite.close(ctx).catch(() => {});
    const failed = recs.filter((r) => r.outcome === "fail");
    const l1 = load();
    record({ suite: name, rep, summary: true, pass: failed.length === 0, failedSteps: failed.map((r) => r.step), restarts: recs.filter((r) => r.attempt > 1).length, steps: recs.length, skipped: recs.filter((r) => r.outcome === "skip").length, secs: Math.round((Date.now() - t0) / 1000), load0: l0.load1, load1: l1.load1, nice: l0.nice });
    log(`== ${name} ${rep}: ${failed.length ? "FAIL " + failed.map((r) => r.step).join(",") : "PASS"} in ${Math.round((Date.now() - t0) / 1000)} s`);
    if (failed.length) anyFail = true;
    await delay(200);
  }
}
closing = true;
await browser.close();
await stopSimlab();
log(`results in ${results} (simlab ${LAB}, scope ${SCOPE})`);
process.exit(anyFail ? 1 : 0);
