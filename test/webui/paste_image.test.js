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

test('images over 10MB in one message are refused at paste time, naming the 10MB limit', async () => {
  const app = await load();
  const input = app.el('input');

  // Three images each within the one-image limit, together over the
  // message's.
  const each = 3500000;
  for (let i = 0; i < 3; i++) {
    input.fire('paste', { clipboardData: { items: [makeImageItem('image/png', each, 'big')] } });
    await app.settle();
  }

  const images = app.internals.getAttachedImages();
  assert.equal(images.length, 2, 'the image that passes 10MB must not be attached');
  const transcript = app.transcript();
  assert.ok(transcript.includes('10MB limit'), 'error must name the 10MB limit: ' + transcript);
});

test('an image past the one-image limit, on a page that cannot scale it, is refused naming the limit', async () => {
  const app = await load();
  const input = app.el('input');

  const ev = input.fire('paste', {
    clipboardData: { items: [makeImageItem('image/png', 11 * 1024 * 1024, 'big')] },
  });
  await app.settle();

  assert.equal(ev.defaultPrevented, true, 'oversized image paste must prevent default');
  assert.equal(app.internals.getAttachedImages().length, 0, 'oversized image must not be attached');
  const transcript = app.transcript();
  assert.ok(transcript.includes('11534336 bytes'), 'error must say how big it is: ' + transcript);
  assert.ok(transcript.includes('3750000 bytes and 8000 pixels a side'), 'error must name the limits: ' + transcript);
});

// A page with createImageBitmap and a canvas: a bitmap of the given size,
// and a canvas whose encoded copy is as many bytes as encodedSize says for
// the type asked. Records what was drawn and encoded.
function scalingGlobals(width, height, encodedSize) {
  const seen = { drawn: null, encoded: [], closed: 0 };
  class FakeOffscreenCanvas {
    constructor(w, h) {
      this.w = w;
      this.h = h;
    }
    getContext() {
      return { drawImage: (_bmp, x, y, w, h) => { seen.drawn = { x, y, w, h, canvas: [this.w, this.h] }; } };
    }
    async convertToBlob({ type, quality }) {
      seen.encoded.push({ type, quality });
      const size = encodedSize(type, quality);
      const bytes = new Uint8Array(Math.min(size, 16)).fill(7);
      return { type, size, arrayBuffer: async () => bytes.buffer };
    }
  }
  return {
    seen,
    globals: {
      createImageBitmap: async () => ({ width, height, close: () => { seen.closed++; } }),
      OffscreenCanvas: FakeOffscreenCanvas,
    },
  };
}

test('fitWithin keeps the shape and touches only what is too big', async () => {
  const app = await load();
  const { fitWithin } = app.internals;
  assert.deepEqual({ ...fitWithin(800, 600, 2576) }, { w: 800, h: 600 });
  assert.deepEqual({ ...fitWithin(5120, 2880, 2576) }, { w: 2576, h: 1449 });
  assert.deepEqual({ ...fitWithin(1000, 9000, 2576) }, { w: 286, h: 2576 });
  assert.deepEqual({ ...fitWithin(100000, 1, 2576) }, { w: 2576, h: 1 });
});

test('a screenshot past the one-image limit is scaled down and attached as PNG', async () => {
  const { seen, globals } = scalingGlobals(5120, 2880, () => 2000000);
  const app = await load({ globals });
  const input = app.el('input');

  input.fire('paste', { clipboardData: { items: [makeImageItem('image/png', 6000000, 'big')] } });
  await app.settle();

  const images = app.internals.getAttachedImages();
  assert.equal(images.length, 1, 'the scaled copy must be attached: ' + app.transcript());
  assert.equal(images[0].mediaType, 'image/png');
  assert.equal(images[0].size, 2000000);
  assert.deepEqual(seen.drawn, { x: 0, y: 0, w: 2576, h: 1449, canvas: [2576, 1449] });
  assert.deepEqual(seen.encoded, [{ type: 'image/png', quality: undefined }]);
  assert.equal(seen.closed, 1, 'the bitmap must be released');
});

test('an image too tall to send is scaled down even when its bytes are small', async () => {
  const { seen, globals } = scalingGlobals(1000, 9000, () => 300000);
  const app = await load({ globals });
  app.el('input').fire('paste', { clipboardData: { items: [makeImageItem('image/png', 900000, 'tall')] } });
  await app.settle();

  assert.equal(app.internals.getAttachedImages().length, 1);
  assert.deepEqual(seen.drawn, { x: 0, y: 0, w: 286, h: 2576, canvas: [286, 2576] });
});

test('a copy that is still too big as PNG goes as JPEG, and one too big as both is refused', async () => {
  {
    const { seen, globals } = scalingGlobals(5120, 2880, (type) => (type === 'image/png' ? 5000000 : 1500000));
    const app = await load({ globals });
    app.el('input').fire('paste', { clipboardData: { items: [makeImageItem('image/png', 9000000, 'photo')] } });
    await app.settle();
    const images = app.internals.getAttachedImages();
    assert.equal(images.length, 1);
    assert.equal(images[0].mediaType, 'image/jpeg');
    assert.deepEqual(seen.encoded.map((e) => e.type), ['image/png', 'image/jpeg']);
  }
  {
    const { globals } = scalingGlobals(5120, 2880, () => 5000000);
    const app = await load({ globals });
    app.el('input').fire('paste', { clipboardData: { items: [makeImageItem('image/png', 9000000, 'noise')] } });
    await app.settle();
    assert.equal(app.internals.getAttachedImages().length, 0);
    assert.ok(app.transcript().includes('a 2576x1449 copy is still too big'), app.transcript());
  }
});

// Found in review: a canvas that could not be made or drawn on left the
// decoded bitmap, over 100 MB at 9000x4000, to the garbage collector.
for (const [failure, says] of [
  ['the canvas cannot be made', /could not read image: Error: allocation failed/],
  ['the canvas has no 2d context', /this page cannot make a smaller copy/],
  ['drawing throws', /could not read image: Error: draw failed/],
]) {
  test(`the decoded bitmap is released when ${failure}`, async () => {
    let closed = 0;
    const app = await load({
      globals: {
        createImageBitmap: async () => ({ width: 9000, height: 4000, close() { closed++; } }),
        OffscreenCanvas: class {
          constructor() {
            if (failure === 'the canvas cannot be made') throw new Error('allocation failed');
          }
          getContext() {
            if (failure === 'the canvas has no 2d context') return null;
            return { drawImage() { throw new Error('draw failed'); } };
          }
        },
      },
    });
    app.el('input').fire('paste', { clipboardData: { items: [makeImageItem('image/png', 6000000, 'big')] } });
    await app.settle();
    assert.equal(app.internals.getAttachedImages().length, 0);
    assert.match(app.transcript(), says);
    assert.equal(closed, 1, 'the bitmap must be released once');
  });
}

// Found in review (agy): a PNG encode that threw ended the attempt, and
// the JPEG that would have fitted was never tried.
test('a copy whose PNG encode fails goes as JPEG', async () => {
  const { seen, globals } = scalingGlobals(5120, 2880, () => 1500000);
  const realCanvas = globals.OffscreenCanvas;
  globals.OffscreenCanvas = class extends realCanvas {
    async convertToBlob(opts) {
      if (opts.type === 'image/png') {
        seen.encoded.push(opts);
        throw new Error('PNG encoding failed');
      }
      return super.convertToBlob(opts);
    }
  };
  const app = await load({ globals });
  app.el('input').fire('paste', { clipboardData: { items: [makeImageItem('image/png', 9000000, 'big')] } });
  await app.settle();
  const images = app.internals.getAttachedImages();
  assert.equal(images.length, 1, app.transcript());
  assert.equal(images[0].mediaType, 'image/jpeg');
});

// Found in review (agy): two images pasted together each passed the 10 MB
// check before either was added, and together went over it.
test('images pasted together stay within the 10MB message total', async () => {
  const app = await load();
  const input = app.el('input');
  for (let i = 0; i < 4; i++) {
    input.fire('paste', { clipboardData: { items: [makeImageItem('image/png', 3000000, 'big')] } });
  }
  await app.settle();
  const total = app.internals.getAttachedImages().reduce((sum, img) => sum + img.size, 0);
  assert.equal(app.internals.getAttachedImages().length, 3);
  assert.ok(total <= 10 * 1024 * 1024, `attached ${total} bytes`);
  assert.ok(app.transcript().includes('10MB limit'), app.transcript());
});

test('an image within the limits is attached as it is, without a copy', async () => {
  const { seen, globals } = scalingGlobals(1920, 1080, () => 1);
  const app = await load({ globals });
  app.el('input').fire('paste', { clipboardData: { items: [makeImageItem('image/png', 512, SAMPLE_PNG_B64)] } });
  await app.settle();

  const images = app.internals.getAttachedImages();
  assert.equal(images.length, 1);
  assert.equal(images[0].data, SAMPLE_PNG_B64);
  assert.equal(seen.drawn, null);
  assert.equal(seen.closed, 1, 'the bitmap must be released');
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

function makeDropFile(type = 'image/png', size = 512, base64 = SAMPLE_PNG_B64, name = 'screenshot.png') {
  return { type, size, base64, name };
}

test('dropping a PNG attaches it as an image, not as a file path', async () => {
  const app = await load();
  const input = app.el('input');

  input.fire('drop', {
    dataTransfer: { files: [makeDropFile('image/png', 512, SAMPLE_PNG_B64, 'screenshot.png')] },
  });
  await app.settle();

  const images = app.internals.getAttachedImages();
  assert.equal(images.length, 1, 'dropped PNG should be attached as an image');
  assert.equal(images[0].mediaType, 'image/png');
  assert.equal(images[0].data, SAMPLE_PNG_B64);
  assert.equal(input.value, '', 'no file-path text should be inserted for an image');

  const container = app.el('center').querySelector('.composer-attachments');
  assert.ok(container, 'composer-attachments element should exist');
  assert.equal(container.querySelectorAll('.attachment-thumb').length, 1);
});

test('dropping a non-image still uploads it and names it by path', async () => {
  let uploaded = null;
  const app = await load({
    routes: {
      'POST /api/sessions/*/uploads': (body) => {
        uploaded = body;
        return { path: '/uploads/sess-1/notes.txt' };
      },
    },
  });
  const input = app.el('input');

  input.fire('drop', {
    dataTransfer: { files: [{ type: 'text/plain', size: 5, name: 'notes.txt' }] },
  });
  await app.settle();

  assert.equal(app.internals.getAttachedImages().length, 0, 'text file must not become an image');
  assert.ok(input.value.includes('[attached file: /uploads/sess-1/notes.txt]'), 'text file keeps the path reference: ' + input.value);
});

test('a dropped image sends through the image wire, like a pasted one', async () => {
  const app = await load();
  const input = app.el('input');

  input.fire('drop', {
    dataTransfer: { files: [makeDropFile('image/png', 512, SAMPLE_PNG_B64, 'screenshot.png')] },
  });
  await app.settle();

  app.type('what is in this image');
  await app.el('send').click();
  await app.settle();

  const calls = app.callsTo('POST', '/api/sessions/sess-1/messages');
  assert.equal(calls.length, 1);
  assert.equal(calls[0].body.text, 'what is in this image');
  assert.ok(Array.isArray(calls[0].body.images));
  assert.equal(calls[0].body.images.length, 1);
  assert.equal(calls[0].body.images[0].media_type, 'image/png');
  assert.equal(calls[0].body.images[0].data, SAMPLE_PNG_B64);
});
