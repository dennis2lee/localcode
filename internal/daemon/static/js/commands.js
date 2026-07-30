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

// Every line here is plain text, rendered through appendToolLines (text
// nodes, never HTML) — so "/<skill name>" displays as written with no
// escaping step for any call site to forget. A previous version built this
// as an HTML string with one entry pre-escaped and the rest not, so that one
// line rendered double-escaped as "&lt;skill name&gt;" (bug B7); the current
// text-node rendering makes that class of bug unrepresentable.
export const HELP_TEXT = [
  'Available commands:',
  '  /help              show this help',
  '  /version            show the daemon version',
  '  /skill              list registered skills',
  '  /<skill name>        run that skill (e.g. /pdf-tools)',
  '  Esc                 cancel the running turn',
  '  Tab / Shift+Tab      switch to the next/previous agent',
  '  /agent              list registered agents',
  '  /agent <name>        switch to that agent (also available via the header dropdown)',
  '  /init              scan the repo and create/improve an AGENTS.md rules file',
  '  /memory            show the auto memory directory/index (MEMORY.md)',
  '  /config            show current settings (auto_compact, show_tps, auto_delegate)',
  '  /config auto_compact on|off   toggle auto-compaction above 80% context usage',
  '  /config show_tps on|off       toggle the tokens/sec display under the prompt',
  '  /config auto_delegate on|off  send matching prompts to a cheaper sub-agent',
  '                        (the pill under the prompt box also sets the target agent and patterns)',
  '  /compact           summarize and compact the conversation right now',
  '  /compact <instructions>      give instructions for how to compact',
  '  /usage              show cumulative token usage per model',
  '  /commands          list registered custom commands',
  '  /<custom command>   run a command defined in .localcode/commands/*.md',
  '  exit, :q            just show a message (close the browser tab yourself)',
];

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
    appendToolLines(HELP_TEXT.concat(['', 'You can drag a file onto the input box to attach it.']));
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
