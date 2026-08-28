package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// takePort holds an address for the duration of a test, standing in for
// whatever else on the machine already had it.
func takePort(t *testing.T) (addr string, close func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("take a port: %v", err)
	}
	return ln.Addr().String(), func() { ln.Close() }
}

// A free address is the ordinary case and nothing about it changes.
func TestAFreeAddressIsSimplyTaken(t *testing.T) {
	got, err := takeListener("127.0.0.1:0", workdirFor(t), false)
	if err != nil {
		t.Fatalf("takeListener: %v", err)
	}
	defer got.ln.Close()

	if got.attachTo != "" {
		t.Errorf("attached to %q instead of binding", got.attachTo)
	}
	if got.moved {
		t.Error("reported a move when the address was free")
	}
}

// Another localcode is the common thing holding port 4096, and refusing
// to start beside it is refusing to start beside exactly the thing this
// program attaches to.
func TestALocalcodeDaemonIsAttachedToRatherThanFoughtOver(t *testing.T) {
	dir := t.TempDir()
	srv := fakeDaemon(t, dir)
	addr := strings.TrimPrefix(srv.URL, "http://")

	got, err := takeListener(addr, dir, false)
	if err != nil {
		t.Fatalf("takeListener: %v", err)
	}
	if got.ln != nil {
		defer got.ln.Close()
		t.Fatal("bound a listener next to a daemon that is already there")
	}
	if got.attachTo != "http://"+addr {
		t.Errorf("attachTo = %q, want the daemon's own address", got.attachTo)
	}
}

// Anything can hold a port. Attaching a TUI to a stranger would turn a
// clear bind error into an obscure one further in, so the address is
// asked what it is before it is trusted.
func TestSomethingElseOnThePortGetsOutOfTheWay(t *testing.T) {
	addr, closePort := takePort(t)
	defer closePort()

	got, err := takeListener(addr, workdirFor(t), false)
	if err != nil {
		t.Fatalf("takeListener: %v", err)
	}
	defer got.ln.Close()

	if got.attachTo != "" {
		t.Errorf("attached to %q, which is not a localcode daemon", got.attachTo)
	}
	if !got.moved {
		t.Error("did not report that the address moved")
	}
	if got.ln.Addr().String() == addr {
		t.Error("bound the address that was already taken")
	}
	// The host is kept, so "--listen 0.0.0.0:4096" does not quietly
	// retreat to loopback when it moves.
	wantHost, _, _ := net.SplitHostPort(addr)
	gotHost, _, _ := net.SplitHostPort(got.ln.Addr().String())
	if gotHost != wantHost {
		t.Errorf("moved to host %q, want to stay on %q", gotHost, wantHost)
	}
}

// An HTTP server that is not localcode answers the probe but not as
// localcode, which is the case a bare "is anything listening" check
// would get wrong.
func TestAStrangerServingHTTPIsNotMistakenForTheDaemon(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"hello":"i am not localcode"}`)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	if _, ok := daemonWorkspace(addr); ok {
		t.Error("a server answering /api/version without a version was taken for the daemon")
	}

	got, err := takeListener(addr, workdirFor(t), false)
	if err != nil {
		t.Fatalf("takeListener: %v", err)
	}
	defer got.ln.Close()
	if got.attachTo != "" {
		t.Errorf("attached to %q", got.attachTo)
	}
}

// An address typed by name is a request. Serving somewhere else would
// answer a different one, so this is where the error stays, with
// something to do about it.
func TestAnExplicitAddressIsNotSilentlyMoved(t *testing.T) {
	addr, closePort := takePort(t)
	defer closePort()

	got, err := takeListener(addr, workdirFor(t), true)
	if err == nil {
		got.ln.Close()
		t.Fatal("an explicitly requested address was moved instead of reported")
	}
	if !strings.Contains(err.Error(), "--listen") {
		t.Errorf("error = %q, want it to say what to do", err)
	}
}

// addrInUse is what decides whether any of the above happens, so it is
// checked against a real bind failure rather than a constructed error.
// On Windows the constant that matches this is a different one; see
// listen_windows.go.
func TestABindFailureIsRecognised(t *testing.T) {
	addr, closePort := takePort(t)
	defer closePort()

	_, err := net.Listen("tcp", addr)
	if err == nil {
		t.Fatal("expected the second bind to fail")
	}
	if !addrInUse(err) {
		t.Errorf("addrInUse(%v) = false, so the recovery would never run", err)
	}
	// And an unrelated failure is not swept into it.
	if _, err := net.Listen("tcp", "no-such-host.invalid:80"); err == nil {
		t.Skip("the resolver answered for an invalid host")
	} else if addrInUse(err) {
		t.Errorf("addrInUse(%v) = true for a name that does not resolve", err)
	}
}

// workdirFor is the directory a test's process is "in". Most of these
// cases never reach the workspace comparison, so any real path does.
func workdirFor(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// fakeDaemon answers the two endpoints takeListener probes: what it is,
// and where it is working.
func fakeDaemon(t *testing.T, workspace string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/version":
			fmt.Fprint(w, `{"version":"1.2.3"}`)
		case "/api/workspace":
			fmt.Fprintf(w, `{"path":%q}`, workspace)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A daemon stamps its own directory onto every session created on it, so
// attaching to one that works somewhere else would open a conversation
// editing the wrong project while the terminal sits in this one.
// Sharing a daemon is a convenience; working where you are is the
// promise.
func TestADaemonInAnotherProjectIsNotAttachedTo(t *testing.T) {
	theirs, mine := t.TempDir(), t.TempDir()
	srv := fakeDaemon(t, theirs)
	addr := strings.TrimPrefix(srv.URL, "http://")

	got, err := takeListener(addr, mine, false)
	if err != nil {
		t.Fatalf("takeListener: %v", err)
	}
	if got.ln != nil {
		got.ln.Close()
	}
	if got.attachTo != "" {
		t.Errorf("attached to a daemon working in %s while this terminal is in %s", theirs, mine)
	}
	if !got.otherDaemon || got.elsewhere != theirs {
		t.Errorf("otherDaemon=%v elsewhere=%q, want the other daemon's directory so the caller can say why",
			got.otherDaemon, got.elsewhere)
	}
}

// And the same, named rather than moved, when the address was asked for
// by name: the message has to say which directory each one is in,
// because "already in use" would not explain why a localcode refuses to
// share with a localcode.
func TestAnExplicitAddressSaysWhichProjectTheOtherDaemonIsIn(t *testing.T) {
	theirs, mine := t.TempDir(), t.TempDir()
	srv := fakeDaemon(t, theirs)
	addr := strings.TrimPrefix(srv.URL, "http://")

	_, err := takeListener(addr, mine, true)
	if err == nil {
		t.Fatal("expected an error for an explicitly requested address")
	}
	if !strings.Contains(err.Error(), theirs) || !strings.Contains(err.Error(), mine) {
		t.Errorf("error = %q, want it to name both directories", err)
	}
}

// A daemon that will not say where it works is treated as working
// somewhere else. The cost is a second daemon; the cost of guessing the
// other way is a session editing the wrong project.
func TestADaemonThatWillNotSayWhereItWorksIsNotAttachedTo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/version" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"version":"1.2.3"}`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	dir, ok := daemonWorkspace(strings.TrimPrefix(srv.URL, "http://"))
	if !ok {
		t.Error("a daemon that answers /api/version is still a daemon")
	}
	if dir != "" {
		t.Errorf("workspace = %q, want it unknown", dir)
	}
	if sameDir(dir, t.TempDir()) {
		t.Error("an unknown workspace matched a real directory")
	}

	// And the whole decision, not just its inputs: this case used to
	// fall out of takeListener with no listener bound and nothing to
	// say, which the caller then dereferenced.
	got, err := takeListener(strings.TrimPrefix(srv.URL, "http://"), t.TempDir(), false)
	if err != nil {
		t.Fatalf("takeListener: %v", err)
	}
	if got.ln != nil {
		got.ln.Close()
		t.Fatal("bound a listener on an address a daemon already holds")
	}
	if !got.otherDaemon {
		t.Error("a daemon that will not say where it works is still a daemon in the way")
	}
}

// Two spellings of one directory are one directory. On macOS /tmp is a
// link to /private/tmp, which is exactly the difference that would
// otherwise start a second daemon for no reason.
func TestSameDirLooksThroughLinks(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if !sameDir(real, link) {
		t.Errorf("%s and %s are the same directory", real, link)
	}
	if sameDir(real, t.TempDir()) {
		t.Error("two different directories matched")
	}
}
