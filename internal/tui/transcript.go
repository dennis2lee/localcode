package tui

import "charm.land/lipgloss/v2"

// entryKind distinguishes the handful of things that ever land in the
// transcript, so view.go can style each one appropriately without the
// transcript itself carrying pre-rendered ANSI.
type entryKind int

const (
	entryUser  entryKind = iota // "You: <prompt>"
	entryModel                  // streamed model output, unstyled
	entryTool                   // a server-driven status line ([delegated to x], [cancelled])
	entryLocal                  // a client-only reply (/help, /version, a queued-prompt notice, ...)
)

// transcriptEntry is one unit of transcript content. Plain data — no
// styling, no ANSI — so it can be rendered at whatever width and in
// whatever style view.go currently uses, instead of the old flat string
// baking both in at append time.
type transcriptEntry struct {
	kind entryKind
	text string
}

// appendLocal writes text straight into the transcript without going
// through the server — for /help and /version, which are answered purely
// client-side (well, /version does hit the daemon, but the answer isn't
// part of the session's event log either way).
func (m *Model) appendLocal(text string) {
	m.transcript = append(m.transcript, transcriptEntry{kind: entryLocal, text: text})
	// A local reply (most commonly "[queued] ...", fired while a turn is
	// mid-stream) always starts a fresh paragraph rather than gluing onto
	// whatever model entry happens to be open — see appendModelDelta.
	m.streamOpen = false
	m.refreshViewport()
}

// appendModelDelta adds one streamed chunk of model output. Consecutive
// deltas for the same message accumulate into a single open entry — the
// entry list gains a new element only when a message actually starts, not
// once per delta — so a chat with hundreds of small deltas doesn't turn
// into hundreds of one-character transcript entries. endModelStream (called
// from message.part.end) closes it, so the next delta — the start of a
// different model message — begins a new entry instead of continuing this
// one.
func (m *Model) appendModelDelta(text string) {
	if m.streamOpen && len(m.transcript) > 0 {
		m.transcript[len(m.transcript)-1].text += text
	} else {
		m.transcript = append(m.transcript, transcriptEntry{kind: entryModel, text: text})
		m.streamOpen = true
	}
}

// endModelStream closes the currently-open model entry, if any, so the next
// message.part.delta starts a new paragraph instead of continuing this one.
func (m *Model) endModelStream() {
	m.streamOpen = false
}

// appendTool adds a server-driven status line — [delegated to x],
// [cancelled] — which, like appendLocal, always starts fresh rather than
// extending an in-progress model entry.
func (m *Model) appendTool(text string) {
	m.transcript = append(m.transcript, transcriptEntry{kind: entryTool, text: text})
	m.streamOpen = false
}

// refreshViewport re-renders the transcript into the viewport at its
// current width. GotoBottom only fires when the viewport was already
// scrolled to the bottom before this update — otherwise every single
// streamed delta would yank the view back down out from under someone who
// scrolled up to reread something, which is exactly what happened when
// this unconditionally called GotoBottom on every event.
func (m *Model) refreshViewport() {
	w := m.viewport.Width()
	if w <= 0 {
		w = 80
	}
	atBottom := m.viewport.AtBottom()
	m.viewport.SetContent(lipgloss.NewStyle().Width(w).Render(renderTranscript(m.transcript)))
	if atBottom {
		m.viewport.GotoBottom()
	}
}
