//go:build !windows

package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
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

	pid, err := spawnSuccessor(ln, alive.r)
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

func processExists(pid int) bool {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "stat=").Output()
	if err != nil {
		return false
	}
	// A zombie is not a running process; it is one whose exit nobody has
	// read yet, and spawnSuccessor's Wait goroutine reads it.
	stat := strings.TrimSpace(string(out))
	return stat != "" && !strings.HasPrefix(stat, "Z")
}
