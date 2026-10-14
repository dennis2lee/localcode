package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"localcode/internal/config"
	"localcode/internal/credentials"
	"localcode/internal/provider"
)

// keyRecorder is an Anthropic endpoint that records the key each request
// carried.
func keyRecorder(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("x-api-key"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), keys...) }
}

func askOnce(p provider.Provider) error {
	ch, err := p.Chat(context.Background(), provider.ChatRequest{Model: "claude-sonnet-5", MaxTokens: 16,
		Messages: []provider.Message{{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("hi")}}}})
	if err != nil {
		return err
	}
	for range ch {
	}
	return nil
}

func writeCredentials(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".localcode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A key saved again, rotated, is the key the next request carries: the
// file is read per request. It was read once and kept until a restart.
// (Found by review.)
func TestAKeySavedAgainIsTheNextRequestsKey(t *testing.T) {
	home := t.TempDir()
	srv, keys := keyRecorder(t)
	p := &keylessAnthropic{name: "anthropic", home: home, baseURL: srv.URL}
	for _, key := range []string{"sk-ant-old", "sk-ant-new"} {
		if err := credentials.SaveAnthropicAPIKey(home, key); err != nil {
			t.Fatal(err)
		}
		if err := askOnce(p); err != nil {
			t.Fatal(err)
		}
	}
	if got := keys(); len(got) != 2 || got[0] != "sk-ant-old" || got[1] != "sk-ant-new" {
		t.Errorf("keys on the wire %v, want the old one then the new one", got)
	}
}

// A key that is only whitespace is no key, and nothing is sent with it.
// (Found by review.)
func TestABlankSavedKeyIsNoKey(t *testing.T) {
	home := t.TempDir()
	writeCredentials(t, home, `{"anthropic_api_key":"  "}`)
	srv, keys := keyRecorder(t)
	err := askOnce(&keylessAnthropic{name: "anthropic", home: home, baseURL: srv.URL})
	if err == nil || !strings.Contains(err.Error(), "localcode login anthropic") {
		t.Errorf("err = %v, want how to add a key", err)
	}
	if got := keys(); len(got) != 0 {
		t.Errorf("a request went out with key %q", got)
	}
}

// A credentials file that does not parse says what to do, and the next
// request after it is fixed works: nothing about the failure is kept.
// (Found by review.)
func TestABrokenCredentialsFileSaysHowToFixItAndRecovers(t *testing.T) {
	home := t.TempDir()
	writeCredentials(t, home, "{")
	srv, keys := keyRecorder(t)
	p := &keylessAnthropic{name: "anthropic", home: home, baseURL: srv.URL}
	if err := askOnce(p); err == nil || !strings.Contains(err.Error(), "localcode login anthropic") {
		t.Errorf("err = %v, want how to write the file again", err)
	}
	writeCredentials(t, home, `{"anthropic_api_key":"sk-ant-fixed"}`)
	if err := askOnce(p); err != nil {
		t.Fatalf("after the file was fixed: %v", err)
	}
	if got := keys(); len(got) != 1 || got[0] != "sk-ant-fixed" {
		t.Errorf("keys on the wire %v", got)
	}
}

// A mistyped agent on a config that declares none says how to get the
// default, rather than ending on "Configured: " with nothing after it.
// (Found by review.)
func TestATypoOnAConfigWithNoAgentsSaysWhatWouldWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(anthropicStarterConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = applyModelChoice(cfg, runOptions{agent: "genral-purpose"})
	if err == nil || !strings.Contains(err.Error(), "declares no agents") || !strings.Contains(err.Error(), "Leave --agent out") {
		t.Errorf("err = %v", err)
	}
}
