// Package session implements the session store: session metadata plus each
// session's append-only event log, with pub/sub for live consumers (TUI,
// web) and parent/child links for background tasks.
package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"localcode/internal/events"
)

type Session struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"`
	Visible  bool   `json:"visible"`
	Agent    string `json:"agent,omitempty"`
	Title    string `json:"title,omitempty"`
	// Workspace is the directory this session is working in. The daemon has
	// one live workspace at a time (see daemon.handleSetWorkspace), so this
	// is not an independent per-session setting — it's this session's
	// record of where it currently is, which is what lets a session list
	// distinguish two sessions that otherwise look identical because they
	// are in different projects, and what selecting a session restores the
	// workspace to. It is set at creation and moved by SetWorkspace
	// whenever the workspace changes while this session is the open one.
	// Empty for sessions created before this field existed.
	Workspace string    `json:"workspace,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type subscriber struct {
	ch chan events.Event
	// lost is closed the first time an event cannot be handed to this
	// subscriber because its buffer is full.
	//
	// A dropped event used to be the end of the story: the writer moved
	// on, and the reader was never told. Every event is one token of
	// model output, so a client that falls behind for a moment loses the
	// middle of a reply — and then loses turn.done as well, which is what
	// clears the spinner. What that looks like is a message that stops
	// halfway and a turn that never ends.
	//
	// Closing this is how the reader finds out. It ends the stream, and
	// the client reconnects with Last-Event-ID and replays what it missed
	// from the log, which is a machinery that already exists.
	lost     chan struct{}
	lostOnce sync.Once
}

// markLost reports that this subscriber missed an event. Safe to call
// from any number of writers; only the first does anything.
func (s *subscriber) markLost() {
	s.lostOnce.Do(func() { close(s.lost) })
}

type sessionState struct {
	meta      Session
	log       []events.Event
	nextSeq   uint64
	subs      map[int]*subscriber
	nextSubID int
	file      *os.File // nil if not persisted
}

// Store holds all sessions in memory, optionally persisting each session's
// event log to <dir>/<sessionID>.jsonl for crash recovery.
type Store struct {
	mu       sync.Mutex
	sessions map[string]*sessionState
	dir      string // empty = no persistence
}

func NewStore(persistDir string) (*Store, error) {
	if persistDir != "" {
		if err := os.MkdirAll(persistDir, 0o755); err != nil {
			return nil, fmt.Errorf("create session dir: %w", err)
		}
	}
	return &Store{
		sessions: map[string]*sessionState{},
		dir:      persistDir,
	}, nil
}

// CreateSession creates a new session with no recorded workspace. parentID
// is empty for a top-level (user-facing) session, or set when this is a
// background task spawned by another session.
func (s *Store) CreateSession(id, parentID, agent string, visible bool) (*Session, error) {
	return s.CreateSessionIn(id, parentID, agent, "", visible)
}

// CreateSessionIn is CreateSession plus the workspace directory to stamp on
// the session — see Session.Workspace. The daemon uses this so a session
// list can show where each conversation was started.
func (s *Store) CreateSessionIn(id, parentID, agent, workspace string, visible bool) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.sessions[id]; exists {
		return nil, fmt.Errorf("session %s already exists", id)
	}

	meta := Session{
		ID:        id,
		ParentID:  parentID,
		Visible:   visible,
		Agent:     agent,
		Workspace: workspace,
		CreatedAt: time.Now().UTC(),
	}

	st := &sessionState{
		meta: meta,
		subs: map[int]*subscriber{},
	}

	if s.dir != "" {
		f, err := os.OpenFile(filepath.Join(s.dir, id+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, fmt.Errorf("open session log: %w", err)
		}
		st.file = f
		if err := writeSessionMeta(s.dir, meta); err != nil {
			_ = f.Close()
			return nil, err
		}
	}

	s.sessions[id] = st
	metaCopy := meta
	return &metaCopy, nil
}

// writeSessionMeta persists Session metadata (everything Append's jsonl
// event log doesn't capture — Agent/Title/Visible/ParentID/CreatedAt) to
// <dir>/<id>.meta.json, so a restart can reconstruct the session list and
// its per-session settings, not just replay the event log. Rewritten
// wholesale on every metadata change (CreateSession/SetAgent/SetTitle);
// small enough that this is simpler and safer than patching in place.
func writeSessionMeta(dir string, meta Session) error {
	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal session meta: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, meta.ID+".meta.json"), data, 0o644); err != nil {
		return fmt.Errorf("write session meta: %w", err)
	}
	return nil
}

func (s *Store) Get(id string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.sessions[id]
	if !ok {
		return nil, fmt.Errorf("session %s not found", id)
	}
	metaCopy := st.meta
	return &metaCopy, nil
}

// SetAgent changes which agent a session sends future messages as —
// e.g. switching a session from "plan" to "build" mid-conversation.
// Message history is untouched; only the agent used for the *next*
// SendMessage call changes, since callers re-read Session.Agent fresh on
// every send rather than caching it.
func (s *Store) SetAgent(sessionID, agent string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	st.meta.Agent = agent
	metaCopy := st.meta
	if s.dir != "" {
		if err := writeSessionMeta(s.dir, metaCopy); err != nil {
			return nil, err
		}
	}
	return &metaCopy, nil
}

// SetTitle renames a session — purely cosmetic (a user-facing label for
// the session picker), doesn't affect resolution or resumption, both of
// which are always by ID.
func (s *Store) SetTitle(sessionID, title string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	st.meta.Title = title
	metaCopy := st.meta
	if s.dir != "" {
		if err := writeSessionMeta(s.dir, metaCopy); err != nil {
			return nil, err
		}
	}
	return &metaCopy, nil
}

// SetWorkspace records dir as the session's current workspace. The daemon
// calls this when the workspace changes while this session is the open
// one, so the session list keeps naming where the conversation actually
// is, and so selecting the session later restores that directory instead
// of the one it happened to be created in.
func (s *Store) SetWorkspace(sessionID, dir string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	st.meta.Workspace = dir
	metaCopy := st.meta
	if s.dir != "" {
		if err := writeSessionMeta(s.dir, metaCopy); err != nil {
			return nil, err
		}
	}
	return &metaCopy, nil
}

// Delete removes a session from the store and, if persisted, deletes its
// on-disk JSONL log. It does not cascade to child sessions (background
// tasks spawned from it) — those are simply left as orphaned, invisible
// entries (Visible:false already keeps them out of any session list).
func (s *Store) Delete(sessionID string) error {
	s.mu.Lock()
	st, ok := s.sessions[sessionID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("session %s not found", sessionID)
	}
	delete(s.sessions, sessionID)
	dir := s.dir
	if st.file != nil {
		_ = st.file.Close()
	}
	// Everyone still reading this session is told, the same way a
	// subscriber that falls behind is told. Otherwise they sat on a
	// stream that would never produce another event and never end,
	// holding the connection until the client itself gave up.
	for _, sub := range st.subs {
		sub.markLost()
	}
	st.subs = map[int]*subscriber{}
	s.mu.Unlock()

	if dir != "" {
		if err := os.Remove(filepath.Join(dir, sessionID+".jsonl")); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove session log: %w", err)
		}
		if err := os.Remove(filepath.Join(dir, sessionID+".meta.json")); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove session meta: %w", err)
		}
	}
	return nil
}

// DeleteAll removes every session in the store — visible sessions and
// background-task children alike — and their persisted files, if any.
// Unlike Delete, callers that need to refuse this while some session has
// a turn in-flight (see daemon.handleDeleteAllSessions) must check that
// themselves first; DeleteAll itself has no such guard.
func (s *Store) DeleteAll() error {
	s.mu.Lock()
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	s.mu.Unlock()

	for _, id := range ids {
		if err := s.Delete(id); err != nil {
			return fmt.Errorf("delete session %s: %w", id, err)
		}
	}
	return nil
}

// ListVisible returns all top-level (visible:true) sessions — i.e. the
// ones a user picks from when resuming, not background tasks — newest
// first.
func (s *Store) ListVisible() []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Session
	for _, st := range s.sessions {
		if st.meta.Visible {
			out = append(out, st.meta)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

// AllSessions returns every session regardless of Visible — unlike
// ListVisible, this also includes background-task child sessions. Used by
// callers that need to rehydrate every session's in-memory state (e.g.
// agent.Loop's conversation history) after a restart, not just the ones a
// user would pick from.
func (s *Store) AllSessions() []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Session, 0, len(s.sessions))
	for _, st := range s.sessions {
		out = append(out, st.meta)
	}
	return out
}

// Children returns sessions spawned by parentID (background tasks).
func (s *Store) Children(parentID string) []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Session
	for _, st := range s.sessions {
		if st.meta.ParentID == parentID {
			out = append(out, st.meta)
		}
	}
	return out
}

// Append adds an event to the session's log, persists it if configured, and
// fans it out to live subscribers. Returns the stored event with its seq.
func (s *Store) Append(sessionID string, typ events.Type, data map[string]any) (events.Event, error) {
	s.mu.Lock()
	st, ok := s.sessions[sessionID]
	if !ok {
		s.mu.Unlock()
		return events.Event{}, fmt.Errorf("session %s not found", sessionID)
	}

	st.nextSeq++
	ev := events.Event{
		Seq:       st.nextSeq,
		Session:   sessionID,
		Type:      typ,
		Timestamp: time.Now().UTC(),
		Data:      data,
	}
	st.log = append(st.log, ev)

	// Delivered while still holding the lock. The sends are
	// non-blocking, so this cannot stall behind a slow reader, and it
	// removes the window in which unsubscribe could close a channel
	// between the snapshot and the send — a send on a closed channel is a
	// panic, and this one would take the daemon down.
	for _, sub := range st.subs {
		select {
		case sub.ch <- ev:
		default:
			// The reader is behind. Still do not block the writer — a
			// stalled browser tab must not hold up the model — but do not
			// pretend nothing happened either: this event is gone for
			// this subscriber, and every event carries meaning it cannot
			// reconstruct. Telling it costs one reconnect and a replay
			// from the log; not telling it costs the rest of the turn.
			sub.markLost()
		}
	}
	// Written under the same lock that handed out the seq, which is what
	// makes the file's order the log's order.
	//
	// Outside it, two appends could take seq N and N+1 and then reach the
	// file in the opposite order — routine rather than exotic: a
	// background task writes task.status to its parent while that parent's
	// turn is streaming deltas. Measured at ~55 inverted lines per 200
	// concurrent appends. Restoring reads the file in file order, so the
	// in-memory log came back out of sequence and every seq-based lookup
	// after it — `since=`, Last-Event-ID resume, the tail cut — was
	// working against an unsorted list.
	//
	// It also closes the window where Delete could close the file between
	// the unlock and the write, since Delete takes this same lock.
	//
	// The cost is a small buffered write inside the lock. The fanout to
	// subscribers above already runs here, and nothing in this function
	// waits on anything but memory and one Write syscall.
	if st.file != nil {
		if line, err := json.Marshal(ev); err == nil {
			_, _ = st.file.Write(append(line, '\n'))
		}
	}
	s.mu.Unlock()

	return ev, nil
}

// Broadcast fans an event out to a session's live subscribers without
// recording it: no seq, no place in the log, nothing written to disk.
//
// For things that are only true right now — the live tokens-per-second
// readout is the case this exists for. Such an event fires once a second
// during a long generation, and appending it would grow the log (and the
// file behind it) by hundreds of entries that say nothing about what was
// said, and would replay as a burst of stale numbers on reconnect.
//
// Seq stays 0, which is how the SSE layer knows not to give it an `id:`
// line: a transient event must never become the Last-Event-ID a browser
// resumes the real log from.
func (s *Store) Broadcast(sessionID string, typ events.Type, data map[string]any) {
	s.mu.Lock()
	st, ok := s.sessions[sessionID]
	if !ok {
		s.mu.Unlock()
		return
	}
	ev := events.Event{
		Session:   sessionID,
		Type:      typ,
		Timestamp: time.Now().UTC(),
		Data:      data,
	}
	for _, sub := range st.subs {
		select {
		case sub.ch <- ev:
		default:
			// Dropping a transient event costs nothing and must NOT count
			// as falling behind: another one is along in a second, it
			// carries no history, and tearing down a working stream over
			// a missed tokens-per-second reading would be absurd.
		}
	}
	s.mu.Unlock()
}

// Events returns all events with seq > since, for catch-up on
// (re)connection.
func (s *Store) Events(sessionID string, since uint64) ([]events.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	var out []events.Event
	for _, ev := range st.log {
		if ev.Seq > since {
			out = append(out, ev)
		}
	}
	return out, nil
}

// Subscribe returns a channel of live events, a channel closed if this
// subscriber ever misses one, and an unsubscribe func.
//
// Callers should first call Events(since) to catch up, then Subscribe to
// avoid missing events in the gap.
//
// The middle return value is the part that has to be honoured. Delivery
// is best effort — a reader that stops reading must never stall the
// model — so an event can be dropped, and a dropped event is not
// recoverable from the stream itself. When that channel closes, the only
// correct response is to stop trusting this stream: drop it, and catch
// up with Events(since) on a fresh one. Ignoring it means showing a
// conversation with a hole in it, and quite possibly missing the event
// that says the turn ended.
func (s *Store) Subscribe(sessionID string) (<-chan events.Event, <-chan struct{}, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.sessions[sessionID]
	if !ok {
		return nil, nil, nil, fmt.Errorf("session %s not found", sessionID)
	}

	id := st.nextSubID
	st.nextSubID++
	// A generous buffer, because every token of model output is one event
	// and 64 of them is well under a second of a local model talking. The
	// lost signal below is the correctness guarantee; this is what keeps
	// it from firing during ordinary use.
	sub := &subscriber{ch: make(chan events.Event, 4096), lost: make(chan struct{})}
	st.subs[id] = sub

	unsub := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(st.subs, id)
		close(sub.ch)
	}
	return sub.ch, sub.lost, unsub, nil
}

// LoadAllFromDisk restores every session found in dir (one <id>.meta.json +
// <id>.jsonl pair each) into a fresh, persisting Store — e.g. at daemon
// startup, so a restart doesn't wipe the session list the way a bare
// NewStore(dir) would. A directory with no sessions yet (or that doesn't
// exist) just yields an empty, working Store, same as NewStore.
//
// A <id>.jsonl with no matching <id>.meta.json (a log from before this
// sidecar file existed, or one that failed to write) is skipped with a
// warning appended to the returned slice rather than failing the whole
// restore — one corrupt session shouldn't take every other session down
// with it.
func LoadAllFromDisk(dir string) (*Store, []error, error) {
	s, err := NewStore(dir)
	if err != nil {
		return nil, nil, err
	}
	if dir == "" {
		return s, nil, nil
	}

	metaFiles, err := filepath.Glob(filepath.Join(dir, "*.meta.json"))
	if err != nil {
		return nil, nil, fmt.Errorf("glob session metadata: %w", err)
	}

	var warnings []error
	for _, metaPath := range metaFiles {
		id := strings.TrimSuffix(filepath.Base(metaPath), ".meta.json")
		if err := s.restoreOne(dir, id); err != nil {
			warnings = append(warnings, fmt.Errorf("session %s: %w", id, err))
		}
	}
	return s, warnings, nil
}

// restoreOne loads one session's metadata + event log into s, opening its
// jsonl file in append mode so future Append calls continue the same file.
func (s *Store) restoreOne(dir, id string) error {
	metaData, err := os.ReadFile(filepath.Join(dir, id+".meta.json"))
	if err != nil {
		return fmt.Errorf("read meta: %w", err)
	}
	var meta Session
	if err := json.Unmarshal(metaData, &meta); err != nil {
		return fmt.Errorf("parse meta: %w", err)
	}
	if meta.ID == "" {
		meta.ID = id
	}

	f, err := os.OpenFile(filepath.Join(dir, id+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open session log: %w", err)
	}

	st := &sessionState{
		meta: meta,
		subs: map[int]*subscriber{},
		file: f,
	}

	if logData, err := os.ReadFile(filepath.Join(dir, id+".jsonl")); err == nil {
		// Read with a Reader, not a Scanner. A Scanner has a maximum line
		// length and nothing here truncates tool output, so one `cat` of a
		// large file writes a line past any cap that could be chosen — and
		// a Scanner reports that by stopping, which is indistinguishable
		// from reaching the end of the file.
		//
		// The consequence was not a truncated transcript but a corrupted
		// log. Every later event was dropped, nextSeq was restored below
		// the highest seq actually in the file, and appends after the
		// restart handed out numbers the file already contained. Two
		// events with one seq breaks `since=` replay and Last-Event-ID
		// resume for that session permanently, and nothing reported a
		// problem: restore returned no error at all.
		r := bufio.NewReader(bytes.NewReader(logData))
		for {
			line, err := r.ReadBytes('\n')
			if len(line) > 0 {
				var ev events.Event
				// A line that does not parse is skipped, not fatal: the
				// last line of a log whose write was interrupted is a
				// partial one, and losing it is right — losing everything
				// after it is not.
				if json.Unmarshal(bytes.TrimSpace(line), &ev) == nil {
					st.log = append(st.log, ev)
					if ev.Seq > st.nextSeq {
						st.nextSeq = ev.Seq
					}
				}
			}
			if err != nil {
				break // io.EOF, or a final line with no newline
			}
		}
	} else if !os.IsNotExist(err) {
		_ = f.Close()
		return fmt.Errorf("read session log: %w", err)
	}

	s.mu.Lock()
	s.sessions[id] = st
	s.mu.Unlock()
	return nil
}

// TailSince returns the seq to replay from so a client sees roughly the
// last n events, starting at a turn boundary.
//
// Opening a long conversation used to replay every event in it. The
// daemon is not what makes that slow — 7,680 events left it in 47ms —
// but the client then has to build the whole transcript, which measured
// at 751ms in a headless DOM and is worse in a real one, where every
// growing markdown re-render costs a reparse and a relayout. Since none
// of that history is on screen when the panel opens, the cheapest way to
// show the end of a conversation is not to send the beginning.
//
// The boundary matters as much as the count. Cutting at an arbitrary
// event can land between a tool call and its result, or part-way through
// a reply, and the transcript then opens on a fragment with a spinner
// that never resolves. So the cut is moved back to the most recent user
// message at or before it: the tail begins where someone asked
// something, which is where a conversation reads from.
//
// Bounded at 2n, because one turn can be longer than the whole window
// and "start at a turn boundary" must not become "send everything".
//
// Returns 0 when the log is shorter than n — there is nothing to trim,
// and 0 is what "from the beginning" already means everywhere else.
func (s *Store) TailSince(sessionID string, n int) (uint64, error) {
	if n <= 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.sessions[sessionID]
	if !ok {
		return 0, fmt.Errorf("session %s not found", sessionID)
	}
	if len(st.log) <= n {
		return 0, nil
	}

	start := len(st.log) - n
	limit := len(st.log) - 2*n
	if limit < 0 {
		limit = 0
	}
	for i := start; i >= limit; i-- {
		if st.log[i].Type == events.TypeUserMessage {
			start = i
			break
		}
	}
	if start == 0 {
		return 0, nil
	}
	// The seq *before* the first event to send: Events(since) is
	// exclusive, and this is fed straight to it.
	return st.log[start-1].Seq, nil
}
