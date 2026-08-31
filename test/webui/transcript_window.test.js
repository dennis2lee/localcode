'use strict';

// Reaching the part of a conversation the transcript did not open with.
//
// A session opens at its end, because rebuilding a long one from the
// beginning would spend the first second of every click rendering
// thousands of messages nobody asked to see. That cut used to be silent
// and permanent: the browser asked for a tail, drew it, and had no way to
// ever ask for anything before it. The record was whole on disk and
// unreachable from here.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

// The first persisted event carries the answer. Sequences begin at 1, so
// anything higher means the daemon cut to a tail.
test('a transcript that does not start at the beginning says so', async () => {
  const app = await load();
  app.sse.emit({ type: 'message.user', seq: 412, data: { text: 'somewhere in the middle' } });

  const banner = app.el('transcript').querySelector('.msg-earlier');
  assert.ok(banner, 'no notice that the conversation was cut off');
  assert.ok(banner.querySelector('.earlier-btn'), 'the notice offers no way to load the rest');
});

test('a transcript that starts at the beginning says nothing', async () => {
  const app = await load();
  app.sse.emit({ type: 'message.user', seq: 1, data: { text: 'the first thing said' } });

  assert.equal(app.el('transcript').querySelector('.msg-earlier'), null,
    'a whole conversation was labelled as truncated');
});

// A transient event (Store.Broadcast) carries no sequence and says
// nothing about position, so it must not be taken for the first one.
test('an event with no sequence does not answer the question', async () => {
  const app = await load();
  app.sse.emit({ type: 'usage', data: { tps: 12 } });
  assert.equal(app.el('transcript').querySelector('.msg-earlier'), null);

  app.sse.emit({ type: 'message.user', seq: 88, data: { text: 'later' } });
  assert.ok(app.el('transcript').querySelector('.msg-earlier'),
    'the first sequenced event still has to be read');
});

// The whole point: the control reopens the stream from the start of the
// log. Omitting ?tail= is what asks for all of it.
test('loading the whole conversation asks the daemon for all of it', async () => {
  const app = await load();
  app.sse.emit({ type: 'message.user', seq: 412, data: { text: 'somewhere in the middle' } });

  app.el('transcript').querySelector('.earlier-btn').click();

  assert.match(app.sse.url, /^\/api\/sessions\/sess-1\/events$/,
    'the reopened stream still carries a tail, so it still cannot see the beginning');
});

// Opening a different conversation goes back to opening at the end: "show
// me all of it" was said about one conversation, not about the app.
test('another session opens at its end again', async () => {
  const app = await load();
  app.sse.emit({ type: 'message.user', seq: 412, data: { text: 'middle' } });
  app.el('transcript').querySelector('.earlier-btn').click();
  assert.match(app.sse.url, /events$/);

  await app.selectSession('sess-2');
  assert.match(app.sse.url, /^\/api\/sessions\/sess-2\/events\?tail=\d+$/);
});

// The daemon's copy of a reply is authoritative. Closing the open message
// without redrawing was a defect: a reconnect mid-reply resumes from the
// last id this page saw, so the fragments sent while it was away are
// never replayed and what is on screen is the reply with a hole in it.
test('a reply is repaired from the whole copy the daemon sends', async () => {
  const app = await load();
  app.sse.emit({ type: 'message.part.delta', seq: 2, data: { text: 'the beginning ' } });
  app.sse.emit({ type: 'message.part.end', seq: 9, data: { text: 'the beginning and the end' } });

  const html = app.transcript();
  assert.ok(html.includes('the beginning and the end'), html);
});

test('a reply that missed nothing is not drawn twice', async () => {
  const app = await load();
  app.sse.emit({ type: 'message.part.delta', seq: 2, data: { text: 'all ' } });
  app.sse.emit({ type: 'message.part.delta', seq: 3, data: { text: 'of it' } });
  app.sse.emit({ type: 'message.part.end', seq: 4, data: { text: 'all of it' } });

  const matches = app.transcript().match(/all of it/g) || [];
  assert.equal(matches.length, 1, app.transcript());
});
