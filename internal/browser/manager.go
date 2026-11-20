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
//
// Two locks, for two jobs. mu guards the state: the running browser and
// every conversation's tabs. It is held to read or change that state and
// never across anything that waits on the browser, a process or the disk:
// a slow CDP call for one conversation used to hold every other
// conversation's browser work behind it (Existing, Tabs, a switch, a
// close, a stream), for as long as a start or an attach took. startMu
// orders what changes which browser there is (a start, a profile copy, a
// close), one at a time, so two conversations asking at once start one
// browser. It is taken before mu, never while holding it.
type Manager struct {
	opts Options

	startMu sync.Mutex

	mu         sync.Mutex
	proc       *process
	convs      map[string]*convTabs
	lastImport *ImportReport

	// What a call made with mu let go has to find when it takes mu again
	// to publish its tab, since the state can change in between:
	//
	// opening counts each conversation's tab opens in flight, and a
	// CloseTab of the conversation cancels them: a tab still opening when
	// its conversation was deleted would otherwise bring the conversation
	// back.
	opening map[string]*opening
	// adopting is each page a link opened that is being attached, and the
	// conversation it is for: a page that one opens in turn finds its
	// conversation there before the first is in its list.
	adopting map[string]string
	// gone is the latest pages that closed while in no conversation's
	// list: one that closes while it attaches gets its close event before
	// it is published, and is not published.
	gone     [goneKept]string
	goneNext int
}

// goneKept is how many closed pages gone remembers, far more than close
// while a tab attaches.
const goneKept = 256

// opening is one conversation's tab opens in flight.
type opening struct {
	n         int
	cancelled bool
}

// beginOpen counts a tab open for conv, and returns what endOpen and the
// publish check read. Called with m.mu.
func (m *Manager) beginOpen(conv string) *opening {
	o := m.opening[conv]
	if o == nil {
		o = &opening{}
		m.opening[conv] = o
	}
	o.n++
	return o
}

// endOpen is the end of a tab open beginOpen counted. Called with m.mu.
func (m *Manager) endOpen(conv string, o *opening) {
	o.n--
	if o.n == 0 && m.opening[conv] == o {
		delete(m.opening, conv)
	}
}

// wasGone is whether the page targetID closed while in no list. Called
// with m.mu.
func (m *Manager) wasGone(targetID string) bool {
	for _, id := range m.gone {
		if id != "" && id == targetID {
			return true
		}
	}
	return false
}

// errClosedMeanwhile is a tab opened for a conversation deleted while it
// opened.
var errClosedMeanwhile = errors.New("the conversation was closed while its tab opened")

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

// convTabs is a conversation's pages, in the order they were opened, and
// the one it works in: the one the model's browser tool acts on and the
// panel shows. A tab a link opens, or one the person opens in the panel,
// is added and becomes the active one. Closing the active tab makes its
// opener active again, as a browser does, when it was opened by a link
// and the opener is still open; otherwise the one before it in the list.
type convTabs struct {
	tabs    []*Tab
	active  int
	changed chan struct{} // closed when the active tab changes
}

func (c *convTabs) current() *Tab {
	if len(c.tabs) == 0 {
		return nil
	}
	return c.tabs[c.active]
}

// notify tells whoever is watching the active tab that it changed.
func (c *convTabs) notify() {
	close(c.changed)
	c.changed = make(chan struct{})
}

// add appends t and makes it the active tab.
func (c *convTabs) add(t *Tab) {
	c.tabs = append(c.tabs, t)
	c.active = len(c.tabs) - 1
	c.notify()
}

// remove takes out the tab at i. Removing the active one makes its
// opener active, if it has one still open, or else the one before it (the
// first one, when it was the first), and reports the tab that is active
// now; nil when the active tab did not change or none is left. Whoever
// watches is not told here: see showActive.
func (c *convTabs) remove(i int) *Tab {
	opener := c.tabs[i].opener
	c.tabs = append(c.tabs[:i], c.tabs[i+1:]...)
	switch {
	case i < c.active:
		c.active--
	case i == c.active:
		if j := c.index(opener); opener != "" && j >= 0 {
			c.active = j
		} else if c.active > 0 {
			c.active--
		}
		return c.current()
	}
	return nil
}

// bringForward puts t in front of its window and waits for it to paint
// there. A browser with no window paints only the tab in front of each
// window, and the screencast the panel shows is built from those paints.
// A tab a link opened sits in its opener's window, in front of it, so a
// switch back to the opener showed no frame of it at all while the model
// worked on it. And a screencast started the moment the tab comes forward
// sends one frame and then nothing (measured: a second later, it streams),
// so this waits for two animation frames, which a page that is not shown
// does not run. Not called with m.mu held: it waits on the browser.
func bringForward(t *Tab) {
	if t == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = t.conn.Call(ctx, "", "Target.activateTarget", map[string]any{"targetId": t.targetID}, nil)
	_ = t.conn.Call(ctx, t.session, "Page.bringToFront", nil, nil)
	_ = t.conn.Call(ctx, t.session, "Runtime.evaluate", map[string]any{
		"expression":    "new Promise(r => requestAnimationFrame(() => requestAnimationFrame(() => r(true))))",
		"awaitPromise":  true,
		"returnByValue": true,
	}, nil)
}

// showActive brings conv's active tab t forward, then tells whoever is
// watching the conversation that it changed: told first, a stream started
// on a tab not yet painting and stayed on one frame. Nothing is told if
// another change came in the meantime; that one tells. Not called with
// m.mu held.
func (m *Manager) showActive(conv string, t *Tab) {
	if t == nil {
		return
	}
	bringForward(t)
	m.mu.Lock()
	defer m.mu.Unlock()
	if c := m.convs[conv]; c != nil && c.current() == t {
		c.notify()
	}
}

// index is where the tab with targetID is in c, or -1.
func (c *convTabs) index(targetID string) int {
	for i, t := range c.tabs {
		if t.targetID == targetID {
			return i
		}
	}
	return -1
}

// NewManager returns a manager that starts nothing until a tab is asked
// for.
func NewManager(opts Options) *Manager {
	return &Manager{opts: opts, convs: map[string]*convTabs{}, opening: map[string]*opening{}, adopting: map[string]string{}}
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

// errReplaced is a tab opened on a browser that was replaced (closed,
// restarted, its profile copied again) while it opened.
var errReplaced = errors.New("the browser restarted while the tab opened; try again")

// Tab returns conv's tab, starting the browser and opening the tab if
// either is not there yet.
func (m *Manager) Tab(ctx context.Context, conv string) (*Tab, error) {
	m.mu.Lock()
	if c := m.convs[conv]; c != nil && c.current() != nil && m.alive() {
		t := c.current()
		m.mu.Unlock()
		return t, nil
	}
	o := m.beginOpen(conv)
	m.mu.Unlock()
	t, err := m.openFor(ctx, conv, o)
	if err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	// Another call for the same conversation got there first: its tab is
	// the conversation's, and this one goes.
	if c := m.convs[conv]; c != nil && c.current() != nil {
		existing := c.current()
		go t.close(context.Background())
		return existing, nil
	}
	m.convs[conv] = &convTabs{tabs: []*Tab{t}, changed: make(chan struct{})}
	return t, nil
}

// openFor opens a tab for conv on the running browser, starting it if it
// is not, with m.mu let go throughout, and checks what changed meanwhile:
// the browser replaced, the conversation closed, the page closed. o is
// the open beginOpen counted, ended here.
//
// On success it returns with m.mu held, for the caller to publish the tab
// and let it go: a check made under one hold and a publish under the next
// would let a close or a delete in between. On failure m.mu is not held
// and the tab, if one opened, is closed.
func (m *Manager) openFor(ctx context.Context, conv string, o *opening) (*Tab, error) {
	p, err := m.running(ctx)
	var t *Tab
	if err == nil {
		t, err = openTab(ctx, p.conn)
	}
	m.mu.Lock()
	m.endOpen(conv, o)
	switch {
	case err != nil:
	case m.proc != p:
		err = errReplaced
	case o.cancelled:
		err = errClosedMeanwhile
	case m.wasGone(t.targetID):
		err = errReplaced
	}
	if err == nil {
		return t, nil
	}
	m.mu.Unlock()
	if t != nil {
		t.close(ctx)
	}
	return nil, err
}

// TabInfo is one of a conversation's tabs, for the panel's tab strip.
type TabInfo struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	Title  string `json:"title"`
	Active bool   `json:"active"`
}

// ErrNoSuchTab is a tab id the conversation does not have (any more).
var ErrNoSuchTab = errors.New("this conversation has no such browser tab")

// Tabs lists conv's tabs in the order they were opened. Nil when it has
// none or the browser has gone away.
func (m *Manager) Tabs(ctx context.Context, conv string) []TabInfo {
	m.mu.Lock()
	var tabs []*Tab
	active := 0
	if c := m.convs[conv]; c != nil && m.alive() {
		tabs = append(tabs, c.tabs...)
		active = c.active
	}
	m.mu.Unlock()
	if len(tabs) == 0 {
		return nil
	}
	out := make([]TabInfo, len(tabs))
	for i, t := range tabs {
		loc, _ := t.Location(ctx)
		out[i] = TabInfo{ID: t.targetID, URL: loc.URL, Title: loc.Title, Active: i == active}
	}
	return out
}

// NewTab opens an empty tab for conv and makes it the active one, starting
// the browser if it is not running.
func (m *Manager) NewTab(ctx context.Context, conv string) (*Tab, error) {
	m.mu.Lock()
	o := m.beginOpen(conv)
	m.mu.Unlock()
	t, err := m.openFor(ctx, conv, o)
	if err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	if c := m.convs[conv]; c != nil {
		c.add(t)
	} else {
		m.convs[conv] = &convTabs{tabs: []*Tab{t}, changed: make(chan struct{})}
	}
	return t, nil
}

// ActivateTab makes conv's tab id the one it works in.
func (m *Manager) ActivateTab(conv, id string) error {
	m.mu.Lock()
	c := m.convs[conv]
	// A browser that has gone away took its tabs with it, whatever the
	// list still says until the next start clears it.
	if c == nil || !m.alive() {
		m.mu.Unlock()
		return ErrNoSuchTab
	}
	i := c.index(id)
	if i < 0 {
		m.mu.Unlock()
		return ErrNoSuchTab
	}
	changed := i != c.active
	c.active = i
	t := c.current()
	m.mu.Unlock()
	if changed {
		m.showActive(conv, t)
	} else {
		bringForward(t)
	}
	return nil
}

// CloseTabID closes conv's tab id. When it was the active one, the tab
// before it becomes active; when it was the last, conv has none.
func (m *Manager) CloseTabID(ctx context.Context, conv, id string) error {
	m.mu.Lock()
	c := m.convs[conv]
	if c == nil || !m.alive() {
		m.mu.Unlock()
		return ErrNoSuchTab
	}
	i := c.index(id)
	if i < 0 {
		m.mu.Unlock()
		return ErrNoSuchTab
	}
	t := c.tabs[i]
	next := c.remove(i)
	if len(c.tabs) == 0 {
		delete(m.convs, conv)
		close(c.changed)
	}
	if m.proc != nil {
		m.proc.conn.Forget(t.session)
	}
	m.mu.Unlock()
	t.close(ctx)
	m.showActive(conv, next)
	return nil
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

// running returns the browser, starting it if it is not running. One
// start at a time (startMu); m.mu only around reading and setting which
// browser there is, never across the start itself, which copies a
// profile, launches a process and makes the first CDP calls.
//
// The new browser is published only once it is guarded and its targets
// are followed: a tab opened on it before then would load pages the deny
// rules had not been asked about.
func (m *Manager) running(ctx context.Context) (*process, error) {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	m.mu.Lock()
	old := m.proc
	if old != nil && m.alive() {
		m.mu.Unlock()
		return old, nil
	}
	if old != nil {
		// It went away (crashed, or someone quit it); every tab went with
		// it, and whoever is watching one is told, so they move on to
		// the next browser rather than waiting on a dead page.
		m.proc = nil
		m.dropAll()
	}
	m.mu.Unlock()
	if old != nil {
		old.kill()
	}
	install, err := Find(m.opts.ChromePath)
	if err != nil {
		return nil, err
	}
	dir, err := m.dataDir()
	if err != nil {
		return nil, err
	}
	profile, imported, err := m.ensureProfile(install, dir)
	if err != nil {
		return nil, err
	}
	p, err := start(ctx, install.Path, dir, profile, imported)
	if err != nil {
		return nil, err
	}
	m.watchTargets(p)
	m.guard(p)
	m.mu.Lock()
	m.proc = p
	m.mu.Unlock()
	return p, nil
}

// ensureProfile seeds dir from the person's everyday profile the first
// time, and returns the profile name to open and whether it was imported.
// Called with startMu, not m.mu: a copy reads a whole profile.
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
			m.mu.Lock()
			m.lastImport = &rep
			m.mu.Unlock()
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
	// No start while the profile is replaced under it.
	m.startMu.Lock()
	defer m.startMu.Unlock()
	m.stop()
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
	m.mu.Lock()
	m.lastImport = &rep
	m.mu.Unlock()
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

// adopt adds a page that one of a conversation's tabs opened (a link
// with target=_blank, window.open) to that conversation, as its active
// tab. Opened from any of its tabs, not only the active one: a page in a
// tab the person switched away from can still open one; and from a page
// that is itself still being adopted, whose conversation it then is.
//
// Attached with m.mu let go, then checked again before it is published:
// the browser can have been replaced, the conversation closed, or the
// page closed (its close event then came before it was in any list, and
// is in gone) while it attached.
func (m *Manager) adopt(p *process, openerID, targetID string) {
	m.mu.Lock()
	conv := ""
	if m.proc == p {
		for name, c := range m.convs {
			if c.index(openerID) >= 0 {
				conv = name
				break
			}
		}
		if conv == "" {
			conv = m.adopting[openerID]
		}
	}
	if conv == "" {
		m.mu.Unlock()
		return
	}
	m.adopting[targetID] = conv
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	t, err := attachTab(ctx, p.conn, targetID)

	m.mu.Lock()
	_, still := m.adopting[targetID]
	delete(m.adopting, targetID)
	c := m.convs[conv]
	if err != nil {
		m.mu.Unlock()
		return
	}
	if m.proc != p || c == nil || !still || m.wasGone(targetID) {
		m.mu.Unlock()
		// The page a closed conversation's tab opened is nobody's.
		t.close(ctx)
		return
	}
	t.opener = openerID
	c.add(t)
	m.mu.Unlock()
}

// forget drops a page that closed from whichever conversation had it.
func (m *Manager) forget(targetID string) {
	var next *Tab
	var owner string
	m.mu.Lock()
	for conv, c := range m.convs {
		i := c.index(targetID)
		if i < 0 {
			continue
		}
		t := c.tabs[i]
		next, owner = c.remove(i), conv
		if m.proc != nil {
			m.proc.conn.Forget(t.session)
		}
		if len(c.tabs) == 0 {
			delete(m.convs, conv)
			close(c.changed)
		}
		break
	}
	if owner == "" {
		m.gone[m.goneNext] = targetID
		m.goneNext = (m.goneNext + 1) % goneKept
	}
	m.mu.Unlock()
	m.showActive(owner, next)
}

// CloseTab closes every page conv has.
func (m *Manager) CloseTab(conv string) {
	m.mu.Lock()
	c := m.convs[conv]
	delete(m.convs, conv)
	// A tab still opening for it is for a conversation that is gone: it
	// closes when it finds this, instead of bringing the conversation back.
	if o := m.opening[conv]; o != nil {
		o.cancelled = true
	}
	for target, owner := range m.adopting {
		if owner == conv {
			delete(m.adopting, target)
		}
	}
	m.mu.Unlock()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, t := range c.tabs {
		t.close(ctx)
	}
	close(c.changed)
}

// Close quits the browser. After a start in progress, which would
// otherwise publish a browser after it was closed.
func (m *Manager) Close() {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	m.stop()
}

// stop forgets the browser and every tab in it, then quits it. Called
// with startMu, not m.mu: quitting waits on the process.
func (m *Manager) stop() {
	m.mu.Lock()
	p := m.proc
	m.proc = nil
	if p != nil {
		m.dropAll()
	}
	m.mu.Unlock()
	if p != nil {
		p.close()
	}
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
		var gone <-chan struct{}
		if c != nil && m.proc != nil {
			t, changed = c.current(), c.changed
			gone = m.proc.conn.Done()
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
			case <-gone:
				// The browser went away with every tab in it: nothing more
				// will come, and the caller waits for the next one.
				stop()
				return errors.New("the browser went away")
			case f := <-frames:
				frame(f)
			}
		}
		stop()
	}
}
