package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"localcode/internal/client"
	"localcode/internal/events"
)

// "/model default" goes back to the agent's model, as the daemon's own
// /model reads it. The TUI sent "default" on as a profile name, and the
// daemon refused it: "no profile called default".
func TestModelDefaultGoesBackToTheAgentsModel(t *testing.T) {
	for _, word := range []string{"default", "unset", "clear", "Default"} {
		t.Run(word, func(t *testing.T) {
			var mu sync.Mutex
			sent := "(nothing)"
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Profile string `json:"profile"`
				}
				json.NewDecoder(r.Body).Decode(&req)
				mu.Lock()
				sent = req.Profile
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{}`))
			}))
			defer srv.Close()
			m := New(client.New(srv.URL), "s1", "general-purpose", make(chan events.Event), false)

			_, cmd := pressEnterWith(t, m, "/model "+word)
			if cmd == nil {
				t.Fatal("nothing was asked of the daemon")
			}
			cmd()
			mu.Lock()
			defer mu.Unlock()
			if sent != "" {
				t.Errorf("sent profile %q, want the empty one that clears the choice", sent)
			}
		})
	}
}
