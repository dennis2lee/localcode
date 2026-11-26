'use strict';

// Every order of what can happen to a model question and an answer typed
// for it, walked against a model of the daemon.
//
// The model keeps what the daemon knows (is it waiting on the question,
// is a resolution in the log, was an answer taken and not yet written)
// and only offers a step a real daemon could produce from there. The
// whole-conversation replay and a session switch replay the log as the
// daemon would serve it: a question nobody waits on and nothing answers
// is sent stale. Orders that need the accepted race (a stale copy or
// resolution ahead of the real resolution of an answer taken) are left
// out.
//
// After every step, what the page shows is checked against what it has
// been told:
//
// * box-line: the box armed for a question means its line invites a reply.
// * box-told: the box is not armed for a question the page has been told
//   nobody waits on (a stale copy or resolution, a real resolution, a
//   successful answer, a 404).
// * left-answered: a line says "(left unanswered)" only for a question
//   nobody answered.
// * settled: with no answer on its way, a question the page was told
//   (stale) nobody waits on, never answered or cancelled, says
//   "(left unanswered)" and does not hold the box.
// * light: the light says a question waits exactly when the box is armed.
//   Not checked right after a send, which never redraws it (before this
//   change too).
//
// It does not walk a turn declared lost (the page never sends the prompt,
// so the turn is never its own), two answers on their way at once, or a
// replay that holds another question. ask_answer_lost_turn,
// ask_answer_two_answers and ask_answer_other_question hold those.

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

function initialModel() {
  return {
    x: { waiting: true, logged: null, taken: false },
    y: null,
    restarted: false,
    post: false,
    answers: 0,
    wholeOffered: true,
    told: { stale: false, answered: false, cancelled: false, refused: false },
  };
}

// The log as the daemon serves it, from seq 400 (the tail) or from seq 1.
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
    const yStale = !m.y.waiting;
    app.sse.emit(ask(500, Y, yStale));
    if (yStale) m.y.toldStale = true;
  }
}

const steps = {
  answer: {
    pre: (m) => !m.post && m.answers < 2,
    async run(c) {
      if (c.app.internals.session.pendingAsk !== X) return 'skip';
      c.app.el('input').value = '1';
      c.app.el('send').click();
      await c.app.settle(12);
      c.m.post = true;
      c.m.postBeforeSwitch = false;
      c.m.answers++;
      return 'ok';
    },
  },
  ok: {
    pre: (m) => m.post && m.x.waiting,
    async run(c) {
      c.settlePost({ status: 200, body: { status: 'resolved' } });
      await c.app.settle(12);
      c.m.post = false;
      c.m.x.waiting = false;
      c.m.x.taken = true;
      c.m.told.answered = true;
    },
  },
  // The resolution of the answer the daemon took from here.
  written: {
    pre: (m) => m.x.taken && !m.x.logged && !m.restarted,
    async run(c) {
      c.app.sse.emit({ seq: 402, type: 'input.resolved', data: { id: X, answer: '1' } });
      await c.app.settle(12);
      c.m.x.logged = 'answer';
      c.m.x.taken = false;
    },
  },
  r404: {
    pre: (m) => m.post && !m.x.waiting,
    async run(c) {
      c.settlePost({ status: 404, body: { error: `question ${X} is not pending` } });
      await c.app.settle(12);
      c.m.post = false;
      // A 404 for an answer the page sent before a switch is not this
      // page's to know: resetSession dropped it.
      if (!c.m.postBeforeSwitch) c.m.told.refused = true;
    },
  },
  net: {
    pre: (m) => m.post,
    async run(c) {
      c.settlePost({ networkError: 'connection reset' });
      await c.app.settle(12);
      c.m.post = false;
    },
  },
  r502: {
    pre: (m) => m.post,
    async run(c) {
      c.settlePost({ status: 502, body: { error: 'bad gateway' } });
      await c.app.settle(12);
      c.m.post = false;
    },
  },
  // The daemon stops; its successor serves the resumed stream, which
  // opens with a stale resolution for each question nobody waits on. Not
  // between taking an answer and writing it, which is microseconds.
  restart: {
    pre: (m) => !m.restarted && !(m.x.taken && !m.x.logged),
    async run(c) {
      const { m, app } = c;
      if (m.x.taken && !m.x.logged) m.x.taken = false;
      m.x.waiting = false;
      m.restarted = true;
      app.sse.fail();
      await app.settle(12);
      app.sse.reopen();
      await app.settle(12);
      if (!m.x.logged) {
        app.sse.emit(staleRes(X));
        m.told.stale = true;
      }
      if (m.y && m.y.waiting) {
        m.y.waiting = false;
        app.sse.emit(staleRes(Y));
        m.y.toldStale = true;
      }
      await app.settle(12);
    },
  },
  // The same, with a resume that brings no stale resolution: only a
  // later replay says nobody waits. A stand-in for the two ways a replay
  // can be the first to say so: the daemon's whole-log read failed, or
  // (an update handover) the page's stream is still the retiring
  // daemon's while the replay comes from its successor.
  quietRestart: {
    pre: (m) => !m.restarted && !(m.x.taken && !m.x.logged),
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
    pre: (m) => m.wholeOffered && !(m.x.taken && !m.x.logged),
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
    pre: (m) => !(m.x.taken && !m.x.logged),
    async run(c) {
      c.app.internals.selectSession('sess-2', 'general-purpose', '/tmp/workspace');
      await c.app.settle(12);
      c.app.internals.selectSession('sess-1', 'general-purpose', '/tmp/workspace');
      await c.app.settle(12);
      // A fresh page state: what it knows is what the replay says.
      c.m.told = { stale: false, answered: c.m.told.answered, cancelled: false, refused: false };
      if (c.m.post) c.m.postBeforeSwitch = true;
      replay(c.app, c.m, false);
      await c.app.settle(12);
      c.m.wholeOffered = true;
    },
  },
  // One question per turn: a newer one needs the first done with.
  newer: {
    pre: (m) => !m.y && (m.x.logged || m.restarted),
    async run(c) {
      c.app.sse.emit(ask(500, Y, false));
      await c.app.settle(12);
      c.m.y = { waiting: true, toldStale: false };
    },
  },
};

function check(app, m, last) {
  const out = [];
  const pa = app.internals.session.pendingAsk;
  const lx = lineOf(app, X);
  const ly = m.y ? lineOf(app, Y) : null;
  const tx = lx ? lx.textContent : '';
  if (pa) {
    const l = lineOf(app, pa);
    if (!l || !l.textContent.endsWith(REPLY)) out.push(`box-line: box armed for ${pa}, whose line reads ${JSON.stringify(l && l.textContent.split('\n').pop())}`);
  }
  // Not after a 404 alone with a quiet resume: no resolution ever follows,
  // which is the case judged already.
  const onlyRefused = m.told.refused && !m.told.stale && !m.told.answered && !m.told.cancelled;
  if (pa === X && !(m.x.taken && !m.x.logged) && !(onlyRefused && m.quiet)
      && (m.told.stale || m.told.answered || m.told.cancelled || m.told.refused)) {
    out.push(`box-told: box armed for ${X} after ${Object.keys(m.told).filter((k) => m.told[k]).join('+')}`);
  }
  if (pa === Y && m.y && m.y.toldStale) out.push(`box-told: box armed for ${Y} after a stale signal`);
  if (tx.endsWith(LEFT) && (m.told.answered || m.told.cancelled)) {
    out.push(`left-answered: ${X} says left unanswered though ${m.told.answered ? 'answered' : 'cancelled'}`);
  }
  if (!m.post && m.told.stale && !m.told.answered && !m.told.cancelled) {
    if (!tx.endsWith(LEFT)) out.push(`settled: ${X} told stale, nothing on its way, line reads ${JSON.stringify(tx.split('\n').pop())}`);
    if (pa === X) out.push(`settled: ${X} told stale, nothing on its way, box armed for it`);
  }
  if (ly && m.y.toldStale && !ly.textContent.endsWith(LEFT)) {
    out.push(`settled: ${Y} told stale, line reads ${JSON.stringify(ly.textContent.split('\n').pop())}`);
  }
  if (last !== 'answer' && app.internals.session.connected && !app.internals.session.pendingPermissionID) {
    const lit = app.el('comm-dot').classList.contains('asking');
    if (lit !== !!pa) out.push(`light: asking=${lit} with the box ${pa ? `armed for ${pa}` : 'disarmed'}`);
  }
  return out;
}

async function runSequence(seq) {
  const posts = [];
  // Every timer the page sets is cleared when the order is done, so
  // thousands of pages do not stay alive behind their timers.
  const timers = [];
  const globals = {
    setTimeout: (...a) => { const t = setTimeout(...a); timers.push(t); return t; },
    setInterval: (...a) => { const t = setInterval(...a); if (t.unref) t.unref(); timers.push(t); return t; },
  };
  try {
    return await runIn(seq, posts, globals);
  } finally {
    for (const t of timers) { clearTimeout(t); clearInterval(t); }
    for (const p of posts) p({ networkError: 'done' });
  }
}

async function runIn(seq, posts, globals) {
  const app = await load({
    globals,
    routes: {
      'GET /api/sessions': [
        { id: 'sess-1', title: 'one', agent: 'general-purpose', workspace: '/tmp/workspace' },
        { id: 'sess-2', title: 'two', agent: 'general-purpose', workspace: '/tmp/workspace' },
      ],
      'POST /api/sessions/sess-1/input/*': () => new Promise((resolve) => { posts.push(resolve); }),
      'POST /api/sessions/*/messages': { status: 202, body: { status: 'accepted' } },
    },
  });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, X, false));
  await app.settle(12);
  const m = initialModel();
  const c = { app, m, settlePost: (v) => posts[posts.length - 1](v) };
  const problems = [];
  const emitting = new Set(['written', 'restart', 'quietRestart', 'elsewhere', 'cancel', 'whole', 'switch', 'newer']);
  for (const [at, name] of seq.entries()) {
    const r = await steps[name].run(c);
    if (r === 'skip') return { skipped: true, problems };
    if (name === 'answer') m.lightLag = true;
    else if (emitting.has(name)) m.lightLag = false;
    else if ((name === 'net' || name === 'r502') && app.internals.session.pendingAsk === X) m.lightLag = false;
    for (const p of check(app, m, m.lightLag ? 'answer' : name)) problems.push({ at, problem: p });
    if (problems.length) break;
  }
  return { problems };
}

function* sequences(depth) {
  // Only the longest orders are run: every step is checked as it is
  // taken, so each prefix is checked on the way.
  function* walk(m, prefix) {
    let extended = false;
    if (prefix.length < depth) {
      for (const [name, s] of Object.entries(steps)) {
        if (!s.pre(m)) continue;
        // The model step without the page, to know what may follow.
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

// The model half of each step, for the walk.
function simulate(name, m) {
  switch (name) {
    case 'answer': m.post = true; m.answers++; break;
    case 'ok': m.post = false; m.x.waiting = false; m.x.taken = true; break;
    case 'written': m.x.logged = 'answer'; m.x.taken = false; break;
    case 'r404': case 'net': case 'r502': m.post = false; break;
    case 'restart':
      m.x.waiting = false; m.restarted = true;
      if (m.y) m.y.waiting = false;
      break;
    case 'quietRestart':
      m.x.waiting = false; m.restarted = true; m.quiet = true;
      if (m.y) m.y.waiting = false;
      break;
    case 'elsewhere': m.x.waiting = false; m.x.logged = 'answer'; break;
    case 'cancel': m.x.waiting = false; m.x.logged = 'cancel'; break;
    case 'whole': m.wholeOffered = false; break;
    case 'switch': m.wholeOffered = true; break;
    case 'newer': m.y = { waiting: true }; break;
  }
}

// Depth 4 is the shallowest that reaches every defect this walk has found;
// ASK_ORDERS_DEPTH=5 with ASK_ORDERS_SHARDS=16 walks 3337 orders.
const DEPTH = Number(process.env.ASK_ORDERS_DEPTH || 4);
const SHARDS = Number(process.env.ASK_ORDERS_SHARDS || 8);

// One shard of the walk. The pages the walk loads are not collected
// (about 1 MB each stays in the heap), so the walk runs as child
// processes, one shard each, one at a time.
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
  test(`shard ${process.env.ASK_ORDERS_SHARD} of the walk`, { timeout: 600000 }, async () => {
    const [shard, shards] = process.env.ASK_ORDERS_SHARD.split('/').map(Number);
    console.log(`ASK_ORDERS_RESULT ${JSON.stringify(await runShard(shard, shards))}`);
  });
} else {
  test(`every order of a model question's answer, to depth ${DEPTH}, leaves box, line and light agreeing`, { timeout: 900000 }, () => {
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
      report.push(`${list.length} orders: ${list[0].problem}\n${list.slice(0, 4).map((x) => `    ${x.seq}`).join('\n')}`);
    }
    console.log(`ran ${ran} orders to depth ${DEPTH}, skipped ${skipped} (an answer the box could not take)`);
    assert.equal(report.length, 0, `\n${report.join('\n')}`);
  });
}
