'use strict';

// The muse reasoning block.
//
// A stream the daemon marks "fold" (fold_thinking on, a muse model) is
// drawn as a block of its own: a header saying Thinking with its running
// time, the text under it, and the whole folded to the header once the
// answer starts. Everything else keeps the plain muted block. The Muse
// tab in the settings window holds the switch.

const test = require('node:test');
const assert = require('node:assert/strict');

const fs = require('node:fs');
const path = require('node:path');

const { load } = require('./harness');

function foldBlocks(app) {
  return Array.from(app.el('transcript').querySelectorAll('.msg-thinking'))
    .filter((el) => el.classList.contains('fold'));
}

test('a muse model\'s reasoning is a labelled block that folds when its end arrives', async () => {
  const app = await load();
  app.sse.emit({ type: 'thinking.delta', data: { text: 'The user asks 17 times 23. ', fold: true } });
  app.sse.emit({ type: 'thinking.delta', data: { text: 'That is 391.', fold: true } });

  const blocks = foldBlocks(app);
  assert.equal(blocks.length, 1, 'two deltas of one block drew two blocks');
  const block = blocks[0];
  const head = block.querySelector('.head');
  const body = block.querySelector('.body');
  assert.equal(head.tagName.toLowerCase(), 'button', 'the header is not a control the keyboard can reach');
  assert.equal(block.querySelector('.label').textContent, 'Thinking');
  assert.ok(block.classList.contains('live'));
  assert.equal(body.hidden, false, 'a streaming block hides what is arriving');
  assert.equal(body.textContent, 'The user asks 17 times 23. That is 391.');
  assert.equal(head.getAttribute('aria-expanded'), 'true');

  app.sse.emit({ type: 'thinking.end', data: { fold: true, elapsed_ms: 12400 } });
  app.sse.emit({ type: 'message.part.delta', data: { text: '17 times 23 is 391.' } });

  assert.equal(block.classList.contains('live'), false);
  assert.equal(block.querySelector('.label').textContent, 'Thought for');
  assert.equal(block.querySelector('.time').textContent, '12s', 'the daemon\'s time was not used');
  assert.equal(body.hidden, true, 'the block did not fold');
  assert.equal(head.getAttribute('aria-expanded'), 'false');
  assert.equal(block.querySelector('.marker').textContent, '▸');
  // The answer is its own element after the block, not inside it.
  const answer = app.el('transcript').querySelectorAll('.msg-model');
  assert.equal(answer.length, 1);
  assert.match(answer[0].textContent, /17 times 23 is 391/);
});

test('clicking the folded line opens the reasoning and closes it again', async () => {
  const app = await load();
  app.sse.emit({ type: 'thinking.delta', data: { text: 'working it out', fold: true } });
  app.sse.emit({ type: 'thinking.end', data: { fold: true, elapsed_ms: 65000 } });
  const [block] = foldBlocks(app);
  const head = block.querySelector('.head');
  const body = block.querySelector('.body');
  assert.equal(block.querySelector('.time').textContent, '1m5s');

  head.click();
  assert.equal(body.hidden, false);
  assert.equal(head.getAttribute('aria-expanded'), 'true');
  assert.equal(block.querySelector('.marker').textContent, '▾');
  head.click();
  assert.equal(body.hidden, true);

  // A find that landed on a match opened the body behind the header's
  // back; the next click follows what is on screen, and folds it.
  body.hidden = false;
  head.click();
  assert.equal(body.hidden, true, 'the click did the opposite of what the reader sees');
});

test('a block whose end never came folds when the answer, a tool or the turn\'s end arrives', async () => {
  const closers = {
    answer: { type: 'message.part.delta', data: { text: 'ok' } },
    tool: { type: 'tool.start', data: { tool_use_id: 't1', name: 'glob', input: '{}' } },
    done: { type: 'turn.done', data: {} },
    cancelled: { type: 'turn.cancelled', data: {} },
    error: { type: 'error', data: { error: 'boom' } },
  };
  for (const [name, ev] of Object.entries(closers)) {
    const app = await load();
    app.sse.emit({ type: 'thinking.delta', data: { text: 'hmm', fold: true } });
    app.sse.emit(ev);
    const [block] = foldBlocks(app);
    assert.equal(block.classList.contains('live'), false, `${name} left the block streaming`);
    assert.equal(block.querySelector('.body').hidden, true, `${name} did not fold the block`);
    assert.equal(block.querySelector('.label').textContent, 'Thought for', name);
  }
});

test('a long block\'s time reads the way the TUI says it', async () => {
  const app = await load();
  app.sse.emit({ type: 'thinking.delta', data: { text: 'a long think', fold: true } });
  app.sse.emit({ type: 'thinking.end', data: { fold: true, elapsed_ms: 3_725_000 } });
  const [block] = foldBlocks(app);
  assert.equal(block.querySelector('.time').textContent, '1h2m5s');
});

test('each request\'s reasoning is its own block', async () => {
  const app = await load();
  app.sse.emit({ type: 'thinking.delta', data: { text: 'first', fold: true } });
  app.sse.emit({ type: 'thinking.end', data: { fold: true, elapsed_ms: 1000 } });
  app.sse.emit({ type: 'tool.start', data: { tool_use_id: 't1', name: 'glob', input: '{}' } });
  app.sse.emit({ type: 'thinking.delta', data: { text: 'second', fold: true } });
  const blocks = foldBlocks(app);
  assert.equal(blocks.length, 2);
  assert.equal(blocks[0].querySelector('.body').textContent, 'first');
  assert.equal(blocks[1].querySelector('.body').textContent, 'second');
  assert.ok(blocks[1].classList.contains('live'));
});

test('reasoning not marked for folding keeps the plain block', async () => {
  const app = await load();
  app.sse.emit({ type: 'thinking.delta', data: { text: 'weighing it up', fold: false } });
  app.sse.emit({ type: 'thinking.end', data: { fold: false, elapsed_ms: 3000 } });
  app.sse.emit({ type: 'message.part.delta', data: { text: 'answer' } });
  assert.equal(foldBlocks(app).length, 0);
  const plain = app.el('transcript').querySelectorAll('.msg-thinking');
  assert.equal(plain.length, 1);
  assert.equal(plain[0].textContent, 'weighing it up', 'the plain block gained a label');
});

test('show_thinking off hides a muse block as it hides the plain one', async () => {
  const app = await load();
  app.applyEvent({ type: 'settings.changed', data: { show_thinking: false } });
  app.sse.emit({ type: 'thinking.delta', data: { text: 'not drawn', fold: true } });
  app.sse.emit({ type: 'thinking.end', data: { fold: true, elapsed_ms: 1000 } });
  assert.equal(foldBlocks(app).length, 0);
  assert.doesNotMatch(app.transcript(), /not drawn/);
});

test('switching conversations drops the block that was streaming', async () => {
  const app = await load();
  app.sse.emit({ type: 'thinking.delta', data: { text: 'old', fold: true } });
  app.selectSession('s2', 'general-purpose', '');
  await app.settle();
  app.sse.emit({ type: 'thinking.delta', data: { text: 'new', fold: true } });
  const blocks = foldBlocks(app);
  assert.equal(blocks.length, 1);
  assert.equal(blocks[0].querySelector('.body').textContent, 'new', 'the new block wrote into the old one');
});

// The Muse tab.

test('the Muse tab holds the reasoning switch and keep going', async () => {
  const app = await load();
  app.el('settings-btn').click();
  await app.settle();
  app.el('settings-tab-muse').click();
  assert.equal(app.el('settings-panel-muse').hidden, false);
  // Located in the shipped markup, the way settings_tabs.test.js locates
  // every control: inside the Muse panel's section, and nowhere else.
  const html = fs.readFileSync(path.join(__dirname, '..', '..', 'internal', 'daemon', 'static', 'index.html'), 'utf8');
  const start = html.indexOf('<section role="tabpanel" id="settings-panel-muse"');
  const end = html.indexOf('</section>', start);
  assert.ok(start >= 0 && end > start, 'index.html has no Muse panel');
  for (const id of ['fold-thinking-checkbox', 'keep-going-checkbox', 'muse-note']) {
    const at = html.indexOf(`id="${id}"`);
    assert.ok(at > start && at < end, `${id} is not on the Muse tab`);
  }
  assert.equal(app.el('fold-thinking-checkbox').checked, true, 'the switch defaults to on');
});

test('the Muse tab says which profiles run a muse model, and says it when none do', async () => {
  let app = await load();
  app.el('settings-btn').click();
  await app.settle();
  assert.match(app.el('muse-note').textContent, /no profile in this config runs one/);

  app = await load({ routes: { 'GET /api/settings': { muse_profiles: ['glimmer', 'spark'], fold_thinking: true } } });
  await app.settle();
  app.el('settings-btn').click();
  await app.settle();
  assert.match(app.el('muse-note').textContent, /Profiles on one: glimmer, spark\./);
});

test('the reasoning checkbox posts the change and follows a change made elsewhere', async () => {
  const app = await load({ routes: { 'POST /api/settings/fold-thinking': { status: 204 } } });
  await app.settle();
  app.el('settings-btn').click();
  await app.settle();

  const box = app.el('fold-thinking-checkbox');
  box.checked = false;
  box.fire('change');
  await app.settle();
  const calls = app.callsTo('POST', '/api/settings/fold-thinking');
  assert.equal(calls.length, 1);
  assert.deepEqual(calls[0].body, { enabled: false });
  assert.equal(app.state.foldThinking, false);
  assert.match(app.el('fold-thinking-note').textContent, /^Off\./);

  app.applyEvent({ type: 'settings.changed', data: { fold_thinking: true } });
  assert.equal(box.checked, true, 'the open panel shows a stale box');

  // With reasoning hidden altogether the note says so, since the box
  // then changes nothing on screen.
  app.applyEvent({ type: 'settings.changed', data: { show_thinking: false } });
  assert.match(app.el('fold-thinking-note').textContent, /show_thinking is off/);
});

test('a refused change puts the box back and says why', async () => {
  const app = await load({ routes: { 'POST /api/settings/fold-thinking': { status: 500, body: 'disk full' } } });
  await app.settle();
  app.el('settings-btn').click();
  await app.settle();
  const box = app.el('fold-thinking-checkbox');
  box.checked = false;
  box.fire('change');
  await app.settle();
  assert.equal(box.checked, true);
  assert.equal(app.el('fold-thinking-warn').hidden, false);
  assert.match(app.el('fold-thinking-warn').textContent, /Not changed/);
});
