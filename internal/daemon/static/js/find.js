// Finding a word in the conversation.
//
// The desktop window has no Ctrl+F. A browser does, and its find is
// perfectly good at finding a string on a page — but the page is not what
// is being searched here. A conversation is a stack of messages with an
// order that means something, and the thing somebody wants from a find in
// one is almost always "where did that path/error/name last come up",
// which is the newest match, not the first one down the document.
//
// So this walks backwards: the newest message first, and inside a message
// the last occurrence first. Pressing next goes further back in time. The
// buttons say "older" and "newer" rather than carrying arrows, because an
// arrow in a find bar means "down the document" everywhere else and this
// is the opposite of that.
import {
  transcriptEl, findBar, findInput, findCount, findOlderBtn, findNewerBtn, findCloseBtn,
} from './dom.js';

// A node holding characters. Portable between the browser and the test
// DOM without asking either what it is: a Text node has a string `data`
// in both, an element has a tagName in both and no `data`, and a comment
// — which the transcript never contains, since every node in it is built
// here — is excluded by name so that this stays true if one ever does.
function isTextNode(node) {
  return node != null && typeof node.data === 'string' && node.nodeName !== '#comment';
}

// The searchable blocks: every element child of the transcript except the
// separators, which carry no message of their own.
//
// The transcript's children rather than a list of class names. Everything
// appended to it is one message-shaped thing — a prompt, a reply, a tool
// row, a thinking block, a review, an error — and a kind added later is
// searchable here without anybody remembering to come back and add it.
// The separator is the exception because it is a label for the boundary
// below it ("YOU"), and matching the word "you" on every turn of every
// conversation would bury every real hit.
export function searchableBlocks() {
  return Array.from(transcriptEl.children || []).filter(
    (el) => !(el.classList && el.classList.contains('turn-sep')),
  );
}

// findMatches is the whole of the ordering decision, and it is a pure
// function of the text so that the order can be tested without a screen.
//
// texts arrive in the order they are drawn, oldest first. The result is
// the reverse: the last block's matches before the first block's, and
// within one block the last occurrence before the first. Reading the
// result front to back walks up the screen.
//
// Matches do not overlap: a search for "aa" in "aaaa" finds two, which is
// what every other find does, and what makes a count mean something.
export function findMatches(texts, query) {
  const needle = String(query == null ? '' : query).toLowerCase();
  if (needle === '') return [];
  const out = [];
  for (let block = texts.length - 1; block >= 0; block--) {
    const hay = String(texts[block] == null ? '' : texts[block]).toLowerCase();
    const inBlock = [];
    for (let at = hay.indexOf(needle); at !== -1; at = hay.indexOf(needle, at + needle.length)) {
      inBlock.push({ block, start: at, end: at + needle.length });
    }
    for (let i = inBlock.length - 1; i >= 0; i--) out.push(inBlock[i]);
  }
  return out;
}

// markRange wraps one [start, end) range of an element's text in <mark>
// elements and answers with them.
//
// Several, not one: the range is measured against the element's text as a
// whole, and that text can be spread over any number of nodes — `a **b**
// c` is three of them once the markdown is rendered — so a match that
// crosses a node boundary is wrapped once per node it covers. The caller
// treats the list as one match, which is what it is.
//
// Built with createElement and createTextNode, like everything else the
// transcript contains. Nothing here composes an HTML string, so a message
// whose text happens to look like markup cannot become markup by being
// searched for.
export function markRange(el, start, end, className) {
  const marks = [];
  let at = 0;
  // The node list is read into an array first: the walk replaces nodes as
  // it goes, and a live childNodes collection would shift under it.
  const walk = (parent) => {
    for (const node of Array.from(parent.childNodes || [])) {
      if (isTextNode(node)) {
        const text = node.data;
        const nodeStart = at;
        at = nodeStart + text.length;
        // The part of this node the range covers, in the node's own
        // coordinates. Empty when the range is entirely before or after
        // it, which is most nodes.
        const from = Math.max(start, nodeStart) - nodeStart;
        const to = Math.min(end, nodeStart + text.length) - nodeStart;
        if (to <= from) continue;
        const head = text.slice(0, from);
        const body = text.slice(from, to);
        const tail = text.slice(to);
        const mark = document.createElement('mark');
        mark.className = className;
        mark.textContent = body;
        if (head !== '') parent.insertBefore(document.createTextNode(head), node);
        parent.insertBefore(mark, node);
        if (tail !== '') parent.insertBefore(document.createTextNode(tail), node);
        parent.removeChild(node);
        marks.push(mark);
      } else if (node.childNodes) {
        walk(node);
      } else {
        // A node with neither children nor characters: in the test DOM
        // this is the opaque node an innerHTML assignment leaves behind.
        // It has no text nodes to split, so it contributes nothing to
        // mark — but its text still counted towards the offsets, or every
        // range after it would be measured against a different string
        // than the one the match was found in.
        at += String(node.text == null ? '' : node.text).length;
      }
    }
  };
  walk(el);
  return marks;
}

// unmark puts one mark's characters back where they were.
//
// The text node is merged into its neighbours rather than left beside
// them. Repeated searching would otherwise cut a long reply into a
// thousand one-word nodes — the text reads the same, so nothing looks
// wrong, and every later search walks a list that keeps growing.
export function unmark(mark) {
  const parent = mark.parentNode;
  if (!parent) return;
  const text = mark.textContent;
  // The siblings are read out of the child list by position rather than
  // through previousSibling: a NodeList has no indexOf and the test DOM
  // has no sibling accessors, and Array.from plus indexOf is the one
  // spelling both of them answer.
  const kids = Array.from(parent.childNodes);
  const i = kids.indexOf(mark);
  const before = i > 0 ? kids[i - 1] : null;
  const after = i >= 0 ? kids[i + 1] : null;
  if (isTextNode(before) && isTextNode(after)) {
    before.data += text + after.data;
    parent.removeChild(after);
    parent.removeChild(mark);
    return;
  }
  if (isTextNode(before)) {
    before.data += text;
    parent.removeChild(mark);
    return;
  }
  if (isTextNode(after)) {
    after.data = text + after.data;
    parent.removeChild(mark);
    return;
  }
  parent.insertBefore(document.createTextNode(text), mark);
  parent.removeChild(mark);
}

// --- the bar ---

// One match is a list of mark elements; the state is which match the
// reader is on, by index into the newest-first list above.
let hits = [];       // [[mark, ...], ...] in newest-first order
let current = -1;
let lastQuery = '';

export function findIsOpen() {
  return !findBar.hidden;
}

function staleHits() {
  for (const marks of hits) {
    for (const mark of marks) if (!blockOf(mark)) return true;
  }
  return false;
}

function clearMarks() {
  for (const marks of hits) for (const mark of marks) unmark(mark);
  hits = [];
  current = -1;
}

function say(text) {
  findCount.textContent = text;
}

// run recomputes everything from the transcript as it is now.
//
// From scratch every time, rather than keeping element references and
// mending them. A reply that is still streaming has its element rewritten
// on every fragment, and a session switch replaces the transcript
// wholesale; anything held across that is a reference into a tree that no
// longer exists. Recomputing is a substring scan over text already in
// memory, which costs nothing next to the render that just happened.
export function runFind(keepPosition = false) {
  const wasAt = current;
  clearMarks();
  const query = findInput.value;
  lastQuery = query;
  if (String(query).trim() === '') {
    say('');
    return;
  }
  const blocks = searchableBlocks();
  const found = findMatches(blocks.map((el) => el.textContent), query);
  if (found.length === 0) {
    say('no matches');
    return;
  }
  for (const m of found) {
    hits.push(markRange(blocks[m.block], m.start, m.end, 'find-hit'));
  }
  // Staying put is for a redraw under the reader's feet; a new query
  // starts at the newest match, which is the first of the list.
  current = keepPosition && wasAt >= 0 ? Math.min(wasAt, hits.length - 1) : 0;
  land();
}

// land marks the current hit, reveals it if it was folded away, and
// brings it on screen.
function land() {
  for (let i = 0; i < hits.length; i++) {
    for (const mark of hits[i]) {
      mark.className = i === current ? 'find-hit current' : 'find-hit';
    }
  }
  say(`${current + 1} of ${hits.length}`);
  const mark = hits[current] && hits[current][0];
  if (!mark) return;

  // A tool call's output is folded shut until somebody opens it, and it
  // is searched all the same — a path or a stack trace in there is
  // exactly what a find is for. Landing on one opens what it is inside,
  // or the view would scroll to a match nothing on screen shows.
  for (let p = mark.parentNode; p && p !== transcriptEl; p = p.parentNode) {
    if (p.hidden) p.hidden = false;
  }

  // Measured against the mark where the layout knows where it is, and
  // against the block otherwise. A block is one message and can be
  // longer than the window, so scrolling to the message is not the same
  // as scrolling to the match.
  const block = blockOf(mark);
  const anchor = mark.offsetTop ? mark : block;
  if (!anchor) return;
  const top = anchor.offsetTop - transcriptEl.offsetTop;
  // A little above the match rather than flush with the top edge, so
  // the line before it is readable and the match does not sit under the
  // top border.
  transcriptEl.scrollTop = Math.max(0, top - 80);
}

function blockOf(node) {
  let el = node;
  while (el && el.parentNode && el.parentNode !== transcriptEl) el = el.parentNode;
  return el && el.parentNode === transcriptEl ? el : null;
}

// step moves through the list. +1 is older, because the list is
// newest-first; -1 is back towards the newest.
//
// It wraps, and says so by moving rather than stopping: a find that stops
// dead at the end of a long conversation reads as broken.
export function stepFind(direction) {
  // Either the word changed, or the transcript did. A reply that was
  // streaming when the search ran has had its element rewritten since,
  // taking those marks with it, so the list is checked against the tree
  // rather than trusted: a hit whose mark is no longer in the transcript
  // is a hit that was counted and cannot be shown.
  if (String(findInput.value) !== lastQuery || staleHits()) {
    runFind(true);
    return;
  }
  if (hits.length === 0) return;
  current = (current + direction + hits.length) % hits.length;
  land();
}

export function openFind() {
  findBar.hidden = false;
  findInput.focus();
  findInput.select();
  // Reopening with the same word still in the box shows where those
  // matches are, rather than an empty bar over a conversation.
  if (String(findInput.value).trim() !== '') runFind();
}

export function closeFind() {
  clearMarks();
  lastQuery = '';
  say('');
  findBar.hidden = true;
}

// The transcript changed under an open bar: a fragment arrived, a turn
// finished, a session was switched. The marks are gone or stale either
// way, so the search runs again and tries to keep the reader where they
// were.
export function findRefresh() {
  if (!findIsOpen()) return;
  if (String(findInput.value).trim() === '') return;
  hits = [];
  current = -1;
  runFind(true);
}

export function wireFind() {
  findInput.addEventListener('input', () => runFind());
  findOlderBtn.addEventListener('click', () => stepFind(1));
  findNewerBtn.addEventListener('click', () => stepFind(-1));
  findCloseBtn.addEventListener('click', () => closeFind());
  // Enter walks older, Shift+Enter newer. Escape is not here: the
  // document's own handler owns it, so that closing the bar and clearing
  // the prompt box cannot both answer one keypress.
  findInput.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') {
      e.preventDefault();
      stepFind(e.shiftKey ? -1 : 1);
    }
  });
}
