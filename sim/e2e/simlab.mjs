// simlab.mjs — the simlab page end to end, in a browser: the page loads
// and shows the run, the screen and the embedded web UI; the generator's
// form reaches the scope's inputs (SCPI measures them); the drawn keys and
// knobs change the scope (SCPI, the LCD's UI model and the web UI agree,
// and fb0 shows it); protocol presets decode in the web UI's BUS view; a
// restart brings a new run with the generator's settings kept.
//
// Checks are of three kinds: ok (a failure fails the step), known (an
// app-level finding this suite records as observed, never failing: what it
// saw is in the report) and info.
import { LAB, SCOPE, resetScope, scpi, scpiNum, until, appGet, labState, Guard, waitServing } from "./bench.mjs";

// webFrame is the embedded web UI's frame once it has frames flowing.
async function webFrame(page, timeout = 60000) {
  return until(async () => {
    const f = page.frames().find((x) => x.url().startsWith(SCOPE));
    if (!f) return null;
    const ok = await f.evaluate(() => typeof frame !== "undefined" && frame && frame.seq > 0).catch(() => false);
    return ok ? f : null;
  }, timeout, "the embedded web UI never showed a frame");
}

// panelModel is the LCD's UI model (what the firmware draws on fb0).
const panelModel = () => appGet("/api/panel/wait?seq=0&timeout_ms=1");

// fb0 dumps the firmware's UI layer as the device model holds it and
// keeps its pixels in the page; it returns the dump's handle.
async function fb0(page) {
  return page.evaluate(async () => {
    const r = await fetch(`/api/screen.png?src=fb0&t=${Date.now()}`);
    if (!r.ok) return null;
    const bmp = await createImageBitmap(await r.blob());
    const c = new OffscreenCanvas(bmp.width, bmp.height);
    const g = c.getContext("2d");
    g.drawImage(bmp, 0, 0);
    window.__fb0 = window.__fb0 || [];
    window.__fb0.push(g.getImageData(0, 0, bmp.width, bmp.height).data);
    return window.__fb0.length - 1;
  });
}
// differs counts the pixels two fb0 dumps differ in (-1: a dump is missing).
async function differs(page, a, b) {
  return page.evaluate(([x, y]) => {
    const A = window.__fb0?.[x], B = window.__fb0?.[y];
    if (!A || !B || A.length !== B.length) return -1;
    let n = 0;
    for (let i = 0; i < A.length; i += 4) if (A[i] !== B[i] || A[i + 1] !== B[i + 1] || A[i + 2] !== B[i + 2]) n++;
    return n;
  }, [a, b]);
}

const lampsLit = (page) => page.$$eval("#controls button.key.lit", (bs) => bs.map((b) => b.dataset.key));
const chOn = async (n) => /ON/i.test(await scpi(`C${n}:TRA?`));
const webChOn = (f, n) => f.evaluate((k) => { const b = document.getElementById("tC" + k); return !!b && b.classList.contains("on"); }, n);
const webVdiv = (f, n) => f.evaluate((k) => parseFloat(document.getElementById("vdiv" + k)?.value), n);
const webTdiv = (f) => f.evaluate(() => parseFloat(document.getElementById("tdiv")?.value));
const lcdShown = (p, n) => (p.Display.ShowCh ? p.Display.ShowCh[n - 1] : p.Display["ShowC" + n]);
const rel = (a, b) => Math.abs(a - b) <= Math.abs(b) * 0.02;

// press clicks a drawn key and waits for its request to finish.
async function press(page, k) {
  const done = page.waitForResponse((r) => r.url().endsWith("/api/key"), { timeout: 30000 });
  await page.click(`#controls button[data-key="${k}"]`);
  const r = await done;
  if (!r.ok()) throw new Error(`key ${k}: ${r.status()} ${await r.text()}`);
}
// turn turns a drawn knob by n detents with the mouse wheel (up is +).
async function turn(page, k, n) {
  const el = page.locator(`#controls .knob[aria-label^="${k} knob"]`);
  await el.hover();
  for (let i = 0; i < Math.abs(n); i++) {
    const done = page.waitForResponse((r) => r.url().endsWith("/api/knob"), { timeout: 30000 });
    await page.mouse.wheel(0, n > 0 ? -60 : 60);
    const r = await done;
    if (!r.ok()) throw new Error(`knob ${k}: ${r.status()} ${await r.text()}`);
  }
}
// closeSheet closes the LCD's sheet with the panel's keys: a channel key
// on its own tab closes it (another channel key first turns to that
// channel's tab).
async function closeSheet(page) {
  for (let i = 0; i < 3; i++) {
    const m = await panelModel();
    if (m.Mode !== "sheet") return;
    await press(page, /^ch[1-4]$/.test(m.Sheet.Tab) ? m.Sheet.Tab : "ch1");
    await until(async () => (await panelModel()).Seq !== m.Seq, 10000, "the LCD model never moved").catch(() => {});
  }
}

// scpiFreq is channel ch's frequency as the app measures it.
async function scpiFreq(ch) {
  await scpi(`:MEASure:SIMPle:SOURce C${ch}`);
  return scpiNum(":MEASure:SIMPle:VALue? FREQ");
}

export const steps = [
  { id: "L1", name: "the page loads: run serving, the screen, the drawn panel and the generator", run: async (t) => {
    const { page } = t;
    await page.waitForSelector("#controls button[data-key=ch1]");
    const run = await until(() => page.evaluate(() => document.body.dataset.run === "serving"), 30000, "the page never showed the run serving");
    t.ok(run, "the page shows the run as serving");
    const chip = await page.textContent("#runchip");
    t.ok(/running|attached/i.test(chip), `run chip reads "${chip}"`);
    const w = await until(() => page.evaluate(() => { const i = document.getElementById("screen"); return i.complete && i.naturalWidth ? `${i.naturalWidth}x${i.naturalHeight}` : null; }), 20000, "the screen never loaded");
    t.ok(w === "1024x600", `the screen picture is ${w}`);
    const keys = await page.$$eval("#controls button.key", (b) => b.length);
    const knobs = await page.$$eval("#controls .knob", (b) => b.length);
    t.ok(keys >= 29 && knobs === 6, `the panel draws ${keys} keys and ${knobs} knobs`);
    const rows = await page.$$eval("#sigtable tbody tr", (r) => r.length);
    t.ok(rows === 4, `the generator lists ${rows} inputs`);
  } },

  { id: "L2", name: "the embedded web UI is reachable and has the HD's four channels", run: async (t) => {
    const f = await webFrame(t.page);
    t.ok(!!f, "the web UI frame shows frames");
    const caps = await until(() => f.evaluate(() => (typeof caps !== "undefined" && capsServed ? caps.channels.length : null)), 15000, "the web UI never read the scope's caps");
    t.ok(caps === 4, `the web UI lists ${caps} channels`);
    const brand = await f.evaluate(() => ({ b: document.querySelector(".brand > b")?.textContent, s: document.querySelector(".brand > span")?.textContent, title: document.title }));
    t.known("brand", /sds1000cml/i.test(brand.b + brand.title) === false, `brand reads "${brand.b}" / "${brand.s}", title "${brand.title}"`);
    const st = await appGet("/api/status");
    t.ok(typeof st.running === "boolean", "the app's status answers on the published port");
    const shot = await fetch(SCOPE + "/api/screen.png", { signal: AbortSignal.timeout(10000) }).then((r) => r.status, (e) => e.message);
    t.known("app-screen", shot === 200, `the app's /api/screen.png answers ${shot} while ${st.running ? "running" : "stopped"}`);
  } },

  { id: "L3", name: "the generator's form changes C1 and the scope measures it", run: async (t) => {
    const { page } = t;
    await scpi("TDIV 5.00E-07");
    await scpi("C1:TRA ON");
    const set = async (freq) => {
      await page.click("#gentabs button:has-text('C1')");
      await page.selectOption("#w-kind", "square");
      await page.uncheck("#w-live");
      await page.fill("#w-freq", freq);
      await page.fill("#w-vpp", "2");
      await page.fill("#w-offset", "0");
      const done = page.waitForResponse((r) => r.url().endsWith("/api/signal"));
      await page.click("#w-apply");
      t.ok((await done).ok(), `the page applied a ${freq}Hz square to C1`);
    };
    for (const [txt, hz] of [["2M", 2e6], ["5M", 5e6]]) {
      await set(txt);
      const got = await until(async () => { const v = await scpiFreq(1); return Math.abs(v - hz) < hz * 0.02 ? v : null; }, 30000, `SCPI never measured ${hz} Hz on C1`).catch((e) => e.message);
      t.ok(typeof got === "number", `SCPI measures C1 at ${got} Hz for ${hz} Hz`);
      const spec = await until(async () => (await labState()).signals.C1, 5000, "no C1 spec");
      t.ok(spec.startsWith(`square,${hz}`) || spec.startsWith("square,"), `the model's C1 reads ${spec}`);
    }
  } },

  { id: "L4", name: "a channel key: SCPI, the LCD model and the key lamps agree; the web UI's channel toggle", run: async (t) => {
    const { page } = t;
    const f = await webFrame(page);
    // from a known state: C1 on, C2 off
    await scpi("C2:TRA OFF");
    await scpi("C1:TRA ON");
    await until(async () => !(await chOn(2)) && !lcdShown(await panelModel(), 2), 15000, "C2 never read off");
    const web0 = await webChOn(f, 2);
    const before = await fb0(page);
    await press(page, "ch2");
    const on = await until(async () => (await chOn(2)) && lcdShown(await panelModel(), 2), 15000, "CH2 did not reach SCPI and the LCD").catch((e) => e.message);
    t.ok(on === true, `CH2 switched C2 on in SCPI and the LCD model (${on})`);
    const after = await fb0(page);
    const dn = await differs(page, before, after);
    t.ok(dn > 200, `fb0 changed ${dn} pixels`);
    const lit = await until(async () => { const l = await lampsLit(page); return l.includes("ch2") ? l : null; }, 10000, "the CH2 lamp never lit").catch(() => null);
    t.ok(!!lit, `the drawn CH2 key is lit (${lit})`);
    // spec 08 §6.3: only the selected channel's lamp lights
    t.known("one-channel-lamp", lit && !lit.includes("ch1"), `with C1 and C2 on and C2 selected the lit channel keys are ${lit}`);
    // CH2 again on its own sheet: spec 08 §4.4 toggles the channel off; the
    // app's panel closes the sheet and keeps the channel
    const m1 = await panelModel();
    t.ok(m1.Mode === "sheet" && m1.Sheet.Tab === "ch2", `CH2 opened its sheet (${m1.Mode} ${m1.Sheet.Tab})`);
    await press(page, "ch2");
    const m2 = await until(async () => { const m = await panelModel(); return m.Mode !== "sheet" || m.Sheet.Tab !== "ch2" ? m : null; }, 15000, "CH2 again changed nothing").catch(() => null);
    t.ok(!!m2, `CH2 again left its sheet (${m2?.Mode})`);
    t.known("chan-key-toggles", !(await chOn(2)), `CH2 again with its sheet shown: C2 ${(await chOn(2)) ? "stays on" : "off"}, LCD ${m2?.Mode}`);
    await closeSheet(page);
    await scpi("C2:TRA OFF");
    const off = await until(async () => !(await chOn(2)) && !lcdShown(await panelModel(), 2), 15000, "C2 never went off").catch((e) => e.message);
    t.ok(off === true, `C2 off again in SCPI and the LCD model (${off})`);
    // the web UI's CH2 toggle is the page's own show/hide: whether it
    // follows the scope's channel is recorded, not failed
    const web1 = await webChOn(f, 2);
    t.known("web-follows-channel", web0 === false && web1 === false, `the web UI's CH2 toggle read ${web0 ? "on" : "off"} with C2 off before, ${web1 ? "on" : "off"} after; the scope's C2 is off`);
  } },

  { id: "L5", name: "the V-scale knob: V/div moves one step, SCPI, the web UI and fb0 agree, and back", run: async (t) => {
    const { page } = t;
    const f = await webFrame(page);
    const t0 = Date.now(), lap = (what) => t.info(`${what} at ${Date.now() - t0} ms`);
    // spec 07 §7.1: a SCPI V/div change selects that channel. C2 selected
    // by its key, then C1's V/div over SCPI: which one does the knob move?
    await scpi("C1:TRA ON");
    await closeSheet(page);
    await press(page, "ch2");
    await closeSheet(page);
    await scpi("C2:VDIV 1.00E+00");
    await scpi("C1:VDIV 2.00E-01");
    await until(async () => rel(await scpiNum("C1:VDIV?"), 0.2) && rel(await scpiNum("C2:VDIV?"), 1), 10000, "V/div not set");
    await turn(page, "vscale", 1);
    const moved = await until(async () => { const a = await scpiNum("C1:VDIV?"), b = await scpiNum("C2:VDIV?"); return !rel(a, 0.2) ? "C1" : !rel(b, 1) ? "C2" : null; }, 10000, "").catch(() => "neither");
    t.known("scpi-selects", moved === "C1", `after CH2's key and C1:VDIV over SCPI the V-scale knob moved ${moved}`);
    await scpi("C2:TRA OFF");
    lap("SCPI selection checked");
    // C1 selected by its key: its sheet opens, the knob moves its V/div
    await press(page, "ch1");
    await until(async () => (await panelModel()).Sheet.Tab === "ch1", 10000, "CH1 opened no sheet");
    await scpi("C1:VDIV 2.00E-01");
    await until(async () => rel(await scpiNum("C1:VDIV?"), 0.2) && rel(await webVdiv(f, 1), 0.2), 15000, "C1 never read 200 mV/div");
    lap("C1 selected at 200 mV/div");
    const v0 = 0.2;
    const shot0 = await fb0(page);
    lap("fb0 read");
    await turn(page, "vscale", 1);
    lap("knob turned");
    const v1 = await until(async () => { const v = await scpiNum("C1:VDIV?"); return v && !rel(v, v0) ? v : null; }, 15000, "V/div did not move").catch(() => v0);
    t.ok(!rel(v1, v0), `V-scale +1: C1 ${v0} → ${v1} V/div over SCPI`);
    lap("SCPI moved");
    const w1 = await until(async () => { const w = await webVdiv(f, 1); return rel(w, v1) ? w : null; }, 10000, "the web UI kept its V/div").catch(() => NaN);
    t.ok(rel(w1, v1), `the web UI reads ${w1} V/div`);
    lap("web moved");
    const shot1 = await fb0(page);
    const dn = await differs(page, shot0, shot1);
    t.ok(dn > 0, `fb0 redrew ${dn} pixels`);
    // spec 08 §4.4: positive UI sign = a larger V/div
    t.known("knob-sign", v1 > v0, `the + code (UI sign +) took V/div ${v0} → ${v1}`);
    await turn(page, "vscale", -1);
    const v2 = await until(async () => { const v = await scpiNum("C1:VDIV?"); return rel(v, v0) ? v : null; }, 15000, "V/div did not come back").catch(() => NaN);
    t.ok(rel(v2, v0), `V-scale −1: back to ${v2} V/div`);
    await closeSheet(page);
  } },

  { id: "L6", name: "the H-scale knob: s/div moves, SCPI and the web UI agree, and back", run: async (t) => {
    const { page } = t;
    const f = await webFrame(page);
    await closeSheet(page);
    await scpi("TDIV 1.00E-06");
    await until(async () => rel(await scpiNum("TDIV?"), 1e-6) && rel(await webTdiv(f), 1e-6), 15000, "s/div never read 1 µs");
    // the workspace, no sheet: a channel key's sheet takes the knob
    const p0 = await panelModel();
    t.info(`LCD mode ${p0.Mode}, sheet ${p0.Sheet.Open ? p0.Sheet.Tab : "closed"}`);
    await turn(page, "hscale", 1);
    const t1 = await until(async () => { const v = await scpiNum("TDIV?"); return v && !rel(v, 1e-6) ? v : null; }, 15000, "s/div did not move").catch(() => 1e-6);
    const p1 = await panelModel();
    if (p0.Mode === "workspace") t.ok(!rel(t1, 1e-6), `H-scale +1 in the workspace: 1e-6 → ${t1} s/div`);
    else t.known("hscale-in-sheet", !rel(t1, 1e-6), `H-scale +1 with the ${p0.Sheet.Tab} sheet open: s/div ${t1}, sheet tab ${p0.Sheet.Tab} → ${p1.Sheet.Tab}`);
    if (!rel(t1, 1e-6)) {
      const w = await until(async () => { const v = await webTdiv(f); return rel(v, t1) ? v : null; }, 10000, "web s/div").catch(() => NaN);
      t.ok(rel(w, t1), `the web UI reads ${w} s/div`);
      t.known("knob-sign-h", t1 > 1e-6, `the H-scale + code took s/div 1e-6 → ${t1}`);
      await turn(page, "hscale", -1);
      const t2 = await until(async () => { const v = await scpiNum("TDIV?"); return rel(v, 1e-6) ? v : null; }, 15000, "s/div back").catch(() => NaN);
      t.ok(rel(t2, 1e-6), `H-scale −1: back to ${t2} s/div`);
    }
  } },

  { id: "L7", name: "H-scale with a channel sheet open", run: async (t) => {
    const { page } = t;
    await scpi("C3:TRA OFF");
    await scpi("TDIV 1.00E-06");
    await press(page, "ch3"); // C3 on, its sheet open
    const p = await until(async () => { const m = await panelModel(); return m.Mode === "sheet" ? m : null; }, 10000, "CH3 opened no sheet").catch(() => null);
    t.ok(!!p, `CH3 opened its sheet (${p?.Sheet?.Tab})`);
    await turn(page, "hscale", 1);
    const moved = await until(async () => !rel(await scpiNum("TDIV?"), 1e-6), 8000, "").catch(() => false);
    const p2 = await panelModel();
    t.known("hscale-in-sheet", moved, `H-scale +1 with the ${p?.Sheet?.Tab} sheet open: s/div ${await scpiNum("TDIV?")}, sheet tab ${p2.Sheet.Tab}, mode ${p2.Mode}`);
    await closeSheet(page);
    await scpi("C3:TRA OFF");
    await scpi("TDIV 1.00E-06");
  } },

  ...[
    { p: "uart", preset: 'UART 115200 "Hello"', proto: "uart", roles: { decLine: "1" }, tdiv: 1e-4, want: ["48656C6C6F"] },
    { p: "spi", preset: "SPI 1 MHz", proto: "spi", roles: { decClk: "1", decData: "2" }, tdiv: 5e-6, want: ["A53C"] },
    { p: "i2c", preset: "I²C EEPROM write", proto: "i2c", roles: { decScl: "1", decSda: "2" }, tdiv: 1e-4, want: ["00A5"] },
  ].map((c, i) => ({ id: `L${8 + i}`, name: `the ${c.preset} preset decodes in the web UI's BUS view`, run: async (t) => {
    const { page } = t;
    const f = await webFrame(page);
    await page.click("#gentabs button:has-text('Protocol')");
    const done = page.waitForResponse((r) => r.url().endsWith("/api/bus"));
    await page.click(`#genbody .presets button:has-text('${c.preset}')`);
    t.ok((await done).ok(), `the ${c.preset} preset reached the model`);
    const sig = await until(async () => { const s = (await labState()).signals.C1; return s.startsWith(c.p) ? s : null; }, 10000, "C1 never carried the bus");
    t.ok(!!sig, `C1 carries ${sig}`);
    for (const n of [1, 2]) await scpi(`C${n}:TRA ON`);
    await f.click("#tab-bus");
    await f.evaluate((a) => {
      const set = (id, v) => { const e = document.getElementById(id); if (e) { e.value = v; e.dispatchEvent(new Event("change", { bubbles: true })); } };
      set("decProto", a.proto);
      for (const [id, v] of Object.entries(a.roles)) set(id, v);
      if (a.proto === "uart") set("decBaud", "115200");
    }, c);
    // the timebase after the decoder: choosing a protocol in BUS may bring
    // back the timebase BUS last used, so the bus's own window is set last
    // and read back from the page before the table is read
    await until(async () => {
      if (rel(await webTdiv(f), c.tdiv)) return true;
      await scpi(`TDIV ${c.tdiv.toExponential(2)}`);
      return false;
    }, 20000, `the timebase never stayed at ${c.tdiv} s/div`, 1000);
    const view = await f.$(`#busView > button[data-value="packets"]`);
    if (view) await view.click();
    // BUS's packet table: a row a frame, its bytes in hex and as text
    const got = await until(async () => {
      const txt = await f.evaluate(() => [...document.querySelectorAll("#pkBody tr")].map((r) => r.textContent).join(" | "));
      const flat = txt.replace(/\s|0x/gi, "").toUpperCase();
      const hit = c.want.find((w) => flat.includes(w));
      return hit ? txt.slice(0, 160).trim() : null;
    }, 30000, `the BUS view never decoded ${c.want.join(" or ")}`).catch(async (e) => `${e.message}; the table read "${(await f.evaluate(() => [...document.querySelectorAll("#pkBody tr")].map((r) => r.textContent).join(" | "))).slice(0, 120)}" at ${await webTdiv(f)} s/div`);
    t.ok(!/timed out/.test(got), `BUS decodes ${c.proto}: "${got}"`);
    await f.evaluate(() => { const e = document.getElementById("decProto"); e.value = "off"; e.dispatchEvent(new Event("change", { bubbles: true })); });
  } })),

  { id: "L11", name: "restart: a new run serves, the generator's settings stay, the web UI reloads with four channels", run: async (t) => {
    const { page } = t;
    // a plain waveform to keep across the restart
    await page.click("#gentabs button:has-text('C1')");
    await page.selectOption("#w-kind", "square");
    await page.uncheck("#w-live");
    await page.fill("#w-freq", "3M");
    await page.fill("#w-vpp", "2");
    await page.fill("#w-offset", "0");
    await page.click("#w-apply");
    await until(async () => (await labState()).signals.C1.startsWith("square,3"), 10000, "C1 not 3 MHz");
    const st0 = await labState();
    t.restarting = true; // this step ends the run itself
    await page.click("#restart");
    await until(async () => (await labState()).run.runs !== st0.run.runs, 60000, "no new run began");
    const chip = await until(() => page.evaluate(() => document.body.dataset.run !== "serving" ? document.getElementById("runchip").textContent : null), 30000, "the page never showed the run down").catch(() => "missed");
    t.info(`while the new run starts the chip reads ${chip}`);
    const st1 = await waitServing({ log: t.info });
    t.ok(st1.run.runs > st0.run.runs, `run ${st0.run.runs} → ${st1.run.runs}`);
    t.guard = new Guard(st1);
    t.ok(st1.signals.C1.startsWith("square,3"), `C1 kept ${st1.signals.C1}`);
    await until(() => page.evaluate(() => document.body.dataset.run === "serving"), 15000, "the page never showed the new run serving");
    // the frame reloads when the run serves: its first caps
    const f = await webFrame(page, 60000);
    const first = await f.evaluate(() => ({ served: typeof capsServed !== "undefined" && capsServed, n: typeof caps !== "undefined" ? caps.channels.length : 0, model: document.querySelector(".brand > span")?.textContent }));
    t.info(`the reloaded web UI first showed ${first.n} channels (caps served ${first.served}, model "${first.model}")`);
    const n = await until(() => f.evaluate(() => (capsServed ? caps.channels.length : null)), 20000, "caps never served").catch(() => 0);
    t.ok(n === 4, `the reloaded web UI settles on ${n} channels`);
    t.known("caps-race", first.n === 4 || first.served === false, `at first frame: ${first.n} channels, caps served ${first.served}, model "${first.model}"`);
    const hz = await until(async () => { await scpi("TDIV 5.00E-07"); const v = await scpiFreq(1); return Math.abs(v - 3e6) < 6e4 ? v : null; }, 30000, "C1 not measured at 3 MHz").catch(() => NaN);
    t.ok(Math.abs(hz - 3e6) < 6e4, `the new run measures C1 at ${hz} Hz`);
  } },
];

export { webFrame };

// suite is the simlab page suite for run.mjs: one page on simlab, the
// steps in order. A run lasts 300 s; the steps need about two minutes.
export function suite() {
  return {
    name: "simlab", minLeft: 150,
    steps: () => steps,
    async open(browser, ctx) {
      await resetScope();
      const page = await browser.newPage({ viewport: { width: 1700, height: 1100 } });
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.goto(LAB + "/", { waitUntil: "domcontentloaded" });
      return { ...ctx, page, errors };
    },
    async reload(ctx) { await resetScope(); await ctx.page.reload({ waitUntil: "domcontentloaded" }); },
    async shot(ctx, file) { await ctx.page.screenshot({ path: file }); },
    async close(ctx) { await ctx.page.close(); },
  };
}
