'use strict';

// Finding your own turns in the transcript.
//
// The prompt used to be distinguished from the model's reply by font
// weight alone — 600 against 400, in the same column, with no boundary
// between turns. That is not a signal the eye catches while scrolling,
// and the CSS comment that chose it had already named the goal it was
// serving: "it needs to be findable when scrolling back".
//
// What these pin down is the shape that replaced it. A turn is a
// separator naming the speaker and a prompt with a rule down its left
// edge, and the two are created and removed as a pair — an orphaned
// separator is a boundary announcing a message that is not there.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

// separators is the boundary rows, by their text, so a test can assert
// on how many turns the transcript claims to have.
const separators = (app) =>
  Array.from(app.el('transcript').querySelectorAll('.turn-sep'), (el) => el.textContent);

test('a user turn is a separator naming the speaker and a prompt with no prefix', async () => {
  const app = await load();
  app.sse.emit({ seq: 1, type: 'message.user', data: { text: 'where does the schedule fire?' } });

  assert.deepEqual(app.userTurns(), ['where does the schedule fire?']);
  assert.deepEqual(separators(app), ['You']);
  // The name is on the separator now, not in front of the prompt: a
  // "You: " prefix indents the first line of a multi-line prompt and no
  // other line of it.
  assert.ok(!app.transcript().includes('You: '), app.transcript());
});

test('the separator of a pending prompt is taken down with it', async () => {
  const app = await load();
  app.type('hello');
  await app.el('send').click();
  await app.settle();

  // The optimistic echo: one turn, drawn dimmed until the daemon confirms.
  assert.deepEqual(separators(app), ['You']);
  assert.ok(app.transcript().includes('turn-sep pending'), app.transcript());

  app.sse.emit({ seq: 1, type: 'message.user', data: { text: 'hello' } });

  // One turn still, and nothing left marked pending. Before the separator
  // was removed alongside the prompt, this left two boundaries with one
  // message under them.
  assert.deepEqual(app.userTurns(), ['hello']);
  assert.deepEqual(separators(app), ['You']);
  assert.ok(!app.transcript().includes('pending'), app.transcript());
});

test('a prompt sent mid-turn is a note, not a turn of its own', async () => {
  const app = await load();
  // A turn already running: the prompt is queued rather than starting
  // one, and the transcript says so on a line of its own. That line is
  // not a turn boundary, so it gets no separator.
  app.sse.emit({ seq: 1, type: 'message.user', data: { text: 'first' } });
  app.setWaiting(true);
  app.type('and another thing');
  await app.el('send').click();
  await app.settle();

  assert.deepEqual(separators(app), ['You'], app.transcript());
});

// layOut gives every element in the transcript a position, since the
// fake DOM has no layout of its own: each turn 100 apart, the prompt 10
// below the separator that announces it.
function layOut(app) {
  const el = app.el('transcript');
  const seps = el.querySelectorAll('.turn-sep');
  const msgs = el.querySelectorAll('.msg-user');
  seps.forEach((s, i) => { s.offsetTop = i * 100; });
  msgs.forEach((m, i) => { m.offsetTop = i * 100 + 10; });
  return { el, seps, msgs };
}

test('the jump walks between your own turns, scrolling to the boundary', async () => {
  const app = await load();
  for (let i = 0; i < 3; i++) {
    app.sse.emit({ seq: i + 1, type: 'message.user', data: { text: `prompt ${i}` } });
  }
  const { el } = layOut(app);

  // Parked on the last turn, walking back.
  el.scrollTop = 210;
  assert.equal(app.jumpToTurn(-1), true);
  assert.equal(el.scrollTop, 100, 'did not land on the second turn');
  assert.equal(app.jumpToTurn(-1), true);
  assert.equal(el.scrollTop, 0, 'did not land on the first turn');

  // And there is nothing above the first one.
  assert.equal(app.jumpToTurn(-1), false);
  assert.equal(el.scrollTop, 0);
});

// Regression: the position was measured against the prompt while the
// scroll went to the separator above it. After a jump the prompt sits
// just below the fold, so the view read as being on the *previous* turn
// and "next" came straight back to where it already was.
test('the jump moves forward from where the last jump landed', async () => {
  const app = await load();
  for (let i = 0; i < 3; i++) {
    app.sse.emit({ seq: i + 1, type: 'message.user', data: { text: `prompt ${i}` } });
  }
  const { el } = layOut(app);

  el.scrollTop = 0;
  assert.equal(app.jumpToTurn(1), true);
  assert.equal(el.scrollTop, 100);
  assert.equal(app.jumpToTurn(1), true);
  assert.equal(el.scrollTop, 200, 'the second forward jump did not advance');
  assert.equal(app.jumpToTurn(1), false, 'there is no fourth turn to reach');
});

test('the jump marks where it landed, and only there', async () => {
  const app = await load();
  for (let i = 0; i < 3; i++) {
    app.sse.emit({ seq: i + 1, type: 'message.user', data: { text: `prompt ${i}` } });
  }
  const { el, msgs } = layOut(app);

  el.scrollTop = 210;
  app.jumpToTurn(-1);
  assert.ok(msgs[1].className.includes('landed'), msgs[1].className);

  // Walking on clears the previous mark. The class outlives its
  // animation, so without this a walk back through ten turns leaves ten
  // of them claiming to be where the reader is.
  app.jumpToTurn(-1);
  assert.ok(msgs[0].className.includes('landed'), msgs[0].className);
  assert.equal(
    msgs.item(1).className.includes('landed'),
    false,
    'the turn jumped away from is still marked',
  );
});

test('an empty transcript has nowhere to jump and says so', async () => {
  const app = await load();
  assert.equal(app.jumpToTurn(-1), false);
  assert.equal(app.jumpToTurn(1), false);
});

test('Alt+Up jumps instead of recalling a prompt', async () => {
  const app = await load();
  app.sse.emit({ seq: 1, type: 'message.user', data: { text: 'first' } });
  app.sse.emit({ seq: 2, type: 'message.user', data: { text: 'second' } });
  const { el } = layOut(app);
  el.scrollTop = 110;

  const input = app.el('input');
  input.value = '';
  app.doc.fire('keydown', { key: 'ArrowUp', altKey: true, target: input });

  assert.equal(el.scrollTop, 0, 'Alt+Up did not move the transcript');
  // Recall is what a bare Up in the prompt box does; with Alt held it
  // must not fire, or the key would do two unrelated things at once.
  assert.equal(input.value, '', `Alt+Up recalled ${input.value} into the box`);
});
