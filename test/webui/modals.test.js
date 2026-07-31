'use strict';

// Each modal owns an explicit isOpen boolean; the 'open' class on the
// element is an output of that, never the place the answer is read back
// from. These tests pin both halves — the flag tracks reality, and the flag
// is what the rest of the app consults.

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const { load } = require('./harness');

const jsDir = path.join(__dirname, '..', '..', 'internal', 'daemon', 'static', 'js');

test('opening and closing a modal moves the flag and the class together', async () => {
  const app = await load();
  assert.equal(app.delegate.isOpen, false);
  assert.equal(app.el('auto-delegate-modal').classList.contains('open'), false);

  app.el('auto-delegate-btn').click();
  assert.equal(app.delegate.isOpen, true);
  assert.equal(app.el('auto-delegate-modal').classList.contains('open'), true);

  app.el('delegate-close').click();
  assert.equal(app.delegate.isOpen, false);
  assert.equal(app.el('auto-delegate-modal').classList.contains('open'), false);
});

test('anyModalOpen reflects every modal, not just the permission one', async () => {
  const app = await load();
  assert.equal(app.anyModalOpen(), false);

  app.el('permission-status-btn').click();
  assert.equal(app.permissionSettings.isOpen, true);
  assert.equal(app.anyModalOpen(), true);
  app.el('permission-settings-close').click();
  assert.equal(app.anyModalOpen(), false);

  // The permission request modal is SSE-driven rather than click-driven,
  // and has to be visible to anyModalOpen the same way.
  app.applyEvent({ type: 'permission.request', data: { id: 'p1', tool: 'bash' } });
  assert.equal(app.permissionRequest.isOpen, true);
  assert.equal(app.anyModalOpen(), true);
  app.applyEvent({ type: 'permission.resolved', data: { id: 'p1' } });
  assert.equal(app.permissionRequest.isOpen, false);
  assert.equal(app.anyModalOpen(), false);
});

// The point of the flag is that nothing goes back to the DOM to ask. A
// stylesheet change or a second code path that hides an element some other
// way would silently break every contains('open') check, so there must not
// be any left.
test('no module reads modal state back out of the class list', () => {
  for (const name of fs.readdirSync(jsDir)) {
    const src = fs.readFileSync(path.join(jsDir, name), 'utf8');
    assert.ok(
      !src.includes("classList.contains('open')"),
      `${name} reads modal state from the DOM instead of the modal's isOpen flag`,
    );
  }
});
