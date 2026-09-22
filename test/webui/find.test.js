'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

// A note on what these can and cannot see.
//
// The test DOM has no HTML parser, so a model reply — which the page draws
// by assigning rendered markdown to innerHTML — arrives as one opaque node
// with no text nodes inside it. Marking splits text nodes, so a reply
// cannot be marked here, and the text a reply reports includes its own
// markup. Every assertion about matched characters therefore uses the
// blocks the page builds out of text nodes (prompts, tool rows, errors),
// which is also where markRange's real work is: a range crossing several
// text nodes. The multi-node case is built explicitly below rather than
// waited for.

function keys(app) {
  return {
    ctrlF: () => app.fireWindow
      ? app.fire('keydown', { key: 'f', ctrlKey: true })
      : null,
  };
}

test('findMatches walks the newest message first, and the last occurrence within it', async () => {
  const app = await load({});
  const { findMatches } = app.internals;

  // Three messages, drawn oldest first, as the transcript holds them.
  const texts = ['alpha one', 'beta alpha', 'alpha alpha'];
  const got = findMatches(texts, 'alpha');

  // Compared as text: findMatches runs inside the page's own module
  // realm, so the arrays it returns do not share this file's Array
  // prototype and a strict deep-equal fails on that alone.
  assert.equal(
    got.map((m) => `${m.block}:${m.start}-${m.end}`).join(' '),
    '2:6-11 2:0-5 1:5-10 0:0-5',
    'newest block first; inside a block the last occurrence first',
  );
});

test('findMatches is case-insensitive and counts non-overlapping matches', async () => {
  const app = await load({});
  const { findMatches } = app.internals;

  assert.equal(findMatches(['Alpha ALPHA alpha'], 'alpha').length, 3, 'case is ignored');
  assert.equal(findMatches(['aaaa'], 'aa').length, 2, 'aa in aaaa is two matches, not three');
  assert.equal(findMatches(['anything'], '').length, 0, 'an empty query matches nothing');
  assert.equal(findMatches([], 'x').length, 0, 'no messages, no matches');
  assert.equal(findMatches([null, undefined], 'x').length, 0, 'a block with no text is not an error');
});

test('markRange wraps a match that crosses several text nodes, and unmark puts it back', async () => {
  const app = await load({});
  const { markRange, unmark } = app.internals;
  const doc = app.document;

  // `a **b** c` once rendered: three text nodes with an element between
  // them. The word "bc" spans the element boundary.
  const block = doc.createElement('div');
  block.appendChild(doc.createTextNode('ab'));
  const strong = doc.createElement('strong');
  strong.appendChild(doc.createTextNode('cd'));
  block.appendChild(strong);
  block.appendChild(doc.createTextNode('ef'));
  assert.equal(block.textContent, 'abcdef');

  // "bcde" starts in the first node, covers the element's whole text and
  // ends in the last.
  const marks = markRange(block, 1, 5, 'find-hit');
  assert.equal(marks.length, 3, 'one mark per node the range covers');
  assert.equal(marks.map((m) => m.textContent).join(''), 'bcde', 'together they are the match');
  assert.equal(block.textContent, 'abcdef', 'and the text is unchanged by being marked');

  for (const m of marks) unmark(m);
  assert.equal(block.textContent, 'abcdef', 'unmarking leaves the text as it was');
  assert.equal(
    block.innerHTML.includes('mark'), false,
    'and leaves no mark elements behind',
  );
});

test('markRange builds elements rather than HTML, so a match inside markup-looking text stays text', async () => {
  const app = await load({});
  const { markRange } = app.internals;
  const doc = app.document;

  const block = doc.createElement('div');
  block.textContent = 'run <script>alert(1)</script> now';
  markRange(block, 4, 12, 'find-hit');

  assert.equal(block.textContent, 'run <script>alert(1)</script> now', 'the characters are the same');
  assert.ok(
    block.innerHTML.includes('&lt;script&gt;'),
    'and they are still escaped in the output: ' + block.innerHTML,
  );
});

// --- the bar, driven the way a person drives it ---

// Four prompts, so the transcript has blocks built from text nodes. Each
// is one searchable block; the separators above them are not.
async function conversation() {
  const app = await load();
  app.sse.emit({ seq: 1, type: 'message.user', data: { text: 'read handoff.go' } });
  app.sse.emit({ seq: 2, type: 'message.user', data: { text: 'the handoff again' } });
  app.sse.emit({ seq: 3, type: 'message.user', data: { text: 'nothing here' } });
  app.sse.emit({ seq: 4, type: 'message.user', data: { text: 'handoff, handoff' } });
  await app.settle();
  return app;
}

function marks(app) {
  return app.el('transcript').querySelectorAll('.find-hit');
}

function currentMark(app) {
  return Array.from(marks(app)).find((m) => m.className.includes('current'));
}

test('Ctrl+F opens the bar and the first match is the newest one', async () => {
  const app = await conversation();

  assert.equal(app.el('find-bar').hidden, true, 'the bar is away until it is asked for');
  app.doc.fire('keydown', { key: 'f', ctrlKey: true, target: app.document.body });
  assert.equal(app.el('find-bar').hidden, false, 'Ctrl+F opened it');

  app.el('find-input').value = 'handoff';
  app.el('find-input').fire('input');
  await app.settle();

  // Four occurrences: two in the last prompt, one in the second, one in
  // the first.
  assert.equal(marks(app).length, 4, 'every occurrence is marked');
  assert.equal(app.el('find-count').textContent, '1 of 4');

  // The newest block is the last prompt, and within it the later of its
  // two occurrences. The whole transcript's text after that match is
  // nothing, which is what makes it the newest.
  const block = currentMark(app).parentNode;
  assert.equal(block.textContent, 'handoff, handoff', 'the current match is in the newest prompt');
  assert.equal(
    block.textContent.slice(block.textContent.lastIndexOf('handoff')),
    'handoff',
    'and it is that prompt\'s last occurrence',
  );
});

test('older walks back in time, newer comes forward, and both wrap', async () => {
  const app = await conversation();
  app.doc.fire('keydown', { key: 'f', ctrlKey: true, target: app.document.body });
  app.el('find-input').value = 'handoff';
  app.el('find-input').fire('input');
  await app.settle();

  const where = () => currentMark(app).parentNode.textContent;

  assert.equal(app.el('find-count').textContent, '1 of 4');
  assert.equal(where(), 'handoff, handoff');

  app.el('find-older').fire('click');
  assert.equal(app.el('find-count').textContent, '2 of 4');
  assert.equal(where(), 'handoff, handoff', 'still the newest prompt, its earlier occurrence');

  app.el('find-older').fire('click');
  assert.equal(app.el('find-count').textContent, '3 of 4');
  assert.equal(where(), 'the handoff again', 'then the prompt before it');

  app.el('find-older').fire('click');
  assert.equal(app.el('find-count').textContent, '4 of 4');
  assert.equal(where(), 'read handoff.go', 'then the oldest');

  // Wrapping rather than stopping: a find that goes dead at the end of a
  // long conversation reads as broken.
  app.el('find-older').fire('click');
  assert.equal(app.el('find-count').textContent, '1 of 4', 'past the oldest comes back to the newest');

  app.el('find-newer').fire('click');
  assert.equal(app.el('find-count').textContent, '4 of 4', 'and newer from the newest wraps the other way');
});

test('Enter is older, Shift+Enter is newer', async () => {
  const app = await conversation();
  app.doc.fire('keydown', { key: 'f', ctrlKey: true, target: app.document.body });
  const input = app.el('find-input');
  input.value = 'handoff';
  input.fire('input');
  await app.settle();

  input.fire('keydown', { key: 'Enter', target: input });
  assert.equal(app.el('find-count').textContent, '2 of 4');
  input.fire('keydown', { key: 'Enter', shiftKey: true, target: input });
  assert.equal(app.el('find-count').textContent, '1 of 4');
});

test('a word that is not there says so, and marks nothing', async () => {
  const app = await conversation();
  app.doc.fire('keydown', { key: 'f', ctrlKey: true, target: app.document.body });
  app.el('find-input').value = 'kubernetes';
  app.el('find-input').fire('input');
  await app.settle();

  assert.equal(app.el('find-count').textContent, 'no matches');
  assert.equal(marks(app).length, 0);
});

test('closing the bar puts every character back', async () => {
  const app = await conversation();
  const before = app.el('transcript').textContent;

  app.doc.fire('keydown', { key: 'f', ctrlKey: true, target: app.document.body });
  app.el('find-input').value = 'handoff';
  app.el('find-input').fire('input');
  await app.settle();
  assert.equal(marks(app).length, 4);

  app.el('find-close').fire('click');
  assert.equal(app.el('find-bar').hidden, true);
  assert.equal(marks(app).length, 0, 'no marks left behind');
  assert.equal(app.el('transcript').textContent, before, 'and the transcript reads as it did');
});

// Escape has three claimants: the prompt box clears itself, a running turn
// is cancelled, and now the bar closes. The bar wins while it is open,
// except in the box.
test('Escape closes the bar instead of cancelling the turn', async () => {
  const app = await conversation();
  app.doc.fire('keydown', { key: 'f', ctrlKey: true, target: app.document.body });
  app.el('find-input').value = 'handoff';
  app.el('find-input').fire('input');
  await app.settle();

  const cancels = () => app.callsTo('POST', '/api/sessions/s1/cancel').length;
  const before = cancels();
  app.doc.fire('keydown', { key: 'Escape', target: app.el('find-input') });
  await app.settle();

  assert.equal(app.el('find-bar').hidden, true, 'Escape closed the bar');
  assert.equal(cancels(), before, 'and did not cancel anything');
});

test('switching conversation closes the bar, because the matches were in the other one', async () => {
  const app = await conversation();
  app.doc.fire('keydown', { key: 'f', ctrlKey: true, target: app.document.body });
  app.el('find-input').value = 'handoff';
  app.el('find-input').fire('input');
  await app.settle();
  assert.equal(app.el('find-bar').hidden, false);

  app.selectSession('s2', 'general-purpose', '');
  await app.settle();
  assert.equal(app.el('find-bar').hidden, true, 'the bar went with the conversation');
  assert.equal(app.el('find-count').textContent, '', 'and left no count from it');
});

test('a match inside a folded tool result opens what it is folded into', async () => {
  const app = await load();
  app.sse.emit({ seq: 1, type: 'tool.start', data: { tool_use_id: 't1', name: 'bash', input: '{"command":"ls"}' } });
  app.sse.emit({ seq: 2, type: 'tool.end', data: { tool_use_id: 't1', content: 'handoff.go\nother.go' } });
  await app.settle();

  // The detail block a tool row keeps its output in starts folded.
  const row = app.el('transcript').querySelectorAll('.msg-toolcall')[0];
  const detail = row.querySelectorAll('.detail')[0];
  assert.ok(detail, 'the row has a detail block');
  assert.equal(detail.hidden, true, 'the output is folded to begin with');

  app.doc.fire('keydown', { key: 'f', ctrlKey: true, target: app.document.body });
  app.el('find-input').value = 'handoff.go';
  app.el('find-input').fire('input');
  await app.settle();

  const mark = currentMark(app);
  assert.ok(mark, 'the folded output was searched');
  for (let p = mark.parentNode; p && p !== app.el('transcript'); p = p.parentNode) {
    assert.equal(p.hidden, false, 'nothing between the match and the transcript is still folded');
  }
});

// A turn ending under an open bar has to search again — the reply was
// rewritten on every fragment, so the marks inside it are gone — and
// searching again has to unmark what is there first.
//
// It did not. The refresh emptied the hit list and then called the
// search, which unmarks by walking that list: the marks stayed in the
// transcript out of reach of the only thing that removes them, and the
// next pass marked over them. One more nested layer per turn, two of
// them claiming to be the current match, and the whole stack left behind
// when the bar closed.
test('a turn ending re-searches without marking over the old marks', async () => {
  const app = await conversation();
  app.doc.fire('keydown', { key: 'f', ctrlKey: true, target: app.document.body });
  app.el('find-input').value = 'handoff';
  app.el('find-input').fire('input');
  await app.settle();
  assert.equal(marks(app).length, 4, 'setup: four occurrences');

  app.el('find-older').fire('click');
  assert.equal(app.el('find-count').textContent, '2 of 4');

  app.sse.emit({ type: 'turn.done' });
  await app.settle();

  assert.equal(marks(app).length, 4, 'still four marks, not a second layer over the first');
  assert.equal(
    Array.from(marks(app)).filter((m) => m.className.includes('current')).length,
    1,
    'and one current match, not one per layer',
  );
  assert.equal(app.el('find-count').textContent, '2 of 4', 'the reader is left where they were');

  app.el('find-close').fire('click');
  assert.equal(marks(app).length, 0, 'and closing takes every mark with it');
});

// Every way a turn can end, not just the tidy one. A cancelled turn has
// added as much text as a finished one.
test('a cancelled turn refreshes the count too', async () => {
  const app = await conversation();
  app.doc.fire('keydown', { key: 'f', ctrlKey: true, target: app.document.body });
  app.el('find-input').value = 'handoff';
  app.el('find-input').fire('input');
  await app.settle();
  assert.equal(app.el('find-count').textContent, '1 of 4', 'setup: four matches');

  app.sse.emit({ seq: 5, type: 'tool.start', data: { tool_use_id: 't1', name: 'bash', input: '{"command":"ls"}' } });
  app.sse.emit({ seq: 6, type: 'tool.end', data: { tool_use_id: 't1', content: 'handoff in the output' } });
  await app.settle();
  app.sse.emit({ type: 'turn.cancelled' });
  await app.settle();

  // Six, not five: a tool result short enough to read is shown twice —
  // once as the row's own summary beside the tool name, and once in the
  // block that folds open. Both are on screen, so both are matches; a
  // find that showed one of them would be hiding the other.
  assert.equal(app.el('find-count').textContent, '1 of 6', 'the tool output is counted after the cancel');
});

// The banner the transcript draws when it has opened a long conversation
// at its end is furniture, like the turn separator. Searching it matches
// text nobody wrote.
test('the earlier-messages banner is not searched', async () => {
  const app = await load();
  // The banner appears when the first event of a connection carries a
  // sequence number past the first: there is history above this.
  app.sse.emit({ seq: 5, type: 'message.user', data: { text: 'read handoff.go' } });
  await app.settle();
  assert.ok(app.el('transcript').querySelectorAll('.msg-earlier')[0], 'setup: the banner is showing');

  app.doc.fire('keydown', { key: 'f', ctrlKey: true, target: app.document.body });
  app.el('find-input').value = 'conversation';
  app.el('find-input').fire('input');
  await app.settle();

  assert.equal(app.el('find-count').textContent, 'no matches', "the banner's own words are not a match");
  assert.equal(marks(app).length, 0);
});

test('closing the bar puts the focus back in the prompt box', async () => {
  const app = await conversation();
  app.doc.fire('keydown', { key: 'f', ctrlKey: true, target: app.document.body });
  assert.equal(app.document.activeElement.id, 'find-input', 'opening it moves the focus into it');

  app.el('find-close').fire('click');
  assert.equal(
    app.document.activeElement.id, 'input',
    'closing it hands the focus back, not to a field that is now hidden',
  );
});

// A refresh is a search of the transcript as it stands, so it has to run
// after whatever changed it. Calling it at the top of the cancelled-turn
// handler searched the transcript that handler was about to write — and
// the abandoned prompts and the [cancelled] line are exactly what a
// reader would be looking for.
test('the cancelled-turn refresh runs after the cancel has written its lines', async () => {
  const app = await load();
  app.sse.emit({ seq: 1, type: 'message.user', data: { text: 'run the tests' } });
  await app.settle();

  app.doc.fire('keydown', { key: 'f', ctrlKey: true, target: app.document.body });
  app.el('find-input').value = 'cancelled';
  app.el('find-input').fire('input');
  await app.settle();
  assert.equal(app.el('find-count').textContent, 'no matches', 'setup: nothing says cancelled yet');

  app.sse.emit({ type: 'turn.cancelled' });
  await app.settle();

  assert.ok(
    app.el('transcript').textContent.includes('[cancelled]'),
    'setup: the cancel wrote its line',
  );
  assert.equal(app.el('find-count').textContent, '1 of 1', 'and the refresh saw it');
});

// closeFind is called on every session switch, whether or not the bar was
// ever opened. Taking the focus there pulls it out of whatever the person
// was using to switch with.
test('switching conversation with the bar closed leaves the focus alone', async () => {
  const app = await conversation();
  app.el('session-filter').focus();
  assert.equal(app.document.activeElement.id, 'session-filter', 'setup: typing in the filter');

  app.selectSession('s2', 'general-purpose', '');
  await app.settle();

  assert.equal(
    app.document.activeElement.id, 'session-filter',
    'a bar that was never open did not reach for the focus',
  );
});

// A line the transcript draws outside a turn — a rewind, a clear, a
// compaction, a fork — is searched without anything having to name it.
//
// The refresh is asked for by the thing that draws the line rather than
// by a list of the handlers that draw them. That list existed and was
// wrong twice: three turn ends, one of which had the call before its own
// writes, and every one-off writer missed.
test('a line drawn outside a turn is searched without being named anywhere', async () => {
  const app = await conversation();
  app.doc.fire('keydown', { key: 'f', ctrlKey: true, target: app.document.body });
  app.el('find-input').value = 'handoff';
  app.el('find-input').fire('input');
  await app.settle();
  assert.equal(app.el('find-count').textContent, '1 of 4', 'setup: four matches');

  app.sse.emit({ type: 'rewound', data: { turn_text: 'handoff in the undone turn' } });
  await app.settle();

  assert.ok(
    app.el('transcript').textContent.includes('handoff in the undone turn'),
    'setup: the rewind drew its line',
  );
  assert.equal(app.el('find-count').textContent, '1 of 5', 'and the bar counted it');
});

// A tool row is drawn the ordinary way, so it is announced the ordinary
// way — no handler names it anywhere.
test('a tool row is counted as soon as it is drawn', async () => {
  const app = await conversation();
  app.doc.fire('keydown', { key: 'f', ctrlKey: true, target: app.document.body });
  app.el('find-input').value = 'handoff';
  app.el('find-input').fire('input');
  await app.settle();
  assert.equal(app.el('find-count').textContent, '1 of 4', 'setup: four matches');

  app.sse.emit({ seq: 5, type: 'tool.start', data: { tool_use_id: 't1', name: 'bash', input: '{"command":"cat handoff.go"}' } });
  await app.settle();

  // Six: the row carries the command twice, beside the tool name and in
  // the arguments that fold open, the same way a short result is. Both
  // are on screen, so both count.
  assert.equal(app.el('find-count').textContent, '1 of 6', 'the row was counted without being named anywhere');
});

// The backstop, for the two writers that are deliberately silent: a reply
// and its thinking, each rewritten once per fragment. Searching the whole
// conversation per token is the one cost worth avoiding, so those say
// nothing — and a step that finds the transcript a different shape than
// the search remembers searches again.
//
// A mark leaving the tree is the other half of the same check. Text
// arriving takes nothing away, which is why it needed its own answer.
test('a step repairs a search the streamed reply has outgrown', async () => {
  const app = await conversation();
  app.doc.fire('keydown', { key: 'f', ctrlKey: true, target: app.document.body });
  app.el('find-input').value = 'handoff';
  app.el('find-input').fire('input');
  await app.settle();
  assert.equal(app.el('find-count').textContent, '1 of 4');

  app.sse.emit({ seq: 5, type: 'message.part.delta', data: { text: 'the handoff again, ' } });
  await app.settle();
  assert.equal(app.el('find-count').textContent, '1 of 4', 'a fragment says nothing, by design');

  app.el('find-older').fire('click');
  assert.equal(app.el('find-count').textContent, '1 of 5', 'the step found the transcript had grown and searched again');
});
