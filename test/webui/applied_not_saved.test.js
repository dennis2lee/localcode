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
      // The one agent this daemon does not know.
      if (body.agent === 'ghost') return { status: 400, body: { error: 'unknown agent "ghost"' } };
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
// go. The answer is made when the request arrives, as the daemon makes
// it. count is how many requests have arrived, answered or not.
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

// The panel does not hold its controls while a request is out. A second
// change used to be sent at once, the daemon applied the two in order,
// and the answers could arrive in the other one: the older value was
// then drawn last. Found by review, with the three below.
test('a delegation change made while another is out is sent after that one is answered', async () => {
  const daemon = fakeDelegation();
  const pending = later((body) => daemon.route(body));
  const app = await openDelegation(daemon, { 'POST /api/settings/auto-delegate': pending.route });

  await chooseAgent(app, 'general-purpose');
  await chooseAgent(app, 'plan');
  await turnDelegation(app, true);
  assert.equal(pending.count, 1, 'a second change was sent while the first was out, so their answers can cross');
  for (let i = 0; i < 3; i++) {
    assert.equal(pending.count, i + 1, 'a change was sent before the one ahead of it was answered');
    pending.answer(i);
    await app.settle();
  }

  assert.deepEqual(app.callsTo('POST', '/api/settings/auto-delegate').map((c) => c.body), [{ agent: 'general-purpose' }, { agent: 'plan' }, { enabled: true }]);
  assert.deepEqual(daemon.held, { auto_delegate: true, auto_delegate_agent: 'plan', auto_delegate_match: [] });
  assert.equal(app.el('delegate-agent-select').value, 'plan', 'the panel shows the agent that was chosen first');
  assert.equal(app.el('delegate-enabled-checkbox').checked, true);
  assert.equal(app.el('auto-delegate-btn').textContent, 'auto-delegate: on');
});

test('the answer to an earlier delegation save does not decide the warning about a later one', async () => {
  // The agent is saved. The pattern after it is not. Both are the target,
  // and share one warning: with the answers crossed, the save that
  // worked took the warning the one that failed had just earned.
  const daemon = fakeDelegation();
  const pending = later((body) => daemon.route(body));
  const app = await openDelegation(daemon, { 'POST /api/settings/auto-delegate': pending.route });
  const warn = app.el('delegate-warn');

  await chooseAgent(app, 'plan');
  await addPattern(app, 'what is *');
  // The agent has reached the file. The pattern is sent when the agent
  // is answered, to a daemon that can no longer write.
  daemon.writable = false;
  pending.answer(0);
  await app.settle();
  assert.equal(pending.count, 2);
  pending.answer(1);
  await app.settle();

  assert.deepEqual(patternsShown(app), ['what is *']);
  assert.match(warn.textContent, UNSAVED, 'the pattern is not in config.json and nothing says so');
  assert.ok(warn.textContent.includes('The agent and patterns'));
});

test('two patterns added one after the other are both kept', async () => {
  // The patterns are sent as a whole list. Built when the button was
  // pressed, the second list did not have the first pattern in it, and
  // the daemon dropped the first.
  const daemon = fakeDelegation();
  const pending = later((body) => daemon.route(body));
  const app = await openDelegation(daemon, { 'POST /api/settings/auto-delegate': pending.route });

  await addPattern(app, 'what is *');
  await addPattern(app, 'where is *');
  // The second is typed and added before the first is answered, and is
  // still in the field: the answer to the first must not clear it.
  pending.answer(0);
  await app.settle();
  assert.equal(pending.count, 2);
  pending.answer(1);
  await app.settle();

  assert.deepEqual(daemon.held.auto_delegate_match, ['what is *', 'where is *'], 'the daemon lost a pattern');
  assert.deepEqual(patternsShown(app), ['what is *', 'where is *']);
  assert.equal(app.el('delegate-match-input').value, '');

  // The same pattern asked for twice is sent once.
  await addPattern(app, 'how do *');
  app.el('delegate-match-input').value = 'how do *';
  app.el('delegate-match-add').click();
  await app.settle();
  pending.answer(2);
  await app.settle();
  assert.equal(pending.count, 3, 'a pattern the list already has was sent again');
  assert.deepEqual(patternsShown(app), ['what is *', 'where is *', 'how do *']);
});

test('a pattern added again while its removal is out is kept', async () => {
  // Whether the list has the pattern is asked when the addition's turn
  // comes. Asked when the button was pressed, the pattern was still
  // listed, the addition was dropped, and the removal then took it.
  const daemon = fakeDelegation({ auto_delegate_match: ['what is *', 'where is *'] });
  const pending = later((body) => daemon.route(body));
  const app = await load({
    routes: {
      'GET /api/settings': { ...defaultRoutes()['GET /api/settings'], auto_delegate_match: ['what is *', 'where is *'] },
      'POST /api/settings/auto-delegate': pending.route,
    },
  });
  app.el('auto-delegate-btn').click();
  await app.settle();
  const remove = (pattern) => {
    const row = Array.from(app.el('delegate-match-list').children).find((r) => r.className === 'match-row' && r.children[0].textContent === pattern);
    assert.ok(row, `"${pattern}" is not listed: ${patternsShown(app).join(' | ')}`);
    row.children[1].click();
  };

  remove('what is *');
  // Pressed twice before the first is answered: sent once.
  remove('what is *');
  await addPattern(app, 'what is *');
  assert.equal(pending.count, 1);
  pending.answer(0);
  await app.settle();
  assert.deepEqual(app.callsTo('POST', '/api/settings/auto-delegate')[0].body, { match: ['where is *'] });
  assert.equal(pending.count, 2, 'the second removal of the same pattern was sent, or the addition was dropped');
  pending.answer(1);
  await app.settle();

  assert.equal(pending.count, 2);
  assert.deepEqual(daemon.held.auto_delegate_match, ['where is *', 'what is *'], 'the pattern added again was lost');
  assert.deepEqual(patternsShown(app), ['where is *', 'what is *']);
  assert.equal(app.el('delegate-match-input').value, '');
});

test('a pattern the list already has is not sent, and the field is cleared', async () => {
  const daemon = fakeDelegation({ auto_delegate_match: ['what is *'] });
  const app = await load({
    routes: {
      'GET /api/settings': { ...defaultRoutes()['GET /api/settings'], auto_delegate_match: ['what is *'] },
      'POST /api/settings/auto-delegate': (body) => daemon.route(body),
    },
  });
  app.el('auto-delegate-btn').click();
  await app.settle();
  await addPattern(app, 'what is *');
  assert.equal(app.callsTo('POST', '/api/settings/auto-delegate').length, 0);
  assert.deepEqual(patternsShown(app), ['what is *']);
  assert.equal(app.el('delegate-match-input').value, '');
});

test('a change that throws is told to whoever made it, and the ones behind it still go', async () => {
  // No control builds a change that throws. If one ever does, it must
  // not leave every later change in that window waiting behind it.
  const daemon = fakeDelegation();
  const app = await openDelegation(daemon);
  await assert.rejects(app.internals.saveAutoDelegate(() => { throw new Error('a change that cannot be built'); }), /cannot be built/);
  await turnDelegation(app, true);
  assert.equal(daemon.held.auto_delegate, true, 'the change behind the one that threw was never sent');
  assert.equal(app.el('delegate-enabled-checkbox').checked, true);
});

test('a pattern typed while another is being added is not cleared by the answer', async () => {
  const daemon = fakeDelegation();
  const pending = later((body) => daemon.route(body));
  const app = await openDelegation(daemon, { 'POST /api/settings/auto-delegate': pending.route });

  await addPattern(app, 'what is *');
  app.el('delegate-match-input').value = 'where is';
  pending.answer(0);
  await app.settle();
  assert.equal(app.el('delegate-match-input').value, 'where is', 'the answer cleared what was being typed');
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
    // A rule added here is a pattern, and is added beside an exact rule
    // of the same text. A rule removed takes both. See addRule and
    // removeRule in internal/config/rules.go.
    add(body) {
      if (d.refusal) return d.refusal;
      held[body.tool] = held[body.tool] || [];
      if (!held[body.tool].some((r) => same(body)(r) && !r.literal)) held[body.tool].push({ match: body.match, decision: body.decision });
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

async function openRulesLater(daemon, extra = {}) {
  const removes = later((body) => daemon.remove(body));
  const adds = later((body) => daemon.add(body));
  const app = await load({
    routes: {
      'GET /api/settings': { ...defaultRoutes()['GET /api/settings'], permission_rules: JSON.parse(JSON.stringify(daemon.held)) },
      'POST /api/permissions/rules': adds.route,
      'POST /api/permissions/rules/remove': removes.route,
      ...extra,
    },
  });
  app.el('permission-status-btn').click();
  await app.settle();
  return { app, adds, removes };
}

// The list does not hold its buttons while a request is out. Two changes
// used to be out at once, and each was applied to the list when its
// answer came, in whichever order that was. Found by review.
test('a rule removed and added again while the removal is out is listed, and warned about when not saved', async () => {
  // With the answers crossed, the addition found the rule still listed
  // and did nothing, and the removal then took it: the daemon ran a rule
  // the list did not show, and nothing said it was not in the file.
  const daemon = fakeRules({ bash: [{ match: 'ls *', decision: 'allow' }] });
  const { app, adds, removes } = await openRulesLater(daemon);

  await removeRule(app, 'bash: "ls *" -> allow');
  await addRule(app, 'bash', 'ls *', 'allow');
  assert.equal(adds.count, 0, 'the addition was sent while the removal was out, so their answers can cross');
  // The removal has reached the file. The addition is sent when the
  // removal is answered, to a daemon that can no longer write.
  daemon.writable = false;
  removes.answer(0);
  await app.settle();
  assert.equal(adds.count, 1);
  adds.answer(0);
  await app.settle();

  assert.deepEqual(daemon.held, { bash: [{ match: 'ls *', decision: 'allow' }] });
  assert.deepEqual(rulesShown(app), ['bash: "ls *" -> allow'], 'the list does not show a rule the daemon runs');
  assert.match(app.el('permission-rules-warn').textContent, UNSAVED);
  assert.ok(app.el('permission-rules-warn').textContent.includes('added bash: "ls *" -> allow'));
});

test('two rules added one after the other are listed in the order the daemon holds them', async () => {
  // The last rule that matches decides, so the order is what the rules
  // mean: allow then deny refuses, deny then allow permits.
  const daemon = fakeRules();
  const { app, adds } = await openRulesLater(daemon);

  await addRule(app, 'bash', 'npm *', 'allow');
  await addRule(app, 'bash', 'npm *', 'deny');
  assert.equal(adds.count, 1, 'the second rule was sent while the first was out');
  // The second rule is still in the fields: the answer to the first
  // leaves it there.
  adds.answer(0);
  await app.settle();
  assert.equal(app.el('rule-match-input').value, 'npm *', 'the answer cleared a rule that was not the one it answered');
  adds.answer(1);
  await app.settle();

  assert.deepEqual(daemon.held, { bash: [{ match: 'npm *', decision: 'allow' }, { match: 'npm *', decision: 'deny' }] });
  assert.deepEqual(rulesShown(app), ['bash: "npm *" -> allow', 'bash: "npm *" -> deny']);
  assert.equal(app.el('rule-match-input').value, '');
});

// A rule is a pattern or an exact command, and the daemon keeps the two
// apart: "always allow" at a prompt writes an exact one, this window
// adds patterns. Found by review: the page compared only the text.
test('a pattern added beside an exact rule of the same text is listed, and warned about when not saved', async () => {
  const daemon = fakeRules({ bash: [{ match: 'echo *', decision: 'allow', literal: true }] });
  daemon.writable = false;
  const app = await openRules(daemon);
  assert.deepEqual(rulesShown(app), ['bash: "echo *" (exact) -> allow']);

  await addRule(app, 'bash', 'echo *', 'allow');
  assert.equal(daemon.held.bash.length, 2, 'the fake daemon is not adding the pattern beside the exact rule');
  assert.deepEqual(rulesShown(app), ['bash: "echo *" (exact) -> allow', 'bash: "echo *" -> allow'], 'the pattern the daemon now runs is not listed');
  const warn = app.el('permission-rules-warn');
  assert.match(warn.textContent, UNSAVED);
  assert.ok(warn.textContent.includes('added bash: "echo *" -> allow'), warn.textContent);
  assert.equal(warn.textContent.includes('(exact)'), false, `the exact rule, which the file has, is named: ${warn.textContent}`);

  // Added a second time, it is still one pattern.
  await addRule(app, 'bash', 'echo *', 'allow');
  assert.equal(rulesShown(app).length, 2);
});

test('an exact rule in the file replaced by a pattern in force keeps its warning', async () => {
  // Removed and not saved: the file still has the exact rule. A pattern
  // of the same text added after it is in force and not in the file. The
  // two hold different rules, which read as "the same text is in both".
  const daemon = fakeRules({ bash: [{ match: 'echo *', decision: 'allow', literal: true }] });
  daemon.writable = false;
  const app = await openRules(daemon);
  const warn = app.el('permission-rules-warn');

  await removeRule(app, 'bash: "echo *" (exact) -> allow');
  assert.ok(warn.textContent.includes('removed bash: "echo *" (exact) -> allow'), warn.textContent);
  await addRule(app, 'bash', 'echo *', 'allow');
  assert.deepEqual(rulesShown(app), ['bash: "echo *" -> allow']);
  assert.match(warn.textContent, UNSAVED, 'the daemon runs a pattern, the file holds an exact rule, and nothing says so');
  assert.ok(warn.textContent.includes('added bash: "echo *" -> allow'), warn.textContent);
  assert.ok(warn.textContent.includes('removed bash: "echo *" (exact) -> allow'), warn.textContent);

  // Saved, the pattern is in the file. The exact rule is still there too,
  // and still not in force.
  daemon.writable = true;
  await addRule(app, 'bash', 'echo *', 'allow');
  assert.equal(warn.textContent.includes('added bash'), false, `a pattern that was saved is named: ${warn.textContent}`);
  assert.ok(warn.textContent.includes('removed bash: "echo *" (exact) -> allow'), 'saving the pattern took the warning about the exact rule');

  // Removing the text again, saved, takes both from the file.
  await removeRule(app, 'bash: "echo *" -> allow');
  assert.equal(warn.textContent, '');
  assert.equal(warn.hidden, true);
});

test('removing a rule takes the exact rule and the pattern of the same text together', async () => {
  const daemon = fakeRules({ bash: [{ match: 'echo *', decision: 'allow', literal: true }, { match: 'echo *', decision: 'allow' }, { match: 'echo *', decision: 'deny' }] });
  const app = await openRules(daemon);
  await removeRule(app, 'bash: "echo *" -> allow');
  assert.deepEqual(daemon.held, { bash: [{ match: 'echo *', decision: 'deny' }] });
  assert.deepEqual(rulesShown(app), ['bash: "echo *" -> deny']);
});

test('a rule change that is applied takes away what a refused one left under the list', async () => {
  const daemon = fakeRules();
  const app = await openRules(daemon);
  const note = app.el('permission-settings-note');
  daemon.refusal = { status: 400, body: 'unknown decision "permit"' };
  await addRule(app, 'bash', 'npm *', 'allow');
  assert.match(note.textContent, /Failed to add rule/);

  daemon.refusal = null;
  await addRule(app, 'bash', 'npm *', 'allow');
  assert.doesNotMatch(note.textContent, /Failed to add rule/, 'the refusal is still said under a rule that was added');
  assert.ok(note.textContent.includes('written to config.json'), note.textContent);

  // And beside a warning that a rule was not written, the line does not
  // say rules are written.
  daemon.writable = false;
  await addRule(app, 'bash', 'go *', 'allow');
  assert.match(app.el('permission-rules-warn').textContent, UNSAVED);
  assert.equal(note.textContent.includes('written to config.json'), false, note.textContent);
  app.el('permission-settings-close').click();
  app.el('permission-status-btn').click();
  await app.settle();
  assert.equal(note.textContent.includes('written to config.json'), false, 'opened again, the line says rules are written');
});

test('a rule change that is applied leaves a switch that could not be changed said', async () => {
  // The same line says when one of the four switches above the rules
  // could not be changed, and the box is left where it was put: the line
  // is the only sign the daemon does not have that value. Found by review
  // of the test before this one, whose fix took the line away.
  const daemon = fakeRules();
  const app = await load({
    routes: {
      'POST /api/sessions/sess-1/permissions': { status: 500, body: { error: 'the session log could not be written' } },
      'POST /api/permissions/rules': (body) => daemon.add(body),
      'POST /api/permissions/rules/remove': (body) => daemon.remove(body),
    },
  });
  app.el('permission-status-btn').click();
  await app.settle();
  const note = app.el('permission-settings-note');

  const box = app.el('skip-permissions-checkbox');
  box.checked = true;
  box.fire('change');
  await app.settle();
  assert.match(note.textContent, /could not change skip_all/);
  assert.equal(note.classList.contains('err'), true);

  await addRule(app, 'bash', 'npm *', 'allow');
  assert.deepEqual(rulesShown(app), ['bash: "npm *" -> allow']);
  assert.match(note.textContent, /could not change skip_all/, 'a rule that was added took away what says the switch was not changed');
  assert.equal(note.classList.contains('err'), true);
  await removeRule(app, 'bash: "npm *" -> allow');
  assert.match(note.textContent, /could not change skip_all/, 'a rule that was removed took it away');

  // A rule the daemon refuses is said beside it, not over it, and goes
  // alone when the next rule is applied.
  daemon.refusal = { status: 400, body: 'unknown decision "permit"' };
  await addRule(app, 'bash', 'go *', 'allow');
  assert.match(note.textContent, /Failed to add rule/);
  assert.match(note.textContent, /could not change skip_all/, 'a refused rule was written over the switch that was not changed');
  daemon.refusal = null;
  await addRule(app, 'bash', 'go *', 'allow');
  assert.doesNotMatch(note.textContent, /Failed to add rule/, 'the refusal stayed after a rule was applied');
  assert.match(note.textContent, /could not change skip_all/);
  assert.equal(note.classList.contains('err'), true);

  // Opened again, the boxes are drawn from what the daemon has and the
  // line starts over.
  app.el('permission-settings-close').click();
  app.el('permission-status-btn').click();
  await app.settle();
  assert.equal(box.checked, false);
  assert.equal(note.classList.contains('err'), false);
  assert.doesNotMatch(note.textContent, /could not change/);
});

test('a rule change answered after a switch could not be changed leaves that said too', async () => {
  // The rule was sent first and answered last. What the line holds is
  // looked at when the answer comes, not when the rule was sent.
  const daemon = fakeRules();
  const { app, adds } = await openRulesLater(daemon, {
    'POST /api/sessions/sess-1/permissions': { status: 500, body: { error: 'the session log could not be written' } },
  });
  const note = app.el('permission-settings-note');

  await addRule(app, 'bash', 'npm *', 'allow');
  const box = app.el('read-outside-checkbox');
  box.checked = true;
  box.fire('change');
  await app.settle();
  assert.match(note.textContent, /could not change read_outside/);
  adds.answer(0);
  await app.settle();

  assert.deepEqual(rulesShown(app), ['bash: "npm *" -> allow']);
  assert.match(note.textContent, /could not change read_outside/, 'the answer to the rule took away what says the switch was not changed');
  assert.equal(note.classList.contains('err'), true);
});

// ---- The two lines that say something went wrong ----

// Each window has one line under it that says, among other things, what
// the daemon refused. Three rounds of review each found a sequence that
// left one of them saying something untrue: a refusal kept after a later
// change was applied, a switch failure taken away by a rule, the error
// mark taken off words that stayed. Each line is now drawn from what it
// has to say, in one place, and the tests below walk every order of the
// things that can happen to it and hold the line to what happened, not
// to how it is drawn.

// sequences is every run of the events, from one long to `longest`.
function sequences(events, longest) {
  let runs = [[]];
  const all = [];
  for (let n = 0; n < longest; n++) {
    runs = runs.flatMap((run) => events.map((e) => [...run, e]));
    all.push(...runs);
  }
  return all;
}

test('the line under the delegation panel says a refusal exactly while the last change sent was refused', async () => {
  const daemon = fakeDelegation({ auto_delegate_match: ['seed *'] });
  const app = await load({
    routes: {
      'GET /api/settings': { ...defaultRoutes()['GET /api/settings'], auto_delegate_match: ['seed *'] },
      'POST /api/settings/auto-delegate': (body) => daemon.route(body),
    },
  });
  const note = app.el('delegate-note');
  const events = {
    // An agent the daemon does not know: sent, and refused.
    refused: () => chooseAgent(app, 'ghost'),
    // The switch: sent, and applied.
    applied: () => turnDelegation(app, !app.el('delegate-enabled-checkbox').checked),
    // A pattern the list has: nothing is sent.
    nothing: () => addPattern(app, 'seed *'),
    // A second pattern, added when the list does not have it and removed
    // when it does: sent, and applied, either way.
    pattern: async () => {
      const row = Array.from(app.el('delegate-match-list').children).find((r) => r.className === 'match-row' && r.children[0].textContent === 'other *');
      if (!row) return addPattern(app, 'other *');
      row.children[1].click();
      return app.settle(5);
    },
  };
  for (const run of sequences(Object.keys(events), 4)) {
    app.el('delegate-close').click();
    app.el('auto-delegate-btn').click();
    await app.settle(5);
    let refused = false;
    for (const name of run) {
      await events[name]();
      if (name === 'refused') refused = true;
      if (name === 'applied' || name === 'pattern') refused = false;
      const where = `after ${run.join(', ')} (at ${name}): ${note.textContent}`;
      assert.equal(note.textContent.includes('unknown agent'), refused, `the refusal is ${refused ? 'not said' : 'still said'} ${where}`);
      assert.equal(note.classList.contains('err'), refused, `the error mark is ${refused ? 'missing' : 'left on'} ${where}`);
    }
  }
});

test('the line under the rules says each switch that could not be changed and the last rule that was refused, and nothing else', async () => {
  const daemon = fakeRules();
  const failing = new Set();
  const effective = {};
  const app = await load({
    routes: {
      'POST /api/sessions/sess-1/permissions': (body) => {
        if (failing.has(body.switch)) return { status: 500, body: { error: 'the session log could not be written' } };
        effective[body.switch] = body.enabled;
        return { effective: { ...effective }, source: {}, remembered: { read: [], write: [] } };
      },
      'POST /api/permissions/rules': (body) => daemon.add(body),
      'POST /api/permissions/rules/remove': (body) => daemon.remove(body),
    },
  });
  const note = app.el('permission-settings-note');
  const BOXES = { skip_all: 'skip-permissions-checkbox', read_outside: 'read-outside-checkbox' };
  const turn = async (name, fails) => {
    if (fails) failing.add(name); else failing.delete(name);
    const box = app.el(BOXES[name]);
    box.checked = !box.checked;
    box.fire('change');
    await app.settle(5);
  };
  let fresh = 0;
  const addOne = async (refused) => {
    daemon.refusal = refused ? { status: 400, body: 'unknown decision "permit"' } : null;
    await addRule(app, 'bash', `r${fresh++} *`, 'allow');
    daemon.refusal = null;
  };
  // What each event leaves true: which switches the daemon refused and
  // has not since taken, and whether the last rule change was refused.
  const events = {
    'skip_all fails': async (m) => { await turn('skip_all', true); m.failed.add('skip_all'); },
    'skip_all taken': async (m) => { await turn('skip_all', false); m.failed.delete('skip_all'); },
    'read_outside fails': async (m) => { await turn('read_outside', true); m.failed.add('read_outside'); },
    'rule refused': async (m) => { await addOne(true); m.refused = true; },
    'rule added': async (m) => { await addOne(false); m.refused = false; },
    'rule removed': async (m) => {
      const listed = ruleRows(app);
      if (listed.length === 0) return;
      listed[0].children[1].click();
      await app.settle(5);
      m.refused = false;
    },
  };
  for (const run of sequences(Object.keys(events), 4)) {
    // Opened again: the boxes are drawn from what the daemon has, and
    // the line starts over.
    app.el('permission-settings-close').click();
    app.el('permission-status-btn').click();
    await app.settle(5);
    const m = { failed: new Set(), refused: false };
    for (const name of run) {
      await events[name](m);
      const where = `after ${run.join(', ')} (at ${name}): ${note.textContent}`;
      for (const sw of Object.keys(BOXES)) {
        assert.equal(note.textContent.includes(`could not change ${sw}`), m.failed.has(sw), `${sw} ${m.failed.has(sw) ? 'is not said' : 'is still said'} ${where}`);
      }
      assert.equal(note.classList.contains('err'), m.failed.size > 0, `the error mark is wrong ${where}`);
      assert.equal(/Failed to (add|remove) rule/.test(note.textContent), m.refused, `the refused rule ${m.refused ? 'is not said' : 'is still said'} ${where}`);
      assert.equal(note.textContent.includes('Rules apply immediately'), m.failed.size === 0 && !m.refused, `the line about how rules are kept is wrong ${where}`);
    }
  }
});

test('a rule that was refused is said without the error mark, and goes when a rule is removed or the window is opened again', async () => {
  const daemon = fakeRules({ bash: [{ match: 'ls *', decision: 'allow' }] });
  const app = await openRules(daemon);
  const note = app.el('permission-settings-note');

  daemon.refusal = { status: 400, body: 'unknown decision "permit"' };
  await removeRule(app, 'bash: "ls *" -> allow');
  assert.match(note.textContent, /Failed to remove rule/);
  assert.equal(note.classList.contains('err'), false, 'a refused rule is marked as the switches are');
  daemon.refusal = null;
  await removeRule(app, 'bash: "ls *" -> allow');
  assert.doesNotMatch(note.textContent, /Failed to remove rule/, 'the refusal stayed after a rule was removed');

  daemon.refusal = { status: 400, body: 'unknown decision "permit"' };
  await addRule(app, 'bash', 'npm *', 'allow');
  assert.match(note.textContent, /Failed to add rule/);
  app.el('permission-settings-close').click();
  app.el('permission-status-btn').click();
  await app.settle();
  assert.doesNotMatch(note.textContent, /Failed to add rule/, 'the refusal was still said when the window was opened again');
});

test('a switch that could not be changed is said in a run that cannot edit rules', async () => {
  // No config.json path: the rules are read-only and the line says so.
  // The four switches belong to the conversation and can still fail.
  const app = await load({
    routes: {
      'GET /api/settings': { ...defaultRoutes()['GET /api/settings'], can_edit_permissions: false },
      'POST /api/sessions/sess-1/permissions': { status: 500, body: { error: 'the session log could not be written' } },
    },
  });
  app.el('permission-status-btn').click();
  await app.settle();
  const note = app.el('permission-settings-note');
  assert.match(note.textContent, /rules are read-only/);
  const box = app.el('write-outside-checkbox');
  box.checked = true;
  box.fire('change');
  await app.settle();
  assert.match(note.textContent, /could not change write_outside/);
  assert.equal(note.classList.contains('err'), true);
});
