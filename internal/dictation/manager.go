package dictation

import (
	"fmt"
	"sync"
	"time"
)

// idleTimeout is how long a dictation session may go without audio
// before it is reaped. A browser tab closed with the microphone on never
// sends a stop, and each live session holds a recognizer — and its
// model — open. Comfortably longer than any pause in speech, short
// enough that an abandoned session doesn't outlive the person's memory
// of opening it.
const idleTimeout = 2 * time.Minute

// Manager owns the live dictation sessions and the model configuration
// they are opened with.
//
// It deliberately does not hold a single shared recognizer: a streaming
// recognizer carries the state of one utterance in progress, so two
// clients dictating at once through one instance would interleave their
// words. One per session, opened on demand.
type Manager struct {
	cfg Config

	mu       sync.Mutex
	sessions map[string]*Session
	nextID   int
	stop     chan struct{}
	// closed is set by Close. Without it, a Start already past its
	// readiness check could insert a session — and start an engine —
	// after Shutdown had torn one down, leaving exactly the orphaned
	// whisper-server that Shutdown exists to prevent.
	closed bool
}

func NewManager(cfg Config) *Manager {
	return &Manager{cfg: cfg, sessions: map[string]*Session{}}
}

// SetConfig replaces the settings new dictations will use.
//
// Sessions already running keep the recognizer they opened with: a
// recognizer holds an utterance in progress, and swapping the engine
// under one mid-sentence would lose it for no gain — the next dictation
// is a click away.
func (m *Manager) SetConfig(cfg Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg = cfg
}

// Config returns the settings in force, so a caller can change one field
// without having to reconstruct the rest.
func (m *Manager) Config() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

// Ready reports whether a dictation could actually start right now, and
// why not when it couldn't. Clients use it to decide between offering a
// microphone button, hiding it, and explaining — a button that can only
// fail is worse than no button.
func (m *Manager) Ready() (bool, string) {
	if _, err := m.Config().resolveEngine(); err != nil {
		return false, err.Error()
	}
	return true, ""
}

// maxSessions caps how many dictations can be open at once.
//
// Each one holds a recognizer, and the daemon has no authentication, so
// a loop of POST /api/dictation opened them faster than the reaper (every
// 60s, and only for sessions already idle for 120s) could take them away.
// Well above any real use: a dictation is one person talking into one
// microphone, and even a shared daemon has a handful of clients.
const maxSessions = 16

// Start opens a recognizer and returns the id audio should be posted to.
func (m *Manager) Start() (string, error) {
	// Checked before opening anything, since opening is the expensive
	// part this is here to bound.
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return "", fmt.Errorf("dictation is shutting down")
	}
	if len(m.sessions) >= maxSessions {
		m.mu.Unlock()
		return "", fmt.Errorf("too many dictation sessions open (%d); stop one before starting another", maxSessions)
	}
	m.mu.Unlock()

	rec, err := Open(m.Config())
	if err != nil {
		return "", err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// Re-checked under the lock: the open above is slow enough for a
	// burst of requests to pass the first check together, and for Close
	// to have happened in the meantime.
	if m.closed {
		rec.Close()
		return "", fmt.Errorf("dictation is shutting down")
	}
	if len(m.sessions) >= maxSessions {
		rec.Close()
		return "", fmt.Errorf("too many dictation sessions open (%d); stop one before starting another", maxSessions)
	}
	m.nextID++
	id := fmt.Sprintf("d-%d", m.nextID)
	m.sessions[id] = NewSession(rec)
	return id, nil
}

// Get returns a live session by id.
func (m *Manager) Get(id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, fmt.Errorf("no dictation session %q (it may have been idle too long and been closed)", id)
	}
	return s, nil
}

// Stop ends a session and returns whatever was still in progress.
func (m *Manager) Stop(id string) (Result, error) {
	m.mu.Lock()
	s, ok := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if !ok {
		return Result{}, fmt.Errorf("no dictation session %q", id)
	}
	return s.Stop(), nil
}

// StartReaper closes sessions that stopped receiving audio. Runs until
// Close.
func (m *Manager) StartReaper() {
	m.mu.Lock()
	if m.stop != nil {
		m.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	m.stop = stop
	m.mu.Unlock()

	go func() {
		t := time.NewTicker(idleTimeout / 2)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				m.reap()
			}
		}
	}()
}

func (m *Manager) reap() {
	m.mu.Lock()
	var dead []*Session
	for id, s := range m.sessions {
		if s.Idle() > idleTimeout {
			dead = append(dead, s)
			delete(m.sessions, id)
		}
	}
	m.mu.Unlock()
	// Stopped outside the lock: Stop closes a recognizer, which for the
	// real one is a CGo call into model teardown, and holding the manager
	// lock through that would block every other client's audio.
	for _, s := range dead {
		s.Stop()
	}
}

// Close stops the reaper and every live session.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	if m.stop != nil {
		close(m.stop)
		m.stop = nil
	}
	var all []*Session
	for id, s := range m.sessions {
		all = append(all, s)
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	for _, s := range all {
		s.Stop()
	}
	// And the engine, which outlives individual sessions on purpose but
	// must not outlive the program. See Shutdown.
	Shutdown()
}
