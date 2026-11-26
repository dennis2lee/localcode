package daemon

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"localcode/internal/sshterm"

	"golang.org/x/crypto/ssh"
)

// The SSH panel's routes. The connections themselves are sshterm's to
// test; here the question is what the routes let through, and that the
// conversation's life ends its connections.

func sshDaemon(t *testing.T) (*Daemon, *httptest.Server, string) {
	t.Helper()
	d := newTestDaemon(t, "http://127.0.0.1:1")
	home := t.TempDir()
	d.SSH = sshterm.NewManager(sshterm.Options{Home: home, DataDir: filepath.Join(home, "data"), LocalUser: "me"})
	t.Cleanup(func() { d.SSH.CloseAll("test over", true) })
	d.Loop.Config.SetSSHRuntime(true)
	srv := httptest.NewServer(d.Handler())
	t.Cleanup(srv.Close)
	if _, err := d.Loop.Store.CreateSessionIn("s1", "", "general-purpose", t.TempDir(), true); err != nil {
		t.Fatal(err)
	}
	return d, srv, "s1"
}

// silentServer accepts TCP connections and says nothing, so a terminal
// opened to it stays connecting.
func silentServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { c.Close() })
		}
	}()
	return ln.Addr().String()
}

func sshRequest(t *testing.T, method, url string, body any, header map[string]string) *http.Response {
	t.Helper()
	var r *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		r = bytes.NewReader(data)
	} else {
		r = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestTheSSHRoutesRefuseWhatAnotherSiteCouldSend(t *testing.T) {
	_, srv, id := sshDaemon(t)
	input := srv.URL + "/api/sessions/" + id + "/ssh/connect"
	for name, c := range map[string]struct {
		body   any
		header map[string]string
	}{
		"a form post, not JSON":       {body: nil, header: map[string]string{"Content-Type": "text/plain"}},
		"another site's fetch":        {body: map[string]string{"target": "h"}, header: map[string]string{"Sec-Fetch-Site": "cross-site"}},
		"another origin":              {body: map[string]string{"target": "h"}, header: map[string]string{"Origin": "http://evil.example"}},
		"a name pointed at this host": {body: map[string]string{"target": "h"}, header: map[string]string{"Host": "rebind.example:4096", "Origin": "http://rebind.example:4096"}},
	} {
		resp := sshRequest(t, "POST", input, c.body, c.header)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, resp.StatusCode)
		}
	}
	// The page's own request goes through.
	u := strings.TrimPrefix(srv.URL, "http://")
	resp := sshRequest(t, "POST", input, map[string]string{"target": silentServer(t)}, map[string]string{"Origin": "http://" + u, "Sec-Fetch-Site": "same-origin"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the page's own connect: %d", resp.StatusCode)
	}
	// Turning SSH off closes every connection: another site may not.
	resp = sshRequest(t, "POST", srv.URL+"/api/settings/ssh", map[string]bool{"enabled": false},
		map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("another site's /api/settings/ssh: %d", resp.StatusCode)
	}
	resp = sshRequest(t, "POST", srv.URL+"/api/settings/ssh", nil, map[string]string{"Content-Type": "text/plain"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a form post to /api/settings/ssh: %d", resp.StatusCode)
	}
	// A stream from another site is refused too.
	resp = sshRequest(t, "GET", srv.URL+"/api/sessions/"+id+"/ssh/stream", nil, map[string]string{"Sec-Fetch-Site": "cross-site"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site stream: %d", resp.StatusCode)
	}
}

func TestSSHOffRefusesConnectingAndClosesEverything(t *testing.T) {
	d, srv, id := sshDaemon(t)
	addr := silentServer(t)
	term, err := d.SSH.Open(id, addr, sshterm.ByPerson)
	if err != nil {
		t.Fatal(err)
	}
	resp := sshRequest(t, "POST", srv.URL+"/api/settings/ssh", map[string]bool{"enabled": false}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("turning off: %d", resp.StatusCode)
	}
	if term.Info().State != sshterm.StateEnded {
		t.Fatal("a connection outlived the setting")
	}
	resp = sshRequest(t, "POST", srv.URL+"/api/sessions/"+id+"/ssh/connect", map[string]string{"target": addr}, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("connect with SSH off: %d", resp.StatusCode)
	}
}

func TestArchivingOrDeletingAConversationClosesItsConnections(t *testing.T) {
	d, srv, id := sshDaemon(t)
	addr := silentServer(t)
	connecting, err := d.SSH.Open(id, addr, sshterm.ByModel)
	if err != nil {
		t.Fatal(err)
	}
	resp := sshRequest(t, "POST", srv.URL+"/api/sessions/"+id+"/archive", map[string]any{}, nil)
	if resp.StatusCode >= 300 {
		t.Fatalf("archive: %d", resp.StatusCode)
	}
	if connecting.Info().State != sshterm.StateEnded || len(d.SSH.List(id)) != 0 {
		t.Fatal("archiving left a connection")
	}
	// An archived conversation opens none.
	resp = sshRequest(t, "POST", srv.URL+"/api/sessions/"+id+"/ssh/connect", map[string]string{"target": addr}, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("connect in an archived conversation: %d", resp.StatusCode)
	}

	if _, err := d.Loop.Store.CreateSessionIn("s2", "", "general-purpose", t.TempDir(), true); err != nil {
		t.Fatal(err)
	}
	other, _ := d.SSH.Open("s2", addr, sshterm.ByModel)
	resp = sshRequest(t, "DELETE", srv.URL+"/api/sessions/s2", map[string]any{}, nil)
	if resp.StatusCode >= 300 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if other.Info().State != sshterm.StateEnded {
		t.Fatal("deleting left a connection")
	}
}

func TestOnlyThePanelsAnswerWithTheFingerprintAcceptsAHostKey(t *testing.T) {
	d, srv, id := sshDaemon(t)
	// No question to answer: refused, whatever is sent.
	term, _ := d.SSH.Open(id, silentServer(t), sshterm.ByPerson)
	resp := sshRequest(t, "POST", srv.URL+"/api/sessions/"+id+"/ssh/"+term.ID+"/answer",
		map[string]any{"question": 1, "accept": true, "fingerprint": "SHA256:x"}, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("an answer to nothing: %d", resp.StatusCode)
	}
}

func TestTheStreamSendsStateAndForgetsTheViewersSize(t *testing.T) {
	d, srv, id := sshDaemon(t)
	term, _ := d.SSH.Open(id, silentServer(t), sshterm.ByPerson)
	req, _ := http.NewRequest("GET", srv.URL+"/api/sessions/"+id+"/ssh/stream?terminal="+term.ID+"&viewer=v1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64*1024)
	n, _ := resp.Body.Read(buf)
	first := string(buf[:n])
	if !strings.HasPrefix(first, "data: {") || !strings.Contains(first, `"kind":"state"`) || !strings.Contains(first, `"id":"`+term.ID+`"`) {
		t.Fatalf("first frame %q", first)
	}
	sshRequest(t, "POST", srv.URL+"/api/sessions/"+id+"/ssh/"+term.ID+"/size", map[string]any{"viewer": "v1", "cols": 120, "rows": 40}, nil)
	if i := term.Info(); i.Cols != 120 || i.Rows != 40 {
		t.Fatalf("size %dx%d", i.Cols, i.Rows)
	}
	resp.Body.Close()
}

func TestAQuestionMarksTheConversationAsAsking(t *testing.T) {
	d, _, id := sshDaemon(t)
	if d.activityOf(id).Asking {
		t.Fatal("asking before anything asked")
	}
	// A server whose host key the test's home has no record of: the
	// terminal asks the person about it.
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		ssh.NewServerConn(c, cfg)
	}()
	term, err := d.SSH.Open(id, ln.Addr().String(), sshterm.ByModel)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for term.Info().Question == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !d.activityOf(id).Asking {
		t.Fatal("a host key question did not mark the conversation as asking")
	}
	term.Close("done")
}

func TestUpdatingWithOpenConnectionsAsksTwice(t *testing.T) {
	d, _, id := sshDaemon(t)
	now := time.Now()
	if err := d.sshUpdateRefusal(now); err != nil {
		t.Fatalf("with nothing open: %v", err)
	}
	d.SSH.Open(id, silentServer(t), sshterm.ByPerson)
	err := d.sshUpdateRefusal(now)
	if err == nil || !strings.Contains(err.Error(), "1 SSH connection") {
		t.Fatalf("first /update: %v", err)
	}
	if err := d.sshUpdateRefusal(now.Add(time.Minute)); err != nil {
		t.Fatalf("asked again within two minutes: %v", err)
	}
	if err := d.sshUpdateRefusal(now.Add(5 * time.Minute)); err == nil {
		t.Fatal("asked again much later went ahead without saying")
	}
}

func TestTheSSHPanelCollapses(t *testing.T) {
	css, err := os.ReadFile(filepath.Join("static", "style.css"))
	if err != nil {
		t.Fatal(err)
	}
	// #ssh-panel sets its own display, which outranks aside.collapsed.
	if !regexp.MustCompile(`#ssh-panel\.collapsed\s*\{[^}]*display:\s*none`).Match(css) {
		t.Fatal("no #ssh-panel.collapsed { display: none }: the hide button would not hide it")
	}
}
