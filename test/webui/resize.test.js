'use strict';

// Dragging the handle between a side panel and the transcript. The width
// is written inline on the panel and remembered in localStorage; the
// bounds are what keep a drag from making either panel useless.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

// The fake DOM has no layout: an element's offsetWidth is its inline width,
// or 0. A transcript 0px wide has no room to give a panel, so a test about
// anything but that room gives it plenty first.
async function loadWide(opts) {
  const app = await load(opts);
  app.el('center').style.width = '4000px';
  return app;
}

// The browser panel is drawn only in the desktop window with the setting
// on, and a drag of a panel that is not drawn ends at once. These tests are
// about its handle, so they draw it.
function showBrowser(app) {
  app.el('browser-panel').hidden = false;
  app.el('resize-browser').hidden = false;
  return app.el('browser-panel');
}

// A drag is pointerdown on the handle, then moves and the release on the
// document — the pointer leaves the 4px handle almost immediately, which
// is the whole reason the listeners live on the document.
function drag(app, handleId, fromX, toX) {
  app.el(handleId).fire('pointerdown', { button: 0, clientX: fromX });
  app.doc.fire('pointermove', { clientX: toX });
  app.doc.fire('pointerup', { clientX: toX });
}

test('dragging the left handle right widens the left panel', async () => {
  const app = await loadWide();
  app.el('left-panel').style.width = '260px';
  drag(app, 'resize-left', 260, 360);
  assert.equal(app.el('left-panel').style.width, '360px');
});

test('the right panel grows in the opposite direction', async () => {
  const app = await loadWide();
  app.el('right-panel').style.width = '280px';
  // Moving the pointer left has to widen the right panel, not shrink it.
  drag(app, 'resize-right', 700, 600);
  assert.equal(app.el('right-panel').style.width, '380px');
});

test('a drag is clamped so neither panel can be made useless', async () => {
  const app = await loadWide();
  app.el('left-panel').style.width = '260px';
  drag(app, 'resize-left', 260, -500);
  assert.equal(app.el('left-panel').style.width, '160px');

  drag(app, 'resize-left', 160, 5000);
  assert.equal(app.el('left-panel').style.width, '640px');
});

test('a width survives a reload, and double-click puts the default back', async () => {
  const app = await loadWide();
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
  const app = await loadWide();
  app.el('left-panel').style.width = '260px';
  app.el('resize-left').fire('pointerdown', { button: 2, clientX: 260 });
  app.doc.fire('pointermove', { clientX: 400 });
  assert.equal(app.el('left-panel').style.width, '260px');
});

test('a page whose storage throws still starts and still resizes', async () => {
  const app = await loadWide({ localStorage: { 'localcode.panelWidths': 'not json' } });
  assert.ok(!app.el('left-panel').style.width); // unparseable, so no saved width was applied
  app.el('left-panel').style.width = '260px';
  drag(app, 'resize-left', 260, 300);
  assert.equal(app.el('left-panel').style.width, '300px');
});

// Dragging can only ever make a panel small, never absent — MIN exists
// because a panel narrower than its own content is worse than no panel.
// The toggle is the way to actually get rid of one.
test('the toggle hides a panel and its handle, and puts them back', async () => {
  const app = await loadWide();
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
  const app = await loadWide();
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
  const app = await loadWide();
  const panel = showBrowser(app);
  panel.style.width = '600px';
  drag(app, 'resize-browser', 900, 700);
  assert.equal(panel.style.width, '800px');

  // Up to the width that shows a 1280-pixel page at its own pixels, and
  // no narrower than a page can still be read and clicked on.
  drag(app, 'resize-browser', 800, -2000);
  assert.equal(panel.style.width, '1307px');
  drag(app, 'resize-browser', 0, 3000);
  assert.equal(panel.style.width, '360px');

  // The side panels keep theirs.
  assert.ok(!app.el('right-panel').style.width);
});

test('the browser panel width survives a reload, and double-click puts the default back', async () => {
  const app = await loadWide();
  showBrowser(app).style.width = '600px';
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
  const app = await loadWide();
  const { CENTER_MIN } = app.internals;
  const panel = showBrowser(app);
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

  // The same holds for the side panels, with no browser panel to give.
  panel.hidden = true;
  app.el('center').style.width = `${CENTER_MIN + 100}px`;
  app.el('left-panel').style.width = '260px';
  drag(app, 'resize-left', 260, 600);
  assert.equal(app.el('left-panel').style.width, '360px');
});

// A transcript the side panels have squeezed to nothing (a window narrower
// than both of them, with the browser off) has no room to give: a drag
// cannot widen a panel further, and can still narrow it.
test('a transcript squeezed to nothing gives a panel no more room', async () => {
  const app = await load();
  app.el('center').style.width = '0px';
  app.el('left-panel').style.width = '400px';
  drag(app, 'resize-left', 400, 500);
  assert.equal(app.el('left-panel').style.width, '400px');
  drag(app, 'resize-left', 400, 300);
  assert.equal(app.el('left-panel').style.width, '300px');
});

// While the browser panel is shown the stylesheet shrinks it before the
// transcript, so a side panel can take what it has above its minimum. Its
// own drag cannot: widening it never squeezes the side panels.
test('a side panel can take the browser panel\'s room, and the browser panel cannot take theirs', async () => {
  const app = await load();
  const browser = showBrowser(app);
  browser.style.width = '800px';
  app.el('center').style.width = '280px';
  app.el('right-panel').style.width = '280px';
  drag(app, 'resize-right', 1000, 900);
  assert.equal(app.el('right-panel').style.width, '380px');

  // The browser panel has given 100px of its 800; the fake DOM has no
  // layout, so say so. 140px above its minimum are left.
  browser.style.width = '500px';
  drag(app, 'resize-right', 1000, 0);
  assert.equal(app.el('right-panel').style.width, '520px',
    'the right panel took more than the browser panel has above its minimum');

  drag(app, 'resize-browser', 1000, 900);
  assert.equal(browser.style.width, '500px', 'the browser panel took room from the side panels');
});

// The browser setting can go off in the middle of a drag of its panel. The
// drag ends there, and the width it had reached is not remembered.
test('a drag whose panel is hidden under it ends without saving', async () => {
  const app = await loadWide();
  const browser = showBrowser(app);
  browser.style.width = '600px';
  app.el('resize-browser').fire('pointerdown', { button: 0, clientX: 900 });
  app.doc.fire('pointermove', { clientX: 850 });
  assert.equal(browser.style.width, '650px');
  browser.hidden = true;
  app.el('resize-browser').hidden = true;
  app.doc.fire('pointermove', { clientX: 700 });
  app.doc.fire('pointerup', { clientX: 700 });
  assert.equal(browser.style.width, '650px', 'the hidden panel went on being resized');
  const saved = app.storage.get('localcode.panelWidths');
  assert.ok(!saved || JSON.parse(saved).browser === undefined, 'a width nobody chose was remembered');
  assert.equal(app.doc.body.classList.contains('resizing'), false);
});
