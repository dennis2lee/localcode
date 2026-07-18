// Package session implements the session store: session metadata plus each
// session's append-only event log, with pub/sub for live consumers (TUI,
// web) and parent/child links for background tasks.
package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	}

	s.sessions[id] = st
	metaCopy := meta
	return &metaCopy, nil
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

// LoadFromDisk replays a persisted session log back into memory, e.g. after
// a daemon restart. It re-establishes the session metadata and log but not
// live subscribers.
func LoadFromDisk(dir, id, parentID, agent string, visible bool) (*Store, error) {
	s, err := NewStore(dir)
	if err != nil {
		return nil, err
	}
	if _, err := s.CreateSession(id, parentID, agent, visible); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, id+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	st := s.sessions[id]
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
	return s, scanner.Err()
}
