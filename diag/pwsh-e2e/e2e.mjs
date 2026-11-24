// The PowerShell panel in a real page: a Chrome of its own (a temporary
// profile, never a person's) opens the Web UI of a daemon that has the
// PowerShell setting on, shows the terminal the model opened, types into
// it with real key events, opens a second terminal with +, and takes
// screenshots along the way. It writes <out>/page.json with what the
// panel showed.
//
//   node e2e.mjs --chrome <path> --url http://127.0.0.1:4196/ --out <dir>

import { spawn } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const argv = process.argv.slice(2);
const opt = {};
for (let i = 0; i < argv.length; i++) if (argv[i].startsWith('--')) opt[argv[i].slice(2)] = argv[i + 1];
const OUT = opt.out || '.';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

class CDP {
  constructor(ws) {
    this.ws = ws; this.n = 0; this.pending = new Map();
    ws.onmessage = (ev) => {
      const m = JSON.parse(ev.data);
      if (m.id && this.pending.has(m.id)) {
        const p = this.pending.get(m.id); this.pending.delete(m.id);
        if (m.error) p.rej(new Error(`${p.method}: ${m.error.message}`)); else p.res(m.result);
      }
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

const VK = { ' ': 32, '-': 189, '=': 187, ',': 188, '.': 190, '/': 191, ';': 186, "'": 222, '[': 219, ']': 221, '\\': 220, '`': 192 };
function keyOf(c) {
  if (/[a-z]/i.test(c)) return { code: 'Key' + c.toUpperCase(), vk: c.toUpperCase().charCodeAt(0) };
  if (/[0-9]/.test(c)) return { code: 'Digit' + c, vk: c.charCodeAt(0) };
  if (c === ' ') return { code: 'Space', vk: 32 };
  return { code: '', vk: VK[c] || 0 };
}

async function main() {
  fs.mkdirSync(OUT, { recursive: true });
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'lc-pwsh-e2e-'));
  const chrome = spawn(opt.chrome, [
    '--headless=new', `--user-data-dir=${dataDir}`, '--remote-debugging-port=0', '--no-first-run',
    '--no-default-browser-check', '--disable-search-engine-choice-screen', '--window-size=1500,950', 'about:blank',
  ], { stdio: 'ignore' });
  let chromeExit = null;
  chrome.on('exit', (code, sig) => { chromeExit = `${code}/${sig}`; });
  const portFile = path.join(dataDir, 'DevToolsActivePort');
  let port = '', wsPath = '';
  for (let i = 0; i < 300 && !port; i++) {
    if (chromeExit) throw new Error(`chrome exited at once: ${chromeExit}`);
    try { [port, wsPath] = fs.readFileSync(portFile, 'utf8').trim().split('\n'); } catch { await sleep(100); }
  }
  if (!port) throw new Error('chrome wrote no DevToolsActivePort');
  const ws = new WebSocket(`ws://127.0.0.1:${port}${wsPath}`);
  await new Promise((res, rej) => { ws.onopen = res; ws.onerror = () => rej(new Error('websocket failed')); });
  const cdp = new CDP(ws);
  const page = { steps: [] };
  try {
    const { targetId } = await cdp.send('Target.createTarget', { url: 'about:blank' });
    const { sessionId } = await cdp.send('Target.attachToTarget', { targetId, flatten: true });
    const send = (m, p) => cdp.send(m, p, sessionId);
    const ev = async (expression) => {
      const r = await send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true });
      if (r.exceptionDetails) throw new Error(`evaluate: ${r.exceptionDetails.exception?.description || r.exceptionDetails.text}`);
      return r.result.value;
    };
    const shot = async (name) => {
      const { data } = await send('Page.captureScreenshot', { format: 'png' });
      fs.writeFileSync(path.join(OUT, `${name}.png`), Buffer.from(data, 'base64'));
    };
    const screen = () => ev(`(() => { const s = document.getElementById('ps-screen'); const c = document.getElementById('ps-cursor'); return s ? [...s.children].filter((x) => x !== c).map((r) => r.textContent.replace(/\\s+$/, '')).join('\\n') : null; })()`);
    const status = () => ev(`(document.getElementById('ps-status') || {}).textContent || ''`);
    const settle = async () => {
      let last = '', same = 0;
      for (let i = 0; i < 120 && same < 6; i++) {
        await sleep(250);
        const cur = (await screen()) + '|' + (await status());
        same = cur === last ? same + 1 : 0; last = cur;
      }
    };
    const click = async (id) => {
      const r = await ev(`(() => { const e = document.getElementById(${JSON.stringify(id)}); if (!e) return null; const b = e.getBoundingClientRect(); return { x: b.left + b.width / 2, y: b.top + b.height / 2 }; })()`);
      if (!r) throw new Error(`no #${id}`);
      await send('Input.dispatchMouseEvent', { type: 'mousePressed', x: r.x, y: r.y, button: 'left', clickCount: 1 });
      await send('Input.dispatchMouseEvent', { type: 'mouseReleased', x: r.x, y: r.y, button: 'left', clickCount: 1 });
    };
    const type = async (text) => {
      for (const c of text) {
        const k = keyOf(c);
        await send('Input.dispatchKeyEvent', { type: 'keyDown', key: c, code: k.code, windowsVirtualKeyCode: k.vk, text: c, unmodifiedText: c });
        await send('Input.dispatchKeyEvent', { type: 'keyUp', key: c, code: k.code, windowsVirtualKeyCode: k.vk });
        await sleep(40);
      }
    };
    const enter = async () => {
      await send('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, text: '\r', unmodifiedText: '\r' });
      await send('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
    };

    await send('Page.enable');
    await send('Runtime.enable');
    await send('Page.navigate', { url: opt.url });
    // The page opens the one conversation there is.
    for (let i = 0; i < 100; i++) {
      await sleep(200);
      if (await ev(`!!document.getElementById('ps-panel') && !document.getElementById('ps-panel').hidden && !!document.getElementById('ps-screen') && document.getElementById('ps-screen').children.length > 1`)) break;
    }
    page.panelShown = await ev(`!!document.getElementById('ps-panel') && !document.getElementById('ps-panel').hidden`);
    page.toggleShown = await ev(`!!document.getElementById('toggle-ps') && !document.getElementById('toggle-ps').hidden`);
    await settle();
    page.steps.push({ name: 'model-terminal', status: await status(), screen: await screen() });
    await shot('1-model-terminal');

    // The person types in the same terminal.
    await click('ps-lines');
    await sleep(300);
    await type("Write-Output ('typed-by-person-' + (6*7))");
    await enter();
    await settle();
    page.steps.push({ name: 'person-typed', status: await status(), screen: await screen() });
    await shot('2-person-typed');

    // + starts a second terminal.
    await click('ps-tab-new');
    for (let i = 0; i < 80; i++) {
      await sleep(250);
      const tabs = await ev(`document.getElementById('ps-tab-list').children.length`);
      if (tabs >= 2) break;
    }
    await settle();
    page.tabs = await ev(`[...document.getElementById('ps-tab-list').children].map((t) => t.textContent.trim())`);
    await click('ps-lines');
    await sleep(300);
    await type('$PSVersionTable.PSEdition');
    await enter();
    await settle();
    page.steps.push({ name: 'second-terminal', status: await status(), screen: await screen() });
    await shot('3-second-terminal');

    // The settings window's Tools tab, with the PowerShell switch.
    await click('settings-btn');
    await sleep(800);
    page.settings = await ev(`(() => { const r = document.getElementById('powershell-settings'); const c = document.getElementById('powershell-checkbox'); return { rowShown: !!r && !r.hidden, checked: !!c && c.checked, live: (document.getElementById('powershell-live') || {}).textContent || '' }; })()`);
    await shot('4-settings-tools');
  } catch (e) {
    page.error = String(e && e.stack || e);
  } finally {
    fs.writeFileSync(path.join(OUT, 'page.json'), JSON.stringify(page, null, 2));
    try { chrome.kill(); } catch {}
  }
  if (page.error) {
    console.error(page.error);
    process.exit(1);
  }
}

main().catch((e) => { console.error(e); process.exit(1); });
