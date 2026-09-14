'use strict';

// A diff for edit and write_file tool results: before and after, added
// and removed lines marked, collapsed the way tool cards already are.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

function finishEdit(app, id, name, input, content, isError = false) {
  app.applyEvent({ type: 'tool.start', data: { tool_use_id: id, name, input: JSON.stringify(input) } });
  app.applyEvent({ type: 'tool.end', data: { tool_use_id: id, content, is_error: isError } });
  const rows = app.el('transcript').querySelectorAll('.msg-toolcall');
  return rows[rows.length - 1];
}

test('an edit shows its removed and added lines', async () => {
  const app = await load();
  const row = finishEdit(app, 'e1', 'edit',
    { path: 'main.go', old_string: 'x := 1\n', new_string: 'x := 2\n' },
    'replaced 1 occurrence(s) in main.go');
  const diff = row.querySelector('.todiff');
  assert.ok(diff, 'an edit row has a diff block');
  assert.ok(diff.querySelector('.diff-del'), 'the removed line is marked');
  assert.ok(diff.querySelector('.diff-add'), 'the added line is marked');
  assert.ok(diff.textContent.includes('- x := 1'), diff.textContent);
  assert.ok(diff.textContent.includes('+ x := 2'), diff.textContent);
});

test('the diff starts collapsed and opens with the row', async () => {
  const app = await load();
  // Fifty lines of change must not push the conversation off the
  // screen: the block starts hidden like the detail block beside it.
  const oldLines = Array.from({ length: 50 }, (_, i) => `old ${i}`).join('\n');
  const newLines = Array.from({ length: 50 }, (_, i) => `new ${i}`).join('\n');
  const row = finishEdit(app, 'e1', 'edit',
    { path: 'big.go', old_string: oldLines, new_string: newLines },
    'replaced 1 occurrence(s) in big.go');
  const diff = row.querySelector('.todiff');
  const detail = row.querySelector('.detail');
  assert.equal(diff.hidden, true, 'the diff starts collapsed');
  assert.equal(detail.hidden, true, 'the detail still starts collapsed');
  row.querySelector('.head').fire('click');
  assert.equal(diff.hidden, false, 'opening the row opens the diff');
  assert.equal(detail.hidden, false, 'opening the row opens the detail');
  row.querySelector('.head').fire('click');
  assert.equal(diff.hidden, true, 'closing the row closes the diff again');
});

test('a diff longer than the render bound is cut with its count', async () => {
  const app = await load();
  const oldLines = Array.from({ length: 150 }, (_, i) => `old ${i}`).join('\n');
  const newLines = Array.from({ length: 150 }, (_, i) => `new ${i}`).join('\n');
  const row = finishEdit(app, 'e1', 'edit',
    { path: 'huge.go', old_string: oldLines, new_string: newLines },
    'replaced 1 occurrence(s) in huge.go');
  const diff = row.querySelector('.todiff');
  assert.ok(diff.textContent.includes('more diff lines, not shown'), diff.textContent);
});

test('a created file shows its content as added lines', async () => {
  const app = await load();
  const row = finishEdit(app, 'w1', 'write_file',
    { path: 'new.go', content: 'package main\n\nfunc main() {}\n' },
    'created new.go: 3 line(s), 29 bytes');
  const diff = row.querySelector('.todiff');
  assert.ok(diff, 'a created file has a diff block');
  assert.ok(!diff.querySelector('.diff-del'), 'nothing was removed from a new file');
  assert.ok(diff.textContent.includes('+ package main'), diff.textContent);
});

// The replaced half cannot be built client-side: the result carries
// counts ("it had 340 line(s), it now has 12") and the input carries
// only the new text, so the before half is nowhere the page can reach.
// The row must then show no diff rather than a wrong one.
test('a replaced file shows no diff, because its old text is not on the wire', async () => {
  const app = await load();
  const row = finishEdit(app, 'w1', 'write_file',
    { path: 'old.go', content: 'package main\n' },
    'replaced old.go entirely: it had 340 line(s), it now has 1 (13 bytes). Everything the file previously held is gone.');
  assert.equal(row.querySelector('.todiff'), null, 'a replaced file has no diff block');
});

test('a failed edit shows no diff, because nothing changed', async () => {
  const app = await load();
  const row = finishEdit(app, 'e1', 'edit',
    { path: 'main.go', old_string: 'x := 1\n', new_string: 'x := 2\n' },
    'old_string not found in main.go', true);
  assert.equal(row.querySelector('.todiff'), null, 'a failed edit has no diff block');
});

test('other tools get no diff block at all', async () => {
  const app = await load();
  const row = finishEdit(app, 'b1', 'bash', { command: 'go test ./...' }, 'ok');
  assert.equal(row.querySelector('.todiff'), null, 'a bash row has no diff block');
});

test('diff content is text, never an element', async () => {
  const app = await load();
  const row = finishEdit(app, 'e1', 'edit',
    {
      path: 'x.html',
      old_string: '<script>alert(1)</script>\n',
      new_string: '<b>bold</b>\n',
    },
    'replaced 1 occurrence(s) in x.html');
  const html = app.transcript();
  assert.ok(!html.includes('<script>alert'), html);
  assert.ok(!html.includes('<b>bold</b>'), html);
  assert.ok(html.includes('&lt;script&gt;alert(1)&lt;/script&gt;'), html);
});

test('an edit that changed nothing shows no diff', async () => {
  const app = await load();
  const row = finishEdit(app, 'e1', 'edit',
    { path: 'same.go', old_string: 'x := 1\n', new_string: 'x := 1\n' },
    'replaced 1 occurrence(s) in same.go');
  assert.equal(row.querySelector('.todiff'), null, 'identical old and new strings have no diff block');
});

test('diffLines keeps one shared line of context and drops the rest', async () => {
  const app = await load();
  const rows = app.diffLines('a\nb\nOLD\nc\nd', 'a\nb\nNEW\nc\nd');
  // Joined to plain strings first: the rows live in the harness VM's
  // realm, whose Array fails deepStrictEqual against this realm's on
  // prototype alone.
  const kinds = Array.from(rows, (r) => r.kind).join(',');
  assert.equal(kinds, 'ctx,del,add,ctx', kinds);
  assert.equal(Array.from(rows, (r) => r.text).join('\n'), 'b\nOLD\nNEW\nc');
});
