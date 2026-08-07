'use strict';

// Dragging the handle between a side panel and the transcript. The width
// is written inline on the panel and remembered in localStorage; the
// bounds are what keep a drag from making either panel useless.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

// A drag is pointerdown on the handle, then moves and the release on the
// document — the pointer leaves the 4px handle almost immediately, which
// is the whole reason the listeners live on the document.
function drag(app, handleId, fromX, toX) {
  app.el(handleId).fire('pointerdown', { button: 0, clientX: fromX });
  app.doc.fire('pointermove', { clientX: toX });
  app.doc.fire('pointerup', { clientX: toX });
}

test('dragging the left handle right widens the left panel', async () => {
  const app = await load();
  app.el('left-panel').style.width = '260px';
  drag(app, 'resize-left', 260, 360);
  assert.equal(app.el('left-panel').style.width, '360px');
});

test('the right panel grows in the opposite direction', async () => {
  const app = await load();
  app.el('right-panel').style.width = '280px';
  // Moving the pointer left has to widen the right panel, not shrink it.
  drag(app, 'resize-right', 700, 600);
  assert.equal(app.el('right-panel').style.width, '380px');
});

test('a drag is clamped so neither panel can be made useless', async () => {
  const app = await load();
  app.el('left-panel').style.width = '260px';
  drag(app, 'resize-left', 260, -500);
  assert.equal(app.el('left-panel').style.width, '160px');

  drag(app, 'resize-left', 160, 5000);
  assert.equal(app.el('left-panel').style.width, '640px');
});

test('a width survives a reload, and double-click puts the default back', async () => {
  const app = await load();
  app.el('left-panel').style.width = '260px';
  drag(app, 'resize-left', 260, 400);

  const saved = JSON.parse(app.storage.get('localcode.panelWidths'));
  assert.equal(saved.left, 400);

  const reopened = await load({ localStorage: { 'localcode.panelWidths': JSON.stringify({ left: 400 }) } });
  assert.equal(reopened.el('left-panel').style.width, '400px');

  // Back to no inline width at all, which is what hands control to the
  // stylesheet again.
  reopened.el('resize-left').fire('dblclick');
  assert.equal(reopened.el('left-panel').style.width, '');
});

test('a non-left button does not start a drag', async () => {
  const app = await load();
  app.el('left-panel').style.width = '260px';
  app.el('resize-left').fire('pointerdown', { button: 2, clientX: 260 });
  app.doc.fire('pointermove', { clientX: 400 });
  assert.equal(app.el('left-panel').style.width, '260px');
});

test('a page whose storage throws still starts and still resizes', async () => {
  const app = await load({ localStorage: { 'localcode.panelWidths': 'not json' } });
  assert.ok(!app.el('left-panel').style.width); // unparseable, so no saved width was applied
  app.el('left-panel').style.width = '260px';
  drag(app, 'resize-left', 260, 300);
  assert.equal(app.el('left-panel').style.width, '300px');
});
