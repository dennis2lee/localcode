'use strict';

// The Orchestration switch in the settings window.
//
// A second switch beside Smart Agent, and the two answer different
// questions. Smart Agent is how one agent works; this is everything that
// hands work to another agent: the six built-in specialists, Task and the
// background pair, and plans of up to thirty-two agent turns. So they move
// independently, and the panel has to be able to show one on and the other
// off without asking the daemon twice.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load, labelText } = require('./harness');

const SETTINGS = {
  auto_compact_enabled: true, show_tps: true, auto_delegate: false,
  auto_delegate_agent: '', auto_delegate_match: [],
  smart_agent: false, orchestrate_roster: ['explore', 'oracle'],
  orchestrate: false,
  skip_permissions: false, permission_rules: {}, can_edit_permissions: true,
};

async function openSettings(app) {
  app.el('settings-btn').click();
  await app.settle();
}

test('the switch is off, and says what that means', async () => {
  const app = await load({ routes: { 'GET /api/settings': SETTINGS } });
  await openSettings(app);

  assert.equal(app.el('orchestrate-checkbox').checked, false);
  assert.match(app.el('orchestrate-note').textContent, /^Off\./);
  assert.equal(app.callsTo('POST', '/api/settings/orchestrate').length, 0,
    'opening the panel changed a setting');
});

// Off means no delegation at all now, so that is what the note says. The
// old "One delegation at a time" was a claim about how many ran; this is a
// claim that none do, which is true with the switch off whatever the
// config declares.
test('off, the note says the model delegates to no agent', async () => {
  const app = await load({ routes: { 'GET /api/settings': SETTINGS } });
  await openSettings(app);

  const note = app.el('orchestrate-note').textContent;
  assert.match(note, /^Off\./);
  assert.match(note, /delegates to no agent/);
  assert.doesNotMatch(note, /one at a time|sequential/i);
});

// The sentence beside the box names both halves of what the switch gates:
// delegation, with the built-in specialists, and plans. How a plan runs
// (stages, four at a time in a fanout) is for the permission prompt and
// USAGE.md.
test('the sentence beside the box names delegation, the specialists and plans', () => {
  const label = labelText('orchestrate-checkbox');
  assert.match(label, /delegate to sub-agents/);
  assert.match(label, /six built-in specialists/);
  assert.match(label, /multi-stage plans/);
  assert.doesNotMatch(label, /at once/, 'the phrase an earlier sentence replaced');
  assert.doesNotMatch(label, /[\u2013\u2014]/, 'an em dash or en dash in a settings sentence');
  assert.ok(label.split(' ').length <= 20, `${label.split(' ').length} words is long-winded`);
});

// On, and the note carries the two ceilings that bound what a run can
// spend, each with its unit: "30 a run" was a number with nothing to say
// what it counted. The timeouts and the parallelism are not in the note.
// They are reference data that the permission prompt and USAGE.md hold, and
// stating them took the note to three lines, the longest text on the tab.
// internal/agent holds the two numbers here to the constants.
test('on, it names what a run can cost, with the unit of every limit', async () => {
  const app = await load({
    routes: { 'GET /api/settings': { ...SETTINGS, smart_agent: true, orchestrate: true } },
  });
  await openSettings(app);

  assert.equal(app.el('orchestrate-checkbox').checked, true);
  const note = app.el('orchestrate-note').textContent;
  assert.match(note, /^On\./);
  for (const limit of ['8 stages', '32 agent turns']) {
    assert.ok(note.includes(limit), `the note does not say ${limit}: ${note}`);
  }
  assert.doesNotMatch(note, /minutes|at a time/, `reference data in the note: ${note}`);
  // Asking is the default and not a rule: skip_all, skip_tools and an
  // allow rule all authorize the tool, so the note must not promise it.
  assert.match(note, /asks first unless pre-approved/);
  assert.doesNotMatch(note, /every run asks/i);
  // The specialists it brings, which the page cannot know, and that they
  // cost model calls.
  assert.match(note, /Specialists: explore, oracle\./);
  assert.match(note, /more model calls/i);
  // Two lines at most.
  assert.ok(note.length <= 170, `${note.length} characters is long for two lines: ${note}`);
});

// Orchestration brings its own roster, so its note does not depend on
// Smart Agent: the same with Smart Agent off as on, and unchanged when
// Smart Agent moves while the panel is open. The note used to tell a
// person with Smart Agent off to turn it on for somebody to delegate to.
test('the note is the same whether Smart Agent is on or off', async () => {
  const notes = [];
  for (const smartAgent of [false, true]) {
    const app = await load({
      routes: { 'GET /api/settings': { ...SETTINGS, smart_agent: smartAgent, orchestrate: true } },
    });
    await openSettings(app);
    notes.push(app.el('orchestrate-note').textContent);
  }
  assert.equal(notes[0], notes[1]);
  assert.doesNotMatch(notes[0], /Smart Agent|two or more agents/);

  const app = await load({
    routes: { 'GET /api/settings': { ...SETTINGS, smart_agent: false, orchestrate: true } },
  });
  await openSettings(app);
  const before = app.el('orchestrate-note').textContent;
  app.sse.emit({ type: 'config.changed', data: { smart_agent: true } });
  await app.settle();
  assert.equal(app.el('orchestrate-note').textContent, before);
});

test('ticking it tells the daemon', async () => {
  const app = await load({
    routes: {
      'GET /api/settings': SETTINGS,
      'POST /api/settings/orchestrate': { orchestrate: true, applied: true, persisted: true },
    },
  });
  await openSettings(app);

  app.el('orchestrate-checkbox').checked = true;
  app.el('orchestrate-checkbox').fire('change');
  await app.settle();

  const calls = app.callsTo('POST', '/api/settings/orchestrate');
  assert.equal(calls.length, 1);
  assert.deepEqual(calls[0].body, { enabled: true });
  assert.equal(app.el('orchestrate-checkbox').checked, true);
});

// Applied and saved are different questions, and the daemon answers both.
// A change that reached the running daemon but not config.json is still a
// change: the box has to keep showing it, with the warning beside it.
test('applied but not saved keeps the box and warns', async () => {
  const app = await load({
    routes: {
      'GET /api/settings': SETTINGS,
      'POST /api/settings/orchestrate': {
        orchestrate: true, applied: true, persisted: false, error: 'permission denied',
      },
    },
  });
  await openSettings(app);

  app.el('orchestrate-checkbox').checked = true;
  app.el('orchestrate-checkbox').fire('change');
  await app.settle();

  assert.equal(app.el('orchestrate-checkbox').checked, true,
    'an unsaved change was shown as a refused one, so the box now denies the state the daemon is in');
  assert.match(app.el('orchestrate-warn').textContent, /not saved to config\.json/);
  assert.equal(app.el('orchestrate-warn').hidden, false);
});

// Nothing was applied. Put the box back.
test('a refused change puts the box back', async () => {
  const app = await load({
    routes: {
      'GET /api/settings': SETTINGS,
      'POST /api/settings/orchestrate': { status: 500, body: 'nope' },
    },
  });
  await openSettings(app);

  app.el('orchestrate-checkbox').checked = true;
  app.el('orchestrate-checkbox').fire('change');
  await app.settle();

  assert.equal(app.el('orchestrate-checkbox').checked, false);
  assert.match(app.el('orchestrate-note').textContent, /^Not changed:/);
});

// The two switches are independent, which is the whole argument for there
// being two of them.
test('the two switches are drawn from one payload and move apart', async () => {
  const app = await load({
    routes: { 'GET /api/settings': { ...SETTINGS, smart_agent: true, orchestrate: false } },
  });
  await openSettings(app);

  assert.equal(app.el('smart-agent-checkbox').checked, true);
  assert.equal(app.el('orchestrate-checkbox').checked, false);
});
