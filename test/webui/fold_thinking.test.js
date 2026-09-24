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

const { load, defaultRoutes } = require('./harness');

function openFind(app, query) {
  app.doc.fire('keydown', { key: 'f', ctrlKey: true, target: app.document.body });
  app.el('find-input').value = query;
  app.el('find-input').fire('input');
}

// headerAgrees reports whether a block's header says what its body shows.
function headerAgrees(block) {
  const open = !block.querySelector('.body').hidden;
  return block.querySelector('.head').getAttribute('aria-expanded') === String(open) &&
    block.querySelector('.marker').textContent === (open ? '▾' : '▸');
}

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
});

// The find bar and a fold block. A find that lands on a match inside the
// folded text opens it, and the header has to say so; a click on the
// header with the bar open has to stay done; and neither may reopen
// something else the reader had closed.
test('a find opens a folded block with its header, and a click folds it again', async () => {
  const app = await load();
  app.sse.emit({ type: 'thinking.delta', data: { text: 'look at handoff.go first', fold: true } });
  app.sse.emit({ type: 'thinking.end', data: { fold: true, elapsed_ms: 2000 } });
  app.sse.emit({ type: 'message.part.delta', data: { text: 'The answer.' } });
  app.sse.emit({ type: 'turn.done', data: {} });
  await app.settle();
  const [block] = foldBlocks(app);
  assert.equal(block.querySelector('.body').hidden, true);

  openFind(app, 'handoff');
  await app.settle();
  assert.equal(block.querySelector('.body').hidden, false, 'the find did not open the block its match is in');
  assert.ok(headerAgrees(block), 'the header says folded while the text shows');

  block.querySelector('.head').click();
  await app.settle();
  assert.equal(block.querySelector('.body').hidden, true, 'the click was undone by the open find bar');
  assert.ok(headerAgrees(block));
});

test('a block folding while the find bar is open keeps its header true', async () => {
  const app = await load();
  app.sse.emit({ type: 'thinking.delta', data: { text: 'look at handoff.go first', fold: true } });
  openFind(app, 'handoff');
  await app.settle();
  app.sse.emit({ type: 'message.part.delta', data: { text: 'The answer.' } });
  await app.settle();
  const [block] = foldBlocks(app);
  assert.ok(headerAgrees(block), 'the header and the text disagree after the fold');
  app.sse.emit({ type: 'turn.done', data: {} });
  await app.settle();
  assert.ok(headerAgrees(block), 'the header and the text disagree after the turn ended');
});

test('a click on a block\'s header does not reopen a tool row the reader closed', async () => {
  const app = await load();
  app.sse.emit({ type: 'tool.start', data: { tool_use_id: 't1', name: 'read_file', input: '{"path":"handoff.go"}' } });
  app.sse.emit({ type: 'tool.end', data: { tool_use_id: 't1', content: 'package handoff.go', is_error: false } });
  app.sse.emit({ type: 'thinking.delta', data: { text: 'nothing to find here', fold: true } });
  app.sse.emit({ type: 'thinking.end', data: { fold: true, elapsed_ms: 1000 } });
  app.sse.emit({ type: 'turn.done', data: {} });
  await app.settle();
  openFind(app, 'handoff.go');
  await app.settle();
  const detail = app.el('transcript').querySelectorAll('.detail')[0];
  assert.equal(detail.hidden, false, 'the find did not open the tool row');
  app.el('transcript').querySelectorAll('.msg-toolcall')[0].querySelector('.head').click();
  assert.equal(detail.hidden, true);

  foldBlocks(app)[0].querySelector('.head').click();
  await app.settle();
  assert.equal(detail.hidden, true, 'clicking the reasoning header reopened the tool row');
});

test('the clock runs between deltas', async () => {
  const app = await load();
  app.sse.emit({ type: 'thinking.delta', data: { text: 'hm', fold: true } });
  const [block] = foldBlocks(app);
  assert.equal(block.querySelector('.time').textContent, '0s');
  // Waited on rather than slept past: the tick lands when the scheduler
  // lets it, and a fixed sleep races it.
  for (let i = 0; i < 40 && block.querySelector('.time').textContent === '0s'; i++) await app.wait(100);
  assert.notEqual(block.querySelector('.time').textContent, '0s', 'the clock stood still with no delta arriving');
  app.sse.emit({ type: 'thinking.end', data: { fold: true, elapsed_ms: 7000 } });
  await app.wait(1200);
  assert.equal(block.querySelector('.time').textContent, '7s', 'the clock went on after the block folded');
});

test('a new prompt closes a block its turn left open', async () => {
  const app = await load();
  app.sse.emit({ type: 'thinking.delta', data: { text: 'OLD TURN', fold: true } });
  app.sse.emit({ type: 'message.user', data: { text: 'second question' } });
  app.sse.emit({ type: 'thinking.delta', data: { text: 'NEW TURN', fold: true } });
  const blocks = foldBlocks(app);
  assert.equal(blocks.length, 2);
  assert.equal(blocks[0].querySelector('.body').textContent, 'OLD TURN');
  assert.equal(blocks[0].classList.contains('live'), false);
  assert.equal(blocks[1].querySelector('.body').textContent, 'NEW TURN');
});

test('a reply that arrives as its end alone folds the block', async () => {
  const app = await load();
  app.sse.emit({ type: 'thinking.delta', data: { text: 'first request', fold: true } });
  app.sse.emit({ type: 'message.part.end', data: { text: 'Answer one.' } });
  const [block] = foldBlocks(app);
  assert.equal(block.classList.contains('live'), false);
  assert.equal(block.querySelector('.body').hidden, true);
});

test('whitespace alone opens no block', async () => {
  const app = await load();
  app.sse.emit({ type: 'thinking.delta', data: { text: '\n\n', fold: true } });
  assert.equal(foldBlocks(app).length, 0);
  app.sse.emit({ type: 'thinking.delta', data: { text: 'real text', fold: true } });
  const blocks = foldBlocks(app);
  assert.equal(blocks.length, 1);
  assert.equal(blocks[0].querySelector('.body').textContent, 'real text');
});

test('a turn lost on reconnect folds its block, and the next turn gets its own', async () => {
  let busy = false;
  const app = await load({
    routes: { ...defaultRoutes(), 'GET /api/sessions': () => [{ id: 'sess-1', title: 'one', busy }] },
  });
  app.el('input').value = 'first question';
  app.el('send').click();
  await app.settle();
  busy = true;
  app.sse.emit({ type: 'message.user', data: { text: 'first question' } });
  app.sse.emit({ type: 'thinking.delta', data: { text: 'OLD TURN reasoning. ', fold: true } });
  app.sse.fail();
  await app.settle();
  busy = false;
  app.sse.reopen();
  await app.settle();
  for (let i = 0; i < 40 && !/did not finish/.test(app.el('transcript').textContent); i++) await app.wait(100);
  assert.match(app.el('transcript').textContent, /did not finish/);
  const [old] = foldBlocks(app);
  assert.equal(old.classList.contains('live'), false, 'the lost turn\'s block still says it is thinking');

  app.sse.emit({ type: 'message.user', data: { text: 'second question' } });
  app.sse.emit({ type: 'thinking.delta', data: { text: 'NEW TURN reasoning.', fold: true } });
  const blocks = foldBlocks(app);
  assert.equal(blocks.length, 2, 'the new turn\'s reasoning was written into the old block');
  assert.equal(old.querySelector('.body').textContent, 'OLD TURN reasoning. ');
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

// A model that reasons after it has started answering: the block goes in
// front of the answer, as the TUI puts it, and the answer is one element.
test('reasoning after the answer started goes in front of it', async () => {
  const app = await load();
  app.sse.emit({ type: 'message.part.delta', data: { text: 'Part one. ' } });
  app.sse.emit({ type: 'thinking.delta', data: { text: 'reconsider', fold: true } });
  app.sse.emit({ type: 'thinking.end', data: { fold: true, elapsed_ms: 1000 } });
  app.sse.emit({ type: 'message.part.delta', data: { text: 'Part two.' } });
  app.sse.emit({ type: 'message.part.end', data: { text: 'Part one. Part two.' } });
  const kids = Array.from(app.el('transcript').children);
  const block = kids.findIndex((el) => el.classList.contains('fold'));
  const answers = kids.filter((el) => el.classList.contains('msg-model'));
  assert.equal(answers.length, 1, 'the answer was split');
  assert.ok(block >= 0 && block < kids.indexOf(answers[0]), 'the block is not in front of the answer');
  assert.match(answers[0].textContent, /Part one\. Part two\./);
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

// A save that failed is still a change that was made: the box keeps the
// value the daemon now has, and the warning survives the redraw the
// change's own settings.changed brings.
test('an applied but unsaved change keeps the box and the warning', async () => {
  const app = await load({ routes: { 'POST /api/settings/fold-thinking': {
    fold_thinking: false, applied: true, persisted: false, error: 'applied for this run, but failed to persist to config.json: disk full',
  } } });
  await app.settle();
  app.el('settings-btn').click();
  await app.settle();
  const box = app.el('fold-thinking-checkbox');
  box.checked = false;
  box.fire('change');
  await app.settle();
  assert.equal(box.checked, false, 'the box went back although the daemon applied the change');
  assert.equal(app.state.foldThinking, false);
  assert.match(app.el('fold-thinking-warn').textContent, /^Applied, but not saved/);
  app.applyEvent({ type: 'settings.changed', data: { fold_thinking: false } });
  assert.equal(app.el('fold-thinking-warn').hidden, false, 'the redraw erased the warning');
  assert.match(app.el('fold-thinking-warn').textContent, /disk full/);
});

test('a refused change puts the box back and says why', async () => {
  const app = await load({ routes: { 'POST /api/settings/fold-thinking': { status: 400, body: 'invalid request body' } } });
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
