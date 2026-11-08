// report.mjs — pass rates from run.mjs's results: per suite and mode
// (naive or not), per step, the restarts the runner retried, the host load
// at each failing step against the passing ones, and the known findings.
//
//   node sim/e2e/report.mjs [build/e2e/results.jsonl ...]
import path from "node:path";
import { REPO, readJSONL } from "./bench.mjs";

const files = process.argv.slice(2).length ? process.argv.slice(2) : [path.join(REPO, "build", "e2e", "results.jsonl")];
const rows = files.flatMap(readJSONL);
const mean = (a) => (a.length ? (a.reduce((x, y) => x + y, 0) / a.length).toFixed(2) : "-");

const groups = new Map();
for (const r of rows) {
  const k = `${r.suite}${r.naive ? " (naive)" : ""}`;
  if (!groups.has(k)) groups.set(k, { sums: [], steps: [] });
  (r.summary ? groups.get(k).sums : groups.get(k).steps).push(r);
}
for (const [k, g] of groups) {
  const pass = g.sums.filter((s) => s.pass).length;
  console.log(`\n## ${k}: ${pass}/${g.sums.length} repetitions passed`);
  console.log(`   load at start: mean ${mean(g.sums.map((s) => s.load0))}, range ${Math.min(...g.sums.map((s) => s.load0))}–${Math.max(...g.sums.map((s) => s.load0))}; restarts retried: ${g.sums.reduce((a, s) => a + s.restarts, 0)}`);
  const byStep = new Map();
  for (const s of g.steps) {
    if (!byStep.has(s.step)) byStep.set(s.step, []);
    byStep.get(s.step).push(s);
  }
  for (const [id, list] of byStep) {
    const final = list.filter((s) => s.outcome !== "restart");
    const fails = final.filter((s) => s.outcome === "fail");
    const restarts = list.filter((s) => s.outcome === "restart").length;
    const skips = final.filter((s) => s.outcome === "skip");
    if (skips.length === final.length && skips.length) { console.log(`   ${id.padEnd(4)} skipped: ${skips[0].skip}`); continue; }
    const passes = final.filter((s) => s.outcome === "pass");
    const ms = passes.map((s) => s.ms);
    let line = `   ${id.padEnd(4)} ${passes.length}/${final.length} pass`;
    if (ms.length) line += `, ${(Math.min(...ms) / 1000).toFixed(1)}–${(Math.max(...ms) / 1000).toFixed(1)} s`;
    if (restarts) line += `, ${restarts} cut by a restart`;
    if (fails.length) line += `; load failing ${mean(fails.map((s) => s.load))} vs passing ${mean(passes.map((s) => s.load))}`;
    console.log(line);
    const why = new Map();
    for (const f of fails) for (const m of f.failed.length ? f.failed : ["(no message)"]) why.set(m.slice(0, 140), (why.get(m.slice(0, 140)) || 0) + 1);
    for (const [m, n] of why) console.log(`        ${n}× ${m}`);
  }
  const known = new Map();
  for (const s of g.steps) for (const c of s.checks || []) if (c.kind === "known") {
    const e = known.get(c.name) || { seen: 0, not: 0, msg: "" };
    if (c.pass) e.not++; else { e.seen++; e.msg = c.msg; }
    known.set(c.name, e);
  }
  for (const [n, e] of known) console.log(`   known ${n}: seen ${e.seen}/${e.seen + e.not}${e.msg ? " — " + e.msg.slice(0, 160) : ""}`);
}
