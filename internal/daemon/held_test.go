package daemon

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The one 409 that must be shown rather than queued.
//
// An ordinary 409 means "a turn is running, send this again when it
// ends", and both clients answer it by queueing. The daemon returns the
// same status for a command it refuses to hand to a running turn, which
// means the opposite — and the clients could not tell them apart. So
// "/clear" typed during a long turn was queued in silence and arrived
// minutes later, against a conversation the person had stopped thinking
// about. Reported from a transcript where exactly that happened.
func TestAHeldCommandSaysSoRatherThanLookingBusy(t *testing.T) {
	d, srv := archiveDaemon(t)
	if _, err := d.Loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	// A turn in flight, claimed the way the daemon claims one.
	if !d.turns.begin("s1", func() {}) {
		t.Fatal("could not begin a turn")
	}
	defer d.turns.end("s1")

	for _, c := range []struct {
		text string
		held string
	}{
		{"/clear", "/clear"},
		{"/rewind", "/rewind"},
		{"just a message", ""}, // the ordinary busy 409: queue it
	} {
		body, _ := json.Marshal(map[string]string{"text": c.text})
		resp := post(t, srv, "/api/sessions/s1/messages", string(body))
		if resp.StatusCode != http.StatusConflict && c.held != "" {
			resp.Body.Close()
			t.Errorf("%q: status %d, want 409", c.text, resp.StatusCode)
			continue
		}
		var out struct {
			Held  string `json:"held"`
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if out.Held != c.held {
			t.Errorf("%q: held = %q, want %q — a client cannot tell this refusal from an ordinary busy one",
				c.text, out.Held, c.held)
		}
	}
}
