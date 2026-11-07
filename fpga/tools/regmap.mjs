#!/usr/bin/env node
// ENGMODEL-OWNER-UNIT: FU-HD-PL-BUILD
// TRLC-LINKS: REQ-HD-015
// regmap.mjs -- generate the open PL register ABI's artefacts from
// fpga/regs/open-pl.yml (spec 20 §10): a Verilog-2005 include of localparams,
// Go constants for the board, a SystemRDL rendering and the manifest hash.
// Generated output is never committed.
//
//   node fpga/tools/regmap.mjs [--yml fpga/regs/open-pl.yml] [--out fpga/build]
//   node fpga/tools/regmap.mjs --check   (validate only)
//
// The YAML is a small subset (block mappings, block sequences, one-line flow
// mappings, scalars, comments) read by the parser below, so the tool needs
// nothing but Node.
import { readFileSync, writeFileSync, mkdirSync } from "node:fs";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const args = process.argv.slice(2);
const opt = (k, d) => {
  const i = args.indexOf(k);
  return i >= 0 ? args[i + 1] : d;
};
const ymlPath = resolve(opt("--yml", join(here, "..", "regs", "open-pl.yml")));
const outDir = resolve(opt("--out", join(here, "..", "build")));
const checkOnly = args.includes("--check");

// ---------------------------------------------------------------- YAML subset
function scalar(s) {
  s = s.trim();
  if (s === "") return null;
  if ((s.startsWith('"') && s.endsWith('"')) || (s.startsWith("'") && s.endsWith("'"))) return s.slice(1, -1);
  if (/^-?0x[0-9a-fA-F_]+$/.test(s)) return Number(BigInt(s.replace(/_/g, "")));
  if (/^-?[0-9]+$/.test(s)) return Number(s);
  if (s === "true") return true;
  if (s === "false") return false;
  return s;
}
function flowMap(s) {
  const body = s.trim().slice(1, -1);
  const o = {};
  for (const part of body.split(",")) {
    if (!part.trim()) continue;
    const i = part.indexOf(":");
    if (i < 0) throw new Error(`flow mapping entry without a key: ${part}`);
    o[part.slice(0, i).trim()] = scalar(part.slice(i + 1));
  }
  return o;
}
function value(s) {
  s = s.trim();
  return s.startsWith("{") ? flowMap(s) : scalar(s);
}
function parseYAML(text) {
  const lines = [];
  text.split("\n").forEach((raw, n) => {
    const noComment = raw.replace(/(^|\s)#.*$/, "");
    if (!noComment.trim()) return;
    lines.push({ ind: noComment.search(/\S/), text: noComment.trim(), n: n + 1 });
  });
  let i = 0;
  function block(ind) {
    if (i >= lines.length) return null;
    if (lines[i].text.startsWith("- ")) return seq(ind);
    return map(ind);
  }
  function map(ind) {
    const o = {};
    while (i < lines.length && lines[i].ind === ind && !lines[i].text.startsWith("- ")) {
      const { text, n } = lines[i];
      const c = text.indexOf(":");
      if (c < 0) throw new Error(`line ${n}: expected key: value`);
      const k = text.slice(0, c).trim();
      const rest = text.slice(c + 1).trim();
      i++;
      if (rest !== "") o[k] = value(rest);
      else if (i < lines.length && lines[i].ind > ind) o[k] = block(lines[i].ind);
      else if (i < lines.length && lines[i].ind === ind && lines[i].text.startsWith("- ")) o[k] = seq(ind);
      else o[k] = null;
    }
    return o;
  }
  function seq(ind) {
    const a = [];
    while (i < lines.length && lines[i].ind === ind && lines[i].text.startsWith("- ")) {
      const line = lines[i];
      const rest = line.text.slice(2);
      if (rest.trim().startsWith("{") || !rest.includes(":")) {
        a.push(value(rest));
        i++;
        continue;
      }
      // "- key: v" starts a mapping whose keys sit at ind + 2
      lines[i] = { ind: ind + 2, text: rest, n: line.n };
      a.push(map(ind + 2));
    }
    return a;
  }
  const r = block(lines[0].ind);
  if (i !== lines.length) throw new Error(`line ${lines[i].n}: unexpected indentation`);
  return r;
}

// ---------------------------------------------------------------- canonical form and checks
function canonical(v) {
  if (Array.isArray(v)) return "[" + v.map(canonical).join(",") + "]";
  if (v && typeof v === "object")
    return "{" + Object.keys(v).sort().map((k) => JSON.stringify(k) + ":" + canonical(v[k])).join(",") + "}";
  return JSON.stringify(v);
}

const ACCESS = new Set(["r", "rw", "w1", "rc", "w1c"]);
function validate(m) {
  const errs = [];
  const names = new Set();
  const offsets = new Map();
  if (!m.abi || !Number.isInteger(m.abi.major) || !Number.isInteger(m.abi.minor)) errs.push("abi.major/minor");
  for (const b of m.blocks) {
    for (const r of b.regs) {
      const full = `${b.name}_${r.name}`;
      if (names.has(full)) errs.push(`duplicate register ${full}`);
      names.add(full);
      if (r.offset % 4) errs.push(`${full}: offset not word aligned`);
      if (offsets.has(r.offset)) errs.push(`${full}: offset 0x${r.offset.toString(16)} also ${offsets.get(r.offset)}`);
      offsets.set(r.offset, full);
      if (r.offset >= m.addrmap.size) errs.push(`${full}: outside the plane`);
      let used = 0n;
      for (const f of r.fields) {
        if (!ACCESS.has(f.sw)) errs.push(`${full}.${f.name}: access ${f.sw}`);
        if (f.lsb < 0 || f.width < 1 || f.lsb + f.width > 32) errs.push(`${full}.${f.name}: bits`);
        const mask = ((1n << BigInt(f.width)) - 1n) << BigInt(f.lsb);
        if (used & mask) errs.push(`${full}.${f.name}: overlaps another field`);
        used |= mask;
        if (BigInt(f.reset ?? 0) >> BigInt(f.width)) errs.push(`${full}.${f.name}: reset wider than the field`);
      }
    }
  }
  return errs;
}

const text = readFileSync(ymlPath, "utf8");
const model = parseYAML(text);
const errors = validate(model);
if (errors.length) {
  for (const e of errors) console.error(`regmap: ${e}`);
  process.exit(1);
}
const hash = createHash("sha256").update(canonical(model)).digest("hex");
const manifest = hash.slice(-16); // low 64 bits
if (checkOnly) {
  console.log(`regmap: ok, manifest ${manifest}`);
  process.exit(0);
}

// ---------------------------------------------------------------- outputs
const hex = (v, w) => v.toString(16).toUpperCase().padStart(w, "0");
const regReset = (r) => r.fields.reduce((a, f) => a | (BigInt(f.reset ?? 0) << BigInt(f.lsb)), 0n);
const fieldMask = (f) => ((1n << BigInt(f.width)) - 1n) << BigInt(f.lsb);
const accMask = (r, set) => r.fields.filter((f) => set.includes(f.sw)).reduce((a, f) => a | fieldMask(f), 0n);

let vh = `// Generated by fpga/tools/regmap.mjs from open-pl.yml; do not edit.\n`;
vh += `localparam [15:0] OPL_ABI_MAJOR = 16'd${model.abi.major};\n`;
vh += `localparam [15:0] OPL_ABI_MINOR = 16'd${model.abi.minor};\n`;
vh += `localparam [63:0] OPL_MANIFEST = 64'h${manifest};\n`;
let go = `// Code generated by fpga/tools/regmap.mjs from open-pl.yml. DO NOT EDIT.\n\n`;
go += `// Package openpl is the open PL's register ABI (spec 20 §10).\npackage openpl\n\n`;
go += `// The ABI and the manifest hash the image reports in ID_MANIFEST.\nconst (\n`;
go += `\tABIMajor = ${model.abi.major}\n\tABIMinor = ${model.abi.minor}\n\tManifest = 0x${manifest}\n`;
go += `\tBase     = 0x${hex(model.addrmap.base, 8)}\n)\n\n`;
const ACRONYMS = new Set(["id", "abi", "dna", "irq", "adc0", "adc1", "cclk", "acq"]);
const goName = (s) =>
  s.toLowerCase().split("_").map((p) => (ACRONYMS.has(p) ? p.toUpperCase() : p[0].toUpperCase() + p.slice(1))).join("");
let rdl = `// Generated by fpga/tools/regmap.mjs from open-pl.yml; do not edit.\naddrmap ${model.addrmap.name} {\n`;
for (const b of model.blocks) {
  go += `// ${b.name}: ${b.desc ?? ""}\nconst (\n`;
  rdl += `  regfile {\n`;
  for (const r of b.regs) {
    const n = `OPL_${b.name}_${r.name}`;
    vh += `localparam [15:0] ${n} = 16'h${hex(r.offset, 4)};\n`;
    vh += `localparam [31:0] ${n}_RESET = 32'h${hex(regReset(r), 8)};\n`;
    vh += `localparam [31:0] ${n}_RW = 32'h${hex(accMask(r, ["rw"]), 8)};\n`;
    const gn = goName(b.name) + goName(r.name);
    go += `\t${gn} = 0x${hex(r.offset, 3)}\n`;
    rdl += `    reg {\n`;
    for (const f of r.fields) {
      const fn = `${n}_${f.name.toUpperCase()}`;
      vh += `localparam integer ${fn}_LSB = ${f.lsb};\nlocalparam integer ${fn}_W = ${f.width};\n`;
      go += `\t${gn}${goName(f.name)}Shift = ${f.lsb}\n\t${gn}${goName(f.name)}Mask = 0x${hex(fieldMask(f), 8)}\n`;
      const sw = { r: "r", rw: "rw", w1: "w", rc: "r", w1c: "rw" }[f.sw];
      const extra = { rc: " rclr;", w1c: " woclr;", w1: " singlepulse;" }[f.sw] ?? "";
      rdl += `      field { sw=${sw};${extra} } ${f.name}[${f.lsb + f.width - 1}:${f.lsb}] = 0x${(f.reset ?? 0).toString(16)};\n`;
    }
    rdl += `    } ${r.name} @ 0x${hex(r.offset, 3)};\n`;
  }
  go += `)\n\n`;
  rdl += `  } ${b.name} @ 0x${hex(b.offset, 3)};\n`;
}
rdl += `};\n`;

mkdirSync(join(outDir, "openpl"), { recursive: true });
writeFileSync(join(outDir, "open_pl_regs.vh"), vh);
writeFileSync(join(outDir, "openpl", "regs.go"), go);
try {
  execFileSync("gofmt", ["-w", join(outDir, "openpl", "regs.go")]);
} catch {
  // gofmt is a convenience; the constants are valid Go without it
}
writeFileSync(join(outDir, "open_pl.rdl"), rdl);
writeFileSync(join(outDir, "manifest.txt"), manifest + "\n");
console.log(`regmap: ${model.blocks.reduce((a, b) => a + b.regs.length, 0)} registers, manifest ${manifest} -> ${outDir}`);
