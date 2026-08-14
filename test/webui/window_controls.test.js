'use strict';

// The page's own title bar. It exists for one window: the desktop build on
// Windows, where the system frame has been removed and these buttons are
// what went with it. Anywhere else drawing them would be a second set of
// window buttons that do nothing.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

test('a browser tab draws no window buttons', async () => {
  const app = await load();
  assert.equal(app.el('window-bar').hidden, true);
});

// The test for "is this that window" is whether it bound the function the
// buttons call — the same object that will carry the clicks.
test('the frameless window gets its buttons, wired to the window', async () => {
  const commands = [];
  const app = await load({ globals: { lcWindowCommand: (cmd) => commands.push(cmd) } });

  assert.equal(app.el('window-bar').hidden, false, 'the title bar stayed hidden in the desktop window');

  app.el('window-minimize').click();
  app.el('window-maximize').click();
  app.el('window-close').click();

  assert.deepEqual(commands, ['minimize', 'maximize', 'close']);
});

// Moving and resizing are asked for, not hit-tested.
//
// The page is rendered into a child window of the frameless one, so a
// press on it never reaches the window's own hit test — which is why
// v0.44.0's buttons worked and its drag strip and edges did nothing.
test('pressing the drag strip asks the window to move', async () => {
  const commands = [];
  const app = await load({ globals: { lcWindowCommand: (cmd) => commands.push(cmd) } });

  const ev = app.el('window-title').fire('pointerdown', { button: 0 });
  assert.deepEqual(commands, ['drag']);
  assert.equal(ev.defaultPrevented, true, 'the press has to be taken from the page');

  // A right-click is the system menu's business, not a drag.
  app.el('window-title').fire('pointerdown', { button: 2 });
  assert.deepEqual(commands, ['drag']);

  app.el('window-title').fire('dblclick');
  assert.deepEqual(commands, ['drag', 'maximize']);
});

test('each edge asks for its own direction', async () => {
  const commands = [];
  const app = await load({ globals: { lcWindowCommand: (cmd) => commands.push(cmd) } });

  assert.equal(app.el('window-edges').hidden, false);
  for (const edge of ['top', 'bottom', 'left', 'right', 'topleft', 'topright', 'bottomleft', 'bottomright']) {
    app.el('window-edge-' + edge).fire('pointerdown', { button: 0 });
  }
  assert.deepEqual(commands, [
    'resize:top', 'resize:bottom', 'resize:left', 'resize:right',
    'resize:topleft', 'resize:topright', 'resize:bottomleft', 'resize:bottomright',
  ]);
});

test('a browser tab has no resize edges either', async () => {
  const app = await load();
  assert.equal(app.el('window-edges').hidden, true);
});
