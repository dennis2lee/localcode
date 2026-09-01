'use strict';

// Reading one booking in full.
//
// The panel row is one line per field and clips every one of them —
// `.sched .when`, `.sched .name` and `.sched .prompt` are all
// `white-space: nowrap` with an ellipsis. Since the prompt is the whole
// of what a booking will do, that left the panel unable to answer the
// one question worth asking of it. Double-clicking a row opens the
// booking in the window Settings uses.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const LONG_PROMPT = [
  'run the full test suite,',
  'then summarise every failure with the file and line it came from,',
  'and say which of them are new since yesterday',
].join('\n');

function book(app, over) {
  app.applyEvent({
    type: 'schedule.created',
    data: {
      id: 's1',
      at: '2026-09-01T09:00:00Z',
      prompt: LONG_PROMPT,
      agent: 'general-purpose',
      ...over,
    },
  });
}

const row = (app) => app.el('schedules').querySelectorAll('.sched')[0];
const modal = (app) => app.el('schedule-details-modal');
const isOpen = (app) => modal(app).classList.contains('open');

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

test('double-clicking a booking opens it in a window', async () => {
  const app = await load();
  book(app);
  assert.equal(isOpen(app), false, 'the window should start closed');

  row(app).fire('dblclick');
  assert.equal(isOpen(app), true, 'double-clicking the row opened nothing');
});

// The reason the window exists: the row shows the first few words of the
// prompt and the window shows all of it, newlines included.
test('the window shows the whole prompt the row clips', async () => {
  const app = await load();
  book(app);
  row(app).fire('dblclick');

  assert.equal(app.el('schedule-details-prompt').textContent, LONG_PROMPT);
});

// A booking is named after it is made, so the name arrives on its own
// event rather than with the booking.
test('the window names when it runs, its status and its agent', async () => {
  const app = await load();
  book(app);
  app.applyEvent({ type: 'schedule.renamed', data: { id: 's1', name: 'nightly check' } });
  row(app).fire('dblclick');

  assert.match(app.el('schedule-details-status').textContent, /nightly check/);
  assert.match(app.el('schedule-details-status').textContent, /pending/);
  assert.equal(app.el('schedule-details-agent').textContent, 'general-purpose');
  assert.ok(app.el('schedule-details-when').textContent.length > 0, 'no time shown');
});

// A booking that has not run has no window of output to lead to, so the
// control that would open one is not there to be pressed.
test('the run is offered only once there is a run', async () => {
  const app = await load();
  book(app);
  row(app).fire('dblclick');
  assert.equal(app.el('schedule-details-open').hidden, true);

  app.el('schedule-details-close').click();
  app.applyEvent({ type: 'schedule.status', data: { id: 's1', status: 'done', run_session: 's1-run' } });
  row(app).fire('dblclick');
  assert.equal(app.el('schedule-details-open').hidden, false);
});

test('a failure is shown, and nothing is shown when there was none', async () => {
  const app = await load();
  book(app);
  row(app).fire('dblclick');
  assert.equal(app.el('schedule-details-error').hidden, true);

  app.el('schedule-details-close').click();
  app.applyEvent({ type: 'schedule.status', data: { id: 's1', status: 'error', error: 'the model refused' } });
  row(app).fire('dblclick');
  assert.equal(app.el('schedule-details-error').hidden, false);
  assert.equal(app.el('schedule-details-error').textContent, 'the model refused');
});

test('closing the window closes it', async () => {
  const app = await load();
  book(app);
  row(app).fire('dblclick');
  app.el('schedule-details-close').click();
  assert.equal(isOpen(app), false);
});

// Both gestures live on the same row, so the single click has to wait to
// find out whether a second one is coming. A double-click must open the
// booking and not also the run.
test('double-clicking a finished task does not also open the run', async () => {
  const app = await load();
  book(app);
  app.applyEvent({ type: 'schedule.status', data: { id: 's1', status: 'done', run_session: 's1-run' } });

  const el = row(app);
  el.fire('click');
  el.fire('dblclick');
  await sleep(400);

  assert.equal(isOpen(app), true, 'the booking did not open');
  assert.equal(app.el('task-modal').classList.contains('open'), false,
    'the run opened as well, so one gesture produced two windows');
});

// And the single click still does what it always did.
test('a single click still opens the run', async () => {
  const app = await load();
  book(app);
  app.applyEvent({ type: 'schedule.status', data: { id: 's1', status: 'done', run_session: 's1-run' } });

  row(app).fire('click');
  await sleep(400);

  assert.equal(app.el('task-modal').classList.contains('open'), true,
    'the run no longer opens on a click');
  assert.equal(isOpen(app), false, 'a single click should not open the booking');
});

// A booking with nothing to open pays no wait: its details appear the
// instant they are asked for.
test('a pending task opens its details with no delay', async () => {
  const app = await load();
  book(app);
  row(app).fire('dblclick');
  assert.equal(isOpen(app), true);
});

// Every field is somebody's own text, and the name and prompt come back
// from the daemon exactly as they were typed.
test('the window renders its values as text, not markup', async () => {
  const app = await load();
  const XSS = '<img src=x onerror="alert(1)">';
  book(app, { prompt: XSS });
  app.applyEvent({ type: 'schedule.renamed', data: { id: 's1', name: XSS } });
  row(app).fire('dblclick');

  assert.equal(app.el('schedule-details-prompt').textContent, XSS);
  assert.ok(!modal(app).innerHTML.includes('<img src=x'), modal(app).innerHTML);
});
