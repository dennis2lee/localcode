'use strict';

// The daemon-wide settings are numbered (IMPROVEMENTS item 65). A page
// hears of a change twice, by the answer to its request and by
// settings.changed, and the two keep no order between them. Each
// announcement carries the next number, the answer to the change that
// caused it carries the same one, and the page draws nothing numbered
// lower than what it has drawn.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const UNSAVED = /Applied, but not saved to config\.json/;
const NOT_WRITTEN = 'applied for this run, but failed to persist to config.json: read config /home/u/.localcode/config.json: is a directory';

async function open(app) {
  app.el('settings-btn').click();
  await app.settle();
}

function announce(app, data) {
  const stream = app.streams.find((s) => !s.closed && /\/events(\?|$)/.test(s.url));
  assert.ok(stream, 'the page has no conversation stream open');
  stream.emit({ type: 'settings.changed', data });
}

// heldDaemon numbers each change as the daemon does and holds its answer
// until the test lets it go. Each change is announced by the test, which
// decides when the announcement lands.
function heldDaemon(key, field) {
  const d = {
    seq: 0,
    held: [],
    writable: true,
    route(body) {
      d.seq += 1;
      const answer = { [key]: body[field], seq: d.seq, applied: true, persisted: d.writable };
      if (!d.writable) answer.error = NOT_WRITTEN;
      return new Promise((resolve) => d.held.push({ seq: d.seq, value: body[field], answer: () => resolve(answer) }));
    },
  };
  return d;
}

const SWITCHES = [
  { title: 'keep going', path: '/api/settings/keep-going', key: 'keep_going', box: 'keep-going-checkbox', warn: 'keep-going-warn', first: false, second: true },
  { title: 'Smart Agent', path: '/api/settings/smart-agent', key: 'smart_agent', box: 'smart-agent-checkbox', warn: 'smart-agent-warn', first: true, second: false },
  { title: 'orchestration', path: '/api/settings/orchestrate', key: 'orchestrate', box: 'orchestrate-checkbox', warn: 'orchestrate-warn', first: true, second: false },
];

async function press(app, c, value) {
  const box = app.el(c.box);
  box.checked = value;
  box.fire('change');
  await app.settle();
}

for (const c of SWITCHES) {
  test(`${c.title}: this page's own announcement, late, does not take the warning its next change earned`, async () => {
    const daemon = heldDaemon(c.key, 'enabled');
    const app = await load({ globals: { lcGUI: true }, routes: { [`POST ${c.path}`]: (body) => daemon.route(body) } });
    await open(app);
    const warn = app.el(c.warn);

    // The first change is saved and answered.
    await press(app, c, c.first);
    daemon.held[0].answer();
    await app.settle();
    // The second is not saved, and is answered before the first one's
    // announcement arrives.
    daemon.writable = false;
    await press(app, c, c.second);
    daemon.held[1].answer();
    await app.settle();
    assert.match(warn.textContent, UNSAVED);

    announce(app, { [c.key]: c.first, seq: 1 });
    await app.settle();
    assert.equal(app.el(c.box).checked, c.second, 'the announcement of the first change put its value back');
    assert.match(warn.textContent, UNSAVED, 'the late announcement read as a move made elsewhere and took the warning');

    // Its own announcement changes nothing either.
    announce(app, { [c.key]: c.second, seq: 2 });
    await app.settle();
    assert.equal(app.el(c.box).checked, c.second);
    assert.match(warn.textContent, UNSAVED);
  });

  test(`${c.title}: an answer that arrives after a later change made elsewhere is not drawn over it`, async () => {
    // Not saved, so its warning would be wrong too: the switch no longer
    // has the value this page could not save.
    const daemon = heldDaemon(c.key, 'enabled');
    daemon.writable = false;
    const app = await load({ globals: { lcGUI: true }, routes: { [`POST ${c.path}`]: (body) => daemon.route(body) } });
    await open(app);

    await press(app, c, c.first);
    // Another window moves it back, and its announcement lands first.
    announce(app, { [c.key]: !c.first, seq: 2 });
    await app.settle();
    daemon.held[0].answer();
    await app.settle();
    assert.equal(app.el(c.box).checked, !c.first, 'the older answer was drawn over the later change');
    assert.equal(app.el(c.warn).textContent, '');
  });

  test(`${c.title}: a late answer keeps its warning while the switch still has its value`, async () => {
    const daemon = heldDaemon(c.key, 'enabled');
    daemon.writable = false;
    const app = await load({ globals: { lcGUI: true }, routes: { [`POST ${c.path}`]: (body) => daemon.route(body) } });
    await open(app);

    await press(app, c, c.first);
    // A later change elsewhere, to another setting: this one keeps the
    // value this page gave it, which config.json still does not have.
    announce(app, { [c.key]: c.first, repeat_limit: 4, seq: 2 });
    await app.settle();
    daemon.held[0].answer();
    await app.settle();
    assert.equal(app.el(c.box).checked, c.first);
    assert.match(app.el(c.warn).textContent, UNSAVED, 'the switch runs a value that was not saved, and nothing says so');
    assert.equal(app.el('repeat-limit-checkbox').checked, true, 'the later announcement was not drawn');
  });
}

// The delegation panel: the other window the item reproduced it in.
test('delegation: this window\'s own announcement, late, does not take the warning its next change earned', async () => {
  let seq = 0;
  let writable = true;
  const held = [];
  const app = await load({
    routes: {
      'POST /api/settings/auto-delegate': (body) => {
        seq += 1;
        const answer = { auto_delegate: body.enabled, auto_delegate_agent: '', auto_delegate_match: [], seq, applied: true, persisted: writable };
        if (!writable) answer.error = NOT_WRITTEN;
        return new Promise((resolve) => held.push(() => resolve(answer)));
      },
    },
  });
  app.el('auto-delegate-btn').click();
  await app.settle();
  const box = app.el('delegate-enabled-checkbox');
  const warn = app.el('delegate-warn');
  const turn = async (on) => { box.checked = on; box.fire('change'); await app.settle(); };

  await turn(true);
  held[0]();
  await app.settle();
  writable = false;
  await turn(false);
  held[1]();
  await app.settle();
  assert.match(warn.textContent, UNSAVED);

  announce(app, { auto_delegate: true, seq: 1 });
  await app.settle();
  assert.equal(box.checked, false, 'the announcement of the first change put its value back');
  assert.match(warn.textContent, UNSAVED, 'the late announcement took the warning');
});

test('an announcement numbered lower than what the page has drawn is not drawn at all', async () => {
  const app = await load({ globals: { lcGUI: true } });
  await open(app);
  announce(app, { keep_going: false, orchestrate: true, seq: 5 });
  await app.settle();
  announce(app, { keep_going: true, orchestrate: false, seq: 4 });
  await app.settle();
  assert.equal(app.el('keep-going-checkbox').checked, false);
  assert.equal(app.el('orchestrate-checkbox').checked, true);
  announce(app, { keep_going: true, orchestrate: false, seq: 6 });
  await app.settle();
  assert.equal(app.el('keep-going-checkbox').checked, true);
  assert.equal(app.el('orchestrate-checkbox').checked, false);
});

// A daemon that replaced the one this page talked to (a restart, an
// update's handover) counts from the start again, under a new epoch. Its
// changes are drawn though their numbers are below what the page drew,
// and from then on its numbers are the ones compared. Found by review: a
// count taken from the clock, compared across daemons, dropped every
// change of a daemon started with its clock set back.
test('a daemon that replaced another is drawn, though it counts from the start again', async () => {
  const app = await load({ globals: { lcGUI: true } });
  await open(app);
  announce(app, { keep_going: false, seq: 100, epoch: 'first' });
  await app.settle();
  announce(app, { keep_going: true, seq: 1, epoch: 'second' });
  await app.settle();
  assert.equal(app.el('keep-going-checkbox').checked, true, "the new daemon's change was dropped as older");
  // Its own count orders it now.
  announce(app, { keep_going: false, seq: 3, epoch: 'second' });
  await app.settle();
  announce(app, { keep_going: true, seq: 2, epoch: 'second' });
  await app.settle();
  assert.equal(app.el('keep-going-checkbox').checked, false, 'an older change of the new daemon was drawn');
});

// The delegation target is in the numbered snapshot. An answer to this
// page's own target change that is older than an announcement is not
// drawn, so the announcement's target has to be. Found by review.
test('delegation: a target changed elsewhere is drawn, and loses the warning this window put under it', async () => {
  const app = await load({
    routes: {
      'POST /api/settings/auto-delegate': (body) => ({
        auto_delegate: false, auto_delegate_agent: body.agent || '', auto_delegate_match: [], seq: 1, applied: true, persisted: false, error: NOT_WRITTEN,
      }),
    },
  });
  app.el('auto-delegate-btn').click();
  await app.settle();
  const warn = app.el('delegate-warn');
  const select = app.el('delegate-agent-select');
  select.value = 'plan';
  select.fire('change');
  await app.settle();
  assert.ok(warn.textContent.includes('The agent and patterns'), 'the unsaved target is not warned about');

  announce(app, { auto_delegate: false, auto_delegate_agent: 'explore', auto_delegate_match: ['what is *'], seq: 2 });
  await app.settle();
  assert.equal(select.value, 'explore', 'the target another window chose is not drawn');
  assert.equal(app.state.autoDelegateMatch.join(), 'what is *');
  assert.equal(warn.textContent.includes('The agent and patterns'), false, 'a warning stayed under a target somebody else has since moved');
});

// During an update's handover the page keeps its stream to the daemon
// being replaced until that daemon is done, so a message of the old
// daemon can arrive after the page has taken the new one's epoch. An
// epoch the page has left is not drawn again. Found by review.
test('a message from the daemon that was replaced, arriving after the new one, is not drawn', async () => {
  let seq = 0;
  let held;
  const app = await load({
    globals: { lcGUI: true },
    routes: {
      'POST /api/settings/keep-going': (body) => new Promise((resolve) => {
        seq += 1;
        held = () => resolve({ keep_going: body.enabled, seq, epoch: 'old', applied: true, persisted: false, error: NOT_WRITTEN });
      }),
    },
  });
  await open(app);
  const box = app.el('keep-going-checkbox');
  const warn = app.el('keep-going-warn');
  announce(app, { keep_going: true, seq: 7, epoch: 'old' });
  await app.settle();
  // A change made while the old daemon still answers on this connection.
  box.checked = false;
  box.fire('change');
  await app.settle();
  // The new daemon's first announcement lands first.
  announce(app, { keep_going: true, seq: 1, epoch: 'new' });
  await app.settle();
  assert.equal(box.checked, true);

  // Then the old daemon's: its answer, and an announcement of a command
  // still running in it.
  held();
  await app.settle();
  assert.equal(box.checked, true, "the replaced daemon's answer was drawn over the new daemon's state");
  assert.equal(warn.textContent, '', 'the answer about a value the new daemon does not have left a warning');
  announce(app, { keep_going: false, seq: 9, epoch: 'old' });
  await app.settle();
  assert.equal(box.checked, true, "the replaced daemon's announcement was drawn over the new daemon's state");
  // The new daemon's count goes on ordering what it sends.
  announce(app, { keep_going: false, seq: 2, epoch: 'new' });
  await app.settle();
  assert.equal(box.checked, false);
});
