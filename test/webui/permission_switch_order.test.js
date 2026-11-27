'use strict';

// The four switches in the permissions window go one at a time, and each
// answer is drawn only in the conversation it was asked for.
//
// They were sent as they were pressed. Two changes could be out at once
// and their answers arrive in either order, and every answer is a
// snapshot of all four, so the one that arrived last was drawn: an older
// value over a newer one. A change still out when the conversation was
// switched drew its snapshot, or said its failure, in the next one.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const BOXES = {
  skip_all: 'skip-permissions-checkbox',
  skip_tools: 'skip-tools-checkbox',
  read_outside: 'read-outside-checkbox',
  write_outside: 'write-outside-checkbox',
};

const REFUSED = { status: 500, body: { error: 'the session log could not be written' } };

function snapshot(effective) {
  return { effective: { ...effective }, source: {}, remembered: { read: [], write: [] } };
}

// heldDaemon applies each change when it arrives and holds its answer
// until the test lets it go, so a test decides the order answers land in.
// refuse(n) says whether the nth change is refused.
function heldDaemon(refuse = () => false) {
  const d = {
    has: { skip_all: false, skip_tools: false, read_outside: false, write_outside: false },
    sent: [],
    held: [],
    route(body) {
      const n = d.sent.length;
      d.sent.push(body);
      let answer = REFUSED;
      if (!refuse(n)) {
        d.has[body.switch] = body.enabled;
        answer = snapshot(d.has);
      }
      return new Promise((resolve) => d.held.push(() => resolve(answer)));
    },
  };
  return d;
}

async function press(app, name, to) {
  const box = app.el(BOXES[name]);
  box.checked = to;
  box.fire('change');
  await app.settle();
}

async function openWindow(routes) {
  const app = await load({ routes });
  app.el('permission-status-btn').click();
  await app.settle();
  return app;
}

test('the switches are sent one at a time, in the order they were pressed', async () => {
  const daemon = heldDaemon();
  const app = await openWindow({ 'POST /api/sessions/sess-1/permissions': (body) => daemon.route(body) });

  await press(app, 'read_outside', true);
  await press(app, 'skip_all', true);
  await press(app, 'read_outside', false);
  assert.equal(daemon.sent.length, 1, 'a change was sent before the one before it was answered');

  // Each answer lets the next change go, in the order the boxes moved.
  for (let i = 0; i < 3; i++) {
    assert.equal(daemon.held.length, i + 1);
    daemon.held[i]();
    await app.settle();
  }
  assert.deepEqual(daemon.sent.map((b) => [b.switch, b.enabled]), [
    ['read_outside', true], ['skip_all', true], ['read_outside', false],
  ]);
  assert.equal(app.el(BOXES.read_outside).checked, false, 'an older answer was drawn over the last change');
  assert.equal(app.el(BOXES.skip_all).checked, true);
  assert.equal(app.el('permission-status-btn').textContent, 'permissions: skip');
});

test('a switch failure goes when a later change of that switch is taken, and not when another is', async () => {
  // The first change is refused, the rest are taken.
  const daemon = heldDaemon((n) => n === 0);
  const app = await openWindow({ 'POST /api/sessions/sess-1/permissions': (body) => daemon.route(body) });
  const note = app.el('permission-settings-note');
  const answerNext = async () => { daemon.held[daemon.held.length - 1](); await app.settle(); };

  await press(app, 'read_outside', true);
  await answerNext();
  assert.match(note.textContent, /could not change read_outside/);
  assert.equal(note.classList.contains('err'), true);

  await press(app, 'skip_all', true);
  await answerNext();
  assert.match(note.textContent, /could not change read_outside/, 'another switch being taken took the failure away');

  await press(app, 'read_outside', true);
  await answerNext();
  assert.equal(daemon.has.read_outside, true);
  assert.doesNotMatch(note.textContent, /could not change read_outside/, 'the daemon has what the box shows, and the failure is still said');
  assert.equal(note.classList.contains('err'), false);
});

test('an answer for a conversation that is no longer open is not drawn in the one that is', async () => {
  // sess-1's first change is taken and its second refused. sess-2 has
  // every switch off, and nothing sent to it is expected.
  const daemon = heldDaemon((n) => n === 1);
  const toSess2 = [];
  const app = await openWindow({
    'POST /api/sessions/sess-1/permissions': (body) => daemon.route(body),
    'POST /api/sessions/sess-2/permissions': (body) => { toSess2.push(body); return REFUSED; },
    'GET /api/sessions/sess-2/permissions': snapshot({ skip_all: false, skip_tools: false, read_outside: false, write_outside: false }),
  });
  const note = app.el('permission-settings-note');

  await press(app, 'skip_all', true);
  // Pressed in sess-1, and still waiting behind the first when sess-2 is
  // opened.
  await press(app, 'read_outside', true);
  assert.equal(daemon.sent.length, 1);
  app.selectSession('sess-2', 'general-purpose', '/tmp/workspace');
  await app.settle();
  assert.equal(app.el('permission-status-btn').textContent, 'permissions: ask');

  daemon.held[0]();
  await app.settle();
  assert.equal(app.el('permission-status-btn').textContent, 'permissions: ask', "sess-1's snapshot was drawn in sess-2");
  assert.equal(app.el(BOXES.skip_all).checked, false, "sess-2's box was set to sess-1's value");

  // The second change is still sent, to the conversation it was made in.
  assert.equal(daemon.sent.length, 2, 'the change waiting in the queue was not sent');
  assert.deepEqual(daemon.sent[1], { switch: 'read_outside', enabled: true });
  assert.deepEqual(toSess2, [], 'a change made in sess-1 was sent to sess-2');
  daemon.held[1]();
  await app.settle();
  assert.doesNotMatch(note.textContent, /could not change/, "sess-1's failure was said in sess-2");
  assert.equal(note.classList.contains('err'), false);
});

// A forget answers with the same snapshot of all four switches, so it
// goes in their queue. Sent at once, it reached the daemon ahead of a
// switch change still waiting behind another, and its answer, taken
// before that change, could be drawn last. Found by review.
test('a forget pressed behind switch changes is sent after them, and the boxes end where the daemon is', async () => {
  const daemon = heldDaemon();
  let remembered = ['/tmp/elsewhere'];
  const arrivals = [];
  const answer = () => ({ effective: { ...daemon.has }, source: {}, remembered: { read: remembered.slice(), write: [] } });
  const app = await openWindow({
    'GET /api/sessions/sess-1/permissions': () => answer(),
    'POST /api/sessions/sess-1/permissions': (body) => { arrivals.push(body.switch); return daemon.route(body); },
    'POST /api/sessions/sess-1/permissions/forget': () => {
      arrivals.push('forget');
      remembered = [];
      const now = answer();
      return new Promise((resolve) => daemon.held.push(() => resolve(now)));
    },
  });
  // The window was opened before the remembered list was read.
  app.el('permission-settings-close').click();
  app.el('permission-status-btn').click();
  await app.settle();
  const forget = Array.from(app.el('remembered-outside').children)[0].children[1];

  await press(app, 'read_outside', true);
  await press(app, 'skip_all', true);
  forget.click();
  await app.settle();
  for (let i = 0; i < 3; i++) {
    daemon.held[i]();
    await app.settle();
  }
  assert.deepEqual(arrivals, ['read_outside', 'skip_all', 'forget']);
  assert.equal(app.el(BOXES.skip_all).checked, true, "the forget's answer drew skip_all as it was before it was taken");
  assert.equal(app.el('permission-status-btn').textContent, 'permissions: skip');
  assert.equal(app.el('remembered-outside').children.length, 0);
});

test('a forget answered after a conversation switch is not drawn in the one that is open', async () => {
  const daemon = heldDaemon();
  daemon.has.skip_all = true;
  const app = await openWindow({
    'GET /api/sessions/sess-1/permissions': () => ({ ...snapshot(daemon.has), remembered: { read: ['/tmp/elsewhere'], write: [] } }),
    'POST /api/sessions/sess-1/permissions/forget': () => {
      const now = snapshot(daemon.has);
      return new Promise((resolve) => daemon.held.push(() => resolve(now)));
    },
    'GET /api/sessions/sess-2/permissions': snapshot({ skip_all: false, skip_tools: false, read_outside: false, write_outside: false }),
  });
  app.el('permission-settings-close').click();
  app.el('permission-status-btn').click();
  await app.settle();
  Array.from(app.el('remembered-outside').children)[0].children[1].click();
  await app.settle();
  app.selectSession('sess-2', 'general-purpose', '/tmp/workspace');
  await app.settle();

  daemon.held[0]();
  await app.settle();
  assert.equal(app.el('permission-status-btn').textContent, 'permissions: ask', "sess-1's snapshot was drawn in sess-2");
  assert.equal(app.el(BOXES.skip_all).checked, false);
});

test('a change taken in another conversation does not take away a failure said in this one', async () => {
  // The queue is one for the page, so a change made in sess-1 can answer
  // while sess-2 is open. sess-2's own refused read_outside is still true.
  const sess1 = heldDaemon();
  const app = await openWindow({
    'POST /api/sessions/sess-1/permissions': (body) => sess1.route(body),
    'POST /api/sessions/sess-2/permissions': REFUSED,
    'GET /api/sessions/sess-1/permissions': () => snapshot(sess1.has),
    'GET /api/sessions/sess-2/permissions': snapshot({ skip_all: false, skip_tools: false, read_outside: false, write_outside: false }),
  });
  const note = app.el('permission-settings-note');

  app.selectSession('sess-2', 'general-purpose', '/tmp/workspace');
  await app.settle();
  await press(app, 'read_outside', true);
  assert.match(note.textContent, /could not change read_outside/);

  app.selectSession('sess-1', 'general-purpose', '/tmp/workspace');
  await app.settle();
  await press(app, 'read_outside', true);
  app.selectSession('sess-2', 'general-purpose', '/tmp/workspace');
  await app.settle();
  sess1.held[0]();
  await app.settle();
  assert.match(note.textContent, /could not change read_outside/, "sess-1's change took away sess-2's failure");
});
