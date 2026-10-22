package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"localcode/internal/client"
	"localcode/internal/events"
	"localcode/internal/session"
)

// A fork numbers its copy of the log from one, and leaves the source's
// renames out, so the sequence numbers a rewind and a redo name are the
// fork's own in its copy. Copied unchanged they pointed at other events,
// and the fork's replay took a redone turn for an undone one: its history
// and its session notes were gone.
func TestAForkMapsWhatARewindAndARedoName(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()
	d := newTestDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	c := client.New(httpSrv.URL)
	ctx := context.Background()
	src, err := c.CreateSession(ctx, "plan")
	if err != nil {
		t.Fatal(err)
	}
	// Renames, which the fork does not copy, so the numbers shift.
	for _, title := range []string{"one", "two"} {
		if _, err := c.RenameSession(ctx, src.ID, title); err != nil {
			t.Fatal(err)
		}
	}
	turn, _ := d.Loop.Store.Append(src.ID, events.TypeUserMessage, map[string]any{"text": "try plan b"})
	d.Loop.Store.Append(src.ID, events.TypeMemorySession, map[string]any{"name": "plan-b", "content": "try the second approach"})
	d.Loop.Store.Append(src.ID, events.TypeMessagePartEnd, map[string]any{"text": "noted"})
	rewound, _ := d.Loop.Store.Append(src.ID, events.TypeRewound, map[string]any{"from_seq": turn.Seq})
	d.Loop.Store.Append(src.ID, events.TypeRedone, map[string]any{"rewind_seq": rewound.Seq})

	resp, err := http.Post(httpSrv.URL+"/api/sessions/"+src.ID+"/fork", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var forked session.Session
	if err := json.NewDecoder(resp.Body).Decode(&forked); err != nil {
		t.Fatal(err)
	}
	evs, err := d.Loop.Store.Events(forked.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var userSeq, rewoundSeq uint64
	for _, ev := range evs {
		switch ev.Type {
		case events.TypeUserMessage:
			userSeq = ev.Seq
		case events.TypeRewound:
			rewoundSeq = ev.Seq
			if got := seqOf(ev.Data["from_seq"]); got != userSeq {
				t.Errorf("the fork's rewind names %d; its copy of the turn is %d", got, userSeq)
			}
		case events.TypeRedone:
			if got := seqOf(ev.Data["rewind_seq"]); got != rewoundSeq {
				t.Errorf("the fork's redo names %d; its copy of the rewind is %d", got, rewoundSeq)
			}
		}
	}
	if userSeq == 0 || rewoundSeq == 0 {
		t.Fatalf("the fork's log lacks the turn or the rewind: %+v", evs)
	}
}

// seqOf reads a sequence number as the log holds it: a uint64 in memory,
// a float64 once it has been through JSON.
func seqOf(v any) uint64 {
	switch n := v.(type) {
	case uint64:
		return n
	case float64:
		return uint64(n)
	case int:
		return uint64(n)
	}
	return 0
}
