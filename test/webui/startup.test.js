'use strict';

// Startup: the contract between index.html and js/*.js, and what the page
// does when the daemon answers badly. These are the failures that would
// otherwise only show up as a blank page in front of a user.

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const { load } = require('./harness');

const STATIC_DIR = path.join(__dirname, '..', '..', 'internal', 'daemon', 'static');

test('every element id js/*.js looks up exists in index.html', async () => {
  // load() throws when one is missing; this asserts it actually checked
  // something, so the guarantee can't quietly become vacuous.
  const app = await load();
  assert.ok(app.document.requestedIDs.size > 30, `only ${app.document.requestedIDs.size} ids looked up`);
  assert.equal(app.document.missingIDs.size, 0);
});

test('index.html loads the split-out stylesheet and the ES module entry point', () => {
  const html = fs.readFileSync(path.join(STATIC_DIR, 'index.html'), 'utf8');
  assert.match(html, /<link rel="stylesheet" href="style\.css">/);
  assert.match(html, /<script type="module" src="js\/main\.js"><\/script>/);
  // The markup and the code stay separate — no inline blocks crept back in.
  assert.ok(!/<style[\s>]/.test(html), 'index.html has an inline <style> block again');
  assert.ok(!/<script(?!\s+type="module")/.test(html), 'index.html has a non-module inline <script> block again');
});

test('the shipped files contain no stray NUL bytes', () => {
  const jsDir = path.join(STATIC_DIR, 'js');
  const files = ['style.css', 'index.html', ...fs.readdirSync(jsDir).map((f) => path.join('js', f))];
  for (const name of files) {
    const data = fs.readFileSync(path.join(STATIC_DIR, name));
    assert.equal(data.indexOf(0), -1, `${name} contains a NUL byte`);
  }
});

test('every js/*.js file uses only relative imports (no bundler, no bare specifiers)', () => {
  const jsDir = path.join(STATIC_DIR, 'js');
  for (const name of fs.readdirSync(jsDir)) {
    const src = fs.readFileSync(path.join(jsDir, name), 'utf8');
    for (const m of src.matchAll(/^import\b[^;]*\bfrom\s+['"]([^'"]+)['"]/gm)) {
      assert.ok(m[1].startsWith('./') || m[1].startsWith('../'), `${name} imports "${m[1]}" — not a relative specifier`);
    }
  }
});

test('a fresh start with no sessions creates one', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': [],
      'POST /api/sessions': { id: 'sess-created', agent: 'general-purpose', workspace: '/tmp/workspace' },
    },
  });
  assert.equal(app.state.sessionID, 'sess-created');
  assert.equal(app.callsTo('POST', '/api/sessions').length, 1);
  assert.equal(app.el('session-id').textContent, 'sess-created');
});

test('an existing session is resumed rather than replaced', async () => {
  const app = await load();
  assert.equal(app.state.sessionID, 'sess-1');
  assert.equal(app.callsTo('POST', '/api/sessions').length, 0);
});

test('the agent dropdown is filled from /api/agents with the model in the label', async () => {
  const app = await load();
  const select = app.el('agent-select');
  assert.deepEqual(select.options.map((o) => o.value), ['general-purpose', 'plan']);
  assert.equal(select.options[0].textContent, 'general-purpose (test-model-1)');
  assert.equal(select.options[0].title, 'the default agent');
  assert.equal(select.value, 'general-purpose');
});

test('the workspace button shows the daemon workspace', async () => {
  const app = await load({
    routes: {
      'GET /api/workspace': { path: '/srv/project', can_browse: false },
      'GET /api/sessions': [{ id: 'sess-1', title: 't', agent: 'general-purpose', workspace: '/srv/project' }],
    },
  });
  assert.equal(app.el('workspace-btn').textContent, '/srv/project');
  // Already there, so selecting the session doesn't ask the daemon to move.
  assert.equal(app.callsTo('POST', '/api/workspace').length, 0);
});

// Each session remembers the project it belongs to, so opening one moves the
// daemon's working directory — which every later tool call resolves from, and
// is therefore announced in the transcript rather than done silently.
test('selecting a session switches the daemon to that session\'s workspace', async () => {
  const app = await load({
    routes: {
      'GET /api/workspace': { path: '/srv/other', can_browse: false },
      'GET /api/sessions': [{ id: 'sess-1', title: 't', agent: 'general-purpose', workspace: '/srv/project' }],
      'POST /api/workspace': { path: '/srv/project' },
    },
  });
  const calls = app.callsTo('POST', '/api/workspace');
  assert.equal(calls.length, 1);
  assert.deepEqual(calls[0].body, { path: '/srv/project', session_id: 'sess-1' });
  assert.equal(app.el('workspace-btn').textContent, '/srv/project');
  assert.match(app.transcript(), /\[workspace\] \/srv\/project/);
});

test('a refused workspace switch is reported and does not move the button', async () => {
  const app = await load({
    routes: {
      'GET /api/workspace': { path: '/srv/other', can_browse: false },
      'GET /api/sessions': [{ id: 'sess-1', title: 't', agent: 'general-purpose', workspace: '/srv/project' }],
      'POST /api/workspace': { status: 409, body: { error: 'a turn is in progress' } },
    },
  });
  assert.equal(app.el('workspace-btn').textContent, '/srv/other');
  assert.match(app.transcript(), /could not switch the workspace to \/srv\/project/);
});

// A failing settings/agents/workspace call must not stop the page loading:
// each loader keeps its defaults and the session still comes up.
test('the page still comes up when the metadata endpoints fail', async () => {
  const app = await load({
    routes: {
      'GET /api/agents': { status: 500 },
      'GET /api/commands': { status: 500 },
      'GET /api/settings': { status: 500 },
      'GET /api/workspace': { status: 500 },
      'GET /api/mcp-servers': { status: 500 },
      // A session from before workspace tracking: nothing to apply, so the
      // failed /api/workspace answer is the only thing on screen.
      'GET /api/sessions': [{ id: 'sess-1', title: 't', agent: 'general-purpose' }],
    },
  });
  assert.equal(app.state.sessionID, 'sess-1');
  assert.deepEqual(Array.from(app.state.agents), []);
  assert.equal(app.el('workspace-btn').textContent, '(unknown workspace)');
  assert.match(app.el('mcp-servers').innerHTML, /no configured servers/);
});

test('a session list that cannot be fetched leaves the page usable', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': { status: 500 },
      'POST /api/sessions': { id: 'sess-created', agent: 'general-purpose', workspace: '/tmp/workspace' },
    },
  });
  assert.equal(app.state.sessionID, 'sess-created');
  assert.match(app.el('session-list').innerHTML, /no sessions/);
});

test('a failing session create is reported instead of leaving a dead page', async () => {
  const app = await load({
    routes: { 'GET /api/sessions': [], 'POST /api/sessions': { status: 500, body: { error: 'disk full' } } },
  });
  assert.equal(app.el('session-id').textContent, 'error');
  assert.match(app.transcript(), /failed to create session/);
});

// The other half of the blank-line bug lived in the stylesheet, and no
// amount of renderer testing catches it: white-space: pre-wrap on
// #transcript is inherited by .msg-model, where the content is rendered
// HTML whose newlines are source formatting, not content. It belongs only
// on the messages built with textContent, where the newlines are the
// formatting. This pins the scoping so it cannot drift back.
test('pre-wrap is scoped to the plain-text messages, not the rendered ones', () => {
  const css = fs.readFileSync(
    path.join(__dirname, '..', '..', 'internal', 'daemon', 'static', 'style.css'),
    'utf8',
  );
  const transcriptBlock = css.match(/#transcript\s*\{([^}]*)\}/);
  assert.ok(transcriptBlock, 'no #transcript rule found');
  assert.ok(
    !/white-space/.test(transcriptBlock[1]),
    'white-space on #transcript is inherited by .msg-model and doubles every gap in a rendered reply',
  );
  assert.match(css, /#transcript \.msg-user[^{]*\{[^}]*white-space:\s*pre-wrap/);
});

// The header names the *daemon's* version, which is the one that matters
// when a client is attached to a core running somewhere else.
test('the header shows the daemon version next to the name', async () => {
  const app = await load({ routes: { 'GET /api/version': { version: '0.31.1' } } });
  assert.equal(app.el('app-version').textContent, 'v0.31.1');
});

test('a version the daemon will not report leaves the header clean', async () => {
  const app = await load({ routes: { 'GET /api/version': { status: 500 } } });
  assert.equal(app.el('app-version').textContent, '');
  // and the rest of the page still came up
  assert.equal(app.state.sessionID, 'sess-1');
});

// The microphone button is offered only when a dictation could actually
// start. A build with no recognizer, or one with no model configured,
// gets no button — one that can only fail is worse than none.
test('the microphone button is hidden unless dictation is ready', async () => {
  const off = await load({
    routes: { 'GET /api/dictation': { ready: false, detail: 'no speech recognizer in this build' } },
  });
  assert.equal(off.el('mic').hidden, true);

  const on = await load({ routes: { 'GET /api/dictation': { ready: true } } });
  assert.equal(on.el('mic').hidden, false);
});

test('a daemon too old to know about dictation just gets no button', async () => {
  const app = await load({ routes: { 'GET /api/dictation': { status: 404 } } });
  assert.equal(app.el('mic').hidden, true);
  assert.equal(app.state.sessionID, 'sess-1'); // and the page still came up
});
