package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The process half of a handoff, done for real: this test binds a port,
// starts itself as the successor with the listener and the pipes, and
// checks that the successor answers on the same port while this
// process's copy of the listener is closed — and that the successor goes
// away when the terminal's pipe does.
//
// The successor is this test binary re-entered under an environment
// variable, the way TestExecSelfReplacesThisProcess does it. What it
// runs there is a stand-in daemon: one handler answering /api/version
// with its pid, which is all the parent needs to tell the two apart.
func TestASuccessorTakesTheListenerAndOutlivesNothing(t *testing.T) {
	if os.Getenv("LC_HANDOFF_CHILD") != "" {
		handoffChildMain()
		return
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()

	alive, err := newTUIAlivePipe()
	if err != nil {
		t.Fatal(err)
	}

	// spawnSuccessor runs this program's own binary with os.Args[1:], so
	// the child is "go test" re-entered on this one test.
	oldArgs := os.Args
	os.Args = []string{oldArgs[0], "-test.run=TestASuccessorTakesTheListenerAndOutlivesNothing"}
	defer func() { os.Args = oldArgs }()
	os.Setenv("LC_HANDOFF_CHILD", "1")
	defer os.Unsetenv("LC_HANDOFF_CHILD")

	pid, _, err := spawnSuccessor(os.Args[0], ln, alive.r)
	if err != nil {
		t.Fatalf("spawnSuccessor: %v", err)
	}
	if pid == os.Getpid() {
		t.Fatal("the successor's pid is this process's")
	}

	// This is the retiring side: the listener is closed here, and the
	// port must still answer, from the other process.
	ln.Close()

	got := versionPID(t, addr)
	if got != pid {
		t.Fatalf("the port is answered by pid %d, want the successor %d", got, pid)
	}

	// The terminal goes away: the write end of the pipe closes with the
	// process that has the TUI, which is what closing it here stands for.
	alive.w.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processExists(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the successor (pid %d) did not exit after the terminal's pipe closed", pid)
}

// handoffChildMain is the successor: it takes what it inherited, serves
// one handler on it, says it is ready, and stays until the pipe closes.
func handoffChildMain() {
	in, ok := takingOver()
	if !ok {
		fmt.Fprintln(os.Stderr, "child: nothing inherited")
		os.Exit(2)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"version": "child", "pid": os.Getpid()})
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(in.ln)
	in.announceReady() // exits this process when the alive pipe closes
	select {}
}

func versionPID(t *testing.T, addr string) int {
	t.Helper()
	var last error
	for i := 0; i < 40; i++ {
		resp, err := http.Get("http://" + addr + "/api/version")
		if err == nil {
			var v struct {
				PID int `json:"pid"`
			}
			err = json.NewDecoder(resp.Body).Decode(&v)
			resp.Body.Close()
			if err == nil {
				return v.PID
			}
		}
		last = err
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no answer on %s: %v", addr, last)
	return 0
}

// The successor leaves when its parent does, and stays when the pipe
// merely breaks.
//
// The second half is the one that matters. Reading the pipe with io.Copy
// reported EOF and every other error the same way, so any fault on the
// handle stopped the daemon — which from the window looks like the daemon
// vanishing in the middle of a turn and every request afterwards
// answering 502.
func TestTheSuccessorLeavesOnlyWhenItsParentGoes(t *testing.T) {
	t.Run("the parent closing stops it", func(t *testing.T) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		stopped := make(chan struct{})
		go watchParent(r, func() { close(stopped) })
		w.Close() // the parent has gone

		select {
		case <-stopped:
		case <-time.After(2 * time.Second):
			t.Fatal("the daemon did not stop when the process that started it closed the pipe")
		}
	})

	t.Run("a broken pipe does not", func(t *testing.T) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer w.Close()
		stopped := make(chan struct{})
		done := make(chan struct{})
		go func() { watchParent(r, func() { close(stopped) }); close(done) }()
		// Not the parent leaving: the handle itself becomes unusable
		// while the write end is still open.
		r.Close()

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("watchParent did not return on an unreadable pipe")
		}
		select {
		case <-stopped:
			t.Error("an unreadable pipe stopped the daemon; only the parent leaving may do that")
		default:
		}
	})
}

// Every path that starts a successor uses the process's own alive pipe,
// not one made in a local.
//
// A local is reachable only until the last statement that mentions it,
// and every one of these passes alive.r to spawnSuccessor and then never
// touches alive again — so the collector could run os.File's finalizer
// and close the write end while the successor was still starting. The
// successor reads EOF and calls os.Exit(0). That was found and fixed in
// the window's handoff in v0.101.0 and left in the two startup paths,
// which is the same fault in the two modes nobody was running.
//
// A source check rather than a behavioural one, because the failure is a
// garbage collection that may or may not happen on any given run — the
// thing to hold is that nobody makes a pipe of their own again.
func TestEveryHandoffPathHoldsTheProcessAlivePipe(t *testing.T) {
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	dir := filepath.Dir(here)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if !strings.Contains(line, "newTUIAlivePipe()") {
				continue
			}
			// Its own declaration, and the one legitimate caller: the
			// accessor that makes it once.
			if name == "handoff.go" &&
				(strings.Contains(line, "processAliveOnce.Do") || strings.HasPrefix(line, "func newTUIAlivePipe")) {
				continue
			}
			t.Errorf("%s:%d makes an alive pipe of its own; use processAlivePipe() so it outlives the "+
				"statement that passes it:\n\t%s", name, i+1, strings.TrimSpace(line))
		}
	}
}
