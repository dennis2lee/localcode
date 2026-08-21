'use strict';

// Following the newest output, and letting go of it when the reader
// scrolls away.
//
// Reported from real use: "while the model is writing, scrolling up moves
// the view back to where it is writing, so I cannot read what I scrolled
// up to". Every append used to end at the bottom unconditionally.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

// layout describes a scrolling box, since the harness DOM has none: a
// 1000px transcript in a 200px window. atBottom is scrollTop 800.
function layout(el, { scrollTop = 800, scrollHeight = 1000, clientHeight = 200 } = {}) {
  el.scrollHeight = scrollHeight;
  el.clientHeight = clientHeight;
  el.scrollTop = scrollTop;
  return el;
}

// scrollTo moves the view the way a wheel does: the position changes and
// the browser fires a scroll event.
function scrollTo(el, top) {
  el.scrollTop = top;
  el.fire('scroll');
}

test('output arriving while the reader is at the bottom keeps following it', async () => {
  const app = await load();
  const el = layout(app.el('transcript'));

  app.sse.emit({ type: 'message.part.delta', data: { text: 'a first line of the answer' } });
  assert.equal(el.scrollTop, el.scrollHeight, 'the view did not follow the newest output');
});

test('scrolling up holds the view there while the model keeps writing', async () => {
  const app = await load();
  const el = layout(app.el('transcript'));

  scrollTo(el, 120);
  for (let i = 0; i < 5; i++) {
    app.sse.emit({ type: 'message.part.delta', data: { text: `line ${i}\n` } });
  }
  app.sse.emit({ type: 'tool.start', data: { tool_use_id: 't1', name: 'bash', input: '{"command":"ls"}' } });
  app.sse.emit({ type: 'tool.end', data: { tool_use_id: 't1', content: 'ok' } });

  assert.equal(el.scrollTop, 120, 'the view was dragged back to the newest output');
});

test('scrolling back to the bottom resumes following, with nothing to turn on', async () => {
  const app = await load();
  const el = layout(app.el('transcript'));

  scrollTo(el, 120);
  app.sse.emit({ type: 'message.part.delta', data: { text: 'while away' } });
  assert.equal(el.scrollTop, 120);

  scrollTo(el, 800);
  app.sse.emit({ type: 'message.part.delta', data: { text: 'back at the bottom' } });
  assert.equal(el.scrollTop, el.scrollHeight, 'following did not resume at the bottom');
});

// The position is measured when the content changes, not remembered from
// the last scroll event. A browser delivers those asynchronously and
// throttles them in a background tab, and a remembered flag that misses
// one is wrong until the next scroll — which, for someone reading with
// the mouse still, never comes.
test('a scroll the page never heard about is still honoured', async () => {
  const app = await load();
  const el = layout(app.el('transcript'));

  el.scrollTop = 120; // moved, with no scroll event fired at all
  app.sse.emit({ type: 'message.part.delta', data: { text: 'output while away' } });
  assert.equal(el.scrollTop, 120, 'the view was dragged back by output it should have ignored');
});

test('the jump control appears only while the view is away from the bottom', async () => {
  const app = await load();
  const el = layout(app.el('transcript'));
  const jump = app.el('jump-bottom');
  assert.equal(jump.hidden, true, 'the jump control is showing at the bottom of the transcript');

  scrollTo(el, 120);
  assert.equal(jump.hidden, false, 'scrolling up did not offer a way back');

  jump.click();
  assert.equal(el.scrollTop, el.scrollHeight, 'the jump control did not go to the bottom');
  assert.equal(jump.hidden, true, 'the jump control stayed after it was used');

  app.sse.emit({ type: 'message.part.delta', data: { text: 'more' } });
  assert.equal(el.scrollTop, el.scrollHeight, 'following did not resume after the jump');
});

test('sending a prompt takes the reader back to the bottom', async () => {
  const app = await load();
  const el = layout(app.el('transcript'));

  scrollTo(el, 120);
  app.type('what does this do?');
  app.press('Enter');
  await app.settle();

  assert.equal(el.scrollTop, el.scrollHeight, 'the prompt was sent to a view still scrolled away');
  assert.equal(app.el('jump-bottom').hidden, true);
});

test('opening another session starts at its newest output', async () => {
  const app = await load();
  const el = layout(app.el('transcript'));

  scrollTo(el, 120);
  app.selectSession('sess-2', 'general-purpose', '');
  await app.settle();

  assert.equal(el.scrollTop, el.scrollHeight, 'a session opened part-way up the previous one');
  assert.equal(app.el('jump-bottom').hidden, true);
});

// A background task's own window is a live transcript too, and reading
// back through what a task did while it keeps working is the same need.
test("a task window holds its place while the task keeps writing", async () => {
  const app = await load();
  app.sse.emit({ seq: 1, type: 'task.spawned', data: { task_id: 'task-1', agent: 'explore' } });
  app.sse.emit({ seq: 2, type: 'task.status', data: { task_id: 'task-1', status: 'running' } });
  app.el('tasks').children[0].click();
  await app.settle();

  const body = layout(app.el('task-modal-body'));
  const stream = app.streamFor('task-1');
  assert.ok(stream, 'the task window opened no stream of its own');

  scrollTo(body, 120);
  stream.emit({ type: 'message.part.delta', data: { text: 'still working' } });
  assert.equal(body.scrollTop, 120, 'the task window was dragged back to its newest output');

  scrollTo(body, 800);
  stream.emit({ type: 'message.part.delta', data: { text: 'and more' } });
  assert.equal(body.scrollTop, body.scrollHeight, 'following did not resume at the bottom');
});
