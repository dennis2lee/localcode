'use strict';

// Every order of what can happen to a model question and an answer typed
// for it, walked against a model of the daemon. Besides one question and
// one answer: two answers to it on their way at once (the second typed
// after the whole conversation armed the box again), a newer question,
// a turn that asks nothing run by another client, an earlier answered
// question in the whole-conversation replay,
// later resumes that bring stale resolutions, the newer question resolved
// from another window, and with ASK_ORDERS_Y=1 answers to the newer
// question from here too.
//
// It does not walk a turn declared lost, which costs a second of real
// time per order; ask_answer_lost_turn, _lost_rearmed and _record hold
// those. Depth 4 by default; ASK_ORDERS_DEPTH=5 with ASK_ORDERS_SHARDS=32
// walks 5063 orders, and 5681 with ASK_ORDERS_Y=1.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

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
    told: { stale: false, answered: false, cancelled: false, refused: false },
  };
}

// An earlier question, answered, whose turn ended: the whole-conversation
// reload replays it, unmarked, and its turn's end, before X is drawn again.
const A = 'd0-i1';

function replay(app, m, whole) {
  if (whole) {
    app.sse.emit({ seq: 1, type: 'message.user', data: { text: 'first' } });
    app.sse.emit(ask(2, A, false));
    app.sse.emit({ seq: 3, type: 'input.resolved', data: { id: A, answer: 'a' } });
    app.sse.emit({ seq: 4, type: 'turn.done', data: {} });
  }
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
  r502: { pre: (m) => m.posts.length > 0, ...settleStep('last', { status: 502, body: { error: 'bad gateway' } }, () => {}) },
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
      await app.settle(12);
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
      await app.settle(12);
    },
  },
  // A later blip under the successor: its resume puts away every question
  // nobody waits on and nothing in the log answers.
  resume: {
    pre: (m) => m.restarted && !yUnwritten(m),
    async run(c) {
      const { m, app } = c;
      app.sse.fail();
      await app.settle(12);
      app.sse.reopen();
      await app.settle(12);
      if (!m.x.waiting && !m.x.logged) { app.sse.emit(staleRes(X)); m.told.stale = true; }
      if (m.y && !m.y.waiting && !m.y.logged) { app.sse.emit(staleRes(Y)); m.y.toldStale = true; }
      await app.settle(12);
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
      await c.app.settle(12);
      c.m.wholeOffered = false;
    },
  },
  switch: {
    pre: (m) => !(m.x.taken && !m.x.logged) && !yUnwritten(m),
    async run(c) {
      c.app.internals.selectSession('sess-2', 'general-purpose', '/tmp/workspace');
      await c.app.settle(12);
      c.app.internals.selectSession('sess-1', 'general-purpose', '/tmp/workspace');
      await c.app.settle(12);
      c.m.told = { stale: false, answered: c.m.told.answered, cancelled: false, refused: false };
      for (const p of c.m.posts) p.beforeSwitch = true;
      if (c.m.y) c.m.y.toldStale = false;
      replay(c.app, c.m, false);
      await c.app.settle(12);
      c.m.wholeOffered = true;
    },
  },
  // ASK_ORDERS_NO_DISPLACE=1 leaves out a newer question after a quiet
  // restart with X unresolved, the root of the one defect this walk
  // finds, to see what it masks.
  newer: {
    pre: (m) => !m.y && (m.x.logged || (m.restarted && !(process.env.ASK_ORDERS_NO_DISPLACE && m.quiet))),
    async run(c) {
      c.app.sse.emit({ seq: 499, type: 'message.user', data: { text: 'next' } });
      c.app.sse.emit(ask(500, Y, false));
      await c.app.settle(12);
      c.m.y = { waiting: true, logged: null, toldStale: false, posts: [], answered: false, took: false };
      // One question at a time: a newer one says nobody waits on X.
      if (!c.m.x.logged) c.m.told.stale = true;
    },
  },
  // Another client runs a turn on the daemon that came up, and it asks
  // nothing. A turn ends only after the question it asked is resolved, so
  // its end says nobody waits on X.
  turnRan: {
    pre: (m) => m.restarted && !m.y && !m.turnRan,
    async run(c) {
      c.app.sse.emit({ seq: 450, type: 'message.user', data: { text: 'other' } });
      c.app.sse.emit({ seq: 451, type: 'turn.done', data: {} });
      await c.app.settle(12);
      c.m.turnRan = true;
      if (!c.m.x.logged) c.m.told.stale = true;
    },
  },
  // ASK_ORDERS_Y=1: Y answered from here too, so an answer to each question
  // can be on its way at once.
  answerY: {
    pre: (m) => !!process.env.ASK_ORDERS_Y && m.y && (m.y.posts || []).length === 0 && !m.y.answered,
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
  r404Y: {
    pre: (m) => m.y && (m.y.posts || []).length > 0 && !m.y.waiting,
    async run(c) {
      c.settleY({ status: 404, body: { error: `question ${Y} is not pending` } });
      await c.app.settle(12);
      c.m.y.posts.pop();
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
  writtenY: {
    pre: (m) => m.y && m.y.took && !m.y.logged,
    async run(c) {
      c.app.sse.emit({ seq: 501, type: 'input.resolved', data: { id: Y, answer: '2' } });
      await c.app.settle(12);
      c.m.y.logged = 'answer';
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
    setTimeout: (...a) => { const t = setTimeout(...a); timers.push(t); return t; },
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
  const app = await load({
    globals,
    routes: {
      'GET /api/sessions': [
        { id: 'sess-1', title: 'one', agent: 'general-purpose', workspace: '/tmp/workspace' },
        { id: 'sess-2', title: 'two', agent: 'general-purpose', workspace: '/tmp/workspace' },
      ],
      'POST /api/sessions/sess-1/input/*': (_b, { path }) => new Promise((resolve) => {
        if (path.endsWith(Y)) open.y.push(resolve);
        else open.push(resolve);
      }),
      'POST /api/sessions/*/messages': { status: 202, body: { status: 'accepted' } },
    },
  });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, X, false));
  await app.settle(12);
  const m = initialModel();
  const c = {
    app,
    m,
    settle: (which, v) => (which === 'first' ? open.shift() : open.pop())(v),
    settleY: (v) => open.y.pop()(v),
  };
  const problems = [];
  const emitting = new Set(['written', 'restart', 'quietRestart', 'resume', 'elsewhere', 'cancel', 'whole', 'switch', 'newer', 'yElsewhere', 'writtenY']);
  for (const [at, name] of seq.entries()) {
    const r = await steps[name].run(c);
    if (r === 'skip') return { skipped: true, problems };
    if (name === 'answer' || name === 'answerY') m.lightLag = true;
    else if (emitting.has(name)) m.lightLag = false;
    else if (['net', 'netFirst', 'r502'].includes(name) && app.internals.session.pendingAsk === X) m.lightLag = false;
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
    case 'r404': case 'net': case 'r502': m.posts.pop(); break;
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
    case 'elsewhere': m.x.waiting = false; m.x.logged = 'answer'; break;
    case 'cancel': m.x.waiting = false; m.x.logged = 'cancel'; break;
    case 'whole': m.wholeOffered = false; break;
    case 'switch': m.wholeOffered = true; break;
    case 'newer': m.y = { waiting: true, logged: null, posts: [], answered: false, took: false }; break;
    case 'turnRan': m.turnRan = true; break;
    case 'answerY': m.y.posts.push({}); m.y.answered = true; break;
    case 'okY': m.y.posts.pop(); m.y.waiting = false; m.y.took = true; break;
    case 'r404Y': case 'netY': m.y.posts.pop(); break;
    case 'writtenY': m.y.logged = 'answer'; break;
    case 'yElsewhere': m.y.waiting = false; m.y.logged = 'answer'; break;
  }
}

function* sequences(depth) {
  function* walk(m, prefix) {
    let extended = false;
    if (prefix.length < depth) {
      for (const [name, s] of Object.entries(steps)) {
        if (!s.pre(m)) continue;
        // A second resume in a row adds nothing the first did not.
        if (name === 'resume' && prefix[prefix.length - 1] === 'resume') continue;
        const next = structuredClone(m);
        simulate(name, next);
        extended = true;
        yield* walk(next, [...prefix, name]);
      }
    }
    if (!extended && prefix.length) yield prefix;
  }
  yield* walk(initialModel(), []);
}

const DEPTH = Number(process.env.ASK_ORDERS_DEPTH || 4);
// 16 shards of depth 5 ran a child out of its 1500 MB heap; 32 do not.
const SHARDS = Number(process.env.ASK_ORDERS_SHARDS || 8);

async function runShard(shard, shards) {
  const rows = new Map();
  let ran = 0;
  let skipped = 0;
  let i = 0;
  for (const seq of sequences(DEPTH)) {
    if (i++ % shards !== shard) continue;
    const r = await runSequence(seq);
    if (r.skipped) skipped++;
    else ran++;
    for (const { at, problem } of r.problems) {
      const prefix = seq.slice(0, at + 1).join(' > ');
      rows.set(`${problem}|${prefix}`, { problem, seq: prefix });
    }
  }
  return { ran, skipped, rows: [...rows.values()] };
}

if (process.env.ASK_ORDERS_SHARD) {
  test(`shard ${process.env.ASK_ORDERS_SHARD} of the walk`, { timeout: 1800000 }, async () => {
    const [shard, shards] = process.env.ASK_ORDERS_SHARD.split('/').map(Number);
    console.log(`ASK_ORDERS_RESULT ${JSON.stringify(await runShard(shard, shards))}`);
  });
} else if (process.env.ASK_ORDERS_COUNT) {
  test('count', () => {
    let n = 0;
    for (const _ of sequences(DEPTH)) n++;
    console.log(`orders to depth ${DEPTH}: ${n}`);
  });
} else {
  test(`every order of a model question's answer, to depth ${DEPTH}, leaves box, line and light agreeing`, { timeout: 3600000 }, () => {
    const { spawnSync } = require('node:child_process');
    let ran = 0;
    let skipped = 0;
    const byKind = new Map();
    for (let i = 0; i < SHARDS; i++) {
      const r = spawnSync(process.execPath, ['--experimental-vm-modules', '--max-old-space-size=1500', __filename], {
        env: { ...process.env, ASK_ORDERS_SHARD: `${i}/${SHARDS}`, ASK_ORDERS_DEPTH: String(DEPTH) },
        encoding: 'utf8',
        maxBuffer: 64 << 20,
      });
      const line = (r.stdout || '').split('\n').find((l) => l.includes('ASK_ORDERS_RESULT '));
      assert.ok(line, `shard ${i} reported nothing:\n${(r.stdout || '').slice(-2000)}${(r.stderr || '').slice(-2000)}`);
      const d = JSON.parse(line.slice(line.indexOf('ASK_ORDERS_RESULT ') + 'ASK_ORDERS_RESULT '.length));
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
    if (process.env.ASK_ORDERS_DUMP) {
      require('node:fs').writeFileSync(process.env.ASK_ORDERS_DUMP, JSON.stringify([...byKind.values()].flat(), null, 1));
    }
    console.log(`ran ${ran} orders to depth ${DEPTH}, skipped ${skipped}`);
    assert.equal(report.length, 0, `\n${report.join('\n')}`);
  });
}
