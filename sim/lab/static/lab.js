// simlab page: the front panel, the screen, the signal generator and the firmware's web UI.
'use strict';

const $ = (s, el = document) => el.querySelector(s);
const h = (tag, attrs = {}, ...kids) => {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === 'class') e.className = v;
    else if (k.startsWith('on')) e.addEventListener(k.slice(2), v);
    else if (v !== false && v != null) e.setAttribute(k, v === true ? '' : v);
  }
  for (const k of kids.flat()) if (k != null) e.append(k);
  return e;
};

async function api(path, body) {
  const r = await fetch(path, body === undefined ? {} : {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  });
  const j = await r.json().catch(() => ({ ok: false, err: r.statusText }));
  if (!r.ok && j.ok !== false) j.ok = false;
  return j;
}

// ---------------------------------------------------------------- front panel
// The right-hand control area as spec 08 §2.4 lays it out (column, row).
const LAYOUT = [
  { key: 'cursors', label: 'Cursors', c: 1, r: 1 }, { key: 'acquire', label: 'Acquire', c: 2, r: 1 },
  { key: 'save', label: 'Save/ Recall', c: 3, r: 1 }, { key: 'clear', label: 'Clear Sweeps', c: 4, r: 1 },
  { key: 'run', label: 'Run/ Stop', c: 7, r: 1, lamps: ['run', 'stop'], big: true },
  { knob: 'adjust', label: 'Adjust', c: 1, r: 2 }, { key: 'measure', label: 'Measure', c: 2, r: 2 },
  { key: 'display', label: 'Display', c: 3, r: 2 }, { key: 'quick', label: 'Quick', c: 4, r: 2 },
  { key: 'decode', label: 'Decode', c: 6, r: 2 }, { key: 'autoset', label: 'Auto Setup', c: 7, r: 2, big: true },
  { key: 'navigate', label: 'Navi- gate', c: 2, r: 3 }, { key: 'nav-prev', label: '◀', c: 3, r: 3 },
  { key: 'nav-stop', label: '■', c: 4, r: 3 }, { key: 'nav-next', label: '▶', c: 5, r: 3 },
  { key: 'touch', label: 'Touch', c: 6, r: 3 }, { key: 'default', label: 'Default', c: 7, r: 3 },
  { sect: 'Vertical', c: 1, r: 4, span: 2 }, { sect: 'Horizontal', c: 3, r: 4, span: 3 }, { sect: 'Trigger', c: 6, r: 4, span: 2 },
  { key: 'ch1', label: 'CH1', c: 1, r: 5, cls: 'ch1' }, { knob: 'vscale', label: 'Scale', c: 2, r: 5, rs: 2, big: true },
  { knob: 'hscale', label: 'Scale', c: 4, r: 5, rs: 2, big: true }, { key: 'trigger', label: 'Setup', c: 7, r: 5 },
  { key: 'ch2', label: 'CH2', c: 1, r: 6, cls: 'ch2' }, { key: 'auto', label: 'Auto', c: 7, r: 6 },
  { key: 'ch3', label: 'CH3', c: 1, r: 7, cls: 'ch3' }, { key: 'math', label: 'Math', c: 3, r: 7 },
  { key: 'ref', label: 'Ref', c: 4, r: 7 }, { key: 'roll', label: 'Roll', c: 5, r: 7 },
  { key: 'zoom', label: 'Zoom', c: 6, r: 7 }, { key: 'normal', label: 'Normal', c: 7, r: 7 },
  { key: 'ch4', label: 'CH4', c: 1, r: 8, cls: 'ch4' }, { key: 'single', label: 'Single', c: 7, r: 8 },
  { key: 'digital', label: 'Digital', c: 1, r: 9 }, { knob: 'vpos', label: 'Position', c: 2, r: 9 },
  { knob: 'hpos', label: 'Position', c: 4, r: 9 }, { knob: 'level', label: 'Level', c: 6, r: 9, cs: 2 },
];

const LAMP_COLOUR = {
  ch1: 'var(--c1)', ch2: 'var(--c2)', ch3: 'var(--c3)', ch4: 'var(--c4)', run: 'var(--run)', stop: 'var(--stop)',
  math: 'var(--math)', auto: 'var(--run)', normal: 'var(--trigger)', single: 'var(--trigger)',
};
const keyEls = {};
const knobEls = {};
let msgTimer;

function flash(text, err) {
  const m = $('#genmsg');
  m.textContent = text;
  m.classList.toggle('err', !!err);
  clearTimeout(msgTimer);
  msgTimer = setTimeout(() => { m.textContent = ''; }, err ? 8000 : 3000);
}

async function press(key, el) {
  el?.classList.add('down');
  const r = await api('/api/key', { key });
  el?.classList.remove('down');
  if (!r.ok) flash(`${key}: ${r.err}`, true);
}

function buildPanel() {
  const ctl = $('#controls');
  for (const it of LAYOUT) {
    const place = (e) => {
      e.style.gridColumn = `${it.c} / span ${it.cs || it.span || 1}`;
      e.style.gridRow = `${it.r} / span ${it.rs || 1}`;
      return e;
    };
    if (it.sect) { ctl.append(place(h('div', { class: 'sect' }, it.sect))); continue; }
    if (it.key) {
      const b = h('button', { class: `key ${it.cls || ''} ${it.big ? 'big' : ''}`, title: it.label.replace(/- /, ''), 'data-key': it.key },
        h('span', { class: 'led' }), it.label.replace(/- /, '-​'));
      b.addEventListener('click', () => press(it.key, b));
      b._lamps = it.lamps || [it.key];
      keyEls[it.key] = b;
      ctl.append(place(b));
      continue;
    }
    const k = h('div', { class: `knob ${it.big ? 'big' : ''}`, tabindex: '0', role: 'slider',
      'aria-label': `${it.knob} knob: drag or scroll to turn, click to push` }, h('span', { class: 'ind' }));
    const cell = h('div', { class: 'knobcell' }, k, h('span', { class: 'knoblabel' }, it.label));
    knobEls[it.knob] = k;
    wireKnob(k, it.knob);
    ctl.append(place(cell));
  }
}

// A knob: detents accumulate and go out as one turn at a time.
function wireKnob(el, name) {
  let angle = 0, pending = 0, busy = false;
  const turn = (n) => {
    pending += n;
    angle += n * 18;
    el.querySelector('.ind').style.transform = `rotate(${angle}deg)`;
    flush();
  };
  const flush = async () => {
    if (busy || pending === 0) return;
    busy = true;
    const n = Math.max(-100, Math.min(100, pending));
    pending -= n;
    const r = await api('/api/knob', { knob: name, steps: n });
    if (!r.ok) flash(`${name}: ${r.err}`, true);
    busy = false;
    if (pending) flush();
  };
  const push = async () => {
    el.classList.add('pushed');
    const r = await api('/api/knob', { knob: name, push: true });
    el.classList.remove('pushed');
    if (!r.ok) flash(`${name} push: ${r.err}`, true);
  };
  let y0 = null, acc = 0, moved = false;
  el.addEventListener('pointerdown', (e) => { y0 = e.clientY; acc = 0; moved = false; el.setPointerCapture(e.pointerId); el.focus(); });
  el.addEventListener('pointermove', (e) => {
    if (y0 === null) return;
    acc += y0 - e.clientY;
    y0 = e.clientY;
    while (Math.abs(acc) >= 12) {
      const d = Math.sign(acc);
      acc -= 12 * d;
      moved = true;
      turn(d);
    }
  });
  el.addEventListener('pointerup', () => { if (y0 !== null && !moved) push(); y0 = null; });
  el.addEventListener('pointercancel', () => { y0 = null; });
  el.addEventListener('wheel', (e) => { e.preventDefault(); turn(e.deltaY < 0 ? 1 : -1); }, { passive: false });
  el.addEventListener('keydown', (e) => {
    if (e.key === 'ArrowUp' || e.key === 'ArrowRight') { turn(1); e.preventDefault(); }
    if (e.key === 'ArrowDown' || e.key === 'ArrowLeft') { turn(-1); e.preventDefault(); }
    if (e.key === 'Enter' || e.key === ' ') { push(); e.preventDefault(); }
  });
}

function showLamps(on) {
  const lit = new Set(on);
  for (const b of Object.values(keyEls)) {
    const l = b._lamps.find((x) => lit.has(x));
    b.classList.toggle('lit', !!l);
    if (l) b.style.setProperty('--lamp', LAMP_COLOUR[l] || '#e6ecf2');
  }
  knobEls.adjust?.classList.toggle('lamp', lit.has('adjust'));
}

// ---------------------------------------------------------------- screen
let screenSrc = 'sim';
let screenUp = false;
function nextScreen(delay) {
  setTimeout(() => {
    const img = $('#screen');
    img.src = `/api/screen.png?src=${screenSrc}&t=${Date.now()}`;
  }, delay);
}
function wireScreen() {
  const img = $('#screen');
  img.addEventListener('load', () => {
    screenUp = true;
    img.classList.add('up');
    $('#screenmsg').hidden = true;
    nextScreen(document.hidden ? 1500 : 200);
  });
  img.addEventListener('error', () => {
    screenUp = false;
    img.classList.remove('up');
    $('#screenmsg').hidden = false;
    nextScreen(1500);
  });
  img.addEventListener('click', async (e) => {
    if (!screenUp) return;
    const r = img.getBoundingClientRect();
    const x = Math.round((e.clientX - r.left) / r.width * 1024);
    const y = Math.round((e.clientY - r.top) / r.height * 600);
    const rip = h('div', { class: 'ripple' });
    rip.style.left = `${e.clientX - r.left}px`;
    rip.style.top = `${e.clientY - r.top}px`;
    img.parentElement.append(rip);
    setTimeout(() => rip.remove(), 500);
    const res = await api('/api/touch', { x: Math.min(1023, Math.max(0, x)), y: Math.min(599, Math.max(0, y)) });
    if (!res.ok) flash(`touch: ${res.err}`, true);
  });
  $('#screensrc').addEventListener('change', (e) => { screenSrc = e.target.value; });
  nextScreen(0);
}

// ---------------------------------------------------------------- numbers
const SI = { p: 1e-12, n: 1e-9, u: 1e-6, 'µ': 1e-6, m: 1e-3, '': 1, k: 1e3, K: 1e3, M: 1e6, G: 1e9 };
function parseSI(s) {
  const m = String(s).trim().match(/^([-+]?(?:\d+\.?\d*|\.\d+)(?:e[-+]?\d+)?)\s*([pnuµmkKMG]?)[a-zA-Z]*$/);
  if (!m) return NaN;
  return parseFloat(m[1]) * SI[m[2]];
}
function fmtSI(v) {
  if (!isFinite(v)) return '';
  if (v === 0) return '0';
  const a = Math.abs(v);
  for (const [p, f] of [['G', 1e9], ['M', 1e6], ['k', 1e3], ['', 1], ['m', 1e-3], ['u', 1e-6], ['n', 1e-9], ['p', 1e-12]]) {
    if (a >= f * 0.9999) return `${+(v / f).toPrecision(6)}${p}`;
  }
  return String(v);
}
const num = (v) => String(+(+v).toPrecision(9));

// ---------------------------------------------------------------- signal generator
const WAVES = ['sine', 'square', 'pulse', 'triangle', 'ramp', 'dc', 'noise'];
const PROTOS = {
  uart: { name: 'UART', roles: { tx: 'TX' }, def: { tx: 'C1' }, rate: '115200', data: 'Hello', fmt: 'text' },
  spi: { name: 'SPI', roles: { clk: 'CLK', mosi: 'MOSI', miso: 'MISO', cs: 'CS' }, def: { clk: 'C1', mosi: 'C2', cs: 'C3' }, rate: '1M', data: 'A5 3C', fmt: 'hex' },
  i2c: { name: 'I²C', roles: { scl: 'SCL', sda: 'SDA' }, def: { scl: 'C1', sda: 'C2' }, rate: '100k', data: '00 A5', fmt: 'hex' },
  can: { name: 'CAN', roles: { rx: 'RX (logic)', h: 'CAN-H', l: 'CAN-L' }, def: { rx: 'C1' }, rate: '500k', data: 'DE AD BE EF', fmt: 'hex' },
  lin: { name: 'LIN', roles: { tx: 'Bus' }, def: { tx: 'C1' }, rate: '19.2k', data: '01 02', fmt: 'hex' },
  manchester: { name: 'Manchester', roles: { tx: 'Line' }, def: { tx: 'C1' }, rate: '1M', data: 'A5 F0', fmt: 'hex' },
};
const PRESETS = [
  { label: 'UART 115200 "Hello"', p: 'uart' },
  { label: 'SPI 1 MHz', p: 'spi' },
  { label: 'I²C EEPROM write', p: 'i2c' },
  { label: 'CAN 500 k on H/L', p: 'can', lines: { h: 'C1', l: 'C2' }, vpp: '2', offset: '2.5' },
  { label: 'LIN 19.2 k', p: 'lin', vpp: '12', offset: '6' },
  { label: 'Manchester 1 Mb/s', p: 'manchester' },
];
let state = { signals: {} };
let tab = 'C1';
let formFor = null; // which tab the form shows

function parseSpec(spec) {
  const f = (spec || '').split(',');
  const o = { kind: f[0] || 'dc', freq: +f[1] || 0, vpp: +f[2] || 0, offset: +f[3] || 0, opts: {} };
  for (const kv of f.slice(4)) { const i = kv.indexOf('='); if (i > 0) o.opts[kv.slice(0, i)] = kv.slice(i + 1); }
  return o;
}
const protoOf = (kind) => kind.split('.')[0];
const isLine = (kind) => !!PROTOS[protoOf(kind)];

function buildTabs() {
  const t = $('#gentabs');
  t.innerHTML = '';
  for (const c of ['C1', 'C2', 'C3', 'C4', 'Protocol']) {
    const b = h('button', { class: `sm ${c === tab ? 'on' : ''} cc${c[1] || ''}`, role: 'tab', 'aria-selected': c === tab ? 'true' : 'false' }, c);
    b.addEventListener('click', () => { tab = c; buildTabs(); buildForm(); });
    t.append(b);
  }
}

function row(label, ctl, hint) {
  return [h('label', {}, label), hint ? h('div', { class: 'pair' }, ctl, h('span', { class: 'hint' }, hint)) : ctl];
}
const inp = (id, value, attrs = {}) => h('input', { id, value, autocomplete: 'off', spellcheck: 'false', ...attrs });
function sel(id, options, value) {
  const s = h('select', { id });
  for (const o of options) {
    const [v, l] = Array.isArray(o) ? o : [o, o];
    s.append(h('option', { value: v, selected: String(v) === String(value) }, l));
  }
  return s;
}

function buildForm() {
  formFor = tab;
  const body = $('#genbody');
  body.innerHTML = '';
  if (tab === 'Protocol') { buildProtoForm(body); return; }
  const s = parseSpec(state.signals?.[tab]);
  const o = s.opts;
  const line = isLine(s.kind);
  const wave = line ? 'sine' : s.kind;
  const glitch = (o.glitch || '').split(':');
  const burst = (o.burst || '').split(':');
  const form = h('div', { class: 'form' },
    line ? h('div', { class: 'full hint' }, `${tab} now carries a protocol line (${s.kind}); applying a waveform replaces it.`) : null,
    row('Waveform', sel('w-kind', WAVES, wave)),
    row('Frequency', inp('w-freq', fmtSI(line ? 1e3 : s.freq || 1e3)), 'Hz'),
    row('Amplitude', inp('w-vpp', num(s.vpp)), 'Vpp'),
    row('Offset', inp('w-offset', num(s.offset)), 'V'),
    row('Duty', inp('w-duty', o.duty ? num(o.duty * 100) : ''), '%'),
    row('Pulse width', inp('w-width', o.width ? fmtSI(+o.width) : ''), 's'),
    row('Rise / fall', h('div', { class: 'pair' }, inp('w-rise', o.rise ? fmtSI(+o.rise) : '', { placeholder: 'rise' }),
      inp('w-fall', o.fall ? fmtSI(+o.fall) : '', { placeholder: 'fall' })), 's'),
    row('Added noise', inp('w-noise', o.noise ? fmtSI(+o.noise) : ''), 'Vrms'),
    h('div', { class: 'sep' }),
    row('Glitch', h('div', { class: 'pair' }, inp('w-gevery', glitch[0] || '', { placeholder: 'every N', title: 'every N-th period' }),
      inp('w-gwidth', glitch[1] ? fmtSI(+glitch[1]) : '', { placeholder: 'width s' }),
      inp('w-gamp', glitch[2] || '', { placeholder: 'amp V' }))),
    row('Burst', h('div', { class: 'pair' }, inp('w-bon', burst[0] || '', { placeholder: 'on periods' }),
      inp('w-boff', burst[1] || '', { placeholder: 'off periods' }))),
    row('Invert', h('input', { id: 'w-inv', type: 'checkbox', checked: o.inv === '1' })),
  );
  body.append(form, h('div', { class: 'genfoot' },
    h('button', { class: 'ok', id: 'w-apply' }, `Apply to ${tab}`),
    h('label', { class: 'row-tight' }, h('input', { type: 'checkbox', id: 'w-live', checked: true }), 'apply as I type'),
    h('button', { class: 'sm', id: 'w-off' }, 'Off (0 V)')));
  const showRows = () => {
    const k = $('#w-kind').value;
    const vis = { 'w-duty': ['square', 'triangle', 'ramp', 'pulse'], 'w-width': ['pulse'], 'w-rise': ['square', 'pulse'] };
    for (const [id, kinds] of Object.entries(vis)) {
      const el = $(`#${id}`);
      const show = kinds.includes(k);
      el.disabled = !show;
      if (id === 'w-rise') $('#w-fall').disabled = !show;
    }
    $('#w-freq').disabled = k === 'dc' || k === 'noise';
  };
  showRows();
  let t;
  form.addEventListener('input', () => { showRows(); if ($('#w-live').checked) { clearTimeout(t); t = setTimeout(applyWave, 350); } });
  form.addEventListener('change', () => { showRows(); if ($('#w-live').checked) applyWave(); });
  $('#w-apply').addEventListener('click', applyWave);
  $('#w-off').addEventListener('click', () => sendSignal(tab, 'dc,0,0,0'));
}

function waveSpec() {
  const k = $('#w-kind').value;
  const v = (id) => $(`#${id}`).value.trim();
  const need = (id, what, opt) => {
    const s = v(id);
    if (s === '' && opt) return null;
    const x = parseSI(s);
    if (!isFinite(x)) throw new Error(`${what}: "${s}" is not a number (SI suffixes like 2.5k, 10n work)`);
    return x;
  };
  const freq = (k === 'dc' || k === 'noise') ? 0 : need('w-freq', 'frequency');
  const parts = [k, num(freq), num(need('w-vpp', 'amplitude')), num(need('w-offset', 'offset'))];
  const opt = (key, id, what, scale = 1) => {
    if ($(`#${id}`).disabled) return;
    const x = need(id, what, true);
    if (x !== null && x !== 0) parts.push(`${key}=${num(x * scale)}`);
  };
  opt('duty', 'w-duty', 'duty', 0.01);
  opt('width', 'w-width', 'pulse width');
  opt('rise', 'w-rise', 'rise');
  opt('fall', 'w-fall', 'fall');
  opt('noise', 'w-noise', 'noise');
  const ge = need('w-gevery', 'glitch every', true), gw = need('w-gwidth', 'glitch width', true), ga = need('w-gamp', 'glitch amp', true);
  if (ge && gw) parts.push(`glitch=${Math.round(ge)}:${num(gw)}:${num(ga || 0)}`);
  const bon = need('w-bon', 'burst on', true), boff = need('w-boff', 'burst off', true);
  if (bon && boff) parts.push(`burst=${Math.round(bon)}:${Math.round(boff)}`);
  if ($('#w-inv').checked) parts.push('inv=1');
  return parts.join(',');
}

async function applyWave() {
  let spec;
  try { spec = waveSpec(); } catch (e) { flash(e.message, true); return; }
  await sendSignal(tab, spec);
}

async function sendSignal(ch, spec) {
  const r = await api('/api/signal', { ch, spec });
  if (r.ok) { state.signals[ch] = spec; renderSigTable(); flash(`${ch} ← ${spec}`); } else flash(`${ch}: ${r.err}`, true);
}

let proto = { p: 'uart', lines: { ...PROTOS.uart.def }, rate: PROTOS.uart.rate, vpp: '3.3', offset: '1.65', data: PROTOS.uart.data, fmt: 'text', idle: '', opts: {} };

function buildProtoForm(body) {
  const P = PROTOS[proto.p];
  const presets = h('div', { class: 'presets' }, PRESETS.map((pr) => h('button', { class: 'xs', onclick: () => {
    const d = PROTOS[pr.p];
    proto = { p: pr.p, lines: { ...(pr.lines || d.def) }, rate: d.rate, vpp: pr.vpp || '3.3', offset: pr.offset || '1.65', data: d.data, fmt: d.fmt, idle: '', opts: {} };
    buildForm();
    applyBus();
  } }, pr.label)));
  const lineRows = Object.entries(P.roles).map(([role, label]) =>
    row(label, sel(`p-line-${role}`, [['', '—'], 'C1', 'C2', 'C3', 'C4'], proto.lines[role] || '')));
  const o = proto.opts;
  const extra = {
    uart: [row('Data bits', sel('p-bits', [5, 6, 7, 8, 9], o.bits || 8)),
      row('Parity', sel('p-parity', [['n', 'none'], ['e', 'even'], ['o', 'odd'], ['m', 'mark'], ['s', 'space']], o.parity || 'n')),
      row('Stop bits', sel('p-stop', [1, 1.5, 2], o.stop || 1)),
      row('Byte gap', inp('p-gap', o.gap || ''), 's'),
      row('Bit order', sel('p-lsb', [['1', 'LSB first'], ['0', 'MSB first']], o.lsb ?? '1'))],
    spi: [row('Mode', sel('p-mode', [['0', '0 (CPOL 0, CPHA 0)'], ['1', '1 (CPOL 0, CPHA 1)'], ['2', '2 (CPOL 1, CPHA 0)'], ['3', '3 (CPOL 1, CPHA 1)']], o.mode || '0')),
      row('Bits / word', sel('p-bits', [8, 16, 24, 32], o.bits || 8)),
      row('Bit order', sel('p-lsb', [['0', 'MSB first'], ['1', 'LSB first']], o.lsb ?? '0')),
      row('MISO data', inp('p-miso', o.miso || '', { placeholder: 'hex' })),
      row('CS → clock', inp('p-csgap', o.csgap || ''), 's')],
    i2c: [row('Address', inp('p-addr', o.addr || '0x50'), '7-bit'),
      row('Direction', sel('p-rw', [['w', 'write'], ['r', 'read (target sends the data)']], o.rw || 'w'))],
    can: [row('Identifier', inp('p-id', o.id || '0x123')),
      row('Extended id', sel('p-ext', [['0', '11-bit'], ['1', '29-bit']], o.ext || '0')),
      row('Frame', sel('p-rtr', [['0', 'data'], ['1', 'remote (RTR)']], o.rtr || '0'))],
    lin: [row('Frame id', inp('p-id', o.id || '0x10'), '0…0x3F'),
      row('Checksum', sel('p-checksum', [['enh', 'enhanced (2.x)'], ['cls', 'classic (1.x)']], o.checksum || 'enh'))],
    manchester: [row('Bit order', sel('p-lsb', [['0', 'MSB first'], ['1', 'LSB first']], o.lsb ?? '0'))],
  }[proto.p];
  const form = h('div', { class: 'form' },
    row('Protocol', sel('p-kind', Object.entries(PROTOS).map(([k, v]) => [k, v.name]), proto.p)),
    ...lineRows,
    row('Bit rate', inp('p-rate', proto.rate), 'bit/s'),
    row('Levels', h('div', { class: 'pair' }, inp('p-vpp', proto.vpp, { title: 'swing (Vpp)' }), h('span', { class: 'hint' }, 'Vpp at'),
      inp('p-offset', proto.offset, { title: 'mid level (V)' }), h('span', { class: 'hint' }, 'V mid'))),
    row('Payload', h('div', { class: 'pair' }, inp('p-data', proto.data), sel('p-fmt', [['text', 'text'], ['hex', 'hex']], proto.fmt))),
    row('Idle after frame', inp('p-idle', proto.idle, { placeholder: '20 bit times' }), 's'),
    h('div', { class: 'sep' }),
    ...extra,
  );
  body.append(presets, form, h('div', { class: 'genfoot' }, h('button', { class: 'ok', id: 'p-apply' }, 'Apply protocol'),
    h('span', { class: 'hint' }, 'the frame repeats from the trigger point')));
  $('#p-kind').addEventListener('change', (e) => {
    const d = PROTOS[e.target.value];
    proto = { ...proto, p: e.target.value, lines: { ...d.def }, rate: d.rate, data: d.data, fmt: d.fmt, opts: {} };
    buildForm();
  });
  $('#p-apply').addEventListener('click', applyBus);
}

function toHex(s, fmt) {
  if (fmt === 'text') return Array.from(new TextEncoder().encode(s), (b) => b.toString(16).padStart(2, '0')).join('');
  const x = s.replace(/0x/gi, '').replace(/[\s,:_]/g, '');
  if (!/^([0-9a-fA-F]{2})*$/.test(x)) throw new Error(`payload "${s}": want hex bytes like A5 3C`);
  return x.toLowerCase();
}

function readProto() {
  const v = (id) => $(`#${id}`)?.value.trim() ?? '';
  const p = { ...proto, lines: {}, opts: {} };
  for (const role of Object.keys(PROTOS[proto.p].roles)) p.lines[role] = v(`p-line-${role}`);
  p.rate = v('p-rate'); p.vpp = v('p-vpp'); p.offset = v('p-offset'); p.data = $('#p-data').value; p.fmt = v('p-fmt'); p.idle = v('p-idle');
  for (const k of ['bits', 'parity', 'stop', 'gap', 'lsb', 'mode', 'miso', 'csgap', 'addr', 'rw', 'id', 'ext', 'rtr', 'checksum']) {
    if ($(`#p-${k}`)) p.opts[k] = v(`p-${k}`);
  }
  return p;
}

async function applyBus() {
  try {
    const p = readProto();
    proto = p;
    const used = Object.values(p.lines).filter(Boolean);
    if (!used.length) throw new Error('place at least one line on a channel');
    if (new Set(used).size !== used.length) throw new Error('two lines on one channel');
    const rate = parseSI(p.rate), vpp = parseSI(p.vpp), offset = parseSI(p.offset);
    if (!(rate > 0)) throw new Error(`bit rate "${p.rate}"`);
    if (!isFinite(vpp) || !isFinite(offset)) throw new Error('levels: numbers in V');
    const opts = { data: toHex(p.data, p.fmt) };
    if (p.idle) { const x = parseSI(p.idle); if (!isFinite(x)) throw new Error(`idle "${p.idle}"`); opts.idle = num(x); }
    for (const [k, val] of Object.entries(p.opts)) {
      if (val === '') continue;
      if (k === 'gap' || k === 'csgap') { const x = parseSI(val); if (!isFinite(x)) throw new Error(`${k} "${val}"`); opts[k] = num(x); continue; }
      if (k === 'miso') { opts[k] = toHex(val, 'hex'); continue; }
      opts[k] = val;
    }
    const lines = {};
    for (const [r, c] of Object.entries(p.lines)) if (c) lines[r] = c;
    const r = await api('/api/bus', { kind: p.p, lines, rate, vpp, offset, opts });
    if (!r.ok) throw new Error(r.err);
    flash(`${PROTOS[p.p].name} on ${Object.entries(lines).map(([k, c]) => `${k.toUpperCase()}=${c}`).join(' ')}`);
    poll();
  } catch (e) { flash(e.message, true); }
}

function describe(spec) {
  const s = parseSpec(spec);
  if (isLine(s.kind)) {
    const P = PROTOS[protoOf(s.kind)];
    return `${P.name} ${s.kind.split('.')[1]?.toUpperCase() || ''} ${fmtSI(s.freq)}bit/s`;
  }
  if (s.kind === 'dc') return `DC ${num(s.offset)} V`;
  if (s.kind === 'noise') return `noise σ ${num(s.vpp / 6)} V`;
  return `${s.kind} ${fmtSI(s.freq)}Hz ${num(s.vpp)} Vpp ${s.offset ? `${num(s.offset)} V offset` : ''}`;
}

function renderSigTable() {
  const tb = $('#sigtable tbody');
  tb.innerHTML = '';
  for (const c of ['C1', 'C2', 'C3', 'C4']) {
    const spec = state.signals?.[c] || '';
    tb.append(h('tr', {}, h('td', { class: `cc${c[1]}`, style: `color: var(--c${c[1]})` }, c),
      h('td', {}, describe(spec), h('div', { class: 'spec' }, spec))));
  }
}

// ---------------------------------------------------------------- state
let polling = false;
async function poll() {
  if (polling) return;
  polling = true;
  try {
    const st = await api('/api/state');
    const first = !state.run;
    state = st;
    const run = st.run || {};
    const chip = $('#runchip');
    chip.className = `chip ${run.state}`;
    let info = '';
    const up = run.state === 'running' || run.state === 'attached';
    if (up && !run.serving) chip.className += ' booting';
    if (run.state === 'running' && run.secs) {
      const left = run.left;
      info = `run ${run.name} · ${run.serving ? '' : 'the app is starting · '}${Math.floor(left / 60)}:${String(Math.floor(left % 60)).padStart(2, '0')} left (the simulator caps a run; simlab starts the next)`;
    } else if (run.state === 'queued') info = 'waiting for the simulator (other runs hold its lock)';
    else if (run.state === 'attached') info = `attached to ${run.name}`;
    else if (run.err) info = run.err.split('\n')[0];
    chip.textContent = up && !run.serving ? 'starting' : (run.state || '?');
    document.body.dataset.run = up && run.serving ? 'serving' : (run.state || '');
    document.body.dataset.runs = String(run.runs || 0);
    $('#runinfo').textContent = info;
    showLamps(st.lamps || []);
    renderSigTable();
    const url = st.web_url.replace('127.0.0.1', location.hostname || '127.0.0.1');
    for (const a of ['#weblink', '#weblink2']) $(a).href = url;
    $('#webhint').textContent = url;
    // the web UI loads once this run's app serves, and again for each new run
    const fr = $('#webframe');
    const want = up && run.serving ? `${url}#run${run.runs}` : '';
    if (want && fr.dataset.src !== want) { fr.dataset.src = want; fr.dataset.url = url; fr.src = url; }
    if (!want) { fr.dataset.src = ''; }
    if (first || formFor === null) buildForm();
  } catch { /* simlab itself is gone: keep trying */ }
  polling = false;
}

$('#restart').addEventListener('click', async () => {
  const r = await api('/api/restart', {});
  flash(r.ok ? 'restarting the simulator run…' : r.err, !r.ok);
  poll();
});
$('#webreload').addEventListener('click', () => { const fr = $('#webframe'); if (fr.dataset.url && fr.dataset.src) fr.src = fr.dataset.url; });

buildPanel();
buildTabs();
wireScreen();
poll();
setInterval(poll, 800);
