package sshterm

import (
	"context"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// The integration line against the real shells, through a real sshd.
//
// The test server's shell only pretends to be bash. This starts the
// system's sshd as the current user, on a loopback port, with a
// configuration, host key and authorized key of the test's own, and logs
// in to it as that user. The person's ~/.ssh is not read or written: the
// client side has a home of its own, and the server reads only the files
// this test names. Skipped where there is no sshd (it is not installed on
// every machine) and on Windows.

func startRealSSHD(t *testing.T, shell string) (addr string, home string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("no sshd to start as a user here")
	}
	sshd := "/usr/sbin/sshd"
	if _, err := os.Stat(sshd); err != nil {
		t.Skip("no sshd")
	}
	if _, err := os.Stat(shell); err != nil {
		t.Skipf("no %s", shell)
	}
	me, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	// The remote shell's own files (history, zsh's session files) go in a
	// directory the test does not hold to a clean removal: a shell writes
	// them as it exits, which can be after the test's own cleanup began.
	// Registered before sshd's kill, so removed after it.
	shellDir, err := os.MkdirTemp("", "lc-sshd-shell-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for i := 0; i < 20; i++ {
			if os.RemoveAll(shellDir) == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	hostKey := newSigner(t)
	clientKey := newSigner(t)
	writeKey(t, filepath.Join(dir, "host_key"), hostKey, nil)
	os.WriteFile(filepath.Join(dir, "authorized_keys"), ssh.MarshalAuthorizedKey(clientKey.PublicKey()), 0o600)
	// The shell for a session, and $SHELL for an exec, are the test's,
	// whatever the user's login shell is.
	wrapper := filepath.Join(dir, "shell.sh")
	os.WriteFile(wrapper, []byte(`#!/bin/sh
export SHELL=`+shell+`
export HISTFILE=`+filepath.Join(shellDir, "history")+`
export ZDOTDIR=`+shellDir+`
if [ -n "$SSH_ORIGINAL_COMMAND" ]; then exec /bin/sh -c "$SSH_ORIGINAL_COMMAND"; fi
exec `+shell+` -i
`), 0o700)
	// A prompt like a person's: PS1 set by the startup file.
	os.WriteFile(filepath.Join(shellDir, ".zshrc"), []byte("PS1='%n@test %~ %# '\n"), 0o600)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr = ln.Addr().String()
	ln.Close()
	_, port, _ := net.SplitHostPort(addr)
	cfg := filepath.Join(dir, "sshd_config")
	os.WriteFile(cfg, []byte(strings.Join([]string{
		"Port " + port,
		"ListenAddress 127.0.0.1",
		"HostKey " + filepath.Join(dir, "host_key"),
		"AuthorizedKeysFile " + filepath.Join(dir, "authorized_keys"),
		"PasswordAuthentication no",
		"KbdInteractiveAuthentication no",
		"UsePAM no",
		"StrictModes no",
		"PidFile " + filepath.Join(dir, "sshd.pid"),
		"ForceCommand " + wrapper,
		"AcceptEnv LANG LC_*",
		"",
	}, "\n")), 0o600)
	cmd := exec.Command(sshd, "-D", "-e", "-f", cfg)
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Skip("sshd did not start")
		}
		time.Sleep(50 * time.Millisecond)
	}
	home = t.TempDir()
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	writeKey(t, filepath.Join(home, ".ssh", "id_ed25519"), clientKey, nil)
	os.WriteFile(filepath.Join(home, ".ssh", "known_hosts"), []byte(knownName("127.0.0.1", mustPort(port))+" "+string(ssh.MarshalAuthorizedKey(hostKey.PublicKey()))), 0o600)
	_ = me
	return addr, home
}

func mustPort(s string) int {
	p, _ := parsePort(s)
	return p
}

func TestRealShells(t *testing.T) {
	// The system's bash (3.2 on macOS, with no PS0), zsh, and a newer bash
	// where there is one (PS0, and PROMPT_COMMAND that can be an array).
	shells := []string{"/bin/bash", "/bin/zsh"}
	for _, extra := range []string{"/opt/homebrew/bin/bash", "/usr/local/bin/bash"} {
		if _, err := os.Stat(extra); err == nil {
			shells = append(shells, extra)
		}
	}
	for _, shell := range shells {
		t.Run(strings.ReplaceAll(strings.TrimPrefix(shell, "/"), "/", "_"), func(t *testing.T) {
			addr, home := startRealSSHD(t, shell)
			me, _ := user.Current()
			m := testManager(t, home)
			term, err := m.Open("c1", me.Username+"@"+addr, ByModel)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := term.WaitConnected(ctx); err != nil {
				t.Fatal(err)
			}
			within(t, 15*time.Second, "integration on", func() bool { return term.Info().Integrated })
			if term.Info().Shell != filepath.Base(shell) {
				t.Fatalf("shell %q", term.Info().Shell)
			}
			settled(t, term)

			if res := run(t, term, "echo hello"); res.Output != "hello" || res.Status != "0" {
				t.Fatalf("echo: %+v", res)
			}
			if res := run(t, term, "false"); res.Status != "1" {
				t.Fatalf("false: %+v", res)
			}
			// The shell keeps its state between runs.
			run(t, term, "cd /tmp && export LC_TEST_VAR=kept")
			if res := run(t, term, `echo "$PWD $LC_TEST_VAR"`); !strings.HasSuffix(res.Output, "tmp kept") {
				t.Fatalf("state: %+v", res)
			}
			// $? reaches a prompt command that reads it, after ours.
			if res := run(t, term, "(exit 7); echo $?"); res.Output != "7" || res.Status != "0" {
				t.Fatalf("status kept: %+v", res)
			}
			if res := run(t, term, "seq 1 300 | tail -2"); res.Output != "299\n300" {
				t.Fatalf("pipe: %+v", res)
			}
			// Output that prints a mark without the nonce is not the end.
			res := run(t, term, `printf '\033]133;D;0\007fake\n'; sleep 1; echo real`)
			if !strings.Contains(res.Output, "real") {
				t.Fatalf("forged: %+v", res)
			}
			// Back to back, quickly.
			for i := 0; i < 5; i++ {
				if res := run(t, term, "echo n"); res.Output != "n" {
					t.Fatalf("run %d: %q", i, res.Output)
				}
			}
			// The person and the model in turn.
			r, _ := term.Read(0, true)
			if len(r.Screen) == 0 {
				t.Fatal("no screen")
			}
		})
	}
}

// Korean typed at the right margin stays on the screen. zsh ends a line
// that fills the width exactly with a space and a carriage return; an
// emulator that did not wrap after a wide character in the last two
// columns erased the line (third_party/vt/LOCALCODE.md). With the locale
// sent, as OpenSSH sends it, zsh reads the Hangul as characters.
func TestKoreanAtTheMarginStaysOnTheScreen(t *testing.T) {
	addr, home := startRealSSHD(t, "/bin/zsh")
	me, _ := user.Current()
	m := testManager(t, home)
	m.opts.Env = []string{"LANG=en_US.UTF-8"}
	term, err := m.Open("c1", me.Username+"@"+addr, ByModel)
	if err != nil {
		t.Fatal(err)
	}
	if err := term.WaitConnected(context.Background()); err != nil {
		t.Fatal(err)
	}
	within(t, 15*time.Second, "integrated", func() bool { return term.Info().Integrated })
	term.ViewerSize("v", 66, 40)
	time.Sleep(300 * time.Millisecond)
	settled(t, term)
	start := term.Snapshot().CursorX
	// Enough a's that 안녕 ends exactly at the margin.
	cmd := "echo " + strings.Repeat("a", 66-start-len("echo ")-4)
	for _, r := range cmd + "안녕하세요 한글" {
		term.PersonInput(nil, string(r), "")
	}
	time.Sleep(300 * time.Millisecond)
	term.PersonInput(&PersonKey{Key: "Enter"}, "", "")
	within(t, 5*time.Second, "the command done", func() bool { return !term.Info().Running })
	screen := readAll(term)
	if !strings.Contains(screen, cmd+"안녕\n하세요 한글") {
		t.Fatalf("the command line was not kept:\n%s", screen)
	}
	if !strings.Contains(screen, strings.TrimPrefix(cmd, "echo ")+"안녕하세요 한글") {
		t.Fatalf("no output:\n%s", screen)
	}
}
