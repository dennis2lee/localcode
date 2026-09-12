import { app, session } from './state.js';
import * as apiClient from './api.js';
import { appendUser, appendTool, appendToolLines, appendError } from './transcript.js';

// isPlainPrompt reports whether text is an ordinary chat message rather
// than a "/"-prefixed command. Only plain prompts are safe to queue while
// a turn is in flight — queueing a command would mean replaying it as
// literal chat text to the model once dequeued, instead of running it.
export function isPlainPrompt(text) {
  return !text.startsWith('/');
}

// The help, in two halves: what this client answers itself, and what the
// daemon answers.
//
// The second half is read off the daemon's own list rather than written
// down here. There used to be three copies of every command —
// agent.SlashCommands, the terminal's own paragraph, and an array in
// this file — and two of them were prose somebody had to remember to
// edit. Four guard-test failures in one afternoon are what that cost;
// they were the duplication reporting itself rather than a defence of
// it.
//
// Plain text, rendered through appendToolLines (text nodes, never HTML),
// so "/<skill name>" displays as written with no escaping step for any
// call site to forget. A previous version built this as an HTML string
// with one entry pre-escaped and the rest not, so that one line rendered
// double-escaped as "&lt;skill name&gt;" (bug B7).
const LOCAL_HELP = [
  'Available commands:',
  '  /help              show this help',
  '  /version            show the daemon version',
  '  /<skill name>        run that skill (e.g. /pdf-tools)',
  '                        type part of a name and press the right arrow to complete it;',
  '                        press it again to cycle through the other matches',
  '  /<custom command>   run a command defined in .localcode/commands/*.md',
  '  Esc                 cancel the running turn',
  '  Tab / Shift+Tab      switch to the next/previous agent',
  '  Alt+Up / Alt+Down    jump back and forth between your own prompts',
  '  /agent              list registered agents',
  '  /agent <name>        switch to that agent (also available via the header dropdown)',
  '  /commands          list registered custom commands',
  '  exit, :q            just show a message (close the browser tab yourself)',
];

// helpLines is what /help prints now: the local half, then one line per
// command the daemon serves.
export function helpLines() {
  const out = LOCAL_HELP.slice();
  if (!app.slashCommands.length) {
    // Honest rather than hardcoded: this page can be attached to a
    // daemon of a different version, and a paragraph written here would
    // be right until it is not.
    out.push('', '  (the daemon\'s own commands have not been fetched yet)');
    return out;
  }
  out.push('', 'Answered by the daemon:');
  for (const c of app.slashCommands) {
    const name = `/${c.name}${c.usage ? ' ' + c.usage : ''}`;
    out.push(`  ${name.padEnd(34)} ${c.description || ''}`);
  }
  return out;
}


// Commands handled entirely client-side — never touch the session's
// event log, so they don't show up again on session replay. Returns
// true if `text` was a local command (and thus already handled).
export async function tryLocalCommand(text) {
  const lower = text.toLowerCase();

  if (lower === 'exit' || lower === ':q') {
    appendUser(text);
    appendTool("Closing the browser tab ends the session (the Web UI can't quit the program directly).");
    return true;
  }

  if (lower === '/help') {
    appendUser(text);
    appendToolLines(helpLines().concat(['', 'You can drag a file onto the input box to attach it.']));
    return true;
  }

  if (lower === '/version') {
    appendUser(text);
    try {
      const v = await apiClient.getVersion();
      appendTool(`localcode ${v.version}`);
    } catch (err) {
      appendError(`failed to fetch version: ${err}`);
    }
    return true;
  }

  if (lower === '/agent') {
    appendUser(text);
    if (app.agents.length === 0) {
      appendTool('No agents registered.');
    } else {
      appendToolLines(
        [`Available agents (/agent <name> to switch, current: ${session.currentAgent || ''}):`]
          .concat(app.agents.map(a => `- ${a.name}: ${a.description || ''}`)),
      );
    }
    return true;
  }

  if (lower === '/commands') {
    appendUser(text);
    if (app.customCommands.length === 0) {
      appendTool('No custom commands registered. (add one under .localcode/commands/*.md)');
    } else {
      appendToolLines(
        ['Available custom commands:'].concat(app.customCommands.map(c => `- /${c.name}: ${c.description || ''}`)),
      );
    }
    return true;
  }

  const agentMatch = text.match(/^\/agent\s+(\S+)/);
  if (agentMatch) {
    appendUser(text);
    try {
      await apiClient.switchAgent(session.sessionID, agentMatch[1]);
    } catch (err) {
      appendError(`failed to switch agent: ${err}`);
    }
    return true;
  }

  // "/config" itself is handled server-side (agent.Loop) like /memory —
  // not intercepted here, so it falls through to sendMessage() below and
  // its response/config.changed event flow back over SSE like anything
  // else the model or a local command answers.
  return false;
}
