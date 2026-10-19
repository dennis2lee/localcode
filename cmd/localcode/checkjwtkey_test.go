package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"localcode/internal/config"
)

// testJWT is a token that expires at exp, with a signature nobody checks.
func testJWT(t *testing.T, exp time.Time) string {
	t.Helper()
	seg := func(v any) string {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(data)
	}
	return seg(map[string]any{"alg": "RS256", "kid": "test-key", "typ": "JWT"}) + "." +
		seg(map[string]any{"iss": "test-auth", "aud": "test-api", "exp": exp.Unix()}) + "." +
		base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 64))
}

// fakeGateway accepts one bearer token. Anything else is refused with a
// 401 whose body repeats the Authorization header it was sent, the way a
// careless server might, so a test can see that the report does not.
type fakeGateway struct {
	*httptest.Server
	chats atomic.Int32
}

func newFakeGateway(t *testing.T, accept string) *fakeGateway {
	t.Helper()
	g := &fakeGateway{}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+accept {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, `{"error":{"message":"invalid token: %s"}}`, r.Header.Get("Authorization"))
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			w.Write([]byte(`{"data":[{"id":"CodeA"},{"id":"CodeB"}]}`))
		case "/v1/chat/completions":
			g.chats.Add(1)
			var req struct {
				Model string `json:"model"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			fmt.Fprintf(w, `{"choices":[{"message":{"content":"OK from %s"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`, req.Model)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.Close)
	return g
}

// writeKeyConfig writes a config with one gateway provider holding key, at
// path, with the given mode.
func writeKeyConfig(t *testing.T, path, baseURL, key string, mode os.FileMode) {
	t.Helper()
	cfg := map[string]any{
		"providers":       map[string]any{"gw": map[string]any{"type": "openai-compat", "base_url": baseURL, "api_key": key}},
		"profiles":        map[string]any{"main": map[string]any{"provider": "gw", "model": "CodeB"}},
		"default_profile": "main",
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// keyCheckEnv is a home and a project directory to run check-jwt-key in, with
// the process in the project.
func keyCheckEnv(t *testing.T) (home, project string) {
	t.Helper()
	home, project = t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(project)
	return home, project
}

func noKeyIn(t *testing.T, out, key string) {
	t.Helper()
	if strings.Contains(out, key) {
		t.Errorf("the key is in the report:\n%s", out)
	}
}

// A key the gateway accepts is reported as working: what the JWT says,
// when it expires, where it is written, the models the gateway lists, and
// one chat reply from the default profile's model. The key is not printed.
func TestCheckJWTKeyReportsAWorkingJWT(t *testing.T) {
	home, _ := keyCheckEnv(t)
	key := testJWT(t, time.Now().Add(400*24*time.Hour))
	g := newFakeGateway(t, key)
	writeKeyConfig(t, filepath.Join(home, ".localcode", "config.json"), g.URL+"/v1", key, 0o600)

	var out bytes.Buffer
	if err := checkJWTKey(nil, &out); err != nil {
		t.Fatalf("check-jwt-key: %v\n%s", err, out.String())
	}
	report := out.String()
	for _, want := range []string{
		"provider gw (openai-compat, " + g.URL + "/v1)",
		"JWT, RS256, key id test-key, issuer test-auth, audience test-api",
		"days left",
		"written   in " + filepath.Join("~", ".localcode", "config.json"),
		"accepted: 2 model(s): CodeA, CodeB",
		`accepted: CodeB answered "OK from CodeB" (5 + 2 tokens)`,
		"result: the key works",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not say %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "warning") {
		t.Errorf("a warning for a 0600 file in the home directory:\n%s", report)
	}
	noKeyIn(t, report, key)
}

// A key the gateway refuses is reported as not working, with the status,
// and the check fails. The gateway's answer repeats the key, and the
// report does not. An expired token says so in the last line.
func TestCheckJWTKeyReportsARefusedKey(t *testing.T) {
	home, _ := keyCheckEnv(t)
	g := newFakeGateway(t, "the-only-key-it-takes")
	for _, tc := range []struct {
		name    string
		exp     time.Time
		expired bool
	}{
		{"current", time.Now().Add(24 * time.Hour), false},
		{"expired", time.Now().Add(-48 * time.Hour), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := testJWT(t, tc.exp)
			writeKeyConfig(t, filepath.Join(home, ".localcode", "config.json"), g.URL+"/v1", key, 0o600)
			var out bytes.Buffer
			err := checkJWTKey([]string{"gw"}, &out)
			if err == nil || !strings.Contains(err.Error(), "the key of gw did not work") {
				t.Fatalf("a refused key: err %v\n%s", err, out.String())
			}
			report := out.String()
			for _, want := range []string{"refused (401)", "[the key]", "result: the key does not work: the endpoint refused it (401)"} {
				if !strings.Contains(report, want) {
					t.Errorf("the report does not say %q:\n%s", want, report)
				}
			}
			if got := strings.Contains(report, "EXPIRED on") && strings.Contains(report, "and the token has expired"); got != tc.expired {
				t.Errorf("expired said %v, want %v:\n%s", got, tc.expired, report)
			}
			noKeyIn(t, report, key)
		})
	}
}

// --no-chat asks only for the model list and sends no chat request.
func TestCheckJWTKeyWithoutChat(t *testing.T) {
	home, _ := keyCheckEnv(t)
	key := testJWT(t, time.Now().Add(24*time.Hour))
	g := newFakeGateway(t, key)
	writeKeyConfig(t, filepath.Join(home, ".localcode", "config.json"), g.URL+"/v1", key, 0o600)

	var out bytes.Buffer
	if err := checkJWTKey([]string{"--no-chat"}, &out); err != nil {
		t.Fatalf("check-jwt-key --no-chat: %v\n%s", err, out.String())
	}
	if g.chats.Load() != 0 || !strings.Contains(out.String(), "no chat request sent") {
		t.Errorf("%d chat requests; report:\n%s", g.chats.Load(), out.String())
	}
}

// A token written where others can read it, or in the project's own config
// file, is said to be at risk.
func TestCheckJWTKeyWarnsWhereTheKeyIsWritten(t *testing.T) {
	_, project := keyCheckEnv(t)
	key := testJWT(t, time.Now().Add(24*time.Hour))
	g := newFakeGateway(t, key)
	writeKeyConfig(t, filepath.Join(project, ".localcode", "config.json"), g.URL+"/v1", key, 0o644)

	var out bytes.Buffer
	if err := checkJWTKey([]string{"gw", "--model", "CodeA"}, &out); err != nil {
		t.Fatalf("check-jwt-key: %v\n%s", err, out.String())
	}
	report := out.String()
	wants := []string{"the project's config file", `CodeA answered "OK from CodeA"`}
	if runtime.GOOS != "windows" {
		wants = append(wants, "readable by other users (mode 0644)")
	}
	for _, want := range wants {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not say %q:\n%s", want, report)
		}
	}
	noKeyIn(t, report, key)
}

// What check-jwt-key covers, and what it refuses before sending anything:
// it checks JWT keys only, so a provider whose api_key is anything else is
// refused by name and passed over when no name is given.
func TestCheckJWTKeyChoosesItsProviders(t *testing.T) {
	home, _ := keyCheckEnv(t)
	cfg := `{"providers": {
		"gw": {"type": "openai-compat", "base_url": "http://127.0.0.1:9/v1", "api_key": "sk-not-a-jwt-key"},
		"local": {"type": "openai-compat", "base_url": "http://127.0.0.1:9/v1"},
		"claude": {"type": "anthropic", "api_key": "a"}},
	  "profiles": {"main": {"provider": "gw", "model": "m"}}, "default_profile": "main"}`
	if err := os.MkdirAll(filepath.Join(home, ".localcode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".localcode", "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	for args, want := range map[string]string{
		"nosuch": `no provider "nosuch" in config. Configured: claude, gw, local`,
		"claude": `provider "claude" is anthropic; check-jwt-key checks openai-compat providers`,
		"local":  `provider "local" has no api_key to check`,
		"gw":     `provider "gw"'s api_key is not a JWT`,
		"":       "no openai-compat provider in config has a JWT in api_key",
		"a b":    "usage: localcode check-jwt-key",
	} {
		err := checkJWTKey(strings.Fields(args), &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("check-jwt-key %s: %v, want %q", args, err, want)
		}
	}
}

// The model the chat check asks: the flag, else the default profile's on
// this provider, else another profile's, else the first one listed.
func TestChatModelPrecedence(t *testing.T) {
	cfg := mustConfig(t, `{"providers": {"gw": {"type": "openai-compat", "base_url": "http://x/v1"}, "other": {"type": "openai-compat", "base_url": "http://y/v1"}},
	  "profiles": {"a": {"provider": "gw", "model": "from-a"}, "b": {"provider": "gw", "model": "from-b"}, "o": {"provider": "other", "model": "from-o"}},
	  "default_profile": "b"}`)
	listed := []string{"listed"}
	for _, tc := range []struct{ provider, flag, want string }{
		{"gw", "flagged", "flagged"},
		{"gw", "", "from-b"},
		{"other", "", "from-o"},
		{"none", "", "listed"},
	} {
		if got := chatModel(tc.provider, cfg, tc.flag, listed); got != tc.want {
			t.Errorf("chatModel(%s, %q) = %q, want %q", tc.provider, tc.flag, got, tc.want)
		}
	}
	cfg.DefaultProfile = "o"
	if got := chatModel("gw", cfg, "", listed); got != "from-a" {
		t.Errorf("with the default profile on another provider: %q, want the first gw profile", got)
	}
}

// A key goes over the network unencrypted only on plain http to another
// machine.
func TestSendsKeyInClear(t *testing.T) {
	for url, want := range map[string]bool{
		"http://gateway.example.com/v1": true,
		"http://10.0.0.5:8080/v1":       true,
		"https://gateway.example.com":   false,
		"http://localhost:1234/v1":      false,
		"http://127.0.0.1:1234/v1":      false,
		"http://[::1]:1234/v1":          false,
	} {
		if got := sendsKeyInClear(url); got != want {
			t.Errorf("sendsKeyInClear(%s) = %v, want %v", url, got, want)
		}
	}
}

// mustConfig loads a config from JSON text through the same loader a run
// uses.
func mustConfig(t *testing.T, text string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// A server can echo part of a key: a JWT's signature alone, say. Each part
// long enough to matter is taken out as the whole key is. A key too short
// to be a secret is left alone, rather than every letter like it in the
// report being replaced.
func TestTheReportTakesOutPartsOfTheKey(t *testing.T) {
	key := "eyJhbGciOiJSUzI1NiJ9.eyJleHAiOjE5MDAwMDAwMDB9.c2lnbmF0dXJlLXNlY3JldC12YWx1ZS0xMjM0NTY3ODkw"
	parts := strings.Split(key, ".")
	var out bytes.Buffer
	r := &keyReport{out: &out, key: " " + key + "\n"}
	r.line("chat", "echoed: %s | %s | %s | %s", key, parts[2], parts[1], parts[0])
	for _, p := range append(parts, key) {
		if strings.Contains(out.String(), p) {
			t.Errorf("%q was printed: %s", p, out.String())
		}
	}
	if n := strings.Count(out.String(), "[the key]"); n != 4 {
		t.Errorf("%d replacements, want 4: %s", n, out.String())
	}

	out.Reset()
	r = &keyReport{out: &out, key: "k"}
	r.line("models", `refused (401): {"error": "token check failed"}`)
	if strings.Contains(out.String(), "[the key]") {
		t.Errorf("a one-letter key rewrote the line: %s", out.String())
	}
	out.Reset()
	r.describeFile("k", []string{filepath.Join(t.TempDir(), "config.json")})
	if !strings.Contains(out.String(), "not looked for") {
		t.Errorf("a one-letter key was looked for in config files: %s", out.String())
	}
}
