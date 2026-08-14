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
