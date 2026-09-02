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

// The list the daemon offers is the one the clients complete from, so a
// command added to the router and not to that list is uncompletable in
// both of them at once, with nothing failing. There is a guard on the Go
// side for the list being incomplete; this is the guard for the list
// being fetched and used at all.
test('a command the daemon added today completes without the page knowing about it', async () => {
  const app = await load({
    routes: {
      'GET /api/slash-commands': [
        { name: 'orchestrate', description: 'turn the Orchestrate tool on or off' },
        { name: 'smart-agent', description: 'turn the Smart Agent bundle on or off' },
      ],
    },
  });
  const input = typeAt(app, '/or');

  app.press('ArrowRight');
  assert.equal(input.value, '/orchestrate',
    'the page completes only what it hard-codes, so a new daemon command is unreachable');
});

// And an ambiguous prefix walks rather than guessing, across the kinds:
// somebody typing "/s" is not thinking about which list a name is in.
test('the walk crosses skills, custom commands and daemon commands alike', async () => {
  const app = await load({
    routes: {
      'GET /api/skills': [{ name: 'summarise' }],
      'GET /api/slash-commands': [
        { name: 'schedule', description: 'book a prompt for later' },
        { name: 'smart-agent', description: 'the bundle' },
      ],
    },
  });
  const input = typeAt(app, '/s');

  const seen = [];
  for (let i = 0; i < 4; i++) {
    app.press('ArrowRight');
    seen.push(input.value);
  }
  assert.ok(seen.includes('/summarise'), `a skill was skipped: ${seen}`);
  assert.ok(seen.includes('/schedule'), `a daemon command was skipped: ${seen}`);
  assert.ok(seen.includes('/smart-agent'), `a daemon command was skipped: ${seen}`);
  // The text typed is the last stop, so the walk is never a trap.
  assert.ok(seen.includes('/s'), `the walk never came back to what was typed: ${seen}`);
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

// typeCaretAt puts text in the box with the caret somewhere inside it,
// which is what completing mid-sentence means and what typeAt cannot say.
function typeCaretAt(app, text, caret) {
  const input = app.type(text);
  input.selectionStart = input.selectionEnd = caret;
  input.fire('input');
  return input;
}

// The shape the change exists for: a prompt that mentions a command
// rather than being one. Under the old rule this box was past completing,
// because a command was the whole prompt and this one has a sentence in
// front of it.
test('a command completes at the end of a sentence', async () => {
  const app = await load({ routes: { 'GET /api/skills': [{ name: 'tidy-context' }] } });
  const input = typeAt(app, 'read the mail, then run /tid');

  app.press('ArrowRight');
  assert.equal(input.value, 'read the mail, then run /tidy-context');
});

// And spliced, with the sentence carrying on past it. The caret lands
// after the name so the rest of what was written is still there.
test('a command completes with a sentence on both sides', async () => {
  const app = await load({ routes: { 'GET /api/skills': [{ name: 'tidy-context' }] } });
  const typed = 'read the mail, then run /tid';
  const input = typeCaretAt(app, typed + ' and tell me', typed.length);

  app.press('ArrowRight');
  assert.equal(input.value, 'read the mail, then run /tidy-context and tell me');
  assert.equal(input.selectionStart, 'read the mail, then run /tidy-context'.length,
    'the caret should sit after the name, not at the end of the sentence');
});

// A prompt written over several lines, which the box allows and the TUI's
// widget does not let us follow the caret through.
test('a command completes on the second line of a prompt', async () => {
  const app = await load({ routes: { 'GET /api/skills': [{ name: 'tidy-context' }] } });
  const input = typeAt(app, 'read the mail.\nif it is from 철수, run /tid');

  app.press('ArrowRight');
  assert.equal(input.value, 'read the mail.\nif it is from 철수, run /tidy-context');
});

// The rule that keeps paths out, and it is not a rule about paths: the
// scan takes the nearest slash and requires it to open a word, so a slash
// with a letter in front of it ends the search where it stands.
test('a slash inside a word is not a command', async () => {
  const app = await load({
    routes: { 'GET /api/slash-commands': [{ name: 'commands' }, { name: 'compact' }] },
  });
  for (const text of [
    'see internal/tui/com',  // a path, mid-word
    'see /Users/me/com',     // an absolute path, mid-word
    'a/com',                 // no whitespace in front of it
    'see / com',             // a bare slash is not a prefix
    '/com and then some',    // the word ended before the caret
  ]) {
    const input = typeAt(app, text);
    app.press('ArrowRight');
    assert.equal(input.value, text, `${text} started a completion`);
  }
});

// A quoted title may contain a slash, so the reference scan has to be
// asked first: taking whichever sigil sits nearest the caret would hand a
// half-typed title to the commands and complete it to nothing.
test('a slash inside a quoted title is still a reference', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': [
        { id: 's-here', title: 'this one' },
        { id: 's-slash', title: 'the /parser notes' },
      ],
    },
  });
  app.state.sessionID = 's-here';
  const input = typeAt(app, '#"the /par');

  app.press('ArrowRight');
  assert.equal(input.value, '#"the /parser notes"');
});
