// sshfake is an SSH server with a shell written in Go, for the flicker
// diagnosis only: a remote that runs the same on every runner, and whose
// way of echoing a key can be chosen.
//
//	sshfake -addr 127.0.0.1:22999 -known <known_hosts file to write>
//	        [-shell /bin/bash] [-echo plain|redraw|split] [-gap 20ms]
//
// It accepts every client. -echo is how a typed character comes back:
//
//	plain   the character, as bash does;
//	redraw  the whole line again with the cursor hidden meanwhile, as a
//	        line editor with highlighting does, in one write;
//	split   the same in two writes, -gap apart: the line erased, then
//	        drawn.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

var (
	addr     = flag.String("addr", "127.0.0.1:22999", "where to listen")
	known    = flag.String("known", "", "known_hosts file to write this server's key to")
	shell    = flag.String("shell", "/bin/bash", "what $SHELL says on an exec channel; empty refuses exec")
	echoMode = flag.String("echo", "plain", "plain, redraw or split")
	gap      = flag.Duration("gap", 20*time.Millisecond, "time between the two writes of -echo split")
)

func main() {
	flag.Parse()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		log.Fatal(err)
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	if *known != "" {
		host, port, _ := net.SplitHostPort(ln.Addr().String())
		name := host
		if port != "22" {
			name = "[" + host + "]:" + port
		}
		line := name + " " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))) + "\n"
		if err := os.WriteFile(*known, []byte(line), 0o600); err != nil {
			log.Fatal(err)
		}
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	log.Printf("sshfake listening on %s, echo %s", ln.Addr(), *echoMode)
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go serve(c, cfg)
	}
}

func serve(c net.Conn, cfg *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	defer conn.Close()
	go func() {
		for r := range reqs {
			if r.WantReply {
				r.Reply(strings.HasPrefix(r.Type, "keepalive"), nil)
			}
		}
	}()
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go session(ch, creqs)
	}
}

func session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	var sh *fakeShell
	cols := 80
	for r := range reqs {
		switch r.Type {
		case "pty-req":
			var p struct {
				Term  string
				Cols  uint32
				Rows  uint32
				W, H  uint32
				Modes string
			}
			ssh.Unmarshal(r.Payload, &p)
			cols = int(p.Cols)
			r.Reply(true, nil)
		case "env":
			r.Reply(true, nil)
		case "window-change":
			if len(r.Payload) >= 8 && sh != nil {
				sh.resized(int(binary.BigEndian.Uint32(r.Payload)))
			}
		case "shell":
			r.Reply(true, nil)
			sh = &fakeShell{ch: ch, in: make(chan byte, 1<<16), cols: cols}
			go sh.run()
		case "exec":
			var p struct{ Cmd string }
			ssh.Unmarshal(r.Payload, &p)
			if *shell == "" {
				r.Reply(false, nil)
				continue
			}
			r.Reply(true, nil)
			if strings.Contains(p.Cmd, "$SHELL") {
				io.WriteString(ch, *shell)
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

const promptText = "me@fake:~$ "

type fakeShell struct {
	ch       ssh.Channel
	in       chan byte
	nonce    string
	wmu      sync.Mutex
	lmu      sync.Mutex
	line     []byte
	cols     int
	atPrompt bool
}

func (f *fakeShell) write(s string) {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	io.WriteString(f.ch, s)
}

func (f *fakeShell) mark(kind string, status int) string {
	if f.nonce == "" {
		return ""
	}
	if kind == "C" {
		return "\x1b]133;C;lc=" + f.nonce + "\x07"
	}
	return fmt.Sprintf("\x1b]133;D;%d;lc=%s\x07", status, f.nonce)
}

func (f *fakeShell) prompt(status int) {
	f.lmu.Lock()
	f.line = f.line[:0]
	f.atPrompt = true
	f.lmu.Unlock()
	f.write(f.mark("D", status) + promptText)
}

// resized draws the line again, as readline does when the window changes.
func (f *fakeShell) resized(cols int) {
	f.lmu.Lock()
	f.cols = cols
	at := f.atPrompt
	line := string(f.line)
	f.lmu.Unlock()
	if at {
		f.write("\r\x1b[K" + promptText + line)
	}
}

func (f *fakeShell) run() {
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := f.ch.Read(buf)
			for _, b := range buf[:n] {
				f.in <- b
			}
			if err != nil {
				close(f.in)
				return
			}
		}
	}()
	f.write("Welcome to the fake server\r\n")
	f.prompt(0)
	for {
		line, ok := f.readLine()
		if !ok {
			return
		}
		f.lmu.Lock()
		f.atPrompt = false
		f.lmu.Unlock()
		if line == "\x03" {
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

// echo sends a typed character back the way -echo says.
func (f *fakeShell) echo(b byte) {
	f.lmu.Lock()
	line := string(f.line)
	f.lmu.Unlock()
	mode := *echoMode
	if strings.HasPrefix(line, " __lc") {
		// The integration line is typed at once: echoed plainly, so the
		// slow modes do not hold it up for seconds.
		mode = "plain"
	}
	switch mode {
	case "redraw":
		f.write("\x1b[?25l\r\x1b[K" + promptText + "\x1b[32m" + line + "\x1b[0m\x1b[?25h")
	case "split":
		f.write("\x1b[?25l\r\x1b[K")
		time.Sleep(*gap)
		f.write(promptText + "\x1b[32m" + line + "\x1b[0m\x1b[?25h")
	default:
		f.write(string([]byte{b}))
	}
}

func (f *fakeShell) readLine() (string, bool) {
	for {
		b, ok := <-f.in
		if !ok {
			return "", false
		}
		switch {
		case b == '\r' || b == '\n':
			f.write("\r\n")
			f.lmu.Lock()
			line := string(f.line)
			f.lmu.Unlock()
			return line, true
		case b == 0x03:
			return "\x03", true
		case b == 0x7f:
			f.lmu.Lock()
			had := len(f.line) > 0
			if had {
				f.line = f.line[:len(f.line)-1]
			}
			f.lmu.Unlock()
			if had {
				f.write("\b \b")
			}
		case b == 0x1b:
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
			f.lmu.Lock()
			f.line = append(f.line, b)
			f.lmu.Unlock()
			f.echo(b)
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
		return 0, true
	case "clear":
		f.write("\x1b[H\x1b[2J\x1b[3J")
	case "seq", "lines":
		n := 10
		if len(fields) > 1 {
			n, _ = strconv.Atoi(fields[len(fields)-1])
		}
		var b strings.Builder
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&b, "%d\r\n", i)
		}
		f.write(b.String())
	case "ls":
		var b strings.Builder
		for i := 1; i <= 40; i++ {
			fmt.Fprintf(&b, "-rwxr-xr-x  1 root  wheel  %6d Nov 23 10:00 file%02d\r\n", i*137, i)
		}
		f.write(b.String())
	default:
		f.write(fields[0] + ": command not found\r\n")
		return 127, false
	}
	return 0, false
}
