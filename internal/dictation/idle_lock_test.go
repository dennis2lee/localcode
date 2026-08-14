package dictation

import (
	"context"
	"sync"
	"testing"
	"time"
)

// slowFinalizer commits an utterance by blocking, the way a real engine
// does when the whisper server it talks to has stopped answering: the
// call has a 60s timeout, and until it returns the session lock is held.
type slowFinalizer struct {
	fakeRecognizer
	release   chan struct{}
	cancelled chan struct{}
	once      sync.Once
}

func newSlowFinalizer() *slowFinalizer {
	return &slowFinalizer{release: make(chan struct{}), cancelled: make(chan struct{})}
}

// Cancel is what a real recognizer does when the microphone is switched
// off: give up on the request in flight rather than sit out its timeout.
func (s *slowFinalizer) Cancel() {
	s.once.Do(func() { close(s.cancelled) })
}

func (s *slowFinalizer) Final(ctx context.Context) string {
	select {
	case <-s.release:
		return "committed"
	case <-s.cancelled:
		return ""
	case <-ctx.Done():
		return ""
	}
}

// The reaper reads Idle for every session while holding the manager lock,
// and the manager lock is what Start, Get and Stop need. So if Idle waited
// on the session lock, one wedged engine would take dictation down for
// every client: no new dictation, no audio accepted anywhere, and no way
// to switch the microphone off.
func TestIdleDoesNotWaitOnACommitInProgress(t *testing.T) {
	rec := newSlowFinalizer()
	rec.endpointNow = true
	s := NewSession(rec)

	committing := make(chan struct{})
	go func() {
		close(committing)
		s.Write(context.Background(), pcm(1, 2, 3, 4)) // blocks in Final, holding the session lock
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
	rec := newSlowFinalizer()
	rec.endpointNow = true

	m.mu.Lock()
	m.sessions["d-1"] = NewSession(rec)
	stuck := m.sessions["d-1"]
	m.mu.Unlock()

	committing := make(chan struct{})
	go func() {
		close(committing)
		stuck.Write(context.Background(), pcm(1, 2, 3, 4))
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

// Switching the microphone off has to work while a commit is stuck.
//
// This is the reported failure with a speech server that accepts the
// connection and never answers: Write holds the session lock across the
// engine call, Stop wanted the same lock, and so the stop queued behind a
// request that would not land for its whole timeout. Nothing appeared,
// nothing failed, and the button did nothing — dictation was hung.
func TestStopDoesNotWaitForAWedgedCommit(t *testing.T) {
	rec := newSlowFinalizer()
	rec.endpointNow = true
	s := NewSession(rec)

	committing := make(chan struct{})
	go func() {
		close(committing)
		s.Write(context.Background(), pcm(1, 2, 3, 4)) // blocks in Final, holding the session lock
	}()
	<-committing

	stopped := make(chan struct{})
	go func() {
		s.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop queued behind a commit that will never finish; the microphone cannot be switched off")
	}
	close(rec.release)
}

// An abandoned request takes its work with it: a browser that has given up
// on a chunk must not leave the session's lock held for the rest of the
// engine's timeout, or the next chunk and the stop both queue behind it.
func TestAnAbandonedRequestDoesNotHoldTheSession(t *testing.T) {
	rec := newSlowFinalizer()
	rec.endpointNow = true
	s := NewSession(rec)

	ctx, cancel := context.WithCancel(context.Background())
	writing := make(chan struct{})
	go func() {
		close(writing)
		s.Write(ctx, pcm(1, 2, 3, 4))
	}()
	<-writing
	cancel()

	done := make(chan struct{})
	go func() {
		s.Write(context.Background(), pcm(5, 6))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a request the client gave up on still holds the session lock")
	}
	close(rec.release)
}

// slowAsyncFinalizer is a recognizer that hands its audio over — the shape
// the whisper one has — and takes a long time to transcribe it.
type slowAsyncFinalizer struct {
	fakeRecognizer
	release chan struct{}
	took    chan struct{}
}

func (s *slowAsyncFinalizer) TakeUtterance() []float32 {
	select {
	case s.took <- struct{}{}:
	default:
	}
	return []float32{0.1, 0.2}
}

func (s *slowAsyncFinalizer) Transcribe(ctx context.Context, window []float32) string {
	select {
	case <-s.release:
		return "committed"
	case <-ctx.Done():
		return ""
	}
}

// The request that delivers audio must not wait for the engine.
//
// It used to: committing an utterance was a blocking call inside that
// request, so the engine's time was time the browser sat on an open POST
// with this session's lock held. On a speech server on another machine
// that is however long the other machine takes, and every mechanism built
// on top of it — the client's deadline, the lock, the queue — turned the
// delay into a failure.
func TestCommittingAnUtteranceDoesNotBlockTheAudioRequest(t *testing.T) {
	rec := &slowAsyncFinalizer{release: make(chan struct{}), took: make(chan struct{}, 1)}
	rec.endpointNow = true
	s := NewSession(rec)

	done := make(chan Result, 1)
	go func() {
		res, _ := s.Write(context.Background(), pcm(1, 2, 3, 4))
		done <- res
	}()

	select {
	case res := <-done:
		if res.Final != "" {
			t.Errorf("Final = %q, want the sentence to arrive later rather than hold the request", res.Final)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the audio request waited for the engine")
	}

	// And the sentence is delivered with a later chunk, not lost.
	close(rec.release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		res, err := s.Write(context.Background(), pcm(5, 6))
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
		if res.Final == "committed" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the committed sentence never arrived")
}
