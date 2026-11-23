package sshterm

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Reading ~/.ssh/config.
//
// Our own reader rather than a library: the one library there is refuses
// a whole file over one "Match exec" or "Match user" line, which takes
// every alias with it, and resolves Include against the home directory of
// the real user, which a test with a home of its own must not touch.
//
// What is read: Host blocks, and Match blocks whose criteria are all,
// host, originalhost, user or localuser. A Match block with any other
// criterion (exec, canonical, final, tagged, address, localnetwork) never
// matches. "Match exec" is never run: it would run a local command, and
// localcode starts no process for SSH. Include is followed, relative to
// ~/.ssh, at most 16 deep. The first value of a keyword wins, as in
// OpenSSH, except IdentityFile and UserKnownHostsFile, which add up.

// Target is where a connection goes, after ~/.ssh/config.
type Target struct {
	// Alias is what was asked for: a Host alias or a host name.
	Alias string
	// Host is the name or address connected to (HostName).
	Host string
	User string
	Port int
	// IdentityFiles are the key files to try, after the agent's keys.
	// Empty means the default ones.
	IdentityFiles  []string
	IdentitiesOnly bool
	// ProxyJump are the hosts to go through, in order, as written.
	ProxyJump []string
	// ProxyCommand is set when the configuration wants a local command
	// to carry the connection. localcode does not run one.
	ProxyCommand string
	// KnownHostsFiles are UserKnownHostsFile's files, when set.
	KnownHostsFiles []string
	// GlobalKnownHostsFiles are GlobalKnownHostsFile's, when set.
	GlobalKnownHostsFiles []string
	// HostKeyAlias is the name host keys are recorded under, when set.
	HostKeyAlias string
	// IdentityAgent is the agent socket to use: "" for SSH_AUTH_SOCK,
	// "none" for no agent, or a path.
	IdentityAgent string
	// CertificateFiles are certificates to present with the keys they
	// certify.
	CertificateFiles []string
	AliveInterval    time.Duration
	AliveCountMax    int
	ConnectTimeout   time.Duration
}

// Address is host:port for dialing.
func (t Target) Address() string {
	return net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
}

// KnownName is the name the host's key is recorded under.
func (t Target) KnownName() string {
	if t.HostKeyAlias != "" {
		return t.HostKeyAlias
	}
	return knownName(t.Host, t.Port)
}

// String is user@host:port.
func (t Target) String() string {
	return t.User + "@" + t.Address()
}

// Spec is a target as a person or the model writes it: [user@]host[:port],
// [user@][v6]:port, or ssh://[user@]host[:port].
type Spec struct {
	User string
	Host string
	Port int
}

// ParseSpec reads a target as written.
func ParseSpec(s string) (Spec, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "ssh://")
	s = strings.TrimSuffix(s, "/")
	var sp Spec
	if i := strings.LastIndex(s, "@"); i >= 0 {
		sp.User, s = s[:i], s[i+1:]
		if sp.User == "" {
			return Spec{}, errors.New("an empty user name before @")
		}
	}
	host := s
	switch {
	case strings.HasPrefix(s, "["):
		end := strings.Index(s, "]")
		if end < 0 {
			return Spec{}, fmt.Errorf("%q: no ] after the address", s)
		}
		host = s[1:end]
		if rest := s[end+1:]; rest != "" {
			if !strings.HasPrefix(rest, ":") {
				return Spec{}, fmt.Errorf("%q: unexpected text after ]", s)
			}
			p, err := parsePort(rest[1:])
			if err != nil {
				return Spec{}, err
			}
			sp.Port = p
		}
	case strings.Count(s, ":") == 1:
		i := strings.Index(s, ":")
		host = s[:i]
		p, err := parsePort(s[i+1:])
		if err != nil {
			return Spec{}, err
		}
		sp.Port = p
	}
	if host == "" {
		return Spec{}, errors.New("no host")
	}
	if strings.ContainsAny(host, " \t\r\n/\\") {
		return Spec{}, fmt.Errorf("%q is not a host name", host)
	}
	// Host names are not case sensitive, and OpenSSH lowers them before
	// it looks them up: a rule or a known_hosts entry written for
	// "prod.example.com" covers "PROD.example.com".
	sp.Host = strings.ToLower(host)
	return sp, nil
}

func parsePort(s string) (int, error) {
	p, err := strconv.Atoi(s)
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("%q is not a port", s)
	}
	return p, nil
}

// block is one Host or Match block, with its lines.
type block struct {
	// hosts are a Host line's patterns. Empty for a Match block.
	hosts []string
	// match is a Match line's criteria, as words. Empty for a Host block.
	match []string
	kvs   []kv
}

type kv struct {
	key  string // lower case
	args []string
}

// sshConfig is a parsed configuration: blocks in file order, the lines
// before the first Host or Match in a block that always matches.
type sshConfig struct {
	blocks []*block
	// warnings are lines that were skipped, kept to say why an alias
	// does not do what its file says.
	warnings []string
}

const maxIncludeDepth = 16

// loadConfig reads home/.ssh/config, then systemDir/ssh_config (as
// OpenSSH does: the first value found wins, so the person's file comes
// first). A missing file is an empty configuration.
func loadConfig(home, systemDir string) (*sshConfig, error) {
	c := &sshConfig{}
	user := &block{hosts: []string{"*"}}
	c.blocks = append(c.blocks, user)
	if err := c.readFile(home, filepath.Join(home, ".ssh"), filepath.Join(home, ".ssh", "config"), 0, user); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if systemDir != "" {
		sys := &block{hosts: []string{"*"}}
		c.blocks = append(c.blocks, sys)
		if err := c.readFile(home, systemDir, filepath.Join(systemDir, "ssh_config"), 0, sys); err != nil && !errors.Is(err, os.ErrNotExist) {
			c.warnings = append(c.warnings, err.Error())
		}
	}
	return c, nil
}

// readFile adds path's lines to c. Lines before a Host or Match in this
// file go to cur, the block the Include that named the file was in. A
// relative Include is under dir.
func (c *sshConfig) readFile(home, dir, path string, depth int, cur *block) error {
	if depth > maxIncludeDepth {
		return fmt.Errorf("%s: Include nested more than %d deep", path, maxIncludeDepth)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		key, args, err := splitLine(sc.Text())
		if err != nil {
			c.warnings = append(c.warnings, fmt.Sprintf("%s:%d: %v", path, line, err))
			continue
		}
		switch key {
		case "":
			continue
		case "host":
			cur = &block{hosts: args}
			c.blocks = append(c.blocks, cur)
		case "match":
			cur = &block{match: args}
			c.blocks = append(c.blocks, cur)
		case "include":
			for _, pattern := range args {
				p := expandHome(pattern, home)
				if !filepath.IsAbs(p) {
					p = filepath.Join(dir, p)
				}
				files, err := filepath.Glob(p)
				if err != nil {
					c.warnings = append(c.warnings, fmt.Sprintf("%s:%d: %v", path, line, err))
					continue
				}
				for _, inc := range files {
					// The included file's own Host lines start blocks of
					// their own; its lines before them are this block's.
					// After it, this file goes on in this block, as in
					// OpenSSH.
					if err := c.readFile(home, dir, inc, depth+1, cur); err != nil {
						if errors.Is(err, os.ErrNotExist) {
							continue
						}
						return err
					}
				}
			}
		default:
			cur.kvs = append(cur.kvs, kv{key: key, args: args})
		}
	}
	return sc.Err()
}

// splitLine reads one line: a keyword and its arguments, "" for a blank
// line or a comment. "Key value", "Key=value" and "Key = value" are all
// one keyword and value; arguments may be double-quoted.
func splitLine(s string) (string, []string, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "#") {
		return "", nil, nil
	}
	i := strings.IndexAny(s, " \t=")
	if i < 0 {
		return strings.ToLower(s), nil, nil
	}
	key := strings.ToLower(s[:i])
	rest := strings.TrimLeft(s[i:], " \t")
	if strings.HasPrefix(rest, "=") {
		rest = strings.TrimLeft(rest[1:], " \t")
	}
	var args []string
	for rest != "" {
		if strings.HasPrefix(rest, "#") {
			break
		}
		if rest[0] == '"' {
			end := strings.Index(rest[1:], `"`)
			if end < 0 {
				return "", nil, fmt.Errorf("unterminated quote")
			}
			args = append(args, rest[1:1+end])
			rest = strings.TrimLeft(rest[2+end:], " \t")
			continue
		}
		end := strings.IndexAny(rest, " \t")
		if end < 0 {
			args = append(args, rest)
			break
		}
		args = append(args, rest[:end])
		rest = strings.TrimLeft(rest[end:], " \t")
	}
	return key, args, nil
}

// matchPattern is ssh_config's pattern: * and ? wildcards, case
// insensitive.
func matchPattern(pattern, s string) bool {
	ok, err := filepath.Match(strings.ToLower(pattern), strings.ToLower(s))
	return err == nil && ok && !strings.ContainsAny(s, "/")
}

// hostMatches reports whether a Host line's patterns take name: one
// pattern matches and no negated pattern does. Patterns may be split by
// commas as well as spaces.
func hostMatches(patterns []string, name string) bool {
	hit := false
	for _, field := range patterns {
		for _, p := range strings.Split(field, ",") {
			if p == "" {
				continue
			}
			if strings.HasPrefix(p, "!") {
				if matchPattern(p[1:], name) {
					return false
				}
				continue
			}
			if matchPattern(p, name) {
				hit = true
			}
		}
	}
	return hit
}

// matchApplies evaluates a Match line's criteria. Any criterion this
// reader does not evaluate makes the block not match: a block meant for
// a condition we cannot check is left out rather than applied to every
// host.
func matchApplies(words []string, alias, host, user, localUser string) bool {
	if len(words) == 0 {
		return false
	}
	for i := 0; i < len(words); i++ {
		w := strings.ToLower(words[i])
		negate := strings.HasPrefix(w, "!")
		w = strings.TrimPrefix(w, "!")
		if w == "all" {
			if negate {
				return false
			}
			continue
		}
		if i+1 >= len(words) {
			return false
		}
		arg := words[i+1]
		i++
		var against string
		switch w {
		case "host":
			against = host
		case "originalhost":
			against = alias
		case "user":
			against = user
		case "localuser":
			against = localUser
		default:
			return false
		}
		if hostMatches([]string{arg}, against) == negate {
			return false
		}
	}
	return true
}

// resolve applies the configuration to what was asked for.
func (c *sshConfig) resolve(spec Spec, localUser, home string) Target {
	t := Target{Alias: spec.Host, Host: spec.Host, User: spec.User, Port: spec.Port}
	seen := map[string]bool{}
	first := func(key string) bool {
		if seen[key] {
			return false
		}
		seen[key] = true
		return true
	}
	for _, b := range c.blocks {
		if b.hosts != nil {
			if !hostMatches(b.hosts, spec.Host) {
				continue
			}
		} else if !matchApplies(b.match, spec.Host, t.Host, t.User, localUser) {
			continue
		}
		for _, e := range b.kvs {
			if len(e.args) == 0 {
				continue
			}
			v := e.args[0]
			switch e.key {
			case "hostname":
				if first(e.key) {
					t.Host = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(v, "%h", spec.Host), "%%", "%"))
				}
			case "user":
				if first(e.key) && t.User == "" {
					t.User = v
				}
			case "port":
				if first(e.key) && t.Port == 0 {
					if p, err := parsePort(v); err == nil {
						t.Port = p
					}
				}
			case "identityfile":
				for _, a := range e.args {
					if strings.EqualFold(a, "none") {
						continue
					}
					t.IdentityFiles = append(t.IdentityFiles, a)
				}
			case "identitiesonly":
				if first(e.key) {
					t.IdentitiesOnly = strings.EqualFold(v, "yes")
				}
			case "proxyjump":
				if first(e.key) && !strings.EqualFold(v, "none") {
					for _, hop := range strings.Split(v, ",") {
						if hop = strings.TrimSpace(hop); hop != "" {
							t.ProxyJump = append(t.ProxyJump, hop)
						}
					}
				}
			case "proxycommand":
				if first(e.key) && !strings.EqualFold(v, "none") {
					t.ProxyCommand = strings.Join(e.args, " ")
				}
			case "userknownhostsfile":
				if first(e.key) {
					for _, a := range e.args {
						if !strings.EqualFold(a, "none") {
							t.KnownHostsFiles = append(t.KnownHostsFiles, a)
						}
					}
				}
			case "globalknownhostsfile":
				if first(e.key) {
					for _, a := range e.args {
						if !strings.EqualFold(a, "none") {
							t.GlobalKnownHostsFiles = append(t.GlobalKnownHostsFiles, a)
						}
					}
				}
			case "hostkeyalias":
				if first(e.key) {
					t.HostKeyAlias = strings.ToLower(v)
				}
			case "identityagent":
				if first(e.key) {
					t.IdentityAgent = v
				}
			case "certificatefile":
				for _, a := range e.args {
					if !strings.EqualFold(a, "none") {
						t.CertificateFiles = append(t.CertificateFiles, a)
					}
				}
			case "serveraliveinterval":
				if first(e.key) {
					if n, err := strconv.Atoi(v); err == nil && n > 0 {
						t.AliveInterval = time.Duration(n) * time.Second
					}
				}
			case "serveralivecountmax":
				if first(e.key) {
					if n, err := strconv.Atoi(v); err == nil && n > 0 {
						t.AliveCountMax = n
					}
				}
			case "connecttimeout":
				if first(e.key) {
					if n, err := strconv.Atoi(v); err == nil && n > 0 {
						t.ConnectTimeout = time.Duration(n) * time.Second
					}
				}
			}
		}
	}
	if t.User == "" {
		t.User = localUser
	}
	if t.Port == 0 {
		t.Port = 22
	}
	if t.AliveInterval == 0 {
		t.AliveInterval = 30 * time.Second
	}
	if t.AliveCountMax == 0 {
		t.AliveCountMax = 3
	}
	if t.ConnectTimeout == 0 {
		t.ConnectTimeout = 15 * time.Second
	}
	for i, f := range t.IdentityFiles {
		t.IdentityFiles[i] = expandTokens(f, t, home, localUser)
	}
	for i, f := range t.KnownHostsFiles {
		t.KnownHostsFiles[i] = expandTokens(f, t, home, localUser)
	}
	for i, f := range t.GlobalKnownHostsFiles {
		t.GlobalKnownHostsFiles[i] = expandTokens(f, t, home, localUser)
	}
	for i, f := range t.CertificateFiles {
		t.CertificateFiles[i] = expandTokens(f, t, home, localUser)
	}
	switch {
	case t.IdentityAgent == "", strings.EqualFold(t.IdentityAgent, "none"), t.IdentityAgent == "SSH_AUTH_SOCK", t.IdentityAgent == "$SSH_AUTH_SOCK":
		if t.IdentityAgent == "$SSH_AUTH_SOCK" {
			t.IdentityAgent = "SSH_AUTH_SOCK"
		}
	case strings.HasPrefix(t.IdentityAgent, "$"):
		t.IdentityAgent = os.Getenv(strings.Trim(t.IdentityAgent[1:], "{}"))
	default:
		t.IdentityAgent = expandTokens(t.IdentityAgent, t, home, localUser)
	}
	return t
}

// expandTokens expands ~ and the % tokens a file name may carry.
func expandTokens(s string, t Target, home, localUser string) string {
	s = expandHome(s, home)
	r := strings.NewReplacer(
		"%%", "%",
		"%d", home,
		"%h", t.Host,
		"%n", t.Alias,
		"%p", strconv.Itoa(t.Port),
		"%r", t.User,
		"%u", localUser,
	)
	s = r.Replace(s)
	if !filepath.IsAbs(s) {
		s = filepath.Join(home, s)
	}
	// One separator throughout: "%d/.ssh/key" on Windows is the home's
	// backslashes and the config's slash.
	return filepath.Clean(s)
}

func expandHome(s, home string) string {
	if s == "~" {
		return home
	}
	if strings.HasPrefix(s, "~/") || strings.HasPrefix(s, `~\`) {
		return filepath.Join(home, s[2:])
	}
	return s
}
