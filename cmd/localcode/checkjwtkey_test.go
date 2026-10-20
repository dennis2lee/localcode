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

// noKeyIn fails when any run of minEchoLen characters of the key is in the
// report: the whole key, one of its parts, or what is left of it after a
// reply was cut short.
func noKeyIn(t *testing.T, out, key string) {
	t.Helper()
	for i := 0; i+minEchoLen <= len(key); i++ {
		if run := key[i : i+minEchoLen]; strings.Contains(out, run) {
			t.Errorf("%q, part of the key, is in the report:\n%s", run, out)
			return
		}
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
			if strings.Contains(report, "hint:") {
				t.Errorf("the chat line carries the turn's hint paragraph:\n%s", report)
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
	r.describeFile("k", []string{filepath.Join(t.TempDir(), "config.json")}, "")
	if !strings.Contains(out.String(), "not looked for") {
		t.Errorf("a one-letter key was looked for in config files: %s", out.String())
	}
}

// A gateway that answers each path with what the test gives it.
func newScriptedGateway(t *testing.T, models func(w http.ResponseWriter), chat func(w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			models(w)
		case "/v1/chat/completions":
			chat(w)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func status(code int, body string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.WriteHeader(code)
		w.Write([]byte(body))
	}
}

// What the check concludes from each kind of answer. A refusal is an
// answer whatever else could be asked, 403 as much as 401; a 200 that is
// not a model list does not count as the key being accepted.
func TestCheckJWTKeyVerdicts(t *testing.T) {
	home, _ := keyCheckEnv(t)
	key := testJWT(t, time.Now().Add(24*time.Hour))
	cfgPath := filepath.Join(home, ".localcode", "config.json")
	for _, tc := range []struct {
		name         string
		models, chat func(w http.ResponseWriter)
		args         []string
		noProfile    bool
		works        bool
		want         string
	}{
		{"403 on chat", status(200, `{"data":[{"id":"CodeA"}]}`), status(403, `{"error":"no access to this model"}`), nil, false, false,
			"result: the key does not work: the endpoint refused it (403)"},
		{"refused, --no-chat", status(401, `{"error":"invalid token"}`), nil, []string{"--no-chat"}, false, false,
			"result: the key does not work: the endpoint refused it (401)"},
		{"sign-in page, --no-chat", status(200, "<html>sign in</html>"), nil, []string{"--no-chat"}, false, false,
			"result: not known"},
		{"error object, --no-chat", status(200, `{"error":"sign in"}`), nil, []string{"--no-chat"}, false, false,
			"no model list (200): the answer is not a model list"},
		{"refused, nothing to ask", status(401, `{"error":"invalid token"}`), nil, nil, true, false,
			"result: the key does not work: the endpoint refused it (401)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chat := tc.chat
			if chat == nil {
				chat = status(500, "not expected")
			}
			srv := newScriptedGateway(t, tc.models, chat)
			if tc.noProfile {
				cfg := `{"providers": {"gw": {"type": "openai-compat", "base_url": "` + srv.URL + `/v1", "api_key": "` + key + `"}}}`
				if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				writeKeyConfig(t, cfgPath, srv.URL+"/v1", key, 0o600)
			}
			var out bytes.Buffer
			err := checkJWTKey(tc.args, &out)
			if (err == nil) != tc.works {
				t.Errorf("err = %v, want works=%v", err, tc.works)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Errorf("the report does not say %q:\n%s", tc.want, out.String())
			}
		})
	}
}

// A reply cut short before the report sees it can end part way through the
// key, and a base URL can carry the key in its query. Neither puts a run
// of the key on screen.
func TestCheckJWTKeyHidesACutKeyAndOneInTheURL(t *testing.T) {
	home, _ := keyCheckEnv(t)
	key := testJWT(t, time.Now().Add(24*time.Hour))
	echo := strings.Repeat("x", 250) + " token was " + key
	srv := newScriptedGateway(t, status(401, `{"error":"`+echo+`"}`), status(401, `{"error":"`+echo+`"}`))
	cfgPath := filepath.Join(home, ".localcode", "config.json")
	writeKeyConfig(t, cfgPath, srv.URL+"/v1", key, 0o600)
	var out bytes.Buffer
	checkJWTKey(nil, &out)
	if !strings.Contains(out.String(), "refused (401)") || !strings.Contains(out.String(), "token was [the key]") {
		t.Fatalf("the test did not reach a refusal that repeats the cut key:\n%s", out.String())
	}
	noKeyIn(t, out.String(), key)

	writeKeyConfig(t, cfgPath, srv.URL+"/v1?token="+key, key, 0o600)
	out.Reset()
	checkJWTKey(nil, &out)
	if !strings.Contains(out.String(), "/v1?token=[the key])") {
		t.Errorf("the provider line does not hide the key in its URL:\n%s", out.String())
	}
	noKeyIn(t, out.String(), key)
	if got := redactKey("an ordinary sentence with no key in it", key); got != "an ordinary sentence with no key in it" {
		t.Errorf("text with no key in it was changed: %q", got)
	}
}

// The provider may come before the flags, after them, or between them; two
// providers are an error that says how the command is used.
func TestCheckJWTKeyArgumentOrder(t *testing.T) {
	home, _ := keyCheckEnv(t)
	key := testJWT(t, time.Now().Add(24*time.Hour))
	g := newFakeGateway(t, key)
	writeKeyConfig(t, filepath.Join(home, ".localcode", "config.json"), g.URL+"/v1", key, 0o600)
	for _, args := range [][]string{
		{"gw", "--no-chat"},
		{"--no-chat", "gw"},
		{"--model", "CodeA", "gw", "--no-chat"},
	} {
		var out bytes.Buffer
		if err := checkJWTKey(args, &out); err != nil {
			t.Errorf("%v: %v\n%s", args, err, out.String())
		}
	}
	err := checkJWTKey([]string{"gw", "--no-chat", "other"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "[--timeout d]") {
		t.Errorf("two providers: %v, want the usage with every flag", err)
	}
}

// How long is left is counted in days, exactly, and a far expiry is not cut
// off where a time.Duration stops.
func TestCheckJWTKeyCountsTheDaysLeft(t *testing.T) {
	var out bytes.Buffer
	r := &keyReport{out: &out}
	r.describeKey(testJWT(t, time.Now().Add(400*24*time.Hour+time.Hour)))
	if !strings.Contains(out.String(), "(400 days left)") {
		t.Errorf("400 days and an hour: %s", out.String())
	}
	out.Reset()
	r.describeKey(testJWT(t, time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)))
	if strings.Contains(out.String(), "106751") || !strings.Contains(out.String(), "9999-12-31") {
		t.Errorf("an expiry in 9999: %s", out.String())
	}
}

// Run in the home directory, the home's config file is not the project's;
// a file only the world can read is still a file others can read.
func TestCheckJWTKeyFileWarnings(t *testing.T) {
	home, _ := keyCheckEnv(t)
	t.Chdir(home)
	key := testJWT(t, time.Now().Add(24*time.Hour))
	g := newFakeGateway(t, key)
	writeKeyConfig(t, filepath.Join(home, ".localcode", "config.json"), g.URL+"/v1", key, 0o604)
	var out bytes.Buffer
	if err := checkJWTKey([]string{"--no-chat"}, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "project's config file") {
		t.Errorf("the home's file was called the project's:\n%s", out.String())
	}
	if runtime.GOOS != "windows" && !strings.Contains(out.String(), "readable by other users (mode 0604)") {
		t.Errorf("a world-readable file was not warned about:\n%s", out.String())
	}
}

// The check says so when the base URL would send the key unencrypted. The
// address is a documentation one nothing answers on, and the timeout is
// short, since only the warning is wanted.
func TestCheckJWTKeyWarnsOfCleartext(t *testing.T) {
	home, _ := keyCheckEnv(t)
	key := testJWT(t, time.Now().Add(24*time.Hour))
	writeKeyConfig(t, filepath.Join(home, ".localcode", "config.json"), "http://192.0.2.1:9/v1", key, 0o600)
	var out bytes.Buffer
	checkJWTKey([]string{"--no-chat", "--timeout", "200ms"}, &out)
	if !strings.Contains(out.String(), "base_url is http and not this machine") {
		t.Errorf("no cleartext warning:\n%s", out.String())
	}
	if !sendsKeyInClear("http://192.0.2.1/v1") || sendsKeyInClear("http://LOCALHOST:1234/v1") {
		t.Error("sendsKeyInClear judged a remote or an upper-case localhost wrongly")
	}
	if got := chatModel("none", mustConfig(t, `{"providers": {"gw": {"type": "openai-compat", "base_url": "http://x/v1"}}}`), "", nil); got != "" {
		t.Errorf("no flag, no profile and no list: %q, want none", got)
	}
}

// From the second review. A refusal on the model list stays the answer
// when the chat request then fails some other way; arguments after "--"
// are positional; an empty provider name is refused; the project's config
// file is warned about however it was named.
func TestCheckJWTKeySecondReview(t *testing.T) {
	home, project := keyCheckEnv(t)
	key := testJWT(t, time.Now().Add(-24*time.Hour))
	srv := newScriptedGateway(t, status(401, `{"error":"expired"}`), status(500, `{"error":"upstream down"}`))
	cfgPath := filepath.Join(home, ".localcode", "config.json")
	writeKeyConfig(t, cfgPath, srv.URL+"/v1", key, 0o600)
	var out bytes.Buffer
	if err := checkJWTKey(nil, &out); err == nil {
		t.Errorf("a refused key passed:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "refused it (401), and the token has expired") {
		t.Errorf("the refusal was lost to the chat failure:\n%s", out.String())
	}

	for _, args := range [][]string{{"--", "gw"}, {"--no-chat", "--", "gw"}} {
		out.Reset()
		if err := checkJWTKey(args, &out); err == nil || !strings.Contains(out.String(), "provider gw") {
			t.Errorf("%v: %v\n%s", args, err, out.String())
		}
	}
	if err := checkJWTKey([]string{"--", "gw", "--no-chat"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Errorf("a flag after -- was not read as a second provider: %v", err)
	}
	if err := checkJWTKey([]string{""}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "the provider name is empty") {
		t.Errorf("an empty provider name: %v", err)
	}

	good := testJWT(t, time.Now().Add(24*time.Hour))
	g := newFakeGateway(t, good)
	projectCfg := filepath.Join(project, ".localcode", "config.json")
	writeKeyConfig(t, projectCfg, g.URL+"/v1", good, 0o600)
	out.Reset()
	if err := checkJWTKey([]string{"--no-chat", "--config", projectCfg}, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "the project's config file") {
		t.Errorf("the project's file named with --config was not warned about:\n%s", out.String())
	}
}

// The edges the second review showed could move unnoticed: a run of
// exactly minEchoLen characters is hidden and one shorter is not; a 403
// with no model to ask is still a refusal.
func TestCheckJWTKeyEdges(t *testing.T) {
	key := testJWT(t, time.Now().Add(24*time.Hour))
	sig := strings.Split(key, ".")[2]
	if got := redactKey("x "+sig[:minEchoLen]+" y", key); got != "x [the key] y" {
		t.Errorf("a run of exactly %d: %q", minEchoLen, got)
	}
	if got := redactKey("x "+sig[:minEchoLen-1]+" y", key); strings.Contains(got, "[the key]") {
		t.Errorf("a run of %d was hidden: %q", minEchoLen-1, got)
	}

	home, _ := keyCheckEnv(t)
	srv := newScriptedGateway(t, status(403, `{"error":"forbidden"}`), status(500, "unused"))
	cfg := `{"providers": {"gw": {"type": "openai-compat", "base_url": "` + srv.URL + `/v1", "api_key": "` + key + `"}}}`
	if err := os.MkdirAll(filepath.Join(home, ".localcode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".localcode", "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	checkJWTKey(nil, &out)
	if !strings.Contains(out.String(), "refused it (403)") {
		t.Errorf("a 403 with no model to ask:\n%s", out.String())
	}
}

// missingHeaderBody is a refusal in the shape a gateway that wants a key
// of its own beside the JWT gives it, with a made-up header name.
const missingHeaderBody = `{"error":{"message":"Missing client API key (X-Team-Key header required)","type":"None","param":"None","code":"401"}}`

// newTeamGateway takes the bearer token accept for its model list, and
// for chat also wants X-Team-Key to be team. Without the header it says
// the header is missing; with another value it says the header is
// required and repeats what it was sent, the way a careless gateway might.
func newTeamGateway(t *testing.T, accept, team string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+accept {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"invalid token"}`))
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			w.Write([]byte(`{"data":[{"id":"CodeA"},{"id":"CodeB"}]}`))
		case "/v1/chat/completions":
			switch got := r.Header.Get("X-Team-Key"); got {
			case team:
				w.Write([]byte(`{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`))
			case "":
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(missingHeaderBody))
			default:
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprintf(w, `{"error":{"message":"X-Team-Key header required: %s is not a client key"}}`, got)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// writeHeaderConfig is writeKeyConfig with a headers block on the
// provider.
func writeHeaderConfig(t *testing.T, path, baseURL, key string, headers map[string]string) {
	t.Helper()
	cfg := map[string]any{
		"providers":       map[string]any{"gw": map[string]any{"type": "openai-compat", "base_url": baseURL, "api_key": key, "headers": headers}},
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
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// resultLine is the report's last line.
func resultLine(report string) string {
	_, after, _ := strings.Cut(report, "\nresult: ")
	line, _, _ := strings.Cut(after, "\n")
	return "result: " + line
}

// The headers a provider's config adds go on both requests, the way a
// turn sends them, and the report lists them by name. Their values are
// never printed.
func TestCheckJWTKeySendsTheConfiguredHeaders(t *testing.T) {
	home, _ := keyCheckEnv(t)
	key := testJWT(t, time.Now().Add(24*time.Hour))
	team := "made-up-team-key-0001"
	srv := newTeamGateway(t, key, team)
	writeHeaderConfig(t, filepath.Join(home, ".localcode", "config.json"), srv.URL+"/v1", key, map[string]string{"X-Team-Key": " " + team + "\n"})

	var out bytes.Buffer
	if err := checkJWTKey(nil, &out); err != nil {
		t.Fatalf("check-jwt-key: %v\n%s", err, out.String())
	}
	report := out.String()
	for _, want := range []string{"headers   X-Team-Key\n", `accepted: CodeB answered "OK"`, "result: the key works"} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not say %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, team) {
		t.Errorf("the report prints the header's value:\n%s", report)
	}
	noKeyIn(t, report, key)
}

// When the refusal says a header is missing or required, the verdict is
// about that header and not about the token: not configured, add it under
// providers.<name>.headers; configured with no value, it is not sent;
// configured and sent, the endpoint did not take its value. It never says
// to check the token. An expired token is said as a second problem. The
// model list's refusal counts the same way under --no-chat, and a refusal
// that names no header still says a header is missing.
func TestCheckJWTKeyBlamesTheHeaderAndNotTheToken(t *testing.T) {
	home, _ := keyCheckEnv(t)
	cfgPath := filepath.Join(home, ".localcode", "config.json")
	good := testJWT(t, time.Now().Add(24*time.Hour))
	old := testJWT(t, time.Now().Add(-24*time.Hour))
	team := "made-up-team-key-0001"
	wrong := "made-up-wrong-key-0002"
	gw := func(key string) string { return newTeamGateway(t, key, team).URL }
	for _, tc := range []struct {
		name    string
		url     string
		key     string
		headers map[string]string
		args    []string
		want    []string
	}{
		{"not configured", gw(good), good, nil, nil, []string{
			"headers   none",
			"chat      CodeB: the server answered 401: " + missingHeaderBody,
			"result: the key was not confirmed: the endpoint refused the request (401) because header X-Team-Key is missing, and it is not configured. Add it under providers.gw.headers\n",
		}},
		{"configured empty", gw(good), good, map[string]string{"X-Team-Key": " "}, nil, []string{
			"headers   X-Team-Key (empty, so not sent)",
			"because header X-Team-Key is missing. It is in providers.gw.headers, but its value is empty, so it is not sent",
		}},
		{"value refused", gw(good), good, map[string]string{"x-team-key": wrong}, nil, []string{
			"headers   x-team-key",
			"X-Team-Key header required: [the x-team-key value] is not a client key",
			"and asks for header x-team-key, which is sent: it did not take that header's value. Check the value in providers.gw.headers",
		}},
		{"token expired too", gw(old), old, nil, nil, []string{
			"EXPIRED on",
			"because header X-Team-Key is missing, and it is not configured. Add it under providers.gw.headers. The token has also expired: replace api_key as well",
		}},
		{"model list, --no-chat", newScriptedGateway(t, status(403, missingHeaderBody), status(500, "unused")).URL, good, nil, []string{"--no-chat"}, []string{
			"models    refused (403): ",
			"the endpoint refused the request (403) because header X-Team-Key is missing, and it is not configured",
		}},
		{"no name", newScriptedGateway(t, status(200, `{"data":[{"id":"CodeA"}]}`), status(401, `{"error":"a required header is missing"}`)).URL, good, nil, nil, []string{
			"because a request header is missing, and does not say which. Add the one it needs under providers.gw.headers",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeHeaderConfig(t, cfgPath, tc.url+"/v1", tc.key, tc.headers)
			var out bytes.Buffer
			err := checkJWTKey(tc.args, &out)
			if err == nil || !strings.Contains(err.Error(), "the key of gw did not work") {
				t.Errorf("err = %v, want the check to fail", err)
			}
			report := out.String()
			for _, want := range tc.want {
				if !strings.Contains(report, want) {
					t.Errorf("the report does not say %q:\n%s", want, report)
				}
			}
			result := resultLine(report)
			for _, blame := range []string{"Check that api_key", "issued for this endpoint", "JWT", "the key does not work"} {
				if strings.Contains(result, blame) {
					t.Errorf("the verdict points at the token (%q): %s", blame, result)
				}
			}
			for _, secret := range []string{team, wrong} {
				if strings.Contains(report, secret) {
					t.Errorf("the report prints a header value:\n%s", report)
				}
			}
			noKeyIn(t, report, tc.key)
		})
	}
}

// The headers line names headers and holds no value, so nothing is taken
// out of it: a value that has its header's name in it took that name out
// of its own line.
func TestTheHeadersLineIsNotRedacted(t *testing.T) {
	home, _ := keyCheckEnv(t)
	key := testJWT(t, time.Now().Add(24*time.Hour))
	srv := newTeamGateway(t, key, "made-up-team-key-0001")
	writeHeaderConfig(t, filepath.Join(home, ".localcode", "config.json"), srv.URL+"/v1", key, map[string]string{
		"X-Team-Client": "X-Team-Client:made-up-0001",
	})
	var out bytes.Buffer
	_ = checkJWTKey(nil, &out)
	if !strings.Contains(out.String(), "  headers   X-Team-Client\n") {
		t.Errorf("the headers line lost a header's name:\n%s", out.String())
	}
}

// A header value written as plain text gets the warnings the token gets,
// for the file it is in, when the token itself is not written there.
func TestAHeaderValueWrittenInTheProjectConfigIsWarnedAbout(t *testing.T) {
	_, project := keyCheckEnv(t)
	key := testJWT(t, time.Now().Add(24*time.Hour))
	t.Setenv("MADE_UP_GATEWAY_JWT", key)
	srv := newTeamGateway(t, key, "made-up-team-key-0001")
	cfg := filepath.Join(project, ".localcode", "config.json")
	writeHeaderConfig(t, cfg, srv.URL+"/v1", "{env:MADE_UP_GATEWAY_JWT}", map[string]string{"X-Team-Key": "made-up-team-key-0001"})
	if err := os.Chmod(cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := checkJWTKey(nil, &out); err != nil {
		t.Fatalf("check-jwt-key: %v\n%s", err, out.String())
	}
	report := out.String()
	if !strings.Contains(report, "the value of header X-Team-Key is written in the project's config file") {
		t.Errorf("no warning about the project's config file:\n%s", report)
	}
	if runtime.GOOS != "windows" && !strings.Contains(report, "which is readable by other users (mode 0644)") {
		t.Errorf("no warning about a file other users can read:\n%s", report)
	}
}

// In the file the token is written in, the token's warnings are said and
// cover the header values beside it; they are not said a second time for
// each header.
func TestAHeaderValueBesideTheTokenIsNotWarnedAboutTwice(t *testing.T) {
	_, project := keyCheckEnv(t)
	key := testJWT(t, time.Now().Add(24*time.Hour))
	srv := newTeamGateway(t, key, "made-up-team-key-0001")
	cfg := filepath.Join(project, ".localcode", "config.json")
	writeHeaderConfig(t, cfg, srv.URL+"/v1", key, map[string]string{"X-Team-Key": "made-up-team-key-0001"})
	var out bytes.Buffer
	if err := checkJWTKey(nil, &out); err != nil {
		t.Fatalf("check-jwt-key: %v\n%s", err, out.String())
	}
	if report := out.String(); !strings.Contains(report, "that is the project's config file") || strings.Contains(report, "the value of header") {
		t.Errorf("want the token's warning alone:\n%s", report)
	}
}
