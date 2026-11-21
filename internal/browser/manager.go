package browser

import (
	"context"
	"encoding/json"
	"errors"
	"localcode/internal/cdp"
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
// mu guards the state: the running browser and every conversation's tabs.
// It is held to read or change that state and never across anything that
// waits on the browser, a process or the disk: a slow CDP call for one
// conversation used to hold every other conversation's browser work
// behind it (Existing, Tabs, a switch, a close, a stream), for as long as
// a start or an attach took.
//
// What one global lock also gave, order, is kept per conversation: each
// conversation's browser work (opening a tab, attaching a page one of its
// tabs opened, a page closing, a switch, a close, deleting it) takes a
// ticket in its queue when it is asked for, a browser event in the order
// the browser sent it, and runs when its ticket comes up, across its calls
// to the browser. So a conversation's tabs change in the order its work
// was asked for, and only its own work waits for it. Existing, Tabs and
// Watch take no ticket.
//
// startMu orders what changes which browser there is (a start, a profile
// copy, a close), one at a time, so two conversations asking at once
// start one browser. Lock order: a ticket, then startMu, then mu.
type Manager struct {
	opts Options

	startMu sync.Mutex

	mu         sync.Mutex
	proc       *process
	convs      map[string]*convTabs
	lastImport *ImportReport

	// queues is each conversation's queue of browser work.
	queues map[string]*queue
	// adopting is each page a link opened that is waiting to be attached,
	// and the conversation it is for: claimed when the browser says it was
	// created, so a page it opens in turn finds its conversation before it
	// is in any list. Deleting the conversation drops its claims.
	adopting map[string]string
	// gone is the pages that closed while in no conversation's list, and
	// when: a tab this manager opened can close before it is published,
	// and is not published. Kept for goneFor, longer than any open takes.
	gone map[string]time.Time
}

// goneFor is how long a page that closed unlisted is remembered: longer
// than an open can take (openTab's and attachTab's callers give at most
// 45 seconds).
const goneFor = 2 * time.Minute

// queue is one conversation's browser work, in the order it was asked
// for: tickets are handed out in that order and served one at a time.
// Its condition is on Manager.mu. A queue with no ticket out is dropped,
// so there is one only while the conversation has work in it.
type queue struct {
	conv          string
	next, serving uint64
	abandoned     map[uint64]bool // tickets given up by callers that stopped waiting
	turn          *sync.Cond
}

// ticket is conv's next place in its queue. Called with m.mu.
func (m *Manager) ticket(conv string) (*queue, uint64) {
	q := m.queues[conv]
	if q == nil {
		q = &queue{conv: conv, abandoned: map[uint64]bool{}, turn: sync.NewCond(&m.mu)}
		m.queues[conv] = q
	}
	tk := q.next
	q.next++
	return q, tk
}

// await waits for tk to come up in q, or for ctx to end, and then gives
// the ticket up: it is skipped when it comes up, so the queue goes on.
// Called without m.mu.
func (m *Manager) await(ctx context.Context, q *queue, tk uint64) error {
	// A condition cannot wait on a context; ending it wakes every waiter,
	// and each looks at its own.
	stop := context.AfterFunc(ctx, func() {
		m.mu.Lock()
		q.turn.Broadcast()
		m.mu.Unlock()
	})
	defer stop()
	m.mu.Lock()
	defer m.mu.Unlock()
	for q.serving != tk {
		if err := ctx.Err(); err != nil {
			q.abandoned[tk] = true
			return err
		}
		q.turn.Wait()
	}
	return nil
}

// done passes q's turn to the next ticket still wanted, and drops the
// queue once none is out. Called without m.mu.
func (m *Manager) done(q *queue) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q.serving++
	for q.abandoned[q.serving] {
		delete(q.abandoned, q.serving)
		q.serving++
	}
	q.turn.Broadcast()
	if q.serving == q.next && m.queues[q.conv] == q {
		delete(m.queues, q.conv)
	}
}

// queued takes a ticket for conv and waits for it, and returns the
// function that passes the turn on; an error, and no turn, if ctx ended
// first.
func (m *Manager) queued(ctx context.Context, conv string) (func(), error) {
	m.mu.Lock()
	q, tk := m.ticket(conv)
	m.mu.Unlock()
	if err := m.await(ctx, q, tk); err != nil {
		return nil, err
	}
	return func() { m.done(q) }, nil
}

// wasGone is whether the page targetID closed while in no list. Called
// with m.mu.
func (m *Manager) wasGone(targetID string) bool {
	_, ok := m.gone[targetID]
	return ok
}

// markGone remembers that targetID closed while in no list, and forgets
// what closed longer ago than goneFor. Called with m.mu.
func (m *Manager) markGone(targetID string) {
	now := time.Now()
	for id, at := range m.gone {
		if now.Sub(at) > goneFor {
			delete(m.gone, id)
		}
	}
	m.gone[targetID] = now
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
	m.adopting = map[string]string{}
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

// front and painted put t in front of its window and wait for it to paint
// there. A browser with no window paints only the tab in front of each
// window, and the screencast the panel shows is built from those paints.
// A tab a link opened sits in its opener's window, in front of it, so a
// switch back to the opener showed no frame of it at all while the model
// worked on it. And a screencast started the moment the tab comes forward
// sends one frame and then nothing (measured: a second later, it streams),
// so this waits for two animation frames, which a page that is not shown
// does not run.
//
// It comes in two parts. front puts the tab in front: the browser does
// that without the page, and it runs in the conversation's turn, because
// two of them reaching the browser out of order would leave the wrong tab
// in front. painted waits for the page, which a busy or hung page can make
// take seconds, so it runs after the turn and holds up nothing of the
// conversation's that comes after. Neither is called with m.mu held.
func front(t *Tab) {
	if t == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = t.conn.Call(ctx, "", "Target.activateTarget", map[string]any{"targetId": t.targetID}, nil)
	_ = t.conn.Call(ctx, t.session, "Page.bringToFront", nil, nil)
}

// painted waits, up to five seconds, for t to paint two frames: see front.
func painted(t *Tab) {
	if t == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = t.conn.Call(ctx, t.session, "Runtime.evaluate", map[string]any{
		"expression":    "new Promise(r => requestAnimationFrame(() => requestAnimationFrame(() => r(true))))",
		"awaitPromise":  true,
		"returnByValue": true,
	}, nil)
}

// showActive puts conv's active tab t in front, in conv's turn, and
// returns the rest, for after the turn: wait for t to paint, then tell
// whoever is watching the conversation that it changed. Told first, a
// stream started on a tab not yet painting and stayed on one frame.
// Nothing is told if another change came in the meantime; that one tells.
// Not called with m.mu held.
func (m *Manager) showActive(conv string, t *Tab) (after func()) {
	if t == nil {
		return func() {}
	}
	front(t)
	return func() {
		painted(t)
		m.mu.Lock()
		defer m.mu.Unlock()
		if c := m.convs[conv]; c != nil && c.current() == t {
			c.notify()
		}
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
	return &Manager{
		opts:     opts,
		convs:    map[string]*convTabs{},
		queues:   map[string]*queue{},
		adopting: map[string]string{},
		gone:     map[string]time.Time{},
	}
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
	// The tab there now, if nothing of the conversation's is under way: a
	// page a link opened that is still attaching, or a tab opening, is
	// about to be the one it works in, and the answer waits for it.
	m.mu.Lock()
	if m.queues[conv] == nil {
		if t := m.existingLocked(conv); t != nil {
			m.mu.Unlock()
			return t, nil
		}
	}
	m.mu.Unlock()
	release, err := m.queued(ctx, conv)
	if err != nil {
		return nil, err
	}
	defer release()
	if t := m.Existing(conv); t != nil {
		return t, nil
	}
	t, err := m.openFor(ctx)
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

// openFor opens a tab on the running browser, starting it if it is not,
// with m.mu let go throughout, then checks what changed meanwhile: the
// browser replaced, or the page closed before it could be listed. Called
// in the conversation's turn, so nothing else of its can change.
//
// On success it returns with m.mu held, for the caller to publish the tab
// and let it go. On failure m.mu is not held and the tab, if one opened,
// is closed.
func (m *Manager) openFor(ctx context.Context) (*Tab, error) {
	// A caller that stopped waiting while queued opens nothing.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := m.running(ctx)
	var t *Tab
	if err == nil {
		t, err = openTab(ctx, p.conn)
	}
	m.mu.Lock()
	switch {
	case err != nil:
	case m.proc != p:
		err = errReplaced
	case m.wasGone(t.targetID):
		err = errReplaced
	}
	if err == nil {
		return t, nil
	}
	m.mu.Unlock()
	if t != nil {
		t.discard()
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
	release, err := m.queued(ctx, conv)
	if err != nil {
		return nil, err
	}
	defer release()
	t, err := m.openFor(ctx)
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
	release, err := m.queued(context.Background(), conv)
	if err != nil {
		return err
	}
	m.mu.Lock()
	c := m.convs[conv]
	// A browser that has gone away took its tabs with it, whatever the
	// list still says until the next start clears it.
	if c == nil || !m.alive() {
		m.mu.Unlock()
		release()
		return ErrNoSuchTab
	}
	i := c.index(id)
	if i < 0 {
		m.mu.Unlock()
		release()
		return ErrNoSuchTab
	}
	changed := i != c.active
	c.active = i
	t := c.current()
	m.mu.Unlock()
	after := func() { painted(t) }
	if changed {
		after = m.showActive(conv, t)
	} else {
		front(t)
	}
	release()
	after()
	return nil
}

// CloseTabID closes conv's tab id. When it was the active one, the tab
// before it becomes active; when it was the last, conv has none.
func (m *Manager) CloseTabID(ctx context.Context, conv, id string) error {
	release, err := m.queued(ctx, conv)
	if err != nil {
		return err
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	// A caller that stopped waiting changes nothing.
	if err := ctx.Err(); err != nil {
		return err
	}
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
	m.markGone(t.targetID)
	m.mu.Unlock()
	after := m.showActive(conv, next)
	// The page closes after the turn: a page that is slow to close holds
	// up nothing of the conversation's that comes after.
	release()
	release = nil
	after()
	t.discard()
	return nil
}

// Existing returns conv's tab if it has one, without starting anything.
// Nil once the browser has gone away, whatever tab it had.
func (m *Manager) Existing(conv string) *Tab {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.existingLocked(conv)
}

// existingLocked is Existing, called with m.mu.
func (m *Manager) existingLocked(conv string) *Tab {
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
		m.created(p, ev.TargetInfo.OpenerID, ev.TargetInfo.TargetID)
	})
	conn.On("", "Target.targetDestroyed", func(params json.RawMessage) {
		var ev struct {
			TargetID string `json:"targetId"`
		}
		if json.Unmarshal(params, &ev) == nil {
			m.destroyed(p, ev.TargetID)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = conn.Call(ctx, "", "Target.setDiscoverTargets", map[string]any{"discover": true}, nil)
}

// owner is the conversation a page belongs to: listed in its tabs, or
// claimed for it while it waits to be attached. "" for none. Called with
// m.mu.
func (m *Manager) owner(targetID string) string {
	for name, c := range m.convs {
		if c.index(targetID) >= 0 {
			return name
		}
	}
	return m.adopting[targetID]
}

// created is the browser saying a page opened another (a link with
// target=_blank, window.open). Called in the connection's read loop, in
// the order the browser sent its events, and quick: it claims the page
// for the opener's conversation and takes its place in that conversation's
// queue now, so a page the new one opens in turn finds its conversation,
// and the pages are added in the order they opened. The attach, which
// waits on the browser, is adopt's.
func (m *Manager) created(p *process, openerID, targetID string) {
	m.mu.Lock()
	if m.proc != p {
		m.mu.Unlock()
		return
	}
	conv := m.owner(openerID)
	if conv == "" {
		// Opened by a page this manager has let go of (its conversation
		// closed while the page was still there): nobody's, so closed.
		orphan := m.wasGone(openerID)
		m.mu.Unlock()
		if orphan {
			go closeTarget(p.conn, targetID)
		}
		return
	}
	m.adopting[targetID] = conv
	q, tk := m.ticket(conv)
	m.mu.Unlock()
	go m.adopt(p, conv, q, tk, openerID, targetID)
}

// adopt attaches a page created claimed for conv and adds it as conv's
// active tab, in conv's turn. Opened from any of its tabs, not only the
// active one: a page in a tab the person switched away from can still
// open one. Not added if the conversation was deleted (its claim is
// gone), the browser replaced, or the page already closed; a
// conversation whose last tab closed meanwhile has this one as its tab.
func (m *Manager) adopt(p *process, conv string, q *queue, tk uint64, openerID, targetID string) {
	_ = m.await(context.Background(), q, tk)
	defer m.done(q)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	t, err := attachTab(ctx, p.conn, targetID)

	m.mu.Lock()
	_, claimed := m.adopting[targetID]
	delete(m.adopting, targetID)
	if err != nil {
		// Not attached, it can be no conversation's tab: closed rather than
		// left open in the browser where nothing reaches it, and so are the
		// pages it opened before it closed.
		m.markGone(targetID)
		m.mu.Unlock()
		closeTarget(p.conn, targetID)
		return
	}
	if m.proc != p || !claimed || m.wasGone(targetID) {
		m.mu.Unlock()
		// The page a deleted conversation's tab opened is nobody's, and a
		// page that closed while it attached is not a tab.
		t.discard()
		return
	}
	t.opener = openerID
	if c := m.convs[conv]; c != nil {
		c.add(t)
	} else {
		m.convs[conv] = &convTabs{tabs: []*Tab{t}, changed: make(chan struct{})}
	}
	m.mu.Unlock()
}

// destroyed is the browser saying a page closed. Called in the connection's
// read loop, in event order, and quick: a page of a conversation (listed,
// or claimed and waiting to be attached) takes its place in that
// conversation's queue, behind its attach, and is dropped in its turn; a
// page of none is remembered in gone, for a tab that closed before it
// could be listed.
func (m *Manager) destroyed(p *process, targetID string) {
	m.mu.Lock()
	if m.proc != p {
		// A browser that was replaced: its tabs were dropped with it.
		m.mu.Unlock()
		return
	}
	if _, claimed := m.adopting[targetID]; claimed {
		// Closed before it was attached: never a tab. Its attach finds the
		// claim gone and the page in gone, and adds nothing; a tab of the
		// conversation stays the one it works in.
		delete(m.adopting, targetID)
		m.markGone(targetID)
		m.mu.Unlock()
		return
	}
	conv := m.owner(targetID)
	if conv == "" {
		m.markGone(targetID)
		m.mu.Unlock()
		return
	}
	q, tk := m.ticket(conv)
	m.mu.Unlock()
	go m.forget(conv, q, tk, targetID)
}

// forget drops a page that closed from conv, in conv's turn.
func (m *Manager) forget(conv string, q *queue, tk uint64, targetID string) {
	_ = m.await(context.Background(), q, tk)
	turn := true
	defer func() {
		if turn {
			m.done(q)
		}
	}()
	m.mu.Lock()
	c := m.convs[conv]
	i := -1
	if c != nil {
		i = c.index(targetID)
	}
	if i < 0 {
		// Never attached, or the conversation was deleted: nothing listed.
		m.markGone(targetID)
		m.mu.Unlock()
		return
	}
	t := c.tabs[i]
	next := c.remove(i)
	if len(c.tabs) == 0 {
		delete(m.convs, conv)
		close(c.changed)
	}
	conn := m.connLocked()
	m.mu.Unlock()
	if conn != nil {
		conn.Forget(t.session)
	}
	after := m.showActive(conv, next)
	turn = false
	m.done(q)
	after()
}

// connLocked is the running browser's connection, or nil. Called with
// m.mu.
func (m *Manager) connLocked() *cdp.Conn {
	if m.proc == nil {
		return nil
	}
	return m.proc.conn
}

// CloseTab closes every page conv has, in its turn: after the work asked
// for before it, a tab still opening included, so nothing it opens
// outlives the conversation. Its claims go too, so a page one of its tabs
// opened is closed rather than adopted.
func (m *Manager) CloseTab(conv string) {
	release, _ := m.queued(context.Background(), conv)
	m.mu.Lock()
	c := m.convs[conv]
	delete(m.convs, conv)
	for target, owner := range m.adopting {
		if owner == conv {
			delete(m.adopting, target)
			// A page it opens before its attach closes it is closed too.
			m.markGone(target)
		}
	}
	if c != nil {
		for _, t := range c.tabs {
			m.markGone(t.targetID)
		}
	}
	m.mu.Unlock()
	// The pages close after the turn, and side by side: nothing of the
	// conversation's is listed any more, and a page slow to close holds
	// up no other.
	release()
	if c == nil {
		return
	}
	var wg sync.WaitGroup
	for _, t := range c.tabs {
		wg.Add(1)
		go func(t *Tab) {
			defer wg.Done()
			t.discard()
		}(t)
	}
	wg.Wait()
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
