'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const SESSIONS = [
  { id: 's1', title: 'ungrouped session', agent: 'general-purpose', workspace: '/w1' },
  { id: 's2', title: 'work task A', agent: 'general-purpose', workspace: '/w2', group: 'work' },
  { id: 's3', title: 'work task B', agent: 'general-purpose', workspace: '/w3', group: 'work' },
  { id: 's4', title: 'personal notes', agent: 'general-purpose', workspace: '/w4', group: 'personal' },
];

function transfer() {
  const data = new Map();
  return {
    effectAllowed: '',
    dropEffect: '',
    setData: (k, v) => data.set(k, v),
    getData: (k) => data.get(k) || '',
  };
}

// The guard on the whole feature. Someone who has made no groups must see
// the panel they saw before groups existed: the same rows, in the same
// order, with the same gesture still working. A feature that changed the
// thing it was added beside would show up here and nowhere else — the
// tests below all make groups first, so none of them would notice.
test('regression parity: with no groups the panel is exactly what it was', async () => {
  const THREE = [
    { id: 's1', title: 'one', agent: 'general-purpose', workspace: '/w' },
    { id: 's2', title: 'two', agent: 'general-purpose', workspace: '/w' },
    { id: 's3', title: 'three', agent: 'general-purpose', workspace: '/w' },
  ];

  const app = await load({
    routes: {
      'GET /api/sessions': THREE,
      'GET /api/sessions/groups': { names: [] },
      'POST /api/sessions/order': { status: 204 },
    },
  });

  const list = app.el('session-list');
  const children = list.children || [];
  assert.equal(children.length, 3, 'three sessions, three rows and nothing else');
  for (const child of children) {
    assert.ok(child.classList.contains('session-item'), 'every child is a session row');
  }
  assert.deepEqual(
    [...children].map(c => c.title.split('\n')[0]),
    ['s1', 's2', 's3'],
    'in the order the listing gave them',
  );

  // And the gesture still does what it did: drag the third row onto the
  // first and the order that reaches the daemon is the new one, with no
  // group assignment asked for along the way.
  children[2].fire('dragstart', { dataTransfer: transfer() });
  children[0].fire('dragover', { dataTransfer: transfer() });
  children[0].fire('drop', { dataTransfer: transfer() });
  await app.settle();

  const orderCalls = app.callsTo('POST', '/api/sessions/order');
  assert.equal(orderCalls.length, 1, 'dragging still saves an order');
  assert.deepEqual(orderCalls[0].body, { ids: ['s3', 's1', 's2'] });
  assert.equal(
    app.callsTo('POST', '/api/sessions/s3/group').length,
    0,
    'and does not touch a group, because there are none',
  );
});

test('grouped rendering: ungrouped sessions render at top with no header, groups render below with headers', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': SESSIONS,
      'GET /api/sessions/groups': { names: ['work', 'personal'] },
    },
  });

  const list = app.el('session-list');
  const headers = list.querySelectorAll('.session-group-header');
  assert.equal(headers.length, 2, 'should render two group headers');

  const workName = headers[0].querySelector('.group-name');
  const workCount = headers[0].querySelector('.group-count');
  assert.equal(workName.textContent, 'work');
  assert.ok(workCount.textContent.includes('2'), 'work count should show 2');

  const personalName = headers[1].querySelector('.group-name');
  const personalCount = headers[1].querySelector('.group-count');
  assert.equal(personalName.textContent, 'personal');
  assert.ok(personalCount.textContent.includes('1'), 'personal count should show 1');

  // Verify child order in session-list:
  // 0: s1 (ungrouped)
  // 1: work header
  // 2: s2 (work)
  // 3: s3 (work)
  // 4: personal header
  // 5: s4 (personal)
  const children = list.children;
  assert.equal(children[0].title.split('\n')[0], 's1', 'ungrouped session should be first');
  assert.ok(children[1].classList.contains('session-group-header'), 'child 1 is work group header');
  assert.equal(children[2].title.split('\n')[0], 's2', 'child 2 is s2');
  assert.equal(children[3].title.split('\n')[0], 's3', 'child 3 is s3');
  assert.ok(children[4].classList.contains('session-group-header'), 'child 4 is personal group header');
  assert.equal(children[5].title.split('\n')[0], 's4', 'child 5 is s4');
});

test('dragging a session onto a group header joins that group and moves into position', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': SESSIONS,
      'GET /api/sessions/groups': { names: ['work', 'personal'] },
      'POST /api/sessions/s1/group': { status: 200 },
      'POST /api/sessions/order': { status: 204 },
    },
  });

  const list = app.el('session-list');
  const s1Card = list.children[0];
  const workHeader = list.children[1];

  // Drag s1 onto work group header
  s1Card.fire('dragstart', { dataTransfer: transfer() });
  workHeader.fire('dragover', { dataTransfer: transfer() });
  workHeader.fire('drop', { dataTransfer: transfer() });
  await app.settle();

  const groupCalls = app.callsTo('POST', '/api/sessions/s1/group');
  assert.equal(groupCalls.length, 1, 'should call setSessionGroup');
  assert.deepEqual(groupCalls[0].body, { group: 'work' });

  const orderCalls = app.callsTo('POST', '/api/sessions/order');
  assert.equal(orderCalls.length, 1, 'should reorder sessions');
});

test('dragging a session to ungrouped area leaves the group', async () => {
  const ALL_GROUPED = [
    { id: 's2', title: 'work task A', agent: 'general-purpose', workspace: '/w2', group: 'work' },
    { id: 's3', title: 'work task B', agent: 'general-purpose', workspace: '/w3', group: 'work' },
  ];

  const app = await load({
    routes: {
      'GET /api/sessions': ALL_GROUPED,
      'GET /api/sessions/groups': { names: ['work'] },
      'POST /api/sessions/s2/group': { status: 200 },
      'POST /api/sessions/order': { status: 204 },
    },
  });

  const list = app.el('session-list');
  const ungroupedDrop = list.querySelector('.session-group-ungrouped-drop');
  assert.ok(ungroupedDrop, 'ungrouped drop area exists when all sessions are grouped');

  const s2Card = list.querySelectorAll('.session-item')[0];
  s2Card.fire('dragstart', { dataTransfer: transfer() });
  ungroupedDrop.fire('dragover', { dataTransfer: transfer() });
  ungroupedDrop.fire('drop', { dataTransfer: transfer() });
  await app.settle();

  const groupCalls = app.callsTo('POST', '/api/sessions/s2/group');
  assert.equal(groupCalls.length, 1, 'should call setSessionGroup with empty group');
  assert.deepEqual(groupCalls[0].body, { group: '' });
});

test('dropping a session among rows of another group joins that group', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': SESSIONS,
      'GET /api/sessions/groups': { names: ['work', 'personal'] },
      'POST /api/sessions/s1/group': { status: 200 },
      'POST /api/sessions/order': { status: 204 },
    },
  });

  const list = app.el('session-list');
  const items = list.querySelectorAll('.session-item');
  const s1Card = items[0]; // ungrouped
  const s4Card = items[3]; // in personal

  s1Card.fire('dragstart', { dataTransfer: transfer() });
  s4Card.fire('dragover', { dataTransfer: transfer() });
  s4Card.fire('drop', { dataTransfer: transfer() });
  await app.settle();

  const groupCalls = app.callsTo('POST', '/api/sessions/s1/group');
  assert.equal(groupCalls.length, 1, 'should call setSessionGroup to personal');
  assert.deepEqual(groupCalls[0].body, { group: 'personal' });

  const orderCalls = app.callsTo('POST', '/api/sessions/order');
  assert.equal(orderCalls.length, 1);
});

test('filtering hides non-matching sessions and hides group headers with no visible sessions', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': SESSIONS,
      'GET /api/sessions/groups': { names: ['work', 'personal'] },
    },
  });

  const filterInput = app.el('session-filter');
  filterInput.value = 'work task';
  filterInput.fire('input');
  await app.settle();

  const list = app.el('session-list');
  const headers = list.querySelectorAll('.session-group-header');
  assert.equal(headers.length, 1, 'only work group header should be visible');
  assert.equal(headers[0].querySelector('.group-name').textContent, 'work');

  // Dragging should be disabled on cards while filtering
  const items = list.querySelectorAll('.session-item');
  for (const item of items) {
    assert.equal(item.draggable, false, 'cards should not be draggable while filtering');
  }
});

test('collapsed group state is remembered and toggled', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': SESSIONS,
      'GET /api/sessions/groups': { names: ['work', 'personal'] },
    },
    localStorage: {
      'localcode.collapsedGroups': JSON.stringify(['work']),
    },
  });

  const list = app.el('session-list');
  const workHeader = list.querySelectorAll('.session-group-header')[0];
  const workToggle = workHeader.querySelector('.group-toggle');
  assert.equal(workToggle.textContent, '▸', 'collapsed group should show collapsed indicator');

  // s2 and s3 in work should not be rendered when work is collapsed
  const itemsBefore = list.querySelectorAll('.session-item');
  assert.equal(itemsBefore.length, 2, 'only s1 and s4 should be rendered when work is collapsed');
  assert.equal(itemsBefore[0].title.split('\n')[0], 's1');
  assert.equal(itemsBefore[1].title.split('\n')[0], 's4');

  // Click toggle to expand work group
  workHeader.fire('click');
  await app.settle();

  const newToggle = list.querySelectorAll('.session-group-header')[0].querySelector('.group-toggle');
  assert.equal(newToggle.textContent, '▾', 'expanded group should show expanded indicator');
  const itemsAfter = list.querySelectorAll('.session-item');
  assert.equal(itemsAfter.length, 4, 'all 4 sessions rendered after expansion');
});

test('collapsed group state handles storage failure safely', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': SESSIONS,
      'GET /api/sessions/groups': { names: ['work', 'personal'] },
    },
    localStorage: {
      'localcode.collapsedGroups': 'not json',
    },
  });

  const list = app.el('session-list');
  const workHeader = list.querySelectorAll('.session-group-header')[0];
  assert.ok(workHeader, 'header should render despite corrupt localStorage');

  // Clicking toggle when localStorage holds unparseable data shouldn't throw
  assert.doesNotThrow(() => {
    workHeader.fire('click');
  });
});

// Folding a group shut and then searching for something inside it. The
// filter wins: a shut group that holds the only match would be the panel
// hiding what it had just been asked to find.
test('a filter opens a group that was folded shut', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': SESSIONS,
      'GET /api/sessions/groups': { names: ['work', 'personal'] },
    },
    localStorage: {
      'localcode.collapsedGroups': JSON.stringify(['work']),
    },
  });

  const list = app.el('session-list');
  assert.equal(list.querySelectorAll('.session-item').length, 2, 'work is shut, so only s1 and s4 show');

  app.el('session-filter').value = 'work task A';
  app.el('session-filter').fire('input');
  await app.settle();

  const found = list.querySelectorAll('.session-item');
  assert.equal(found.length, 1, 'the row the filter matched is drawn, though its group is folded');
  assert.equal(found[0].title.split('\n')[0], 's2');
});

// The collapse map is the one piece of group state kept in the browser,
// and nothing on the server ever prunes it. Names that no longer exist
// have to fall out here, or a group folded shut and later deleted leaves
// its name behind for good — and folds itself again if the name returns.
test('folding forgets groups that no longer exist', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': SESSIONS,
      'GET /api/sessions/groups': { names: ['work', 'personal'] },
    },
    localStorage: {
      'localcode.collapsedGroups': JSON.stringify(['work', 'a group that was deleted']),
    },
  });

  const list = app.el('session-list');
  list.querySelectorAll('.session-group-header')[1].fire('click');
  await app.settle();

  const kept = JSON.parse(app.storage.get('localcode.collapsedGroups'));
  assert.deepEqual(kept.sort(), ['personal', 'work'], 'the deleted group is not carried along');
});

// Deleting a group must take its fold with it. Nothing on the server
// knows about folds, so if the panel does not drop the name here it stays
// in this browser for good — and folds a future group of the same name
// shut on the day it is made, for a reason nobody can see.
test('deleting a group forgets that it was folded', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': SESSIONS,
      'GET /api/sessions/groups': { names: ['work', 'personal'] },
      'POST /api/sessions/groups': { names: ['personal'] },
    },
    localStorage: { 'localcode.collapsedGroups': JSON.stringify(['work', 'personal']) },
    confirm: true,
  });

  // A daemon that took the delete answers the next read with the new list.
  app.routes['GET /api/sessions/groups'] = { names: ['personal'] };
  await app.internals.promptDeleteGroup('work');
  await app.settle();

  assert.deepEqual(
    JSON.parse(app.storage.get('localcode.collapsedGroups')),
    ['personal'],
    'the deleted group is gone from the fold list; the one still there stays',
  );
});

// Renaming carries the fold across. The fold belongs to the group, not to
// the name it had that morning.
test('renaming a group keeps it folded, under the new name', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': SESSIONS,
      'GET /api/sessions/groups': { names: ['work', 'personal'] },
      'POST /api/sessions/groups': { names: ['job', 'personal'] },
    },
    localStorage: { 'localcode.collapsedGroups': JSON.stringify(['work']) },
    prompt: 'job',
  });

  app.routes['GET /api/sessions/groups'] = { names: ['job', 'personal'] };
  await app.internals.promptRenameGroup('work');
  await app.settle();

  const kept = JSON.parse(app.storage.get('localcode.collapsedGroups'));
  assert.deepEqual(kept, ['job'], 'the fold moved to the new name and did not stay on the old one');

  const sent = app.callsTo('POST', '/api/sessions/groups')[0];
  assert.deepEqual(sent.body.rename, { from: 'work', to: 'job' }, 'and the rename was stated, not left to be guessed');
});

// A group named after something every object already has. The fold used to
// be looked up on a plain object, which answers `collapsed['constructor']`
// with a function it inherited — so the group drew folded shut on the day
// it was made and no number of clicks would open it, with its sessions
// unreachable from the panel.
test('a group named constructor behaves like any other group', async () => {
  for (const name of ['constructor', '__proto__', 'toString', 'valueOf', 'hasOwnProperty']) {
    const app = await load({
      routes: {
        'GET /api/sessions': [
          { id: 's1', title: 'ungrouped', agent: 'general-purpose', workspace: '/w' },
          { id: 's2', title: 'inside', agent: 'general-purpose', workspace: '/w', group: name },
        ],
        'GET /api/sessions/groups': { names: [name] },
      },
      localStorage: {},
    });

    const list = app.el('session-list');
    const header = list.querySelectorAll('.session-group-header')[0];
    assert.equal(header.querySelector('.group-toggle').textContent, '▾',
      `a group named ${name} that nobody folded should start open`);
    assert.equal(list.querySelectorAll('.session-item').length, 2,
      `both rows show for a group named ${name}`);

    header.fire('click');
    await app.settle();
    assert.equal(list.querySelectorAll('.session-item').length, 1,
      `clicking the header folds a group named ${name}`);

    list.querySelectorAll('.session-group-header')[0].fire('click');
    await app.settle();
    assert.equal(list.querySelectorAll('.session-item').length, 2,
      `and clicking again opens a group named ${name}`);
  }
});

// A folded group must not be a way to lose a session that needs you. The
// per-session light is the whole reason a turn left running somewhere else
// — or stopped waiting for a permission answer — is visible without going
// and looking; folding the group over it would take that away.
test('a folded group carries the light of what it is hiding', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': [
        { id: 's1', title: 'ungrouped', agent: 'general-purpose', workspace: '/w' },
        { id: 's2', title: 'asking', agent: 'general-purpose', workspace: '/w', group: 'work', asking: true },
      ],
      'GET /api/sessions/groups': { names: ['work'] },
    },
    localStorage: { 'localcode.collapsedGroups': JSON.stringify(['work']) },
  });

  const header = app.el('session-list').querySelectorAll('.session-group-header')[0];
  const led = header.querySelector('.session-led');
  assert.ok(led, 'the folded header shows a light for the session waiting inside it');
  assert.ok(led.className.includes('asking'), 'and it is the amber one, because somebody is waited on');
});

// A drop is two saves: the group, then the order. When the first lands and
// the second does not, the panel cannot be put back — putting it back
// would draw a state the daemon has already contradicted — and it cannot
// be left alone, because the half that was refused never happened. So it
// asks, and draws the answer.
test('a drop whose group saved but whose order failed re-reads from the daemon', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': [
        { id: 's1', title: 'one', agent: 'general-purpose', workspace: '/w' },
        { id: 's2', title: 'two', agent: 'general-purpose', workspace: '/w', group: 'g' },
      ],
      'GET /api/sessions/groups': { names: ['g'] },
      'POST /api/sessions/s1/group': { status: 200 },
      'POST /api/sessions/order': { status: 500 },
    },
  });

  const listedBefore = app.callsTo('GET', '/api/sessions').length;
  await app.internals.dropSessionOn('s1', 's2');
  await app.settle();

  assert.equal(app.callsTo('POST', '/api/sessions/s1/group').length, 1, 'the group move was sent');
  assert.ok(
    app.callsTo('GET', '/api/sessions').length > listedBefore,
    'and when the order save failed it went back to the daemon rather than guessing',
  );
});

// When the FIRST save is the one refused, nothing landed, so the panel
// goes back exactly as it was — and that rollback must really restore the
// group, not a snapshot the drop had already written through.
test('a drop whose group move was refused puts the card back', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': [
        { id: 's1', title: 'one', agent: 'general-purpose', workspace: '/w' },
        { id: 's2', title: 'two', agent: 'general-purpose', workspace: '/w', group: 'g' },
      ],
      'GET /api/sessions/groups': { names: ['g'] },
      'POST /api/sessions/s1/group': { status: 400 },
      'POST /api/sessions/order': { status: 204 },
    },
  });

  await app.internals.dropSessionOn('s1', 's2');
  await app.settle();

  const s1 = app.internals.app.sessions.find(s => s.id === 's1');
  assert.equal(s1.group || '', '', 'the refused group is not left on the card');
  const list = app.el('session-list');
  assert.ok(
    list.children[0].classList.contains('session-item'),
    'and s1 is drawn above the group header again, where it started',
  );
  assert.equal(app.callsTo('POST', '/api/sessions/order').length, 0, 'no order was saved for a move that did not happen');
});

// Once groups exist, the order app.sessions is in and the order the rows
// are drawn in stop being the same list. Everything that moves a card
// picks a side by comparing two positions, so those two lists have to be
// the same one or a card dragged downward lands above what it was dropped
// on.
test('a card dragged downward lands below the row it was dropped on', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': [
        { id: 'a', title: 'a', agent: 'general-purpose', workspace: '/w', group: 'one' },
        { id: 'b', title: 'b', agent: 'general-purpose', workspace: '/w', group: 'two' },
        { id: 'c', title: 'c', agent: 'general-purpose', workspace: '/w', group: 'one' },
      ],
      'GET /api/sessions/groups': { names: ['one', 'two'] },
      'POST /api/sessions/c/group': { status: 200 },
      'POST /api/sessions/order': { status: 204 },
    },
  });

  const list = app.el('session-list');
  const rows = () => [...list.querySelectorAll('.session-item')].map(e => e.title.split('\n')[0]);
  assert.deepEqual(rows(), ['a', 'c', 'b'], 'drawn: group one holds a and c, group two holds b');

  // c is drawn third, b last. Drag c down onto b.
  await app.internals.dropSessionOn('c', 'b');
  await app.settle();

  assert.deepEqual(rows(), ['a', 'b', 'c'], 'c joined group two and landed below b, which is where it was dropped');
  assert.deepEqual(
    app.callsTo('POST', '/api/sessions/order')[0].body,
    { ids: ['a', 'b', 'c'] },
    'and the order saved is the order drawn',
  );
});

// Dropping on a header means the top of that group: it is the only target
// a folded group offers, and the rows it would otherwise be placed among
// are not on screen.
test('a card dropped on a group header lands first in that group', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': [
        { id: 'x', title: 'x', agent: 'general-purpose', workspace: '/w' },
        { id: 'p', title: 'p', agent: 'general-purpose', workspace: '/w', group: 'g' },
        { id: 'q', title: 'q', agent: 'general-purpose', workspace: '/w', group: 'g' },
      ],
      'GET /api/sessions/groups': { names: ['g'] },
      'POST /api/sessions/x/group': { status: 200 },
      'POST /api/sessions/order': { status: 204 },
    },
  });

  await app.internals.dropSessionOnGroupHeader('x', 'g');
  await app.settle();

  const rows = [...app.el('session-list').querySelectorAll('.session-item')].map(e => e.title.split('\n')[0]);
  assert.deepEqual(rows, ['x', 'p', 'q'], 'x is first in the group, not second');
});

// A group deleted from the other window, or the desktop build. This panel
// only hears about it on the next read, and if the fold is not dropped
// then, a group later made with that name draws shut for a reason nobody
// can see.
test('a group deleted elsewhere loses its fold on the next read', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': SESSIONS,
      'GET /api/sessions/groups': { names: ['work', 'personal'] },
    },
    localStorage: { 'localcode.collapsedGroups': JSON.stringify(['work', 'personal']) },
  });

  // Somewhere else, "work" is deleted. Nothing in this window did it.
  app.routes['GET /api/sessions/groups'] = { names: ['personal'] };
  await app.internals.loadSessions();
  await app.settle();

  assert.deepEqual(
    JSON.parse(app.storage.get('localcode.collapsedGroups')),
    ['personal'],
    'the fold of the group that is gone went with it',
  );
});

// But a read that did not arrive is not news that every group is gone.
// Pruning against a list that failed to come back would throw away every
// fold in this browser on one bad request.
test('a failed read of the group list does not throw the folds away', async () => {
  // The daemon is not answering, so this window never learns what groups
  // there are. Pruning against the nothing it knows would empty the fold
  // list — and then the folds would be gone for good, even though every
  // group is still there.
  const app = await load({
    routes: {
      'GET /api/sessions': SESSIONS,
      'GET /api/sessions/groups': { status: 500 },
    },
    localStorage: { 'localcode.collapsedGroups': JSON.stringify(['work', 'personal']) },
  });

  assert.deepEqual(
    JSON.parse(app.storage.get('localcode.collapsedGroups')).sort(),
    ['personal', 'work'],
    'both folds are still there',
  );

  // And when the daemon comes back, they still do their job.
  app.routes['GET /api/sessions/groups'] = { names: ['work', 'personal'] };
  await app.internals.loadSessions();
  await app.settle();
  const headers = app.el('session-list').querySelectorAll('.session-group-header');
  assert.equal(headers.length, 2, 'both groups draw');
  for (const h of headers) {
    assert.equal(h.querySelector('.group-toggle').textContent, '▸', 'and both are still folded');
  }
});
