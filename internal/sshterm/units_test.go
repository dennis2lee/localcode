package sshterm

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"golang.org/x/crypto/ssh"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestParseSpec(t *testing.T) {
	for in, want := range map[string]Spec{
		"host":               {Host: "host"},
		"u@host":             {User: "u", Host: "host"},
		"u@host:2222":        {User: "u", Host: "host", Port: 2222},
		"ssh://u@host:2222/": {User: "u", Host: "host", Port: 2222},
		"[::1]:22":           {Host: "::1", Port: 22},
		"fe80::1":            {Host: "fe80::1"},
		"a.b@c@host":         {User: "a.b@c", Host: "host"},
	} {
		got, err := ParseSpec(in)
		if err != nil || got != want {
			t.Errorf("%q: %+v %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "@host", "host:0", "host:x", "ho st", "host\nrm", "[::1", "a/b"} {
		if _, err := ParseSpec(bad); err == nil {
			t.Errorf("%q was taken", bad)
		}
	}
}

func TestConfigIsReadAsOpenSSHReadsIt(t *testing.T) {
	home := t.TempDir()
	sys := t.TempDir()
	writeFile(t, filepath.Join(home, ".ssh", "config"), `
# comment
User toplevel

Host prod-* !prod-skip
  HostName %h.example.com
  User deploy
  Port 2200
  IdentityFile ~/.ssh/prod_key
  IdentityFile %d/.ssh/second
  ProxyJump bastion
  ServerAliveInterval 5

Match exec "touch /tmp/should-never-run"
  User exec-matched

Match user deploy host prod-db.example.com
  Port 2300

Match originalhost=alias
  User from-match

Include conf.d/*

Host *
  User everyone
  Port 2222
`)
	writeFile(t, filepath.Join(home, ".ssh", "conf.d", "extra"), "Host inc\n  HostName included.example.com\n")
	writeFile(t, filepath.Join(sys, "ssh_config"), "Host *\n  ConnectTimeout 7\n  User system\n")
	c, err := loadConfig(home, sys)
	if err != nil {
		t.Fatal(err)
	}
	r := func(s string) Target {
		sp, err := ParseSpec(s)
		if err != nil {
			t.Fatal(err)
		}
		return c.resolve(sp, "local", home)
	}

	got := r("prod-db")
	if got.Host != "prod-db.example.com" || got.User != "toplevel" || got.Port != 2200 {
		// A User line before any Host applies to every host, and the first value wins.
		t.Fatalf("prod-db: %+v", got)
	}
	if !reflect.DeepEqual(got.IdentityFiles, []string{filepath.Join(home, ".ssh", "prod_key"), filepath.Join(home, ".ssh", "second")}) {
		t.Fatalf("identity files %v", got.IdentityFiles)
	}
	if !reflect.DeepEqual(got.ProxyJump, []string{"bastion"}) || got.AliveInterval != 5*time.Second || got.ConnectTimeout != 7*time.Second {
		t.Fatalf("prod-db: %+v", got)
	}
	if _, err := os.Stat("/tmp/should-never-run"); err == nil {
		t.Fatal("Match exec ran")
	}
	if got := r("prod-skip"); got.Host != "prod-skip" || got.Port != 2222 {
		t.Fatalf("negated: %+v", got)
	}
	if got := r("inc"); got.Host != "included.example.com" {
		t.Fatalf("include: %+v", got)
	}
	if got := r("bob@other:99"); got.User != "bob" || got.Port != 99 {
		t.Fatalf("what was asked wins: %+v", got)
	}

	// No file at all: defaults.
	empty, err := loadConfig(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := empty.resolve(Spec{Host: "h"}, "local", home); got.User != "local" || got.Port != 22 || got.AliveCountMax != 3 {
		t.Fatalf("defaults %+v", got)
	}
}

func TestConfigMatchUserAndAgentKeys(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".ssh", "config"), `
Host db
  User admin
Match user admin
  Port 4022
  IdentityAgent ~/agent.sock
  HostKeyAlias db-alias
  CertificateFile ~/.ssh/cert.pub
  GlobalKnownHostsFile /dev/null
Match localuser nobody-local
  Port 1
`)
	c, _ := loadConfig(home, "")
	got := c.resolve(Spec{Host: "db"}, "local", home)
	if got.Port != 4022 || got.IdentityAgent != filepath.Join(home, "agent.sock") || got.KnownName() != "db-alias" ||
		len(got.CertificateFiles) != 1 || len(got.GlobalKnownHostsFiles) != 1 {
		t.Fatalf("%+v", got)
	}
}

func hashedName(t *testing.T, name string) string {
	salt := []byte("0123456789abcdefghij")
	mac := hmac.New(sha1.New, salt)
	mac.Write([]byte(name))
	return "|1|" + base64.StdEncoding.EncodeToString(salt) + "|" + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func TestKnownHostsAreReadLineByLine(t *testing.T) {
	key := newSigner(t).PublicKey()
	other := newSigner(t).PublicKey()
	ca := newSigner(t)
	home := t.TempDir()
	data := t.TempDir()
	auth := func(k ssh.PublicKey) string { return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))) }
	writeFile(t, filepath.Join(home, ".ssh", "known_hosts"), strings.Join([]string{
		"this line is not a known_hosts line",
		"plain.example " + auth(key),
		hashedName(t, "[hashed.example]:2200") + " " + auth(key),
		"*.wild.example,!no.wild.example " + auth(key),
		"@revoked * " + auth(other),
		"@cert-authority *.certs.example " + auth(ca.PublicKey()),
	}, "\n"))
	h := newHostKeys(Options{Home: home, DataDir: data}, Target{})
	never := func(context.Context, HostKeyQuestion) (bool, error) {
		t.Fatal("asked")
		return false, nil
	}
	check := func(name string, k ssh.PublicKey) error {
		return h.callback(context.Background(), name, never)("", nil, k)
	}
	for _, name := range []string{"plain.example", "[hashed.example]:2200", "a.wild.example"} {
		if err := check(name, key); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := check("plain.example", newSigner(t).PublicKey()); err == nil || !strings.Contains(err.Error(), ErrHostKeyChanged.Error()) {
		t.Errorf("changed: %v", err)
	}
	if err := check("anything", other); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Errorf("revoked: %v", err)
	}

	// A host certificate signed by the authority.
	cert := &ssh.Certificate{Key: newSigner(t).PublicKey(), CertType: ssh.HostCert, ValidPrincipals: []string{"h.certs.example"}, ValidBefore: ssh.CertTimeInfinity}
	if err := cert.SignCert(rand.Reader, ca); err != nil {
		t.Fatal(err)
	}
	if err := check("h.certs.example", cert); err != nil {
		t.Errorf("certificate: %v", err)
	}
	if algs := h.algorithms("h.certs.example"); len(algs) == 0 || !strings.Contains(algs[0], "cert") {
		t.Errorf("algorithms for a certified host: %v", algs)
	}

	// Unknown hosts are asked; the negated name is unknown.
	asked := ""
	ask := func(_ context.Context, q HostKeyQuestion) (bool, error) { asked = q.Host; return true, nil }
	if err := h.callback(context.Background(), "no.wild.example", ask)("", nil, key); err != nil || asked != "no.wild.example" {
		t.Errorf("unknown: %v %q", err, asked)
	}
	if err := check("no.wild.example", key); err != nil {
		t.Errorf("remembered: %v", err)
	}
	if algs := h.algorithms("plain.example"); len(algs) != 1 || algs[0] != ssh.KeyAlgoED25519 {
		t.Errorf("algorithms %v", algs)
	}
}

func TestKeysAreEncodedAsXtermDoes(t *testing.T) {
	for in, want := range map[string]string{
		"ctrl+left":   "\x1b[1;5D",
		"shift+up":    "\x1b[1;2A",
		"ctrl+Delete": "\x1b[3;5~",
		"shift+F5":    "\x1b[15;2~",
		"shift+Tab":   "\x1b[Z",
		"ctrl+/":      "\x1f",
		"alt+Enter":   "\x1b\r",
	} {
		evs, err := ParseKeys(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		got, ok := encodeSpecial(evs[0])
		if !ok || got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
	evs, err := ParseKeys("Up Up Enter")
	if err != nil || len(evs) != 3 || evs[0].Code != uv.KeyUp || evs[2].Code != uv.KeyEnter {
		t.Fatalf("%v %v", evs, err)
	}
	if evs, _ := ParseKeys("ctrl+C"); evs[0].Code != 'c' || evs[0].Mod != uv.ModCtrl {
		t.Fatalf("ctrl+C: %+v", evs[0])
	}
	for _, bad := range []string{"", "hyper+x", "ctrl+nokey"} {
		if _, err := ParseKeys(bad); err == nil {
			t.Errorf("%q was taken", bad)
		}
	}
	// The panel's keys.
	if ev, ok := personKeyEvent(PersonKey{Key: "a"}); !ok || ev.Text != "a" {
		t.Fatalf("a: %+v", ev)
	}
	if _, ok := personKeyEvent(PersonKey{Key: "c", Meta: true}); ok {
		t.Fatal("Cmd+C went to the terminal")
	}
	if ev, _ := personKeyEvent(PersonKey{Key: "C", Ctrl: true, Shift: true}); ev.Code != 'c' || ev.Mod != uv.ModCtrl {
		t.Fatalf("ctrl+shift+C: %+v", ev)
	}
}

func TestRenderText(t *testing.T) {
	for in, want := range map[string]string{
		"a\r\nb\r\n":             "a\nb\n",
		"10%\r50%\r100%":         "100%",
		"long line\rshort\x1b[K": "short",
		"abc\b\bX":               "aXc",
		"\x1b[1;31mred\x1b[0m":   "red",
		"\x1b]0;title\x07text":   "text",
		"tab\tx":                 "tab     x",
		"bad \xff byte":          "bad � byte",
	} {
		if got := renderText([]byte(in)); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	c := capture{max: 20}
	c.write([]byte("echo x\r\n" + strings.Repeat("y", 30) + "\r\nend"))
	if got := c.text(true); !strings.Contains(got, "left out") || !strings.HasSuffix(got, "end") {
		t.Errorf("capped: %q", got)
	}
}

func TestMarksNeedTheNonceAndSurviveSplitReads(t *testing.T) {
	s := markScanner{nonce: "abc"}
	stream := "x\x1b]133;D;0;lc=zzz\x07y\x1b]133;C;lc=abc\x1b\\z\x1b]133;D;42;lc=abc\x07"
	var got []markEvent
	for i := 0; i < len(stream); i++ {
		for _, ev := range s.feed([]byte{stream[i]}) {
			ev.end = i
			got = append(got, ev)
		}
	}
	if len(got) != 2 || got[0].kind != 'C' || got[1].kind != 'D' || got[1].status != "42" {
		t.Fatalf("%+v", got)
	}
	if evs := (&markScanner{}).feed([]byte(stream)); len(evs) != 0 {
		t.Fatal("a scanner with no nonce took a mark")
	}
}

func TestCleanTextCannotEndAPasteEarly(t *testing.T) {
	if got := cleanText("a\x1b[201~rm -rf /\x03\r\nb"); got != "a[201~rm -rf /\r\nb" && got != "arm -rf /\r\nb" {
		t.Fatalf("%q", got)
	}
}
