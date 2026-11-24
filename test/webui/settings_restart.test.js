'use strict';

// A setting made in the settings window is still shown there after the
// page has been loaded again: the desktop window closed and opened, or a
// reload.
//
// Two things have to hold for that, and each failed once. The value has
// to be kept somewhere a new page reads (config.json for what the daemon
// holds, see internal/config; the page's own storage, see
// window_state.test.js). And the window has to draw it when it opens,
// which the repeat guard did not: its box was drawn only when the number
// changed, so a freshly loaded page showed it unticked and empty whatever
// the daemon was holding.
//
// So this opens the window on a freshly loaded page against every control
// in it, with a value that is not the one the markup starts with.

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const { load } = require('./harness');

const STATIC_DIR = path.join(__dirname, '..', '..', 'internal', 'daemon', 'static');

// What a daemon answers with every setting moved off the value a page
// starts with, and what each control then has to show.
const MOVED = {
  auto_compact_enabled: true,
  show_tps: true,
  auto_delegate: false,
  auto_delegate_agent: '',
  auto_delegate_match: [],
  orchestrate_roster: [],
  skip_permissions: false,
  permission_rules: {},
  can_edit_permissions: true,
  smart_agent: true,
  browser: true,
  ssh: true,
  orchestrate: true,
  model_invocable: true,
  repeat_limit: 7,
  keep_going: false,
  fold_thinking: false,
};

// Every control the daemon holds the value of: its id, what it shows on a
// page that has read nothing yet, and what it shows for MOVED.
const DAEMON_CONTROLS = [
  { id: 'smart-agent-checkbox', read: (el) => el.checked, start: false, moved: true },
  { id: 'browser-checkbox', read: (el) => el.checked, start: false, moved: true },
  { id: 'ssh-checkbox', read: (el) => el.checked, start: false, moved: true },
  { id: 'orchestrate-checkbox', read: (el) => el.checked, start: false, moved: true },
  { id: 'model-invocable-checkbox', read: (el) => el.checked, start: false, moved: true },
  { id: 'repeat-limit-checkbox', read: (el) => el.checked, start: false, moved: true },
  { id: 'repeat-limit-input', read: (el) => String(el.value), start: '', moved: '7' },
  { id: 'keep-going-checkbox', read: (el) => el.checked, start: true, moved: false },
  { id: 'fold-thinking-checkbox', read: (el) => el.checked, start: true, moved: false },
];

// The page's own: the three faces and the four sizes.
const GEORGIA = 'Georgia, Palatino, "Palatino Linotype", "Book Antiqua", serif';
const VERDANA = 'Verdana, Geneva, Tahoma, sans-serif';
const CONSOLAS = 'Consolas, Menlo, Monaco, "Courier New", monospace';
const PAGE_CONTROLS = [
  { id: 'typo-doc-select', key: 'localcode.face.doc', stored: GEORGIA, shows: GEORGIA },
  { id: 'typo-ui-select', key: 'localcode.face.ui', stored: VERDANA, shows: VERDANA },
  { id: 'typo-mono-select', key: 'localcode.face.mono', stored: CONSOLAS, shows: CONSOLAS },
  { id: 'typo-size-select', key: 'localcode.textScale', stored: '1.125', shows: '1.125' },
  { id: 'typo-size-read-select', key: 'localcode.textScale.read', stored: '1.25', shows: '1.25' },
  { id: 'typo-size-ui-select', key: 'localcode.textScale.ui', stored: '0.875', shows: '0.875' },
  { id: 'typo-size-code-select', key: 'localcode.textScale.code', stored: '1.4', shows: '1.4' },
];

async function open(app) {
  app.el('settings-btn').click();
  await app.settle();
}

test('every control the daemon holds shows its value when the window opens on a fresh page', async () => {
  const app = await load({ globals: { lcGUI: true }, routes: { 'GET /api/settings': MOVED } });
  await open(app);
  for (const c of DAEMON_CONTROLS) {
    assert.deepEqual(c.read(app.el(c.id)), c.moved, `${c.id} does not show what the daemon holds`);
  }
  // The number can be changed: it is not left disabled by a box that was
  // drawn ticked.
  assert.equal(app.el('repeat-limit-input').disabled, false);
});

test('and shows the other value when the daemon holds that', async () => {
  // The opposite of MOVED for every one of them, so a control stuck on
  // either value fails one of the two tests.
  const back = { ...MOVED, smart_agent: false, browser: false, ssh: false, orchestrate: false, model_invocable: false, repeat_limit: 0, keep_going: true, fold_thinking: true };
  const app = await load({ globals: { lcGUI: true }, routes: { 'GET /api/settings': back } });
  await open(app);
  for (const c of DAEMON_CONTROLS) {
    if (c.id === 'repeat-limit-input') continue; // the number is not cleared, only disabled
    assert.deepEqual(c.read(app.el(c.id)), c.start, `${c.id} does not show what the daemon holds`);
  }
  assert.equal(app.el('repeat-limit-input').disabled, true);
});

test('every control the page keeps for itself shows its value when the window opens on a fresh page', async () => {
  const stored = {};
  for (const c of PAGE_CONTROLS) stored[c.key] = c.stored;
  const app = await load({ localStorage: stored });
  await open(app);
  app.el('settings-tab-typography').click();
  for (const c of PAGE_CONTROLS) {
    assert.equal(app.el(c.id).value, c.shows, `${c.id} does not show what was stored under ${c.key}`);
  }
});

test('a font named by hand is shown again as the name that was typed', async () => {
  const app = await load({ localStorage: { 'localcode.face.doc': '"IBM Plex Serif", Charter, "Sitka Text", "Iowan Old Style", "Bitstream Charter", Cambria, Georgia, serif' } });
  await open(app);
  app.el('settings-tab-typography').click();
  assert.equal(app.el('typo-doc-custom').hidden, false, 'the field for a typed name is hidden');
  assert.equal(app.el('typo-doc-custom').value, 'IBM Plex Serif');
});

test('the window opened before the daemon answered is drawn again when it does', async () => {
  // The first moment after the page appears: the settings button already
  // works, and the page's first read of the settings has not come back.
  let answer;
  const pending = new Promise((resolve) => { answer = resolve; });
  const app = await load({ init: false, globals: { lcGUI: true }, routes: { 'GET /api/settings': () => pending } });
  await app.settle();
  app.el('settings-btn').click();
  await app.settle();
  for (const c of DAEMON_CONTROLS) {
    assert.deepEqual(c.read(app.el(c.id)), c.start, `${c.id} shows a value the page has not been given`);
  }

  answer(MOVED);
  await app.ready;
  await app.settle();
  for (const c of DAEMON_CONTROLS) {
    assert.deepEqual(c.read(app.el(c.id)), c.moved, `${c.id} was left showing what the page started with`);
  }
});

test('a window that is not open is not drawn by the first read of the settings', async () => {
  // Drawn when it opens, as before: nothing is rendered into a closed
  // window, which would also take the note under a switch with it.
  const app = await load({ globals: { lcGUI: true }, routes: { 'GET /api/settings': MOVED } });
  assert.equal(app.el('smart-agent-checkbox').checked, false, 'the closed window was drawn at load');
  await open(app);
  assert.equal(app.el('smart-agent-checkbox').checked, true);
});

// A switch the daemon applied and could not write to config.json says so
// under it. That is the one sign that the setting will not be there after
// a restart, so it has to outlast every redraw that is not a save of
// that switch.
const UNSAVED = /Applied, but not saved to config\.json/;

// announce sends what the daemon sends every window when a setting
// changes, on the conversation's own stream. Not app.sse: once the SSH
// switch is on, the page has a second stream open for the SSH panel, and
// an event sent there is one the page never reads as a setting.
function announce(app, data) {
  const stream = app.streams.find((s) => !s.closed && /\/events(\?|$)/.test(s.url));
  assert.ok(stream, 'the page has no conversation stream open');
  stream.emit({ type: 'settings.changed', data });
}

test('a warning that a switch was not saved stays through other redraws, until it is saved', async () => {
  let persisted = false;
  const app = await load({
    globals: { lcGUI: true },
    routes: {
      'POST /api/settings/smart-agent': (body) => (persisted
        ? { smart_agent: body.enabled, applied: true, persisted: true }
        : { smart_agent: body.enabled, applied: true, persisted: false, error: 'config.json is read-only' }),
      'POST /api/settings/ssh': (body) => ({ ssh: body.enabled, applied: true, persisted: true }),
    },
  });
  await open(app);
  const box = app.el('smart-agent-checkbox');
  const warn = app.el('smart-agent-warn');
  box.checked = true;
  box.fire('change');
  await app.settle();
  assert.match(warn.textContent, UNSAVED);
  assert.equal(warn.hidden, false);

  // Another switch is saved, which redraws this one.
  const ssh = app.el('ssh-checkbox');
  ssh.checked = true;
  ssh.fire('change');
  await app.settle();
  // And the daemon announces the change, as it does to every window.
  // With another switch moved in the same announcement, which shows the
  // announcement was read and the window drawn from it.
  announce(app, { smart_agent: true, ssh: true, orchestrate: true });
  await app.settle();
  assert.equal(app.el('orchestrate-checkbox').checked, true, 'the announcement did not reach the settings window');
  assert.match(warn.textContent, UNSAVED, 'the warning went with a redraw that saved nothing');
  assert.equal(warn.hidden, false);
  assert.equal(app.el('ssh-warn').hidden, true, 'a switch that was saved shows a warning');

  // The window closed and opened again: still not saved, still said.
  app.el('settings-close').click();
  await open(app);
  assert.match(warn.textContent, UNSAVED, 'the warning went when the window was opened again');

  // Saved: gone.
  persisted = true;
  box.checked = false;
  box.fire('change');
  await app.settle();
  assert.equal(warn.textContent, '');
  assert.equal(warn.hidden, true);
});

test('each of the five switches that answer with whether they were saved keeps its own warning', async () => {
  // Each with its own reason, and one of them saved, so a warning kept
  // under the wrong switch shows.
  const reply = (key, error) => (body) => (error
    ? { [key]: body.enabled, applied: true, persisted: false, error }
    : { [key]: body.enabled, applied: true, persisted: true });
  const app = await load({
    globals: { lcGUI: true },
    routes: {
      'POST /api/settings/smart-agent': reply('smart_agent', 'reason one'),
      'POST /api/settings/browser': reply('browser', 'reason two'),
      'POST /api/settings/ssh': reply('ssh', 'reason three'),
      'POST /api/settings/orchestrate': reply('orchestrate', ''),
      'POST /api/settings/model-invocable': reply('model_invocable', 'reason five'),
    },
  });
  await open(app);
  const switches = [
    ['smart-agent-checkbox', 'smart-agent-warn', 'reason one'],
    ['browser-checkbox', 'browser-warn', 'reason two'],
    ['ssh-checkbox', 'ssh-warn', 'reason three'],
    ['orchestrate-checkbox', 'orchestrate-warn', ''],
    ['model-invocable-checkbox', 'model-invocable-warn', 'reason five'],
  ];
  for (const [boxId] of switches) {
    const box = app.el(boxId);
    box.checked = true;
    box.fire('change');
    await app.settle();
  }
  // Every one drawn again, as a change anywhere does.
  // The same values, and the repeat guard moved, which shows the
  // announcement was read and the window drawn from it.
  announce(app, { smart_agent: true, ssh: true, browser: true, orchestrate: true, model_invocable: true, repeat_limit: 9 });
  await app.settle();
  assert.equal(String(app.el('repeat-limit-input').value), '9', 'the announcement did not reach the settings window');
  for (const [, warnId, reason] of switches) {
    const warn = app.el(warnId);
    if (!reason) {
      assert.equal(warn.textContent, '', `${warnId} shows a warning for a switch that was saved`);
      assert.equal(warn.hidden, true);
      continue;
    }
    assert.match(warn.textContent, UNSAVED, `${warnId} lost its warning`);
    assert.ok(warn.textContent.includes(reason), `${warnId} shows another switch's reason: ${warn.textContent}`);
  }
});

test('a switch moved from somewhere else loses the warning this window put under it', async () => {
  // The warning was about this window's own save of the value it had
  // then. Another window, or a command typed at a prompt, has since
  // moved the switch, and the warning says nothing true about it now.
  const app = await load({
    globals: { lcGUI: true },
    routes: {
      'POST /api/settings/smart-agent': (body) => ({ smart_agent: body.enabled, applied: true, persisted: false, error: 'config.json is read-only' }),
      'POST /api/settings/orchestrate': (body) => ({ orchestrate: body.enabled, applied: true, persisted: false, error: 'config.json is read-only' }),
    },
  });
  await open(app);
  for (const id of ['smart-agent-checkbox', 'orchestrate-checkbox']) {
    const box = app.el(id);
    box.checked = true;
    box.fire('change');
    await app.settle();
  }
  assert.match(app.el('smart-agent-warn').textContent, UNSAVED);
  assert.match(app.el('orchestrate-warn').textContent, UNSAVED);

  announce(app, { smart_agent: false, orchestrate: true });
  await app.settle();
  assert.equal(app.el('smart-agent-checkbox').checked, false);
  assert.equal(app.el('smart-agent-warn').textContent, '', 'a warning stayed under a switch somebody else has since moved');
  assert.equal(app.el('smart-agent-warn').hidden, true);
  // The switch nobody moved keeps its own.
  assert.match(app.el('orchestrate-warn').textContent, UNSAVED);
});

test('the announcement of this window\'s own change does not take the warning its save earned', async () => {
  // The daemon announces a change to every window, this one included,
  // and the announcement can arrive before the reply to the save.
  let reply;
  const app = await load({
    globals: { lcGUI: true },
    routes: {
      'POST /api/settings/smart-agent': () => new Promise((resolve) => {
        reply = () => resolve({ smart_agent: true, applied: true, persisted: false, error: 'config.json is read-only' });
      }),
    },
  });
  await open(app);
  const box = app.el('smart-agent-checkbox');
  box.checked = true;
  box.fire('change');
  await app.settle();
  announce(app, { smart_agent: true });
  await app.settle();
  reply();
  await app.settle();
  assert.match(app.el('smart-agent-warn').textContent, UNSAVED, 'announcement first, reply second: the warning is missing');

  // And the other way round: the reply, then the announcement.
  announce(app, { smart_agent: true });
  await app.settle();
  assert.match(app.el('smart-agent-warn').textContent, UNSAVED, 'reply first, announcement second: the warning went');
});

test('the redraw when the settings arrive leaves a warning that was shown before they did', async () => {
  // The window opened, and a switch turned, while another of the page's
  // first requests was still on its way.
  let late;
  const pending = new Promise((resolve) => { late = resolve; });
  const app = await load({
    init: false,
    globals: { lcGUI: true },
    routes: {
      'GET /api/version': () => pending,
      'POST /api/settings/smart-agent': (body) => ({ smart_agent: body.enabled, applied: true, persisted: false, error: 'disk full' }),
    },
  });
  await app.settle();
  await open(app);
  const box = app.el('smart-agent-checkbox');
  box.checked = true;
  box.fire('change');
  await app.settle();
  assert.match(app.el('smart-agent-warn').textContent, UNSAVED);

  late({ version: 'test' });
  await app.ready;
  await app.settle();
  assert.match(app.el('smart-agent-warn').textContent, UNSAVED, 'the page\'s own startup redraw took the warning');
  assert.equal(box.checked, true);
});

test('every control in the settings window is one of the above', () => {
  // A control added to a tab and to neither list is a control nobody
  // opened the window against.
  const html = fs.readFileSync(path.join(STATIC_DIR, 'index.html'), 'utf8');
  const from = html.indexOf('<div id="settings-modal">');
  const to = html.indexOf('<div id="effort-modal">');
  assert.ok(from > 0 && to > from, 'the settings window was not found in index.html');
  const modal = html.slice(from, to).replace(/<!--[\s\S]*?-->/g, '');
  const ids = [...modal.matchAll(/<(?:input|select|textarea)\b[^>]*\bid="([^"]+)"/g)].map((m) => m[1]);
  assert.ok(ids.length >= 16, `only ${ids.length} controls found; this test no longer reads what it thinks it reads`);
  const known = new Set([
    ...DAEMON_CONTROLS.map((c) => c.id),
    ...PAGE_CONTROLS.map((c) => c.id),
    // The three fields for a font named by hand, which belong to the
    // face pickers beside them.
    'typo-doc-custom', 'typo-ui-custom', 'typo-mono-custom',
  ]);
  for (const id of ids) assert.ok(known.has(id), `the settings window has a control "${id}" that no test here opens the window against`);
  for (const id of known) assert.ok(ids.includes(id), `"${id}" is tested here and is not in the settings window`);
});
