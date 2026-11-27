'use strict';

// A send refused as busy goes into this page's queue, to be sent again.
// The queue is the shown conversation's, so a refusal that comes after
// the page moved to another conversation would send the prompt there. It
// goes back into the box of the conversation it was typed into instead.
// The TUI does the same (internal/tui/switch_refusal_test.go).

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const BUSY = { status: 409, body: { error: 'session s is already processing a message' } };

function daemon() {
  const replies = [];
  return {
    busy() { replies.push(BUSY); },
    // holdBusy makes the next send wait; the function it returns refuses
    // it as busy.
    holdBusy() {
      let refuse;
      replies.push(new Promise((resolve) => { refuse = resolve; }));
      return () => refuse(BUSY);
    },
    routes: { 'POST /api/sessions/*/messages': () => replies.shift() ?? { status: 202, body: { status: 'accepted', seq: 1 } } },
  };
}

async function send(app, text) {
  app.type(text);
  await app.el('send').click();
  await app.settle();
}

const sentTo = (app, id) => app.callsTo('POST', `/api/sessions/${id}/messages`).map((c) => c.body.text);

async function refusedAfterTheSwitch(app, refuse) {
  app.selectSession('sess-2', 'general-purpose', '/tmp/workspace');
  await app.settle();
  refuse();
  await app.settle();
  // Past the queue's retry.
  await app.wait(400);
  await app.settle();
  assert.deepEqual(sentTo(app, 'sess-2'), [], 'a prompt typed into sess-1 was sent to sess-2');
  assert.deepEqual(Array.from(app.state.promptQueue), [], 'a prompt typed into sess-1 is in sess-2\'s queue');
  app.selectSession('sess-1', 'general-purpose', '/tmp/workspace');
  await app.settle();
}

test('a prompt sent to an idle session and refused after a switch goes back to its own box', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  const refuse = d.holdBusy();
  await send(app, 'for the first conversation');

  await refusedAfterTheSwitch(app, refuse);
  assert.equal(app.el('input').value, 'for the first conversation', 'the prompt is gone from its own conversation');
});

test('a prompt sent mid-turn and refused after a switch goes back to its own box', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  app.state.waiting = true;
  const refuse = d.holdBusy();
  await send(app, 'and the docs');

  await refusedAfterTheSwitch(app, refuse);
  assert.equal(app.el('input').value, 'and the docs');
});

test('a queued prompt refused again after a switch goes back to its own box', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  d.busy();
  await send(app, 'later');
  assert.deepEqual(Array.from(app.state.promptQueue), ['later'], 'precondition: queued, retry armed');
  const refuse = d.holdBusy();
  await app.wait(400);
  await app.settle();
  assert.deepEqual(sentTo(app, 'sess-1'), ['later', 'later'], 'precondition: the retry went out');

  await refusedAfterTheSwitch(app, refuse);
  assert.equal(app.el('input').value, 'later');
});

test('a refused prompt goes ahead of what its box already held', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  const refuse = d.holdBusy();
  await send(app, 'first thought');
  app.type('second thought');

  await refusedAfterTheSwitch(app, refuse);
  assert.equal(app.el('input').value, 'first thought\nsecond thought');
});

// Away and back before the refusal: it is the shown conversation's again,
// and it is queued and sent to it, as it always was.
test('a refusal after going away and back is queued for the conversation it was sent to', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  const refuse = d.holdBusy();
  await send(app, 'for the first conversation');
  app.selectSession('sess-2', 'general-purpose', '/tmp/workspace');
  await app.settle();
  app.selectSession('sess-1', 'general-purpose', '/tmp/workspace');
  await app.settle();

  refuse();
  await app.settle();
  assert.deepEqual(Array.from(app.state.promptQueue), ['for the first conversation']);
  await app.wait(400);
  await app.settle();
  assert.deepEqual(sentTo(app, 'sess-1'), ['for the first conversation', 'for the first conversation']);
  assert.deepEqual(sentTo(app, 'sess-2'), []);
});

// A prompt already in the queue when the page moves on: the queue is
// emptied with the conversation, and nothing would send it.
test('a prompt still queued at a switch goes back to its own box', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  d.busy();
  await send(app, 'later');
  assert.deepEqual(Array.from(app.state.promptQueue), ['later'], 'precondition: queued, retry armed');

  app.selectSession('sess-2', 'general-purpose', '/tmp/workspace');
  await app.settle();
  await app.wait(400);
  await app.settle();
  assert.deepEqual(sentTo(app, 'sess-2'), []);
  app.selectSession('sess-1', 'general-purpose', '/tmp/workspace');
  await app.settle();
  assert.equal(app.el('input').value, 'later', 'a queued prompt is gone after the switch, unsent and unsaid');
});

test('prompts still queued at a switch keep their order, ahead of the box', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  app.state.waiting = true;
  d.busy();
  await send(app, 'first');
  d.busy();
  await send(app, 'second');
  assert.deepEqual(Array.from(app.state.promptQueue), ['first', 'second'], 'precondition: both queued');
  app.type('third');

  app.selectSession('sess-2', 'general-purpose', '/tmp/workspace');
  await app.settle();
  app.selectSession('sess-1', 'general-purpose', '/tmp/workspace');
  await app.settle();
  assert.equal(app.el('input').value, 'first\nsecond\nthird');
});
