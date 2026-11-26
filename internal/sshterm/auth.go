package sshterm

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Authentication.
//
// In OpenSSH's order: the agent's keys, then key files, then
// keyboard-interactive, then a password. Every secret (a password, a
// passphrase, an answer to a server's prompt) is asked of the person in
// the panel and goes from that answer to the library and nowhere else:
// not into the conversation, an event, a log or a file.

// QuestionKind is what a question asks.
type QuestionKind string

const (
	AskHostKey    QuestionKind = "host_key"
	AskPassword   QuestionKind = "password"
	AskPassphrase QuestionKind = "passphrase"
	AskPrompts    QuestionKind = "prompts"
)

// Prompt is one line a server asks in keyboard-interactive
// authentication. Echo is whether the answer may be shown as typed.
type Prompt struct {
	Text string `json:"text"`
	Echo bool   `json:"echo"`
}

// Question is something only the person can answer.
type Question struct {
	ID   int          `json:"id"`
	Kind QuestionKind `json:"kind"`
	// For is the connection asking: user@host:port.
	For         string           `json:"for"`
	HostKey     *HostKeyQuestion `json:"host_key,omitempty"`
	Key         string           `json:"key,omitempty"`
	Name        string           `json:"name,omitempty"`
	Instruction string           `json:"instruction,omitempty"`
	Prompts     []Prompt         `json:"prompts,omitempty"`
}

// Answer is the person's answer. Values are secrets: whoever holds one
// clears it when done (see wipe).
type Answer struct {
	Accept bool
	Values [][]byte
}

// asker puts a question to the person and waits for the answer.
type asker func(ctx context.Context, q Question) (Answer, error)

// errNotAnswered is a question the person dismissed.
var errNotAnswered = errors.New("not answered")

func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// defaultKeyFiles are tried when the configuration names none.
var defaultKeyFiles = []string{"id_ed25519", "id_ecdsa", "id_rsa"}

// authMethods builds the methods for t, in order. agentConn, when not
// nil, is the agent's connection, closed by the caller after the
// handshake.
func authMethods(ctx context.Context, t Target, home string, ag agent.ExtendedAgent, ask asker) []ssh.AuthMethod {
	var methods []ssh.AuthMethod
	signers := func() ([]ssh.Signer, error) {
		var out []ssh.Signer
		var agentKeys []ssh.Signer
		if ag != nil {
			if s, err := ag.Signers(); err == nil {
				agentKeys = s
			}
		}
		files := t.IdentityFiles
		if len(files) == 0 {
			for _, f := range defaultKeyFiles {
				files = append(files, filepath.Join(home, ".ssh", f))
			}
		}
		var fileKeys []ssh.Signer
		for _, f := range files {
			if s := fileSigner(ctx, f, t, ask); s != nil {
				fileKeys = append(fileKeys, s)
			}
		}
		certs := certificates(t, files)
		if t.IdentitiesOnly {
			// Only the agent's keys that are also a named file's.
			want := map[string]bool{}
			for _, s := range fileKeys {
				want[string(s.PublicKey().Marshal())] = true
			}
			for _, s := range agentKeys {
				if want[string(s.PublicKey().Marshal())] {
					out = append(out, s)
				}
			}
		} else {
			out = append(out, agentKeys...)
		}
		// A file's key the agent already offered is not offered twice.
		have := map[string]bool{}
		for _, s := range out {
			have[string(s.PublicKey().Marshal())] = true
		}
		for _, s := range fileKeys {
			if !have[string(s.PublicKey().Marshal())] {
				out = append(out, s)
			}
		}
		// A certificate goes first, with the key it certifies, as
		// OpenSSH offers it.
		var withCerts []ssh.Signer
		for _, c := range certs {
			for _, s := range out {
				if bytes.Equal(s.PublicKey().Marshal(), c.Key.Marshal()) {
					if cs, err := ssh.NewCertSigner(c, s); err == nil {
						withCerts = append(withCerts, cs)
					}
					break
				}
			}
		}
		return append(withCerts, out...), nil
	}
	methods = append(methods, ssh.PublicKeysCallback(signers))
	methods = append(methods, ssh.RetryableAuthMethod(ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
		if len(questions) == 0 {
			return nil, nil
		}
		q := Question{Kind: AskPrompts, For: t.String(), Name: name, Instruction: instruction}
		for i, text := range questions {
			q.Prompts = append(q.Prompts, Prompt{Text: text, Echo: i < len(echos) && echos[i]})
		}
		a, err := ask(ctx, q)
		if err != nil {
			return nil, err
		}
		out := make([]string, len(questions))
		for i := range out {
			if i < len(a.Values) {
				out[i] = string(a.Values[i])
				wipe(a.Values[i])
			}
		}
		return out, nil
	}), 3))
	methods = append(methods, ssh.RetryableAuthMethod(ssh.PasswordCallback(func() (string, error) {
		a, err := ask(ctx, Question{Kind: AskPassword, For: t.String()})
		if err != nil {
			return "", err
		}
		if len(a.Values) == 0 {
			return "", errNotAnswered
		}
		pw := string(a.Values[0])
		wipe(a.Values[0])
		return pw, nil
	}), 3))
	return methods
}

// fileSigner is the key in file, or nil when there is none to use. An
// encrypted key with its .pub beside it is offered by its public half and
// asks for the passphrase only when the server has accepted it, as
// OpenSSH does; one without a .pub asks at once.
func fileSigner(ctx context.Context, file string, t Target, ask asker) ssh.Signer {
	pem, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	s, err := ssh.ParsePrivateKey(pem)
	if err == nil {
		return s
	}
	var missing *ssh.PassphraseMissingError
	if !errors.As(err, &missing) {
		return nil
	}
	pub := missing.PublicKey
	if pub == nil {
		if b, err := os.ReadFile(file + ".pub"); err == nil {
			if k, _, _, _, err := ssh.ParseAuthorizedKey(b); err == nil {
				pub = k
			}
		}
	}
	l := &lazySigner{ctx: ctx, file: file, pem: pem, pub: pub, ask: ask, target: t}
	if pub == nil {
		if err := l.unlock(); err != nil {
			return nil
		}
		return l.inner
	}
	return l
}

// lazySigner is an encrypted key file's key, unlocked on its first
// signature.
type lazySigner struct {
	ctx    context.Context
	file   string
	pem    []byte
	pub    ssh.PublicKey
	ask    asker
	target Target

	mu    sync.Mutex
	inner ssh.Signer
	err   error
}

func (l *lazySigner) unlock() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inner != nil || l.err != nil {
		return l.err
	}
	for tries := 0; tries < 3; tries++ {
		a, err := l.ask(l.ctx, Question{Kind: AskPassphrase, For: l.target.String(), Key: l.file})
		if err != nil {
			l.err = err
			return err
		}
		if len(a.Values) == 0 {
			l.err = errNotAnswered
			return l.err
		}
		s, err := ssh.ParsePrivateKeyWithPassphrase(l.pem, a.Values[0])
		wipe(a.Values[0])
		if err == nil {
			l.inner = s
			return nil
		}
		if !errors.Is(err, x509.IncorrectPasswordError) {
			l.err = err
			return err
		}
	}
	l.err = fmt.Errorf("%s: wrong passphrase", l.file)
	return l.err
}

func (l *lazySigner) PublicKey() ssh.PublicKey { return l.pub }

func (l *lazySigner) Sign(r io.Reader, data []byte) (*ssh.Signature, error) {
	if err := l.unlock(); err != nil {
		return nil, err
	}
	return l.inner.Sign(r, data)
}

func (l *lazySigner) SignWithAlgorithm(r io.Reader, data []byte, algorithm string) (*ssh.Signature, error) {
	if err := l.unlock(); err != nil {
		return nil, err
	}
	if as, ok := l.inner.(ssh.AlgorithmSigner); ok {
		return as.SignWithAlgorithm(r, data, algorithm)
	}
	return l.inner.Sign(r, data)
}

// certificates are the certificates CertificateFile names, and the
// "-cert.pub" beside each key file, as OpenSSH finds them.
func certificates(t Target, keyFiles []string) []*ssh.Certificate {
	files := append([]string(nil), t.CertificateFiles...)
	for _, f := range keyFiles {
		files = append(files, f+"-cert.pub")
	}
	var out []*ssh.Certificate
	seen := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		k, _, _, _, err := ssh.ParseAuthorizedKey(b)
		if err != nil {
			continue
		}
		c, ok := k.(*ssh.Certificate)
		if !ok || seen[string(c.Marshal())] {
			continue
		}
		seen[string(c.Marshal())] = true
		out = append(out, c)
	}
	return out
}
