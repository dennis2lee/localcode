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

  // Inside the last turn, walking back: its own prompt first. Stepping
  // back from "the turn the view is on" skipped it, so from the end of a
  // long answer the first press went past the prompt just sent.
  el.scrollTop = 210;
  assert.equal(app.jumpToTurn(-1), true);
  assert.equal(el.scrollTop, 200, 'did not land on the prompt of the turn being read');
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
  assert.ok(msgs[2].className.includes('landed'), msgs[2].className);

  // Walking on clears the previous mark. The class outlives its
  // animation, so without this a walk back through ten turns leaves ten
  // of them claiming to be where the reader is.
  app.jumpToTurn(-1);
  assert.ok(msgs[1].className.includes('landed'), msgs[1].className);
  assert.equal(
    msgs.item(2).className.includes('landed'),
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
  el.scrollTop = 150;

  const input = app.el('input');
  input.value = '';
  app.doc.fire('keydown', { key: 'ArrowUp', altKey: true, target: input });

  assert.equal(el.scrollTop, 100, 'Alt+Up did not move the transcript');
  // Recall is what a bare Up in the prompt box does; with Alt held it
  // must not fire, or the key would do two unrelated things at once.
  assert.equal(input.value, '', `Alt+Up recalled ${input.value} into the box`);
});

// The browser stops scrolling at the end of the content, which layOut
// leaves out: scrollTop is clamped to scrollHeight - clientHeight.
function clampScroll(el, scrollHeight, clientHeight) {
  el.scrollHeight = scrollHeight;
  el.clientHeight = clientHeight;
  let top = 0;
  Object.defineProperty(el, 'scrollTop', {
    configurable: true,
    get: () => top,
    set: (v) => { top = Math.max(0, Math.min(v, el.scrollHeight - el.clientHeight)); },
  });
}

function landedIndex(msgs) {
  for (let i = 0; i < msgs.length; i++) if (msgs[i].className.includes('landed')) return i;
  return -1;
}

// At the end of the conversation the newest prompt is the first stop on
// the way back, wherever it sits on the screen. Near the end the turns
// cannot be brought to the top, and the walk goes on from the turn it
// landed on rather than from the position: measured from the position,
// Alt+Down landed on the same turn every time.
test('the walk near the end steps one turn at a time both ways', async () => {
  const app = await load();
  for (let i = 0; i < 3; i++) {
    app.sse.emit({ seq: i + 1, type: 'message.user', data: { text: `prompt ${i}` } });
  }
  const { el, msgs } = layOut(app);
  clampScroll(el, 450, 400);
  el.scrollTop = 50; // the bottom

  const walk = [];
  for (const dir of [-1, -1, -1, 1, 1]) {
    assert.equal(app.jumpToTurn(dir), true);
    walk.push(landedIndex(msgs));
  }
  assert.deepEqual(walk, [2, 1, 0, 1, 2], 'the walk did not visit each turn once');
  assert.equal(app.jumpToTurn(1), false, 'past the newest turn at the bottom there is nowhere to go');
});

// Past the last prompt is the end of the conversation, where the walk
// back began.
test('Alt+Down past the newest prompt returns to the end', async () => {
  const app = await load();
  for (let i = 0; i < 3; i++) {
    app.sse.emit({ seq: i + 1, type: 'message.user', data: { text: `prompt ${i}` } });
  }
  const { el } = layOut(app);
  clampScroll(el, 1000, 400);
  el.scrollTop = 200;
  assert.equal(app.jumpToTurn(1), true);
  assert.equal(el.scrollTop, 600, 'did not go back to the bottom');
});

// A reply arriving below a turn the reader jumped to does not drag the
// view to the bottom, even when the jump left it there; the reader
// scrolling lets go, and following works as it always did.
test('a jump holds the view against output arriving below it', async () => {
  const app = await load();
  for (let i = 0; i < 3; i++) {
    app.sse.emit({ seq: i + 1, type: 'message.user', data: { text: `prompt ${i}` } });
  }
  const { el } = layOut(app);
  clampScroll(el, 450, 400);
  el.scrollTop = 50;
  app.jumpToTurn(-1); // the newest, which cannot reach the top: the view stays at the bottom
  assert.equal(el.scrollTop, 50);

  // Unclamped from here, so that following, which sends the view to
  // scrollHeight, can be seen: the fake layout does not grow while a
  // reply is written.
  Object.defineProperty(el, 'scrollTop', { value: 50, writable: true, configurable: true });
  app.sse.emit({ type: 'message.part.delta', data: { text: 'more of the answer' } });
  assert.equal(el.scrollTop, 50, 'the reply arriving below dragged the view to the bottom');

  el.scrollTop = 60;
  el.fire('scroll');
  app.sse.emit({ type: 'message.part.delta', data: { text: ' and more' } });
  assert.equal(el.scrollTop, 450, 'following did not resume once the reader scrolled');
});

// A turn scrolled exactly to the top by hand is the one the view is at,
// and Up goes to the one before it.
test('Up from a turn already at the top goes to the one before', async () => {
  const app = await load();
  for (let i = 0; i < 3; i++) {
    app.sse.emit({ seq: i + 1, type: 'message.user', data: { text: `prompt ${i}` } });
  }
  const { el } = layOut(app);
  el.scrollHeight = 2000;
  el.clientHeight = 400;
  el.scrollTop = 102;
  assert.equal(app.jumpToTurn(-1), true);
  assert.equal(el.scrollTop, 0);
});

// A long or re-opened conversation holds only its last few hundred events,
// which can be a handful of prompts. The walk back used to stop at the
// oldest loaded one without a word. It now loads the whole conversation
// and lands on the prompt before that one once it is drawn.
test('walking back past the oldest loaded prompt loads the rest and lands before it', async () => {
  const app = await load();
  app.sse.emit({ seq: 41, type: 'message.user', data: { text: 'prompt 4' } });
  app.sse.emit({ seq: 46, type: 'message.part.end', data: { text: 'answer 4' } });
  app.sse.emit({ seq: 51, type: 'message.user', data: { text: 'prompt 5' } });
  app.sse.emit({ seq: 56, type: 'message.part.end', data: { text: 'answer 5' } });
  assert.ok(app.el('transcript').querySelector('.msg-earlier'), 'the tail is not marked as cut');
  let { el } = layOut(app);
  el.scrollTop = 150;

  assert.equal(app.jumpToTurn(-1), true); // prompt 5
  assert.equal(app.jumpToTurn(-1), true); // prompt 4, the oldest loaded
  assert.equal(app.jumpToTurn(-1), true, 'the walk stopped at the oldest loaded prompt');
  assert.match(app.sse.url, /^\/api\/sessions\/sess-1\/events$/, 'the rest of the conversation was not asked for');

  // The whole conversation comes back, from its first event to the last
  // one the page had seen; the landing waits for that.
  for (let i = 0; i < 6; i++) {
    app.sse.emit({ seq: i * 10 + 1, type: 'message.user', data: { text: `prompt ${i}` } });
    if (i < 5) layOut(app);
    app.sse.emit({ seq: i * 10 + 6, type: 'message.part.end', data: { text: `answer ${i}` } });
  }
  ({ el } = layOut(app));
  const msgs = el.querySelectorAll('.msg-user');
  assert.equal(msgs.length, 6);
  assert.equal(landedIndex(msgs), 3, 'did not land on the prompt before the oldest one loaded');
  assert.equal(el.scrollTop, 300);
  assert.equal(el.querySelector('.msg-earlier'), null, 'the whole conversation is still marked as cut');
});

// The banner's own button still opens the whole conversation at its end,
// with no landing.
test('the banner button loads the rest without moving the reader to a turn', async () => {
  const app = await load();
  app.sse.emit({ seq: 41, type: 'message.user', data: { text: 'prompt 4' } });
  app.el('transcript').querySelector('.earlier-btn').click();
  for (let i = 0; i < 5; i++) app.sse.emit({ seq: i * 10 + 1, type: 'message.user', data: { text: `prompt ${i}` } });
  const { msgs } = layOut(app);
  assert.equal(landedIndex(msgs), -1);
});
