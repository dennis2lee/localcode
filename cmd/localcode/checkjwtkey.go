package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"localcode/internal/config"
	"localcode/internal/provider"
	"localcode/internal/userdirs"
)

// runCheckJWTKey answers "does the JWT I put in config.json work": for each
// OpenAI-compatible provider whose api_key is a JWT, or the one named, it
// says what the token claims (decoded, not verified: the endpoint holds the
// key that checks it), when it expires, where it is written and who can
// read that file, and then asks the endpoint with it: once for its model
// list and once for one short chat reply. The token itself is never
// printed. A key that is not a JWT is not its business.
//
// The chat reply is the answer. A model list can be open to anybody or not
// offered at all, so a 200 there proves less than the request every turn
// will make.
func runCheckJWTKey(args []string) error {
	return checkJWTKey(args, os.Stdout)
}

const checkJWTKeyUsage = "usage: localcode check-jwt-key [provider] [--model M] [--no-chat] [--config path] [--timeout d]"

func checkJWTKey(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("check-jwt-key", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "", "config file to read instead of the merged ~/.localcode and project ones")
	model := fs.String("model", "", "model for the chat check; default: the model of a profile on the provider, else the first one the endpoint lists")
	noChat := fs.Bool("no-chat", false, "only ask for the model list, and send no chat request")
	timeout := fs.Duration("timeout", time.Minute, "how long to wait for each answer")
	// Flags before the provider name, after it, or both: the flag package
	// stops at the first word that is not a flag, so the name is taken out
	// and the rest parsed again.
	//
	// After "--" nothing is a flag: the flag package stops there and drops
	// it, so whatever follows is positional, and parsing it again would
	// read it as flags.
	var name string
	named := false
	for {
		if err := fs.Parse(args); err != nil {
			return err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		if named {
			return errors.New(checkJWTKeyUsage)
		}
		name, named = rest[0], true
		if consumed := len(args) - len(rest); consumed > 0 && args[consumed-1] == "--" {
			if len(rest) > 1 {
				return errors.New(checkJWTKeyUsage)
			}
			break
		}
		args = rest[1:]
	}
	if named && strings.TrimSpace(name) == "" {
		return errors.New("the provider name is empty. " + checkJWTKeyUsage)
	}

	e, err := resolveEnv()
	if err != nil {
		return err
	}
	cfg, err := loadConfig(*configPath, e)
	if err != nil {
		return err
	}
	names, err := jwtProviders(cfg, name)
	if err != nil {
		return err
	}
	files := keyFiles(*configPath, e)
	project := projectConfigFile(e)

	var failed []string
	for i, n := range names {
		if i > 0 {
			fmt.Fprintln(out)
		}
		pc := cfg.Providers[n]
		r := &keyReport{out: out, key: pc.APIKey}
		ok := r.check(n, pc, cfg, files, project, *model, *noChat, *timeout)
		if !ok {
			failed = append(failed, n)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("the key of %s did not work", strings.Join(failed, ", "))
	}
	return nil
}

// jwtProviders is the providers a check covers: the one named, or every
// OpenAI-compatible provider whose api_key is a JWT, in name order.
func jwtProviders(cfg *config.Config, name string) ([]string, error) {
	if name != "" {
		pc, ok := cfg.Providers[name]
		switch {
		case !ok:
			return nil, fmt.Errorf("no provider %q in config. Configured: %s", name, strings.Join(sortedNames(cfg.Providers), ", "))
		case pc.Type != config.ProviderOpenAICompat:
			return nil, fmt.Errorf("provider %q is %s; check-jwt-key checks openai-compat providers, which send api_key as a bearer token", name, pc.Type)
		case strings.TrimSpace(pc.APIKey) == "":
			return nil, fmt.Errorf("provider %q has no api_key to check. Put the JWT in providers.%s.api_key", name, name)
		case !isJWT(pc.APIKey):
			return nil, fmt.Errorf("provider %q's api_key is not a JWT (three dot-separated base64url parts with a header naming its algorithm); check-jwt-key checks JWT keys only", name)
		}
		return []string{name}, nil
	}
	var out []string
	for n, pc := range cfg.Providers {
		if pc.Type == config.ProviderOpenAICompat && isJWT(pc.APIKey) {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no openai-compat provider in config has a JWT in api_key. Put the token in providers.<name>.api_key, or name the provider to check")
	}
	sort.Strings(out)
	return out, nil
}

func isJWT(key string) bool {
	_, ok := provider.ParseJWT(key)
	return ok
}

// keyFiles is the config files a key may be written in, the project one
// first: the files LoadMerged reads, or the one --config names. One file
// when the project is the home directory, where it is not a project's own.
// A file listed under include is not among them.
func keyFiles(explicit string, e env) []string {
	if explicit != "" {
		return []string{explicit}
	}
	if userdirs.SameDirectory(e.cwd, e.home) {
		return []string{filepath.Join(e.home, ".localcode", "config.json")}
	}
	return []string{
		filepath.Join(e.cwd, ".localcode", "config.json"),
		filepath.Join(e.home, ".localcode", "config.json"),
	}
}

// projectConfigFile is the project's own config file, the one committed
// with it, or "" when the project is the home directory and has none of
// its own.
func projectConfigFile(e env) string {
	if userdirs.SameDirectory(e.cwd, e.home) {
		return ""
	}
	return filepath.Join(e.cwd, ".localcode", "config.json")
}

// keyReport writes one provider's report. Every line goes through line,
// which takes the key out of anything an endpoint said back, so a server
// that echoes the Authorization header does not put the key on screen.
type keyReport struct {
	out io.Writer
	key string
}

func (r *keyReport) line(label, format string, a ...any) {
	fmt.Fprintf(r.out, "  %-9s %s\n", label, redactKey(fmt.Sprintf(format, a...), r.key))
}

// minSecretLen is the shortest key the report hides. Shorter, and hiding
// it rewrites ordinary words while protecting nothing a guess would not
// find.
const minSecretLen = 8

// minEchoLen is the shortest run of the key's characters taken out of a
// line: long enough that ordinary text does not match it by chance, short
// enough that what is left of a key cut off mid-way is no use to anyone.
const minEchoLen = 12

// redactKey replaces every run of minEchoLen or more characters that also
// appears, in that order, in the key: the whole key, one of a JWT's three
// parts, or the part of one that survived an endpoint's reply being cut
// short before it got here. Matching the key whole missed all but the
// first of those.
func redactKey(text, key string) string {
	key = strings.TrimSpace(key)
	if len(key) < minSecretLen {
		return text
	}
	least := min(minEchoLen, len(key))
	var b strings.Builder
	for i := 0; i < len(text); {
		j := i
		for j < len(text) && strings.Contains(key, text[i:j+1]) {
			j++
		}
		if j-i >= least {
			b.WriteString("[the key]")
			i = j
			continue
		}
		b.WriteByte(text[i])
		i++
	}
	return b.String()
}

func (r *keyReport) check(name string, pc config.ProviderConfig, cfg *config.Config, files []string, project, model string, noChat bool, timeout time.Duration) bool {
	key := strings.TrimSpace(pc.APIKey)
	fmt.Fprintln(r.out, redactKey(fmt.Sprintf("provider %s (openai-compat, %s)", name, pc.BaseURL), key))

	expired := r.describeKey(key)
	r.describeFile(key, files, project)
	if sendsKeyInClear(pc.BaseURL) {
		r.line("warning", "base_url is http and not this machine: the key goes over the network unencrypted")
	}

	p := provider.NewOpenAICompat(pc.BaseURL, key)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	ids, status, detail, err := p.ModelList(ctx)
	cancel()
	listed := false
	switch {
	case err != nil:
		r.line("models", "no answer: %v", err)
	case status == 200 && detail == "":
		listed = true
		r.line("models", "accepted: %d model(s)%s", len(ids), listOf(ids))
	case status == 401 || status == 403:
		r.line("models", "refused (%d): %s", status, detail)
	default:
		r.line("models", "no model list (%d): %s", status, detail)
	}

	if noChat {
		switch {
		case listed:
			fmt.Fprintln(r.out, "result: the endpoint accepted the key for its model list (no chat request sent)")
			return true
		case status == 401 || status == 403:
			r.result(false, expired, status)
			return false
		default:
			fmt.Fprintln(r.out, "result: not known: the model list did not answer, and --no-chat sent nothing else")
			return false
		}
	}

	m := chatModel(name, cfg, model, ids)
	if m == "" {
		r.line("chat", "not sent: no model to ask. Pass --model, or add a profile on provider %q", name)
		// A refusal is an answer, whether or not a chat request could be
		// made after it.
		if status == 401 || status == 403 {
			r.result(false, expired, status)
		} else {
			fmt.Fprintln(r.out, "result: not known")
		}
		return false
	}
	body, _ := json.Marshal(map[string]any{
		"model":      m,
		"messages":   []map[string]string{{"role": "user", "content": "Reply with the word OK."}},
		"max_tokens": 16,
		"stream":     false,
	})
	ctx, cancel = context.WithTimeout(context.Background(), timeout)
	reply, err := p.RawChat(ctx, body)
	cancel()
	if err != nil {
		r.line("chat", "%s: %v", m, err)
		// A refusal from either request is the answer: a chat request that
		// failed some other way after the model list refused the key does
		// not make the key less refused.
		verdict := chatStatus(err)
		if verdict != 401 && verdict != 403 && (status == 401 || status == 403) {
			verdict = status
		}
		r.result(false, expired, verdict)
		return false
	}
	r.line("chat", "accepted: %s answered %q (%d + %d tokens)", m, shortReply(reply.Content), reply.PromptTokens, reply.OutputTokens)
	r.result(true, expired, 200)
	return true
}

// describeKey says what the token claims, and reports whether it has
// expired. The server decides; an expired token is still sent, because a
// clock here can be wrong and the endpoint is the one that knows.
func (r *keyReport) describeKey(key string) bool {
	c, _ := provider.ParseJWT(key)
	parts := []string{"JWT", c.Algorithm}
	if c.KeyID != "" {
		parts = append(parts, "key id "+c.KeyID)
	}
	if c.Issuer != "" {
		parts = append(parts, "issuer "+c.Issuer)
	}
	if len(c.Audience) > 0 {
		parts = append(parts, "audience "+strings.Join(c.Audience, " "))
	}
	r.line("key", "%s", strings.Join(parts, ", "))
	now := time.Now()
	if !c.NotBefore.IsZero() && now.Before(c.NotBefore) {
		r.line("warning", "not valid until %s", c.NotBefore.Format("2006-01-02 15:04 MST"))
	}
	switch {
	case c.Expires.IsZero():
		r.line("expires", "never: the token carries no exp")
	case now.After(c.Expires):
		r.line("expires", "EXPIRED on %s (%s ago)", c.Expires.Format("2006-01-02 15:04 MST"), timeBetween(c.Expires, now))
		return true
	default:
		r.line("expires", "%s (%s left)", c.Expires.Format("2006-01-02 15:04 MST"), timeBetween(now, c.Expires))
	}
	return false
}

// describeFile says which config file holds the key and who can read it. A
// key written into the project's file travels with the project, and a file
// others can read gives them the key.
func (r *keyReport) describeFile(key string, files []string, project string) {
	if len(key) < minSecretLen {
		r.line("written", "not looked for: a key this short would match text that is not the key")
		return
	}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(data), key) {
			continue
		}
		shown := path
		if home, err := os.UserHomeDir(); err == nil {
			if rel, err := filepath.Rel(home, path); err == nil && !strings.HasPrefix(rel, "..") {
				shown = filepath.Join("~", rel)
			}
		}
		r.line("written", "in %s", shown)
		if project != "" && userdirs.SameDirectory(path, project) {
			r.line("warning", "that is the project's config file: it is committed with the project unless ignored. Keep the token in ~/.localcode/config.json, or write {env:NAME} and keep it in the environment")
		}
		if info, err := os.Stat(path); err == nil && runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			r.line("warning", "%s is readable by other users (mode %04o). Run: chmod 600 %s", shown, info.Mode().Perm(), path)
		}
		return
	}
	r.line("written", "not as written text in %s: an {env:} value or an included file", strings.Join(files, " or "))
}

// result is the last line: whether the key works, and when it does not,
// what to do about it.
func (r *keyReport) result(ok, expired bool, status int) {
	switch {
	case ok:
		fmt.Fprintln(r.out, "result: the key works")
	case (status == 401 || status == 403) && expired:
		fmt.Fprintf(r.out, "result: the key does not work: the endpoint refused it (%d), and the token has expired. Get a new one and replace api_key\n", status)
	case status == 401 || status == 403:
		fmt.Fprintf(r.out, "result: the key does not work: the endpoint refused it (%d). Check that api_key holds the whole token, and that it was issued for this endpoint\n", status)
	default:
		fmt.Fprintln(r.out, "result: the key was not confirmed: the request failed for another reason, shown above")
	}
}

// chatModel is the model the chat check asks: the one passed, else the
// model of a profile on this provider (default_profile first), else the
// first one the endpoint listed.
func chatModel(name string, cfg *config.Config, flagModel string, listed []string) string {
	if flagModel != "" {
		return flagModel
	}
	if p, ok := cfg.Profiles[cfg.DefaultProfile]; ok && p.Provider == name && p.Model != "" {
		return p.Model
	}
	var profiles []string
	for pn, p := range cfg.Profiles {
		if p.Provider == name && p.Model != "" {
			profiles = append(profiles, pn)
		}
	}
	if len(profiles) > 0 {
		sort.Strings(profiles)
		return cfg.Profiles[profiles[0]].Model
	}
	if len(listed) > 0 {
		return listed[0]
	}
	return ""
}

// chatStatus is the HTTP status RawChat's error names, or 0.
func chatStatus(err error) int {
	var status int
	if _, scanErr := fmt.Sscanf(err.Error(), "the server answered %d", &status); scanErr != nil {
		return 0
	}
	return status
}

func listOf(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	if len(ids) > 12 {
		return fmt.Sprintf(": %s, and %d more", strings.Join(ids[:12], ", "), len(ids)-12)
	}
	return ": " + strings.Join(ids, ", ")
}

func shortReply(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 60 {
		return string(r[:60]) + "…"
	}
	return s
}

// timeBetween is how long from a to b, in days when it is one or more. In
// seconds rather than a time.Duration, which tops out at 292 years and so
// read a far expiry as 106751 days.
func timeBetween(a, b time.Time) string {
	secs := b.Unix() - a.Unix()
	switch n := secs / 86400; {
	case n == 1:
		return "1 day"
	case n > 1:
		return fmt.Sprintf("%d days", n)
	}
	return (time.Duration(secs) * time.Second).Round(time.Minute).String()
}

// sendsKeyInClear reports whether a key sent to baseURL crosses a network
// unencrypted: plain http to anything but this machine.
func sendsKeyInClear(baseURL string) bool {
	u, err := url.Parse(baseURL)
	return err == nil && u.Scheme == "http" && !isLoopbackHost(u.Hostname())
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
