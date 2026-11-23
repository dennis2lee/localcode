// Prints one run as a timeline: keys, the daemon's frames, the panel's
// requests, what each animation frame showed (changed fields only), and
// the pixels that changed.
//   node timeline.mjs out/run.json [from-ms] [to-ms] [--text]
import fs from 'node:fs';
const r = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
const from = Number(process.argv[3] || 0), to = Number(process.argv[4] || 1e12);
const showText = process.argv.includes('--text');
const L = r.log, origin = L.origin;
const ev = [];
for (const k of r.keys) ev.push({ t: k.t - origin, s: `KEY   ${JSON.stringify(k.c)}` });
for (const m of L.marks) ev.push({ t: m.t, s: `MARK  ${m.name}` });
let lastState = null;
for (const s of L.sse) {
  if (s.ev === 'screen') ev.push({ t: s.t, s: `SSE   screen h=${s.h} ink=${s.ink} cur=${s.cur ? 1 : 0}@${s.cx},${s.cy} ${s.cols}x${s.rows}${s.alt ? ' alt' : ''}` });
  else if (s.ev === 'state') {
    let d = '';
    try {
      const cur = JSON.parse(s.state); const a = lastState && lastState.terminals && lastState.terminals[0], b = cur.terminals && cur.terminals[0];
      if (a && b) d = Object.keys(b).filter((k) => JSON.stringify(a[k]) !== JSON.stringify(b[k])).map((k) => `${k}:${JSON.stringify(a[k])}->${JSON.stringify(b[k])}`).join(' ');
      else d = `terminals=${(cur.terminals || []).length}`;
      if (lastState && lastState.wants !== cur.wants) d += ` wants:${lastState.wants}->${cur.wants}`;
      lastState = cur;
    } catch (e) {}
    ev.push({ t: s.t, s: `SSE   state ${d}` });
  } else if (s.ev === 'scroll') ev.push({ t: s.t, s: `SSE   scroll from=${s.from} n=${s.n}` });
  else ev.push({ t: s.t, s: `SSE   ${s.ev} ${s.url || ''}${s.rs !== undefined ? ' rs=' + s.rs : ''}` });
}
for (const n of L.net) if (!/input$/.test(n.url) || process.argv.includes('--input')) ev.push({ t: n.t, s: `NET   ${n.method} ${n.url} ${n.body || ''} -> ${n.status} (${n.dt}ms)` });
let prev = null;
for (const f of L.frames) {
  const ch = prev ? Object.keys(f.s).filter((k) => JSON.stringify(f.s[k]) !== JSON.stringify(prev[k])) : Object.keys(f.s);
  ev.push({ t: f.t, s: `FRAME #${f.n} ${ch.map((k) => `${k}=${JSON.stringify(f.s[k])}`).join(' ')}` + (showText && ch.includes('th') ? `\n${(L.texts['d' + f.s.th] || '').split('\n').map((l, i) => `        ${String(i).padStart(2)}|${l}`).filter((l) => l.trim().length > 12).join('\n')}` : '') });
  prev = f.s;
}
for (const m of L.muts) ev.push({ t: m.t, s: `MUT   ${Object.entries(m.agg).map(([k, v]) => `${k}x${v}`).join(' ')}` });
for (const s of L.shifts) ev.push({ t: s.t, s: `SHIFT v=${s.v.toExponential(2)} ${s.src.map((x) => `${x.node} y:${x.prev[1].toFixed(1)}->${x.cur[1].toFixed(1)} x:${x.prev[0].toFixed(1)}->${x.cur[0].toFixed(1)} h:${x.prev[3].toFixed(1)}->${x.cur[3].toFixed(1)}`).join(' ; ')}` });
for (const p of r.pix || []) if (p.n) ev.push({ t: p.t, s: `PIX   changed=${p.n} box=${JSON.stringify(p.box)} vs2back=${p.back2}` });
ev.sort((a, b) => a.t - b.t);
for (const e of ev) if (e.t >= from && e.t <= to) console.log(String(Math.round(e.t)).padStart(7), e.s);
