package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

func expiredJWT(t *testing.T, exp time.Time) string {
	t.Helper()
	seg := func(v any) string {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(data)
	}
	return seg(map[string]any{"alg": "RS256", "typ": "JWT"}) + "." + seg(map[string]any{"exp": exp.Unix()}) + "." + seg("signature")
}

// A gateway that refuses an expired JWT puts that in front of the person:
// the turn's error says when the token expired. The hint changes nothing
// about what is done with the refusal: a 401 is still not retried in place,
// and is still worth another endpoint, whatever the date it names.
func TestARefusedExpiredJWTSaysWhenItExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid or expired token"}}`))
	}))
	defer srv.Close()

	// Dates whose digits could look like a status to the classifier, were
	// they printed with a count beside them.
	for _, exp := range []time.Time{
		time.Now().Add(-503 * 24 * time.Hour),
		time.Date(2025, 5, 3, 5, 3, 0, 0, time.UTC),
		time.Now().Add(-time.Hour),
	} {
		_, err := provider.NewOpenAICompat(srv.URL, expiredJWT(t, exp)).Chat(context.Background(),
			provider.ChatRequest{Model: "m", Messages: []provider.Message{{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("hi")}}}})
		if err == nil || !strings.Contains(err.Error(), "expired on "+exp.UTC().Format("2006-01-02 15:04 MST")) {
			t.Fatalf("exp %v: %v", exp, err)
		}
		if retryableInPlace(err) {
			t.Errorf("exp %v: the hint made a 401 retryable: %v", exp, err)
		}
		if !worthFallingBackOver(err) {
			t.Errorf("exp %v: the hint stopped a 401 from falling back: %v", exp, err)
		}
	}

	store, err := session.NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	cfg := &config.Config{
		Providers:      map[string]config.ProviderConfig{"gw": {Type: config.ProviderOpenAICompat, BaseURL: srv.URL}},
		Profiles:       map[string]config.Profile{"main": {Provider: "gw", Model: "m"}},
		DefaultProfile: "main",
	}
	exp := time.Now().Add(-72 * time.Hour)
	loop := New(store, tools.NewRegistry(nil), map[string]provider.Provider{"gw": provider.NewOpenAICompat(srv.URL, expiredJWT(t, exp))}, cfg)
	loop.ProjectDir = t.TempDir()
	sess, err := store.CreateSession("s1", "", "general-purpose", true)
	if err != nil {
		t.Fatal(err)
	}
	sendErr := loop.SendMessage(context.Background(), sess.ID, "", "hello")
	evs, err := store.Events(sess.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := fmt.Sprint(sendErr)
	for _, ev := range evs {
		if ev.Type == events.TypeError {
			seen += fmt.Sprint(ev.Data["error"])
		}
	}
	if !strings.Contains(seen, "expired on "+exp.UTC().Format("2006-01-02 15:04 MST")) {
		t.Errorf("the person was not told when the token expired: %s", seen)
	}
}
