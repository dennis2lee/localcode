// Flicker harness for the SSH panel.
//
// Starts a Chrome of its own (a temporary profile, never the person's),
// opens the Web UI, types into the SSH panel with real key events through
// the DevTools protocol, and records, for every animation frame, what the
// panel shows: its rows, cursor, scroll offsets and boxes. Beside that it
// records the frames the daemon sent, the requests the panel made, and the
// pixels Chrome drew (a screencast, diffed frame by frame).
//
//   node flicker.mjs --chrome <path> --url http://127.0.0.1:4197/ --out <dir> --name <run>
//        [--headful] [--dsf 1.25] [--size 1400x900] [--classic 17]
//        [--text "echo hello"] [--interval 90] [--enter] [--pre "seq 1 80"]...
//        [--fresh] [--inject hide|scroll|cursor] [--ime] [--target devbox]

import { spawn } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import zlib from 'node:zlib';

const argv = process.argv.slice(2);
const opt = { pre: [], extra: [] };
for (let i = 0; i < argv.length; i++) {
  const a = argv[i];
  if (!a.startsWith('--')) continue;
  const k = a.slice(2);
  if (['headful', 'enter', 'fresh', 'ime', 'noshot', 'norec', 'pin', 'guard', 'dump'].includes(k)) opt[k] = true;
  else if (k === 'pre') opt.pre.push(argv[++i]);
  else if (k === 'extra') opt.extra.push(argv[++i]);
  else opt[k] = argv[++i];
}
const URL0 = opt.url || 'http://127.0.0.1:4197/';
const OUT = opt.out || '.';
const NAME = opt.name || 'run';
const [W, H] = (opt.size || '1400x900').split('x').map(Number);
const INTERVAL = Number(opt.interval || 90);
const TEXT = opt.text === undefined ? 'echo flicker test 12345' : opt.text;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ---------- the script that runs in the page before its own ----------
const INIT = `(() => {
  const L = (window.__lc = { origin: performance.timeOrigin, sse: [], net: [], frames: [], muts: [], shifts: [], texts: {}, marks: [] });
  const now = () => Math.round(performance.now() * 10) / 10;
  const hash = (s) => { let h = 5381; for (let i = 0; i < s.length; i++) h = ((h << 5) + h + s.charCodeAt(i)) | 0; return (h >>> 0).toString(36); };
  const ES = window.EventSource;
  window.EventSource = class extends ES {
    constructor(url, init) {
      super(url, init);
      if (!String(url).includes('/ssh/stream')) return;
      L.sse.push({ t: now(), ev: 'new', url: String(url).replace(/^.*\\/ssh/, '/ssh') });
      this.addEventListener('open', () => L.sse.push({ t: now(), ev: 'open' }));
      this.addEventListener('error', () => L.sse.push({ t: now(), ev: 'error', rs: this.readyState }));
      this.addEventListener('message', (e) => {
        try {
          const m = JSON.parse(e.data);
          const rec = { t: now(), ev: m.kind };
          if (m.kind === 'screen') {
            const s = m.data.screen;
            const text = s.lines.map((l) => (l || []).map((r) => r.t).join('')).join('\\n');
            rec.h = hash(JSON.stringify(s.lines)); rec.ink = text.replace(/\\s/g, '').length;
            rec.cur = s.cursor; rec.cx = s.cx; rec.cy = s.cy; rec.cols = s.cols; rec.rows = s.rows; rec.alt = s.alt;
            if (!L.texts['s' + rec.h]) L.texts['s' + rec.h] = text;
          } else if (m.kind === 'state') rec.state = JSON.stringify(m.data);
          else if (m.kind === 'scroll') { rec.from = m.data.from; rec.n = (m.data.lines || []).length; }
          L.sse.push(rec);
        } catch (err) {}
      });
    }
  };
  const F = window.fetch;
  window.fetch = function (input, init) {
    const url = String(typeof input === 'string' ? input : input.url);
    if (!url.includes('/ssh')) return F.apply(this, arguments);
    const rec = { t: now(), method: (init && init.method) || 'GET', url: url.replace(/^.*\\/ssh/, '/ssh'), body: init && typeof init.body === 'string' ? init.body.slice(0, 160) : undefined };
    L.net.push(rec);
    return F.apply(this, arguments).then((r) => { rec.status = r.status; rec.dt = Math.round(now() - rec.t); return r; }, (e) => { rec.err = String(e); throw e; });
  };
  L.start = () => {
    const el = (id) => document.getElementById(id);
    const panel = el('ssh-panel'), term = el('ssh-term'), lines = el('ssh-lines'), screen = el('ssh-screen'), scroll = el('ssh-scroll'), cursor = el('ssh-cursor'), input = el('ssh-input');
    const r1 = (n) => Math.round(n * 100) / 100;
    const sig = () => {
      const rows = [...screen.children].filter((c) => c !== cursor);
      const text = rows.map((r) => r.textContent).join('\\n');
      const th = hash(text);
      if (!L.texts['d' + th]) L.texts['d' + th] = text;
      const lr = lines.getBoundingClientRect(), tr = term.getBoundingClientRect(), cr = cursor.getBoundingClientRect(), ir = input.getBoundingClientRect(), sr = screen.getBoundingClientRect();
      const anc = [];
      for (let n = term; n && n !== document; n = n.parentNode) if (n.scrollTop || n.scrollLeft) anc.push((n.id || n.className || n.tagName) + ':' + n.scrollLeft + ',' + n.scrollTop);
      const ae = document.activeElement;
      return {
        th, nrows: rows.length, ink: text.replace(/\\s/g, '').length,
        curHidden: cursor.hidden, cx: r1(cr.left - sr.left), cy: r1(cr.top - sr.top),
        focusIn: term.matches(':focus-within'), focus: ae ? (ae.id || ae.tagName) : '',
        lst: r1(lines.scrollTop), lsh: lines.scrollHeight, lcw: lines.clientWidth, lch: lines.clientHeight, sbar: lines.offsetWidth - lines.clientWidth,
        screenTop: r1(sr.top), screenLeft: r1(sr.left), screenH: r1(sr.height),
        term: [r1(tr.left), r1(tr.top), r1(tr.width), r1(tr.height)].join(','),
        termHidden: term.hidden, panelHidden: panel.hidden || panel.classList.contains('collapsed'),
        anc: anc.join('|'), doc: scrollX + ',' + scrollY,
        nscroll: scroll.children.length,
        status: (el('ssh-status') || {}).textContent || '',
        inp: [r1(ir.left - tr.left), r1(ir.top - tr.top)].join(','),
        err: el('ssh-error') && !el('ssh-error').hidden ? el('ssh-error').textContent : '',
        q: !!(el('ssh-question') && !el('ssh-question').hidden),
        tabs: hash(el('ssh-tab-list').innerHTML),
        off: [r1(ir.left - cr.left), r1(ir.top - cr.top)].join(','),
        out: [r1(Math.max(0, tr.left - ir.left)), r1(Math.max(0, tr.top - ir.top)), r1(Math.max(0, ir.right - tr.right)), r1(Math.max(0, ir.bottom - tr.bottom))].join(','),
      };
    };
    L.sig = sig;
    let prev = null; let n = 0;
    const tick = () => {
      n++;
      const s = sig();
      const j = JSON.stringify(s);
      if (j !== prev) { L.frames.push({ t: now(), n, s }); prev = j; }
      L.raf = n;
      if (L.running) requestAnimationFrame(tick);
    };
    L.running = true;
    requestAnimationFrame(tick);
    new MutationObserver((recs) => {
      const agg = {};
      for (const r of recs) {
        const tg = r.target.nodeType === 1 ? (r.target.id || r.target.className || r.target.tagName) : '#text';
        const k = r.type + ':' + tg + (r.attributeName ? '@' + r.attributeName : '') + (r.type === 'childList' ? '+' + r.addedNodes.length + '-' + r.removedNodes.length : '');
        agg[k] = (agg[k] || 0) + 1;
      }
      L.muts.push({ t: now(), agg });
    }).observe(panel, { subtree: true, childList: true, attributes: true, attributeFilter: ['hidden', 'class'] });
    try {
      new PerformanceObserver((list) => {
        for (const e of list.getEntries()) L.shifts.push({ t: Math.round(e.startTime * 10) / 10, v: e.value, input: e.hadRecentInput, src: (e.sources || []).map((s) => ({ node: s.node ? (s.node.id || s.node.className || s.node.nodeName) : '?', prev: [s.previousRect.x, s.previousRect.y, s.previousRect.width, s.previousRect.height], cur: [s.currentRect.x, s.currentRect.y, s.currentRect.width, s.currentRect.height] })) });
      }).observe({ type: 'layout-shift', buffered: false });
    } catch (e) { L.shiftErr = String(e); }
  };
  L.stop = () => { L.running = false; };
  L.mark = (name) => L.marks.push({ t: now(), name });
  const ZOOM = __ZOOM__;
  if (ZOOM) { try { localStorage.setItem('localcode.zoom', String(ZOOM)); } catch (e) {} }
  const CLASSIC = __CLASSIC__;
  if (CLASSIC) {
    const add = () => { const st = document.createElement('style'); st.textContent = '::-webkit-scrollbar{width:' + CLASSIC + 'px;height:' + CLASSIC + 'px}::-webkit-scrollbar-thumb{background:#888}::-webkit-scrollbar-track{background:#333}'; document.documentElement.appendChild(st); };
    if (document.documentElement) add(); else document.addEventListener('readystatechange', add, { once: true });
  }
})();`;

// ---------- DevTools protocol ----------
class CDP {
  constructor(ws) {
    this.ws = ws; this.n = 0; this.pending = new Map(); this.handlers = [];
    ws.onmessage = (ev) => {
      const m = JSON.parse(ev.data);
      if (m.id && this.pending.has(m.id)) {
        const p = this.pending.get(m.id); this.pending.delete(m.id);
        if (m.error) p.rej(new Error(`${p.method}: ${m.error.message}`)); else p.res(m.result);
      } else if (m.method) for (const h of this.handlers) h(m);
    };
  }
  send(method, params = {}, sessionId) {
    const id = ++this.n;
    return new Promise((res, rej) => {
      this.pending.set(id, { res, rej, method });
      this.ws.send(JSON.stringify(sessionId ? { id, method, params, sessionId } : { id, method, params }));
    });
  }
}

function decodePNG(buf) {
  let pos = 8; let width = 0, height = 0, depth = 0, ctype = 0, lace = 0; const idat = [];
  while (pos < buf.length) {
    const len = buf.readUInt32BE(pos); const type = buf.toString('latin1', pos + 4, pos + 8);
    const data = buf.subarray(pos + 8, pos + 8 + len);
    if (type === 'IHDR') { width = data.readUInt32BE(0); height = data.readUInt32BE(4); depth = data[8]; ctype = data[9]; lace = data[12]; }
    else if (type === 'IDAT') idat.push(data);
    else if (type === 'IEND') break;
    pos += 12 + len;
  }
  if (depth !== 8 || lace !== 0 || (ctype !== 2 && ctype !== 6)) throw new Error(`png ${depth}/${ctype}/${lace} not handled`);
  const bpp = ctype === 6 ? 4 : 3;
  const raw = zlib.inflateSync(Buffer.concat(idat));
  const stride = width * bpp;
  const out = Buffer.alloc(height * stride);
  let prev = null;
  for (let y = 0; y < height; y++) {
    const f = raw[y * (stride + 1)];
    const row = raw.subarray(y * (stride + 1) + 1, (y + 1) * (stride + 1));
    const cur = out.subarray(y * stride, (y + 1) * stride);
    for (let x = 0; x < stride; x++) {
      const a = x >= bpp ? cur[x - bpp] : 0; const b = prev ? prev[x] : 0; const c = x >= bpp && prev ? prev[x - bpp] : 0;
      let v = row[x];
      if (f === 1) v += a; else if (f === 2) v += b; else if (f === 3) v += (a + b) >> 1;
      else if (f === 4) { const p = a + b - c; const pa = Math.abs(p - a), pb = Math.abs(p - b), pc = Math.abs(p - c); v += pa <= pb && pa <= pc ? a : pb <= pc ? b : c; }
      cur[x] = v & 255;
    }
    prev = cur;
  }
  return { width, height, bpp, data: out };
}

// diff counts the pixels of rect (device px) that differ between two frames.
function diff(a, b, rect) {
  if (!a || !b || a.width !== b.width || a.height !== b.height) return { n: -1 };
  let n = 0, x0 = 1e9, y0 = 1e9, x1 = -1, y1 = -1;
  const bpp = a.bpp; const stride = a.width * bpp;
  const rx0 = Math.max(0, rect.x0), ry0 = Math.max(0, rect.y0), rx1 = Math.min(a.width, rect.x1), ry1 = Math.min(a.height, rect.y1);
  for (let y = ry0; y < ry1; y++) {
    const o = y * stride;
    for (let x = rx0; x < rx1; x++) {
      const i = o + x * bpp;
      if (a.data[i] !== b.data[i] || a.data[i + 1] !== b.data[i + 1] || a.data[i + 2] !== b.data[i + 2]) {
        n++; if (x < x0) x0 = x; if (x > x1) x1 = x; if (y < y0) y0 = y; if (y > y1) y1 = y;
      }
    }
  }
  if (!n) return { n: 0 };
  // The rows that changed, as bands: [first row, last row, pixels, leftmost, rightmost].
  const bands = [];
  for (let y = y0; y <= y1; y++) {
    const o = y * stride;
    let c = 0, l = 1e9, r = -1;
    for (let x = rx0; x < rx1; x++) {
      const i = o + x * bpp;
      if (a.data[i] !== b.data[i] || a.data[i + 1] !== b.data[i + 1] || a.data[i + 2] !== b.data[i + 2]) { c++; if (x < l) l = x; if (x > r) r = x; }
    }
    if (!c) continue;
    const last = bands[bands.length - 1];
    if (last && last[1] === y - 1) { last[1] = y; last[2] += c; last[3] = Math.min(last[3], l); last[4] = Math.max(last[4], r); } else bands.push([y, y, c, l, r]);
  }
  return { n, box: [x0, y0, x1 - x0 + 1, y1 - y0 + 1], bands };
}

const VK = { ' ': 32, '-': 189, '=': 187, ',': 188, '.': 190, '/': 191, ';': 186, "'": 222, '[': 219, ']': 221, '\\': 220, '`': 192 };
function keyOf(c) {
  if (/[a-z]/i.test(c)) return { code: 'Key' + c.toUpperCase(), vk: c.toUpperCase().charCodeAt(0) };
  if (/[0-9]/.test(c)) return { code: 'Digit' + c, vk: c.charCodeAt(0) };
  if (c === ' ') return { code: 'Space', vk: 32 };
  return { code: '', vk: VK[c] || 0 };
}

async function main() {
  fs.mkdirSync(OUT, { recursive: true });
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'lc-flicker-'));
  const chromeArgs = [
    `--user-data-dir=${dataDir}`, '--remote-debugging-port=0', '--no-first-run', '--no-default-browser-check',
    '--disable-search-engine-choice-screen', '--disable-background-timer-throttling', '--disable-renderer-backgrounding',
    '--disable-backgrounding-occluded-windows', '--disable-features=Translate,CalculateNativeWinOcclusion', `--window-size=${W},${H}`,
  ];
  if (!opt.headful) chromeArgs.unshift('--headless=new');
  if (opt.dsf) chromeArgs.push(`--force-device-scale-factor=${opt.dsf}`);
  chromeArgs.push(...opt.extra);
  chromeArgs.push('about:blank');
  const chrome = spawn(opt.chrome, chromeArgs, { stdio: 'ignore' });
  let chromeExit = null;
  chrome.on('exit', (code, sig) => { chromeExit = `${code}/${sig}`; });
  const portFile = path.join(dataDir, 'DevToolsActivePort');
  let port = '', wsPath = '';
  for (let i = 0; i < 300 && !port; i++) {
    if (chromeExit) throw new Error(`chrome exited at once: ${chromeExit}`);
    try { [port, wsPath] = fs.readFileSync(portFile, 'utf8').trim().split('\n'); } catch (e) { await sleep(100); }
  }
  if (!port) throw new Error('chrome wrote no DevToolsActivePort');
  const ws = new WebSocket(`ws://127.0.0.1:${port}${wsPath}`);
  await new Promise((res, rej) => { ws.onopen = res; ws.onerror = () => rej(new Error('websocket failed')); });
  const cdp = new CDP(ws);
  const result = { name: NAME, opt, started: Date.now() };
  const shots = [];
  try {
    const { targetId } = await cdp.send('Target.createTarget', { url: 'about:blank' });
    const { sessionId } = await cdp.send('Target.attachToTarget', { targetId, flatten: true });
    const send = (m, p) => cdp.send(m, p, sessionId);
    const ev = async (expression) => {
      const r = await send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true });
      if (r.exceptionDetails) throw new Error(`evaluate: ${r.exceptionDetails.exception?.description || r.exceptionDetails.text}`);
      return r.result.value;
    };
    cdp.handlers.push((m) => {
      if (m.method === 'Page.screencastFrame' && m.sessionId === sessionId) {
        shots.push({ ts: m.params.metadata.timestamp * 1000, data: m.params.data });
        send('Page.screencastFrameAck', { sessionId: m.params.sessionId }).catch(() => {});
      }
    });
    await send('Page.enable');
    await send('Runtime.enable');
    await send('Page.addScriptToEvaluateOnNewDocument', { source: INIT.replace('__CLASSIC__', String(Number(opt.classic || 0))).replace('__ZOOM__', String(Number(opt.zoom || 0))) });
    await send('Page.navigate', { url: URL0 });
    // The page opens the one conversation the sandbox has.
    let sid = '';
    for (let i = 0; i < 100 && !sid; i++) {
      await sleep(200);
      sid = await ev(`fetch('/api/sessions').then(r => r.json()).then(d => { const s = Array.isArray(d) ? d : d.sessions; return s && s[0] ? s[0].id : ''; }).catch(() => '')`);
    }
    if (!sid) throw new Error('no conversation');
    const api = (method, p, body) => ev(`fetch('/api/sessions/${sid}/ssh${p}', { method: '${method}', headers: { 'Content-Type': 'application/json' }${body ? `, body: ${JSON.stringify(JSON.stringify(body))}` : ''} }).then(async r => ({ status: r.status, body: await r.text() }))`);
    const state = async () => JSON.parse((await api('GET', '')).body);
    for (let i = 0; i < 50; i++) { if (await ev(`!!document.getElementById('ssh-panel') && !document.getElementById('ssh-panel').hidden`)) break; await sleep(200); }
    if (opt.fresh) {
      for (const t of (await state()).terminals || []) await api('DELETE', `/${t.id}`, {});
      await sleep(300);
    }
    let st = await state();
    if (!(st.terminals || []).some((t) => t.state === 'connected')) {
      const r = await api('POST', '/connect', { target: opt.target || 'devbox' });
      if (r.status >= 300) throw new Error(`connect: ${r.status} ${r.body}`);
    }
    for (let i = 0; i < 100; i++) {
      st = await state();
      const t = (st.terminals || [])[st.terminals.length - 1];
      if (t && t.question) throw new Error(`the terminal asks: ${JSON.stringify(t.question)}`);
      if (t && t.state === 'connected') break;
      if (t && t.state === 'ended') throw new Error(`ended: ${t.reason}`);
      await sleep(200);
    }
    // Wait for the first screen to settle (integration is typed once the prompt is quiet).
    let last = '', same = 0;
    for (let i = 0; i < 80 && same < 8; i++) {
      await sleep(250);
      const cur = await ev(`document.getElementById('ssh-screen').textContent + '|' + document.getElementById('ssh-status').textContent`);
      same = cur === last && cur.length > 3 ? same + 1 : 0; last = cur;
    }
    result.info = await ev(`(() => { const d = document.createElement('div'); d.style.cssText = 'width:100px;height:100px;overflow:scroll;position:absolute;top:-999px'; document.body.appendChild(d); const sb = d.offsetWidth - d.clientWidth; d.remove(); const l = document.getElementById('ssh-lines'); const cs = getComputedStyle(l); return { ua: navigator.userAgent, zoom: document.documentElement.style.zoom || '', dpr: devicePixelRatio, inner: [innerWidth, innerHeight], scrollbar: sb, font: cs.fontFamily, fontSize: cs.fontSize, lines: [l.clientWidth, l.clientHeight, l.offsetWidth], status: document.getElementById('ssh-status').textContent }; })()`);
    const rect = await ev(`(() => { const r = document.getElementById('ssh-lines').getBoundingClientRect(); return { x: r.left, y: r.top, w: r.width, h: r.height }; })()`);
    const click = async () => {
      const x = rect.x + rect.w / 2, y = rect.y + rect.h / 2;
      await send('Input.dispatchMouseEvent', { type: 'mousePressed', x, y, button: 'left', clickCount: 1 });
      await send('Input.dispatchMouseEvent', { type: 'mouseReleased', x, y, button: 'left', clickCount: 1 });
    };
    await click();
    await sleep(200);
    result.focusAfterClick = await ev(`document.activeElement && document.activeElement.id`);
    const keys = [];
    const typeChar = async (c) => {
      const k = keyOf(c);
      keys.push({ t: Date.now(), c });
      await send('Input.dispatchKeyEvent', { type: 'keyDown', key: c, code: k.code, text: c, unmodifiedText: c, windowsVirtualKeyCode: k.vk });
      await send('Input.dispatchKeyEvent', { type: 'keyUp', key: c, code: k.code, windowsVirtualKeyCode: k.vk });
    };
    const enter = async () => {
      keys.push({ t: Date.now(), c: '\\r' });
      await send('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, text: '\r' });
      await send('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
    };
    for (const cmd of opt.pre) {
      for (const c of cmd) { await typeChar(c); await sleep(15); }
      await enter();
      await sleep(1500);
    }
    keys.length = 0;
    // Record: idle, type, idle.
    // --pin keeps the typing box at the terminal's corner: the intervention
    // that tells whether its place is the cause.
    if (opt.pin) await ev(`(() => { const st = document.createElement('style'); st.textContent = '#ssh-input{left:0!important;top:0!important}'; document.head.appendChild(st); })()`);
    // --guard puts the terminal's box back whenever it scrolls: the
    // intervention that tells whether that scroll is what is seen.
    if (opt.guard) await ev(`(() => { const t = document.getElementById('ssh-term'); t.addEventListener('scroll', () => { if (t.scrollTop) t.scrollTop = 0; if (t.scrollLeft) t.scrollLeft = 0; }); })()`);
    // --norec leaves the page unobserved from inside: the screencast alone.
    if (!opt.norec) await ev(`__lc.start()`);
    if (!opt.noshot) await send('Page.startScreencast', { format: 'png', everyNthFrame: 1 });
    await sleep(1500);
    await ev(`__lc.mark('type-start')`);
    if (opt.ime) {
      // A composition as an input method makes one: the syllable grows, then commits.
      for (const syl of [['ㅎ', '하', '한'], ['ㄱ', '그', '글']]) {
        for (const step of syl) { keys.push({ t: Date.now(), c: `ime:${step}` }); await send('Input.imeSetComposition', { text: step, selectionStart: step.length, selectionEnd: step.length }); await sleep(INTERVAL); }
        keys.push({ t: Date.now(), c: `commit:${syl[2]}` });
        await send('Input.insertText', { text: syl[2] });
        await sleep(INTERVAL);
      }
    }
    let i = 0;
    for (const c of TEXT) {
      await typeChar(c);
      i++;
      if (opt.inject && i === Math.floor(TEXT.length / 2)) {
        // A flicker made on purpose, one frame long, to see that the recorders see one.
        const what = { hide: `const s = document.getElementById('ssh-screen'); s.style.visibility = 'hidden'; requestAnimationFrame(() => requestAnimationFrame(() => { s.style.visibility = ''; }));`,
          scroll: `const l = document.getElementById('ssh-lines'); const v = l.scrollTop; l.scrollTop = Math.max(0, v - 17); requestAnimationFrame(() => requestAnimationFrame(() => { l.scrollTop = v; }));`,
          cursor: `const c = document.getElementById('ssh-cursor'); c.hidden = true; requestAnimationFrame(() => requestAnimationFrame(() => { c.hidden = false; }));` }[opt.inject];
        await ev(`__lc.mark('inject-${opt.inject}'); requestAnimationFrame(() => { ${what} }); 0`);
      }
      await sleep(INTERVAL);
    }
    if (opt.enter) { await enter(); await sleep(1500); }
    await ev(`__lc.mark('type-end')`);
    await sleep(1500);
    if (!opt.noshot) await send('Page.stopScreencast');
    await ev(`__lc.stop()`);
    const log = await ev(`JSON.stringify(__lc)`);
    result.log = JSON.parse(log);
    result.keys = keys;
    result.rect = rect;
    result.final = opt.norec ? null : await ev(`__lc.sig()`);
  } finally {
    try { await cdp.send('Browser.close'); } catch (e) {}
    await sleep(500);
    try { chrome.kill(); } catch (e) {}
    try { fs.rmSync(dataDir, { recursive: true, force: true }); } catch (e) {}
  }

  // ---------- analysis ----------
  const L = result.log; const origin = L.origin;
  const dpr = result.info.dpr;
  const r = result.rect;
  const box = { x0: Math.floor(r.x * dpr), y0: Math.floor(r.y * dpr), x1: Math.ceil((r.x + r.w) * dpr), y1: Math.ceil((r.y + r.h) * dpr) };
  const pix = [];
  let prev = null, prev2 = null, dumped = 0;
  for (let i = 0; i < shots.length; i++) {
    const img = decodePNG(Buffer.from(shots[i].data, 'base64'));
    const d1 = prev ? diff(prev, img, box) : { n: 0 };
    const d2 = prev2 ? diff(prev2, img, box) : { n: 0 };
    const tall = d1.box && d1.box[3] > 40 && d1.n < 2000;
    pix.push({ t: Math.round(shots[i].ts - origin), n: d1.n, box: d1.box, back2: d2.n, bands: tall ? d1.bands : undefined });
    if (opt.dump && tall && dumped < 6) {
      dumped++;
      fs.writeFileSync(path.join(OUT, `${NAME}-frame${i - 1}.png`), Buffer.from(shots[i - 1].data, 'base64'));
      fs.writeFileSync(path.join(OUT, `${NAME}-frame${i}.png`), Buffer.from(shots[i].data, 'base64'));
    }
    prev2 = prev; prev = img;
  }
  result.pix = pix;
  result.shots = shots.length;
  // A row is about this many pixels; a key changes a cell or two.
  const cell = { w: 7.8 * dpr, h: 16.9 * dpr };
  const big = pix.filter((p) => p.n > 0 && p.box && (p.box[3] > 2.5 * cell.h || p.n > 60 * cell.w * cell.h));
  // A flash: a big change that a later frame within 150 ms mostly takes back.
  const flashes = [];
  for (let i = 1; i + 1 < pix.length; i++) {
    const p = pix[i], q = pix[i + 1];
    if (big.includes(p) && q.t - p.t < 150 && q.n > 0 && q.back2 >= 0 && q.back2 < p.n / 4) flashes.push({ t: p.t, ms: q.t - p.t, n: p.n, box: p.box, left: q.back2 });
  }
  const frames = L.frames;
  const fields = Object.keys(frames[0]?.s || {});
  const flips = {};
  for (let i = 1; i < frames.length; i++) for (const f of fields) if (JSON.stringify(frames[i].s[f]) !== JSON.stringify(frames[i - 1].s[f])) (flips[f] = flips[f] || []).push({ t: frames[i].t, from: frames[i - 1].s[f], to: frames[i].s[f] });
  // A value that came back within 150 ms is a transient: A, B, A.
  const transients = [];
  for (const f of fields) {
    if (f === 'th' || f === 'ink' || f === 'cx' || f === 'inp' || f === 'off' || f === 'out') continue;
    const fl = flips[f] || [];
    for (let i = 1; i < fl.length; i++) if (fl[i].t - fl[i - 1].t < 150 && JSON.stringify(fl[i].to) === JSON.stringify(fl[i - 1].from)) transients.push({ field: f, t: fl[i - 1].t, ms: Math.round(fl[i].t - fl[i - 1].t), a: fl[i - 1].from, b: fl[i - 1].to });
  }
  // Ink that dropped and came back: a row drawn empty for a frame.
  const inkDips = [];
  for (let i = 1; i + 1 < frames.length; i++) {
    const a = frames[i - 1].s.ink, b = frames[i].s.ink, c = frames[i + 1].s.ink;
    if (b < a - 3 && c >= a - 1 && frames[i + 1].t - frames[i].t < 200) inkDips.push({ t: frames[i].t, a, b, c, ms: Math.round(frames[i + 1].t - frames[i].t) });
  }
  const sseScreens = L.sse.filter((s) => s.ev === 'screen');
  const sseDips = [];
  for (let i = 1; i + 1 < sseScreens.length; i++) {
    const a = sseScreens[i - 1], b = sseScreens[i], c = sseScreens[i + 1];
    if ((b.ink < a.ink - 3 && c.ink >= a.ink - 1) || (a.cur && !b.cur && c.cur)) sseDips.push({ t: b.t, ink: [a.ink, b.ink, c.ink], cur: [a.cur, b.cur, c.cur] });
  }
  const summary = {
    name: NAME, info: result.info, focus: result.focusAfterClick,
    keys: result.keys.length, raf: L.raf, domStates: frames.length, shots: shots.length,
    sse: L.sse.reduce((m, s) => { m[s.ev] = (m[s.ev] || 0) + 1; return m; }, {}),
    net: L.net.reduce((m, s) => { const k = `${s.method} ${s.url.replace(/\/t\d+/, '/T').replace(/\?.*/, '')} ${s.status}`; m[k] = (m[k] || 0) + 1; return m; }, {}),
    flipCounts: Object.fromEntries(Object.entries(flips).map(([k, v]) => [k, v.length])),
    transients, inkDips, sseDips,
    boxFromCursor: frames.length ? [frames[0].s.off, frames[frames.length - 1].s.off] : [],
    scrolled: (flips.anc || []).length,
    layoutShifts: L.shifts,
    pixFrames: pix.filter((p) => p.n > 0).length,
    pixBig: big,
    flashes,
    marks: L.marks,
  };
  result.summary = summary;
  delete result.log.sig;
  fs.writeFileSync(path.join(OUT, `${NAME}.json`), JSON.stringify(result));
  console.log(JSON.stringify(summary, null, 1));
}

main().catch((e) => { console.error('FAILED', e.stack || e); process.exit(1); });
