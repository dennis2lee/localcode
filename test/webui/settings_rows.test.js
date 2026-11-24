'use strict';

// The settings window, one row per switch.
//
// The window used to give each switch a section of its own: a heading, a
// label wrapping over two lines, and a note that said "On." or "Off." and
// then explained. Five of them on one tab read as a wall of text. Now a
// switch is one row of one shape, the rows of a group sit in one box, and
// a note says only what the switch cannot. These hold the window to that
// shape, read from the shipped index.html and style.css, so a switch added
// later in the old shape fails here by name.

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const { load } = require('./harness');

const STATIC_DIR = path.join(__dirname, '..', '..', 'internal', 'daemon', 'static');

function settingsMarkup() {
  const html = fs.readFileSync(path.join(STATIC_DIR, 'index.html'), 'utf8');
  const start = html.indexOf('<div id="settings-modal">');
  assert.ok(start >= 0, 'index.html has no settings modal');
  const end = html.indexOf('<div id="effort-modal">', start);
  assert.ok(end > start, 'cannot find the end of the settings modal');
  // Comments out, keeping the offsets: a comment may name a class.
  return html.slice(start, end).replace(/<!--[\s\S]*?-->/g, (c) => ' '.repeat(c.length));
}

const VOID = new Set(['area', 'base', 'br', 'col', 'embed', 'hr', 'img', 'input', 'link', 'meta', 'source', 'track', 'wbr']);

// elementsOf reads the markup into elements that know their parent, their
// ancestors and their text, which is all these tests need of a tree. It
// insists that every element is closed by its own tag, so markup that
// would nest differently in a browser fails here rather than passing on a
// tree no browser builds.
function elementsOf(markup) {
  const elements = [];
  const stack = [];
  const tag = /<(\/?)([a-zA-Z][a-zA-Z0-9-]*)\b([^>]*)>/g;
  let m;
  while ((m = tag.exec(markup)) !== null) {
    const [, closing, rawName, attrText] = m;
    const name = rawName.toLowerCase();
    if (closing) {
      const open = stack.pop();
      assert.ok(open && open.tag === name, `</${name}> closes <${open ? open.tag : 'nothing'}> at ${m.index}`);
      open.text = markup.slice(open.innerStart, m.index).replace(/<[^>]*>/g, '').replace(/\s+/g, ' ').trim();
      continue;
    }
    const attrs = {};
    for (const a of attrText.matchAll(/([a-zA-Z_:][-a-zA-Z0-9_:.]*)(?:\s*=\s*"([^"]*)")?/g)) {
      attrs[a[1].toLowerCase()] = a[2] === undefined ? '' : a[2];
    }
    const el = {
      tag: name,
      attrs,
      classes: (attrs.class || '').split(/\s+/).filter(Boolean),
      parent: stack[stack.length - 1] || null,
      ancestors: [...stack],
      innerStart: tag.lastIndex,
      text: '',
    };
    elements.push(el);
    if (!VOID.has(name)) stack.push(el);
  }
  assert.equal(stack.length, 0, `unclosed in the settings window: ${stack.map((e) => e.tag).join(', ')}`);
  return elements;
}

const inside = (el, box) => el.ancestors.includes(box);
const byId = (els, id) => els.find((e) => e.attrs.id === id);

test('every switch in the window is one row: a name, one line, and the switch on the right', () => {
  const els = elementsOf(settingsMarkup());
  const switches = els.filter((e) => e.tag === 'input' && e.attrs.type === 'checkbox');
  assert.equal(switches.length, 8, `expected the eight switches, found ${switches.length}`);
  for (const box of switches) {
    const id = box.attrs.id;
    const row = box.parent;
    assert.ok(row && row.classes.includes('setting-row'), `${id} is not directly inside a .setting-row`);
    assert.ok(box.classes.includes('setting-switch'), `${id} is not drawn as a switch (.setting-switch)`);
    assert.equal(box.attrs.role, 'switch', `${id} is not announced as a switch`);
    const children = els.filter((e) => e.parent === row);
    assert.equal(children[children.length - 1], box, `${id} is not the last thing in its row, so it is not on the right`);
    // One row, one switch.
    assert.deepEqual(switches.filter((s) => inside(s, row)), [box], `the row of ${id} holds another switch`);

    const names = els.filter((e) => inside(e, row) && e.tag === 'label' && e.classes.includes('setting-name'));
    assert.equal(names.length, 1, `the row of ${id} has ${names.length} names`);
    assert.equal(names[0].attrs.for, id, `the name in the row of ${id} labels ${names[0].attrs.for}`);
    assert.ok(names[0].text.length > 0 && names[0].text.length <= 30, `"${names[0].text}" is a sentence, not a name`);

    const descs = els.filter((e) => inside(e, row) && e.classes.includes('setting-desc'));
    assert.equal(descs.length, 1, `the row of ${id} has ${descs.length} lines under its name`);
    const desc = descs[0].text;
    assert.ok(desc.length > 0 && desc.length <= 100, `the line under ${id} is ${desc.length} characters, not one short line: ${desc}`);
    assert.doesNotMatch(desc, /^Let the model\b/, `the line under ${id} keeps the preamble: ${desc}`);
    assert.doesNotMatch(desc, /[\u2013\u2014]/, `an em dash or en dash under ${id}`);
  }
  // And the row shape is only for switches: a row with no switch is a
  // section in a row's clothes.
  for (const row of els.filter((e) => e.classes.includes('setting-row'))) {
    assert.equal(switches.filter((s) => s.parent === row).length, 1, `a .setting-row (${row.attrs.id || 'no id'}) holds no switch`);
  }
});

test('the rows of a group sit in one box, under the group\'s heading, in the tab it belongs to', () => {
  const els = elementsOf(settingsMarkup());
  const want = {
    'settings-panel-tools': [
      ['Panels beside the conversation', ['browser-checkbox', 'ssh-checkbox']],
      ['Other agents and commands', ['orchestrate-checkbox', 'model-invocable-checkbox']],
    ],
    'settings-panel-behavior': [
      ['Every model', ['smart-agent-checkbox', 'repeat-limit-checkbox']],
      ['Muse models only', ['fold-thinking-checkbox', 'keep-going-checkbox']],
    ],
  };
  for (const [panelId, groups] of Object.entries(want)) {
    const panel = byId(els, panelId);
    assert.ok(panel, `no ${panelId}`);
    const got = [];
    let current = null;
    for (const e of els.filter((x) => inside(x, panel))) {
      if (e.tag === 'h4' && e.classes.includes('settings-group')) {
        current = [e.text, []];
        got.push(current);
      } else if (e.tag === 'input' && e.attrs.type === 'checkbox') {
        assert.ok(current, `${e.attrs.id} comes before any group heading in ${panelId}`);
        current[1].push(e.attrs.id);
        const list = e.ancestors.find((a) => a.classes.includes('setting-list'));
        assert.ok(list && list.parent === panel, `${e.attrs.id} is not in a group's box`);
      }
    }
    assert.deepEqual(got, groups, `${panelId} is not grouped as decided`);
  }
  // The rows of one group share one box.
  for (const [a, b] of [['browser-checkbox', 'ssh-checkbox'], ['orchestrate-checkbox', 'model-invocable-checkbox'],
    ['smart-agent-checkbox', 'repeat-limit-checkbox'], ['fold-thinking-checkbox', 'keep-going-checkbox']]) {
    const listOf = (id) => byId(els, id).ancestors.find((x) => x.classes.includes('setting-list'));
    assert.equal(listOf(a), listOf(b), `${a} and ${b} are in one group and two boxes`);
  }
  // The browser row is still the element shown only in the desktop
  // window, and the SSH row still carries its id.
  assert.ok(byId(els, 'browser-settings').classes.includes('setting-row'));
  assert.ok('hidden' in byId(els, 'browser-settings').attrs, 'the browser row is shown outside the desktop window');
  assert.ok(inside(byId(els, 'browser-import-btn'), byId(els, 'browser-settings')));
  assert.ok(inside(byId(els, 'ssh-live'), byId(els, 'ssh-settings')));
  // The muse note is said once, under its heading and above the box.
  const muse = byId(els, 'muse-note');
  assert.equal(muse.parent, byId(els, 'settings-panel-behavior'));
  // How SSH asks is fixed, so it is markup in the row, not a note.
  const more = els.find((e) => e.tag === 'details' && inside(e, byId(els, 'ssh-settings')));
  assert.ok(more && more.classes.includes('setting-more'), 'the SSH row has no "How it asks"');
  assert.match(more.text, /^How it asks /);
  assert.match(more.text, /"ssh_connect" for hosts, "ssh" for commands, "ssh_input" for typing/);
  assert.match(more.text, /Turning it off closes every connection\./);
});

test('the Appearance tab sets all seven pickers in two columns, faces then sizes', () => {
  const els = elementsOf(settingsMarkup());
  const panel = byId(els, 'settings-panel-appearance');
  assert.ok(panel, 'no Appearance panel');
  const want = [
    ['Faces', [['typo-doc-select', 'Reading'], ['typo-ui-select', 'Interface'], ['typo-mono-select', 'Monospace']]],
    ['Text size', [['typo-size-select', 'Overall'], ['typo-size-read-select', 'Reading'],
      ['typo-size-ui-select', 'Interface'], ['typo-size-code-select', 'Code']]],
  ];
  const selects = els.filter((e) => e.tag === 'select' && inside(e, panel));
  assert.equal(selects.length, 7, `the Appearance tab has ${selects.length} pickers`);

  const got = [];
  for (const e of els.filter((x) => inside(x, panel))) {
    if (e.tag === 'h4' && e.classes.includes('settings-group')) got.push([e.text, []]);
    if (e.tag !== 'select') continue;
    const id = e.attrs.id;
    const grid = e.ancestors.find((a) => a.classes.includes('setting-grid'));
    assert.ok(grid && grid.parent === panel, `${id} is not in the Appearance grid`);
    // Two columns: the text, then the control holding the picker.
    const control = e.parent;
    assert.ok(control.classes.includes('setting-control'), `${id} is not in a .setting-control`);
    const row = control.parent;
    assert.ok(row.classes.includes('setting-pick'), `${id} is not in a .setting-pick row`);
    const cells = els.filter((x) => x.parent === row);
    assert.equal(cells.length, 2, `the row of ${id} is not two columns`);
    assert.ok(cells[0].classes.includes('setting-text'), `the left column of ${id} is not its text`);
    assert.equal(cells[1], control, `the right column of ${id} is not its picker`);
    const label = els.find((x) => inside(x, cells[0]) && x.tag === 'label');
    assert.equal(label.attrs.for, id);
    const desc = els.find((x) => inside(x, cells[0]) && x.classes.includes('setting-desc'));
    assert.ok(desc && desc.text, `${id} has no line under its label`);
    got[got.length - 1][1].push([id, label.text]);
  }
  assert.deepEqual(got, want);

  // A typed font name stays under its own picker, hidden until asked for.
  for (const role of ['doc', 'ui', 'mono']) {
    const custom = byId(els, `typo-${role}-custom`);
    assert.equal(custom.parent, byId(els, `typo-${role}-select`).parent, `typo-${role}-custom is not under its picker`);
    assert.ok('hidden' in custom.attrs, `typo-${role}-custom is shown before a font is named`);
  }
  // The note on the trims is last, below both groups.
  const note = byId(els, 'typo-note');
  assert.equal(note.parent, panel);
  assert.equal(els.filter((x) => inside(x, panel)).filter((x) => x.parent === panel).pop(), note, 'typo-note is not at the bottom');
});

test('an empty note takes no space, and the script never hides one by its flag', () => {
  const css = fs.readFileSync(path.join(STATIC_DIR, 'style.css'), 'utf8');
  assert.match(css, /#settings-modal\s+\.note:empty\s*\{[^}]*display:\s*none\s*;?[^}]*\}/,
    'no #settings-modal .note:empty { display: none } rule, so an empty note leaves a gap under its switch');
  const js = fs.readFileSync(path.join(STATIC_DIR, 'js', 'settings.js'), 'utf8');
  assert.doesNotMatch(js, /[nN]ote(El)?\.hidden\s*=/, 'settings.js hides a note by its hidden flag; empty it instead');
  // The warnings keep theirs: hidden whenever there is nothing wrong.
  assert.match(js, /smartAgentWarnEl\.hidden = !warning/);
});

test('the switch is drawn from the checkbox with CSS only, and can be seen to have focus', () => {
  const css = fs.readFileSync(path.join(STATIC_DIR, 'style.css'), 'utf8');
  const rule = (sel) => {
    const m = new RegExp(`${sel.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\s*\\{([^}]*)\\}`).exec(css);
    assert.ok(m, `no ${sel} rule`);
    return m[1];
  };
  assert.match(rule('#settings-modal .setting-switch'), /(^|[;\s])appearance:\s*none/);
  assert.match(rule('#settings-modal .setting-switch:checked'), /var\(--interactive\)/, 'a checked switch is not in the accent colour');
  assert.match(rule('#settings-modal .setting-switch:focus-visible'), /outline:\s*[^;]+;/, 'the switch shows no focus ring');
  assert.match(rule('#settings-modal .setting-switch:disabled'), /opacity:/, 'a disabled switch looks like one that works');
});

// Every note under a switch, by the setting it is under.
const NOTES = ['smart-agent-note', 'browser-note', 'ssh-note', 'orchestrate-note', 'model-invocable-note', 'fold-thinking-note'];

test('with every switch off, no note under one says anything', async () => {
  const off = {
    smart_agent: false, browser: false, ssh: false, orchestrate: false, model_invocable: false,
    model_commands: ['/compact'], repeat_limit: 0, keep_going: false, fold_thinking: false,
  };
  const app = await load({ globals: { lcGUI: true }, routes: { 'GET /api/settings': off } });
  app.el('settings-btn').click();
  await app.settle();
  for (const id of NOTES) assert.equal(app.el(id).textContent, '', `${id} says something the switch already shows`);
});

test('with every switch on, no note repeats the state', async () => {
  const on = {
    smart_agent: true, browser: true, ssh: true, orchestrate: true, model_invocable: true,
    model_commands: ['/compact'], orchestrate_roster: ['explore'], repeat_limit: 3, keep_going: true, fold_thinking: true,
  };
  const app = await load({ globals: { lcGUI: true }, routes: { 'GET /api/settings': on } });
  app.el('settings-btn').click();
  await app.settle();
  for (const id of NOTES) assert.doesNotMatch(app.el(id).textContent, /^(On|Off)\b/, `${id} repeats the state: ${app.el(id).textContent}`);
  // And what each that speaks says instead.
  assert.equal(app.el('smart-agent-note').textContent, 'Turn log in ~/.localcode/trace.');
  assert.match(app.el('browser-note').textContent, /~\/\.localcode\/browser/);
  assert.equal(app.el('ssh-note').textContent, '');
  assert.equal(app.el('fold-thinking-note').textContent, '');
});
