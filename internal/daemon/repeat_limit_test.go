package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"localcode/internal/client"
)

// The repeat guard's ceiling has two homes, a number in the settings
// window and a command, and they have to be one setting: moved in
// either, the other hears about it, and a client that just opened reads
// the same value.
func TestRepeatLimitSyncsBetweenTheCommandAndTheSettingsWindow(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	c := client.New(httpSrv.URL)
	ctx := context.Background()
	sess, err := c.CreateSession(ctx, "general-purpose")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	evCtx, cancelEvents := context.WithCancel(ctx)
	defer cancelEvents()
	evCh, err := c.SubscribeEvents(evCtx, sess.ID, 0)
	if err != nil {
		t.Fatalf("SubscribeEvents: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// The window's endpoint moves it and every client hears.
	resp, err := http.Post(httpSrv.URL+"/api/settings/repeat-limit", "application/json",
		strings.NewReader(`{"limit":0}`))
	if err != nil {
		t.Fatalf("POST repeat-limit: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST repeat-limit: %s", resp.Status)
	}
	if data := waitForSettings(t, evCh); data["repeat_limit"] != float64(0) {
		t.Errorf("repeat_limit = %v after the window turned it off", data["repeat_limit"])
	}
	if d.Loop.RepeatLimit() != 0 {
		t.Errorf("the daemon still has a limit of %d", d.Loop.RepeatLimit())
	}

	// Out of range is refused, not clamped: a window that sent 500 and
	// was told "ok" would show a number the daemon does not hold.
	resp, err = http.Post(httpSrv.URL+"/api/settings/repeat-limit", "application/json",
		strings.NewReader(`{"limit":500}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("limit 500 answered %s, want 400", resp.Status)
	}

	// And GET /api/settings is where a client that just opened reads it.
	var got map[string]any
	r2, err := http.Get(httpSrv.URL + "/api/settings")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(r2.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if got["repeat_limit"] != float64(0) {
		t.Errorf("GET /api/settings repeat_limit = %v, want 0", got["repeat_limit"])
	}
}
