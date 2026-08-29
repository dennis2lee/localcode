import { app, session } from './state.js';
import {
  debateModal, debateReviewersEl, debateRoundsInput, debateTaskInput,
  debatePreviewEl, debateNoteEl, debateStartBtn, inputEl,
} from './dom.js';
import { Modal } from './modal.js';
import { sendMessage, autoResizeInput } from './composer.js';

// Starting a debate from a form.
//
// The command is precise and nobody knows it exists. The form asks for
// the three things it needs as three things — who reviews, how many
// rounds, the work — and shows the command line it is about to send, so
// it teaches the command rather than replacing it.
//
// It sends that line through the ordinary send path rather than an
// endpoint of its own. There is nothing a debate needs that a typed
// "/debate" does not already do, and one code path is one set of guards:
// a turn already running, a session not yet chosen, the queue.

export const debateDialog = new Modal(debateModal);

const MAX_REVIEWERS = 3;
const MAX_ROUNDS = 10;

function setNote(el, text, kind) {
  el.textContent = text;
  el.className = kind ? `note ${kind}` : 'note';
}

// The reviewers are every agent except the one already running: an agent
// cannot review its own work, and offering it is offering a choice that
// is about to be refused.
//
// session.currentAgent and not the header dropdown, which is a near miss
// worth recording: the dropdown shows its first option when nothing has
// selected one, so a page whose session runs an agent that is not in the
// list at all reads as running the first agent in it. Filtering by that
// would hide a perfectly good reviewer. setCurrentAgent is the one place
// that moves this, and it moves the dropdown with it.
function reviewerCandidates() {
  const agents = Array.isArray(app.agents) ? app.agents : [];
  return agents.filter((a) => a && a.name && a.name !== session.currentAgent);
}

export function renderDebateReviewers() {
  if (!debateReviewersEl) return;
  debateReviewersEl.innerHTML = '';
  const candidates = reviewerCandidates();
  if (candidates.length === 0) {
    const p = document.createElement('p');
    p.className = 'note';
    p.textContent = 'This configuration has no second agent to review with. Add one in config.json.';
    debateReviewersEl.appendChild(p);
    return;
  }
  for (const agent of candidates) {
    const row = document.createElement('label');
    row.className = 'debate-reviewer';
    const box = document.createElement('input');
    box.type = 'checkbox';
    box.value = agent.name;
    box.addEventListener('change', onReviewerToggle);
    const name = document.createElement('span');
    name.className = 'name';
    name.textContent = agent.name;
    const model = document.createElement('span');
    model.className = 'model';
    model.textContent = agent.model || '';
    row.append(box, name, model);
    debateReviewersEl.appendChild(row);
  }
}

function checkedReviewers() {
  if (!debateReviewersEl) return [];
  return Array.from(debateReviewersEl.querySelectorAll('input'))
    .filter((b) => b.checked)
    .map((b) => b.value);
}

// A panel is capped because each member is a model turn in every round.
// The cap is enforced here as well as in the daemon so the fourth box
// simply will not tick, rather than the form being refused after it is
// filled in.
function onReviewerToggle() {
  const chosen = checkedReviewers();
  if (chosen.length > MAX_REVIEWERS) {
    const boxes = Array.from(debateReviewersEl.querySelectorAll('input')).filter((b) => b.checked);
    boxes[boxes.length - 1].checked = false;
    setNote(debateNoteEl, `At most ${MAX_REVIEWERS} reviewers: each one is a model turn in every round.`, 'err');
  } else {
    setNote(debateNoteEl, '');
  }
  renderDebatePreview();
}

export function debateCommand() {
  const reviewers = checkedReviewers();
  const rounds = clampRounds(debateRoundsInput.value);
  const task = debateTaskInput.value.trim();
  if (reviewers.length === 0 || !task) return '';
  return `/debate ${reviewers.join(',')} ${rounds} ${task}`;
}

function clampRounds(raw) {
  const n = parseInt(raw, 10);
  if (!Number.isFinite(n) || n < 1) return 1;
  return Math.min(n, MAX_ROUNDS);
}

// The preview is the command and the size of it. The turn count is the
// part worth seeing before agreeing: rounds x (1 + reviewers), which is
// twenty model turns before anybody notices they typed 10.
export function renderDebatePreview() {
  if (!debatePreviewEl) return;
  const command = debateCommand();
  if (!command) {
    setNote(debatePreviewEl, '');
    return;
  }
  const reviewers = checkedReviewers().length;
  const rounds = clampRounds(debateRoundsInput.value);
  const turns = rounds * (1 + reviewers);
  setNote(debatePreviewEl, `${command}\n≈ ${turns} model turns`);
}

export function openDebateDialog() {
  renderDebateReviewers();
  debateRoundsInput.value = '3';
  debateTaskInput.value = '';
  setNote(debateNoteEl, '');
  setNote(debatePreviewEl, '');
  debateStartBtn.disabled = false;
  debateDialog.open();
  debateTaskInput.focus();
}

export function closeDebateDialog() {
  debateDialog.close();
}

export function startDebate() {
  const reviewers = checkedReviewers();
  if (reviewers.length === 0) {
    setNote(debateNoteEl, 'Pick at least one reviewer.', 'err');
    return;
  }
  if (!debateTaskInput.value.trim()) {
    setNote(debateNoteEl, 'Say what to do.', 'err');
    return;
  }
  const command = debateCommand();
  closeDebateDialog();
  // Through the prompt box, so the transcript records the command that
  // started it and every guard the box already has applies to this too.
  inputEl.value = command;
  autoResizeInput();
  sendMessage();
}
