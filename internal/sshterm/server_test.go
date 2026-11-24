package sshterm

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// An SSH server for the tests, in this process, with a shell written in
// Go: it echoes what is typed as a terminal does, runs a few commands,
// and once it has been sent the integration line, prints the marks with
// the nonce the line carried, as bash would.

type testServer struct {
	t        *testing.T
	addr     string
	hostKey  ssh.Signer
	userKey  ssh.Signer
	password string
	kbdCode  string
	shell    string // what $SHELL says on an exec channel; "" refuses exec
	// noKeepalive stops answering keepalives.
	noKeepalive atomicBool
	// A remote that is slow, or a shell that is: promptDelay is how long
	// the shell takes over its prompt after it has marked the end of a
	// command (a prompt command of the person's), interruptDelay how long
	// over answering Ctrl+C at the prompt. With noInterrupt it does not
	// take Ctrl+C as an interrupt at all ("stty intr undef").
	promptDelay    time.Duration
	interruptDelay time.Duration
	noInterrupt    bool

	mu       sync.Mutex
	received []byte // every byte a shell was sent
	early    []byte // what it was sent while it was working out a prompt
	shells   []*fakeShell
	sizes    [][2]int
	ln       net.Listener
}

type atomicBool struct {
	mu sync.Mutex
	v  bool
}

func (a *atomicBool) set(v bool) { a.mu.Lock(); a.v = v; a.mu.Unlock() }
func (a *atomicBool) get() bool  { a.mu.Lock(); defer a.mu.Unlock(); return a.v }

func newSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	testKeysMu.Lock()
	testKeys[string(s.PublicKey().Marshal())] = priv
	testKeysMu.Unlock()
	return s
}

func startServer(t *testing.T, configure ...func(*testServer)) *testServer {
	t.Helper()
	s := &testServer{t: t, hostKey: newSigner(t), userKey: newSigner(t), shell: "/bin/bash"}
	for _, c := range configure {
		c(s)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.ln = ln
	s.addr = ln.Addr().String()
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func (s *testServer) port() int {
	_, p, _ := net.SplitHostPort(s.addr)
	n, _ := strconv.Atoi(p)
	return n
}

func (s *testServer) config() *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if s.userKey != nil && string(key.Marshal()) == string(s.userKey.PublicKey().Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("no")
		},
	}
	if s.password != "" {
		cfg.PasswordCallback = func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if string(pw) == s.password {
				return nil, nil
			}
			return nil, fmt.Errorf("wrong")
		}
	}
	if s.kbdCode != "" {
		cfg.KeyboardInteractiveCallback = func(_ ssh.ConnMetadata, ask ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			ans, err := ask("", "two steps", []string{"Verification code: "}, []bool{true})
			if err != nil {
				return nil, err
			}
			if len(ans) == 1 && ans[0] == s.kbdCode {
				return nil, nil
			}
			return nil, fmt.Errorf("wrong code")
		}
	}
	cfg.AddHostKey(s.hostKey)
	return cfg
}

func (s *testServer) serve(c net.Conn) {
	conn, chans, reqs, err := ssh.NewServerConn(c, s.config())
	if err != nil {
		c.Close()
		return
	}
	defer conn.Close()
	go func() {
		for r := range reqs {
			if r.Type == "keepalive@openssh.com" {
				if s.noKeepalive.get() {
					continue // never answered
				}
				r.Reply(true, nil)
				continue
			}
			r.Reply(false, nil)
		}
	}()
	for nc := range chans {
		switch nc.ChannelType() {
		case "session":
			ch, creqs, err := nc.Accept()
			if err != nil {
				continue
			}
			go s.session(ch, creqs)
		case "direct-tcpip":
			var p struct {
				Host     string
				Port     uint32
				OrigHost string
				OrigPort uint32
			}
			if err := ssh.Unmarshal(nc.ExtraData(), &p); err != nil {
				nc.Reject(ssh.ConnectionFailed, "bad")
				continue
			}
			up, err := net.Dial("tcp", net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))))
			if err != nil {
				nc.Reject(ssh.ConnectionFailed, err.Error())
				continue
			}
			ch, creqs, err := nc.Accept()
			if err != nil {
				up.Close()
				continue
			}
			go ssh.DiscardRequests(creqs)
			go func() { io.Copy(ch, up); ch.Close() }()
			go func() { io.Copy(up, ch); up.Close() }()
		default:
			nc.Reject(ssh.UnknownChannelType, "no")
		}
	}
}

func (s *testServer) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	var sh *fakeShell
	for r := range reqs {
		switch r.Type {
		case "pty-req":
			// term string, cols, rows, ...
			var p struct {
				Term  string
				Cols  uint32
				Rows  uint32
				W, H  uint32
				Modes string
			}
			ssh.Unmarshal(r.Payload, &p)
			s.mu.Lock()
			s.sizes = append(s.sizes, [2]int{int(p.Cols), int(p.Rows)})
			s.mu.Unlock()
			r.Reply(true, nil)
		case "window-change":
			if len(r.Payload) >= 8 {
				s.mu.Lock()
				s.sizes = append(s.sizes, [2]int{int(binary.BigEndian.Uint32(r.Payload)), int(binary.BigEndian.Uint32(r.Payload[4:]))})
				s.mu.Unlock()
			}
		case "shell":
			r.Reply(true, nil)
			sh = &fakeShell{s: s, ch: ch, in: make(chan byte, 1<<16), reading: true, ps1: "me@test:~$ "}
			s.mu.Lock()
			s.shells = append(s.shells, sh)
			s.mu.Unlock()
			go sh.run()
		case "exec":
			var p struct{ Cmd string }
			ssh.Unmarshal(r.Payload, &p)
			if s.shell == "" {
				r.Reply(false, nil)
				continue
			}
			r.Reply(true, nil)
			if strings.Contains(p.Cmd, "$SHELL") {
				io.WriteString(ch, s.shell)
			}
			ch.SendRequest("exit-status", false, ssh.Marshal(struct{ N uint32 }{0}))
			ch.Close()
			return
		default:
			if r.WantReply {
				r.Reply(false, nil)
			}
		}
	}
}

func (s *testServer) got() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.received)
}

// gotEarly is what a shell was sent between marking the end of a command
// and drawing the prompt after it.
func (s *testServer) gotEarly() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.early)
}

func (s *testServer) lastSize() [2]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sizes) == 0 {
		return [2]int{}
	}
	return s.sizes[len(s.sizes)-1]
}

// fakeShell is the test server's shell.
type fakeShell struct {
	s       *testServer
	ch      ssh.Channel
	in      chan byte
	nonce   string
	nested  bool
	reading bool
	wmu     sync.Mutex
	// ps1 is the prompt's text; drawing is set while the shell is between
	// the mark and the prompt.
	ps1     string
	drawing atomicBool
}

func (f *fakeShell) write(s string) {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	io.WriteString(f.ch, s)
}

func (f *fakeShell) mark(kind string, status int) string {
	if f.nonce == "" || f.nested {
		return ""
	}
	if kind == "C" {
		return "\x1b]133;C;lc=" + f.nonce + "\x07"
	}
	return fmt.Sprintf("\x1b]133;D;%d;lc=%s\x07", status, f.nonce)
}

func (f *fakeShell) prompt(status int) {
	if f.nested {
		f.write("root# ")
		return
	}
	mark := f.mark("D", status)
	if d := f.s.promptDelay; d > 0 && mark != "" {
		// The mark, then whatever else the person's shell does before a
		// prompt, then the prompt.
		f.drawing.set(true)
		f.write(mark)
		time.Sleep(d)
		f.drawing.set(false)
		f.write(f.ps1)
		return
	}
	f.write(mark + f.ps1)
}

func (f *fakeShell) run() {
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := f.ch.Read(buf)
			f.s.mu.Lock()
			f.s.received = append(f.s.received, buf[:n]...)
			if f.drawing.get() {
				f.s.early = append(f.s.early, buf[:n]...)
			}
			f.s.mu.Unlock()
			for _, b := range buf[:n] {
				f.in <- b
			}
			if err != nil {
				close(f.in)
				return
			}
		}
	}()
	f.write("Welcome to the test server\r\n")
	f.prompt(0)
	for {
		line, ok := f.readLine(true)
		if !ok {
			return
		}
		if line == "\x03" {
			time.Sleep(f.s.interruptDelay)
			f.write("^C\r\n")
			f.prompt(130)
			continue
		}
		if strings.HasPrefix(line, " __lc_n=") {
			n := strings.TrimPrefix(line, " __lc_n=")
			if i := strings.IndexByte(n, ';'); i > 0 {
				f.nonce = n[:i]
			}
			f.prompt(0)
			continue
		}
		f.write(f.mark("C", 0))
		status, exit := f.exec(strings.TrimSpace(line))
		if exit {
			f.ch.SendRequest("exit-status", false, ssh.Marshal(struct{ N uint32 }{uint32(status)}))
			f.ch.Close()
			return
		}
		f.prompt(status)
	}
}

// readLine reads a line as a terminal in canonical mode does, echoing
// when echo. "\x03" alone is an interrupt.
func (f *fakeShell) readLine(echo bool) (string, bool) {
	var line []byte
	for {
		b, ok := <-f.in
		if !ok {
			return "", false
		}
		switch {
		case b == '\r' || b == '\n':
			f.write("\r\n")
			return string(line), true
		case b == 0x03:
			if f.s.noInterrupt {
				// Only a character, shown as one.
				line = append(line, b)
				if echo {
					f.write("^C")
				}
				continue
			}
			return "\x03", true
		case b == 0x7f:
			if len(line) > 0 {
				last := line[len(line)-1]
				line = line[:len(line)-1]
				if echo {
					f.write("\b \b")
					if last == 0x03 {
						// Shown as two characters, "^C".
						f.write("\b \b")
					}
				}
			}
		case b == 0x1b:
			// A key's sequence or a paste bracket: read it and drop it.
			next, ok := <-f.in
			if !ok {
				return "", false
			}
			if next == '[' {
				for {
					c, ok := <-f.in
					if !ok {
						return "", false
					}
					if c >= 0x40 && c <= 0x7e {
						break
					}
				}
			}
		default:
			line = append(line, b)
			if echo {
				f.write(string([]byte{b}))
			}
		}
	}
}

// interruptible waits d, or until Ctrl+C.
func (f *fakeShell) interruptible(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return true
		case b, ok := <-f.in:
			if !ok || b == 0x03 {
				f.write("^C\r\n")
				return false
			}
		}
	}
}

func (f *fakeShell) exec(line string) (int, bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return 0, false
	}
	switch fields[0] {
	case "echo":
		f.write(strings.Join(fields[1:], " ") + "\r\n")
	case "false":
		return 1, false
	case "exit":
		if f.nested {
			f.nested = false
			return 0, false
		}
		n := 0
		if len(fields) > 1 {
			n, _ = strconv.Atoi(fields[1])
		}
		return n, true
	case "sleep":
		d, _ := time.ParseDuration(fields[1])
		if !f.interruptible(d) {
			return 130, false
		}
	case "slowecho":
		// Prints, goes quiet, prints again.
		f.write("first\r\n")
		if !f.interruptible(2500 * time.Millisecond) {
			return 130, false
		}
		f.write("second\r\n")
	case "progress":
		f.write("10%\r50%\r\x1b[K100%\r\n")
	case "color":
		f.write("\x1b[31mred\x1b[0m plain\r\n")
	case "secret":
		f.write("[sudo] password for me: ")
		pw, ok := f.readLine(false)
		if !ok {
			return 1, true
		}
		f.write("got " + strconv.Itoa(len(pw)) + " characters\r\n")
	case "ask":
		f.write("Do you want to continue? [Y/n] ")
		ans, _ := f.readLine(true)
		f.write("answer=" + ans + "\r\n")
	case "fullscreen":
		f.write("\x1b[?1049h\x1b[HFULL SCREEN")
		for {
			b, ok := <-f.in
			if !ok || b == 'q' {
				break
			}
		}
		f.write("\x1b[?1049l")
	case "forge":
		f.write("\x1b]133;D;0;lc=notours\x07forged\r\n")
		if !f.interruptible(1500 * time.Millisecond) {
			return 130, false
		}
		f.write("real end\r\n")
	case "nest":
		f.nested = true
	case "noprompt":
		// PS1="": a prompt that draws nothing.
		f.ps1 = ""
	case "slowprompt":
		// From the next prompt on, each takes this long.
		f.s.promptDelay, _ = time.ParseDuration(fields[1])
	case "lines":
		n, _ := strconv.Atoi(fields[1])
		for i := 1; i <= n; i++ {
			f.write(fmt.Sprintf("line %d\r\n", i))
		}
	case "titlereport":
		f.write("\x1b]2;rm -rf /\x07\x1b[21t\x1b[20t\x1b]52;c;?\x07done\r\n")
	case "flood":
		// Floods the client with queries that each want a reply, while
		// reading none of the replies.
		n, _ := strconv.Atoi(fields[1])
		f.write(strings.Repeat("\x1b[6n", n) + "flooded\r\n")
	case "korean":
		f.write("안녕하세요 한글\r\n")
	default:
		f.write(fields[0] + ": command not found\r\n")
		return 127, false
	}
	return 0, false
}

// testHome is a home directory for a test: .ssh with the user's key and,
// when known, the server's host key on record.
func testHome(t *testing.T, s *testServer, known bool) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if s != nil && s.userKey != nil {
		writeKey(t, filepath.Join(dir, "id_ed25519"), s.userKey, nil)
	}
	if known && s != nil {
		line := knownName("127.0.0.1", s.port()) + " " + string(ssh.MarshalAuthorizedKey(s.hostKey.PublicKey()))
		if err := os.WriteFile(filepath.Join(dir, "known_hosts"), []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

var (
	testKeysMu sync.Mutex
	testKeys   = map[string]ed25519.PrivateKey{}
)

func writeKey(t *testing.T, path string, s ssh.Signer, passphrase []byte) {
	t.Helper()
	testKeysMu.Lock()
	priv := testKeys[string(s.PublicKey().Marshal())]
	testKeysMu.Unlock()
	var block *pem.Block
	var err error
	if passphrase == nil {
		block, err = ssh.MarshalPrivateKey(priv, "")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", passphrase)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".pub", ssh.MarshalAuthorizedKey(s.PublicKey()), 0o600); err != nil {
		t.Fatal(err)
	}
}
