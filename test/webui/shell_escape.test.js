'use strict';

// A "!" line runs a shell command the person typed, so its output must
// read as command output rather than as anything the model said. The
// daemon carries the output on message.part.end with a shell_command,
// and the page draws that as a tool line instead of a model message.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

test('command output draws as a tool line naming the command', async () => {
  const app = await load();

  app.sse.emit({ seq: 1, type: 'message.user', data: { text: '!git status' } });
  app.sse.emit({ seq: 2, type: 'message.part.end', data: { text: ' M main.go', shell_command: 'git status' } });
  await app.settle();

  const html = app.transcript();
  assert.ok(html.includes('msg-tool'), 'the output should draw as a tool line, not a model message');
  assert.ok(html.includes('$ git status'), 'the line should name the command that ran');
  assert.ok(html.includes('M main.go'), 'the line should hold the command output');
  assert.ok(!html.includes('msg-model'), 'no model message should be drawn for command output');
});

test('an ordinary reply still draws as a model message', async () => {
  const app = await load();

  app.sse.emit({ seq: 1, type: 'message.part.end', data: { text: 'hello there' } });
  await app.settle();

  const html = app.transcript();
  assert.ok(html.includes('msg-model'), 'a reply without shell_command should draw as a model message');
  assert.ok(!html.includes('msg-tool'), 'a plain reply should draw no tool line');
});
