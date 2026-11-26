package sshterm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

// Host keys.
//
// A host's key is looked up in the system's and the person's known_hosts
// files and in localcode's own. A key that differs from a known one ends
// the connection; nothing here overrides that, and the person fixes their
// file. A host with no key on record is a question for the person in the
// panel, never for the model, and an accepted key is written to
// localcode's own file only: the person's ~/.ssh is read, never written.
//
// Our own reader rather than x/crypto's knownhosts: that one refuses every
// file over a single line it cannot parse, and a file that cannot be read
// must not look like a host with no key, which would turn "changed" into
// "new" and ask the person to accept an impostor.

// ErrHostKeyChanged is a host whose key is not the one on record.
var ErrHostKeyChanged = errors.New("the host's key has changed")

// knownEntry is one line of a known_hosts file.
type knownEntry struct {
	marker   string // "", "@cert-authority" or "@revoked"
	patterns []string
	key      ssh.PublicKey
	file     string
	line     int
}

// hostKeys checks host keys against a set of known_hosts files.
type hostKeys struct {
	files []string // read in order; own is the last
	own   string
	mu    sync.Mutex // orders writes to own
}

func newHostKeys(o Options, t Target) *hostKeys {
	var files []string
	files = append(files, t.GlobalKnownHostsFiles...)
	if len(t.GlobalKnownHostsFiles) == 0 && o.SystemDir != "" {
		files = append(files, filepath.Join(o.SystemDir, "ssh_known_hosts"), filepath.Join(o.SystemDir, "ssh_known_hosts2"))
	}
	if len(t.KnownHostsFiles) > 0 {
		files = append(files, t.KnownHostsFiles...)
	} else {
		files = append(files, filepath.Join(o.Home, ".ssh", "known_hosts"), filepath.Join(o.Home, ".ssh", "known_hosts2"))
	}
	own := filepath.Join(o.DataDir, "known_hosts")
	return &hostKeys{files: append(files, own), own: own}
}

// load reads every file. A missing file is empty. A file that exists and
// cannot be read is an error. A line that does not parse is skipped.
func (h *hostKeys) load() ([]knownEntry, error) {
	var out []knownEntry
	for _, f := range h.files {
		data, err := os.ReadFile(f)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("reading %s: %w", f, err)
		}
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		n := 0
		for sc.Scan() {
			n++
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 || line[0] == '#' {
				continue
			}
			marker, hosts, key, _, _, err := ssh.ParseKnownHosts(line)
			if err != nil || key == nil {
				continue
			}
			var patterns []string
			for _, h := range hosts {
				patterns = append(patterns, strings.Split(h, ",")...)
			}
			if marker != "" {
				marker = "@" + marker
			}
			out = append(out, knownEntry{marker: marker, patterns: patterns, key: key, file: f, line: n})
		}
	}
	return out, nil
}

// knownName is how known_hosts names host on port.
func knownName(host string, port int) string {
	if port == 22 {
		return host
	}
	return "[" + host + "]:" + strconv.Itoa(port)
}

// entryMatches reports whether e names name: one pattern matches and no
// negated one does. A hashed pattern is |1|salt|hash.
func entryMatches(e knownEntry, name string) bool {
	hit := false
	for _, p := range e.patterns {
		negate := strings.HasPrefix(p, "!")
		p = strings.TrimPrefix(p, "!")
		var ok bool
		if strings.HasPrefix(p, "|1|") {
			ok = hashedMatches(p, name)
		} else {
			ok = matchKnownPattern(p, name)
		}
		if ok && negate {
			return false
		}
		if ok {
			hit = true
		}
	}
	return hit
}

func hashedMatches(p, name string) bool {
	parts := strings.Split(p, "|")
	if len(parts) != 4 {
		return false
	}
	salt, err1 := base64.StdEncoding.DecodeString(parts[2])
	want, err2 := base64.StdEncoding.DecodeString(parts[3])
	if err1 != nil || err2 != nil {
		return false
	}
	mac := hmac.New(sha1.New, salt)
	mac.Write([]byte(name))
	return hmac.Equal(mac.Sum(nil), want)
}

// matchKnownPattern is a known_hosts pattern: * and ? wildcards, case
// insensitive. Brackets are literal.
func matchKnownPattern(p, name string) bool {
	p, name = strings.ToLower(p), strings.ToLower(name)
	return globMatch(p, name)
}

// globMatch matches * and ? only.
func globMatch(p, s string) bool {
	for len(p) > 0 {
		switch p[0] {
		case '*':
			for i := 0; i <= len(s); i++ {
				if globMatch(p[1:], s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if len(s) == 0 {
				return false
			}
			p, s = p[1:], s[1:]
		default:
			if len(s) == 0 || p[0] != s[0] {
				return false
			}
			p, s = p[1:], s[1:]
		}
	}
	return len(s) == 0
}

func sameKey(a, b ssh.PublicKey) bool {
	return bytes.Equal(a.Marshal(), b.Marshal())
}

// algorithms are the host key algorithms to offer for name: those of the
// keys on record, so a server that prefers another type is not taken for
// one whose key changed, and the certificate algorithms first when a
// certificate authority covers the host. Nil when nothing is known, which
// leaves the library's default list.
func (h *hostKeys) algorithms(name string) []string {
	entries, err := h.load()
	if err != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(a ...string) {
		for _, x := range a {
			if !seen[x] {
				seen[x] = true
				out = append(out, x)
			}
		}
	}
	for _, e := range entries {
		if e.marker == "@cert-authority" && entryMatches(e, name) {
			add(ssh.CertAlgoED25519v01, ssh.CertAlgoECDSA256v01, ssh.CertAlgoECDSA384v01, ssh.CertAlgoECDSA521v01, ssh.CertAlgoRSASHA512v01, ssh.CertAlgoRSASHA256v01)
		}
	}
	for _, e := range entries {
		if e.marker != "" || !entryMatches(e, name) {
			continue
		}
		switch e.key.Type() {
		case ssh.KeyAlgoRSA:
			add(ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA)
		default:
			add(e.key.Type())
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// HostKeyQuestion is what the person is asked about a host with no key
// on record.
type HostKeyQuestion struct {
	Host        string `json:"host"`
	KeyType     string `json:"key_type"`
	Fingerprint string `json:"fingerprint"`
}

// callback checks a host's key against name (known_hosts' name for the
// host: the HostKeyAlias, or the target's host and port), whatever
// address the bytes came from. ask puts an unknown host's key to the
// person and returns whether they accepted it.
func (h *hostKeys) callback(ctx context.Context, name string, ask func(context.Context, HostKeyQuestion) (bool, error)) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		entries, err := h.load()
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.marker == "@revoked" && sameKey(e.key, key) {
				return fmt.Errorf("the host's key is marked revoked in %s:%d", e.file, e.line)
			}
		}
		if cert, ok := key.(*ssh.Certificate); ok {
			for _, e := range entries {
				if e.marker == "@revoked" && sameKey(e.key, cert.SignatureKey) {
					return fmt.Errorf("the host's certificate authority is marked revoked in %s:%d", e.file, e.line)
				}
			}
			checker := &ssh.CertChecker{
				IsHostAuthority: func(auth ssh.PublicKey, _ string) bool {
					for _, e := range entries {
						if e.marker == "@cert-authority" && sameKey(e.key, auth) && entryMatches(e, name) {
							return true
						}
					}
					return false
				},
			}
			host, _, _ := net.SplitHostPort(name)
			if host == "" {
				host = strings.Trim(name, "[]")
			}
			if err := checker.CheckHostKey(net.JoinHostPort(host, "22"), nil, key); err != nil {
				return fmt.Errorf("the host's certificate was not accepted: %w", err)
			}
			return nil
		}
		var want []string
		for _, e := range entries {
			if e.marker != "" || !entryMatches(e, name) {
				continue
			}
			if sameKey(e.key, key) {
				return nil
			}
			want = append(want, fmt.Sprintf("%s %s (%s:%d)", e.key.Type(), ssh.FingerprintSHA256(e.key), e.file, e.line))
		}
		if len(want) > 0 {
			return fmt.Errorf("%w: %s presented %s %s, but the key on record is %s. Someone may be intercepting the connection; if the host was reinstalled, remove the old key from that file",
				ErrHostKeyChanged, name, key.Type(), ssh.FingerprintSHA256(key), strings.Join(want, "; "))
		}
		ok, err := ask(ctx, HostKeyQuestion{Host: name, KeyType: key.Type(), Fingerprint: ssh.FingerprintSHA256(key)})
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("the host key of %s was not accepted", name)
		}
		return h.remember(name, key)
	}
}

// remember writes an accepted key to localcode's own file.
func (h *hostKeys) remember(name string, key ssh.PublicKey) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(h.own), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(h.own, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	line := name + " " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	if _, err := f.WriteString(line + "\n"); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
