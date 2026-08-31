'use strict';

// Completing "#<conversation>" with the right arrow.
//
// The same key as "/", and deliberately, but not the same rule. A command
// is the whole prompt, so completing one can replace the box. A reference
// is a word inside a sentence — "check #S2 against the file here" is the
// shape the feature exists for — so it has to be found where the caret is
// and spliced back where it was.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const SESSIONS = [
  { id: 'sess-1', title: 'this one', agent: 'general-purpose', workspace: '/w' },
  { id: 's-alpha', title: 'the parser rewrite', agent: 'general-purpose', workspace: '/a' },
  { id: 's-beta', title: 'the parser tests', agent: 'general-purpose', workspace: '/b' },
  { id: 's-gamma', title: '', agent: 'general-purpose', workspace: '/c' },
];

// typeAt puts text in the box with the caret somewhere in it. Unlike the
// "/" case the caret is a parameter, because where it is decides which
// word is being completed.
function typeAt(app, text, caret = text.length) {
  const input = app.type(text);
  input.selectionStart = input.selectionEnd = caret;
  input.fire('input');
  return input;
}

async function open(routes = {}) {
  return load({ routes: { 'GET /api/sessions': SESSIONS, ...routes } });
}

test('a reference completes in the middle of a sentence and leaves the sentence alone', async () => {
  const app = await open();
  const typed = 'check #"the parser r';
  const input = typeAt(app, typed + ' against the file here', typed.length);

  app.press('ArrowRight');
  assert.equal(input.value, 'check #"the parser rewrite" against the file here');
  // The caret lands after the name rather than at the end of the box, so
  // typing carries on where the reference ended.
  assert.equal(input.selectionStart, 'check #"the parser rewrite"'.length);
});

test('an unquoted reference completes to a quoted name when the title has spaces', async () => {
  const app = await open();
  const input = typeAt(app, '#the');
  app.press('ArrowRight');
  assert.equal(input.value, '#"the parser rewrite"');
});

test('the arrow walks every conversation and comes back to what you typed', async () => {
  const app = await open();
  const input = typeAt(app, '#"the parser');

  const seen = [];
  for (let i = 0; i < 4; i++) {
    app.press('ArrowRight');
    seen.push(input.value);
    input.selectionStart = input.selectionEnd = input.value.length;
  }
  assert.deepEqual(seen, [
    '#"the parser rewrite"',
    '#"the parser tests"',
    '#"the parser',
    '#"the parser rewrite"',
  ]);
});

test('an untitled conversation completes by its id', async () => {
  const app = await open();
  const input = typeAt(app, '#s-ga');
  app.press('ArrowRight');
  assert.equal(input.value, '#s-gamma');
});

// Referring to the conversation you are in resolves to "there is nothing
// to read", so offering it would be offering a mistake.
test('the conversation you are in is not offered', async () => {
  const app = await open();
  const input = typeAt(app, '#this');
  app.press('ArrowRight');
  assert.equal(input.value, '#this', 'the current conversation was offered');
});

// The archive is where the conversation whose name you cannot remember
// actually is, and referring to one is reading, which archiving never
// refuses.
test('archived conversations complete too', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': (body, { query }) =>
        query.get('archived')
          ? [{ id: 's-old', title: 'last month', agent: 'general-purpose', workspace: '/o' }]
          : SESSIONS,
    },
  });
  const input = typeAt(app, '#last');
  app.press('ArrowRight');
  assert.equal(input.value, '#"last month"', 'an archived conversation was not offered');
});

// The hash that is not a reference. Every one of these appears in ordinary
// prose and none of them should start a walk.
test('a hash that is not a reference is left alone', async () => {
  const app = await open();
  for (const [text, caret] of [
    ['# heading', 9],
    ['see issue #42', 13],
    ['the colour is #fff', 18],
    ['a#b', 3],
    ['#"the parser rewrite" ', 22],
    ['#the parser', 11],
  ]) {
    const input = typeAt(app, text, caret);
    app.press('ArrowRight');
    assert.equal(input.value, text, `"${text}" started a completion`);
  }
});

// The relaxation this needed, and its limit: the arrow is still a cursor
// key inside a word.
test('the arrow still moves the caret when it is inside a word', async () => {
  const app = await open();
  const text = 'check #then against it';
  const input = typeAt(app, text, 9);
  app.press('ArrowRight');
  assert.equal(input.value, text);
});

test('the status line counts the conversations before anything is pressed', async () => {
  const app = await open();
  typeAt(app, 'look at #"the parser');
  assert.match(app.el('status-text').textContent, /2 matches/);

  typeAt(app, 'look at #"the parser r');
  assert.match(app.el('status-text').textContent, /→ #"the parser rewrite"/);
});
