'use strict';

// Changing the workspace has to land in three places at once: the daemon,
// the header button, and the session list. It used to land in one.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const routes = {
  'GET /api/workspace': { path: '/tmp/old', can_browse: true },
  'GET /api/sessions': [{ id: 'sess-1', title: 't', agent: 'general-purpose', workspace: '/tmp/old' }],
  'POST /api/workspace': { path: '/tmp/new' },
  'POST /api/workspace/browse': { path: '/tmp/new' },
};

test('picking a folder updates the header, the session list, and the daemon', async () => {
  const app = await load({ routes });
  assert.equal(app.el('workspace-btn').textContent, '/tmp/old');
  assert.match(app.el('session-list').innerHTML, /\/tmp\/old/);

  app.el('workspace-btn').click();
  await app.settle();

  assert.equal(app.el('workspace-btn').textContent, '/tmp/new');
  // Regression: the left panel went on naming the directory the session
  // was created in, because its cached listing is only refetched on a
  // rename or a session switch.
  assert.match(app.el('session-list').innerHTML, /\/tmp\/new/, app.el('session-list').innerHTML);
  assert.doesNotMatch(app.el('session-list').innerHTML, /\/tmp\/old/);
});

// Regression: without the session id the daemon had nothing to record the
// move against, so re-selecting the session put the workspace back where
// the session was created — which is what made a switch look like it had
// never taken.
test('the switch tells the daemon which session moved', async () => {
  const app = await load({ routes });
  app.el('workspace-btn').click();
  await app.settle();

  const posted = app.callsTo('POST', '/api/workspace')[0];
  assert.equal(posted.body.path, '/tmp/new');
  assert.equal(posted.body.session_id, 'sess-1');
});

test('a refused switch leaves every view on the old workspace', async () => {
  const app = await load({
    routes: { ...routes, 'POST /api/workspace': { status: 409, body: { error: 'a turn is in progress' } } },
  });
  app.el('workspace-btn').click();
  await app.settle();

  assert.equal(app.el('workspace-btn').textContent, '/tmp/old');
  assert.match(app.el('session-list').innerHTML, /\/tmp\/old/);
  assert.match(app.transcript(), /could not switch the workspace/);
});
