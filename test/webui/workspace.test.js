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

// Regression: clicking the workspace button twice opened two "Browse for
// Folder" dialogs.
//
// The dialog is modal to the daemon, not to this page: the request that
// opens it does not answer until someone picks or cancels, and the page
// keeps handling clicks the whole time. So the second click put a second
// dialog on top of the first, both had to be answered, and whichever was
// answered last silently overwrote the other's choice.
test('a second click does not open a second folder dialog', async () => {
  let open = 0;
  let release;
  const picked = new Promise((r) => { release = r; });

  const app = await load({
    routes: {
      'GET /api/workspace': { path: '/srv/project', can_browse: true },
      'GET /api/sessions': [{ id: 'sess-1', title: 't', agent: 'general-purpose', workspace: '/srv/project' }],
      'POST /api/workspace/browse': async () => {
        open++;
        await picked; // the dialog sits there, exactly as the real one does
        return { path: '/srv/chosen' };
      },
      'POST /api/workspace': { path: '/srv/chosen' },
    },
  });

  app.el('workspace-btn').click();
  await app.settle();
  assert.equal(open, 1);
  // Visible, not just ignored: the button says it is busy.
  assert.equal(app.el('workspace-btn').disabled, true);

  app.el('workspace-btn').click();
  app.el('workspace-btn').click();
  await app.settle();
  assert.equal(open, 1, 'a second dialog was opened on top of the first');

  release();
  await app.settle();

  // Once answered, the button works again and the choice was applied.
  assert.equal(app.el('workspace-btn').disabled, false);
  assert.equal(app.state.workspacePath, '/srv/chosen');

  app.el('workspace-btn').click();
  await app.settle();
  assert.equal(open, 2, 'the button stayed stuck after the dialog closed');
});

// A picker that fails must not leave the button disabled forever — that
// would cost the typed-path fallback too.
test('a failed folder dialog releases the button', async () => {
  const app = await load({
    routes: {
      'GET /api/workspace': { path: '/srv/project', can_browse: true },
      'GET /api/sessions': [{ id: 'sess-1', title: 't', agent: 'general-purpose', workspace: '/srv/project' }],
      'POST /api/workspace/browse': { status: 500, body: { error: 'no display' } },
    },
  });

  app.el('workspace-btn').click();
  await app.settle();

  assert.equal(app.el('workspace-btn').disabled, false);
  assert.match(app.transcript(), /could not open the folder picker/);
});
