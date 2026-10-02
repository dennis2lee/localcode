'use strict';

// The Orchestration switch in the settings window.
//
// A second switch beside Smart Agent, and the reason there are two is the
// reason these tests exist: the two are different sizes. Smart Agent lets
// the model hand one question to a specialist; this lets it commit to a
// shape and spend up to thirty-two agent turns on it. So they have to move
// independently, and the panel has to be able to show one on and the other
// off without asking the daemon twice.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load, labelText } = require('./harness');

const SETTINGS = {
  auto_compact_enabled: true, show_tps: true, auto_delegate: false,
  auto_delegate_agent: '', auto_delegate_match: [],
  smart_agent: false, smart_agent_roster: ['explore', 'oracle'],
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

// Off is a statement about the Orchestrate tool and nothing else. The old
// note, "One delegation at a time", was false the moment Smart Agent was
// on, because TaskBackground launches several. Any sentence about how
// many delegations run is the same claim reworded, so the whole word is
// refused, not one phrasing of it.
test('off, the note says the model cannot run plans, and nothing about delegation', async () => {
  const app = await load({ routes: { 'GET /api/settings': SETTINGS } });
  await openSettings(app);

  const note = app.el('orchestrate-note').textContent;
  assert.match(note, /^Off\./);
  assert.match(note, /cannot run/);
  assert.doesNotMatch(note, /delegat/i);
  assert.doesNotMatch(note, /one at a time|one agent|sequential/i);
});

// The sentence beside the box. A plan has stages and the agents inside a
// stage run together, which is the shape the old "several sub-agents at
// once" blurred: a step stage runs one agent, and a fanout runs at most
// four at a time. The sentence does not give the four; the permission
// prompt and USAGE.md do.
test('the sentence beside the box says a plan has stages and agents run in parallel inside one', () => {
  const label = labelText('orchestrate-checkbox');
  assert.match(label, /multi-stage plan/);
  assert.match(label, /sub-agents/);
  assert.match(label, /in parallel within a stage/);
  assert.doesNotMatch(label, /at once/, 'the phrase this sentence replaced');
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
  // With Smart Agent on the built-in roster is there, so no requirement
  // to state.
  assert.doesNotMatch(note, /two or more agents/);
  assert.ok(note.length <= 90, `${note.length} characters is long for one line: ${note}`);
});

// A plan needs somewhere to delegate its stages to: two or more agents,
// declared in config.json or supplied by Smart Agent. The panel cannot
// count them, so with Smart Agent off it states the requirement, which is
// true in every state, rather than claiming nobody is there. The old note
// did claim it for every config with Smart Agent off, including one that
// declares agents of its own (the default routes here declare two),
// because the roster it looked at was the built-in six and not the agents
// the config has.
test('on with Smart Agent off, it states the two-agent requirement and still shows the limits', async () => {
  const app = await load({
    routes: { 'GET /api/settings': { ...SETTINGS, smart_agent: false, orchestrate: true } },
  });
  await openSettings(app);

  const note = app.el('orchestrate-note').textContent;
  assert.match(note, /^On\./);
  assert.match(note, /Needs two or more agents/);
  assert.match(note, /config\.json/);
  assert.match(note, /turn on Smart Agent/);
  assert.match(note, /8 stages/);
  assert.doesNotMatch(note, /nobody/i);
  // The requirement is the one thing this state adds, and it must not
  // carry the note past two lines: it was 211 characters with the
  // timeouts in it.
  assert.ok(note.length <= 170, `${note.length} characters is long for two lines: ${note}`);
});

// The note follows Smart Agent while the panel is open: "/config smart_agent
// on" typed in the TUI, or the box ticked in another window, removes the
// requirement from the sentence without a reload.
test('Smart Agent turned on elsewhere drops the requirement from an open note', async () => {
  const app = await load({
    routes: { 'GET /api/settings': { ...SETTINGS, smart_agent: false, orchestrate: true } },
  });
  await openSettings(app);
  assert.match(app.el('orchestrate-note').textContent, /Needs two or more agents/);

  app.sse.emit({ type: 'settings.changed', data: { smart_agent: true, orchestrate: true } });
  await app.settle();

  assert.doesNotMatch(app.el('orchestrate-note').textContent, /Needs two or more agents/);
  assert.match(app.el('orchestrate-note').textContent, /8 stages/);
});

// "/config smart_agent on" typed into this conversation, in the TUI or in
// another client on the same session, emits config.changed and no
// settings.changed: the command appends to the session and never reaches
// the daemon's settings announcement. So the session-scoped handler has to
// redraw the note on its own.
test('Smart Agent turned on by a /config line in the session drops the requirement too', async () => {
  const app = await load({
    routes: { 'GET /api/settings': { ...SETTINGS, smart_agent: false, orchestrate: true } },
  });
  await openSettings(app);
  assert.match(app.el('orchestrate-note').textContent, /Needs two or more agents/);

  app.sse.emit({ type: 'config.changed', data: { smart_agent: true } });
  await app.settle();

  assert.doesNotMatch(app.el('orchestrate-note').textContent, /Needs two or more agents/);
  assert.match(app.el('orchestrate-note').textContent, /8 stages/);
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
