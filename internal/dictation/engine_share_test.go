package dictation

import "testing"

// The shared engine is keyed on what it was started with, and the thread
// count it is started with is not always the one asked for: startWhisper
// turns anything <= 0 into defaultThreads(). Keyed on the raw value,
// Threads:0 and Threads:N on an N-thread machine described the same server
// under two names and took turns killing each other.
func TestThreadsNormalizeToTheSameEngineKey(t *testing.T) {
	if got, want := normalizeThreads(0), defaultThreads(); got != want {
		t.Errorf("normalizeThreads(0) = %d, want %d", got, want)
	}
	if got := normalizeThreads(-4); got != defaultThreads() {
		t.Errorf("normalizeThreads(-4) = %d, want the default", got)
	}
	if got := normalizeThreads(3); got != 3 {
		t.Errorf("normalizeThreads(3) = %d, want it left alone", got)
	}
}

// A dictation is one person at one microphone. Without a cap, a loop of
// POST /api/dictation opened recognizers faster than the reaper (every
// 60s, only for sessions idle 120s) could take them away, and the daemon
// has no authentication to make that hard.
func TestManagerRefusesTooManySessions(t *testing.T) {
	m := NewManager(Config{})
	m.mu.Lock()
	for i := range maxSessions {
		m.sessions[string(rune('a'+i))] = NewSession(&fakeRecognizer{})
	}
	m.mu.Unlock()

	if _, err := m.Start(); err == nil {
		t.Fatal("opened a recognizer past the cap")
	}
}
