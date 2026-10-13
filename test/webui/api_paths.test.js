'use strict';

// Every request the page makes goes through api.js, and a path segment a
// URL resolves away sends it to another route: DELETE
// /api/sessions/{id}/tasks/.. is DELETE /api/sessions/{id}, which deletes
// the conversation. api.js refuses such a path before anything is sent,
// whatever wrapper built it. (Found by review: the task routes were closed
// and the schedule routes were not.)
//
// api.js is loaded on its own, with fetch replaced, and every URL it would
// send is resolved the way a browser resolves it (WHATWG URL).

const test = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');
const { pathToFileURL } = require('node:url');

const API = pathToFileURL(path.join(__dirname, '../../internal/daemon/static/js/api.js')).href;

async function load() {
  const sent = [];
  global.fetch = async (url, init = {}) => {
    sent.push({ method: init.method || 'GET', url: String(url) });
    return { ok: true, status: 204, headers: { get: () => '' }, text: async () => '', json: async () => ({}) };
  };
  return { api: await import(API), sent };
}

const resolved = (u) => { const r = new URL(u, 'http://127.0.0.1:4096'); return r.pathname + r.search; };

// One call per wrapper that puts an id in a path, with the id where the
// wrapper puts it.
const calls = (api, id) => ({
  renameSession: () => api.renameSession(id, 't'),
  forkSession: () => api.forkSession(id),
  deleteSession: () => api.deleteSession(id),
  setSessionGroup: () => api.setSessionGroup(id, 'g'),
  switchAgent: () => api.switchAgent(id, 'plan'),
  sendChatMessage: () => api.sendChatMessage(id, 'hi'),
  cancelTask: () => api.cancelTask(id),
  deleteTask_task: () => api.deleteTask('S', id),
  deleteTask_session: () => api.deleteTask(id, 'T'),
  deleteAllTasks: () => api.deleteAllTasks(id),
  cancelSessionTurn: () => api.cancelSessionTurn(id),
  detachSessionChild: () => api.detachSessionChild(id),
  resolvePermissionRequest: () => api.resolvePermissionRequest('S', id, true, 'once'),
  answerQuestion: () => api.answerQuestion('S', id, 'a'),
  archiveSession: () => api.archiveSession(id),
  retrieveSession: () => api.retrieveSession(id),
  getEffort: () => api.getEffort(id),
  setEffort: () => api.setEffort(id, 'high'),
  listSchedules: () => api.listSchedules(id),
  markScheduleSeen: () => api.markScheduleSeen('S', id),
  renameSchedule: () => api.renameSchedule('S', id, 'n'),
  deleteSchedule: () => api.deleteSchedule('S', id),
  deleteSchedule_session: () => api.deleteSchedule(id, 'X'),
  uploadFile: () => api.uploadFile(id, new Blob(['x']), 'f.txt'),
});

test('an ordinary id gives a URL the browser sends as written', async () => {
  const { api, sent } = await load();
  for (const [name, call] of Object.entries(calls(api, 'task-1788019200000000000-1'))) {
    sent.length = 0;
    await call();
    assert.equal(sent.length, 1, `${name} sent nothing`);
    assert.equal(resolved(sent[0].url), sent[0].url, `${name}: ${sent[0].url}`);
  }
});

// Refused, or encoded into a segment that stays one: either way nothing
// reaches a route other than the one the wrapper names.
test('an id a URL would resolve away never reaches another route', async () => {
  const { api, sent } = await load();
  for (const id of ['.', '..', '%2e', '%2E%2e', '.%2e', '', 'a\\..']) {
    for (const [name, call] of Object.entries(calls(api, id))) {
      sent.length = 0;
      let refused = false;
      try { await call(); } catch (err) { refused = err.status === 400; }
      if (refused) {
        assert.equal(sent.length, 0, `${name}(${JSON.stringify(id)}) was refused and sent anyway`);
        continue;
      }
      assert.equal(sent.length, 1, `${name}(${JSON.stringify(id)}) neither sent nor refused`);
      assert.equal(resolved(sent[0].url), sent[0].url, `${name}(${JSON.stringify(id)}) sent ${sent[0].url}`);
    }
  }
});

test('the ids that become dot segments are refused, not sent', async () => {
  const { api, sent } = await load();
  for (const id of ['.', '..', '%2e%2e', '']) {
    sent.length = 0;
    await assert.rejects(api.deleteSchedule('S', id), (err) => err.status === 400, JSON.stringify(id));
    await assert.rejects(api.deleteTask('S', id === '%2e%2e' ? '..' : id), (err) => err.status === 400, JSON.stringify(id));
    assert.equal(sent.length, 0);
  }
});

// A URL drops a tab, newline or carriage return anywhere in it, and
// spaces and control characters at either end, before reading the path:
// ".\t." is "..". (Found by review.)
test('a path a URL would read differently once it drops characters is never sent', async () => {
  const { api, sent } = await load();
  for (const raw of [
    '/api/sessions/s-1/.\t.', '/api/sessions/s-1/.\n.', '/api/sessions/s-1/.\r.',
    '/api/sessions/s-1/%2\te%2e', '/api/sessions/s-1/schedules/\t..', '/\t/elsewhere.example/x',
    '/api/sessions/s-1/.. ', ' /api/sessions', '/api/sessions/s-1\u0000/x',
  ]) {
    sent.length = 0;
    await assert.rejects(api.api('DELETE', raw), (err) => err.status === 400, JSON.stringify(raw));
    assert.equal(sent.length, 0, JSON.stringify(raw));
  }
  for (const [name, call] of Object.entries(calls(api, '.\t.'))) {
    sent.length = 0;
    try { await call(); } catch { continue; }
    assert.equal(resolved(sent[0].url), sent[0].url, `${name} sent ${JSON.stringify(sent[0].url)}`);
  }
});

// What the page does send is not refused: query strings carry anything,
// and the root is its own clean form.
test('the shapes the page sends are not refused', async () => {
  const { api, sent } = await load();
  for (const p of ['/api/sessions', '/api/usage?window=week', '/api/workspace?session=a%2Fb%23c%2e',
    '/api/sessions/s-1/events?tail=200', '/api/x?next=/a/../b#/..', '/']) {
    sent.length = 0;
    await api.api('GET', p);
    assert.equal(sent.length, 1, p);
  }
});

// An id is one segment: a slash, question mark or hash in it is encoded
// rather than becoming part of the route or the query.
test('a task id is encoded as one path segment', async () => {
  const { api, sent } = await load();
  await api.deleteTask('S', 'a/b?c#d');
  assert.equal(sent[0].url, '/api/sessions/S/tasks/a%2Fb%3Fc%23d');
  sent.length = 0;
  await api.deleteAllTasks('s/x');
  assert.equal(sent[0].url, '/api/sessions/s%2Fx/tasks');
});
