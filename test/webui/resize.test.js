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

// A side panel's toggle can be pressed from the keyboard while its handle
// is held. The drag ends there too.
test('a drag whose side panel is collapsed under it ends without saving', async () => {
  const app = await loadWide();
  const left = app.el('left-panel');
  left.style.width = '260px';
  app.el('resize-left').fire('pointerdown', { button: 0, clientX: 260 });
  app.doc.fire('pointermove', { clientX: 300 });
  app.el('toggle-left').click();
  app.doc.fire('pointermove', { clientX: 380 });
  app.doc.fire('pointerup', { clientX: 380 });
  assert.equal(left.style.width, '300px', 'the collapsed panel went on being resized');
  assert.equal(JSON.parse(app.storage.get('localcode.panelWidths')).left, undefined, 'a width nobody chose was remembered');
});

// Which panels right of the conversation are shown is the conversation's
// own: the browser panel, the SSH panel, the tasks and MCP panel. A
// conversation that works in a browser and one that works over SSH want
// different columns, and switching between them meant pressing the
// toggles each time.
const collapsed = (app, id) => app.el(id).classList.contains('collapsed');
const rightOf = (app) => ['browser-panel', 'ssh-panel', 'right-panel'].map((id) => (collapsed(app, id) ? 'hidden' : 'shown')).join(' ');
const open = (app, id) => app.selectSession(id, 'general-purpose', '/tmp/workspace');
const views = (app) => JSON.parse(app.storage.get('localcode.panelViews') || '[]');

test('each conversation keeps which panels are shown right of it', async () => {
  const app = await loadWide();
  // The first conversation: the tasks panel hidden.
  app.el('toggle-right').click();
  assert.equal(rightOf(app), 'shown shown hidden');

  // A conversation opened for the first time keeps what is on screen.
  open(app, 'sess-2');
  assert.equal(rightOf(app), 'shown shown hidden');
  // And then makes its own choice: SSH hidden, tasks shown.
  app.el('toggle-right').click();
  app.el('toggle-ssh').click();
  assert.equal(rightOf(app), 'shown hidden shown');

  open(app, 'sess-1');
  assert.equal(rightOf(app), 'shown shown hidden', 'the first conversation came back with the second one\'s panels');
  assert.equal(app.el('toggle-right').getAttribute('aria-expanded'), 'false');
  assert.equal(app.el('toggle-ssh').getAttribute('aria-expanded'), 'true');
  assert.ok(collapsed(app, 'resize-right') && !collapsed(app, 'resize-ssh'), 'a handle goes with its panel');

  open(app, 'sess-2');
  assert.equal(rightOf(app), 'shown hidden shown');
  assert.equal(app.el('toggle-ssh').getAttribute('aria-expanded'), 'false');
  assert.equal(app.el('toggle-right').getAttribute('aria-expanded'), 'true');
});

test('the session panel and the widths stay the window\'s', async () => {
  const app = await loadWide();
  app.el('toggle-left').click();
  open(app, 'sess-2');
  app.el('toggle-left').click();
  drag(app, 'resize-right', 1000, 900);
  const width = app.el('right-panel').style.width;
  open(app, 'sess-1');
  assert.ok(!collapsed(app, 'left-panel'), 'the session panel followed a conversation');
  assert.equal(app.el('right-panel').style.width, width);
  assert.ok(views(app).every(([, hidden]) => !hidden.includes('left')));
});

test('a conversation\'s panels are the same after a reload, with the last change made in it', async () => {
  const app = await loadWide();
  app.el('toggle-ssh').click();
  open(app, 'sess-2');
  app.el('toggle-ssh').click();
  app.el('toggle-right').click();
  open(app, 'sess-1');
  // Changed after coming back: this is the choice a reload must keep,
  // not the one written when the conversation was left.
  app.el('toggle-browser').click();
  assert.equal(rightOf(app), 'hidden hidden shown');

  const reopened = await loadWide({ localStorage: Object.fromEntries(app.storage) });
  assert.equal(rightOf(reopened), 'hidden hidden shown');
  open(reopened, 'sess-2');
  assert.equal(rightOf(reopened), 'shown shown hidden');
  // The window's own record follows what is on screen, so a window whose
  // conversation has no choice written yet starts from it.
  const fresh = await loadWide({ localStorage: { 'localcode.panelWidths': reopened.storage.get('localcode.panelWidths') } });
  assert.equal(rightOf(fresh), 'shown shown hidden');
});

test('a panel the program shows or hides is the conversation\'s choice too', async () => {
  const app = await loadWide();
  app.el('browser-panel').hidden = false;
  app.el('ssh-panel').hidden = false;
  const { makeRoomFor } = app.internals;
  // The window cannot hold both: showing SSH hides the browser panel.
  makeRoomFor(app.el('ssh-panel'), 1100);
  assert.equal(rightOf(app), 'hidden shown shown');
  open(app, 'sess-2');
  app.el('toggle-browser').click();
  open(app, 'sess-1');
  assert.equal(rightOf(app), 'hidden shown shown');
});

test('a window too narrow for a conversation\'s panels keeps the SSH panel', async () => {
  const app = await loadWide();
  app.el('browser-panel').hidden = false;
  app.el('ssh-panel').hidden = false;
  open(app, 'sess-2');
  assert.equal(rightOf(app), 'shown shown shown');
  open(app, 'sess-1');
  // The page is 1100 pixels wide now: not room for both. (The harness
  // has the two settings off, and opening a conversation hid the panels.)
  app.doc.body.style.width = '1100px';
  app.el('browser-panel').hidden = false;
  app.el('ssh-panel').hidden = false;
  open(app, 'sess-2');
  assert.equal(rightOf(app), 'hidden shown shown');
});

test('the room the panels have is counted in the page\'s own pixels', async () => {
  // A window 1400 pixels wide, zoomed to 125%, lays out 1120.
  const app = await load({ globals: { innerWidth: 1400 } });
  const { fitsWith } = app.internals;
  app.el('browser-panel').hidden = false;
  app.el('ssh-panel').hidden = false;
  assert.equal(fitsWith(app.el('ssh-panel')), true, 'with no layout to ask, the window\'s width');
  app.doc.body.style.width = '1120px';
  assert.equal(fitsWith(app.el('ssh-panel')), false, 'counted as 1400, it held panels that did not fit');
});

test('a deleted conversation\'s panels are forgotten, and only the newest are kept', async () => {
  const app = await loadWide();
  const { showPanelsFor, forgetPanels, VIEWS_KEEP } = app.internals;
  open(app, 'sess-2');
  assert.deepEqual(views(app).map(([id]) => id), ['sess-1', 'sess-2']);
  forgetPanels('sess-1');
  assert.deepEqual(views(app).map(([id]) => id), ['sess-2']);
  // An id is not a key of an object: one named like a property is kept
  // like any other.
  showPanelsFor('__proto__');
  showPanelsFor('constructor');
  assert.deepEqual(views(app).map(([id]) => id), ['sess-2', '__proto__', 'constructor']);
  // Opened again, a conversation is the newest.
  showPanelsFor('sess-2');
  assert.deepEqual(views(app).map(([id]) => id), ['__proto__', 'constructor', 'sess-2']);
  for (let i = 0; i < VIEWS_KEEP + 5; i++) {
    showPanelsFor(`s-${i}`);
    if (i === 100) showPanelsFor('sess-2');
  }
  const ids = views(app).map(([id]) => id);
  assert.equal(ids.length, VIEWS_KEEP);
  assert.equal(ids[ids.length - 1], `s-${VIEWS_KEEP + 4}`);
  assert.ok(!ids.includes('constructor') && !ids.includes('s-0'), 'the ones opened longest ago go first');
  assert.ok(ids.includes('sess-2'), 'one opened again since is kept');
});

test('a record that is not one starts the page all the same', async () => {
  for (const bad of ['{', '{"a":1}', '[1,[2],["x"],["sess-1","ssh"],["sess-1",["ssh"]]]', 'null']) {
    const app = await loadWide({ localStorage: { 'localcode.panelViews': bad } });
    assert.ok(app.el('toggle-right'), bad);
  }
  // The one well-formed pair in the third is used.
  const app = await loadWide({ localStorage: { 'localcode.panelViews': '[1,[2],["x"],["sess-1","ssh"],["sess-1",["ssh"]]]' } });
  assert.equal(rightOf(app), 'shown hidden shown');
});

// The PowerShell panel is a fourth panel right of the conversation, and
// the third wide one: the browser, SSH and PowerShell panels each want
// hundreds of pixels.
const fourOf = (app) => ['browser-panel', 'ssh-panel', 'ps-panel', 'right-panel'].map((id) => (collapsed(app, id) ? 'hidden' : 'shown')).join(' ');
const wideOf = (app) => ['browser', 'ssh', 'ps'].map((n) => (collapsed(app, `${n}-panel`) ? 'hidden' : 'shown')).join(' ');

// The three wide panels drawn, as their settings would draw them, with
// their handles.
function showWide(app) {
  for (const id of ['browser', 'ssh', 'ps']) {
    app.el(`${id}-panel`).hidden = false;
    app.el(`resize-${id}`).hidden = false;
  }
}

test('each conversation keeps whether the PowerShell panel is shown', async () => {
  const app = await loadWide();
  open(app, 'sess-2');
  app.el('toggle-ps').click();
  assert.equal(fourOf(app), 'shown shown hidden shown');
  assert.ok(views(app).find(([id]) => id === 'sess-2')[1].includes('powershell'));
  assert.equal(JSON.parse(app.storage.get('localcode.panelWidths')).powershellCollapsed, true);

  open(app, 'sess-1');
  assert.equal(fourOf(app), 'shown shown shown shown', 'the first conversation came back with the second one\'s PowerShell panel');
  assert.equal(app.el('toggle-ps').getAttribute('aria-expanded'), 'true');
  assert.ok(!collapsed(app, 'resize-ps'), 'the handle stayed hidden');

  open(app, 'sess-2');
  assert.equal(fourOf(app), 'shown shown hidden shown');
  assert.equal(app.el('toggle-ps').getAttribute('aria-expanded'), 'false');
  assert.ok(collapsed(app, 'resize-ps'), 'a handle goes with its panel');

  // And after a reload.
  const reopened = await loadWide({ localStorage: Object.fromEntries(app.storage) });
  open(reopened, 'sess-2');
  assert.equal(fourOf(reopened), 'shown shown hidden shown');
  open(reopened, 'sess-1');
  assert.equal(fourOf(reopened), 'shown shown shown shown');
});

test('the PowerShell panel has its own width, its own bounds, and a default to go back to', async () => {
  const app = await loadWide();
  const { PS_BOUNDS } = app.internals;
  showWide(app);
  const panel = app.el('ps-panel');
  panel.style.width = '500px';
  drag(app, 'resize-ps', 900, 800);
  assert.equal(panel.style.width, '600px');
  assert.equal(JSON.parse(app.storage.get('localcode.panelWidths')).powershell, 600);
  drag(app, 'resize-ps', 900, 5000);
  assert.equal(panel.style.width, `${PS_BOUNDS.min}px`);
  drag(app, 'resize-ps', 900, -5000);
  assert.equal(panel.style.width, `${PS_BOUNDS.max}px`);
  assert.ok(!app.el('ssh-panel').style.width, 'the SSH panel moved with it');

  const reopened = await load({ localStorage: { 'localcode.panelWidths': JSON.stringify({ powershell: 700 }) } });
  assert.equal(reopened.el('ps-panel').style.width, '700px');
  reopened.el('resize-ps').fire('dblclick');
  assert.equal(reopened.el('ps-panel').style.width, '');
});

// In a window that cannot hold every wide panel beside the side panels
// and the transcript, the one just shown wins, and the others showing are
// hidden one at a time, browser first, then SSH, then PowerShell, until
// it fits. At their minimums with a handle each: the side panels and the
// transcript 608 pixels, the browser panel 364, a terminal panel 324.
test('in a narrow window the three wide panels take turns', async () => {
  const cases = [
    // [window width, shown before, the one shown now, what is shown after]
    [1300, ['browser', 'ssh'], 'ps', 'hidden shown shown'],
    [1000, ['browser', 'ssh'], 'ps', 'hidden hidden shown'],
    [1300, ['ssh', 'ps'], 'browser', 'shown hidden shown'],
    [1000, ['ssh', 'ps'], 'browser', 'shown hidden hidden'],
    [1300, ['browser', 'ps'], 'ssh', 'hidden shown shown'],
    [1000, ['browser', 'ps'], 'ssh', 'hidden shown hidden'],
    [2000, ['browser', 'ssh'], 'ps', 'shown shown shown'],
  ];
  for (const [width, before, now, after] of cases) {
    const what = `${width}px, ${before.join(' and ')} shown, then ${now}`;
    const app = await loadWide();
    showWide(app);
    app.doc.body.style.width = `${width}px`;
    // Hiding one leaves the two before: hiding never makes room.
    app.el(`toggle-${now}`).click();
    for (const n of ['browser', 'ssh', 'ps']) {
      assert.equal(collapsed(app, `${n}-panel`), !before.includes(n), `${what}: set up`);
    }
    app.el(`toggle-${now}`).click();
    assert.equal(wideOf(app), after, what);
    for (const n of ['browser', 'ssh', 'ps']) {
      assert.equal(app.el(`toggle-${n}`).getAttribute('aria-expanded'), String(!collapsed(app, `${n}-panel`)), `${what}: ${n}'s toggle says otherwise`);
    }
    // And what was hidden is the conversation's choice.
    const hidden = views(app).find(([id]) => id === 'sess-1')[1];
    for (const [n, name] of [['browser', 'browser'], ['ssh', 'ssh'], ['ps', 'powershell']]) {
      assert.equal(hidden.includes(name), collapsed(app, `${n}-panel`), `${what}: ${name} in the conversation's record`);
    }
  }
});

test('a window too narrow for a conversation\'s wide panels keeps the terminals, PowerShell first', async () => {
  for (const [width, after] of [[1300, 'hidden shown shown'], [1000, 'hidden hidden shown'], [2000, 'shown shown shown']]) {
    const app = await loadWide();
    open(app, 'sess-2');
    open(app, 'sess-1');
    app.doc.body.style.width = `${width}px`;
    showWide(app);
    open(app, 'sess-2');
    assert.equal(wideOf(app), after, `${width}px`);
  }
  // Without the PowerShell panel the SSH panel is kept, as before.
  const app = await loadWide();
  open(app, 'sess-2');
  app.el('toggle-ps').click();
  open(app, 'sess-1');
  app.doc.body.style.width = '1100px';
  showWide(app);
  open(app, 'sess-2');
  assert.equal(fourOf(app), 'hidden shown hidden shown');
});

test('a terminal panel\'s drag can take the other wide panels\' room, and the browser panel\'s cannot', async () => {
  const app = await load();
  showWide(app);
  app.el('center').style.width = '280px';
  app.el('browser-panel').style.width = '400px';
  app.el('ssh-panel').style.width = '400px';
  app.el('ps-panel').style.width = '520px';
  // The browser panel has 40 pixels above its minimum, the PowerShell
  // panel 200: the SSH panel can take 240.
  drag(app, 'resize-ssh', 1000, 0);
  assert.equal(app.el('ssh-panel').style.width, '640px');
  // And the PowerShell panel what the browser and SSH panels have.
  app.el('ssh-panel').style.width = '400px';
  drag(app, 'resize-ps', 1000, 0);
  assert.equal(app.el('ps-panel').style.width, `${520 + 40 + 80}px`);
  // The browser panel's drag gets only the transcript's room.
  drag(app, 'resize-browser', 1000, 0);
  assert.equal(app.el('browser-panel').style.width, '400px');
  // A side panel can take what each wide panel has above its minimum.
  app.el('ps-panel').style.width = '400px';
  app.el('right-panel').style.width = '200px';
  drag(app, 'resize-right', 1000, 0);
  assert.equal(app.el('right-panel').style.width, `${200 + 40 + 80 + 80}px`);
});
