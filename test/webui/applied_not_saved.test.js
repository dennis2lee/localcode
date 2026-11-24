'use strict';

// A change the daemon applied and could not write to config.json is a
// change that was made. The page shows it as made, and says under it that
// it lasts only until the daemon restarts.
//
// The switches in the settings window are in settings_restart.test.js.
// These are the two windows outside it whose routes answered a failed
// save with a 500 until they were given the same two-part answer: the
// delegation panel and the rules in the permissions window. The page
// read the 500 as nothing having changed, so the panel kept showing the
// old switch, agent and patterns, and the list kept or lost a rule, while
// the daemon ran the new ones.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load, defaultRoutes } = require('./harness');

const UNSAVED = /Applied, but not saved to config\.json, so it lasts only until the daemon restarts/;

// What the daemon gives as the reason when config.json cannot be written:
// its directory is read-only, so the file it writes beside it and renames
// cannot be made.
const NOT_WRITTEN = 'applied for this run, but failed to persist to config.json: create temp config: open /home/u/.localcode/.config-1.json: permission denied';

// ---- The delegation panel ----

// fakeDelegation answers POST /api/settings/auto-delegate the way the
// daemon does: it applies the fields the request carries, and answers
// with the whole setting as it now holds it and whether config.json was
// written.
function fakeDelegation(start = {}) {
  const held = { auto_delegate: false, auto_delegate_agent: '', auto_delegate_match: [], ...start };
  const d = {
    held,
    writable: true,
    refusal: null,
    route(body) {
      if (d.refusal) return d.refusal;
      if (typeof body.enabled === 'boolean') held.auto_delegate = body.enabled;
      if (typeof body.agent === 'string') held.auto_delegate_agent = body.agent;
      if (Array.isArray(body.match)) held.auto_delegate_match = body.match.slice();
      const reply = { ...held, auto_delegate_match: held.auto_delegate_match.slice(), applied: true, persisted: d.writable };
      if (!d.writable) reply.error = NOT_WRITTEN;
      return reply;
    },
  };
  return d;
}

async function openDelegation(daemon, extra = {}) {
  const app = await load({ routes: { 'POST /api/settings/auto-delegate': (body) => daemon.route(body), ...extra } });
  app.el('auto-delegate-btn').click();
  await app.settle();
  return app;
}

async function turnDelegation(app, on) {
  const box = app.el('delegate-enabled-checkbox');
  box.checked = on;
  box.fire('change');
  await app.settle();
}

async function chooseAgent(app, name) {
  const select = app.el('delegate-agent-select');
  select.value = name;
  select.fire('change');
  await app.settle();
}

async function addPattern(app, pattern) {
  app.el('delegate-match-input').value = pattern;
  app.el('delegate-match-add').click();
  await app.settle();
}

const patternsShown = (app) =>
  Array.from(app.el('delegate-match-list').children).filter((row) => row.className === 'match-row').map((row) => row.children[0].textContent);

test('the delegation switch the daemon could not write to config.json is shown on, with the warning', async () => {
  const daemon = fakeDelegation();
  daemon.writable = false;
  const app = await openDelegation(daemon);
  const warn = app.el('delegate-warn');
  assert.equal(warn.hidden, true);

  await turnDelegation(app, true);
  assert.deepEqual(app.callsTo('POST', '/api/settings/auto-delegate').map((c) => c.body), [{ enabled: true }]);
  assert.equal(app.el('delegate-enabled-checkbox').checked, true, 'the box went back while the daemon has it on');
  assert.equal(app.state.autoDelegate, true);
  assert.match(app.el('auto-delegate-btn').textContent, /auto-delegate: on/, 'the pill says off while the daemon has it on');
  assert.match(warn.textContent, UNSAVED);
  assert.ok(warn.textContent.includes('The switch'), `the warning does not say which half: ${warn.textContent}`);
  assert.ok(warn.textContent.includes('permission denied'), `the reason is missing: ${warn.textContent}`);
  assert.equal(warn.hidden, false);
  assert.equal(app.el('delegate-note').classList.contains('err'), false, 'a change that was applied is shown as an error');
});

test('an agent and a pattern the daemon could not write are shown as chosen, with the warning', async () => {
  const daemon = fakeDelegation();
  daemon.writable = false;
  const app = await openDelegation(daemon);
  const warn = app.el('delegate-warn');

  await chooseAgent(app, 'plan');
  assert.equal(app.el('delegate-agent-select').value, 'plan', 'the list went back while the daemon has the agent');
  assert.equal(app.state.autoDelegateAgent, 'plan');
  assert.match(warn.textContent, UNSAVED);
  assert.ok(warn.textContent.includes('The agent and patterns'), `the warning does not say which half: ${warn.textContent}`);
  // The line above the warning does not say the opposite.
  assert.equal(app.el('delegate-note').textContent.includes('written to config.json'), false, app.el('delegate-note').textContent);

  await addPattern(app, 'what is *');
  assert.deepEqual(patternsShown(app), ['what is *'], 'the pattern the daemon is running is not listed');
  assert.equal(app.el('delegate-match-input').value, '', 'the field was left as if the pattern had not been taken');
  assert.match(warn.textContent, UNSAVED);
});

// later holds each request to the route until the test lets its answer
// go, so two can be out at once and answered in either order. The answer
// is made when the request arrives, as the daemon makes it: the answer to
// the first does not know about the second.
function later(route) {
  const held = [];
  return {
    route: (body) => new Promise((resolve) => {
      const answer = route(body);
      held.push(() => resolve(answer));
    }),
    answer: (i) => held[i](),
    get count() { return held.length; },
  };
}

test('two delegation changes answered out of order leave the panel showing both', async () => {
  // The controls are not held while a request is out. The answer to the
  // first carries the whole setting as it was before the second.
  const daemon = fakeDelegation();
  const pending = later((body) => daemon.route(body));
  const app = await openDelegation(daemon, { 'POST /api/settings/auto-delegate': pending.route });

  await turnDelegation(app, true);
  await chooseAgent(app, 'plan');
  assert.equal(pending.count, 2, 'the second change waited for the first');
  pending.answer(1);
  await app.settle();
  pending.answer(0);
  await app.settle();

  assert.deepEqual(daemon.held, { auto_delegate: true, auto_delegate_agent: 'plan', auto_delegate_match: [] });
  assert.equal(app.el('delegate-enabled-checkbox').checked, true);
  assert.equal(app.el('delegate-agent-select').value, 'plan', 'the earlier answer put the agent back');
  assert.equal(app.state.autoDelegateAgent, 'plan');
  assert.equal(app.el('auto-delegate-btn').textContent, 'auto-delegate: on');
});

test('saving one half of the delegation setting leaves the warning about the other', async () => {
  // The switch and the target are written to config.json separately.
  const daemon = fakeDelegation();
  daemon.writable = false;
  const app = await openDelegation(daemon);
  const warn = app.el('delegate-warn');
  await turnDelegation(app, true);
  await chooseAgent(app, 'plan');
  assert.ok(warn.textContent.includes('The switch') && warn.textContent.includes('The agent and patterns'), warn.textContent);

  daemon.writable = true;
  await turnDelegation(app, false);
  assert.equal(warn.textContent.includes('The switch'), false, 'the switch was saved and its warning stayed');
  assert.ok(warn.textContent.includes('The agent and patterns'), 'saving the switch took the warning about the agent');
  assert.match(warn.textContent, UNSAVED);

  // The target is written whole, so saving a pattern saves the agent too.
  await addPattern(app, 'what is *');
  assert.equal(warn.textContent, '');
  assert.equal(warn.hidden, true);
  assert.ok(app.el('delegate-note').textContent.includes('written to config.json'), app.el('delegate-note').textContent);
});

test('the delegation warning stays through other redraws and a reopened panel', async () => {
  const daemon = fakeDelegation();
  daemon.writable = false;
  const app = await openDelegation(daemon);
  const warn = app.el('delegate-warn');
  await turnDelegation(app, true);
  assert.match(warn.textContent, UNSAVED);

  // The daemon announces the change, as it does to every window. With
  // another switch moved in the same announcement, which shows it was read.
  app.applyEvent({ type: 'settings.changed', data: { auto_delegate: true, smart_agent: true } });
  await app.settle();
  assert.equal(app.state.smartAgent, true, 'the announcement was not read');
  assert.match(warn.textContent, UNSAVED, 'the warning went with a redraw that saved nothing');
  assert.equal(warn.hidden, false);

  app.el('delegate-close').click();
  app.el('auto-delegate-btn').click();
  await app.settle();
  assert.match(warn.textContent, UNSAVED, 'the warning went when the panel was opened again');
  assert.equal(app.el('delegate-enabled-checkbox').checked, true);
});

for (const type of ['settings.changed', 'config.changed']) {
  test(`the delegation switch moved from somewhere else (${type}) loses the warning this window put under it`, async () => {
    // Another window, or "/config auto_delegate off" typed at a prompt.
    const daemon = fakeDelegation();
    daemon.writable = false;
    const app = await openDelegation(daemon);
    const warn = app.el('delegate-warn');
    await turnDelegation(app, true);
    await chooseAgent(app, 'plan');

    app.applyEvent({ type, data: { auto_delegate: false } });
    await app.settle();
    assert.equal(app.el('delegate-enabled-checkbox').checked, false);
    assert.equal(warn.textContent.includes('The switch'), false, 'a warning stayed under a switch somebody else has since moved');
    // The agent was not moved by anybody, and is still not saved.
    assert.ok(warn.textContent.includes('The agent and patterns'), 'the other half lost its warning');
  });
}

test('the announcement of this window\'s own delegation change does not take the warning its save earned', async () => {
  let reply;
  const daemon = fakeDelegation();
  daemon.writable = false;
  const app = await openDelegation(daemon, {
    'POST /api/settings/auto-delegate': (body) => new Promise((resolve) => { reply = () => resolve(daemon.route(body)); }),
  });
  const warn = app.el('delegate-warn');
  await turnDelegation(app, true);
  app.applyEvent({ type: 'settings.changed', data: { auto_delegate: true } });
  await app.settle();
  reply();
  await app.settle();
  assert.match(warn.textContent, UNSAVED, 'announcement first, reply second: the warning is missing');

  app.applyEvent({ type: 'settings.changed', data: { auto_delegate: true } });
  await app.settle();
  assert.match(warn.textContent, UNSAVED, 'reply first, announcement second: the warning went');
});

test('a delegation change the daemon refuses is not made, and the control goes back', async () => {
  const daemon = fakeDelegation();
  daemon.writable = false;
  const app = await openDelegation(daemon);
  const warn = app.el('delegate-warn');
  const note = app.el('delegate-note');
  await turnDelegation(app, true);
  assert.match(warn.textContent, UNSAVED);

  // An agent the daemon does not know: refused before anything is applied.
  daemon.refusal = { status: 400, body: { error: 'unknown agent "plan"' } };
  await chooseAgent(app, 'plan');
  assert.equal(app.el('delegate-agent-select').value, '', 'the list shows an agent the daemon refused');
  assert.equal(app.state.autoDelegateAgent, '');
  assert.ok(note.textContent.includes('unknown agent'), `the refusal is not said: ${note.textContent}`);
  assert.equal(note.classList.contains('err'), true);
  assert.equal(warn.textContent.includes('The agent and patterns'), false, 'a change that was not made is called applied');
  // The switch is still on and still not saved.
  assert.match(warn.textContent, UNSAVED, 'the refusal took the warning about the switch');
  assert.equal(app.el('delegate-enabled-checkbox').checked, true);

  // The next change that is applied takes the refusal away. The switch,
  // not a pattern: adding a pattern clears the note itself before it asks.
  daemon.refusal = null;
  daemon.writable = true;
  await turnDelegation(app, false);
  assert.equal(note.classList.contains('err'), false, 'a refusal stayed after a change that was applied');
  assert.equal(note.textContent.includes('unknown agent'), false);
});

// ---- The rules in the permissions window ----

// fakeRules answers the two rule routes the way the daemon does: the
// rule is in force either way, the answer carries every rule it now
// holds, and "persisted" says whether config.json has the change.
function fakeRules(start = {}) {
  const held = JSON.parse(JSON.stringify(start));
  const same = (body) => (r) => r.match === body.match && r.decision === body.decision;
  const d = {
    held,
    writable: true,
    refusal: null,
    reply() {
      const reply = { permission_rules: JSON.parse(JSON.stringify(held)), applied: true, persisted: d.writable };
      if (!d.writable) reply.error = NOT_WRITTEN;
      return reply;
    },
    add(body) {
      if (d.refusal) return d.refusal;
      held[body.tool] = held[body.tool] || [];
      if (!held[body.tool].some(same(body))) held[body.tool].push({ match: body.match, decision: body.decision });
      return d.reply();
    },
    remove(body) {
      if (d.refusal) return d.refusal;
      held[body.tool] = (held[body.tool] || []).filter((r) => !same(body)(r));
      if (held[body.tool].length === 0) delete held[body.tool];
      return d.reply();
    },
  };
  return d;
}

async function openRules(daemon, known = daemon.held) {
  const app = await load({
    routes: {
      'GET /api/settings': { ...defaultRoutes()['GET /api/settings'], permission_rules: JSON.parse(JSON.stringify(known)) },
      'POST /api/permissions/rules': (body) => daemon.add(body),
      'POST /api/permissions/rules/remove': (body) => daemon.remove(body),
    },
  });
  app.el('permission-status-btn').click();
  await app.settle();
  return app;
}

const ruleRows = (app) => Array.from(app.el('permission-rules-list').children).filter((row) => row.className === 'rule-row');
const rulesShown = (app) => ruleRows(app).map((row) => row.children[0].textContent);

async function addRule(app, tool, match, decision) {
  app.el('rule-tool-input').value = tool;
  app.el('rule-match-input').value = match;
  app.el('rule-decision-select').value = decision;
  app.el('rule-add-btn').click();
  await app.settle();
}

async function removeRule(app, text) {
  const row = ruleRows(app).find((r) => r.children[0].textContent === text);
  assert.ok(row, `no rule "${text}" is listed: ${rulesShown(app).join(' | ')}`);
  row.children[1].click();
  await app.settle();
}

test('a rule the daemon could not write to config.json is listed, with the warning', async () => {
  const daemon = fakeRules();
  daemon.writable = false;
  const app = await openRules(daemon);
  const warn = app.el('permission-rules-warn');
  assert.equal(warn.hidden, true);

  await addRule(app, 'bash', 'npm *', 'allow');
  assert.deepEqual(app.callsTo('POST', '/api/permissions/rules').map((c) => c.body), [{ tool: 'bash', match: 'npm *', decision: 'allow' }]);
  assert.deepEqual(rulesShown(app), ['bash: "npm *" -> allow'], 'the rule the daemon is running is not listed');
  assert.equal(app.el('rule-match-input').value, '', 'the fields were left as if the rule had not been taken');
  assert.match(warn.textContent, UNSAVED);
  assert.ok(warn.textContent.includes('added bash: "npm *" -> allow'), `the warning does not name the rule: ${warn.textContent}`);
  assert.ok(warn.textContent.includes('permission denied'), `the reason is missing: ${warn.textContent}`);
  assert.equal(warn.hidden, false);
  assert.doesNotMatch(app.el('permission-settings-note').textContent, /Failed to add rule/, 'a rule that was applied is called failed');
});

test('a rule removed and not written is gone from the list, with the warning', async () => {
  const daemon = fakeRules({ bash: [{ match: 'rm *', decision: 'deny' }, { match: 'ls *', decision: 'allow' }] });
  daemon.writable = false;
  const app = await openRules(daemon);
  const warn = app.el('permission-rules-warn');

  await removeRule(app, 'bash: "rm *" -> deny');
  assert.deepEqual(rulesShown(app), ['bash: "ls *" -> allow'], 'the list keeps a rule the daemon no longer runs');
  assert.match(warn.textContent, UNSAVED);
  assert.ok(warn.textContent.includes('removed bash: "rm *" -> deny'), `the warning does not name the rule: ${warn.textContent}`);
  assert.doesNotMatch(app.el('permission-settings-note').textContent, /Failed to remove rule/);
});

test('a rule that is saved does not take the warning about one that was not', async () => {
  // Rules are written one at a time: saving the second writes only the
  // second, and the first is still in force and still not in the file.
  const daemon = fakeRules();
  daemon.writable = false;
  const app = await openRules(daemon);
  const warn = app.el('permission-rules-warn');
  await addRule(app, 'bash', 'npm *', 'allow');

  daemon.writable = true;
  await addRule(app, 'bash', 'go *', 'allow');
  assert.deepEqual(rulesShown(app), ['bash: "npm *" -> allow', 'bash: "go *" -> allow']);
  assert.match(warn.textContent, UNSAVED, 'a later rule that was saved took the warning');
  assert.ok(warn.textContent.includes('added bash: "npm *" -> allow'));
  assert.equal(warn.textContent.includes('go *'), false, `a rule that was saved is named as not saved: ${warn.textContent}`);

  // The window closed and opened again: still not saved, still said.
  app.el('permission-settings-close').click();
  app.el('permission-status-btn').click();
  await app.settle();
  assert.match(warn.textContent, UNSAVED, 'the warning went when the window was opened again');

  // Added again with the file writable: the daemon and the file agree.
  await addRule(app, 'bash', 'npm *', 'allow');
  assert.deepEqual(rulesShown(app), ['bash: "npm *" -> allow', 'bash: "go *" -> allow'], 'the same rule is listed twice');
  assert.equal(warn.textContent, '');
  assert.equal(warn.hidden, true);
});

test('each rule change that was not saved is named, and one taken back is not', async () => {
  const daemon = fakeRules({ bash: [{ match: 'rm *', decision: 'deny' }] });
  daemon.writable = false;
  const app = await openRules(daemon);
  const warn = app.el('permission-rules-warn');
  await addRule(app, 'bash', 'npm *', 'allow');
  await removeRule(app, 'bash: "rm *" -> deny');
  assert.ok(warn.textContent.includes('added bash: "npm *" -> allow'), warn.textContent);
  assert.ok(warn.textContent.includes('removed bash: "rm *" -> deny'), warn.textContent);

  // The rule that was added and never reached the file is removed, and
  // that is saved: neither the daemon nor the file has it.
  daemon.writable = true;
  await removeRule(app, 'bash: "npm *" -> allow');
  assert.equal(warn.textContent.includes('npm *'), false, `a rule that is gone from both is still named: ${warn.textContent}`);
  assert.ok(warn.textContent.includes('removed bash: "rm *" -> deny'), 'the other change lost its warning');
});

test('a rule change taken back while config.json cannot be written leaves no warning', async () => {
  // The warning is for a rule the daemon and the file disagree about.
  // Added and removed again with neither written, the rule is in neither.
  const daemon = fakeRules({ bash: [{ match: 'ls *', decision: 'allow' }] });
  daemon.writable = false;
  const app = await openRules(daemon);
  const warn = app.el('permission-rules-warn');

  await addRule(app, 'bash', 'npm *', 'allow');
  assert.match(warn.textContent, UNSAVED);
  await removeRule(app, 'bash: "npm *" -> allow');
  assert.deepEqual(rulesShown(app), ['bash: "ls *" -> allow']);
  assert.equal(warn.textContent, '', 'a rule that is in neither the daemon nor the file is called not saved');
  assert.equal(warn.hidden, true);

  // And the other way: a rule the file has, removed and put back.
  await removeRule(app, 'bash: "ls *" -> allow');
  assert.ok(warn.textContent.includes('removed bash: "ls *" -> allow'), warn.textContent);
  await addRule(app, 'bash', 'ls *', 'allow');
  assert.deepEqual(rulesShown(app), ['bash: "ls *" -> allow']);
  assert.equal(warn.textContent, '', 'a rule that is in both the daemon and the file is called not saved');
  assert.equal(warn.hidden, true);

  // A rule the file has, added again and not written, is not a change.
  await addRule(app, 'bash', 'ls *', 'allow');
  assert.equal(warn.hidden, true, 'adding a rule the file already has is called not saved');
});

test('a rule the daemon refuses is not listed, and is said to have failed', async () => {
  const daemon = fakeRules({ bash: [{ match: 'ls *', decision: 'allow' }] });
  const app = await openRules(daemon);
  daemon.refusal = { status: 400, body: 'tool, match, and decision are all required' };

  await addRule(app, 'bash', 'npm *', 'allow');
  assert.deepEqual(rulesShown(app), ['bash: "ls *" -> allow'], 'a rule the daemon refused is listed');
  assert.match(app.el('permission-settings-note').textContent, /Failed to add rule/);
  assert.equal(app.el('permission-rules-warn').hidden, true, 'a rule that was not applied is called applied');

  await removeRule(app, 'bash: "ls *" -> allow');
  assert.deepEqual(rulesShown(app), ['bash: "ls *" -> allow'], 'a rule the daemon still runs is gone from the list');
  assert.match(app.el('permission-settings-note').textContent, /Failed to remove rule/);
  assert.equal(app.el('permission-rules-warn').hidden, true);
});

test('two rule changes answered out of order leave the list showing both', async () => {
  // One rule removed and another added while the first is still out. The
  // answer to the first lists the rules as they were before the second.
  const daemon = fakeRules({ bash: [{ match: 'rm *', decision: 'deny' }, { match: 'ls *', decision: 'allow' }] });
  const removes = later((body) => daemon.remove(body));
  const adds = later((body) => daemon.add(body));
  const app = await load({
    routes: {
      'GET /api/settings': { ...defaultRoutes()['GET /api/settings'], permission_rules: JSON.parse(JSON.stringify(daemon.held)) },
      'POST /api/permissions/rules': adds.route,
      'POST /api/permissions/rules/remove': removes.route,
    },
  });
  app.el('permission-status-btn').click();
  await app.settle();

  await removeRule(app, 'bash: "rm *" -> deny');
  await addRule(app, 'bash', 'npm *', 'allow');
  assert.equal(removes.count + adds.count, 2, 'the second change waited for the first');
  adds.answer(0);
  await app.settle();
  removes.answer(0);
  await app.settle();

  assert.deepEqual(daemon.held, { bash: [{ match: 'ls *', decision: 'allow' }, { match: 'npm *', decision: 'allow' }] });
  assert.deepEqual(rulesShown(app), ['bash: "ls *" -> allow', 'bash: "npm *" -> allow'], 'the earlier answer took the rule the later one added');
});
