// The SSH panel: the SSH terminals the model and the person work in, in a
// column of their own after the browser panel, in the desktop window and
// in the Web UI. It is a terminal panel (js/termpanel.js) over the SSH
// routes, whose + opens the connect form.
//
// What only the person may answer (a host key, a password, a passphrase,
// a server's prompts) is asked in a box drawn by the page, not by the
// remote, and answered here; nothing in the conversation can answer it.

import { app, session } from './state.js';
import { createTerminalPanel } from './termpanel.js';

// The helpers the two terminal panels share, exported here as they were
// before there were two.
export { VIEWER, FLAGS, SCROLL_KEEP, isMac, runStyle, keyAction, fitSize, inputPlace } from './termpanel.js';

// sshShown is whether the panel is drawn for the conversation on screen.
export function sshShown() {
  return !!app.ssh && !!session.sessionID;
}

const panel = createTerminalPanel({
  prefix: 'ssh',
  route: 'ssh',
  layout: 'ssh',
  name: 'SSH',
  shown: sshShown,
  opens: 'form',
  words: {
    close: 'close this connection',
    connecting: (where) => `connecting to ${where}`,
    again: 'Not reconnected',
    opened: 'Not connected',
  },
});

// renderSSHPanel shows or hides the panel and points its stream at the
// conversation on screen. Called when the setting changes and when a
// conversation is opened.
export function renderSSHPanel() {
  panel.render();
}

// initSSHPanel wires the panel's controls. Once, at startup.
export function initSSHPanel() {
  panel.init();
}
