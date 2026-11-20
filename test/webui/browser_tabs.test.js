'use strict';

// The browser panel's tab strip against everything else that can answer
// at the same time: a location poll asked before a click, an older click,
// an address bar open for the tab shown before, a switch to another
// conversation. Whatever comes back late, the strip, the active tab and
// the address bar say what the newest choice made them. From a review of
// the tabs change; each of these failed before its fix.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const settingsWith = (extra) => ({
  'GET /api/settings': {
    auto_compact_enabled: true, show_tps: true, auto_delegate: false,
    auto_delegate_agent: '', auto_delegate_match: [], smart_agent: false,
    skip_permissions: false, permission_rules: {}, can_edit_permissions: true,
    ...extra,
  },
  'GET /api/sessions/*/browser': { has_tab: false, width: 1280, height: 800 },
});

const withTabs = (tabs, extra = {}) => ({
  ...settingsWith({ browser: true }),
  'GET /api/sessions/*/browser': () => {
    const active = tabs.find((t) => t.active);
    return {
      has_tab: tabs.length > 0,
      url: active ? active.url : undefined,
      title: active ? active.title : undefined,
      tabs: tabs.map((t) => ({ ...t })),
    };
  },
  ...extra,
});

// 1. Stale location poll racing "+" reverts the tab strip and activeTab.
test('a location poll asked before + does not erase the new tab', async () => {
  const tabs = [{ id: 't1', url: 'https://one.example/', title: 'One', active: true }];
  let releasePoll;
  let holdPoll = false;
  const app = await load({
    globals: { lcGUI: true },
    routes: withTabs(tabs, {
      'GET /api/sessions/*/browser': () => {
        if (holdPoll) {
          holdPoll = false;
          return new Promise((r) => { releasePoll = r; });
        }
        const active = tabs.find((t) => t.active);
        return {
          has_tab: tabs.length > 0,
          url: active ? active.url : undefined,
          title: active ? active.title : undefined,
          tabs: tabs.map((t) => ({ ...t })),
        };
      },
      'POST /api/sessions/*/browser/tabs': () => {
        tabs[0].active = false;
        tabs.push({ id: 't2', url: 'about:blank', title: 'about:blank', active: true });
        return { tabs: tabs.map((t) => ({ ...t })) };
      },
    }),
  });

  // Start a location poll that captures the old 1-tab state.
  holdPoll = true;
  app.internals.renderBrowserPanel();
  // Before that poll answers, user clicks "+" to open a second tab.
  app.el('browser-tab-new').click();
  await app.settle();
  assert.equal(app.el('browser-tab-list').querySelectorAll('.browser-tab').length, 2);

  // Now the older poll from before "+" resolves.
  releasePoll({
    has_tab: true,
    url: 'https://one.example/',
    title: 'One',
    tabs: [{ id: 't1', url: 'https://one.example/', title: 'One', active: true }],
  });
  await app.settle();

  const drawn = app.el('browser-tab-list').querySelectorAll('.browser-tab');
  assert.equal(drawn.length, 2, 'stale poll from before + erased the new tab from the strip');
  assert.ok(drawn[1].classList.contains('active'), 'stale poll moved active highlight back to t1');
});

// 2. Stale location poll racing tab activation reverts strip AND address bar.
test('a location poll asked before a tab click does not revert the active tab and address bar', async () => {
  const tabs = [
    { id: 't1', url: 'https://one.example/', title: 'One', active: true },
    { id: 't2', url: 'https://two.example/', title: 'Two', active: false },
  ];
  let releasePoll;
  let holdPoll = false;
  const app = await load({
    globals: { lcGUI: true },
    routes: withTabs(tabs, {
      'GET /api/sessions/*/browser': () => {
        if (holdPoll) {
          holdPoll = false;
          return new Promise((r) => { releasePoll = r; });
        }
        const active = tabs.find((t) => t.active);
        return {
          has_tab: true,
          url: active.url,
          title: active.title,
          tabs: tabs.map((t) => ({ ...t })),
        };
      },
      'POST /api/sessions/*/browser/tabs/t2/activate': () => {
        tabs[0].active = false;
        tabs[1].active = true;
        return { tabs: tabs.map((t) => ({ ...t })) };
      },
    }),
  });

  holdPoll = true;
  app.internals.renderBrowserPanel();
  // Click t2 to activate it.
  app.el('browser-tab-list').querySelectorAll('.browser-tab')[1].querySelector('.browser-tab-open').click();
  await app.settle();
  assert.equal(app.el('browser-url').value, 'https://two.example/');

  // Older poll from before the click resolves.
  releasePoll({
    has_tab: true,
    url: 'https://one.example/',
    title: 'One',
    tabs: [
      { id: 't1', url: 'https://one.example/', title: 'One', active: true },
      { id: 't2', url: 'https://two.example/', title: 'Two', active: false },
    ],
  });
  await app.settle();

  assert.equal(app.el('browser-url').value, 'https://two.example/', 'stale poll reverted the address bar to the previous tab');
  assert.ok(app.el('browser-tab-list').querySelectorAll('.browser-tab')[1].classList.contains('active'),
    'stale poll reverted the active tab highlight');
});

// 3. Stale location poll racing tab close resurrects closed tab and its URL.
test('a location poll asked before x does not resurrect the closed tab', async () => {
  const tabs = [
    { id: 't1', url: 'https://one.example/', title: 'One', active: true },
    { id: 't2', url: 'https://two.example/', title: 'Two', active: false },
  ];
  let releasePoll;
  let holdPoll = false;
  const app = await load({
    globals: { lcGUI: true },
    routes: withTabs(tabs, {
      'GET /api/sessions/*/browser': () => {
        if (holdPoll) {
          holdPoll = false;
          return new Promise((r) => { releasePoll = r; });
        }
        const active = tabs.find((t) => t.active);
        return {
          has_tab: tabs.length > 0,
          url: active ? active.url : undefined,
          title: active ? active.title : undefined,
          tabs: tabs.map((t) => ({ ...t })),
        };
      },
      'DELETE /api/sessions/*/browser/tabs/t1': () => {
        tabs.splice(0, 1);
        tabs[0].active = true;
        return { tabs: tabs.map((t) => ({ ...t })) };
      },
    }),
  });

  holdPoll = true;
  app.internals.renderBrowserPanel();
  app.el('browser-tab-list').querySelectorAll('.browser-tab')[0].querySelector('.browser-tab-close').click();
  await app.settle();
  assert.equal(app.el('browser-tab-list').querySelectorAll('.browser-tab').length, 1);

  releasePoll({
    has_tab: true,
    url: 'https://one.example/',
    title: 'One',
    tabs: [
      { id: 't1', url: 'https://one.example/', title: 'One', active: true },
      { id: 't2', url: 'https://two.example/', title: 'Two', active: false },
    ],
  });
  await app.settle();

  assert.equal(app.el('browser-tab-list').querySelectorAll('.browser-tab').length, 1,
    'stale poll resurrected the closed tab in the strip');
  assert.equal(app.el('browser-url').value, 'https://two.example/',
    'stale poll put the closed tab URL back in the address bar');
});

// 4. "+" clicked while address bar is focused (WKWebView behavior) and a poll
// arrives after the server created the new tab but before POST /tabs resolves.
test('+ empties the address bar even when a poll sees the new tab first while the bar is focused', async () => {
  const tabs = [{ id: 't1', url: 'https://one.example/', title: 'One', active: true }];
  let releaseNewTab;
  const app = await load({
    globals: { lcGUI: true },
    routes: withTabs(tabs, {
      'POST /api/sessions/*/browser/tabs': () => {
        tabs[0].active = false;
        tabs.push({ id: 't2', url: 'about:blank', title: 'about:blank', active: true });
        return new Promise((r) => { releaseNewTab = () => r({ tabs: tabs.map((t) => ({ ...t })) }); });
      },
    }),
  });

  // Address bar is focused (on macOS WKWebView clicking a <button> leaves focus on <input>).
  const url = app.el('browser-url');
  url.focus();
  assert.equal(url.value, 'https://one.example/');

  app.el('browser-tab-new').click();
  // Periodic poll runs after server created t2, before POST /tabs finishes.
  app.internals.renderBrowserPanel();
  await app.settle();

  // Now POST /tabs resolves.
  releaseNewTab();
  await app.settle();

  assert.equal(url.value, '', 'the address bar kept the old tab URL on the new empty tab');
});

// 5. Clicking "+" then switching conversations steals focus into the other conversation's address bar.
test('switching conversations while + is in flight does not focus the address bar in the new conversation', async () => {
  let releaseNewTab;
  const app = await load({
    globals: { lcGUI: true },
    routes: {
      ...settingsWith({ browser: true }),
      'POST /api/sessions/*/browser/tabs': () => new Promise((r) => { releaseNewTab = r; }),
    },
  });

  app.el('browser-tab-new').click();
  // User switches to another conversation and focuses its prompt box.
  app.internals.selectSession('sess-2', 'general-purpose', '/tmp/workspace');
  await app.settle();
  app.el('input').focus();
  assert.equal(app.doc.activeElement, app.el('input'));

  // The earlier conversation's POST /tabs finishes.
  releaseNewTab({ tabs: [{ id: 't1', url: 'about:blank', title: '', active: true }] });
  await app.settle();

  assert.equal(app.doc.activeElement, app.el('input'),
    '+ from the previous conversation stole focus into browser-url');
});

// 6. Two rapid tab clicks resolving out of order leave the older tab drawn as active.
test('out-of-order tab activation answers keep the newest click', async () => {
  const tabs = [
    { id: 't1', url: 'https://one.example/', title: 'One', active: true },
    { id: 't2', url: 'https://two.example/', title: 'Two', active: false },
    { id: 't3', url: 'https://three.example/', title: 'Three', active: false },
  ];
  let releaseT2;
  const app = await load({
    globals: { lcGUI: true },
    routes: withTabs(tabs, {
      'POST /api/sessions/*/browser/tabs/t2/activate': () => {
        const snap = [
          { id: 't1', url: 'https://one.example/', title: 'One', active: false },
          { id: 't2', url: 'https://two.example/', title: 'Two', active: true },
          { id: 't3', url: 'https://three.example/', title: 'Three', active: false },
        ];
        return new Promise((r) => { releaseT2 = () => r({ tabs: snap }); });
      },
      'POST /api/sessions/*/browser/tabs/t3/activate': () => {
        tabs[0].active = false;
        tabs[1].active = false;
        tabs[2].active = true;
        return { tabs: tabs.map((t) => ({ ...t })) };
      },
    }),
  });

  const btns = () => app.el('browser-tab-list').querySelectorAll('.browser-tab');
  btns()[1].querySelector('.browser-tab-open').click(); // slow t2
  btns()[2].querySelector('.browser-tab-open').click(); // fast t3
  await app.settle();
  assert.equal(app.el('browser-url').value, 'https://three.example/');

  // Hold the trailing refreshLocation from releaseT2 so we see what tabAction itself applies,
  // or even if refreshLocation hasn't resolved yet.
  let holdFinalPoll;
  app.routes['GET /api/sessions/*/browser'] = () => new Promise((r) => { holdFinalPoll = r; });

  releaseT2();
  await app.settle();

  assert.equal(app.el('browser-url').value, 'https://three.example/',
    'older t2 activation overwrote newer t3 address bar');
  assert.ok(btns()[2].classList.contains('active'),
    'older t2 activation overwrote newer t3 active tab');
  if (holdFinalPoll) holdFinalPoll({ has_tab: true, url: 'https://three.example/', title: 'Three', tabs });
});

// 7. In-flight navigate on tab 1 landing after switching to tab 2 overwrites tab 2's bar / error.
test('a navigate started on one tab does not overwrite the address bar after switching to another tab', async () => {
  const tabs = [
    { id: 't1', url: 'https://one.example/', title: 'One', active: true },
    { id: 't2', url: 'https://two.example/', title: 'Two', active: false },
  ];
  let releaseNav;
  const app = await load({
    globals: { lcGUI: true },
    routes: withTabs(tabs, {
      'POST /api/sessions/*/browser/navigate': () => new Promise((r) => { releaseNav = r; }),
      'POST /api/sessions/*/browser/tabs/t2/activate': () => {
        tabs[0].active = false;
        tabs[1].active = true;
        return { tabs: tabs.map((t) => ({ ...t })) };
      },
    }),
  });

  // Start slow navigate on t1.
  const url = app.el('browser-url');
  url.value = 'slow-on-t1.example';
  url.fire('keydown', { key: 'Enter' });

  // Switch to t2 before t1's navigate finishes.
  app.el('browser-tab-list').querySelectorAll('.browser-tab')[1].querySelector('.browser-tab-open').click();
  await app.settle();
  assert.equal(url.value, 'https://two.example/');

  // Now t1's navigate finishes.
  releaseNav({ url: 'https://slow-on-t1.example/', title: 'Slow' });
  await app.settle();

  assert.equal(url.value, 'https://two.example/',
    'navigate answer from t1 overwrote t2 address bar');
});

// 8. Closing the last tab when daemon returns {"tabs": null} leaves the closed tab
// in the strip until refreshLocation finishes (or indefinitely if refreshLocation fails).
test('closing the last tab clears the tab strip from the DELETE answer even when tabs is null', async () => {
  const tabs = [{ id: 't1', url: 'https://one.example/', title: 'One', active: true }];
  let holdPoll = false;
  let releasePoll;
  const app = await load({
    globals: { lcGUI: true },
    routes: withTabs(tabs, {
      'GET /api/sessions/*/browser': () => {
        if (holdPoll) return new Promise((r) => { releasePoll = r; });
        return {
          has_tab: tabs.length > 0,
          url: tabs[0]?.url,
          title: tabs[0]?.title,
          tabs: tabs.map((t) => ({ ...t })),
        };
      },
      'DELETE /api/sessions/*/browser/tabs/t1': () => {
        tabs.length = 0;
        // Exact JSON payload handleBrowserCloseTab returns when Manager.Tabs returns nil:
        return { tabs: null };
      },
    }),
  });

  holdPoll = true;
  app.el('browser-tab-list').querySelector('.browser-tab-close').click();
  await app.settle();

  assert.equal(app.el('browser-tab-list').querySelectorAll('.browser-tab').length, 0,
    'DELETE of last tab with {"tabs":null} left the closed tab drawn in the strip');
  if (releasePoll) releasePoll({ has_tab: false, width: 1280, height: 800 });
});

