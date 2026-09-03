'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { load } = require('./harness');

// The daemon under this page was replaced by a newer version, which is
// invisible to the page except for one thing: its own JavaScript is
// still the old version's. The event says so, once, in the transcript,
// and the stream that follows it reconnects on its own.
test('a daemon that hands over says so, and the page carries on', async () => {
  const app = await load();
  app.sse.emit({ seq: 0, type: 'daemon.replaced', data: { version: '9.9.9', pid: 4242 } });
  const text = app.el('transcript').textContent;
  assert.ok(text.includes('9.9.9'), 'the notice names the version that took over: ' + text);
  assert.ok(/reload/i.test(text), 'the notice says a reload gets the new interface');
});
