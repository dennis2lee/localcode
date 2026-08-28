'use strict';

// Completing "/<name>" with the right arrow, the Web UI half of the same
// feature the TUI has. The interesting case is the ambiguous prefix: the
// shell answer of "complete to the longest common prefix" tells you
// nothing when the candidates are "pdf-tools" and "pptx", so the same key
// walks them instead.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const skills = [
  { name: 'pdf-tools', description: 'work with PDFs' },
  { name: 'plan-review', description: 'review a plan' },
  { name: 'pptx', description: 'slide decks' },
];

// typeAt puts text in the box with the caret at the end, which is the
// only place the right arrow means completion, and fires the input event
// a browser fires when somebody actually types. The hint under the box
// follows what is in the box, so it hangs off that event.
function typeAt(app, text) {
  const input = app.type(text);
  input.selectionStart = input.selectionEnd = text.length;
  input.fire('input');
  return input;
}

test('the right arrow finishes a skill name', async () => {
  const app = await load({ routes: { 'GET /api/skills': skills } });
  const input = typeAt(app, '/pdf');

  app.press('ArrowRight');
  assert.equal(input.value, '/pdf-tools');
});

test('the right arrow walks every candidate and comes back round', async () => {
  const app = await load({ routes: { 'GET /api/skills': skills } });
  const input = typeAt(app, '/p');

  const seen = [];
  for (let i = 0; i < 4; i++) {
    app.press('ArrowRight');
    seen.push(input.value);
    input.selectionStart = input.selectionEnd = input.value.length;
  }
  assert.deepEqual(seen, ['/pdf-tools', '/plan-review', '/pptx', '/p'],
    'the walk should offer each match and then what was typed');

  app.press('ArrowRight');
  assert.equal(input.value, '/pdf-tools', 'the walk should be a ring, not a dead end');
});

test('editing starts a new walk', async () => {
  const app = await load({ routes: { 'GET /api/skills': skills } });
  const input = typeAt(app, '/p');
  app.press('ArrowRight');
  assert.equal(input.value, '/pdf-tools');

  typeAt(app, '/pl');
  app.press('ArrowRight');
  assert.equal(input.value, '/plan-review', 'the new prefix should decide the match');
});

test('custom commands complete alongside skills', async () => {
  const app = await load({
    routes: {
      'GET /api/skills': [{ name: 'review-diff' }],
      'GET /api/commands': [{ name: 'release' }],
    },
  });
  const input = typeAt(app, '/re');

  app.press('ArrowRight');
  const first = input.value;
  input.selectionStart = input.selectionEnd = first.length;
  app.press('ArrowRight');
  assert.deepEqual([first, input.value], ['/review-diff', '/release'],
    'both lists are invoked the same way, so both complete');
});

test('the right arrow still moves the caret when it has somewhere to go', async () => {
  const app = await load({ routes: { 'GET /api/skills': skills } });
  const input = app.type('/pdf');
  input.selectionStart = input.selectionEnd = 0;

  app.press('ArrowRight');
  assert.equal(input.value, '/pdf', 'completion should not fire from inside the text');

  // And a prompt with arguments is past completing: the skill is chosen
  // and what follows is the request.
  typeAt(app, '/pdf split this');
  app.press('ArrowRight');
  assert.equal(input.value, '/pdf split this');
});

test('nothing to complete leaves the key alone', async () => {
  const app = await load({ routes: { 'GET /api/skills': skills } });
  const input = typeAt(app, '/zz');
  app.press('ArrowRight');
  assert.equal(input.value, '/zz');
});

test('the status line counts the candidates before you press anything', async () => {
  const app = await load({ routes: { 'GET /api/skills': skills } });
  typeAt(app, '/p');
  assert.match(app.el('status-text').textContent, /3 matches/,
    'an ambiguous prefix should look ambiguous');

  typeAt(app, '/pd');
  assert.match(app.el('status-text').textContent, /→ \/pdf-tools/);
  // The rest of the line stays: this one line also carries the busy
  // indicator, and losing it the moment you type is losing it exactly
  // when you are watching it.
  assert.match(app.el('status-text').textContent, /agent:/);

  // With nothing being completed, the hint is simply absent.
  typeAt(app, 'hello');
  assert.doesNotMatch(app.el('status-text').textContent, /matches/);
});

test('the daemon\'s own commands complete like a skill does', async () => {
  const app = await load({
    routes: {
      'GET /api/slash-commands': [
        { name: 'smart-agent', description: 'turn the Smart Agent bundle on or off' },
        { name: 'skill', description: 'list installed skills' },
      ],
    },
  });
  const input = typeAt(app, '/sm');

  app.press('ArrowRight');
  assert.equal(input.value, '/smart-agent',
    'the three switches got a command each so they could be reached from a prompt');
});

test('a name shared by a skill and a command is offered once', async () => {
  const app = await load({
    routes: {
      'GET /api/skills': [{ name: 'review' }],
      'GET /api/commands': [{ name: 'review' }],
    },
  });
  const input = typeAt(app, '/rev');

  app.press('ArrowRight');
  const first = input.value;
  input.selectionStart = input.selectionEnd = first.length;
  app.press('ArrowRight');
  assert.deepEqual([first, input.value], ['/review', '/rev'],
    'offering one name twice looks like the key stopped working');
});
