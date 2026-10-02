'use strict';

// The in-page dialog behind every question the page asks.
//
// window.prompt and window.confirm never resolve in the Mac desktop
// window, whose web view implements the file picker and none of the
// JavaScript dialog panels. These tests pin the replacement's contract:
// what it answers on OK, on Cancel and on Escape, and that a cancelled
// question leaves the page exactly as it was.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

// askText answers with what was typed. The current value is prefilled,
// so renaming starts from the name and typing replaces it.
test('answering a text question returns what was typed', async () => {
  const app = await load();

  const answered = app.askText('Rename session', 'New session name:', 'original', 'Rename');
  await app.settle();

  assert.equal(app.el('prompt-title').textContent, 'Rename session');
  assert.equal(app.el('prompt-message').textContent, 'New session name:');
  assert.equal(app.el('prompt-input').value, 'original');
  assert.equal(app.el('prompt-ok').textContent, 'Rename');

  app.el('prompt-input').value = 'renamed by hand';
  app.el('prompt-ok').click();
  assert.equal(await answered, 'renamed by hand');
});

// Cancel answers null, the way window.prompt's cancel does: callers
// treat null as "do nothing", so nothing may have changed.
test('cancelling a text question answers null', async () => {
  const app = await load();

  const answered = app.askText('Rename session', 'New session name:', 'original');
  await app.settle();

  app.el('prompt-cancel').click();
  assert.equal(await answered, null);
});

// Escape answers the cancelled value too, and does not stop any turn:
// answering a question is not stopping the work behind it. The turn is
// made real first, so the assertion means something: without a turn in
// flight cancelTurn is a no-op and the check would pass trivially.
test('Escape cancels a question without touching the turn', async () => {
  const app = await load();
  app.state.waiting = true;

  const textAnswered = app.askText('Rename session', 'New session name:', 'original');
  await app.settle();
  app.doc.fire('keydown', { key: 'Escape', target: app.el('prompt-input') });
  assert.equal(await textAnswered, null);
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/cancel').length, 0);

  const confirmAnswered = app.askConfirm('Delete session', 'Delete it?');
  await app.settle();
  app.doc.fire('keydown', { key: 'Escape', target: app.el('prompt-modal') });
  assert.equal(await confirmAnswered, false);
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/cancel').length, 0);
});

// Enter answers from the input, the way the native panel does.
test('Enter answers a text question from the input', async () => {
  const app = await load();

  const answered = app.askText('Rename session', 'New session name:', 'original');
  await app.settle();

  app.el('prompt-input').value = 'typed and entered';
  app.doc.fire('keydown', { key: 'Enter', target: app.el('prompt-input') });
  assert.equal(await answered, 'typed and entered');
});

// An empty string is a real answer, not a cancel: some callers clear on
// empty, the way window.prompt's empty string clears.
test('an empty answer is an answer, not a cancel', async () => {
  const app = await load();

  const answered = app.askText('Rename task', 'Name (empty to clear):', 'nightly');
  await app.settle();

  app.el('prompt-input').value = '';
  app.el('prompt-ok').click();
  assert.equal(await answered, '');
});

// The OK button names the action it takes. Renames say Rename and group
// creation says Create because the caller says so; anything else falls
// back to a plain OK rather than a verb from another dialog.
test('the OK button names the action, defaulting to a plain OK', async () => {
  const app = await load();

  let answered = app.askText('New group', 'New group name:', '', 'Create');
  await app.settle();
  assert.equal(app.el('prompt-ok').textContent, 'Create');
  app.el('prompt-cancel').click();
  assert.equal(await answered, null);

  answered = app.askText('Anything', 'Anything:');
  await app.settle();
  assert.equal(app.el('prompt-ok').textContent, 'OK');
  app.el('prompt-cancel').click();
  assert.equal(await answered, null);
});

// askConfirm answers true on OK and false on Cancel. The danger flag
// marks the OK button red for answers that cannot be undone.
test('a confirm answers yes or no, and danger marks the button', async () => {
  const app = await load();

  const yes = app.askConfirm('Delete session', 'Delete it? This cannot be undone.', { okLabel: 'Delete', danger: true });
  await app.settle();

  assert.equal(app.el('prompt-title').textContent, 'Delete session');
  assert.equal(app.el('prompt-message').textContent, 'Delete it? This cannot be undone.');
  assert.equal(app.el('prompt-ok').textContent, 'Delete');
  assert.ok(app.el('prompt-ok').className.includes('danger-btn'));
  app.el('prompt-ok').click();
  assert.equal(await yes, true);

  const no = app.askConfirm('Delete session', 'Delete it?');
  await app.settle();
  assert.ok(!app.el('prompt-ok').className.includes('danger-btn'));
  app.el('prompt-cancel').click();
  assert.equal(await no, false);
});

// Line breaks survive: several confirmations carry a second paragraph
// past a blank line, and a message that lost them would read as one run
// of text.
test('a multi-line message keeps its line breaks', async () => {
  const app = await load();

  app.askConfirm('Install?', 'Download and install?\n\nIt restarts.');
  await app.settle();

  assert.match(app.el('prompt-message').textContent, /install\?\n\nIt restarts/);
  app.el('prompt-cancel').click();
  await app.settle();
});

// Only one question is ever on screen. A second call while one is open
// gets the cancelled answer: the caller treats that as "do nothing",
// which is the only safe answer for a question nobody saw.
test('a second question while one is open is answered cancelled', async () => {
  const app = await load();

  const first = app.askText('First', 'First?');
  await app.settle();
  assert.equal(await app.askText('Second', 'Second?'), null);
  assert.equal(await app.askConfirm('Third', 'Third?'), false);

  // The first question is still the one on screen, unanswered.
  assert.equal(app.el('prompt-title').textContent, 'First');
  app.el('prompt-cancel').click();
  assert.equal(await first, null);
});
