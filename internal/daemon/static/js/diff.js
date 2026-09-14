// A line diff for edit and write_file tool cards.
//
// An edit or a write_file result says a file changed but not what
// changed in it ("replaced 1 occurrence in main.go"), and the full
// arguments and result sit one click away inside a <pre> nobody opens
// for a one-line change. This module builds the before/after view from
// what the tool.end event already carries — edit's input holds both
// old_string and new_string, a created file's input holds its content —
// so no wire change is needed. A replaced file's old text is nowhere in
// the payload (write.go reports counts only), and that half is
// deliberately not built here: see editDiffForTool.
//
// No HTML is produced here, only {kind, text} rows. transcript.js turns
// each row into a textContent node, so a diff of hostile file content
// cannot become an element — the same rule transcript.js states for
// everything it appends.

// diffMaxInputLines bounds the inputs before any work happens. edit's
// old_string is usually a handful of lines, but replace_all on a big
// block or a whole created file can be thousands; the trim below is
// linear, but rendering thousands of rows would still bury the
// conversation the card is collapsing for. Past the bound there is no
// diff rather than a slow one.
export const diffMaxInputLines = 400;

// diffMaxRenderLines bounds what transcript.js draws of one diff. The
// card starts collapsed, so a fifty-line diff costs nothing on screen —
// but opening it should still not mean scrolling through a whole file.
export const diffMaxRenderLines = 100;

// diffLines reduces two texts to removed/added rows with their shared
// head and tail kept as context. Only the common prefix and suffix are
// trimmed and the middle is one removed block followed by one added
// block, rather than a full LCS: an edit's old_string/new_string are
// the changed region itself, so the middle IS the change, and the
// linear scan stays honest on inputs where a quadratic diff would not.
export function diffLines(oldText, newText) {
  const oldL = String(oldText ?? '').split('\n');
  const newL = String(newText ?? '').split('\n');
  if (oldL.length > diffMaxInputLines || newL.length > diffMaxInputLines) return null;
  let head = 0;
  while (head < oldL.length && head < newL.length && oldL[head] === newL[head]) head++;
  let tail = 0;
  while (tail < oldL.length - head && tail < newL.length - head &&
    oldL[oldL.length - 1 - tail] === newL[newL.length - 1 - tail]) tail++;
  const out = [];
  // One shared line on each side locates the change; the rest of the
  // shared head/tail is the file around it, which the result text
  // already quotes back ("The file now reads:").
  const ctxHead = oldL.slice(Math.max(0, head - 1), head);
  for (const t of ctxHead) out.push({ kind: 'ctx', text: t });
  for (const t of oldL.slice(head, oldL.length - tail)) out.push({ kind: 'del', text: t });
  for (const t of newL.slice(head, newL.length - tail)) out.push({ kind: 'add', text: t });
  const tailCtx = oldL.slice(oldL.length - tail, oldL.length - tail + (tail > 0 ? 1 : 0));
  for (const t of tailCtx) out.push({ kind: 'ctx', text: t });
  // All shared: the tool reported an edit that changed nothing (edit.go
  // says so outright when new_string is identical). No rows, no card.
  if (!out.some((r) => r.kind === 'del' || r.kind === 'add')) return [];
  return out;
}

// editDiffForTool decides whether a finished tool call gets a diff, and
// what it shows. name and inputJSON come from the tool.end event, which
// carries the call's arguments next to its result (see turn.go); result
// is the result text, read only for the created/replaced distinction
// write.go draws.
export function editDiffForTool(name, inputJSON, result) {
  let input;
  try { input = JSON.parse(inputJSON || '{}'); } catch { return null; }
  if (!input || typeof input !== 'object') return null;
  if (name === 'edit') {
    if (typeof input.old_string !== 'string' || typeof input.new_string !== 'string') return null;
    return diffLines(input.old_string, input.new_string);
  }
  if (name === 'write_file') {
    // A created file's whole content is the change, shown as added. A
    // replaced file cannot be shown: the result carries counts
    // ("it had 340 line(s), it now has 12") and the input carries only
    // the new text, so the before half is nowhere the client can reach
    // without a wire change — and this task forbids that wire change.
    if (typeof input.content !== 'string') return null;
    if (!/^created /.test(String(result ?? ''))) return null;
    const lines = input.content.split('\n');
    if (lines.length > diffMaxInputLines) return null;
    return lines.map((text) => ({ kind: 'add', text }));
  }
  return null;
}
