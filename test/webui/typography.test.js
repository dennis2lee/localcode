'use strict';

// Typography: the faces and the text sizes, applied immediately.
//
// The three faces and the four size multipliers are written as custom
// properties on the root element, so they take effect on the next frame
// with no reload. They belong to the person and their screen, so they
// live in this browser's localStorage (like the ctrl+wheel zoom) and
// never in config.json. The nine-step scale itself is never rewritten:
// each step is its own pixel value times the master times its group trim.

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const { load } = require('./harness');

const STATIC_DIR = path.join(__dirname, '..', '..', 'internal', 'daemon', 'static');
const CSS_PATH = path.join(STATIC_DIR, 'style.css');

// The nine steps as they are today: every font-size in the sheet is one
// of these, and at scale 1 the computed sizes must be exactly these.
const NINE_STEPS = {
  '--t-cap': '10.5px',
  '--t-fine': '11.5px',
  '--t-ui': '12.5px',
  '--t-code': '13px',
  '--t-ui-l': '13.5px',
  '--t-read-s': '15px',
  '--t-sub': '16.5px',
  '--t-read': '17px',
  '--t-head': '18.5px',
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

test('setting a face writes the property and takes effect without a reload', async () => {
  const app = await load();
  await openTypography(app);

  const select = app.el('typo-doc-select');
  const stack = Array.from(select.querySelectorAll('option')).map((o) => o.value).find((v) => v.includes('Georgia'));
  assert.ok(stack, 'the reading-face picker offers no Georgia stack');
  select.value = stack;
  select.fire('change');
  // No settle: "applies immediately" means synchronously, on this frame.
  assert.equal(rootProp(app, '--f-doc'), stack, 'the face did not reach the root element');
  assert.equal(app.storage.get('localcode.face.doc'), stack, 'the face was not stored');
});

test('setting a size writes the scale without touching the zoom', async () => {
  const app = await load();
  await openTypography(app);

  app.applyZoom(1.5);
  const select = app.el('typo-size-select');
  select.value = '1.25';
  select.fire('change');

  assert.equal(rootProp(app, '--t-scale'), '1.25', 'the size did not reach the root element');
  assert.equal(app.storage.get('localcode.textScale'), '1.25', 'the size was not stored');
  // --zoom is the ctrl+wheel page zoom, a different thing: both stand.
  assert.equal(rootProp(app, '--zoom'), '1.5', 'setting the text size moved the page zoom');
  assert.equal(app.document.documentElement.style.zoom, '1.5');
});

// Each step's group trim. The groups are places, not faces: the mono face
// is set at UI-group sizes wherever it appears in a row, so the interface
// trim reaches those too. Shared with typography_groups.test.js, which
// owns the trim behaviour; this file owns the master and the scale-1
// identity both compose onto.
const GROUP_FOR_STEP = {
  '--t-cap': 'ui',
  '--t-fine': 'ui',
  '--t-ui': 'ui',
  '--t-ui-l': 'ui',
  '--t-code': 'code',
  '--t-read-s': 'read',
  '--t-sub': 'read',
  '--t-read': 'read',
  '--t-head': 'read',
};

test('at every multiplier of 1 every step is exactly what it is today', () => {
  const text = css();
  for (const [step, px] of Object.entries(NINE_STEPS)) {
    const group = GROUP_FOR_STEP[step];
    const re = new RegExp(`${step}:\\s*calc\\(${px.replace('.', '\\.')} \\* var\\(--t-scale, 1\\) \\* var\\(--t-scale-${group}, 1\\)\\)`);
    assert.ok(re.test(text), `${step} is not calc(${px} * var(--t-scale, 1) * var(--t-scale-${group}, 1)): the all-1 sizes moved`);
  }
});

test('the master still multiplies all nine steps, composed with one group trim', () => {
  const text = css();
  const scaled = [...text.matchAll(/(--t-[a-z-]+):\s*calc\([^;]*var\(--t-scale, 1\)[^;]*;/g)]
    .map((m) => m[1])
    .filter((name) => name !== '--t-scale');
  assert.deepEqual([...new Set(scaled)].sort(), Object.keys(NINE_STEPS).sort(),
    'a step is not driven by --t-scale, so the master no longer moves all nine');
  // And each step composes the master with exactly its own trim: one
  // master read, one group read, and no other group's.
  for (const m of text.matchAll(/(--t-[a-z-]+):\s*calc\(([^;]*)\);/g)) {
    if (!NINE_STEPS[m[1]]) continue;
    const body = m[2];
    assert.equal((body.match(/var\(--t-scale,/g) || []).length, 1, `${m[1]} does not read the master exactly once`);
    for (const group of ['read', 'ui', 'code']) {
      const want = group === GROUP_FOR_STEP[m[1]] ? 1 : 0;
      assert.equal((body.match(new RegExp(`var\\(--t-scale-${group},`, 'g')) || []).length, want,
        `${m[1]} reads the ${group} trim ${want === 1 ? 'never' : 'although it belongs to ' + GROUP_FOR_STEP[m[1]]}`);
    }
  }
});

test('a stored choice survives a reload and lands before first paint', async () => {
  // init: false leaves init() un awaited: what is set already ran at
  // module load, which is the "before first paint" half of the claim.
  const app = await load({
    init: false,
    localStorage: {
      'localcode.face.doc': '"Garamond", Georgia, serif',
      'localcode.textScale': '1.25',
    },
  });
  assert.equal(rootProp(app, '--f-doc'), '"Garamond", Georgia, serif',
    'the stored face was not applied before init ran');
  assert.equal(rootProp(app, '--t-scale'), '1.25',
    'the stored size was not applied before init ran');
  await app.settle();
  assert.equal(rootProp(app, '--f-doc'), '"Garamond", Georgia, serif',
    'init undid the stored face');
});

test('a face name that resolves to nothing still has a fallback behind it', async () => {
  const app = await load();
  await openTypography(app);

  const select = app.el('typo-mono-select');
  select.value = 'custom …';
  select.fire('change');
  const custom = app.el('typo-mono-custom');
  assert.equal(custom.hidden, false, 'choosing a named font shows no field to name it in');
  custom.value = 'A Font Nobody Has Installed';
  custom.fire('change');

  const written = rootProp(app, '--f-mono');
  assert.ok(written.startsWith('"A Font Nobody Has Installed", '),
    `the named font is not first: ${written}`);
  assert.ok(/,\s*monospace$/.test(written),
    `no monospace fallback behind the named font: ${written}`);
});

test('every offered stack degrades to something readable', async () => {
  const app = await load();
  await openTypography(app);
  const generic = { doc: 'serif', ui: 'sans-serif', mono: 'monospace' };
  for (const [role, family] of Object.entries(generic)) {
    const select = app.el(`typo-${role}-select`);
    for (const opt of select.querySelectorAll('option')) {
      if (!opt.value || opt.value === 'custom …') continue;
      assert.ok(opt.value.includes(family),
        `${role} offers "${opt.textContent}" with no ${family} behind it`);
    }
  }
});

test('resetting to the default removes the property instead of writing it', async () => {
  const app = await load({
    localStorage: { 'localcode.face.ui': 'Verdana, Geneva, Tahoma, sans-serif', 'localcode.textScale': '1.25' },
  });
  await openTypography(app);
  assert.equal(rootProp(app, '--t-scale'), '1.25');

  const size = app.el('typo-size-select');
  size.value = '1';
  size.fire('change');
  assert.equal(rootProp(app, '--t-scale'), '', 'the default size left a property behind');
  assert.equal(app.storage.get('localcode.textScale'), undefined, 'the default size left storage behind');

  const ui = app.el('typo-ui-select');
  ui.value = '';
  ui.fire('change');
  assert.equal(rootProp(app, '--f-ui'), '', 'the default face left a property behind');
  assert.equal(app.storage.get('localcode.face.ui'), undefined, 'the default face left storage behind');
});

test('a stored size outside any sense is the default, not a broken page', async () => {
  const app = await load({ localStorage: { 'localcode.textScale': 'banana' } });
  assert.equal(rootProp(app, '--t-scale'), '', 'an unparseable stored size wrote a property');
  const app2 = await load({ localStorage: { 'localcode.textScale': '25' } });
  assert.equal(rootProp(app2, '--t-scale'), '', 'an absurd stored size wrote a property');
});

// The pre-paint script in the head and the module say the same thing.
//
// The head script exists because a module runs after the stylesheet has
// already painted, so a reload would flash the default face. It is a
// second copy of the same read, and a second copy is only safe while
// something checks the two still agree: rename a key in typography.js
// and the head script goes on reading the old one, silently, and the
// flash comes back for exactly the people who had set a face.
test('the pre-paint head script reads the same keys and properties as the module', () => {
  const html = fs.readFileSync(path.join(STATIC_DIR, 'index.html'), 'utf8');
  const inline = html.match(/<script>([\s\S]*?)<\/script>/);
  assert.ok(inline, 'index.html has no pre-paint script');
  const head = inline[1];

  const module = fs.readFileSync(path.join(STATIC_DIR, 'js', 'typography.js'), 'utf8');
  const keys = [...module.matchAll(/'(localcode\.[A-Za-z.]+)'/g)].map((m) => m[1]);
  assert.ok(keys.length >= 4, `typography.js names ${keys.length} storage keys, expected the three faces and the scale`);
  // Quoted, not bare: a bare substring match passes when the head script
  // reads a longer key that merely starts the same way, which is the
  // drift this is here to catch.
  for (const key of new Set(keys)) {
    assert.ok(head.includes(`'${key}'`),
      `the head script never reads ${key}, so a face set through the module is not applied before first paint`);
  }

  // The head script builds a face property from the role rather than
  // spelling all three, so the prefix is the whole drift surface there;
  // the scale property is spelled out in both and is compared as written.
  assert.ok(/--f-/.test(head), 'the head script sets no face property, so a stored face never reaches the first paint');
  assert.ok(head.includes('--t-scale'), 'the head script never sets --t-scale, so a stored size does not survive a reload without a flash');
  for (const role of ['doc', 'ui', 'mono']) {
    assert.ok(module.includes(`--f-${role}`), `typography.js no longer names --f-${role}; teach the head script too`);
  }
});
