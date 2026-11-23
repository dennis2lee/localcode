// Package sshterm keeps SSH connections and their interactive shells
// ("terminals") for conversations: the model works in them through the
// ssh tool and the person through the SSH panel, on the same screen.
//
// Pure Go: golang.org/x/crypto/ssh for the protocol and
// github.com/charmbracelet/x/vt for the screen. No local process and no
// local terminal are involved.
package sshterm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
)

// Options configure a Manager.
type Options struct {
	// Home is the home directory whose .ssh is read (config, keys,
	// known_hosts). Never written.
	Home string
	// DataDir is localcode's own SSH directory, for host keys the person
	// accepted.
	DataDir string
	// SystemDir is the system's SSH directory (/etc/ssh), for
	// ssh_config and ssh_known_hosts. Empty reads none.
	SystemDir string
	// LocalUser is the user name to log in as when nothing says another.
	LocalUser string
	// Env is sent to every shell before it starts (KEY=VALUE), as
	// OpenSSH's default SendEnv sends LANG and LC_*: a server that
	// accepts them (AcceptEnv) runs the shell in the person's locale, which
	// is what lets it read Korean typed into it. A server that refuses one
	// is not an error.
	Env []string
	// Agent is the person's SSH agent (a socket or, on Windows, a pipe),
	// used unless the configuration's IdentityAgent names another. Empty
	// uses no agent.
	Agent string
	// Permits, when set, says whether this machine may connect to a host
	// (network.egress). It is asked of the first hop only: the hops after
	// it are reached from the jump host.
	Permits func(host string) bool
	// Dial makes a TCP connection. Nil uses net.Dialer.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// MaxPerConversation and MaxTotal bound the live terminals
	// (connecting or connected). Zero means 8 and 32. A conversation
	// keeps at most MaxPerConversation ended ones too; the oldest goes
	// to make room.
	MaxPerConversation int
	MaxTotal           int
	// CheckTarget, when set, is asked just before a connection is made,
	// with where it resolved to then, and what was approved for it (see
	// OpenApproved; "" when nothing was). An error refuses the connection.
	CheckTarget func(conv string, by Opener, r Route, approved string) error
	// OnAsking, when set, is told a conversation whose terminals started
	// or stopped asking the person something, so a session list can mark
	// it. Called with no lock held.
	OnAsking func(conv string)
}

// Manager holds every conversation's terminals.
type Manager struct {
	opts Options

	mu     sync.Mutex
	convs  map[string]*convTerms
	closed bool
}

type convTerms struct {
	terms    []*Terminal
	next     int
	lastUsed *Terminal
	changed  chan struct{}
	// wants is set when a terminal asks something of the person, and
	// cleared when a panel has shown it.
	wants bool
	// wantsSeq counts the times a terminal wanted the person, so every
	// panel can tell a new time from one it has seen.
	wantsSeq int
}

// NewManager returns a manager with no terminals.
func NewManager(o Options) *Manager {
	if o.MaxPerConversation == 0 {
		o.MaxPerConversation = 8
	}
	if o.MaxTotal == 0 {
		o.MaxTotal = 32
	}
	return &Manager{opts: o, convs: map[string]*convTerms{}}
}

func (m *Manager) conv(id string) *convTerms {
	c := m.convs[id]
	if c == nil {
		c = &convTerms{changed: make(chan struct{})}
		m.convs[id] = c
	}
	return c
}

func (m *Manager) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	if m.opts.Dial != nil {
		return m.opts.Dial(ctx, network, addr)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

// Resolve is where spec goes after ~/.ssh/config: what a permission rule
// for connecting is matched against.
func (m *Manager) Resolve(spec string) (Target, error) {
	sp, err := ParseSpec(spec)
	if err != nil {
		return Target{}, err
	}
	return m.ResolveSpec(sp)
}

// ResolveSpec is Resolve for a parsed spec, with the jump hosts.
func (m *Manager) ResolveSpec(sp Spec) (Target, error) {
	cfg, err := loadConfig(m.opts.Home, m.opts.SystemDir)
	if err != nil {
		return Target{}, fmt.Errorf("reading ~/.ssh/config: %w", err)
	}
	return cfg.resolve(sp, m.opts.LocalUser, m.opts.Home), nil
}

// RouteTo is where spec goes, jump hosts included.
func (m *Manager) RouteTo(spec string) (Route, error) {
	sp, err := ParseSpec(spec)
	if err != nil {
		return Route{}, err
	}
	cfg, err := loadConfig(m.opts.Home, m.opts.SystemDir)
	if err != nil {
		return Route{}, fmt.Errorf("reading ~/.ssh/config: %w", err)
	}
	r := Route{Asked: sp, Target: cfg.resolve(sp, m.opts.LocalUser, m.opts.Home)}
	for _, hop := range r.Target.ProxyJump {
		hs, err := ParseSpec(hop)
		if err != nil {
			return Route{}, fmt.Errorf("ProxyJump %q: %w", hop, err)
		}
		r.Hops = append(r.Hops, cfg.resolve(hs, m.opts.LocalUser, m.opts.Home))
	}
	return r, nil
}

// ErrLimit is a conversation or daemon with as many terminals as it may
// have.
var ErrLimit = errors.New("too many SSH terminals")

// Open starts a connection to spec for conversation conv and returns its
// terminal at once, connecting. WaitConnected waits for the outcome.
func (m *Manager) Open(conv, spec string, by Opener) (*Terminal, error) {
	return m.OpenApproved(conv, spec, by, "")
}

// OpenApproved is Open for a connection a permission question approved:
// approved is the subject the question named, which CheckTarget compares
// with where the connection resolves when it is made, so a change to
// ~/.ssh/config in between cannot send it somewhere else.
func (m *Manager) OpenApproved(conv, spec string, by Opener, approved string) (*Terminal, error) {
	if _, err := ParseSpec(spec); err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("SSH is off")
	}
	c := m.conv(conv)
	live, ended := m.countLocked(c)
	if live >= m.opts.MaxPerConversation {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: this conversation has %d connections open; close one first", ErrLimit, live)
	}
	if all := m.liveLocked(); all >= m.opts.MaxTotal {
		names := m.holdersLocked()
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: %d connections are open, in conversations %s; close some (the settings window lists them)", ErrLimit, all, strings.Join(names, ", "))
	}
	if ended >= m.opts.MaxPerConversation {
		// The oldest ended terminal makes room.
		for i, old := range c.terms {
			if old.ended() {
				c.terms = append(c.terms[:i:i], c.terms[i+1:]...)
				break
			}
		}
	}
	c.next++
	t := newTerminal(m, conv, fmt.Sprintf("t%d", c.next), spec, by)
	t.approved = approved
	c.terms = append(c.terms, t)
	c.lastUsed = t
	m.touchLocked(conv)
	m.mu.Unlock()
	go t.connect(t.ctx)
	return t, nil
}

// ended reports a terminal that has ended. It takes no lock: the
// manager asks it while holding its own, and a terminal holding its lock
// calls into the manager, so taking the terminal's here could deadlock.
func (t *Terminal) ended() bool {
	return t.endedFlag.Load()
}

// countLocked counts c's live and ended terminals. Called with m.mu.
func (m *Manager) countLocked(c *convTerms) (live, ended int) {
	for _, t := range c.terms {
		if t.ended() {
			ended++
		} else {
			live++
		}
	}
	return live, ended
}

func (m *Manager) liveLocked() int {
	n := 0
	for _, c := range m.convs {
		l, _ := m.countLocked(c)
		n += l
	}
	return n
}

func (m *Manager) holdersLocked() []string {
	var names []string
	for id, c := range m.convs {
		if l, _ := m.countLocked(c); l > 0 {
			names = append(names, id)
		}
	}
	sort.Strings(names)
	return names
}

// Open lists every live terminal across conversations, for the settings
// window and for an update to name what it would close.
func (m *Manager) LiveTerminals() []*Terminal {
	m.mu.Lock()
	var all []*Terminal
	for _, c := range m.convs {
		all = append(all, c.terms...)
	}
	m.mu.Unlock()
	var live []*Terminal
	for _, t := range all {
		if !t.ended() {
			live = append(live, t)
		}
	}
	sort.Slice(live, func(i, j int) bool {
		if live[i].Conv != live[j].Conv {
			return live[i].Conv < live[j].Conv
		}
		return live[i].ID < live[j].ID
	})
	return live
}

// Reconnect replaces an ended terminal with a new connection to the same
// place, under the same id, so the panel's tab stays where it was.
func (m *Manager) Reconnect(conv, id string, by Opener) (*Terminal, error) {
	m.mu.Lock()
	c := m.convs[conv]
	if c == nil {
		m.mu.Unlock()
		return nil, errors.New("no such terminal")
	}
	for i, old := range c.terms {
		if old.ID != id {
			continue
		}
		if !old.ended() {
			m.mu.Unlock()
			return nil, errors.New("that terminal is still connected")
		}
		t := newTerminal(m, conv, id, old.Spec, by)
		c.terms[i] = t
		c.lastUsed = t
		m.touchLocked(conv)
		m.mu.Unlock()
		go t.connect(t.ctx)
		return t, nil
	}
	m.mu.Unlock()
	return nil, errors.New("no such terminal")
}

// WaitConnected waits until the terminal is connected or has ended.
func (t *Terminal) WaitConnected(ctx context.Context) error {
	for {
		t.mu.Lock()
		state, reason, ch := t.state, t.reason, t.changed
		t.mu.Unlock()
		switch state {
		case StateConnected:
			return nil
		case StateEnded:
			return errors.New(reason)
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Get is conversation conv's terminal id, or nil.
func (m *Manager) Get(conv, id string) *Terminal {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c := m.convs[conv]; c != nil {
		for _, t := range c.terms {
			if t.ID == id {
				return t
			}
		}
	}
	return nil
}

// Default is the terminal the model used or opened last, while it is
// still listed.
func (m *Manager) Default(conv string) *Terminal {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.convs[conv]
	if c == nil {
		return nil
	}
	if c.lastUsed != nil {
		for _, t := range c.terms {
			if t == c.lastUsed {
				return t
			}
		}
	}
	if len(c.terms) > 0 {
		return c.terms[len(c.terms)-1]
	}
	return nil
}

// List is conversation conv's terminals, in the order they were opened.
func (m *Manager) List(conv string) []*Terminal {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c := m.convs[conv]; c != nil {
		return append([]*Terminal(nil), c.terms...)
	}
	return nil
}

// Remove closes a terminal and takes it off the list.
func (m *Manager) Remove(conv, id string, reason string) bool {
	m.mu.Lock()
	c := m.convs[conv]
	var gone *Terminal
	if c != nil {
		for i, t := range c.terms {
			if t.ID == id {
				gone = t
				c.terms = append(c.terms[:i:i], c.terms[i+1:]...)
				if c.lastUsed == t {
					c.lastUsed = nil
				}
				break
			}
		}
		m.touchLocked(conv)
	}
	m.mu.Unlock()
	if gone != nil {
		gone.Close(reason)
	}
	return gone != nil
}

// CloseConversation closes and forgets every terminal of conv.
func (m *Manager) CloseConversation(conv, reason string) {
	m.mu.Lock()
	c := m.convs[conv]
	delete(m.convs, conv)
	var terms []*Terminal
	if c != nil {
		terms = c.terms
		close(c.changed)
	}
	m.mu.Unlock()
	for _, t := range terms {
		t.Close(reason)
	}
}

// CloseAll closes every terminal. With stop, the manager opens no more
// (the daemon is stopping); without, it can open again (the setting was
// turned off and may be turned on).
func (m *Manager) CloseAll(reason string, stop bool) {
	m.mu.Lock()
	convs := m.convs
	m.convs = map[string]*convTerms{}
	if stop {
		m.closed = true
	}
	m.mu.Unlock()
	for _, c := range convs {
		close(c.changed)
		for _, t := range c.terms {
			t.Close(reason)
		}
	}
}

// Changed is closed the next time conv's terminals change: one opens,
// closes, connects, ends, asks something, or its screen changes.
func (m *Manager) Changed(conv string) <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.conv(conv).changed
}

func (m *Manager) touchLocked(conv string) {
	c := m.convs[conv]
	if c == nil {
		return
	}
	close(c.changed)
	c.changed = make(chan struct{})
}

// touched is told by a terminal that it changed. Called with the
// terminal's lock held, so it takes only the manager's.
func (m *Manager) touched(conv string) {
	m.mu.Lock()
	m.touchLocked(conv)
	m.mu.Unlock()
}

func (m *Manager) used(t *Terminal) {
	m.mu.Lock()
	if c := m.convs[t.Conv]; c != nil {
		c.lastUsed = t
	}
	m.mu.Unlock()
}

// asking records that a terminal of t's conversation wants the person.
func (m *Manager) asking(t *Terminal) {
	m.mu.Lock()
	if c := m.convs[t.Conv]; c != nil {
		c.wants = true
		c.wantsSeq++
		m.touchLocked(t.Conv)
	}
	m.mu.Unlock()
	if m.opts.OnAsking != nil {
		m.opts.OnAsking(t.Conv)
	}
}

// asked records that a terminal's question went away.
func (m *Manager) asked(t *Terminal) {
	m.touched(t.Conv)
	if m.opts.OnAsking != nil {
		m.opts.OnAsking(t.Conv)
	}
}

// WantsSeq counts the times conv's terminals wanted the person.
func (m *Manager) WantsSeq(conv string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c := m.convs[conv]; c != nil {
		return c.wantsSeq
	}
	return 0
}

// Asking reports the conversations with a terminal waiting for the
// person to answer a question (a host key, a password).
func (m *Manager) Asking() map[string]bool {
	m.mu.Lock()
	var all []*Terminal
	for _, c := range m.convs {
		all = append(all, c.terms...)
	}
	m.mu.Unlock()
	out := map[string]bool{}
	for _, t := range all {
		// Without the terminal's lock: the session list asks this, and a
		// terminal busy drawing must not hold it up.
		if t.Summarize().Asking {
			out[t.Conv] = true
		}
	}
	return out
}

// TakeWants reports whether a terminal of conv asked for the person
// since the last call, and clears it.
func (m *Manager) TakeWants(conv string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.convs[conv]
	if c == nil || !c.wants {
		return false
	}
	c.wants = false
	return true
}

func (m *Manager) ended(t *Terminal) {
	m.touched(t.Conv)
}
