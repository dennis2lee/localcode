package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A compaction typed mid-turn used to be handed to the running turn as
// trailing text, where it reached the model as chat saying "/compact"
// rather than running: injected text never walks the command table, and
// the turn slot means two compactions cannot race, so the only exposure
// was the command arriving as a suggestion inside the conversation it
// would have summarized. It now waits for an idle session like the other
// commands that rewrite what the model is holding.
//
// The requests go straight at the handler rather than over a test
// server: this is what the handler decides, and a socket adds nothing.
func TestACompactTypedMidTurnIsRefusedUntilIdle(t *testing.T) {
	for _, text := range []string{
		"/compact",
		"/compact keep only the file paths",
		"   /compact   ",
	} {
		if got := heldUntilIdle(text); got != "/compact" {
			t.Errorf("heldUntilIdle(%q) = %q, want %q", text, got, "/compact")
		}
	}

	// No model behind this URL on purpose: the held refusal answers
	// before any provider call, so it must never be reached.
	d := newTestDaemon(t, "http://example.invalid")
	if _, err := d.Loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	// A turn in flight, claimed the way the daemon claims one.
	if !d.turns.begin("s1", func() {}) {
		t.Fatal("could not begin a turn")
	}
	defer d.turns.end("s1")

	send := func(text string) (int, map[string]any) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"text": text})
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/s1/messages", strings.NewReader(string(body)))
		rec := httptest.NewRecorder()
		d.Handler().ServeHTTP(rec, req)
		var out map[string]any
		_ = json.NewDecoder(rec.Result().Body).Decode(&out)
		rec.Result().Body.Close()
		return rec.Code, out
	}

	status, out := send("/compact keep only the file paths")
	if status != http.StatusConflict {
		t.Fatalf("/compact mid-turn: status %d, want 409", status)
	}
	if out["held"] != "/compact" {
		t.Errorf("/compact mid-turn: held = %v, want %q — a client cannot tell this refusal from an ordinary busy one",
			out["held"], "/compact")
	}
	if errText, _ := out["error"].(string); errText == "" {
		t.Error("/compact mid-turn: empty error — the person is left with a refusal and no reason")
	}

	// And the ordinary path beside it is unchanged: a plain message still
	// goes to the running turn rather than being refused as held.
	_, plain := send("actually, keep the auth check")
	if held, _ := plain["held"].(string); held != "" {
		t.Errorf("ordinary message mid-turn: held = %q, want none", held)
	}
}
