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

// Dragging can only ever make a panel small, never absent — MIN exists
// because a panel narrower than its own content is worse than no panel.
// The toggle is the way to actually get rid of one.
test('the toggle hides a panel and its handle, and puts them back', async () => {
  const app = await load();
  assert.ok(!app.el('left-panel').classList.contains('collapsed'));

  app.el('toggle-left').click();
  assert.ok(app.el('left-panel').classList.contains('collapsed'));
  // The handle goes too: a drag grip for something off screen is a dead
  // 4px strip the pointer still snags on.
  assert.ok(app.el('resize-left').classList.contains('collapsed'));

  app.el('toggle-left').click();
  assert.ok(!app.el('left-panel').classList.contains('collapsed'));
  assert.ok(!app.el('resize-left').classList.contains('collapsed'));
});

test('the two toggles are independent and both survive a reload', async () => {
  const app = await load();
  app.el('toggle-right').click();
  assert.ok(app.el('right-panel').classList.contains('collapsed'));
  assert.ok(!app.el('left-panel').classList.contains('collapsed'));

  const saved = JSON.parse(app.storage.get('localcode.panelWidths'));
  assert.equal(saved.rightCollapsed, true);

  const reopened = await load({
    localStorage: { 'localcode.panelWidths': JSON.stringify({ rightCollapsed: true }) },
  });
  assert.ok(reopened.el('right-panel').classList.contains('collapsed'));
  assert.ok(!reopened.el('left-panel').classList.contains('collapsed'));
});

// The browser panel sits between the transcript and the right panel, with
// its handle on its left edge: it grows as the pointer moves left, like
// the right panel, and has bounds of its own, because a page needs more
// room than a list of tasks.
test('the browser panel has its own handle and its own bounds', async () => {
  const app = await load();
  const panel = app.el('browser-panel');
  panel.style.width = '600px';
  drag(app, 'resize-browser', 900, 700);
  assert.equal(panel.style.width, '800px');

  // Up to the width that shows a 1280-pixel page at its own pixels, and
  // no narrower than a page can still be read and clicked on.
  drag(app, 'resize-browser', 800, -2000);
  assert.equal(panel.style.width, '1306px');
  drag(app, 'resize-browser', 0, 3000);
  assert.equal(panel.style.width, '360px');

  // The side panels keep theirs.
  assert.ok(!app.el('right-panel').style.width);
});

test('the browser panel width survives a reload, and double-click puts the default back', async () => {
  const app = await load();
  app.el('browser-panel').style.width = '600px';
  drag(app, 'resize-browser', 900, 800);
  assert.equal(JSON.parse(app.storage.get('localcode.panelWidths')).browser, 700);

  const reopened = await load({ localStorage: { 'localcode.panelWidths': JSON.stringify({ browser: 700 }) } });
  assert.equal(reopened.el('browser-panel').style.width, '700px');
  reopened.el('resize-browser').fire('dblclick');
  assert.equal(reopened.el('browser-panel').style.width, '');
});

// However wide a panel may be, a drag does not take the transcript below
// CENTER_MIN: the transcript is the content, and a wide browser panel on
// a small window would otherwise leave none of it.
test('a drag leaves the transcript its minimum width', async () => {
  const app = await load();
  const { CENTER_MIN } = app.internals;
  const panel = app.el('browser-panel');
  panel.style.width = '600px';
  app.el('center').style.width = `${CENTER_MIN + 220}px`;
  drag(app, 'resize-browser', 900, 500);
  assert.equal(panel.style.width, '820px', 'the panel took the transcript below its minimum');

  // A transcript already under it gives no more, but a drag can still
  // hand room back to it.
  app.el('center').style.width = `${CENTER_MIN - 40}px`;
  drag(app, 'resize-browser', 900, 800);
  assert.equal(panel.style.width, '820px', 'a drag that could not widen it made it narrower');
  drag(app, 'resize-browser', 900, 1000);
  assert.equal(panel.style.width, '720px');

  // The same holds for the side panels.
  app.el('center').style.width = `${CENTER_MIN + 100}px`;
  app.el('left-panel').style.width = '260px';
  drag(app, 'resize-left', 260, 600);
  assert.equal(app.el('left-panel').style.width, '360px');
});
