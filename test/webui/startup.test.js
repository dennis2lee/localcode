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

// The dictation pill is always on screen. It used to be hidden whenever
// dictation could not start, which also hid the fact that the feature
// exists — and the usual reason it cannot start is a missing config key,
// which is exactly the kind of thing you would go fix if you knew.
test('the dictation pill says it is unavailable rather than disappearing', async () => {
  const off = await load({
    routes: { 'GET /api/dictation': { ready: false, detail: 'no speech recognizer in this build' } },
  });
  assert.equal(off.el('mic').disabled, true);
  assert.match(off.el('mic').textContent, /unavailable/);
  // The daemon's own explanation is what reaches the user, not a
  // generic "not available" that leaves them nowhere to go.
  assert.match(off.el('mic').title, /no speech recognizer in this build/);

  const on = await load({ routes: { 'GET /api/dictation': { ready: true } } });
  assert.equal(on.el('mic').disabled, false);
  assert.match(on.el('mic').textContent, /dictation: off/);
});

test('a daemon too old to know about dictation leaves the pill disabled', async () => {
  const app = await load({ routes: { 'GET /api/dictation': { status: 404 } } });
  assert.equal(app.el('mic').disabled, true);
  assert.equal(app.state.sessionID, 'sess-1'); // and the page still came up
});

// Regression: dictation could never start, in any build, ever.
//
// The daemon answers POST /api/dictation with {"id": "..."}, and the
// client used that whole object as the id. Every request after it went
// to /api/dictation/[object Object]/... — which the daemon, quite
// correctly, answered with "no dictation session". The visible result
// was an error line the moment the microphone was clicked.
//
// The tests that existed only checked whether the button was *shown*.
// Nothing exercised what pressing it did, which is why an unusable
// feature shipped looking finished.
test('starting dictation posts audio to the session the daemon named', async () => {
  const app = await load({
    routes: {
      'GET /api/dictation': { ready: true },
      'POST /api/dictation': { status: 201, body: { id: 'dict-7' } },
      'POST /api/dictation/dict-7/audio': { provisional: 'hello' },
    },
  });

  app.el('mic').click();
  await app.settle();

  assert.equal(app.callsTo('POST', '/api/dictation').length, 1);
  assert.equal(app.isDictating(), true);

  app.micChunk();
  await app.settle();

  // The whole point: a real id in the path, not a stringified object.
  assert.equal(app.callsTo('POST', '/api/dictation/dict-7/audio').length, 1);
  assert.equal(app.callsTo('POST', /\[object/).length, 0);

  // And the recognised text reaches the prompt box.
  assert.equal(app.el('input').value, 'hello');
});

test('stopping dictation stops the same session', async () => {
  const app = await load({
    routes: {
      'GET /api/dictation': { ready: true },
      'POST /api/dictation': { status: 201, body: { id: 'dict-7' } },
      'POST /api/dictation/dict-7/stop': { final: 'the whole sentence' },
    },
  });

  app.el('mic').click();
  await app.settle();
  app.el('mic').click();
  await app.settle();

  assert.equal(app.callsTo('POST', '/api/dictation/dict-7/stop').length, 1);
  assert.equal(app.isDictating(), false);
  // Whatever was mid-sentence when the button was clicked is still text
  // the person said and meant.
  assert.equal(app.el('input').value, 'the whole sentence');
});

// Regression: sending a dictated prompt always ended in a red error.
//
// Enter stops the microphone before it sends (see main.js), and the stop
// request would overtake a chunk of audio that was still uploading. That
// chunk then arrived at a session the daemon had just closed, came back
// as "no dictation session d-1", and was reported as a failure — for
// what is simply the end of a dictation.
//
// It cost the audio too: nulling the session first dropped whatever was
// still queued, so the last word or two never reached the recognizer.
test('stopping waits for the audio already recorded, then closes the session', async () => {
  const order = [];
  const app = await load({
    routes: {
      'GET /api/dictation': { ready: true },
      'POST /api/dictation': { status: 201, body: { id: 'd-1' } },
      'POST /api/dictation/d-1/audio': async () => {
        // A round trip that outlasts the click on Send.
        await new Promise((r) => setTimeout(r, 5));
        order.push('audio');
        return { provisional: 'half a sen' };
      },
      'POST /api/dictation/d-1/stop': () => { order.push('stop'); return { final: 'half a sentence' }; },
    },
  });

  app.el('mic').click();
  await app.settle();
  app.micChunk();

  // No await between the chunk and the stop: this is Enter, which stops
  // the microphone and sends in the same breath.
  await app.stopDictation();
  await app.settle();

  // The invariant. Closing the session before its last audio arrives is
  // what produced "no dictation session d-1" on every dictated prompt,
  // and it threw away the final word or two on the way.
  assert.deepEqual(order, ['audio', 'stop']);
  assert.ok(!app.transcript().includes('Error'), app.transcript());
  assert.equal(app.el('input').value, 'half a sentence');
  assert.equal(app.isDictating(), false);
});

// A failure while still recording is a real one and must still be shown.
// A failure that keeps happening is still reported and still stops the
// microphone — the recognizer really is gone and pretending otherwise
// would leave a lit recording indicator over nothing.
test('an audio failure that persists is still reported', async () => {
  const app = await load({
    routes: {
      'GET /api/dictation': { ready: true },
      'POST /api/dictation': { status: 201, body: { id: 'd-1' } },
      'POST /api/dictation/d-1/audio': { status: 500, body: { error: 'recognizer died' } },
    },
  });

  app.el('mic').click();
  await app.settle();
  for (let i = 0; i < 4; i++) {
    app.micChunk();
    await app.settle();
  }

  assert.match(app.transcript(), /dictation stopped/);
  assert.equal(app.isDictating(), false);
});

// One hiccup used to be fatal: any failed chunk switched the microphone
// off mid-sentence. Most causes are momentary and the next chunk is a
// quarter of a second away.
test('one failed chunk does not switch the microphone off', async () => {
  let n = 0;
  const app = await load({
    routes: {
      'GET /api/dictation': { ready: true },
      'POST /api/dictation': { status: 201, body: { id: 'd-1' } },
      'POST /api/dictation/d-1/audio': () => {
        n++;
        if (n === 1) return { status: 500, body: { error: 'busy' } };
        return { provisional: 'still here' };
      },
    },
  });

  app.el('mic').click();
  await app.settle();
  app.micChunk();
  await app.settle();
  assert.equal(app.isDictating(), true, 'a single failure stopped dictation');
  // The retry happens straight away with the same audio, so nothing said
  // during the hiccup is lost.
  assert.equal(app.el('input').value, 'still here');

  app.micChunk();
  await app.settle();
  assert.equal(app.el('input').value, 'still here');
  assert.equal(app.transcript().includes('dictation stopped'), false, app.transcript());
});

// The daemon closes a session it believes has been abandoned — which can
// happen while someone is still talking, because a long transcription is
// time in which no audio arrives. Switching the microphone off in the
// middle of a sentence is the worst possible answer; opening another
// session and carrying on is the right one.
test('a session closed by the daemon is reopened rather than ending dictation', async () => {
  let opened = 0;
  const app = await load({
    routes: {
      'GET /api/dictation': { ready: true },
      'POST /api/dictation': () => {
        opened++;
        return { status: 201, body: { id: `d-${opened}` } };
      },
      'POST /api/dictation/d-1/audio': { status: 404, body: { error: 'no dictation session d-1' } },
      'POST /api/dictation/d-2/audio': { provisional: 'carried on' },
    },
  });

  app.el('mic').click();
  await app.settle();
  app.micChunk();
  await app.settle();

  assert.equal(opened, 2, 'the closed session was not replaced');
  assert.equal(app.isDictating(), true);
  assert.equal(app.transcript().includes('dictation stopped'), false, app.transcript());
  // The audio that failed is sent again rather than dropped: the words in
  // it are words somebody said.
  assert.equal(app.el('input').value, 'carried on');
});

// The guard on `live` used to sit before the first await, but `live` was
// only assigned after the daemon answered — so two clicks both passed it,
// both opened a microphone, and the second assignment orphaned the first
// stream. stopDictation could never reach it: the browser's recording
// indicator stayed lit with dictation off.
test('a second mic click while the first is still starting opens one session', async () => {
  let started = 0;
  const app = await load({
    routes: {
      'GET /api/dictation': { ready: true },
      'POST /api/dictation': async () => {
        started++;
        await new Promise((r) => setTimeout(r, 5));
        return { id: `d-${started}` };
      },
      'POST /api/dictation/d-1/stop': { final: '' },
      'POST /api/dictation/d-2/stop': { final: '' },
    },
  });

  app.el('mic').click();
  app.el('mic').click();
  // Long enough for the deliberately slow POST above to come back, which
  // is the window the two clicks are racing inside.
  await new Promise((r) => setTimeout(r, 20));
  await app.settle();

  assert.equal(started, 1, `the daemon was asked for ${started} sessions`);
  assert.equal(app.openMicrophones(), 1, 'a microphone was opened and then orphaned');
  await app.stopDictation();
  await app.settle();
  assert.equal(app.openMicrophones(), 0, 'the microphone is still held after stopping');
});

// The stop round trip lands after the box is the user's again. Writing
// the session's snapshot back discarded whatever had been typed since.
test('text typed after stopping survives the final transcription', async () => {
  const app = await load({
    routes: {
      'GET /api/dictation': { ready: true },
      'POST /api/dictation': { status: 201, body: { id: 'd-1' } },
      'POST /api/dictation/d-1/audio': { provisional: 'half a sen' },
      'POST /api/dictation/d-1/stop': async () => {
        await new Promise((r) => setTimeout(r, 5));
        return { final: 'half a sentence' };
      },
    },
  });

  app.el('mic').click();
  await app.settle();
  app.micChunk();
  await app.settle();

  const stopping = app.stopDictation();
  // The user carries on typing while that request is in flight.
  app.el('input').value = app.el('input').value + ' and then some';
  await stopping;
  await app.settle();

  assert.equal(app.el('input').value, 'half a sentence and then some');
});

// Audio arrives on a fixed clock while a request can take much longer
// than one chunk: committing an utterance re-transcribes the whole
// sentence, and on a slow machine that is seconds. One chunk per round
// trip then falls further and further behind, which is what "I am still
// talking and the text has stopped moving" is. The backlog goes as a
// single request instead — lossless, since the recognizer takes any
// length of PCM.
test('a backlog of audio is uploaded as one request, not one per chunk', async () => {
  let release;
  const held = new Promise((r) => { release = r; });
  let posts = 0;

  const app = await load({
    routes: {
      'GET /api/dictation': { ready: true },
      'POST /api/dictation': { status: 201, body: { id: 'd-1' } },
      'POST /api/dictation/d-1/audio': async () => {
        posts++;
        if (posts === 1) await held; // the engine is busy finishing a sentence
        return { provisional: 'caught up' };
      },
    },
  });

  app.el('mic').click();
  await app.settle();

  // The first chunk is in flight and stuck; eight more pile up behind it.
  app.micChunk();
  for (let i = 0; i < 8; i++) app.micChunk();
  await app.settle();
  assert.equal(posts, 1, 'the queue was uploaded while the first request was still out');

  release();
  await app.settle();

  assert.equal(posts, 2, `the backlog took ${posts - 1} requests instead of one`);
  assert.equal(app.el('input').value, 'caught up');
});
