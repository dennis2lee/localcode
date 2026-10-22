'use strict';

// What colour a conversation's light is.
//
// One rule across the product: green is the machine's colour, amber is
// yours. Blinking means something is happening; steady amber means
// nothing is, and will not until you answer something. Before this, work
// blinked amber and so did nothing else — which put "the model is busy"
// and "the model is stopped waiting for you" in one colour, the two
// states a person most needs to tell apart, because exactly one of them
// is theirs to end.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

function leds(app) {
  return (app.el('session-list').children || []).map(row => {
    const led = row.querySelector ? row.querySelector('.session-led') : null;
    return led ? led.className.replace('session-led ', '') : null;
  });
}

test('a session waiting for an answer is drawn differently from one that is working', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': [
        { id: 's1', title: 'working', agent: 'general-purpose', workspace: '/w', busy: true },
        { id: 's2', title: 'asking', agent: 'general-purpose', workspace: '/w', busy: true, asking: true },
        { id: 's3', title: 'idle', agent: 'general-purpose', workspace: '/w' },
      ],
    },
  });

  assert.deepEqual(leds(app), ['running', 'asking', 'idle']);
});

// The ordering is the whole point of the state, not a detail of it. A
// permission request is raised from inside a turn and the turn stays open
// while the question sits there, so "busy" is true for very nearly every
// session that is also waiting. Drawing busy would mean the waiting state
// never appeared at all.
test('waiting wins over working, because both are almost always true at once', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': [
        { id: 's1', title: 'both', agent: 'general-purpose', workspace: '/w', busy: true, asking: true },
      ],
    },
  });

  assert.deepEqual(leds(app), ['asking']);
  const row = app.el('session-list').children[0];
  const led = row.querySelector('.session-led');
  assert.match(led.title, /waiting for you/);
});

// A daemon that does not report the field is an older one, and an older
// daemon is a normal thing to be attached to. It must read as "not
// waiting" rather than as anything else.
test('a session list with no waiting field draws the old three states', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': [
        { id: 's1', title: 'working', agent: 'general-purpose', workspace: '/w', busy: true },
        { id: 's2', title: 'idle', agent: 'general-purpose', workspace: '/w' },
      ],
    },
  });

  assert.deepEqual(leds(app), ['running', 'idle']);
});

// The light under the prompt follows the same rule as the ones in the
// panel. (The rest of that light's behaviour is in activity_light.test.js;
// this is the state the two files share, and it has to agree.)
test('the light under the prompt goes amber and steady while a question is unanswered', async () => {
  const app = await load();
  const dot = app.el('comm-dot');

  app.sse.emit({ seq: 1, type: 'session.activity', data: { session: 'sess-1', busy: true } });
  await app.settle();
  assert.ok(dot.className.includes('active'), 'a running turn should blink');
  assert.ok(!dot.className.includes('asking'));

  app.applyEvent({ type: 'permission.request', data: { id: 'p1', tool: 'bash', description: 'run: ls', can_always: false } });
  await app.settle();
  assert.ok(dot.className.includes('asking'), dot.className);
  assert.match(dot.title, /waiting for you/);

  // Answered: the turn it interrupted is still running, so the light goes
  // back to blinking rather than to idle.
  app.el('permission-allow').click();
  await app.settle();
  assert.ok(!dot.className.includes('asking'), dot.className);
  assert.ok(dot.className.includes('active'), 'the turn is still running');
});

// Reported from real use: a conversation with a task still running after
// its turn had ended drew a steady green light, the light for "finished
// while you were elsewhere". A task it started keeps it blinking.
test('a session whose tasks are still running is drawn as working', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': [
        { id: 's1', title: 'tasks', agent: 'general-purpose', workspace: '/w', tasks: 2 },
        { id: 's2', title: 'turn and task', agent: 'general-purpose', workspace: '/w', busy: true, tasks: 1 },
      ],
    },
  });
  assert.deepEqual(leds(app), ['running', 'running']);
  const titles = app.el('session-list').children.map((row) => row.querySelector('.session-led').title);
  assert.match(titles[0], /2 tasks still running/);
  assert.match(titles[1], /a turn is running in this session, and 1 task$/);
});

// The activity event is the session's whole state. The turn ending while
// a task it started still runs is not the session finishing; the last
// task ending is, and only then does the light say there is something to
// read.
test('a session finishes when its last task does, not when its turn does', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': [
        { id: 'sess-1', title: 'here', agent: 'general-purpose', workspace: '/w' },
        { id: 's2', title: 'elsewhere', agent: 'general-purpose', workspace: '/w', busy: true },
      ],
    },
  });
  app.sse.emit({ type: 'session.activity', data: { session: 's2', busy: true, tasks: 1, asking: false } });
  app.sse.emit({ type: 'session.activity', data: { session: 's2', busy: false, tasks: 1, asking: false } });
  assert.deepEqual(leds(app), ['idle', 'running'], 'the turn ending drew the session as finished');

  app.sse.emit({ type: 'session.activity', data: { session: 's2', busy: false, tasks: 0, asking: false } });
  assert.deepEqual(leds(app), ['idle', 'unread']);
});

// A question in another conversation turns its light amber when it is
// asked, not when the list happens to be loaded again, and back when it
// is answered.
test('a session starts and stops waiting for you as it happens', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': [
        { id: 'sess-1', title: 'here', agent: 'general-purpose', workspace: '/w' },
        { id: 's2', title: 'elsewhere', agent: 'general-purpose', workspace: '/w', busy: true },
      ],
    },
  });
  app.sse.emit({ type: 'session.activity', data: { session: 's2', busy: true, tasks: 0, asking: true } });
  assert.deepEqual(leds(app), ['idle', 'asking']);
  app.sse.emit({ type: 'session.activity', data: { session: 's2', busy: true, tasks: 0, asking: false } });
  assert.deepEqual(leds(app), ['idle', 'running']);
});

// A daemon from before tasks and questions were in the event sends the
// turn alone, and what it does not say is left as the list had it.
test('an older daemon\'s activity event leaves what it does not mention alone', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': [
        { id: 'sess-1', title: 'here', agent: 'general-purpose', workspace: '/w' },
        { id: 's2', title: 'elsewhere', agent: 'general-purpose', workspace: '/w', busy: true, tasks: 1 },
      ],
    },
  });
  app.sse.emit({ type: 'session.activity', data: { session: 's2', busy: false } });
  assert.deepEqual(leds(app), ['idle', 'running'], 'the task the event did not mention was dropped');
});

// A folded group's header carries the strongest light of what it covers,
// and a session whose tasks are running is working there too.
test('a folded group shows a session whose tasks are running as working', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': [
        { id: 's1', title: 'tasks', agent: 'general-purpose', workspace: '/w', group: 'work', tasks: 1 },
      ],
      'GET /api/sessions/groups': { names: ['work'] },
    },
    localStorage: { 'localcode.collapsedGroups': JSON.stringify(['work']) },
  });
  const header = app.el('session-list').querySelector('.session-group-header');
  const led = header.querySelector('.session-led');
  assert.ok(led, 'the folded group has no light');
  assert.equal(led.className, 'session-led running');
});

// The light under the prompt says what the panel says about the session
// on screen: amber for a question the model asked, as for a permission
// request.
test('the light under the prompt is amber while the model waits on your answer', async () => {
  const app = await load({
    routes: { 'GET /api/sessions': [{ id: 'sess-1', title: 'here', agent: 'general-purpose', workspace: '/w', busy: true }] },
  });
  app.applyEvent({ type: 'input.request', data: { id: 'q1', question: 'which one?', options: ['a', 'b'] } });
  const dot = app.el('comm-dot');
  assert.ok(dot.className.includes('asking'), dot.className);
  assert.match(dot.title, /answer a question/);
  app.applyEvent({ type: 'input.resolved', data: { id: 'q1', answer: 'a' } });
  assert.ok(!dot.className.includes('asking'), dot.className);
});

// A list asked for before an activity event and answered after it
// describes the moment it was asked, and does not undo the event.
test('a list answered after a newer activity event keeps the event', async () => {
  let hold = false;
  let answer;
  const app = await load({
    routes: {
      'GET /api/sessions': () => {
        if (!hold) return [{ id: 'sess-1', title: 'here', agent: 'general-purpose', workspace: '/w' }, { id: 's2', title: 'elsewhere', agent: 'general-purpose', workspace: '/w' }];
        return new Promise((resolve) => { answer = resolve; });
      },
    },
  });
  hold = true;
  app.sse.emit({ type: 'session.renamed', data: { title: 'x' } });
  await app.settle();
  assert.ok(answer, 'the list was not asked for again');
  app.sse.emit({ type: 'session.activity', data: { session: 's2', busy: false, tasks: 1, asking: false } });
  answer([{ id: 'sess-1', title: 'here', agent: 'general-purpose', workspace: '/w' }, { id: 's2', title: 'elsewhere', agent: 'general-purpose', workspace: '/w', tasks: 0 }]);
  await app.settle();
  assert.deepEqual(leds(app), ['idle', 'running'], 'the late list drew a working session as idle');
});

// Daemon-wide events sent while the stream was down are in no log, so the
// list is read again when it comes back.
test('after the stream comes back the lights are read again', async () => {
  let tasks = 1;
  const app = await load({
    routes: {
      'GET /api/sessions': () => [{ id: 'sess-1', title: 'here', agent: 'general-purpose', workspace: '/w' }, { id: 's2', title: 'elsewhere', agent: 'general-purpose', workspace: '/w', tasks }],
    },
  });
  assert.deepEqual(leds(app), ['idle', 'running']);
  // An event from before the list was asked for is older than the list,
  // and is not laid over it.
  app.sse.emit({ type: 'session.activity', data: { session: 's2', busy: false, tasks: 1, asking: false } });
  app.sse.fail();
  await app.settle();
  tasks = 0;
  app.sse.reopen();
  await app.settle();
  await app.wait(1200);
  assert.deepEqual(leds(app), ['idle', 'idle'], 'the light still says a task ended long ago is running');
});

// An older daemon's event leaves the question it does not mention as it
// was, as it does the tasks.
test('an older daemon\'s activity event leaves a waiting session waiting', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': [
        { id: 'sess-1', title: 'here', agent: 'general-purpose', workspace: '/w' },
        { id: 's2', title: 'elsewhere', agent: 'general-purpose', workspace: '/w', busy: true, asking: true },
      ],
    },
  });
  app.sse.emit({ type: 'session.activity', data: { session: 's2', busy: true } });
  assert.deepEqual(leds(app), ['idle', 'asking']);
});

// Two list requests in flight: the older answer arriving second described
// an earlier moment than the one already drawn, and is not applied.
test('an older list answered after a newer one is not drawn', async () => {
  const pending = [];
  let hold = false;
  const here = { id: 'sess-1', title: 'here', agent: 'general-purpose', workspace: '/w' };
  const app = await load({
    routes: {
      'GET /api/sessions': () => {
        if (!hold) return [here, { id: 's2', title: 'elsewhere', agent: 'general-purpose', workspace: '/w' }];
        return new Promise((resolve) => pending.push(resolve));
      },
    },
  });
  hold = true;
  app.sse.emit({ type: 'session.renamed', data: {} });
  app.sse.emit({ type: 'session.renamed', data: {} });
  await app.settle();
  assert.equal(pending.length, 2, 'two requests were not in flight');
  pending[1]([here, { id: 's2', title: 'elsewhere', agent: 'general-purpose', workspace: '/w', tasks: 0 }]);
  await app.settle();
  pending[0]([here, { id: 's2', title: 'elsewhere', agent: 'general-purpose', workspace: '/w', tasks: 1 }]);
  await app.settle();
  assert.deepEqual(leds(app), ['idle', 'idle'], 'the older answer was drawn over the newer one');
});

// A question raised after a list was asked for survives the list's late
// answer, as a running task does.
test('a session waiting since the list was asked for stays amber', async () => {
  let answer;
  let hold = false;
  const rows = () => [{ id: 'sess-1', title: 'here', agent: 'general-purpose', workspace: '/w' }, { id: 's2', title: 'elsewhere', agent: 'general-purpose', workspace: '/w', busy: true }];
  const app = await load({
    routes: { 'GET /api/sessions': () => (hold ? new Promise((resolve) => { answer = resolve; }) : rows()) },
  });
  hold = true;
  app.sse.emit({ type: 'session.renamed', data: {} });
  await app.settle();
  app.sse.emit({ type: 'session.activity', data: { session: 's2', busy: true, tasks: 0, asking: true } });
  answer(rows());
  await app.settle();
  assert.deepEqual(leds(app), ['idle', 'asking']);
});

// The light under the prompt says which of the two it is waiting on.
test('the light under the prompt names a permission request as one', async () => {
  const app = await load();
  app.applyEvent({
    type: 'permission.request',
    data: { id: 'p1', tool: 'bash', description: 'run make', rule: 'bash:make', can_always: false },
  });
  const dot = app.el('comm-dot');
  assert.ok(dot.className.includes('asking'), dot.className);
  assert.match(dot.title, /answer a permission request/);
});

// Reported from real use: with Windows' animation effects off, which
// remote desktops and managed machines set for everyone, the page is asked
// for less motion, and the working light stood still: a steady dot read as
// finished, and the green ring drawn instead was not read as working
// either. The status lights blink whatever the setting, since the blink is
// what says "working".
//
// Checked against every rule that applies to the light, wherever it sits
// and however its selector is spelled, rather than against one spelling:
// a rule applies when the classes of the selector's last compound are all
// classes the light carries, and every such rule that sets animation must
// keep the blink.
test('the status lights blink with less motion asked for too', () => {
  const css = require('node:fs').readFileSync(require('node:path').join(__dirname, '../../internal/daemon/static/style.css'), 'utf8')
    .replace(/\/\*[\s\S]*?\*\//g, '');
  const rules = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].map((m) => ({ selectors: m[1], body: m[2] }));
  const classesOf = (selector) => {
    const last = selector.trim().replace(/:not\([^)]*\)/g, '').split(/[\s>+~]+/).pop();
    return [...last.matchAll(/\.([A-Za-z0-9_-]+)/g)].map((m) => m[1]);
  };
  for (const [carries, keyframes] of [
    [['session-led', 'running'], 'session-led-pulse'],
    [['comm-dot', 'connected', 'active'], 'pulse'],
    [['led', 'led-degraded'], 'led-blink'],
  ]) {
    const applying = rules.filter((r) => r.selectors.split(',').some((sel) => {
      const cls = classesOf(sel);
      return cls.length > 0 && cls.every((c) => carries.includes(c));
    }));
    const animating = applying.filter((r) => /animation(-name|-play-state)?\s*:/.test(r.body));
    assert.ok(animating.length > 0, `${carries.join('.')} has no animation`);
    for (const r of animating) {
      assert.match(r.body, new RegExp(`animation(-name)?\\s*:\\s*${keyframes}\\b`),
        `${carries.join('.')} loses its blink to: ${r.selectors.trim()} { ${r.body.trim()} }`);
      assert.ok(!/animation-play-state\s*:\s*paused/.test(r.body), `${carries.join('.')} is paused by ${r.selectors.trim()}`);
    }
  }
});

