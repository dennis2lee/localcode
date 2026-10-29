package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"localcode/internal/events"
)

// reloadStore is a persisting store holding S1 with five events.
func reloadStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if _, err := s.CreateSession("S1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if _, err := s.Append("S1", events.TypeUserMessage, map[string]any{"text": "before"}); err != nil {
			t.Fatal(err)
		}
	}
	return s, dir
}

// reloading re-reads S1 until stop closes, and reports how many re-reads
// went in. Raced is the answer for a session that goes on changing while
// it is read; any other error fails the test.
func reloading(t *testing.T, s *Store, stop <-chan struct{}) <-chan int {
	t.Helper()
	swapped := make(chan int, 1)
	go func() {
		n := 0
		defer func() { swapped <- n }()
		for {
			select {
			case <-stop:
				return
			default:
			}
			err := s.Reload("S1")
			if err == nil {
				n++
			} else if !errors.Is(err, ErrReloadRaced) {
				t.Errorf("reload: %v", err)
				return
			}
		}
	}()
	return swapped
}

// A session is whole throughout a re-read, not only present. The fresh
// copy went into the store unread, to be read by the next caller, so a
// reader that had checked the old copy just before it was replaced found
// no events at all.
func TestASessionIsWholeWhileItIsReread(t *testing.T) {
	s, _ := reloadStore(t)
	stop := make(chan struct{})
	swapped := reloading(t, s, stop)
	short := 0
	for range 20000 {
		if evs, err := s.Events("S1", 0); err != nil || len(evs) != 5 {
			short++
		}
	}
	close(stop)
	n := <-swapped
	if short > 0 {
		t.Errorf("reading S1 while it was re-read %d times found its log short %d times", n, short)
	}
}

// An append made while a session is re-read is not numbered twice. Put in
// unread, the fresh copy numbered the next event from one; read first, it
// could still be behind an append made while it was read, and handed that
// event's seq out again.
func TestAnAppendDuringAReReadIsNotNumberedTwice(t *testing.T) {
	s, dir := reloadStore(t)
	stop := make(chan struct{})
	swapped := reloading(t, s, stop)
	for i := range 2000 {
		if _, err := s.Append("S1", events.TypeUserMessage, map[string]any{"text": "during"}); err != nil {
			close(stop)
			t.Fatalf("append: %v", err)
		}
		// Gaps, so some re-reads go in between appends rather than all of
		// them being refused.
		if i%50 == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	close(stop)
	t.Logf("%d re-reads went in among the appends", <-swapped)

	data, err := os.ReadFile(filepath.Join(dir, "S1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[uint64]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var ev events.Event
		if json.Unmarshal([]byte(line), &ev) == nil {
			seen[ev.Seq]++
		}
	}
	dup := 0
	for _, n := range seen {
		if n > 1 {
			dup++
		}
	}
	if dup > 0 || len(seen) != 2005 {
		t.Errorf("S1.jsonl holds %d distinct seqs, %d of them more than once; want 2005 distinct", len(seen), dup)
	}
	if evs, _ := s.Events("S1", 0); len(evs) != 2005 {
		t.Errorf("the store holds %d of S1's events, want the 2005 on disk", len(evs))
	}
}
