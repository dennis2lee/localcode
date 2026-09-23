'use strict';

// A muse reasoning block the daemon logged (thinking.block), and what a
// page does after its stream has been away.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load, defaultRoutes } = require('./harness');

function foldBlocks(app) {
  return Array.from(app.el('transcript').querySelectorAll('.msg-thinking'))
    .filter((el) => el.classList.contains('fold'));
}

function headerAgrees(block) {
  const open = !block.querySelector('.body').hidden;
  return block.querySelector('.head').getAttribute('aria-expanded') === String(open) &&
    block.querySelector('.marker').textContent === (open ? '▾' : '▸');
}

async function waitFor(app, cond) {
  for (let i = 0; i < 40 && !cond(); i++) await app.wait(100);
}

test('the logged block folds the live one with the whole text, and the end after it adds nothing', async () => {
  const app = await load();
  app.sse.emit({ type: 'thinking.delta', data: { text: 'the first half, then', fold: true } });
  app.sse.emit({ seq: 3, type: 'thinking.block', data: { text: 'the first half, then the second half', elapsed_ms: 4200 } });
  app.sse.emit({ type: 'thinking.end', data: { fold: true, elapsed_ms: 4200 } });
  const blocks = foldBlocks(app);
  assert.equal(blocks.length, 1, 'the end after the logged block drew a second one');
  const [block] = blocks;
  assert.equal(block.classList.contains('live'), false);
  assert.equal(block.querySelector('.body').textContent, 'the first half, then the second half');
  assert.equal(block.querySelector('.time').textContent, '4s');
  assert.ok(headerAgrees(block));
});

test('a replayed block is drawn folded, in front of an answer already started', async () => {
  const app = await load();
  app.sse.emit({ seq: 1, type: 'message.user', data: { text: 'q' } });
  app.sse.emit({ seq: 2, type: 'thinking.block', data: { text: 'kept from before', elapsed_ms: 65000 } });
  app.sse.emit({ seq: 3, type: 'message.part.end', data: { text: 'the answer' } });
  const [block] = foldBlocks(app);
  assert.ok(block, 'the logged block was not drawn');
  assert.equal(block.classList.contains('live'), false);
  assert.equal(block.querySelector('.label').textContent, 'Thought for');
  assert.equal(block.querySelector('.time').textContent, '1m5s');
  assert.equal(block.querySelector('.body').hidden, true);
  assert.ok(headerAgrees(block));
  const kids = Array.from(app.el('transcript').children);
  const answer = kids.find((el) => el.classList.contains('msg-model'));
  assert.ok(kids.indexOf(block) < kids.indexOf(answer), 'the block is not in front of the answer');

  block.querySelector('.head').click();
  assert.equal(block.querySelector('.body').hidden, false, 'a replayed block does not open');
});

test('a replayed block goes in front of an answer that is still being written', async () => {
  const app = await load();
  app.sse.emit({ type: 'message.part.delta', data: { text: 'started ' } });
  app.sse.emit({ seq: 5, type: 'thinking.block', data: { text: 'late', elapsed_ms: 1000 } });
  const kids = Array.from(app.el('transcript').children);
  const block = kids.findIndex((el) => el.classList.contains('fold'));
  const answer = kids.findIndex((el) => el.classList.contains('msg-model'));
  assert.ok(block >= 0 && block < answer);
});

test('a replayed block is not drawn while show_thinking is off, nor for whitespace', async () => {
  const app = await load();
  app.applyEvent({ type: 'settings.changed', data: { show_thinking: false } });
  app.sse.emit({ seq: 1, type: 'thinking.block', data: { text: 'hidden', elapsed_ms: 1000 } });
  assert.equal(foldBlocks(app).length, 0);
  app.applyEvent({ type: 'settings.changed', data: { show_thinking: true } });
  app.sse.emit({ seq: 2, type: 'thinking.block', data: { text: ' \n ', elapsed_ms: 1000 } });
  assert.equal(foldBlocks(app).length, 0);
});

test('a find lands inside a replayed block and its header follows', async () => {
  const app = await load();
  app.sse.emit({ seq: 1, type: 'thinking.block', data: { text: 'look at handoff.go', elapsed_ms: 1000 } });
  app.sse.emit({ seq: 2, type: 'message.part.end', data: { text: 'done' } });
  await app.settle();
  app.doc.fire('keydown', { key: 'f', ctrlKey: true, target: app.document.body });
  app.el('find-input').value = 'handoff';
  app.el('find-input').fire('input');
  await app.settle();
  const [block] = foldBlocks(app);
  assert.equal(block.querySelector('.body').hidden, false, 'the find did not reach the replayed block');
  assert.ok(headerAgrees(block));
});

// A stream the browser gave up on (a reply that was not 200) is rebuilt by
// the page. It resumes after the last event drawn: asking for the tail
// again drew the whole tail a second time.
test('a rebuilt stream resumes after the last event it drew', async () => {
  const app = await load();
  app.sse.emit({ seq: 7, type: 'message.user', data: { text: 'only once' } });
  app.sse.emit({ seq: 8, type: 'message.part.end', data: { text: 'answered once' } });
  const first = app.sse;
  first.failFatally();
  await app.settle();
  await waitFor(app, () => app.streams.length > 1);
  const rebuilt = app.streams[app.streams.length - 1];
  assert.notEqual(rebuilt, first, 'the page did not rebuild the stream');
  assert.match(rebuilt.url, /[?&]since=8(&|$)/, `rebuilt with ${rebuilt.url}`);
  assert.doesNotMatch(rebuilt.url, /tail=/);
});

test('a stream opened for another conversation still starts at the tail', async () => {
  const app = await load();
  app.sse.emit({ seq: 7, type: 'message.user', data: { text: 'first' } });
  app.selectSession('s2', 'general-purpose', '');
  await app.settle();
  assert.match(app.sse.url, /tail=/, `opened with ${app.sse.url}`);
});

// The lost-turn check after a reconnect, the cases it must leave alone
// and the one it must not.
function lostTurnApp(busyRef) {
  return load({ routes: { ...defaultRoutes(), 'GET /api/sessions': () => [{ id: 'sess-1', title: 'one', busy: busyRef.busy }] } });
}

test('a prompt sent while the check waits keeps its turn', async () => {
  const ref = { busy: false };
  const app = await lostTurnApp(ref);
  app.el('input').value = 'first';
  app.el('send').click();
  await app.settle();
  app.sse.fail();
  await app.settle();
  app.sse.reopen();
  await app.settle();
  // The check has found the session idle and is waiting out its grace;
  // a new prompt goes out in that window.
  app.el('input').value = 'second';
  app.el('send').click();
  await app.settle();
  await app.wait(1300);
  assert.doesNotMatch(app.el('transcript').textContent, /did not finish/, 'the new prompt\'s turn was declared lost');
  assert.equal(app.state.waiting, true);
});

test('a watched turn lost across a reconnect folds its block without a word', async () => {
  const ref = { busy: false };
  const app = await lostTurnApp(ref);
  app.sse.emit({ type: 'thinking.delta', data: { text: 'someone else\'s turn', fold: true } });
  app.sse.fail();
  await app.settle();
  app.sse.reopen();
  await app.settle();
  const [block] = foldBlocks(app);
  await waitFor(app, () => !block.classList.contains('live'));
  assert.equal(block.classList.contains('live'), false, 'the watched block is still live');
  assert.doesNotMatch(app.el('transcript').textContent, /did not finish/);
});

test('a lost turn stops its tool rows and marks what was sent into it', async () => {
  // Idle at load, or the composer would queue the prompt behind the turn.
  const ref = { busy: false };
  const app = await lostTurnApp(ref);
  app.el('input').value = 'first';
  app.el('send').click();
  await app.settle();
  ref.busy = true;
  app.sse.emit({ seq: 1, type: 'message.user', data: { text: 'first' } });
  app.sse.emit({ seq: 2, type: 'tool.start', data: { tool_use_id: 't1', name: 'bash', input: '{"command":"make"}' } });
  app.el('input').value = 'and also this';
  app.el('send').click();
  await app.settle();
  app.sse.fail();
  await app.settle();
  ref.busy = false;
  app.sse.reopen();
  await app.settle();
  await waitFor(app, () => /did not finish/.test(app.el('transcript').textContent));
  const text = app.el('transcript').textContent;
  assert.match(text, /did not finish/);
  assert.doesNotMatch(app.el('transcript').querySelector('.msg-toolcall').className, /running/, 'the tool row still spins');
  assert.match(text, /not sent/, 'what was sent into the lost turn still reads as sent');
});
