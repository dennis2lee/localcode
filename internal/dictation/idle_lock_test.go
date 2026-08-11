package dictation

import (
	"testing"
	"time"
)

// slowFinalizer commits an utterance by blocking, the way a real engine
// does when the whisper server it talks to has stopped answering: the
// call has a 60s timeout, and until it returns the session lock is held.
type slowFinalizer struct {
	fakeRecognizer
	release chan struct{}
}

func (s *slowFinalizer) Final() string {
	<-s.release
	return "committed"
}

// The reaper reads Idle for every session while holding the manager lock,
// and the manager lock is what Start, Get and Stop need. So if Idle waited
// on the session lock, one wedged engine would take dictation down for
// every client: no new dictation, no audio accepted anywhere, and no way
// to switch the microphone off.
func TestIdleDoesNotWaitOnACommitInProgress(t *testing.T) {
	rec := &slowFinalizer{release: make(chan struct{})}
	rec.endpointNow = true
	s := NewSession(rec)

	committing := make(chan struct{})
	go func() {
		close(committing)
		s.Write(pcm(1, 2, 3, 4)) // blocks in Final, holding the session lock
	}()
	<-committing

	done := make(chan time.Duration, 1)
	go func() { done <- s.Idle() }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Idle blocked behind a commit; one stuck engine freezes every dictation session")
	}
	close(rec.release)
}

// And the reaper itself keeps working, which is the part that matters:
// it runs under the manager lock.
func TestReaperIsNotBlockedByACommitInProgress(t *testing.T) {
	m := NewManager(Config{})
	rec := &slowFinalizer{release: make(chan struct{})}
	rec.endpointNow = true

	m.mu.Lock()
	m.sessions["d-1"] = NewSession(rec)
	stuck := m.sessions["d-1"]
	m.mu.Unlock()

	committing := make(chan struct{})
	go func() {
		close(committing)
		stuck.Write(pcm(1, 2, 3, 4))
	}()
	<-committing

	done := make(chan struct{})
	go func() {
		m.reap()
		// Get takes the manager lock the reaper holds while reading Idle.
		m.Get("d-1")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the manager lock is held across a stuck commit")
	}
	close(rec.release)
}
