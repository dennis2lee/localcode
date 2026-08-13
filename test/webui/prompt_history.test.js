'use strict';

// Up/Down recall belongs to the conversation, not to the page.
//
// It used to live in the per-session state that a switch wipes, and nothing
// ever refilled it: opening another session and coming back left Up
// recalling nothing, and a reload lost every prompt of the session it
// reopened. Recall is most useful in exactly those moments — the last thing
// you asked in *this* project, after a detour through another one.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

// recallAll walks the whole list backwards and returns what the box showed,
// newest first — the arrow keys' own view of history rather than the array
// behind it.
function recallAll(app) {
  const input = app.type('');
  input.selectionStart = input.selectionEnd = 0;
  const seen = [];
  for (let i = 0; i < 20; i++) {
    app.press('ArrowUp');
    if (seen.length > 0 && input.value === seen[seen.length - 1]) break;
    if (!input.value) break;
    seen.push(input.value);
  }
  return seen;
}

test('history comes back when you return to a session', async () => {
  const app = await load();

  app.type('first in sess-1');
  await app.el('send').click();
  await app.settle();
  app.applyEvent({ type: 'turn.done' });

  app.selectSession('sess-2', 'plan', '');
  await app.settle();
  assert.deepEqual(recallAll(app), [], 'the other session has its own list');

  app.type('typed in sess-2');
  await app.el('send').click();
  await app.settle();

  app.selectSession('sess-1', 'general-purpose', '');
  await app.settle();

  assert.deepEqual(
    recallAll(app),
    ['first in sess-1'],
    'coming back to a session used to find its recall list wiped',
  );
});

// The transcript replay a session opens with is what rebuilds recall after
// a reload — nothing about history is stored client-side, and this is the
// only source for prompts typed before this page existed.
test('the replayed transcript refills recall', async () => {
  const app = await load();

  for (const text of ['ask one', 'ask two', 'ask three']) {
    app.sse.emit({ type: 'message.user', data: { text } });
  }

  assert.deepEqual(recallAll(app), ['ask three', 'ask two', 'ask one']);
});

test('a prompt is not recorded twice by its own event', async () => {
  const app = await load();

  app.type('only once');
  await app.el('send').click();
  await app.settle();
  app.sse.emit({ type: 'message.user', data: { text: 'only once' } });

  assert.deepEqual(Array.from(app.state.history), ['only once']);
});

// An event arriving mid-walk (another client sending, or a slow replay)
// must not yank the box out from under the person walking.
test('an arriving prompt does not interrupt a walk', async () => {
  const app = await load();
  app.state.history.push('older', 'newer');
  app.state.historyIdx = 2;

  const input = app.type('');
  input.selectionStart = input.selectionEnd = 0;
  app.press('ArrowUp');
  app.press('ArrowUp');
  assert.equal(input.value, 'older');

  app.sse.emit({ type: 'message.user', data: { text: 'from another client' } });
  assert.equal(input.value, 'older', 'the box changed under a walk in progress');

  app.press('ArrowDown');
  assert.equal(input.value, 'newer', 'the walk kept its place');
});

test('deleting a session forgets its history', async () => {
  const app = await load({
    confirm: true,
    routes: { 'DELETE /api/sessions/sess-1': { status: 204 } },
  });

  app.type('doomed prompt');
  await app.el('send').click();
  await app.settle();

  await app.deleteSessionConfirm({ id: 'sess-1', title: 'first session' });
  await app.settle();

  app.selectSession('sess-1', 'general-purpose', '');
  await app.settle();
  assert.deepEqual(recallAll(app), [], 'a deleted conversation left its prompts behind');
});
