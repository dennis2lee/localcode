'use strict';

// What a reload has to bring back. "/update" reloads the page on its own
// now, so anything the page forgets across a reload is something the
// update takes away from whoever ran it.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

// Three conversations, so "the one this window was reading" is not also
// the one the old code would have picked anyway.
const SESSIONS = {
  'GET /api/sessions': [
    { id: 's-1', title: 'one', agent: 'general-purpose', workspace: '/w', created_at: '2026-01-01T00:00:00Z' },
    { id: 's-2', title: 'two', agent: 'general-purpose', workspace: '/w', created_at: '2026-01-02T00:00:00Z' },
    { id: 's-3', title: 'three', agent: 'general-purpose', workspace: '/w', created_at: '2026-01-03T00:00:00Z' },
  ],
};

test('a reload comes back to the conversation this window was reading', async () => {
  const app = await load({
    routes: SESSIONS,
    sessionStorage: { 'localcode.openSession': 's-3' },
  });
  assert.equal(app.state.sessionID, 's-3');
});

test('opening a conversation is what makes it the one to come back to', async () => {
  const app = await load({ routes: SESSIONS });
  app.internals.selectSession('s-2', 'general-purpose', '/w');
  assert.equal(app.sessionStorage.get('localcode.openSession'), 's-2');
});

test('a remembered conversation that is gone falls back to the top of the list', async () => {
  const app = await load({
    routes: SESSIONS,
    sessionStorage: { 'localcode.openSession': 's-deleted' },
  });
  assert.equal(app.state.sessionID, 's-1');
});

test('the zoom is the page own and survives a reload', async () => {
  const app = await load({ localStorage: { 'localcode.zoom': '1.5' } });
  assert.equal(app.state.zoom, 1.5);
  assert.equal(app.doc.documentElement.style.zoom, '1.5');
});

test('ctrl+wheel steps the zoom and writes it down', async () => {
  const app = await load();
  assert.equal(app.state.zoom, 1);
  app.doc.fire('wheel', { ctrlKey: true, deltaY: -1, preventDefault() {} });
  assert.ok(app.state.zoom > 1, `zoom stayed at ${app.state.zoom}`);
  assert.equal(app.storage.get('localcode.zoom'), String(app.state.zoom));
});

test('a wheel with no modifier is left to the page to scroll', async () => {
  const app = await load();
  app.doc.fire('wheel', { deltaY: -1, preventDefault() {} });
  assert.equal(app.state.zoom, 1);
});

test('ctrl+0 goes back to 100 percent', async () => {
  const app = await load({ localStorage: { 'localcode.zoom': '2' } });
  app.doc.fire('keydown', { ctrlKey: true, key: '0', preventDefault() {} });
  assert.equal(app.state.zoom, 1);
  assert.equal(app.doc.documentElement.style.zoom, '');
});
