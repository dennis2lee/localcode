'use strict';

// The sidebars and the status line, which are the parts of the page that
// render values coming straight off the wire.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const XSS = '<img src=x onerror="alert(1)">';

// Regression, B5. renderTasks used to build its rows with an innerHTML
// template string, splicing t.agent and t.status — both taken verbatim from
// task.spawned / task.status SSE payloads, i.e. from a subagent name the
// model itself chooses — into markup unescaped. It was the one listing in the
// file that did.
test('a task whose agent name contains markup renders as text, not as an element', async () => {
  const app = await load();
  app.state.tasks.set('task-1', { agent: XSS, status: 'running' });
  app.renderTasks();

  const html = app.el('tasks').innerHTML;
  assert.ok(!html.includes('<img'), html);
  assert.ok(html.includes('&lt;img src=x onerror="alert(1)"&gt;'), html);
});

test('a task id containing markup is escaped too', async () => {
  const app = await load();
  app.state.tasks.set(XSS, { agent: 'plan', status: 'done' });
  app.renderTasks();
  assert.ok(!app.el('tasks').innerHTML.includes('<img'), app.el('tasks').innerHTML);
});

test('the task status becomes a class, so an odd status cannot inject one', async () => {
  const app = await load();
  app.state.tasks.set('t', { agent: 'plan', status: 'running" onmouseover="x' });
  app.renderTasks();
  const html = app.el('tasks').innerHTML;
  assert.ok(!html.includes('onmouseover="x"'), html);
  assert.ok(html.includes('&quot;'), html);
});

test('an empty task list says so', async () => {
  const app = await load();
  app.renderTasks();
  assert.match(app.el('tasks').innerHTML, /none/);
});

test('the status line names the agent and the model from /api/agents', async () => {
  const app = await load();
  app.renderStatusBar();
  const text = app.el('status-text').textContent;
  assert.match(text, /agent: general-purpose/);
  assert.match(text, /model: test-model-1/);
});

test('a usage event puts context percent and tok/s on the status line', async () => {
  const app = await load();
  app.applyEvent({ type: 'usage', data: { percent: 42.25, tps: 13.5, model: 'reported-model' } });
  const text = app.el('status-text').textContent;
  assert.match(text, /model: reported-model/); // what the model reported wins
  assert.match(text, /context: 42\.3%/);
  assert.match(text, /13\.5 tok\/s/);
});

test('context usage colours the status bar at 70% and 90%', async () => {
  const app = await load();
  const bar = app.el('prompt-status');

  app.applyEvent({ type: 'usage', data: { percent: 50 } });
  assert.equal(bar.classList.contains('ctx-warn'), false);
  assert.equal(bar.classList.contains('ctx-crit'), false);

  app.applyEvent({ type: 'usage', data: { percent: 75 } });
  assert.ok(bar.classList.contains('ctx-warn'));
  assert.equal(bar.classList.contains('ctx-crit'), false);

  app.applyEvent({ type: 'usage', data: { percent: 95 } });
  assert.ok(bar.classList.contains('ctx-crit'));
  assert.equal(bar.classList.contains('ctx-warn'), false);
});

test('a running tool and a queue are both reported while waiting', async () => {
  const app = await load();
  app.state.promptQueue = ['later'];
  app.state.runningTool = 'bash';
  app.state.waiting = true;
  app.renderStatusBar();
  assert.match(app.el('status-text').textContent, /bash… esc to cancel \(1 queued\)/);
});

test('the permission pill counts rules, and shouts when prompts are skipped', async () => {
  const app = await load();

  app.state.permissionRules = { bash: [{ match: 'npm *', decision: 'allow' }], read: [{ match: '*', decision: 'ask' }] };
  app.renderPermissionStatus();
  assert.equal(app.el('permission-status-btn').textContent, 'permissions: ask (2 rules)');

  app.state.skipPermissions = true;
  app.renderPermissionStatus();
  assert.equal(app.el('permission-status-btn').textContent, 'permissions: skip');
  assert.ok(app.el('permission-status-btn').classList.contains('skip'));
});

test('auto-delegate on with no target agent says it is doing nothing', async () => {
  const app = await load();
  app.state.autoDelegate = true;
  app.state.autoDelegateAgent = '';
  app.renderAutoDelegate();
  assert.equal(app.el('auto-delegate-btn').textContent, 'auto-delegate: on (unconfigured)');

  app.state.autoDelegateAgent = 'plan';
  app.state.autoDelegateMatch = ['what is *'];
  app.renderAutoDelegate();
  assert.equal(app.el('auto-delegate-btn').textContent, 'auto-delegate: on');
  assert.match(app.el('auto-delegate-btn').title, /"plan" agent \(1 pattern\)/);
});

test('MCP server names and error details render as text', async () => {
  const app = await load();
  app.state.mcpServers = [
    { name: XSS, status: 'disconnected', detail: XSS },
    { name: 'filesystem', status: 'connected' },
  ];
  app.renderMCPServers();
  const html = app.el('mcp-servers').innerHTML;
  assert.ok(!html.includes('<img'), html);
  assert.ok(html.includes('filesystem'), html);
});

// The light is the whole point of the row: green when the server answered,
// blinking green while degraded, grey once it is down. A configured server
// that never came up has to appear too — omitting it made a broken server
// look like one nobody had set up.
test('each server gets a light matching its status', async () => {
  const app = await load();
  app.state.mcpServers = [
    { name: 'alive', status: 'connected' },
    { name: 'flaky', status: 'degraded', detail: 'call failed' },
    { name: 'dead', status: 'disconnected', detail: 'connection refused' },
  ];
  app.renderMCPServers();
  const html = app.el('mcp-servers').innerHTML;
  assert.match(html, /led led-connected/);
  assert.match(html, /led led-degraded/);
  assert.match(html, /led led-disconnected/);
  assert.ok(html.includes('dead'), html);
});

test('a status event replaces the whole list and re-renders', async () => {
  const app = await load();
  app.applyEvent({
    type: 'mcp.status',
    data: { servers: [{ name: 'filesystem', status: 'degraded', detail: 'timed out' }] },
  });
  const html = app.el('mcp-servers').innerHTML;
  assert.match(html, /led-degraded/);
  assert.ok(html.includes('filesystem'), html);
});

test('session titles and workspaces render as text', async () => {
  const app = await load();
  app.state.sessions = [{ id: 's1', title: XSS, workspace: XSS, created_at: '2026-01-02T03:04:05Z' }];
  app.renderSessionList();
  const html = app.el('session-list').innerHTML;
  assert.ok(!html.includes('<img'), html);
  assert.ok(html.includes('&lt;img'), html);
});

test('shortenPath keeps the tail and cuts at a separator', async () => {
  const app = await load();
  assert.equal(app.shortenPath('/short/path'), '/short/path');
  const long = '/Users/someone/very/deeply/nested/projects/localcode';
  const short = app.shortenPath(long);
  assert.ok(short.length <= 32, short);
  assert.ok(short.startsWith('…'), short);
  assert.ok(long.endsWith(short.slice(1)), short);
});

// Regression: switching agents mid-session left the previous agent's model
// on the status line. The usage event's model is preferred over the
// configured one — correct while one agent is answering, wrong the moment
// the agent changes, because no new usage arrives until the next turn ends.
test('switching agents updates the model even before the new one has answered', async () => {
  const app = await load();
  app.applyEvent({ type: 'usage', data: { percent: 10, model: 'test-model-1' } });
  app.applyEvent({ type: 'agent.switched', data: { agent: 'plan' } });
  const text = app.el('status-text').textContent;
  assert.match(text, /agent: plan/);
  assert.match(text, /model: test-model-2/, text);
});
