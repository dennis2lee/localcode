'use strict';

// A stream the browser has given up on.
//
// EventSource retries a transport drop by itself, which is what the page
// relied on. It does not retry a reply that was not 200 text/event-stream:
// per the spec that *fails the connection* — readyState goes to CLOSED and
// nothing happens again, ever. The daemon answers 404 from the SSE handler
// for a session it does not know, and a window whose successor has gone
// answers 502 to everything, so one such reply left the page permanently
// deaf while still looking alive: prompts posted, turns ran server-side,
// and nothing was ever painted.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load, defaultRoutes } = require('./harness');

const dot = (app) => app.el('comm-dot');

test('a stream that failed for good is opened again', async () => {
  const app = await load();
  const first = app.sse;
  assert.equal(app.streams.length, 1);

  first.failFatally();
  await app.settle();
  assert.equal(dot(app).classList.contains('connected'), false, 'the light should say the stream is down');

  // The page owns the retry now. Real timers, so this waits out the
  // first backoff step (1s) rather than pretending to.
  await app.wait(1400);
  assert.ok(app.streams.length > 1, 'the page never reopened a stream the browser had closed for good');

  app.sse.reopen();
  await app.settle();
  assert.equal(dot(app).classList.contains('connected'), true, 'the light stayed down after the stream came back');
});

test('a transport drop is left to the browser', async () => {
  const app = await load();
  assert.equal(app.streams.length, 1);

  // CONNECTING, not CLOSED: this one the browser retries itself, and a
  // second EventSource would be two streams on one conversation.
  app.sse.fail();
  await app.settle();
  // Past the first backoff step, which is when a page that had taken the
  // retry on itself would have opened one.
  await app.wait(1400);

  assert.equal(app.streams.length, 1, 'the page opened a second stream for a drop the browser was already retrying');
});
