'use strict';

// The client-side slash commands, and the help text they print.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

// Regression, B7. Every line of HELP_TEXT is raw text that tryLocalCommand
// escapes exactly once on the way to the transcript. One entry used to be
// pre-escaped in the source, so it went through escapeHtml twice and the page
// showed a literal "&lt;skill name&gt;".
test('the help text is raw, not pre-escaped', async () => {
  const app = await load();
  assert.ok(Array.isArray(app.HELP_TEXT), app.HELP_TEXT);
  assert.ok(app.HELP_TEXT.some((line) => line.includes('/<skill name>')), app.HELP_TEXT);
  for (const line of app.HELP_TEXT) {
    assert.ok(!line.includes('&lt;'), line);
    assert.ok(!line.includes('&gt;'), line);
    assert.ok(!line.includes('&amp;'), line);
  }
});

test('/help renders every angle bracket escaped exactly once', async () => {
  const app = await load();
  assert.equal(await app.tryLocalCommand('/help'), true);

  const html = app.transcript();
  assert.ok(html.includes('/&lt;skill name&gt;'), html);
  assert.ok(!html.includes('&amp;lt;'), html);
  // The block is one <div> with <br> line breaks, not raw newlines.
  assert.ok(html.includes('<br>'), html);
});

test('/help does not reach the daemon', async () => {
  const app = await load();
  const before = app.calls.length;
  await app.tryLocalCommand('/help');
  assert.equal(app.calls.length, before);
});

test('exit and :q explain that the browser tab has to be closed', async () => {
  for (const text of ['exit', ':q', 'EXIT', ':Q']) {
    const app = await load();
    assert.equal(await app.tryLocalCommand(text), true, text);
    assert.match(app.transcript(), /Closing the browser tab ends the session/);
  }
});

test('/version asks the daemon and prints what it answers', async () => {
  const app = await load({ routes: { 'GET /api/version': { version: '1.2.3' } } });
  assert.equal(await app.tryLocalCommand('/version'), true);
  assert.match(app.transcript(), /localcode 1\.2\.3/);
});

test('/version reports a failure instead of printing nothing', async () => {
  const app = await load({ routes: { 'GET /api/version': { status: 500 } } });
  await app.tryLocalCommand('/version');
  assert.match(app.transcript(), /failed to fetch version/);
});

test('/agent lists the agents, escaped, and marks the current one', async () => {
  const app = await load();
  assert.equal(await app.tryLocalCommand('/agent'), true);
  const html = app.transcript();
  assert.match(html, /current: general-purpose/);
  assert.match(html, /- general-purpose: the default agent/);
  assert.match(html, /- plan: read-only planner/);
});

test('/agent escapes agent names and descriptions from config.json', async () => {
  const app = await load({
    routes: { 'GET /api/agents': [{ name: '<b>x</b>', description: '<img src=x>', model: 'm' }] },
  });
  await app.tryLocalCommand('/agent');
  const html = app.transcript();
  assert.ok(!html.includes('<img'), html);
  assert.ok(html.includes('&lt;b&gt;x&lt;/b&gt;'), html);
});

test('/agent <name> switches through the daemon', async () => {
  const app = await load();
  assert.equal(await app.tryLocalCommand('/agent plan'), true);
  const calls = app.callsTo('POST', '/api/sessions/sess-1/agent');
  assert.equal(calls.length, 1);
  assert.deepEqual(calls[0].body, { agent: 'plan' });
  // currentAgent only moves when the daemon says so, so every client agrees.
  assert.equal(app.state.currentAgent, 'general-purpose');
});

test('/agent <name> reports a refused switch', async () => {
  const app = await load({ routes: { 'POST /api/sessions/*/agent': { status: 400, body: { error: 'no such agent' } } } });
  await app.tryLocalCommand('/agent nope');
  assert.match(app.transcript(), /failed to switch agent/);
});

test('/commands says so when there are none, and lists them when there are', async () => {
  const empty = await load();
  await empty.tryLocalCommand('/commands');
  assert.match(empty.transcript(), /No custom commands registered/);

  const app = await load({ routes: { 'GET /api/commands': [{ name: 'review', description: 'review a diff' }] } });
  await app.tryLocalCommand('/commands');
  assert.match(app.transcript(), /- \/review: review a diff/);
});

// /config, /memory, /compact, /usage, /init, skills and custom commands are
// all handled by the daemon (agent.Loop): they must fall through so their
// replies and any config.changed event travel over SSE like everything else.
test('server-side commands are not intercepted', async () => {
  const app = await load();
  for (const text of ['/config', '/config show_tps off', '/memory', '/compact', '/usage', '/init', '/pdf-tools', 'hello']) {
    assert.equal(await app.tryLocalCommand(text), false, text);
  }
  assert.equal(app.transcript(), '');
});

test('a command is matched case-insensitively but sent as typed', async () => {
  const app = await load();
  assert.equal(await app.tryLocalCommand('/HELP'), true);
  assert.deepEqual(app.userTurns(), ['/HELP']);
});

test('isPlainPrompt separates chat from commands', async () => {
  const app = await load();
  assert.equal(app.isPlainPrompt('hello'), true);
  assert.equal(app.isPlainPrompt('/help'), false);
});
