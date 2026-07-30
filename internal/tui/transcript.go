package tui

import "charm.land/lipgloss/v2"

// appendLocal writes text straight into the transcript without going
// through the server — for /help and /version, which are answered purely
// client-side (well, /version does hit the daemon, but the answer isn't
// part of the session's event log either way).
func (m *Model) appendLocal(text string) {
	m.transcript += toolStyle.Render(text) + "\n\n"
	m.refreshViewport()
}

// refreshViewport pushes the current transcript into the viewport,
// word-wrapped to the viewport's width first. The viewport itself never
// wraps on its own — bubbles/viewport just renders whatever lines it's
// given and lets long ones run off the right edge — so without this, a
// model reply with no newlines in it (the common case) streams straight
// off-screen instead of becoming readable multi-line text. lipgloss's
// Width() wraps at rune boundaries while still measuring printable width
// correctly around the ANSI styling userStyle/toolStyle/etc. already
// applied to parts of the transcript.
func (m *Model) refreshViewport() {
	w := m.viewport.Width()
	if w <= 0 {
		w = 80
	}
	m.viewport.SetContent(lipgloss.NewStyle().Width(w).Render(m.transcript))
	m.viewport.GotoBottom()
}
