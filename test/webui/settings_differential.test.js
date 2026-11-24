'use strict';

// After every request has been answered and every announcement read, a
// page that has been open all along shows what a page loaded just then
// shows.
//
// The tests beside this one each walk a sequence somebody thought of.
// This walks sequences nobody did: a seeded run of changes in the
// settings window, the delegation panel and the rules list, against a
// daemon that fails some saves, refuses some requests, answers late and
// announces late. The first version of it was written by a reviewer of
// the two-part answers (v0.179.1), where 42 of 200 sequences disagreed:
// two changes out at once, with their answers crossed, left the older
// value drawn, a rule in the wrong place, or a rule missing. Changes now
// go one at a time in each window (serial, in modals.js), and none do.
//
// It also holds the rules warning to the daemon itself. The fake keeps
// what it is running and what its file holds, so "a rule is named as not
// saved exactly when the two disagree about it" is checked against the
// two, not against the page's own reasoning.
//
// Not walked: a setting moved from somewhere else while this page has a
// request out. The answer can then arrive after the announcement of the
// other change and be drawn over it, for every switch that answers in
// two parts. That is older than this test and is not what it is about.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load, defaultRoutes } = require('./harness');

// A small seeded generator, so a run that fails can be run again.
function seeded(seed) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const pick = (rng, list) => list[Math.floor(rng() * list.length)];
const copy = (v) => JSON.parse(JSON.stringify(v));
const NOT_WRITTEN = 'applied for this run, but failed to persist to config.json: permission denied';

// fakeDaemon answers the five routes the page calls as the daemon does:
// it applies, announces, writes its file when it can, and answers in two
// parts. Answers and announcements are held until the run lets them go.
function fakeDaemon(rules) {
  const running = {
    keep_going: true, repeat_limit: 0,
    auto_delegate: false, auto_delegate_agent: '', auto_delegate_match: [],
    permission_rules: copy(rules),
  };
  const file = copy(running);
  const d = { running, file, writable: true, answers: [], announcements: [] };

  d.settings = () => ({ ...defaultRoutes()['GET /api/settings'], ...copy(running) });
  const announce = () => d.announcements.push({
    type: 'settings.changed',
    data: { keep_going: running.keep_going, repeat_limit: running.repeat_limit, auto_delegate: running.auto_delegate },
  });
  const held = (label, answer) => new Promise((resolve) => d.answers.push({ label, go: () => resolve(answer) }));
  const twoParts = (value, persisted) => (persisted ? { ...value, applied: true, persisted } : { ...value, applied: true, persisted, error: NOT_WRITTEN });
  // One switch: applied, announced, written when the file can be.
  const one = (key, value, label) => {
    running[key] = value;
    announce();
    if (d.writable) file[key] = value;
    return held(label, twoParts({ [key]: value }, d.writable));
  };
  const sameText = (body) => (r) => r.match === body.match && r.decision === body.decision;
  // addRule and removeRule in internal/config/rules.go: a rule added is
  // a pattern, added beside an exact rule of the same text and never
  // twice; a rule removed takes every rule of that text.
  const add = (set, body) => {
    const list = set[body.tool] = set[body.tool] || [];
    if (!list.some((r) => sameText(body)(r) && !r.literal)) list.push({ match: body.match, decision: body.decision });
  };
  const remove = (set, body) => {
    const kept = (set[body.tool] || []).filter((r) => !sameText(body)(r));
    if (kept.length > 0) set[body.tool] = kept;
    else delete set[body.tool];
  };
  const ruleRoute = (change, name) => (body) => {
    change(running.permission_rules, body);
    if (d.writable) change(file.permission_rules, body);
    return held(`${name} ${body.match} ${body.decision}`, twoParts({ permission_rules: copy(running.permission_rules) }, d.writable));
  };

  d.routes = {
    'GET /api/settings': d.settings,
    'POST /api/settings/keep-going': (body) => one('keep_going', !!body.enabled, `keep-going ${body.enabled}`),
    'POST /api/settings/repeat-limit': (body) => (body.limit < 0 || body.limit > 50
      ? held(`repeat-limit ${body.limit} refused`, { status: 400, body: 'limit must be between 0 (off) and 50' })
      : one('repeat_limit', body.limit, `repeat-limit ${body.limit}`)),
    'POST /api/settings/auto-delegate': (body) => {
      if (body.agent === 'ghost') return held('delegate refused', { status: 400, body: { error: 'unknown agent "ghost"' } });
      // The target and the switch are written separately.
      if (typeof body.agent === 'string') running.auto_delegate_agent = body.agent;
      if (Array.isArray(body.match)) running.auto_delegate_match = body.match.slice();
      if (typeof body.enabled === 'boolean') running.auto_delegate = body.enabled;
      if (d.writable) {
        if (typeof body.agent === 'string' || Array.isArray(body.match)) {
          file.auto_delegate_agent = running.auto_delegate_agent;
          file.auto_delegate_match = running.auto_delegate_match.slice();
        }
        if (typeof body.enabled === 'boolean') file.auto_delegate = running.auto_delegate;
      }
      announce();
      return held(`delegate ${JSON.stringify(body)}`, twoParts({
        auto_delegate: running.auto_delegate,
        auto_delegate_agent: running.auto_delegate_agent,
        auto_delegate_match: running.auto_delegate_match.slice(),
      }, d.writable));
    },
    'POST /api/permissions/rules': ruleRoute(add, 'add'),
    'POST /api/permissions/rules/remove': ruleRoute(remove, 'remove'),
  };
  // Another window, or a command typed at a prompt.
  d.movedElsewhere = (key, value) => {
    running[key] = value;
    if (d.writable) file[key] = value;
    announce();
  };
  return d;
}

const rows = (app, id, cls) => Array.from(app.el(id).children).filter((row) => row.className === cls);

// What the three windows show, each opened if it is not.
function shown(app) {
  if (!app.settings.isOpen) app.el('settings-btn').click();
  if (!app.delegate.isOpen) app.el('auto-delegate-btn').click();
  if (!app.permissionSettings.isOpen) app.el('permission-status-btn').click();
  const repeatOn = app.el('repeat-limit-checkbox').checked;
  return {
    keepGoing: app.el('keep-going-checkbox').checked,
    keepGoingHeld: app.el('keep-going-checkbox').disabled,
    repeat: repeatOn ? Number(app.el('repeat-limit-input').value) : 0,
    repeatHeld: app.el('repeat-limit-checkbox').disabled,
    repeatNumberHeld: app.el('repeat-limit-input').disabled,
    delegation: app.el('delegate-enabled-checkbox').checked,
    agent: app.el('delegate-agent-select').value,
    patterns: rows(app, 'delegate-match-list', 'match-row').map((row) => row.children[0].textContent),
    delegationPill: app.el('auto-delegate-btn').textContent,
    rules: rows(app, 'permission-rules-list', 'rule-row').map((row) => row.children[0].textContent),
    rulesPill: app.el('permission-status-btn').textContent,
  };
}

// The rules the daemon is running and its file does not hold, and the
// other way round, in the words the warning names them with.
function rulesNotSaved(d) {
  const named = (set) => {
    const out = new Set();
    for (const [tool, list] of Object.entries(set)) {
      for (const r of list) out.add(`${tool}: "${r.match}"${r.literal ? ' (exact)' : ''} -> ${r.decision}`);
    }
    return out;
  };
  const running = named(d.running.permission_rules);
  const file = named(d.file.permission_rules);
  return [
    ...[...running].filter((r) => !file.has(r)).map((r) => `added ${r}`),
    ...[...file].filter((r) => !running.has(r)).map((r) => `removed ${r}`),
  ].sort();
}

// One step of a run. Each says what it did, for the trace of a failure.
async function step(rng, app, d) {
  const r = rng();
  if (r < 0.10) {
    d.writable = rng() < 0.5;
    return `file ${d.writable ? 'can' : 'cannot'} be written`;
  }
  if (r < 0.32 && d.answers.length > 0) {
    // Any answer that is out, not the oldest: they may cross.
    const [answer] = d.answers.splice(Math.floor(rng() * d.answers.length), 1);
    answer.go();
    await app.settle(8);
    return `answered: ${answer.label}`;
  }
  if (r < 0.46 && d.announcements.length > 0) {
    const ev = d.announcements.shift();
    app.applyEvent(ev);
    await app.settle(8);
    return `announced: ${JSON.stringify(ev.data)}`;
  }
  if (r < 0.53) {
    const box = app.el('keep-going-checkbox');
    if (box.disabled) return 'keep going is held';
    box.checked = !box.checked;
    box.fire('change');
    await app.settle(5);
    return `keep going ${box.checked}`;
  }
  if (r < 0.64) {
    const box = app.el('repeat-limit-checkbox');
    const number = app.el('repeat-limit-input');
    if (rng() < 0.5) {
      if (box.disabled) return 'the repeat guard is held';
      box.checked = !box.checked;
      if (box.checked) number.value = String(pick(rng, [3, 6, 12, 99]));
      box.fire('change');
      await app.settle(5);
      return `repeat guard ${box.checked ? number.value : 'off'}`;
    }
    if (number.disabled) return 'the repeat number is held';
    number.value = String(pick(rng, [4, 10, 20, 99]));
    number.fire('change');
    await app.settle(5);
    return `repeat number ${number.value}`;
  }
  if (r < 0.80) {
    const which = rng();
    if (which < 0.3) {
      const box = app.el('delegate-enabled-checkbox');
      box.checked = !box.checked;
      box.fire('change');
      await app.settle(5);
      return `delegation ${box.checked}`;
    }
    if (which < 0.6) {
      const select = app.el('delegate-agent-select');
      select.value = pick(rng, ['', 'plan', 'general-purpose', 'ghost']);
      select.fire('change');
      await app.settle(5);
      return `agent ${JSON.stringify(select.value)}`;
    }
    const listed = rows(app, 'delegate-match-list', 'match-row');
    if (listed.length > 0 && rng() < 0.45) {
      const row = pick(rng, listed);
      row.children[1].click();
      await app.settle(5);
      return `pattern removed: ${row.children[0].textContent}`;
    }
    const pattern = pick(rng, ['what is *', 'explain *', 'where is *']);
    app.el('delegate-match-input').value = pattern;
    app.el('delegate-match-add').click();
    await app.settle(5);
    return `pattern added: ${pattern}`;
  }
  if (r < 0.96) {
    const listed = rows(app, 'permission-rules-list', 'rule-row');
    if (listed.length > 0 && rng() < 0.45) {
      const row = pick(rng, listed);
      row.children[1].click();
      await app.settle(5);
      return `rule removed: ${row.children[0].textContent}`;
    }
    const match = pick(rng, ['echo *', 'npm *', 'ls *']);
    const decision = pick(rng, ['allow', 'deny']);
    app.el('rule-tool-input').value = 'bash';
    app.el('rule-match-input').value = match;
    app.el('rule-decision-select').value = decision;
    app.el('rule-add-btn').click();
    await app.settle(5);
    return `rule added: ${match} ${decision}`;
  }
  // Moved elsewhere, and only with nothing of this page's out: see the
  // note at the top.
  if (d.answers.length > 0) return 'nothing';
  const key = pick(rng, ['keep_going', 'repeat_limit', 'auto_delegate']);
  const value = key === 'repeat_limit' ? pick(rng, [0, 5, 15]) : rng() < 0.5;
  d.movedElsewhere(key, value);
  return `moved elsewhere: ${key} ${value}`;
}

const RUNS = 60;
const STEPS = 30;

test(`a page open through ${RUNS} runs of changes shows what a fresh page shows, and warns about the rules that were not saved`, async () => {
  for (let run = 0; run < RUNS; run++) {
    const seed = 4100 + run;
    const rng = seeded(seed);
    // Half the runs start with an exact rule, which "always allow" at a
    // prompt writes and this window never does.
    const d = fakeDaemon(run % 2
      ? { bash: [{ match: 'echo *', decision: 'allow', literal: true }] }
      : { bash: [{ match: 'ls *', decision: 'allow' }] });
    const app = await load({ globals: { lcGUI: true }, routes: d.routes });
    shown(app);
    await app.settle(10);

    const trace = [];
    for (let i = 0; i < STEPS; i++) trace.push(await step(rng, app, d));
    // Everything still out is let go, oldest first. An answer can send
    // the change that was waiting behind it, so this goes round until
    // nothing is left.
    while (d.answers.length > 0 || d.announcements.length > 0) {
      if (d.answers.length > 0) d.answers.shift().go();
      else app.applyEvent(d.announcements.shift());
      await app.settle(8);
    }
    await app.settle(15);

    const where = `seed ${seed}:\n  ${trace.join('\n  ')}\n`;
    const open = shown(app);
    const fresh = await load({ globals: { lcGUI: true }, routes: { 'GET /api/settings': d.settings() } });
    assert.deepEqual(open, shown(fresh), `the page that was open shows something a fresh page does not. ${where}`);

    // The page's own reading of the daemon, which is what it draws from.
    assert.deepEqual(open.patterns, d.running.auto_delegate_match, `the patterns shown are not the daemon's. ${where}`);

    const warning = app.el('permission-rules-warn');
    const expected = rulesNotSaved(d);
    const named = warning.hidden ? [] : warning.textContent.split('Not saved: ')[1].replace(/\.$/, '').split('; ').sort();
    assert.deepEqual(named, expected, `the rules named as not saved are not the ones the daemon and its file disagree about. ${where}`);
  }
});
