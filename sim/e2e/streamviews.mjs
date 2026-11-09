// streamviews.mjs — the core's page against the simulated HD (argv[2] =
// the app's URL, argv[3] = seconds to count): BUS's Visual view on the MIDI
// bus the simulator drives on C1 (the scope already decodes it), counting
// the messages the view takes in and the delay to the first, whether it
// follows a stream of the scope's, and the toasts it raises; then the SOUND
// tab, which streams the audio or says why the scope has none. Prints one
// JSON line for the simtest area (streamviews), or SKIP without a browser.
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const CORE = process.env.DSO_CORE_DIR || path.resolve(HERE, "../../../open-dso-core");
const { findPlaywright, openScope } = await import(pathToFileURL(path.join(CORE, "webclient", "scope_po.mjs")).href);

const url = process.argv[2], secs = +(process.argv[3] || 20);
if (!findPlaywright()) { console.log("SKIP playwright not found"); process.exit(0); }
let S;
try {
  S = await openScope(url, { viewport: { width: 1500, height: 950 } });
} catch (e) { console.log("SKIP " + e.message.split("\n")[0]); process.exit(0); }
const { page, browser, pageErrors } = S;
const failed = [];
page.on("response", (r) => {
  const u = new URL(r.url());
  // a part the scope lacks answers 404 by design; anything else is a failure
  if (u.pathname.startsWith("/api/") && r.status() >= 400 && r.status() !== 404) failed.push(r.status() + " " + u.pathname);
});
const ev = (fn, arg) => page.evaluate(fn, arg);
await ev(() => {
  window.__toasts = [];
  const t = window.toast;
  window.toast = (m, k) => { window.__toasts.push(String(m)); return t(m, k); };
});
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const out = { visual: { shown: false, msgs: 0, first_ms: -1, streamed: false, toasts: [] }, sound: { shown: false, unavailable: "", blocks: 0 }, errors: pageErrors, failed };
try {
  await page.click("#tab-bus");
  await page.waitForFunction(() => typeof dcfg !== "undefined" && dcfg.proto === "midi", null, { timeout: 15000 });
  await page.click('#busView button[data-value="visual"]');
  await page.waitForFunction(() => ws.busView === "visual" && !document.querySelector('.bview[data-view="visual"]').hidden, null, { timeout: 5000 });
  out.visual.shown = true;
  const t0 = Date.now();
  for (let end = t0 + secs * 1000; Date.now() < end;) {
    const st = await ev(() => ({ msgs: vis.s ? vis.s.msgs || 0 : 0, streamed: !!vis.stream.on }));
    out.visual.msgs = st.msgs;
    out.visual.streamed = out.visual.streamed || st.streamed;
    if (st.msgs > 0 && out.visual.first_ms < 0) out.visual.first_ms = Date.now() - t0;
    await sleep(250);
  }
  out.visual.toasts = await ev(() => window.__toasts.splice(0));
  await page.click('#busView button[data-value="packets"]');
  await page.click("#tab-sound");
  await page.waitForFunction(() => ws.cur === "sound", null, { timeout: 5000 });
  out.sound.shown = true;
  await sleep(3000);
  out.sound.unavailable = await ev(() => snd.unavailable || "");
  if (!out.sound.unavailable) {
    // the page's own copy of the stream: blocks it took in for a while
    const n0 = await ev(() => snd.blocks || 0);
    await sleep(secs * 1000);
    out.sound.blocks = (await ev(() => snd.blocks || 0)) - n0;
  }
  await page.click("#tab-scope");
} catch (e) {
  pageErrors.push("driver: " + e.message.split("\n")[0]);
}
console.log(JSON.stringify(out));
await browser.close();
