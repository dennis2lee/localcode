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
      'localcode.collapsedGroups': JSON.stringify({ work: true }),
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
      'localcode.collapsedGroups': JSON.stringify({ work: true }),
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
      'localcode.collapsedGroups': JSON.stringify({ work: true, 'a group that was deleted': true }),
    },
  });

  const list = app.el('session-list');
  list.querySelectorAll('.session-group-header')[1].fire('click');
  await app.settle();

  const kept = JSON.parse(app.storage.get('localcode.collapsedGroups'));
  assert.deepEqual(kept, { work: true, personal: true }, 'the deleted group is not carried along');
});
