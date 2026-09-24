'use strict';

// The line the reopened window draws about a failed MSI install.
//
// The helper relaunched this window instead of a message box, so one
// line in the conversation view is the whole of the report until
// somebody clicks Check in the settings panel. It is drawn on page
// load, without any click, the way a recovered notice is drawn (a note
// that ends nothing), and written to no session's log. The daemon
// answers from its local record file and remembers the record it drew,
// so the line appears once: on the first load after the failure, not on
// every reload.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const FAILED = {
  version: '0.46.0', exit_code: 1625, status: 'failed', meaning: 'failed (exit code 1625)',
  log: 'C:\\Users\\u\\AppData\\Local\\localcode\\updates\\localcode-0.46.0-msi.log',
};

test('a failed install is said on load, without any click', async () => {
  const app = await load({
    routes: { 'GET /api/update/install-notice': { notice: FAILED } },
  });

  assert.equal(app.callsTo('GET', '/api/update/install-notice').length, 1);
  assert.equal(app.callsTo('GET', '/api/update').length, 0,
    'the line must not cost a check: it answers with the network down');

  const html = app.transcript();
  assert.match(html, /0\.46\.0 did not install/);
  assert.match(html, /1625/);
  assert.match(html, /failed \(exit code 1625\)/);
  assert.match(html, /localcode-0\.46\.0-msi\.log/);
  assert.ok(!html.includes('class="error"'),
    'a reported failure is a note, not a failure of this turn: ' + html);
});

test('the line is a note that ends nothing and writes nothing', async () => {
  const app = await load({
    routes: { 'GET /api/update/install-notice': { notice: FAILED } },
  });
  app.state.waiting = true;

  await app.settle();

  assert.equal(app.state.waiting, true, 'drawing the line must not stop the turn');
  assert.equal(app.calls.filter((c) => c.method === 'POST').length, 0,
    'the line is drawn in the DOM only: ' + JSON.stringify(app.calls));
});

test('a cancelled install draws nothing', async () => {
  // The daemon answers null for a cancelled record: the person
  // cancelled it themselves, so there is nothing to say.
  const app = await load({
    routes: { 'GET /api/update/install-notice': { notice: null } },
  });

  assert.equal(app.transcript(), '');
});

test('an installed record draws nothing', async () => {
  // The daemon answers null for an installed record too: the window
  // coming back is the whole of that report.
  const app = await load({
    routes: { 'GET /api/update/install-notice': { notice: null } },
  });

  assert.equal(app.transcript(), '');
});

test('the line is not drawn a second time', async () => {
  // One daemon behind two page loads: it carries the notice on the
  // first and answers nothing on the second.
  let first = true;
  const routes = {
    'GET /api/update/install-notice': () => {
      if (first) {
        first = false;
        return { notice: FAILED };
      }
      return { notice: null };
    },
  };
  const reopened = await load({ routes });
  assert.match(reopened.transcript(), /0\.46\.0 did not install/);

  const reloaded = await load({ routes });
  assert.equal(reloaded.transcript(), '',
    'the reload after the reopened window stays silent: ' + reloaded.transcript());
});

test('a notice that cannot load stays silent', async () => {
  const app = await load({
    routes: { 'GET /api/update/install-notice': { networkError: 'connection refused' } },
  });

  assert.equal(app.transcript(), '',
    'a missing line is not worth an error line: the panel still reports the record on Check');
  assert.equal(app.consoleErrors.length, 0,
    'the failed fetch must be caught, not logged: ' + JSON.stringify(app.consoleErrors));
});
