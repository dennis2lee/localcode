'use strict';

// Sending a prompt while dictating.
//
// Sending finishes the sentence by definition, so the microphone goes off
// with it — otherwise the words spoken while the reply is being read land
// in a box that was just emptied, and the person finds them there some
// minutes later with no idea where they came from.
//
// Enter did this and the Send button did not. That split is invisible in
// the code — two controls, one of them wired straight to sendMessage — and
// invisible in use until you happen to click rather than press.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const ROUTES = {
  'GET /api/dictation': { ready: true, detail: '', language: 'en', remote: false, can_save: true },
  'POST /api/dictation': { id: 'd-1' },
  'POST /api/dictation/d-1/audio': { provisional: 'hello there' },
  'POST /api/dictation/d-1/stop': { final: 'hello there' },
};

async function dictate(app) {
  app.el('mic').click();
  await app.settle();
  app.micChunk(new ArrayBuffer(64));
  await app.settle();
}

test('the send button switches dictation off', async () => {
  const app = await load({ routes: ROUTES });
  await dictate(app);
  assert.equal(app.isDictating(), true, 'the microphone should be on to begin with');

  app.el('send').click();
  await app.settle();

  assert.equal(app.isDictating(), false, 'sending left the microphone running');
  assert.equal(app.el('mic').textContent, '\u{1F3A4} dictation: off');
});

// And what was still being transcribed goes with the prompt rather than
// arriving in the empty box behind it.
test('the sentence in progress is sent, not left behind', async () => {
  const app = await load({ routes: ROUTES });
  await dictate(app);

  app.el('send').click();
  await app.settle();

  const sent = app.callsTo('POST', /\/api\/sessions\/.*\/messages/);
  assert.equal(sent.length, 1, 'the prompt was not sent');
  assert.match(sent[0].body.text, /hello there/,
    'the dictated sentence did not make it into the prompt that was sent');
  assert.equal(app.el('input').value, '', 'the box should be empty after sending');
});

// Enter has always done this, and must keep doing it: both controls now
// go through one path, and a test for only one of them would not notice
// if that path stopped being shared.
test('Enter switches dictation off too', async () => {
  const app = await load({ routes: ROUTES });
  await dictate(app);

  app.press('Enter');
  await app.settle();

  assert.equal(app.isDictating(), false, 'Enter left the microphone running');
});
