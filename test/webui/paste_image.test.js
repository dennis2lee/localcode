'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const SAMPLE_PNG_B64 = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==';

function makeImageItem(type = 'image/png', size = 1024, base64 = SAMPLE_PNG_B64) {
  return {
    kind: 'file',
    type,
    getAsFile: () => ({
      type,
      size,
      base64,
      name: 'test.png',
    }),
  };
}

function makeTextItem(type = 'text/plain') {
  return {
    kind: 'string',
    type,
  };
}

test('pasting an image attaches it to the message being composed', async () => {
  const app = await load();
  const input = app.el('input');

  const ev = input.fire('paste', {
    clipboardData: {
      items: [makeImageItem('image/png', 512, SAMPLE_PNG_B64)],
    },
  });

  await app.settle();

  assert.equal(ev.defaultPrevented, true, 'image paste should prevent default text insertion');
  const images = app.internals.getAttachedImages();
  assert.equal(images.length, 1, 'should have 1 attached image');
  assert.equal(images[0].mediaType, 'image/png');
  assert.equal(images[0].data, SAMPLE_PNG_B64);

  // The attachment thumbnail preview is visible in the container
  const container = app.el('center').querySelector('.composer-attachments');
  assert.ok(container, 'composer-attachments element should exist');
  assert.equal(container.style.display, 'flex');
  const thumbs = container.querySelectorAll('.attachment-thumb');
  assert.equal(thumbs.length, 1);
  assert.equal(thumbs[0].src, `data:image/png;base64,${SAMPLE_PNG_B64}`);
});

test('pasting text keeps doing what it does and does not attach', async () => {
  const app = await load();
  const input = app.el('input');

  const ev = input.fire('paste', {
    clipboardData: {
      items: [makeTextItem('text/plain')],
    },
  });

  await app.settle();

  assert.equal(ev.defaultPrevented, false, 'text paste must not prevent default');
  const images = app.internals.getAttachedImages();
  assert.equal(images.length, 0, 'no images should be attached');
});

test('an unsupported image type is refused at paste time, naming the type', async () => {
  const app = await load();
  const input = app.el('input');

  const ev = input.fire('paste', {
    clipboardData: {
      items: [makeImageItem('image/bmp', 2048, 'Qk0=')],
    },
  });

  await app.settle();

  assert.equal(ev.defaultPrevented, true, 'unsupported image paste must prevent default');
  const images = app.internals.getAttachedImages();
  assert.equal(images.length, 0, 'unsupported image must not be attached');

  const transcript = app.transcript();
  assert.ok(transcript.includes('unsupported image type "image/bmp"'), 'error must name the unsupported type: ' + transcript);
  assert.ok(transcript.includes('only PNG, JPEG, GIF, and WEBP'), 'error must state supported types');
});

test('an oversized image is refused at paste time, naming the 10MB limit', async () => {
  const app = await load();
  const input = app.el('input');

  const oversizedBytes = 11 * 1024 * 1024; // 11MB
  const ev = input.fire('paste', {
    clipboardData: {
      items: [makeImageItem('image/png', oversizedBytes, 'big')],
    },
  });

  await app.settle();

  assert.equal(ev.defaultPrevented, true, 'oversized image paste must prevent default');
  const images = app.internals.getAttachedImages();
  assert.equal(images.length, 0, 'oversized image must not be attached');

  const transcript = app.transcript();
  assert.ok(transcript.includes('10MB limit'), 'error must name the 10MB limit: ' + transcript);
});

test('removing an attachment leaves the typed text alone', async () => {
  const app = await load();
  const input = app.el('input');

  app.type('typed instructions before removing image');

  input.fire('paste', {
    clipboardData: {
      items: [makeImageItem('image/png', 512, SAMPLE_PNG_B64)],
    },
  });
  await app.settle();

  assert.equal(app.internals.getAttachedImages().length, 1);
  assert.equal(input.value, 'typed instructions before removing image');

  const container = app.el('center').querySelector('.composer-attachments');
  const removeBtn = container.querySelector('.attachment-remove');
  assert.ok(removeBtn, 'remove button must be present');

  removeBtn.click();
  await app.settle();

  assert.equal(app.internals.getAttachedImages().length, 0, 'attachment should be removed');
  assert.equal(input.value, 'typed instructions before removing image', 'typed text must be left alone');
});

test('sending a message with an attached image sends image payload and renders in transcript', async () => {
  const app = await load();
  const input = app.el('input');

  input.fire('paste', {
    clipboardData: {
      items: [makeImageItem('image/png', 512, SAMPLE_PNG_B64)],
    },
  });
  await app.settle();

  app.type('look at this screenshot');
  await app.el('send').click();
  await app.settle();

  const calls = app.callsTo('POST', '/api/sessions/sess-1/messages');
  assert.equal(calls.length, 1);
  assert.equal(calls[0].body.text, 'look at this screenshot');
  assert.ok(Array.isArray(calls[0].body.images));
  assert.equal(calls[0].body.images.length, 1);
  assert.equal(calls[0].body.images[0].media_type, 'image/png');
  assert.equal(calls[0].body.images[0].data, SAMPLE_PNG_B64);

  // After sending, attachments are cleared from composer
  assert.equal(app.internals.getAttachedImages().length, 0);

  // Daemon confirms with message.user
  app.sse.emit({
    seq: 1,
    type: 'message.user',
    data: {
      text: 'look at this screenshot',
      images: [{ media_type: 'image/png', data: SAMPLE_PNG_B64 }],
    },
  });
  await app.settle();

  const userBlock = app.el('transcript').querySelector('.msg-user');
  assert.ok(userBlock, 'user block must exist in transcript');
  assert.ok(userBlock.textContent.includes('look at this screenshot'));
  const userImgs = userBlock.querySelectorAll('.msg-image');
  assert.equal(userImgs.length, 1);
  assert.equal(userImgs[0].src, `data:image/png;base64,${SAMPLE_PNG_B64}`);
});
