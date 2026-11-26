package daemon

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"localcode/internal/sshterm"
)

// The PowerShell panel's routes: the SSH panel's, over the PowerShell
// manager, with open in place of connect. The shells are sshterm's to
// test; a small fake stands in here, so these run everywhere.

// fakePowerShell draws a prompt with the integration's D mark and echoes
// what is typed.
type fakePowerShell struct {
	inR, outR *io.PipeReader
	inW, outW *io.PipeWriter
	dir       string
	mu        sync.Mutex
	typed     strings.Builder
	once      sync.Once
	done      chan struct{}
}

var fakeNonce = regexp.MustCompile(`\$global:__lc_n='([0-9a-f]+)'`)

func startFakePowerShell(argv []string, dir string) *fakePowerShell {
	f := &fakePowerShell{dir: dir, done: make(chan struct{})}
	f.inR, f.inW = io.Pipe()
	f.outR, f.outW = io.Pipe()
	nonce := ""
	if b, err := base64.StdEncoding.DecodeString(argv[len(argv)-1]); err == nil {
		u := make([]uint16, len(b)/2)
		for i := range u {
			u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
		}
		if m := fakeNonce.FindStringSubmatch(string(utf16.Decode(u))); m != nil {
			nonce = m[1]
		}
	}
	go func() {
		fmt.Fprintf(f.outW, "\x1b]133;D;0;lc=%s\x07PS> ", nonce)
		buf := make([]byte, 64)
		for {
			n, err := f.inR.Read(buf)
			if err != nil {
				return
			}
			f.mu.Lock()
			f.typed.Write(buf[:n])
			f.mu.Unlock()
			f.outW.Write(buf[:n])
		}
	}()
	return f
}

func (f *fakePowerShell) Read(p []byte) (int, error)  { return f.outR.Read(p) }
func (f *fakePowerShell) Write(p []byte) (int, error) { return f.inW.Write(p) }
func (f *fakePowerShell) Resize(int, int) error       { return nil }
func (f *fakePowerShell) Wait() (int, error)          { <-f.done; return 0, nil }
func (f *fakePowerShell) Close() error {
	f.once.Do(func() { f.inR.Close(); f.outW.Close(); close(f.done) })
	return nil
}

func (f *fakePowerShell) got() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.typed.String()
}

func psDaemon(t *testing.T) (*Daemon, *httptest.Server, chan *fakePowerShell) {
	t.Helper()
	d := newTestDaemon(t, "http://127.0.0.1:1")
	started := make(chan *fakePowerShell, 8)
	m := sshterm.NewManager(sshterm.Options{Local: &sshterm.LocalShell{
		Path: "powershell.exe",
		Dir:  d.Loop.SessionDir,
		Start: func(argv []string, dir string, cols, rows int) (sshterm.LocalProcess, error) {
			f := startFakePowerShell(argv, dir)
			started <- f
			return f, nil
		},
	}})
	d.PowerShell = m
	t.Cleanup(func() { m.CloseAll("test over", true) })
	d.Loop.Config.SetPowerShellRuntime(true)
	srv := httptest.NewServer(d.Handler())
	t.Cleanup(srv.Close)
	if _, err := d.Loop.Store.CreateSessionIn("s1", "", "general-purpose", t.TempDir(), true); err != nil {
		t.Fatal(err)
	}
	return d, srv, started
}

func psURL(srv *httptest.Server, rest string) string {
	return srv.URL + "/api/sessions/s1/powershell" + rest
}

func TestThePowerShellPanelOpensTypesHoldsAndCloses(t *testing.T) {
	d, srv, started := psDaemon(t)
	resp := sshRequest(t, "POST", psURL(srv, "/open"), map[string]any{}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("open: %d", resp.StatusCode)
	}
	var opened struct {
		Terminal string `json:"terminal"`
	}
	json.NewDecoder(resp.Body).Decode(&opened)
	if opened.Terminal != "t1" {
		t.Fatalf("opened %q", opened.Terminal)
	}
	f := <-started
	// In the conversation's working directory.
	if want := d.Loop.SessionDir("s1"); f.dir != want {
		t.Errorf("started in %q, want %q", f.dir, want)
	}

	resp = sshRequest(t, "GET", psURL(srv, ""), nil, nil)
	var state struct {
		Enabled   bool           `json:"enabled"`
		Terminals []sshterm.Info `json:"terminals"`
	}
	json.NewDecoder(resp.Body).Decode(&state)
	if !state.Enabled || len(state.Terminals) != 1 || state.Terminals[0].Spec != "PowerShell" || state.Terminals[0].Opener != sshterm.ByPerson {
		t.Fatalf("state %+v", state)
	}

	deadline := time.Now().Add(5 * time.Second)
	for d.PowerShell.Get("s1", "t1").Info().State != sshterm.StateConnected && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if resp := sshRequest(t, "POST", psURL(srv, "/t1/input"), map[string]any{"text": "dir"}, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("input: %d", resp.StatusCode)
	}
	deadline = time.Now().Add(5 * time.Second)
	for !strings.Contains(f.got(), "dir") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(f.got(), "dir") {
		t.Fatalf("the shell got %q", f.got())
	}
	if resp := sshRequest(t, "POST", psURL(srv, "/t1/hold"), map[string]any{"on": true}, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("hold: %d", resp.StatusCode)
	}
	if !d.PowerShell.Get("s1", "t1").Info().Held {
		t.Error("not held")
	}
	if resp := sshRequest(t, "DELETE", psURL(srv, "/t1"), map[string]any{}, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("close: %d", resp.StatusCode)
	}
	if len(d.PowerShell.List("s1")) != 0 {
		t.Error("the terminal is still listed")
	}
	// The SSH panel's terminals are another manager's: none here.
	if d.SSH != nil && len(d.SSH.List("s1")) != 0 {
		t.Error("an SSH terminal appeared")
	}
}

func TestThePowerShellStreamCarriesItsTerminals(t *testing.T) {
	_, srv, started := psDaemon(t)
	sshRequest(t, "POST", psURL(srv, "/open"), map[string]any{}, nil)
	<-started
	resp := sshRequest(t, "GET", psURL(srv, "/stream?viewer=v1&terminal=t1"), nil, nil)
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	r := bufio.NewReader(resp.Body)
	sawState, sawScreen := false, false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !(sawState && sawScreen) {
		line, err := r.ReadString('\n')
		if err != nil {
			break
		}
		if strings.Contains(line, `"kind":"state"`) && strings.Contains(line, `"spec":"PowerShell"`) {
			sawState = true
		}
		// ">" as the JSON encoder writes it.
		if strings.Contains(line, `"kind":"screen"`) && strings.Contains(line, `PS\u003e`) {
			sawScreen = true
		}
	}
	if !sawState || !sawScreen {
		t.Fatalf("state %v, screen %v", sawState, sawScreen)
	}
}

func TestThePowerShellRoutesRefuseWhatTheyShould(t *testing.T) {
	d, srv, _ := psDaemon(t)
	// Another site.
	resp := sshRequest(t, "POST", psURL(srv, "/open"), map[string]any{}, map[string]string{"Sec-Fetch-Site": "cross-site"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site open: %d", resp.StatusCode)
	}
	// Off.
	d.Loop.Config.SetPowerShellRuntime(false)
	resp = sshRequest(t, "POST", psURL(srv, "/open"), map[string]any{}, nil)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "/powershell on") {
		t.Errorf("open while off: %d %s", resp.StatusCode, body)
	}
	// No answer route: a PowerShell terminal asks nothing through the panel.
	d.Loop.Config.SetPowerShellRuntime(true)
	if resp := sshRequest(t, "POST", psURL(srv, "/t1/answer"), map[string]any{}, nil); resp.StatusCode == http.StatusNoContent {
		t.Error("an answer route answered")
	}
	// A daemon with no PowerShell: anywhere but Windows.
	d.PowerShell = nil
	resp = sshRequest(t, "GET", psURL(srv, ""), nil, nil)
	body, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "no PowerShell") {
		t.Errorf("state with no manager: %d %s", resp.StatusCode, body)
	}
	if resp := sshRequest(t, "GET", srv.URL+"/api/powershell", nil, nil); resp.StatusCode != http.StatusOK {
		t.Errorf("the live list with no manager: %d", resp.StatusCode)
	}
}

func TestThePowerShellSettingTurnsOffAndClosesEveryTerminal(t *testing.T) {
	d, srv, started := psDaemon(t)
	sshRequest(t, "POST", psURL(srv, "/open"), map[string]any{}, nil)
	<-started
	resp := sshRequest(t, "GET", srv.URL+"/api/powershell", nil, nil)
	var live []map[string]any
	json.NewDecoder(resp.Body).Decode(&live)
	if len(live) != 1 || live[0]["session"] != "s1" {
		t.Fatalf("live %+v", live)
	}
	resp = sshRequest(t, "POST", srv.URL+"/api/settings/powershell", map[string]any{"enabled": false}, nil)
	var res map[string]any
	json.NewDecoder(resp.Body).Decode(&res)
	if resp.StatusCode != http.StatusOK || res["powershell"] != false || res["applied"] != true {
		t.Fatalf("off: %d %+v", resp.StatusCode, res)
	}
	if d.Loop.Config.PowerShellLive() || len(d.PowerShell.LiveTerminals()) != 0 {
		t.Error("still on, or a terminal still open")
	}
	resp = sshRequest(t, "GET", srv.URL+"/api/settings", nil, nil)
	var settings map[string]any
	json.NewDecoder(resp.Body).Decode(&settings)
	if settings["powershell"] != false || settings["powershell_available"] != true {
		t.Errorf("settings %+v", settings)
	}
	// Another site may not turn it off.
	if resp := sshRequest(t, "POST", srv.URL+"/api/settings/powershell", map[string]any{"enabled": true}, map[string]string{"Origin": "http://evil.example"}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin setting: %d", resp.StatusCode)
	}
}

func TestDeletingAConversationClosesItsPowerShellTerminals(t *testing.T) {
	d, srv, started := psDaemon(t)
	sshRequest(t, "POST", psURL(srv, "/open"), map[string]any{}, nil)
	<-started
	d.closeSSH([]string{"s1"}, "the conversation was deleted")
	if len(d.PowerShell.LiveTerminals()) != 0 {
		t.Error("a PowerShell terminal outlived its conversation")
	}
}

func TestAnUpdateNamesThePowerShellTerminalsItWouldClose(t *testing.T) {
	d, srv, started := psDaemon(t)
	sshRequest(t, "POST", psURL(srv, "/open"), map[string]any{}, nil)
	<-started
	now := time.Now()
	err := d.sshUpdateRefusal(now)
	if err == nil || !strings.Contains(err.Error(), "1 PowerShell terminal(s)") || strings.Contains(err.Error(), "SSH connection") {
		t.Fatalf("refusal: %v", err)
	}
	if err := d.sshUpdateRefusal(now.Add(time.Minute)); err != nil {
		t.Fatalf("asked again: %v", err)
	}
}
