'use strict';

// The settings window. Everything it holds belongs to the daemon and is
// shared by every client attached to it; the update controls have their
// own file (update.test.js), so what is left here is the window itself.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

test('the settings button opens the window', async () => {
  const app = await load();

  app.el('settings-btn').click();
  await app.settle();

  assert.equal(app.settings.isOpen, true);
});

// Tab switches agent from the prompt box. Under a modal it must not:
// the key belongs to whatever has focus in the window that is open.
test('Tab does not cycle agents while the settings window is open', async () => {
  const app = await load();
  app.el('settings-btn').click();
  await app.settle();

  const before = app.callsTo('POST', /\/agent$/).length;
  app.press('Tab');
  await app.settle();
  assert.equal(app.callsTo('POST', /\/agent$/).length, before, 'Tab switched agents under the modal');
});

// Opening the panel asks GitHub nothing. A check is an outbound request
// that says which version this machine runs, so it happens on a click.
test('opening the panel does not check for updates', async () => {
  const app = await load();

  app.el('settings-btn').click();
  await app.settle();

  assert.equal(app.callsTo('GET', '/api/update').length, 0);
});
