'use strict';

// ask_answer_orders.test.js with the page's own turn and a turn declared
// lost as a step, which that walker leaves out for the second of real time
// each costs. Here the page's 1000 ms timers (the lost-turn grace) run at
// once. Only orders that declare a turn lost are walked.
//
// ASK_LOST_DEPTH (default 4), ASK_LOST_SHARDS (default 8); ASK_LOST_Y=1
// adds answers to the newer question from here.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load, defaultRoutes } = require('./harness');

const X = 'd1-i1';
const Y = 'd2-i1';
const REPLY = '  (reply in the box below, in your own words or with a number)';
const LEFT = '  (left unanswered)';

const ask = (seq, id, stale) => ({
  seq,
  type: 'input.request',
  data: { id, question: `${id}?`, options: ['a', 'b'], ...(stale ? { stale: true } : {}) },
});
const staleRes = (id) => ({ type: 'input.resolved', data: { id, cancelled: true, stale: true } });

function lineOf(app, id) {
  return Array.from(app.el('transcript').querySelectorAll('.msg-ask'))
    .filter((el) => el.textContent.startsWith(`[the model is asking] ${id}?`)).pop();
}

const yUnwritten = (m) => !!(m.y && m.y.took && !m.y.logged);

function initialModel() {
  return {
    x: { waiting: true, logged: null, taken: false },
    y: null,
    restarted: false,
    posts: [],
    answers: 0,
    wholeOffered: true,
    waiting: true,
    lost: false,
    told: { stale: false, answered: false, cancelled: false, refused: false },
  };
}

function replay(app, m, whole) {
  if (whole) app.sse.emit({ seq: 1, type: 'message.user', data: { text: 'first' } });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  const xStale = !m.x.waiting && !m.x.logged;
  app.sse.emit(ask(401, X, xStale));
  if (xStale) m.told.stale = true;
  if (m.x.logged === 'answer') app.sse.emit({ seq: 403, type: 'input.resolved', data: { id: X, answer: 'b' } });
  if (m.x.logged === 'cancel') {
    app.sse.emit({ seq: 403, type: 'input.resolved', data: { id: X, cancelled: true } });
    app.sse.emit({ seq: 404, type: 'turn.cancelled', data: {} });
  }
  if (m.y) {
    const yStale = !m.y.waiting && !m.y.logged;
    app.sse.emit({ seq: 499, type: 'message.user', data: { text: 'next' } });
    app.sse.emit(ask(500, Y, yStale));
    if (yStale) m.y.toldStale = true;
    if (m.y.logged) app.sse.emit({ seq: 501, type: 'input.resolved', data: { id: Y, answer: 'a' } });
  }
}

const settleStep = (which, value, after) => ({
  async run(c) {
    c.settle(which, value);
    await c.app.settle(12);
    const p = which === 'first' ? c.m.posts.shift() : c.m.posts.pop();
    after(c.m, p);
  },
});

async function settleLong(app) {
  await app.settle(40);
  await app.wait(2);
  await app.settle(40);
  await app.wait(2);
  await app.settle(40);
}

const steps = {
  answer: {
    pre: (m) => m.posts.length < 2 && m.answers < 3,
    async run(c) {
      if (c.app.internals.session.pendingAsk !== X) return 'skip';
      c.app.el('input').value = '1';
      c.app.el('send').click();
      await c.app.settle(12);
      c.m.posts.push({ beforeSwitch: false });
      c.m.answers++;
      return 'ok';
    },
  },
  ok: { pre: (m) => m.posts.length > 0 && m.x.waiting, ...settleStep('last', { status: 200, body: { status: 'resolved' } }, (m) => { m.x.waiting = false; m.x.taken = true; m.told.answered = true; }) },
  okFirst: { pre: (m) => m.posts.length > 1 && m.x.waiting, ...settleStep('first', { status: 200, body: { status: 'resolved' } }, (m) => { m.x.waiting = false; m.x.taken = true; m.told.answered = true; }) },
  r404: { pre: (m) => m.posts.length > 0 && !m.x.waiting, ...settleStep('last', { status: 404, body: { error: `question ${X} is not pending` } }, (m, p) => { if (!p.beforeSwitch) m.told.refused = true; }) },
  r404First: { pre: (m) => m.posts.length > 1 && !m.x.waiting, ...settleStep('first', { status: 404, body: { error: `question ${X} is not pending` } }, (m, p) => { if (!p.beforeSwitch) m.told.refused = true; }) },
  net: { pre: (m) => m.posts.length > 0, ...settleStep('last', { networkError: 'connection reset' }, () => {}) },
  netFirst: { pre: (m) => m.posts.length > 1, ...settleStep('first', { networkError: 'connection reset' }, () => {}) },
  written: {
    pre: (m) => m.x.taken && !m.x.logged && !m.restarted,
    async run(c) {
      c.app.sse.emit({ seq: 402, type: 'input.resolved', data: { id: X, answer: '1' } });
      await c.app.settle(12);
      c.m.x.logged = 'answer';
      c.m.x.taken = false;
    },
  },
  restart: {
    pre: (m) => !m.restarted && !(m.x.taken && !m.x.logged) && !yUnwritten(m),
    async run(c) {
      const { m, app } = c;
      m.x.waiting = false;
      m.restarted = true;
      app.sse.fail();
      await app.settle(12);
      app.sse.reopen();
      await settleLong(app);
      if (!m.x.logged) { app.sse.emit(staleRes(X)); m.told.stale = true; }
      if (m.y && m.y.waiting) { m.y.waiting = false; app.sse.emit(staleRes(Y)); m.y.toldStale = true; }
      await app.settle(12);
    },
  },
  quietRestart: {
    pre: (m) => !m.restarted && !(m.x.taken && !m.x.logged) && !yUnwritten(m),
    async run(c) {
      const { m, app } = c;
      m.x.waiting = false;
      m.restarted = true;
      m.quiet = true;
      if (m.y) m.y.waiting = false;
      app.sse.fail();
      await app.settle(12);
      app.sse.reopen();
      await settleLong(app);
    },
  },
  resume: {
    pre: (m) => m.restarted && !yUnwritten(m),
    async run(c) {
      const { m, app } = c;
      app.sse.fail();
      await app.settle(12);
      app.sse.reopen();
      await settleLong(app);
      if (!m.x.waiting && !m.x.logged) { app.sse.emit(staleRes(X)); m.told.stale = true; }
      if (m.y && !m.y.waiting && !m.y.logged) { app.sse.emit(staleRes(Y)); m.y.toldStale = true; }
      await app.settle(12);
    },
  },
  // The page's own turn, gone with the daemon that ran it, declared lost on
  // a reconnect to the successor, which reports the session idle.
  lost: {
    pre: (m) => m.restarted && m.waiting && !m.lost && !m.y && !(m.x.taken && !m.x.logged),
    async run(c) {
      const { m, app } = c;
      c.idle.value = true;
      app.sse.fail();
      await app.settle(12);
      app.sse.reopen();
      await settleLong(app);
      if (!/did not finish/.test(app.el('transcript').textContent)) return 'skip';
      m.lost = true;
      m.waiting = false;
      if (!m.x.logged) m.told.stale = true;
      return 'ok';
    },
  },
  elsewhere: {
    pre: (m) => m.x.waiting && !m.restarted,
    async run(c) {
      c.app.sse.emit({ seq: 403, type: 'input.resolved', data: { id: X, answer: 'b' } });
      await c.app.settle(12);
      c.m.x.waiting = false;
      c.m.x.logged = 'answer';
      c.m.told.answered = true;
    },
  },
  cancel: {
    pre: (m) => m.x.waiting && !m.restarted,
    async run(c) {
      c.app.sse.emit({ seq: 403, type: 'input.resolved', data: { id: X, cancelled: true } });
      c.app.sse.emit({ seq: 404, type: 'turn.cancelled', data: {} });
      await c.app.settle(12);
      c.m.x.waiting = false;
      c.m.x.logged = 'cancel';
      c.m.told.cancelled = true;
      c.m.waiting = false;
    },
  },
  whole: {
    pre: (m) => m.wholeOffered && !(m.x.taken && !m.x.logged) && !yUnwritten(m),
    async run(c) {
      const btn = c.app.el('transcript').querySelector('.earlier-btn');
      if (!btn) return 'skip';
      btn.click();
      await c.app.settle(12);
      replay(c.app, c.m, true);
      await settleLong(c.app);
      c.m.wholeOffered = false;
      return 'ok';
    },
  },
  newer: {
    pre: (m) => !m.y && (m.x.logged || m.restarted),
    async run(c) {
      c.app.sse.emit({ seq: 499, type: 'message.user', data: { text: 'next' } });
      c.app.sse.emit(ask(500, Y, false));
      await c.app.settle(12);
      c.m.y = { waiting: true, logged: null, toldStale: false, posts: [], answered: false, took: false };
    },
  },
  answerY: {
    pre: (m) => !!process.env.ASK_LOST_Y && m.y && (m.y.posts || []).length === 0 && !m.y.answered,
    async run(c) {
      if (c.app.internals.session.pendingAsk !== Y) return 'skip';
      c.app.el('input').value = '2';
      c.app.el('send').click();
      await c.app.settle(12);
      c.m.y.posts.push({ beforeSwitch: false });
      c.m.y.answered = true;
      return 'ok';
    },
  },
  okY: {
    pre: (m) => m.y && (m.y.posts || []).length > 0 && m.y.waiting,
    async run(c) {
      c.settleY({ status: 200, body: { status: 'resolved' } });
      await c.app.settle(12);
      c.m.y.posts.pop();
      c.m.y.waiting = false;
      c.m.y.took = true;
    },
  },
  netY: {
    pre: (m) => m.y && (m.y.posts || []).length > 0,
    async run(c) {
      c.settleY({ networkError: 'connection reset' });
      await c.app.settle(12);
      c.m.y.posts.pop();
    },
  },
  yElsewhere: {
    pre: (m) => m.y && m.y.waiting,
    async run(c) {
      c.app.sse.emit({ seq: 501, type: 'input.resolved', data: { id: Y, answer: 'a' } });
      await c.app.settle(12);
      c.m.y.waiting = false;
      c.m.y.logged = 'answer';
    },
  },
};

function check(app, m, last) {
  const out = [];
  const pa = app.internals.session.pendingAsk;
  const lx = lineOf(app, X);
  const ly = m.y ? lineOf(app, Y) : null;
  const tx = lx ? lx.textContent : '';
  const onWay = m.posts.length > 0;
  if (pa) {
    const l = lineOf(app, pa);
    if (!l || !l.textContent.endsWith(REPLY)) out.push(`box-line: box armed for ${pa}, whose line reads ${JSON.stringify(l && l.textContent.split('\n').pop())}`);
  }
  const onlyRefused = m.told.refused && !m.told.stale && !m.told.answered && !m.told.cancelled;
  if (pa === X && !(m.x.taken && !m.x.logged) && !(onlyRefused && m.quiet)
      && (m.told.stale || m.told.answered || m.told.cancelled || m.told.refused)) {
    out.push(`box-told: box armed for ${X} after ${Object.keys(m.told).filter((k) => m.told[k]).join('+')}`);
  }
  if (pa === Y && m.y && (m.y.toldStale || m.y.logged || m.y.took)) out.push(`box-told: box armed for ${Y} after it was put away or taken`);
  if (ly && ly.textContent.endsWith(LEFT) && m.y && (m.y.logged || m.y.took)) out.push(`left-answered: ${Y} says left unanswered though answered`);
  if (tx.endsWith(LEFT) && (m.told.answered || m.told.cancelled)) {
    out.push(`left-answered: ${X} says left unanswered though ${m.told.answered ? 'answered' : 'cancelled'}`);
  }
  if (!onWay && m.told.stale && !m.told.answered && !m.told.cancelled) {
    if (!tx.endsWith(LEFT)) out.push(`settled: ${X} told stale, nothing on its way, line reads ${JSON.stringify(tx.split('\n').pop())}`);
    if (pa === X) out.push(`settled: ${X} told stale, nothing on its way, box armed for it`);
  }
  if (ly && m.y.toldStale && !(m.y.posts || []).length && !m.y.logged && !m.y.took && !ly.textContent.endsWith(LEFT)) {
    out.push(`settled: ${Y} told stale, line reads ${JSON.stringify(ly.textContent.split('\n').pop())}`);
  }
  if (last !== 'answer' && app.internals.session.connected && !app.internals.session.pendingPermissionID) {
    const lit = app.el('comm-dot').classList.contains('asking');
    if (lit !== !!pa) out.push(`light: asking=${lit} with the box ${pa ? `armed for ${pa}` : 'disarmed'}`);
  }
  return out;
}

async function runSequence(seq) {
  const open = [];
  open.y = [];
  const timers = [];
  const globals = {
    // The lost-turn grace (and the reconnect backoff, never reached: the
    // stream is never closed fatally) run at once.
    setTimeout: (fn, ms, ...a) => { const t = setTimeout(fn, ms === 1000 ? 0 : ms, ...a); timers.push(t); return t; },
    setInterval: (...a) => { const t = setInterval(...a); if (t.unref) t.unref(); timers.push(t); return t; },
  };
  try {
    return await runIn(seq, open, globals);
  } finally {
    for (const t of timers) { clearTimeout(t); clearInterval(t); }
    for (const p of [...open, ...open.y]) p({ networkError: 'done' });
  }
}

async function runIn(seq, open, globals) {
  const idle = { value: true };
  const app = await load({
    globals,
    routes: {
      ...defaultRoutes(),
      'GET /api/sessions': () => [
        { id: 'sess-1', title: 'one', agent: 'general-purpose', workspace: '/tmp/workspace', busy: !idle.value },
        { id: 'sess-2', title: 'two', agent: 'general-purpose', workspace: '/tmp/workspace' },
      ],
      'POST /api/sessions/sess-1/input/*': (_b, { path }) => new Promise((resolve) => {
        if (path.endsWith(Y)) open.y.push(resolve);
        else open.push(resolve);
      }),
      'POST /api/sessions/*/messages': { status: 202, body: { status: 'accepted' } },
    },
  });
  app.el('input').value = 'pick one';
  app.el('send').click();
  await app.settle(12);
  if (!app.internals.session.waiting) throw new Error('precondition: the page is not waiting on its own turn');
  idle.value = false;
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, X, false));
  await app.settle(12);
  const m = initialModel();
  const c = {
    app,
    m,
    idle,
    settle: (which, v) => (which === 'first' ? open.shift() : open.pop())(v),
    settleY: (v) => open.y.pop()(v),
  };
  const problems = [];
  const emitting = new Set(['written', 'restart', 'quietRestart', 'resume', 'lost', 'elsewhere', 'cancel', 'whole', 'newer', 'yElsewhere']);
  for (const [at, name] of seq.entries()) {
    const r = await steps[name].run(c);
    if (r === 'skip') return { skipped: name, problems };
    if (name === 'answer' || name === 'answerY') m.lightLag = true;
    else if (emitting.has(name)) m.lightLag = false;
    else if (['net', 'netFirst'].includes(name) && app.internals.session.pendingAsk === X) m.lightLag = false;
    else if (name === 'netY' && app.internals.session.pendingAsk === Y) m.lightLag = false;
    for (const p of check(app, m, m.lightLag ? 'answer' : name)) problems.push({ at, problem: p });
    if (problems.length) break;
  }
  return { problems };
}

function simulate(name, m) {
  switch (name) {
    case 'answer': m.posts.push({ beforeSwitch: false }); m.answers++; break;
    case 'ok': m.posts.pop(); m.x.waiting = false; m.x.taken = true; break;
    case 'okFirst': m.posts.shift(); m.x.waiting = false; m.x.taken = true; break;
    case 'r404': case 'net': m.posts.pop(); break;
    case 'r404First': case 'netFirst': m.posts.shift(); break;
    case 'written': m.x.logged = 'answer'; m.x.taken = false; break;
    case 'restart':
      m.x.waiting = false; m.restarted = true;
      if (m.y) m.y.waiting = false;
      break;
    case 'quietRestart':
      m.x.waiting = false; m.restarted = true; m.quiet = true;
      if (m.y) m.y.waiting = false;
      break;
    case 'resume': break;
    case 'lost': m.lost = true; m.waiting = false; break;
    case 'elsewhere': m.x.waiting = false; m.x.logged = 'answer'; break;
    case 'cancel': m.x.waiting = false; m.x.logged = 'cancel'; m.waiting = false; break;
    case 'whole': m.wholeOffered = false; break;
    case 'newer': m.y = { waiting: true, logged: null, posts: [], answered: false, took: false }; break;
    case 'answerY': m.y.posts.push({}); m.y.answered = true; break;
    case 'okY': m.y.posts.pop(); m.y.waiting = false; m.y.took = true; break;
    case 'netY': m.y.posts.pop(); break;
    case 'yElsewhere': m.y.waiting = false; m.y.logged = 'answer'; break;
  }
}

function* sequences(depth) {
  function* walk(m, prefix) {
    let extended = false;
    if (prefix.length < depth) {
      for (const [name, s] of Object.entries(steps)) {
        if (!s.pre(m)) continue;
        if (name === 'resume' && prefix[prefix.length - 1] === 'resume') continue;
        const next = structuredClone(m);
        simulate(name, next);
        extended = true;
        yield* walk(next, [...prefix, name]);
      }
    }
    if (!extended && prefix.length && prefix.includes('lost')) yield prefix;
  }
  yield* walk(initialModel(), []);
}

const DEPTH = Number(process.env.ASK_LOST_DEPTH || 4);
const SHARDS = Number(process.env.ASK_LOST_SHARDS || 8);

async function runShard(shard, shards) {
  const rows = new Map();
  let ran = 0;
  let skipped = 0;
  const whySkipped = {};
  let i = 0;
  for (const seq of sequences(DEPTH)) {
    if (i++ % shards !== shard) continue;
    const r = await runSequence(seq);
    if (r.skipped) { skipped++; whySkipped[r.skipped] = (whySkipped[r.skipped] || 0) + 1; }
    else ran++;
    for (const { at, problem } of r.problems) {
      const prefix = seq.slice(0, at + 1).join(' > ');
      rows.set(`${problem}|${prefix}`, { problem, seq: prefix });
    }
  }
  return { ran, skipped, whySkipped, rows: [...rows.values()] };
}

if (process.env.ASK_LOST_SHARD) {
  test(`shard ${process.env.ASK_LOST_SHARD} of the lost-turn walk`, { timeout: 1800000 }, async () => {
    const [shard, shards] = process.env.ASK_LOST_SHARD.split('/').map(Number);
    console.log(`ASK_LOST_RESULT ${JSON.stringify(await runShard(shard, shards))}`);
  });
} else if (process.env.ASK_LOST_COUNT) {
  test('count', () => {
    let n = 0;
    for (const _ of sequences(DEPTH)) n++;
    console.log(`lost-turn orders to depth ${DEPTH}: ${n}`);
  });
} else {
  test(`every order with a turn declared lost, to depth ${DEPTH}, leaves box, line and light agreeing`, { timeout: 3600000 }, () => {
    const { spawnSync } = require('node:child_process');
    let ran = 0;
    let skipped = 0;
    const byKind = new Map();
    for (let i = 0; i < SHARDS; i++) {
      const r = spawnSync(process.execPath, ['--experimental-vm-modules', '--max-old-space-size=1500', __filename], {
        env: { ...process.env, ASK_LOST_SHARD: `${i}/${SHARDS}`, ASK_LOST_DEPTH: String(DEPTH) },
        encoding: 'utf8',
        maxBuffer: 64 << 20,
      });
      const line = (r.stdout || '').split('\n').find((l) => l.includes('ASK_LOST_RESULT '));
      assert.ok(line, `shard ${i} reported nothing:\n${(r.stdout || '').slice(-2000)}${(r.stderr || '').slice(-2000)}`);
      const d = JSON.parse(line.slice(line.indexOf('ASK_LOST_RESULT ') + 'ASK_LOST_RESULT '.length));
      ran += d.ran;
      skipped += d.skipped;
      for (const { problem, seq } of d.rows) {
        const kind = problem.replace(/ reads .*$/, '').replace(/asking=\w+ /, '');
        if (!byKind.has(kind)) byKind.set(kind, []);
        if (!byKind.get(kind).some((x) => x.seq === seq)) byKind.get(kind).push({ problem, seq });
      }
    }
    const report = [];
    for (const [, list] of byKind) {
      list.sort((a, b) => a.seq.split(' > ').length - b.seq.split(' > ').length || a.seq.localeCompare(b.seq));
      report.push(`${list.length} orders: ${list[0].problem}\n${list.slice(0, 6).map((x) => `    ${x.seq}`).join('\n')}`);
    }
    console.log(`ran ${ran} lost-turn orders to depth ${DEPTH}, skipped ${skipped}`);
    assert.ok(ran > 0, 'no order declared a turn lost');
    assert.equal(report.length, 0, `\n${report.join('\n')}`);
  });
}
