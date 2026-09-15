'use strict';

// Typography trims: one size control per group of steps.
//
// The master (typo-size-select) multiplies all nine steps. Each trim
// multiplies only its own group and composes with the master, so making
// everything bigger and then bringing the code down a notch is two
// choices, not nine. The groups are places, not faces: font-size cannot
// depend on which font-family an element inherited, and the mono face is
// set at UI-group sizes wherever it appears in a row (the session id, the
// version, the usage figures), so those follow the interface trim. The
// labels say what each control reaches; this file holds them to it.
//
// What a trim moving a step means here: the control writes its trim
// property on the root element (which the step's calc reads) and stores
// it, and writes no other trim property. The calc side of that contract —
// which steps read which trim — is asserted against the stylesheet below,
// so a step assigned to the wrong list fails here either way.

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const { load } = require('./harness');

const STATIC_DIR = path.join(__dirname, '..', '..', 'internal', 'daemon', 'static');
const CSS_PATH = path.join(STATIC_DIR, 'style.css');

// The nine steps by the group that must move them. The three reserved
// steps (--t-ui-l, --t-sub, --t-head) are read by no rule yet; each sits
// in the group its comment or size points at, so wiring one later changes
// nothing already on the page.
const GROUP_STEPS = {
  read: ['--t-read-s', '--t-sub', '--t-read', '--t-head'],
  ui: ['--t-cap', '--t-fine', '--t-ui', '--t-ui-l'],
  code: ['--t-code'],
};

const GROUP_KEY = {
  read: 'localcode.textScale.read',
  ui: 'localcode.textScale.ui',
  code: 'localcode.textScale.code',
};

const GROUP_PROP = {
  read: '--t-scale-read',
  ui: '--t-scale-ui',
  code: '--t-scale-code',
};

function css() {
  return fs.readFileSync(CSS_PATH, 'utf8');
}

async function openTypography(app) {
  app.el('settings-btn').click();
  await app.settle();
  app.el('settings-tab-typography').click();
  return app;
}

function rootProp(app, name) {
  return app.document.documentElement.style.getPropertyValue(name);
}

function stepsReading(text, prop) {
  const found = [];
  for (const m of text.matchAll(/(--t-[a-z-]+):\s*calc\(([^;]*)\);/g)) {
    if (m[2].includes(`var(${prop}, 1)`)) found.push(m[1]);
  }
  return found.sort();
}

test('each trim reaches exactly its own steps in the stylesheet', () => {
  const text = css();
  for (const [group, steps] of Object.entries(GROUP_STEPS)) {
    assert.deepEqual(stepsReading(text, GROUP_PROP[group]), [...steps].sort(),
      `the ${group} trim does not reach exactly its steps: a step is in the wrong list`);
  }
});

for (const group of ['read', 'ui', 'code']) {
  test(`the ${group} trim moves its steps and leaves the other trims alone`, async () => {
    const app = await load();
    await openTypography(app);

    const select = app.el(`typo-size-${group}-select`);
    assert.ok(select, `the typography tab has no ${group} trim control`);
    select.value = '1.25';
    select.fire('change');

    assert.equal(rootProp(app, GROUP_PROP[group]), '1.25',
      `the ${group} trim did not reach the root element`);
    assert.equal(app.storage.get(GROUP_KEY[group]), '1.25',
      `the ${group} trim was not stored`);
    for (const other of ['read', 'ui', 'code']) {
      if (other === group) continue;
      assert.equal(rootProp(app, GROUP_PROP[other]), '',
        `moving the ${group} trim also moved the ${other} trim's property`);
      assert.equal(app.storage.get(GROUP_KEY[other]), undefined,
        `moving the ${group} trim stored a ${other} trim value`);
    }
    // The master and the zoom are neighbours, not parts: both stand.
    assert.equal(rootProp(app, '--t-scale'), '', 'moving a trim set the master');
    assert.equal(app.storage.get('localcode.textScale'), undefined, 'moving a trim stored a master value');
  });
}

test('the master and a trim compose instead of overwriting each other', async () => {
  const app = await load();
  await openTypography(app);

  const master = app.el('typo-size-select');
  master.value = '1.25';
  master.fire('change');
  const code = app.el('typo-size-code-select');
  code.value = '0.875';
  code.fire('change');

  assert.equal(rootProp(app, '--t-scale'), '1.25', 'the master did not stand');
  assert.equal(rootProp(app, '--t-scale-code'), '0.875', 'the trim did not stand beside the master');
  // And the stylesheet multiplies the two rather than choosing one: every
  // step reads the master and its trim.
  const text = css();
  for (const m of text.matchAll(/(--t-[a-z-]+):\s*calc\(([^;]*)\);/g)) {
    if (!GROUP_STEPS.read.concat(GROUP_STEPS.ui, GROUP_STEPS.code).includes(m[1])) continue;
    assert.ok(m[2].includes('var(--t-scale, 1)'), `${m[1]} no longer reads the master`);
    assert.ok(/var\(--t-scale-(read|ui|code), 1\)/.test(m[2]), `${m[1]} no longer reads a trim`);
  }
});

test('a setting stored before the trims still sizes everything', async () => {
  // localcode.textScale with no group keys is v0.125.0's whole state: it
  // must size all nine steps the way it did, which it does because every
  // step still reads the master.
  const app = await load({ localStorage: { 'localcode.textScale': '1.25' } });
  await openTypography(app);

  assert.equal(rootProp(app, '--t-scale'), '1.25', 'the stored master did not apply');
  for (const group of ['read', 'ui', 'code']) {
    assert.equal(rootProp(app, GROUP_PROP[group]), '',
      `no stored ${group} trim, yet its property is set`);
    assert.equal(app.el(`typo-size-${group}-select`).value, '1',
      `the ${group} trim does not show the default for a pre-trim setting`);
  }
  assert.equal(app.el('typo-size-select').value, '1.25', 'the master control does not show the stored setting');
});

test('each trim survives a reload and lands before first paint', async () => {
  // init: false leaves init() unawaited: what is set already ran at
  // module load, which is the "before first paint" half of the claim.
  const app = await load({
    init: false,
    localStorage: {
      'localcode.textScale.read': '1.25',
      'localcode.textScale.ui': '0.875',
      'localcode.textScale.code': '1.4',
    },
  });
  assert.equal(rootProp(app, '--t-scale-read'), '1.25', 'the stored reading trim missed the first paint');
  assert.equal(rootProp(app, '--t-scale-ui'), '0.875', 'the stored interface trim missed the first paint');
  assert.equal(rootProp(app, '--t-scale-code'), '1.4', 'the stored code trim missed the first paint');
  assert.equal(rootProp(app, '--t-scale'), '', 'no stored master, yet its property is set');
  await app.settle();
  await openTypography(app);
  assert.equal(app.el('typo-size-read-select').value, '1.25', 'init undid the stored reading trim');
  assert.equal(app.el('typo-size-ui-select').value, '0.875', 'init undid the stored interface trim');
  assert.equal(app.el('typo-size-code-select').value, '1.4', 'init undid the stored code trim');
});

test('resetting a trim to the default removes it instead of writing it', async () => {
  const app = await load({
    localStorage: { 'localcode.textScale.ui': '1.25' },
  });
  await openTypography(app);
  assert.equal(rootProp(app, '--t-scale-ui'), '1.25');

  const select = app.el('typo-size-ui-select');
  select.value = '1';
  select.fire('change');
  assert.equal(rootProp(app, '--t-scale-ui'), '', 'the default trim left a property behind');
  assert.equal(app.storage.get('localcode.textScale.ui'), undefined, 'the default trim left storage behind');
});

test('a stored trim outside any sense is the default, not a broken page', async () => {
  const app = await load({ localStorage: { 'localcode.textScale.read': 'banana' } });
  assert.equal(rootProp(app, '--t-scale-read'), '', 'an unparseable stored trim wrote a property');
  const app2 = await load({ localStorage: { 'localcode.textScale.code': '25' } });
  assert.equal(rootProp(app2, '--t-scale-code'), '', 'an absurd stored trim wrote a property');
});

// The pre-paint head script and the module say the same thing about the
// trims, not just the faces and the master: the module's keys are the
// drift surface and the head script must read every one of them, which
// the guard in typography.test.js already compares exactly.
test('the pre-paint head script reads every trim key the module does', () => {
  const html = fs.readFileSync(path.join(STATIC_DIR, 'index.html'), 'utf8');
  const inline = html.match(/<script>([\s\S]*?)<\/script>/);
  assert.ok(inline, 'index.html has no pre-paint script');
  const head = inline[1];
  for (const key of Object.values(GROUP_KEY)) {
    assert.ok(head.includes(`'${key}'`),
      `the head script never reads ${key}, so a trim set through the module is not applied before first paint`);
  }
  for (const prop of Object.values(GROUP_PROP)) {
    assert.ok(head.includes(prop),
      `the head script never sets ${prop}, so a stored trim does not survive a reload without a flash`);
  }
});
