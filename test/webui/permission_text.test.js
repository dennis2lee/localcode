'use strict';

// A permission question draws every field it carries as text: a character
// a browser acts on, or draws as nothing, is shown as an escape. The daemon
// writes the question that way, and the page reads the fields through the
// same rule, because a log written before the daemon did is still replayed
// and the directory and the rule are kept as they are in the log.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const RAW = /[\r\u001b\u202e\u200b\u034f\ufe0f]/u;

test('every field of a question is drawn with its hidden characters escaped', async () => {
  const app = await load();
  app.sse.emit({
    seq: 1,
    type: 'permission.request',
    data: {
      id: 'p1', tool: 'write_file', can_always: true,
      description: 'write /tmp/x\rHIDDEN\u001b[8mMORE\u202eABC\u200bZ',
      outside: 'write', outside_dir: '/tmp/\u001b[8msecret\u202e', workspace: '/work/\u034fproj',
      rule: '/tmp/\u202ex\ufe0f',
    },
  });

  const text = app.el('permission-text').textContent;
  assert.ok(!RAW.test(text), `the question text holds raw characters: ${JSON.stringify(text)}`);
  assert.ok(text.includes('write /tmp/x\\rHIDDEN\\x1b[8mMORE\\u202eABC\\u200bZ'), text);

  const outside = app.el('permission-outside').textContent;
  assert.ok(!RAW.test(outside), JSON.stringify(outside));
  assert.ok(outside.includes('/work/\\u034fproj'), outside);

  const dir = app.el('permission-allow-dir').textContent;
  assert.ok(!RAW.test(dir), JSON.stringify(dir));
  assert.ok(dir.includes('/tmp/\\x1b[8msecret\\u202e'), dir);
});

test('line breaks and tabs in a question stay as they are', async () => {
  const app = await load();
  app.sse.emit({
    seq: 1,
    type: 'permission.request',
    data: { id: 'p1', tool: 'Schedule', description: 'schedule for 09:00: run the tests\n\n\tthen the second thing' },
  });
  const text = app.el('permission-text').textContent;
  assert.ok(text.includes('run the tests\n\n\tthen the second thing'), JSON.stringify(text));
});

test('the rule an "always allow" would write is drawn escaped', async () => {
  const app = await load();
  const rlo = String.fromCodePoint(0x202e);
  app.sse.emit({
    seq: 1,
    type: 'permission.request',
    data: { id: 'p1', tool: 'write_file', description: 'write /tmp/x', can_always: true, rule: '/tmp/' + rlo + 'x' },
  });
  const title = app.el('permission-allow-always').title;
  assert.ok(!title.includes(rlo), JSON.stringify(title));
  assert.ok(title.includes('/tmp/' + String.fromCharCode(92) + 'u202ex'), title);
});
