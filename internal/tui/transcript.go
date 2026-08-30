package tui

import (
	"encoding/json"
	"strings"

	"charm.land/lipgloss/v2"
)

// argKeys are the tool arguments worth putting beside a tool's name — the
// command for bash, the path for a file read. Order matters: the first one
// present wins.
var argKeys = []string{"command", "path", "file_path", "pattern", "query", "url", "name", "prompt", "description"}

// summarizeToolInput reduces a tool call's arguments to the single value
// that identifies it, on one line and short enough to sit in a transcript
// row. An unknown tool (an MCP server's, say) falls back to the first
// string in the object, so it still says something rather than nothing.
func summarizeToolInput(inputJSON string) string {
	var obj map[string]any
	if json.Unmarshal([]byte(inputJSON), &obj) != nil {
		return ""
	}
	for _, k := range argKeys {
		if s, ok := obj[k].(string); ok && s != "" {
			return oneLine(s)
		}
	}
	for _, v := range obj {
		if s, ok := v.(string); ok && s != "" {
			return oneLine(s)
		}
	}
	return ""
}

func oneLine(s string) string {
	flat := strings.Join(strings.Fields(s), " ")
	if len(flat) > 140 {
		return flat[:139] + "…"
	}
	return flat
}

// entryKind distinguishes the handful of things that ever land in the
// transcript, so view.go can style each one appropriately without the
// transcript itself carrying pre-rendered ANSI.
type entryKind int

const (
	entryUser    entryKind = iota // "You: <prompt>"
	entryModel                    // streamed model output, unstyled
	entryTool                     // a server-driven status line ([delegated to x], [cancelled])
	entryLocal                    // a client-only reply (/help, /version, a queued-prompt notice, ...)
	entryPending                  // a prompt drawn on Enter, until the daemon confirms it
)

// transcriptEntry is one unit of transcript content. Plain data — no
// styling, no ANSI — so it can be rendered at whatever width and in
// whatever style view.go currently uses, instead of the old flat string
// baking both in at append time.
type transcriptEntry struct {
	kind entryKind
	text string
}

// appendEntry is the single mutation point for the transcript. Every write
// goes through here so transcriptRev can't fall behind: applyEvent uses that
// counter to decide whether an event changed anything visible, and a mutator
// that bumped the slice without bumping the counter would leave the viewport
// showing stale text.
func (m *Model) appendEntry(kind entryKind, text string) {
	m.transcript = append(m.transcript, transcriptEntry{kind: kind, text: text})
	m.transcriptRev++
}

// appendUser records a prompt as it comes back from the daemon's event log.
// The server's message.user event stays the single source of truth for what
// the session actually holds, so the local echo this replaces (see
// appendPendingUser) is removed rather than left above a duplicate.
func (m *Model) appendUser(text string) {
	m.resolvePendingUser(text)
	m.appendEntry(entryUser, text)
	m.streamOpen = false
}

// appendPendingUser draws a prompt the instant Enter is pressed, dimmed,
// until the daemon's message.user event arrives with the real line.
//
// The wait in between is everything the daemon does before the model is
// handed the text — hooks, the delegation decision, the first request — and
// on a slow or remote model it is seconds. A screen that does not change in
// that time reads as an Enter that never registered, and the honest
// response to that is to type it again.
func (m *Model) appendPendingUser(text string) {
	m.appendEntry(entryPending, text)
	m.streamOpen = false
	// Called from the key handler, not applyEvent, so it refreshes itself.
	m.refreshViewport()
}

// resolvePendingUser drops the oldest echo of text, and reports whether it
// found one. Oldest first: the same prompt can be sent twice, and each send
// owns one echo.
func (m *Model) resolvePendingUser(text string) bool {
	for i, e := range m.transcript {
		if e.kind != entryPending || e.text != text {
			continue
		}
		m.transcript = append(m.transcript[:i:i], m.transcript[i+1:]...)
		m.transcriptRev++
		return true
	}
	return false
}

// appendLocal writes text straight into the transcript without going
// through the server — for /help and /version, which are answered purely
// client-side (well, /version does hit the daemon, but the answer isn't
// part of the session's event log either way).
func (m *Model) appendLocal(text string) {
	m.appendEntry(entryLocal, text)
	// A local reply (most commonly "[queued] ...", fired while a turn is
	// mid-stream) always starts a fresh paragraph rather than gluing onto
	// whatever model entry happens to be open — see appendModelDelta.
	m.streamOpen = false
	// Called from key handlers, not applyEvent, so it refreshes on its own.
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
		m.transcriptRev++
	} else {
		m.appendEntry(entryModel, text)
		m.streamOpen = true
	}
}

// endModelStream closes the currently-open model entry, if any, so the next
// message.part.delta starts a new paragraph instead of continuing this one.
// endModelStream closes one model message. text is the whole reply as the
// daemon recorded it, and is drawn here when no deltas arrived for it —
// which is the case on replay, where the daemon drops the fragments of
// replies that have already finished and sends only this. See
// collapseFinishedDeltas in the daemon.
func (m *Model) endModelStream(text string) {
	if !m.streamOpen && text != "" {
		m.appendModelDelta(text)
	}
	m.streamOpen = false
}

// appendTool adds a server-driven status line — [delegated to x],
// [cancelled] — which, like appendLocal, always starts fresh rather than
// extending an in-progress model entry.
func (m *Model) appendTool(text string) {
	m.appendEntry(entryTool, text)
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
		w = fallbackWidth
	}
	atBottom := m.viewport.AtBottom()
	m.viewport.SetContent(lipgloss.NewStyle().Width(w).Render(renderTranscript(m.transcript, w)))
	if atBottom {
		m.viewport.GotoBottom()
	}
}
