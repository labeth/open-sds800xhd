// remote_sweep.mjs — the core's phone remote (/remote) against the simulated
// HD (argv[2] = the app's URL) on a phone (Pixel 7 emulation, touch): every
// Menus tab opened with all its rows drawn, every row that opens a sheet
// opened and closed, one safe value changed on each tab's page and read
// back from the scope's sheets (then put back), and the time/div and C2's
// V/div steppers checked against the scope's status. Prints one JSON line
// for the simtest web area, or SKIP without a browser.
import { mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const CORE = process.env.DSO_CORE_DIR || path.resolve(HERE, "../../../open-dso-core");
const { findPlaywright } = await import(pathToFileURL(path.join(CORE, "webclient", "scope_po.mjs")).href);

const APP = process.argv[2], SHOTS = process.argv[3] || "";
const pwPath = findPlaywright();
if (!pwPath) { console.log("SKIP playwright not found"); process.exit(0); }
const pw = (await import(pwPath)).default;
let browser;
try {
  browser = await pw.chromium.launch({ headless: true, args: ["--no-sandbox", "--autoplay-policy=user-gesture-required"] });
} catch (e) { console.log("SKIP " + e.message.split("\n")[0]); process.exit(0); }

// Tabs whose rows act on the scope beyond a setting (setups, the system,
// sound captures) are opened and drawn but not changed; rows that start
// streams, decoders, sweeps or change the view are never changed, nor the
// memory depth, whose choices the page redraws as the timebase moves.
const NO_CHANGE_TABS = new Set(["file", "sys", "snd"]);
const NO_CHANGE_ROW = /stream|protocol|roll|default|screen|saver|lang|reset|update|boot|setup|record|stack|master|mask|mode|view|layer|match|persist|ref|math|on$|sound|clicks|depth|\.run$|\.sr_|superres/;

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const sheets = async () => {
  const m = await (await fetch(APP + "/api/sheets")).json(), rows = {}, tabs = {};
  for (const t of m.Tabs) { tabs[t.ID] = t.Rows.map((r) => r.ID); for (const r of t.Rows) rows[r.ID] = { ...r, tab: t.ID }; }
  return { rows, tabs };
};
const status = async () => (await fetch(APP + "/api/status")).json();
const until = async (fn, ms) => { for (let end = Date.now() + ms; Date.now() < end; await sleep(200)) { try { if (await fn()) return true; } catch (e) { /* again */ } } return false; };

const out = { device: "Pixel 7", tabs: [], rows: 0, opened: 0, changed: [], refused: [], followed: [], stuck: [], errors: [], failed: [] };
try {
  const d = Object.assign({}, pw.devices["Pixel 7"]);
  delete d.defaultBrowserType;
  const ctx = await browser.newContext(d);
  const page = await ctx.newPage();
  page.on("pageerror", (e) => out.errors.push("uncaught: " + e.message));
  page.on("console", (m) => { if (m.type() === "error" && !/Failed to load resource: the server responded with a status of 404/.test(m.text())) out.errors.push(m.text()); });
  page.on("response", (r) => {
    const u = new URL(r.url());
    if (u.pathname.startsWith("/api/") && r.status() >= 400 && r.status() !== 404) out.failed.push(r.status() + " " + u.pathname);
  });
  await page.goto(APP + "/remote", { waitUntil: "domcontentloaded" });
  await page.waitForSelector(".tile .tv", { timeout: 20000 });
  out.viewport = page.viewportSize();
  const tap = async (sel) => {
    try { await page.locator(sel).first().tap({ timeout: 5000 }); } catch (e) { throw new Error(sel + ": " + e.message.split("\n")[0]); }
    await sleep(120);
  };
  const closeSheet = async () => {
    if (await page.locator(".sheet-close").first().isVisible().catch(() => false)) await tap(".sheet-close");
  };

  // the steppers of two rows the scope reports in its status
  for (const [id, key] of [["time.tdiv", "tdiv_set_s"], ["ch2.vdiv", "vdiv2"]]) {
    await tap('[data-tabbar="menus"]');
    if (await page.locator("#back").isVisible().catch(() => false)) await tap("#back");
    await tap(`[data-tab="${id.split(".")[0]}"]`);
    const v0 = (await status())[key];
    await tap(`[data-row="${id}"] [data-q="cw"]`);
    const moved = await until(async () => (await status())[key] !== v0, 8000);
    const v1 = (await status())[key];
    await tap(`[data-row="${id}"] [data-q="ccw"]`);
    const back = await until(async () => (await status())[key] === v0, 8000);
    out.followed.push({ row: id, from: v0, to: v1, moved, back });
    await tap("#back");
  }

  await tap('[data-tabbar="menus"]');
  if (await page.locator("#back").isVisible().catch(() => false)) await tap("#back");
  const tabIds = await page.evaluate(() => [...document.querySelectorAll(".tabc[data-tab]")].map((b) => b.dataset.tab));
  // back on the Menus grid whatever is open (a sheet, a page)
  const toGrid = async () => {
    await closeSheet().catch(() => {});
    await tap('[data-tabbar="menus"]').catch(() => {});
    if (await page.locator("#back").isVisible().catch(() => false)) await tap("#back").catch(() => {});
  };
  for (const tid of tabIds) {
    try { await sweepTab(tid); } catch (e) {
      out.stuck.push(tid + ": " + e.message.split("\n")[0]);
      if (SHOTS) { mkdirSync(SHOTS, { recursive: true }); await page.screenshot({ path: path.join(SHOTS, "remote-stuck-" + tid + ".png") }).catch(() => {}); }
      await toGrid();
    }
  }
  out.sideScroll = await page.evaluate(() => document.scrollingElement.scrollWidth > innerWidth + 1);
  async function sweepTab(tid) {
    const want = (await sheets()).tabs[tid] || [];
    await tap(`.tabc[data-tab="${tid}"]`);
    const drawn = await page.evaluate(() => [...document.querySelectorAll(".mbody .list [data-row]")].map((e) => e.dataset.row));
    out.rows += drawn.length;
    const tab = { id: tid, rows: want.length, drawn: drawn.length, missing: want.filter((r) => !drawn.includes(r)), opened: 0 };
    // every row that opens a sheet: open it, close it
    const opens = await page.evaluate(() => [...document.querySelectorAll(".mbody .list [data-open]")].map((e) => e.dataset.open));
    for (const id of opens) {
      try {
        await tap(`.mbody [data-open="${id}"]`);
        await page.waitForSelector(".sheet", { state: "visible", timeout: 3000 });
        await closeSheet();
        tab.opened++;
      } catch (e) {
        tab.failedOpen = (tab.failedOpen || []).concat(id + " (" + e.message.split("\n")[0] + ")");
        if (SHOTS) { mkdirSync(SHOTS, { recursive: true }); await page.screenshot({ path: path.join(SHOTS, "remote-open-" + id + ".png") }).catch(() => {}); }
        await closeSheet().catch(() => {});
      }
    }
    out.opened += tab.opened;
    // one safe value changed and read back from the scope, then put back
    if (!NO_CHANGE_TABS.has(tid)) {
      const { rows } = await sheets();
      for (const id of drawn) {
        const r = rows[id];
        if (!r || !r.Available || r.Kind !== "enum" || NO_CHANGE_ROW.test(id)) continue;
        const btns = await page.evaluate((i) => [...document.querySelectorAll(`.mbody [data-row="${i}"] .seg [data-value]`)].map((b) => +b.dataset.value), id);
        const other = btns.find((v) => v !== r.Value);
        if (other === undefined) continue;
        await tap(`.mbody [data-row="${id}"] .seg [data-value="${other}"]`);
        const ok = await until(async () => (await sheets()).rows[id].Value === other, 8000);
        const now = (await sheets()).rows[id];
        if (!ok) { out.refused.push({ row: id, want: other, got: now.Value, text: now.Text }); continue; }
        await tap(`.mbody [data-row="${id}"] .seg [data-value="${r.Value}"]`);
        const back = await until(async () => (await sheets()).rows[id].Value === r.Value, 8000);
        out.changed.push({ row: id, from: r.Text, to: now.Text, back });
        break;
      }
    }
    out.tabs.push(tab);
    await tap("#back");
  }
} catch (e) {
  out.errors.push("driver: " + e.message.split("\n")[0]);
}
console.log(JSON.stringify(out));
await browser.close();
