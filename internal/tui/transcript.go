package tui

import (
	"encoding/json"
	"strings"

	"localcode/internal/client"
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
	entryUser     entryKind = iota // "You: <prompt>"
	entryModel                     // streamed model output, unstyled
	entryTool                      // a server-driven status line ([delegated to x], [cancelled])
	entryLocal                     // a client-only reply (/help, /version, a queued-prompt notice, ...)
	entryPending                   // a prompt drawn on Enter, until the daemon confirms it
	entrySent                      // a message handed to a turn already running
	entryThinking                  // a muse model's reasoning, folded once the answer starts; see thinking.go
)

// transcriptEntry is one unit of transcript content. Plain data — no
// styling, no ANSI — so it can be rendered at whatever width and in
// whatever style view.go currently uses, instead of the old flat string
// baking both in at append time.
type transcriptEntry struct {
	kind entryKind
	text string
	// The rest is entryThinking's alone: its header line, whether it is
	// still streaming, and whether its body shows once it has folded.
	// Plain comparable fields, so the render cache still tells an entry
	// that changed from one that did not by comparing the two.
	note string
	live bool
	open bool
	// send is the send an echo stands for (entryPending, entrySent), so a
	// send nothing took removes its own echo and no other. Zero for every
	// other entry.
	send int
	// took is what the daemon has said about that send, and at the log's
	// last seq when it took it (tookAt). heldBy is the seq of the latest
	// stop that came while the answer was still on its way, decided when
	// it arrives. See abandonPendingUsers and noteReceipt.
	took   tookState
	at     uint64
	heldBy uint64
}

// tookState is what an echo knows of its send.
type tookState uint8

const (
	// tookAwaited: the daemon has not answered the send yet.
	tookAwaited tookState = iota
	// tookAt: the daemon took it when its log's last seq was the echo's at.
	tookAt
	// tookNoSeq: the daemon took it and said no seq, as an older one does.
	tookNoSeq
)

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
// A prompt arrives twice: echoed locally when it is sent, then again as what
// the session actually holds, so the local echo this replaces (see
// appendPendingUser) is removed rather than left above a duplicate.
//
// The two texts are the same string for an ordinary prompt and differ when
// the transcript shows something the person did not type: a message that
// carried an image is displayed with a note saying so, while the echo it
// replaces has to be matched on what was actually typed.
//
// seq is the event's, for resolvePendingUser.
func (m *Model) appendUser(rawText, displayText string, seq uint64) {
	// A new prompt starts a new reply, whether or not the last one's end
	// ever came.
	m.streamOpen = false
	m.resolvePendingUser(rawText, seq)
	m.appendEntry(entryUser, displayText)
}

// appendSent draws a message handed to a turn that is already running.
//
// Its own kind rather than an ordinary local note, so a stop can find it
// again: the daemon drops the queue when a turn is cancelled, and a line
// still saying the model will pick this up at its next step is then a
// promise about a message nobody has.
func (m *Model) appendSent(text string) int {
	m.appendEntry(entrySent, text)
	return m.markSend()
}

// markSend numbers the echo just appended as the next send's.
func (m *Model) markSend() int {
	m.sends++
	m.transcript[len(m.transcript)-1].send = m.sends
	return m.sends
}

// appendPendingUser draws a prompt the instant Enter is pressed, dimmed,
// until the daemon's message.user event arrives with the real line.
//
// The wait in between is everything the daemon does before the model is
// handed the text — hooks, the delegation decision, the first request — and
// on a slow or remote model it is seconds. A screen that does not change in
// that time reads as an Enter that never registered, and the honest
// response to that is to type it again.
func (m *Model) appendPendingUser(text string) int {
	m.appendEntry(entryPending, text)
	send := m.markSend()
	// Called from the key handler, not applyEvent, so it refreshes itself.
	m.refreshViewport()
	return send
}

// resolvePendingUser drops the oldest echo of text, and reports whether it
// found one. Oldest first: the same prompt can be sent twice, and each send
// owns one echo.
//
// A line sent into a running turn is an echo too: "[sent — …] text"
// stands for the message until the model is given it, and then the
// message's own line is drawn. Left in place, it said "will pick this
// up" about a message already picked up, and a later stop or lost turn
// relabelled it as never seen.
//
// seq is the message's own, 0 when it has none. An echo whose send the
// daemon took when the log had already reached seq is not that message's:
// its own is written after it was taken. A backlog replayed on a
// reconnect brings such a message, another client's with the same words.
// An echo whose answer has not come yet cannot be told apart that way, so
// of two sends of the same words waiting for their answers, a message can
// resolve the one it is not.
func (m *Model) resolvePendingUser(text string, seq uint64) bool {
	for i, e := range m.transcript {
		switch {
		case e.kind == entryPending && e.text == text:
		case e.kind == entrySent && sentText(e.text) == text:
		default:
			continue
		}
		if seq > 0 && e.took == tookAt && e.at >= seq {
			continue
		}
		m.removeEntry(i)
		return true
	}
	return false
}

// resolveOwnEcho drops the echo of a send nothing took, found by the
// number it was given. Not by its text: the same line can be on screen
// twice, once for a send that was taken and is waiting for its turn to say
// so.
func (m *Model) resolveOwnEcho(send int) {
	if send == 0 {
		return
	}
	for i, e := range m.transcript {
		if e.send == send {
			m.removeEntry(i)
			return
		}
	}
}

func (m *Model) removeEntry(i int) {
	m.transcript = append(m.transcript[:i:i], m.transcript[i+1:]...)
	m.transcriptRev++
	if m.streamOpen && i < m.streamAt {
		m.streamAt--
	}
}

// abandonPendingUsers answers a stopped turn: a prompt still drawn as sent
// that was in the queue the daemon has just dropped was never handed to
// the model.
//
// stop is the seq of the stop's event, 0 for a turn lost with no event.
// The daemon writes a stop after it took every message the stop drops,
// and before it takes one for a turn that begins after it. So an echo
// whose send was taken at or after stop is not one it dropped: the daemon
// started the next turn for it, or handed it to that turn, and its
// message.user resolves the echo as usual. An echo whose send has no
// answer yet is held until the answer comes (noteReceipt). With no seq
// to go by, from either side, every echo is taken as dropped.
//
// Rewritten rather than removed. What is on screen is text somebody
// typed, and taking it away silently is the other half of the same
// fault — they would be left knowing neither that it was discarded nor
// what it said. The line keeps the words and stops claiming they went
// anywhere.
func (m *Model) abandonPendingUsers(stop uint64) {
	changed := false
	for i, e := range m.transcript {
		if e.kind != entryPending && e.kind != entrySent {
			continue
		}
		if stop > 0 {
			if e.took == tookAt && e.at >= stop {
				continue
			}
			if e.took == tookAwaited {
				if stop > e.heldBy {
					m.transcript[i].heldBy = stop
				}
				continue
			}
		}
		m.markNotSent(i)
		changed = true
	}
	if changed {
		m.transcriptRev++
	}
}

// markNotSent rewrites the echo at i as a prompt nobody was handed. The
// caller moves transcriptRev.
func (m *Model) markNotSent(i int) {
	e := m.transcript[i]
	m.transcript[i].kind = entryTool
	// An entrySent line already carries its own "[sent — ...]" prefix;
	// replacing it rather than prepending keeps the result a sentence
	// rather than two contradicting ones.
	text := e.text
	if _, rest, found := strings.Cut(text, "] "); found && e.kind == entrySent {
		text = rest
	}
	m.transcript[i].text = "[not sent — the turn was stopped before the model saw this] " + text
}

// noteReceipt records what the daemon said about a send whose echo is
// still drawn, and decides an echo a stop held while the answer was on
// its way: a send taken before that stop was in the queue it dropped.
func (m *Model) noteReceipt(send int, r client.Receipt) {
	if send == 0 {
		return
	}
	for i, e := range m.transcript {
		if e.send != send {
			continue
		}
		if e.kind != entryPending && e.kind != entrySent {
			return
		}
		took, at := tookNoSeq, uint64(0)
		if r.HasSeq {
			took, at = tookAt, r.Seq
		}
		m.transcript[i].took, m.transcript[i].at = took, at
		if e.heldBy > 0 && (took != tookAt || at < e.heldBy) {
			m.markNotSent(i)
			m.transcriptRev++
			m.refreshViewport()
		}
		return
	}
}

// appendLocal writes text straight into the transcript without going
// through the server — for /help and /version, which are answered purely
// client-side (well, /version does hit the daemon, but the answer isn't
// part of the session's event log either way).
func (m *Model) appendLocal(text string) {
	// A local reply (most commonly "[queued] ...", fired while a turn is
	// mid-stream) is its own entry below the reply, and the reply goes on
	// streaming above it — see appendModelDelta.
	m.appendEntry(entryLocal, text)
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
//
// The open entry is not always the last one. A tool line, or a line this
// client wrote, can sit below a reply that is still streaming, and the
// rest of the reply goes into the reply, not after them.
func (m *Model) appendModelDelta(text string) {
	if i := m.openReply(); i >= 0 {
		m.transcript[i].text += text
		m.transcriptRev++
		return
	}
	m.appendEntry(entryModel, text)
	m.streamOpen = true
	m.streamAt = len(m.transcript) - 1
}

// openReply is the index of the model message streaming now, or -1.
//
// Checked rather than trusted: an index into a slice that other code
// appends to, inserts into and deletes from is right only while every
// one of those keeps it right, and a stale one would write a reply over
// some other line. One that no longer names a model entry is treated as
// no reply at all, which at worst draws the rest of the reply as a new
// one.
func (m *Model) openReply() int {
	if !m.streamOpen || m.streamAt < 0 || m.streamAt >= len(m.transcript) || m.transcript[m.streamAt].kind != entryModel {
		return -1
	}
	return m.streamAt
}

// endModelStream closes one model message. text is the whole reply as the
// daemon recorded it, and it is authoritative: it is written over whatever
// the deltas drew.
//
// Closing without redrawing was the bug. Deltas can be missed — an SSE
// reconnect resumes from the last id the browser or the TUI saw, and a
// reconnect in the middle of a reply means the fragments sent while the
// connection was down are simply not replayed. What was drawn from the
// deltas that did arrive is then the reply with a hole in it, and since
// the entry was already open this closed it and kept the hole for the
// life of the process.
//
// The daemon has the whole reply and sends it here, so there is never a
// reason to prefer the fragments. When nothing was missed this writes
// back exactly what is already on screen.
//
// The other case is replay, where the daemon drops the fragments of
// replies that have already finished and sends only this — there the
// entry is not open and the text starts one. See collapseFinishedDeltas
// in the daemon.
func (m *Model) endModelStream(text string) {
	switch i := m.openReply(); {
	case text == "":
	case i >= 0:
		m.transcript[i].text = text
		m.transcriptRev++
	default:
		m.appendModelDelta(text)
	}
	m.streamOpen = false
}

// appendTool adds a server-driven status line — [delegated to x],
// [cancelled] — which, like appendLocal, is its own entry rather than an
// extension of an in-progress model entry.
func (m *Model) appendTool(text string) {
	m.appendEntry(entryTool, text)
}

// refreshViewport re-renders the transcript into the viewport at its
// current width. GotoBottom only fires when the viewport was already
// scrolled to the bottom before this update — otherwise every single
// streamed delta would yank the view back down out from under someone who
// scrolled up to reread something, which is exactly what happened when
// this unconditionally called GotoBottom on every event.
func (m *Model) refreshViewport() {
	full := m.termWidth
	if full <= 0 {
		full = m.viewport.Width()
		if full <= 0 {
			full = fallbackWidth
		}
	}
	w := full
	if m.renderCache == nil {
		// Lazily, because a Model built as a literal rather than by New
		// is a shape the tests use and there is no reason to make them
		// carry this.
		m.renderCache = &transcriptRenderCache{}
	}
	render := func(width int) {
		m.viewport.SetWidth(width)
		m.viewport.SetContent(m.renderCache.content(m.transcript, width))
	}
	atBottom := m.viewport.AtBottom()
	render(w)
	// The scrollbar stands in the transcript's last column rather than
	// over it, so a transcript that overflows gives up one column while
	// the switch is on. Decided at the full width every time, which is
	// what keeps it stable: narrowing can only lengthen the transcript,
	// so a scrollbar earned at full width is still earned one narrower.
	if m.mouseEnabled && full > 1 && m.viewport.Height() > 0 && m.viewport.TotalLineCount() > m.viewport.Height() {
		w = full - 1
		render(w)
	}
	if atBottom {
		m.viewport.GotoBottom()
	}
}

// sentText is the message an entrySent line stands for, less its
// "[sent — …] " prefix.
func sentText(line string) string {
	if _, rest, found := strings.Cut(line, "] "); found {
		return rest
	}
	return line
}
