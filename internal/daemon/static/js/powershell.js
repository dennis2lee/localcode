// The PowerShell panel: PowerShell terminals on this computer, which the
// model and the person work in, in a column of their own after the SSH
// panel, in the desktop window and in the Web UI. Windows only: the
// daemon reports powershell_available, and anywhere else the panel, its
// handle and its header button are never drawn.
//
// It is a terminal panel (js/termpanel.js) over the PowerShell routes
// (docs/API.md, "PowerShell (per session)"). Its + starts a PowerShell at
// once, in the conversation's working directory, and shows it: there is
// no host to name. A PowerShell terminal asks the person nothing through
// the panel; a program's own question is on its screen.

import { app, session } from './state.js';
import { createTerminalPanel } from './termpanel.js';

// powershellShown is whether the panel is drawn for the conversation on
// screen.
export function powershellShown() {
  return !!app.powershell && !!app.powershellAvailable && !!session.sessionID;
}

const panel = createTerminalPanel({
  prefix: 'ps',
  route: 'powershell',
  layout: 'powershell',
  name: 'PowerShell',
  shown: powershellShown,
  opens: 'start',
  words: {
    close: 'end this PowerShell',
    connecting: (where) => `starting ${where}`,
    again: 'Not started again',
    opened: 'Not started',
  },
});

// renderPowerShellPanel shows or hides the panel and points its stream at
// the conversation on screen. Called when the setting changes and when a
// conversation is opened.
export function renderPowerShellPanel() {
  panel.render();
}

// initPowerShellPanel wires the panel's controls. Once, at startup.
export function initPowerShellPanel() {
  panel.init();
}
