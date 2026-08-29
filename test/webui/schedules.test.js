'use strict';

// Work booked for later, in the right-hand panel.
//
// The light is the whole row: blinking green while it waits, solid green
// once there is an answer nobody has read, grey once it has been read.
// The third state is what makes the panel a list of what still wants
// attention rather than a list of everything that ever ran.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const XSS = '<img src=x onerror="alert(1)">';

function book(app, over) {
  app.applyEvent({
    type: 'schedule.created',
    data: { id: 's1', at: '2026-09-01T09:00:00Z', prompt: 'run the tests', agent: 'general-purpose', ...over },
  });
}

test('a booked task blinks until it has run', async () => {
  const app = await load();
  book(app);
  const html = app.el('schedules').innerHTML;
  assert.match(html, /led-degraded/, 'a task still waiting should blink');
  assert.ok(html.includes('run the tests'), html);
});

test('a finished task goes solid, and grey once it has been read', async () => {
  const app = await load();
  book(app);

  app.applyEvent({ type: 'schedule.status', data: { id: 's1', status: 'done', run_session: 's1-run' } });
  assert.match(app.el('schedules').innerHTML, /led-connected/,
    'a finished task nobody has read should be solid green');

  app.applyEvent({ type: 'schedule.seen', data: { id: 's1' } });
  const html = app.el('schedules').innerHTML;
  assert.match(html, /led-disconnected/, 'a task that has been read should go grey');
  assert.ok(!html.includes('led-connected'), html);
});

test('a task that ran while localcode was closed says it was missed', async () => {
  const app = await load();
  book(app);
  app.applyEvent({
    type: 'schedule.status',
    data: { id: 's1', status: 'missed', error: 'localcode was not running at 2026-09-01 09:00' },
  });
  const html = app.el('schedules').innerHTML;
  assert.ok(html.includes('missed'), html);
  assert.ok(html.includes('not running'), 'the row has to say why it did not happen');
  // Not solid green: there is nothing to read.
  assert.ok(!html.includes('led-connected'), html);
});

test('a removed task leaves no row behind', async () => {
  const app = await load();
  book(app);
  app.applyEvent({ type: 'schedule.removed', data: { id: 's1' } });
  assert.match(app.el('schedules').innerHTML, /none/);
});

test('an empty panel says so', async () => {
  const app = await load();
  app.renderSchedules();
  assert.match(app.el('schedules').innerHTML, /none/);
});

// The prompt is text somebody typed and the id comes off the wire, so
// both render as text. Same rule as the task rows.
test('a booked prompt containing markup renders as text', async () => {
  const app = await load();
  book(app, { prompt: XSS });
  const html = app.el('schedules').innerHTML;
  assert.ok(!html.includes('<img'), html);
  assert.ok(html.includes('&lt;img'), html);
});

// A booked prompt is a paragraph and a row is one line, so a task can be
// given a name. The prompt stays underneath it: naming a task adds a
// label rather than hiding what it will actually run, which is the thing
// worth being able to check before it does.
test('a named task shows the name and still shows the prompt', async () => {
  const app = await load();
  book(app);
  app.applyEvent({ type: 'schedule.renamed', data: { id: 's1', name: 'nightly tests' } });
  const html = app.el('schedules').innerHTML;
  assert.ok(html.includes('nightly tests'), html);
  assert.ok(html.includes('run the tests'), 'the prompt must still be visible');
});

test('an empty name clears it and the row goes back to the prompt alone', async () => {
  const app = await load();
  book(app);
  app.applyEvent({ type: 'schedule.renamed', data: { id: 's1', name: 'nightly tests' } });
  app.applyEvent({ type: 'schedule.renamed', data: { id: 's1', name: '' } });
  const html = app.el('schedules').innerHTML;
  assert.ok(!html.includes('nightly tests'), html);
  assert.ok(html.includes('run the tests'), html);
});

// A name is typed by a person and comes back off the wire, so it renders
// as text. Same rule as everything else in the panel.
test('a name containing markup renders as text', async () => {
  const app = await load();
  book(app);
  app.applyEvent({ type: 'schedule.renamed', data: { id: 's1', name: XSS } });
  const html = app.el('schedules').innerHTML;
  assert.ok(!html.includes('<img'), html);
  assert.ok(html.includes('&lt;img'), html);
});

// Renaming a task must not disturb what it is waiting for.
test('renaming leaves the light and the time alone', async () => {
  const app = await load();
  book(app);
  const led = () => app.el('schedules').querySelector('.led').className;
  const when = () => app.el('schedules').querySelector('.when').textContent;
  const [led0, when0] = [led(), when()];
  app.applyEvent({ type: 'schedule.renamed', data: { id: 's1', name: 'nightly' } });
  // Compared against what was there, not against a literal time: the row
  // renders in the reader's own timezone, and a test that spells one out
  // is a test that fails on somebody else's machine.
  assert.equal(led(), led0);
  assert.equal(when(), when0);
});

// The same two jobs the session list offers, named the same way. Two
// panels doing the same thing should not ask somebody to learn two
// vocabularies, and an icon is a vocabulary.
test('a row offers rename and delete, named', async () => {
  const app = await load();
  book(app);
  const labels = [...app.el('schedules').querySelectorAll('button')].map(b => b.textContent);
  assert.deepEqual(labels, ['rename', 'delete']);
});

// Delete is outlined rather than filled, the same class the session list
// uses: a solid red on every row makes a panel read as a wall of alarms.
test('delete carries the shared danger class', async () => {
  const app = await load();
  book(app);
  const del = app.el('schedules').querySelectorAll('button')[1];
  assert.ok(del.classList.contains('danger-btn'));
});
