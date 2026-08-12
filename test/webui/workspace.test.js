'use strict';

// Changing the workspace has to land in three places at once: the daemon,
// the header button, and the session list. It used to land in one.
//
// The route in is the modal, always. Clicking the workspace used to go
// straight to the OS folder picker whenever the daemon had one — which
// meant the desktop build, the only build with a picker, was the only
// build with no way to type or paste a path. The picker is now a button
// inside the modal that fills the box, and Save is the single action that
// moves the workspace.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const routes = {
  'GET /api/workspace': { path: '/tmp/old', can_browse: true, can_reveal: true },
  'GET /api/sessions': [{ id: 'sess-1', title: 't', agent: 'general-purpose', workspace: '/tmp/old' }],
  'POST /api/workspace': { path: '/tmp/new' },
  'POST /api/workspace/browse': { path: '/tmp/new' },
};

// Fills the path box the way someone typing or pasting would, then saves.
async function typeAndSave(app, path) {
  app.el('workspace-btn').click();
  await app.settle();
  app.el('workspace-input').value = path;
  app.el('workspace-save').click();
  await app.settle();
}

test('a typed path updates the header, the session list, and the daemon', async () => {
  const app = await load({ routes });
  assert.equal(app.el('workspace-btn').textContent, '/tmp/old');
  assert.match(app.el('session-list').innerHTML, /\/tmp\/old/);

  await typeAndSave(app, '/tmp/new');

  assert.equal(app.el('workspace-btn').textContent, '/tmp/new');
  // Regression: the left panel went on naming the directory the session
  // was created in, because its cached listing is only refetched on a
  // rename or a session switch.
  assert.match(app.el('session-list').innerHTML, /\/tmp\/new/, app.el('session-list').innerHTML);
  assert.doesNotMatch(app.el('session-list').innerHTML, /\/tmp\/old/);
});

// The whole point of the change: on the desktop build there is now a way
// in that does not involve clicking through a folder tree.
test('the path box is reachable even where the daemon has a folder picker', async () => {
  const app = await load({ routes });
  app.el('workspace-btn').click();
  await app.settle();

  assert.equal(app.workspace.isOpen, true, 'clicking the workspace did not open the modal');
  assert.equal(app.el('workspace-input').value, '/tmp/old', 'the box does not start on the current workspace');
  assert.equal(app.el('workspace-browse').style.display, '', 'the Browse button should be offered here');
  assert.equal(app.callsTo('POST', '/api/workspace/browse').length, 0, 'opening the modal opened a folder dialog too');
});

// A browser talking to a daemon on another machine has no picker to open:
// the dialog would appear on the server. An inert button invites clicking,
// so it is not shown at all.
test('the Browse button is hidden where there is no picker behind it', async () => {
  const app = await load({
    routes: { ...routes, 'GET /api/workspace': { path: '/tmp/old', can_browse: false, can_reveal: false } },
  });
  app.el('workspace-btn').click();
  await app.settle();
  assert.equal(app.el('workspace-browse').style.display, 'none');
});

test('Browse fills the path box rather than switching straight away', async () => {
  const app = await load({ routes });
  app.el('workspace-btn').click();
  await app.settle();

  app.el('workspace-browse').click();
  await app.settle();

  assert.equal(app.el('workspace-input').value, '/tmp/new', 'the chosen folder did not land in the box');
  // Nothing has moved yet: picking the wrong folder is a correction here,
  // not a switch that has to be undone.
  assert.equal(app.callsTo('POST', '/api/workspace').length, 0);
  assert.equal(app.el('workspace-btn').textContent, '/tmp/old');

  app.el('workspace-save').click();
  await app.settle();
  assert.equal(app.el('workspace-btn').textContent, '/tmp/new');
});

// Regression: without the session id the daemon had nothing to record the
// move against, so re-selecting the session put the workspace back where
// the session was created — which is what made a switch look like it had
// never taken.
test('the switch tells the daemon which session moved', async () => {
  const app = await load({ routes });
  await typeAndSave(app, '/tmp/new');

  const posted = app.callsTo('POST', '/api/workspace')[0];
  assert.equal(posted.body.path, '/tmp/new');
  assert.equal(posted.body.session_id, 'sess-1');
});

test('a refused switch leaves every view on the old workspace', async () => {
  const app = await load({
    routes: { ...routes, 'POST /api/workspace': { status: 409, body: { error: 'a turn is in progress' } } },
  });
  await typeAndSave(app, '/tmp/new');

  assert.equal(app.el('workspace-btn').textContent, '/tmp/old');
  assert.match(app.el('session-list').innerHTML, /\/tmp\/old/);
  // Reported in the modal, which stays open so the path does not have to
  // be typed again once whatever was running has stopped.
  assert.equal(app.workspace.isOpen, true);
  assert.match(app.el('workspace-note').textContent, /turn is in progress/);
});

// Regression: clicking twice opened two "Browse for Folder" dialogs.
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
      'GET /api/workspace': { path: '/srv/project', can_browse: true, can_reveal: true },
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
  app.el('workspace-browse').click();
  await app.settle();
  assert.equal(open, 1);
  // Visible, not just ignored: the button says it is busy.
  assert.equal(app.el('workspace-browse').disabled, true);

  app.el('workspace-browse').click();
  app.el('workspace-browse').click();
  await app.settle();
  assert.equal(open, 1, 'a second dialog was opened on top of the first');

  release();
  await app.settle();

  assert.equal(app.el('workspace-browse').disabled, false);
  assert.equal(app.el('workspace-input').value, '/srv/chosen');

  app.el('workspace-browse').click();
  await app.settle();
  assert.equal(open, 2, 'the button stayed stuck after the dialog closed');
});

// A picker that fails must not leave the button disabled forever — that
// would cost the typed-path fallback too.
test('a failed folder dialog releases the button and leaves the typed path alone', async () => {
  const app = await load({
    routes: {
      'GET /api/workspace': { path: '/srv/project', can_browse: true, can_reveal: true },
      'GET /api/sessions': [{ id: 'sess-1', title: 't', agent: 'general-purpose', workspace: '/srv/project' }],
      'POST /api/workspace/browse': { status: 500, body: { error: 'no display' } },
    },
  });

  app.el('workspace-btn').click();
  await app.settle();
  app.el('workspace-input').value = '/srv/typed';
  app.el('workspace-browse').click();
  await app.settle();

  assert.equal(app.el('workspace-browse').disabled, false);
  assert.equal(app.el('workspace-input').value, '/srv/typed', 'the typed path was thrown away');
  assert.match(app.el('workspace-note').textContent, /could not open/i);
});

// The folder icon beside the workspace name opens the directory in the
// machine's own file manager.
test('the folder icon asks the daemon to open a file-manager window', async () => {
  const app = await load({ routes: { ...routes, 'POST /api/workspace/reveal': { path: '/tmp/old' } } });

  app.el('workspace-reveal-btn').click();
  await app.settle();

  assert.equal(app.callsTo('POST', '/api/workspace/reveal').length, 1);
  assert.equal(app.transcript().includes('Error'), false, app.transcript());
});

// Over the network the window would open on the daemon's machine, in front
// of nobody — the same reason the folder picker is hidden there.
test('the folder icon is hidden when the daemon has no screen to open it on', async () => {
  const app = await load({
    routes: { ...routes, 'GET /api/workspace': { path: '/tmp/old', can_browse: false, can_reveal: false } },
  });
  assert.equal(app.el('workspace-reveal-btn').style.display, 'none');
});

// The workspace guard is daemon-wide: a turn in any session blocks a move,
// including one nobody is watching and one parked forever on a permission
// request nobody answered. Being told "a turn is in progress" and left to
// go and find it is what makes this read as "I often just can't change the
// workspace" — so the daemon names them and the modal offers a way out.
test('a refused switch offers to stop the turns that are blocking it', async () => {
  let refuse = true;
  const app = await load({
    routes: {
      ...routes,
      'POST /api/workspace': () => {
        if (refuse) {
          return {
            status: 409,
            body: { error: 'a turn is in progress in s-1, s-2', busy: ['s-1', 's-2'] },
          };
        }
        return { path: '/tmp/new' };
      },
      'POST /api/sessions/s-1/cancel': { cancelled: true },
      'POST /api/sessions/s-2/cancel': { cancelled: true },
    },
  });

  await typeAndSave(app, '/tmp/new');
  assert.equal(app.el('workspace-stop-busy').style.display, '', 'no way out was offered');
  assert.match(app.el('workspace-note').textContent, /s-1, s-2/);

  refuse = false;
  app.el('workspace-stop-busy').click();
  await app.settle();

  // Every named session was stopped, and the switch went through without
  // the path having to be typed again.
  assert.equal(app.callsTo('POST', '/api/sessions/s-1/cancel').length, 1);
  assert.equal(app.callsTo('POST', '/api/sessions/s-2/cancel').length, 1);
  assert.equal(app.el('workspace-btn').textContent, '/tmp/new');
  assert.equal(app.workspace.isOpen, false);
});

// An ordinary refusal — a path that does not exist — has nothing to stop,
// so the button stays out of the way.
test('a refusal with nothing running offers no stop button', async () => {
  const app = await load({
    routes: { ...routes, 'POST /api/workspace': { status: 400, body: { error: 'stat /nope: no such file or directory' } } },
  });
  await typeAndSave(app, '/nope');
  assert.equal(app.el('workspace-stop-busy').style.display, 'none');
  assert.match(app.el('workspace-note').textContent, /no such file/);
});
