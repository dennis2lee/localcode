package browser

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// DefaultDataDir is the profile directory localcode's browser runs on.
func DefaultDataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".localcode", "browser"), nil
}

// profileNameFile records which profile name the import brought over, so
// later starts open the same one, and on its second line whether it was
// imported ("imported") or started empty: see launchArgs.
const profileNameFile = "localcode-profile"

// Options configure a Manager.
type Options struct {
	ChromePath string // a browser to run instead of the one Find picks
	DataDir    string // the profile directory; DefaultDataDir when empty
	// Block reports whether a page (a top-level document or a frame's)
	// may not load: a deny rule covers it. Asked of every document
	// request the browser makes, redirects and frames included, so a
	// denied site is never loaded rather than refused after the fact.
	Block func(url string) bool
}

// Manager owns the one browser and every conversation's tab in it.
type Manager struct {
	opts Options

	mu         sync.Mutex
	proc       *process
	convs      map[string]*convTabs
	lastImport *ImportReport
}

// LastImport is what the most recent copy of the person's profile did,
// nil before the first.
func (m *Manager) LastImport() *ImportReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastImport == nil {
		return nil
	}
	r := *m.lastImport
	return &r
}

// alive reports whether the browser is up. Called with m.mu.
func (m *Manager) alive() bool {
	if m.proc == nil {
		return false
	}
	select {
	case <-m.proc.conn.Done():
		return false
	default:
		return true
	}
}

// dropAll forgets every conversation's tabs, telling anyone watching one.
// Called with m.mu.
func (m *Manager) dropAll() {
	for _, c := range m.convs {
		close(c.changed)
	}
	m.convs = map[string]*convTabs{}
}

// convTabs is a conversation's pages: the one it works in is the last. A
// link that opens a new tab pushes it; closing that tab pops it.
type convTabs struct {
	stack   []*Tab
	changed chan struct{} // closed when the current tab changes
}

func (c *convTabs) current() *Tab {
	if len(c.stack) == 0 {
		return nil
	}
	return c.stack[len(c.stack)-1]
}

// NewManager returns a manager that starts nothing until a tab is asked
// for.
func NewManager(opts Options) *Manager {
	return &Manager{opts: opts, convs: map[string]*convTabs{}}
}

// Available is whether there is a browser to start: a Chrome or Edge
// install, or the one browser_path names.
func (m *Manager) Available() bool {
	_, err := Find(m.opts.ChromePath)
	return err == nil
}

func (m *Manager) dataDir() (string, error) {
	if m.opts.DataDir != "" {
		return m.opts.DataDir, nil
	}
	return DefaultDataDir()
}

// Tab returns conv's tab, starting the browser and opening the tab if
// either is not there yet.
func (m *Manager) Tab(ctx context.Context, conv string) (*Tab, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureRunning(ctx); err != nil {
		return nil, err
	}
	if c := m.convs[conv]; c != nil && c.current() != nil {
		return c.current(), nil
	}
	t, err := openTab(ctx, m.proc.conn)
	if err != nil {
		return nil, err
	}
	m.convs[conv] = &convTabs{stack: []*Tab{t}, changed: make(chan struct{})}
	return t, nil
}

// Existing returns conv's tab if it has one, without starting anything.
// Nil once the browser has gone away, whatever tab it had.
func (m *Manager) Existing(conv string) *Tab {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.alive() {
		return nil
	}
	if c := m.convs[conv]; c != nil {
		return c.current()
	}
	return nil
}

// ensureRunning starts the browser if it is not running. Called with m.mu.
func (m *Manager) ensureRunning(ctx context.Context) error {
	if m.proc != nil {
		if m.alive() {
			return nil
		}
		// It went away (crashed, or someone quit it); every tab went with
		// it, and whoever is watching one is told, so they move on to
		// the next browser rather than waiting on a dead page.
		m.proc.kill()
		m.proc = nil
		m.dropAll()
	}
	install, err := Find(m.opts.ChromePath)
	if err != nil {
		return err
	}
	dir, err := m.dataDir()
	if err != nil {
		return err
	}
	profile, imported, err := m.ensureProfile(install, dir)
	if err != nil {
		return err
	}
	p, err := start(ctx, install.Path, dir, profile, imported)
	if err != nil {
		return err
	}
	m.proc = p
	m.watchTargets(p)
	m.guard(p)
	return nil
}

// ensureProfile seeds dir from the person's everyday profile the first
// time, and returns the profile name to open and whether it was imported.
// Called with m.mu.
func (m *Manager) ensureProfile(install Install, dir string) (string, bool, error) {
	if data, err := os.ReadFile(filepath.Join(dir, profileNameFile)); err == nil {
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if name := strings.TrimSpace(lines[0]); name != "" {
			return name, len(lines) > 1 && strings.TrimSpace(lines[1]) == "imported", nil
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", false, err
	}
	name, imported := "Default", false
	src := UserDataDir(install.Kind, runtime.GOOS, os.Getenv)
	if _, err := os.Stat(src); err == nil {
		name = LastUsedProfile(src)
		if rep, err := ImportProfile(src, name, dir); err == nil {
			imported = true
			m.lastImport = &rep
		} else {
			name = "Default"
		}
	}
	if err := writeProfileName(dir, name, imported); err != nil {
		return "", false, err
	}
	return name, imported, nil
}

func writeProfileName(dir, name string, imported bool) error {
	how := "fresh"
	if imported {
		how = "imported"
	}
	return os.WriteFile(filepath.Join(dir, profileNameFile), []byte(name+"\n"+how+"\n"), 0o600)
}

// Import copies the person's everyday profile in again, for logins made
// there since the last import. The browser is closed first, since a
// profile cannot be replaced under a running browser; every
// conversation's tab closes with it, and the next use starts afresh.
func (m *Manager) Import() (ImportReport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
	install, err := Find(m.opts.ChromePath)
	if err != nil {
		return ImportReport{}, err
	}
	dir, err := m.dataDir()
	if err != nil {
		return ImportReport{}, err
	}
	src := UserDataDir(install.Kind, runtime.GOOS, os.Getenv)
	name := LastUsedProfile(src)
	rep, err := ImportProfile(src, name, dir)
	if err != nil {
		return rep, err
	}
	m.lastImport = &rep
	return rep, writeProfileName(dir, name, true)
}

// watchTargets follows pages a conversation's tab opens (a link with
// target=_blank, window.open) and closes, so the conversation keeps
// working in the page in front, as a person would.
func (m *Manager) watchTargets(p *process) {
	conn := p.conn
	conn.On("", "Target.targetCreated", func(params json.RawMessage) {
		var ev struct {
			TargetInfo struct {
				TargetID string `json:"targetId"`
				Type     string `json:"type"`
				OpenerID string `json:"openerId"`
			} `json:"targetInfo"`
		}
		if json.Unmarshal(params, &ev) != nil || ev.TargetInfo.Type != "page" || ev.TargetInfo.OpenerID == "" {
			return
		}
		go m.adopt(p, ev.TargetInfo.OpenerID, ev.TargetInfo.TargetID)
	})
	conn.On("", "Target.targetDestroyed", func(params json.RawMessage) {
		var ev struct {
			TargetID string `json:"targetId"`
		}
		if json.Unmarshal(params, &ev) == nil {
			go m.forget(ev.TargetID)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = conn.Call(ctx, "", "Target.setDiscoverTargets", map[string]any{"discover": true}, nil)
}

// adopt makes a page opened by a conversation's current tab its new
// current tab.
func (m *Manager) adopt(p *process, openerID, targetID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.proc != p {
		return
	}
	for _, c := range m.convs {
		if cur := c.current(); cur != nil && cur.targetID == openerID {
			t, err := attachTab(ctx, p.conn, targetID)
			if err != nil {
				return
			}
			c.stack = append(c.stack, t)
			close(c.changed)
			c.changed = make(chan struct{})
			return
		}
	}
}

// forget drops a page that closed from whichever conversation had it.
func (m *Manager) forget(targetID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for conv, c := range m.convs {
		for i, t := range c.stack {
			if t.targetID != targetID {
				continue
			}
			wasCurrent := i == len(c.stack)-1
			c.stack = append(c.stack[:i], c.stack[i+1:]...)
			if m.proc != nil {
				m.proc.conn.Forget(t.session)
			}
			if len(c.stack) == 0 {
				delete(m.convs, conv)
			}
			if wasCurrent {
				close(c.changed)
				c.changed = make(chan struct{})
			}
			return
		}
	}
}

// CloseTab closes every page conv has.
func (m *Manager) CloseTab(conv string) {
	m.mu.Lock()
	c := m.convs[conv]
	delete(m.convs, conv)
	m.mu.Unlock()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, t := range c.stack {
		t.close(ctx)
	}
	close(c.changed)
}

// Close quits the browser.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
}

func (m *Manager) stopLocked() {
	if m.proc == nil {
		return
	}
	m.proc.close()
	m.proc = nil
	m.dropAll()
}

// Watch streams the frames of conv's current tab to the panel, following
// it when a link opens a new tab or the tab closes. It returns when ctx
// ends. frame is called from one goroutine at a time.
func (m *Manager) Watch(ctx context.Context, conv string, frame func([]byte)) error {
	for {
		m.mu.Lock()
		c := m.convs[conv]
		var t *Tab
		var changed chan struct{}
		if c != nil {
			t, changed = c.current(), c.changed
		}
		m.mu.Unlock()
		if t == nil {
			return errors.New("this conversation has no browser tab")
		}
		frames, stop, err := t.Watch(ctx)
		if err != nil {
			return err
		}
		switched := false
		for !switched {
			select {
			case <-ctx.Done():
				stop()
				return nil
			case <-changed:
				switched = true
			case f := <-frames:
				frame(f)
			}
		}
		stop()
	}
}
