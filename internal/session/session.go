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
	ID        string    `json:"id"`
	ParentID  string    `json:"parent_id,omitempty"`
	Visible   bool      `json:"visible"`
	Agent     string    `json:"agent,omitempty"`
	Title     string    `json:"title,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type subscriber struct {
	ch chan events.Event
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

// CreateSession creates a new session. parentID is empty for a top-level
// (user-facing) session, or set when this is a background task spawned by
// another session.
func (s *Store) CreateSession(id, parentID, agent string, visible bool) (*Session, error) {
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

	subs := make([]*subscriber, 0, len(st.subs))
	for _, sub := range st.subs {
		subs = append(subs, sub)
	}
	file := st.file
	s.mu.Unlock()

	if file != nil {
		line, err := json.Marshal(ev)
		if err == nil {
			_, _ = file.Write(append(line, '\n'))
		}
	}

	for _, sub := range subs {
		select {
		case sub.ch <- ev:
		default:
			// Slow consumer: drop rather than block the writer. Consumers
			// that need a gapless log should replay via Events(since=...).
		}
	}

	return ev, nil
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

// Subscribe returns a channel of live events plus an unsubscribe func.
// Callers should first call Events(since) to catch up, then Subscribe to
// avoid missing events in the gap (a small race remains for MVP; the
// channel buffer + since-based catch-up on reconnect covers reconnect
// scenarios in practice).
func (s *Store) Subscribe(sessionID string) (<-chan events.Event, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.sessions[sessionID]
	if !ok {
		return nil, nil, fmt.Errorf("session %s not found", sessionID)
	}

	id := st.nextSubID
	st.nextSubID++
	sub := &subscriber{ch: make(chan events.Event, 64)}
	st.subs[id] = sub

	unsub := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(st.subs, id)
		close(sub.ch)
	}
	return sub.ch, unsub, nil
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
		scanner := bufio.NewScanner(bytes.NewReader(logData))
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			var ev events.Event
			if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
				continue
			}
			st.log = append(st.log, ev)
			if ev.Seq > st.nextSeq {
				st.nextSeq = ev.Seq
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
