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
	// asks is the links pages have asked the browser to open in a tab or
	// a window of their own, oldest first, each until its page turns up:
	// see ask.
	asks []ask
	// born is where the browser put each page that is claimed and not
	// yet attached, as the page said when it started: see bornAt.
	born map[string]string
	// popups is that a conversation has had a page in a popup window on
	// this browser, and landings the windows kept for what such a page
	// opens: the blank page of each, and its window's number, 0 until the
	// browser has said it. One, but for the moment one is replaced. See
	// land.
	popups   bool
	landings map[string]int
}

// ask is one link a page asked the browser to open in a tab or a window
// of its own.
type ask struct {
	from string // the page that asked
	conv string // its conversation, when it asked
	url  string
	at   time.Time
	// closed is that the conversation was closed since: the page asked
	// for is nobody's, and is closed when it turns up.
	closed bool
}

// askFor is how long an ask waits for its page. The browser makes the
// page within tens of milliseconds of being asked (measured, at most 75);
// this is for a machine too busy to.
const askFor = 10 * time.Second

// askAfter is how long after a click or a key that can open a link
// elsewhere a page's ask is taken for that click's or key's: see ask.
// The page asks as it handles the event, and a busy page handles it late.
const askAfter = 10 * time.Second

// askMax is how many asks are kept: more than are ever open at once, and
// a bound on what a page that asks without end can make this hold.
const askMax = 64

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
	m.born = map[string]string{}
	m.asks = nil
	m.popups, m.landings = false, map[string]int{}
}

// convTabs is a conversation's pages, in the order they were opened, and
// the one it works in: the one the model's browser tool acts on and the
// panel shows. A tab a link opens, or one the person opens in the panel,
// is added and becomes the active one. Closing the active tab makes its
// opener active again, as a browser does, when it was opened by a link
// and the opener is still open; otherwise the one before it in the list.
type convTabs struct {
	tabs     []*Tab
	active   int
	changed  chan struct{} // closed when the active tab changes
	switches uint64        // showActive calls so far: which one tells
	// unshown is that the active tab became the active one without being
	// known to be in front, and nobody watching has been told of it: it
	// is to be brought forward and told of once painted. See settle.
	unshown bool
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

// closed tells whoever watches c that a tab of it was removed, when next,
// the active tab remove reported, is nil: none is left (told for the last
// time), or the active one is the same. A watcher can be on the removed
// one even then, because a switch away from it is told only once the tab
// switched to paints (see showActive); told now, it moves to the active
// one. A change of active tab is told by showActive. Called with m.mu.
func (c *convTabs) closed(next *Tab) {
	switch {
	case len(c.tabs) == 0:
		close(c.changed)
	case next == nil:
		c.notify()
	}
}

// add appends t and makes it the active tab: a page in front of its
// window, which whoever watches is told of at once.
func (c *convTabs) add(t *Tab) {
	c.tabs = append(c.tabs, t)
	c.active = len(c.tabs) - 1
	c.unshown = false
	c.notify()
}

// addBehind appends t and leaves the active tab the one it was: a page
// the browser opened behind it. Nobody watching is told, because what
// they watch has not changed; the panel's tab strip reads the list.
func (c *convTabs) addBehind(t *Tab) {
	c.tabs = append(c.tabs, t)
}

// addUntold appends t and makes it the active tab, and leaves telling
// whoever watches to settle, once the tab is in front and painted.
func (c *convTabs) addUntold(t *Tab) {
	c.tabs = append(c.tabs, t)
	c.active = len(c.tabs) - 1
	c.unshown = true
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
// That holds for a switch back to the same tab too (A to B, to A, to B):
// the first switch to B can finish waiting while B is behind A, and a
// stream started then stays on one frame. Not called with m.mu held.
func (m *Manager) showActive(conv string, t *Tab) (after func()) {
	if t == nil {
		return func() {}
	}
	m.mu.Lock()
	var n uint64
	if c := m.convs[conv]; c != nil {
		c.switches++
		n = c.switches
		if c.current() == t {
			c.unshown = false
		}
	}
	m.mu.Unlock()
	front(t)
	return func() {
		painted(t)
		m.mu.Lock()
		defer m.mu.Unlock()
		if c := m.convs[conv]; c != nil && c.current() == t && c.switches == n {
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
		born:     map[string]string{},
		landings: map[string]int{},
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
	// A caller that stopped waiting is told so, whatever the turn finds.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
		t, err = openTab(ctx, p.conn, func() { m.landAfter(p) })
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
	c.closed(next)
	if len(c.tabs) == 0 {
		delete(m.convs, conv)
	}
	m.markGone(t.targetID)
	m.mu.Unlock()
	after := m.showActive(conv, next)
	// The page closes after the turn: a page that is slow to close holds
	// up nothing of the conversation's that comes after. It closes while
	// the next one paints, and neither waits for the other.
	release()
	release = nil
	discarded := make(chan struct{})
	go func() {
		t.discard()
		close(discarded)
	}()
	after()
	<-discarded
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
// target=_blank, window.open, a link opened in a tab or a window of its
// own) and closes, so the conversation keeps working in the page in
// front, as a person would.
func (m *Manager) watchTargets(p *process) {
	conn := p.conn
	conn.On("", "Target.targetCreated", func(params json.RawMessage) {
		var ev struct {
			TargetInfo struct {
				TargetID string `json:"targetId"`
				Type     string `json:"type"`
				URL      string `json:"url"`
				OpenerID string `json:"openerId"`
			} `json:"targetInfo"`
		}
		if json.Unmarshal(params, &ev) != nil || ev.TargetInfo.Type != "page" {
			return
		}
		if ev.TargetInfo.OpenerID == "" {
			// No page opened it, as the browser tells it: one a page asked
			// for, or not a conversation's (a tab this manager is opening,
			// the page the browser starts with).
			m.made(p, ev.TargetInfo.TargetID, ev.TargetInfo.URL)
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

// ask is a page asking the browser to open a link somewhere other than
// in itself: params is the Page.frameRequestedNavigation event, sent on
// the session of the frame the link is in, and from is the page that
// frame is in. Called in the connection's read loop, and quick.
//
// A link clicked with the wheel, or with Ctrl, Cmd or Shift held, and
// Enter pressed on a focused link with one of them held, opens in a tab
// or a window of its own. The browser makes that page with no opener:
// nothing in what it says of the page tells which page it came from, so
// such a page was no conversation's. It was in no tab strip and could not
// be closed, and one opened in front (Ctrl+Shift+click, Shift and the
// wheel) covered the conversation's page in its window, which the browser
// then stopped drawing: the panel showed a still picture that took no
// click.
//
// What does tell is this event. Measured on Chrome 140 to 158 on macOS
// and Linux, and on Chrome and Edge on Windows, over every such click
// and key, and a form sent with one: the page it is sent for says it
// first, with the address and "newTab" or "newWindow", and the browser
// then makes a page with that same address, character for character (a
// fragment, a query, an address it had to escape, one three thousand
// characters long, one that then redirects). So an ask is kept until a
// page with no opener and its address turns up, and that page is the
// asking page's conversation's: see made.
//
// The address is all that ties the two, and a page can say this with no
// page to follow: a script that sends a link a click of its own making,
// with Shift or Ctrl set on it, has the page ask, and the browser then
// refuses to open anything for a click nobody made. Such an ask stayed,
// and the next page made with its address was taken for it, whichever
// conversation's it was. So an ask is kept only:
//
//   - from a page that is a conversation's tab, for that conversation;
//   - within askAfter of a click or a key this manager sent that page
//     which can open a link elsewhere (see Tab.opening): a hidden browser
//     gets no other, so an ask at any other time is a script's;
//   - for askFor.
//
// And the newest ask for an address is the one a page is taken for, so
// one left behind does not come before the ask a click has just made.
// What is left: a page that is being clicked with a key held, and asks
// again and again for an address another conversation opens in those
// same seconds, can have that conversation's page listed as its own.
//
// Not kept either: an ask for about:blank, which is the address this
// manager opens its own tabs with (see openTab), so that no tab of its
// own is taken for a page a conversation asked for.
func (m *Manager) ask(p *process, from string, params json.RawMessage) {
	var ev struct {
		URL         string `json:"url"`
		Disposition string `json:"disposition"`
	}
	if json.Unmarshal(params, &ev) != nil {
		return
	}
	if ev.Disposition != "newTab" && ev.Disposition != "newWindow" {
		return
	}
	if ev.URL == "" || ev.URL == blankPage {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.proc != p {
		return
	}
	conv, t := m.listed(from)
	if t == nil || time.Since(t.opening()) > askAfter {
		return
	}
	m.asks = append(m.fresh(), ask{from: from, conv: conv, url: ev.URL, at: time.Now()})
	if len(m.asks) > askMax {
		m.asks = m.asks[len(m.asks)-askMax:]
	}
}

// listed is the tab with targetID in a conversation's list, and that
// conversation; nil for a page in none. Called with m.mu.
func (m *Manager) listed(targetID string) (string, *Tab) {
	for name, c := range m.convs {
		if i := c.index(targetID); i >= 0 {
			return name, c.tabs[i]
		}
	}
	return "", nil
}

// fresh is the asks still waiting for their page: those older than
// askFor are dropped. Called with m.mu.
func (m *Manager) fresh() []ask {
	now := time.Now()
	n := 0
	for n < len(m.asks) && now.Sub(m.asks[n].at) > askFor {
		n++
	}
	m.asks = m.asks[n:]
	return m.asks
}

// made is the browser saying it made a page with no opener, at address
// url. A page that was asked for (see ask) is claimed for the
// conversation that asked, as a page with an opener is for its opener's
// (see created), and comes from the page that asked: closing its tab
// goes back to that page's. The conversation, and not the asking page,
// because that page may have closed since, and what it asked for is still
// the conversation's. A page a conversation asked for and then was closed
// is nobody's, and is closed. Any other page is left alone. Called in
// the connection's read loop, in the order the browser sent its events.
func (m *Manager) made(p *process, targetID, url string) {
	m.mu.Lock()
	if m.proc != p {
		m.mu.Unlock()
		return
	}
	asks := m.fresh()
	i := len(asks) - 1
	for i >= 0 && asks[i].url != url {
		i--
	}
	if i < 0 {
		m.mu.Unlock()
		return
	}
	a := asks[i]
	m.asks = append(m.asks[:i], m.asks[i+1:]...)
	if a.closed {
		// And so is a page it opens before its close reaches the browser.
		m.markGone(targetID)
		m.mu.Unlock()
		go closeTarget(p.conn, targetID)
		return
	}
	m.claim(p, a.conv, a.from, targetID)
}

// land makes a new landing window, the newest window the browser has,
// and closes the page of the one before. Not called with m.mu.
//
// A page in a popup window, one a script opened with a size as a sign-in
// window is, has no tab strip. A tab it opens (a link with target=_blank,
// window.open with no size, a link opened with a key held) the browser
// puts in another window: the tabbed window that was made last. Measured
// on Chrome on macOS, Linux and Windows and on Edge on Windows: it is
// the one made last and no other, whichever tab was put in front, brought
// forward or clicked in since. That window was whichever conversation's
// had opened a tab last, so one conversation's page was put in another's
// window: in front, where it covered that conversation's page, which the
// browser then stopped drawing, or behind, to cover it when switched to.
//
// So once a conversation has had a page in a popup window, the tabbed
// window made last is kept one of this manager's own, with a blank page
// and no conversation's: the landing window. What a popup's page opens
// lands there, with nothing of anybody's to cover. A new one is made
// after each thing that makes a tabbed window:
//
//   - a page that has landed there, which has that window from then on,
//     and the blank page it landed beside is closed (see landed);
//   - a tab this manager opens, which is in a new window (see landAfter);
//   - a link opened in a window of its own, with Shift held, which the
//     browser makes a tabbed window for (see landed).
//
// Before the first popup there is none, and nothing is made or asked.
//
// Windows are numbered in the order they are made. That is how a tabbed
// window made after the landing window is known, and how two landing
// windows made at once are told apart: the later is kept. A landing
// window is listed as soon as the browser has made it, before its number
// is known, so that a page that lands in it in that moment is seen to
// have.
//
// What is left: a popup's page that opens a tab in the moment between a
// new tabbed window and the landing window made after it lands in that
// window; two pages of different conversations that land in the landing
// window before either is seen share it; a page that says its window has
// a toolbar when it has none is not known to be in a popup; and a page
// the browser makes in a window of its own that is no conversation's
// tab is not seen at all.
func (m *Manager) land(p *process) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var made struct {
		TargetID string `json:"targetId"`
	}
	if err := p.conn.Call(ctx, "", "Target.createTarget", map[string]any{"url": blankPage, "newWindow": true}, &made); err != nil {
		return
	}
	mine := made.TargetID
	m.mu.Lock()
	// Not kept: no popup any more (the browser was replaced), or its
	// page has closed already.
	kept := m.proc == p && m.popups && !m.wasGone(mine)
	if kept {
		m.landings[mine] = 0
	}
	m.mu.Unlock()
	if !kept {
		closeTarget(p.conn, mine)
		return
	}
	window := windowOf(ctx, p.conn, mine)
	var old []string
	m.mu.Lock()
	// Still listed: a page that landed in it meanwhile takes it out.
	if _, listed := m.landings[mine]; listed && m.proc == p {
		if window == 0 {
			// The browser does not say which window it is: no use.
			delete(m.landings, mine)
			old = append(old, mine)
		} else {
			m.landings[mine] = window
			newest := 0
			for _, w := range m.landings {
				newest = max(newest, w)
			}
			for target, w := range m.landings {
				if w != 0 && w < newest {
					delete(m.landings, target)
					old = append(old, target)
				}
			}
		}
	}
	m.mu.Unlock()
	for _, target := range old {
		closeTarget(p.conn, target)
	}
}

// landAfter makes a new landing window after a window of this manager's
// own making, if there is to be one: see land. Not called with m.mu.
func (m *Manager) landAfter(p *process) {
	m.mu.Lock()
	wanted := m.proc == p && m.popups
	m.mu.Unlock()
	if wanted {
		m.land(p)
	}
}

// landed looks at where a page that is becoming a conversation's tab is:
// see land. A page in a popup window is the reason for a landing window,
// and makes one if there is none. A page in a landing window has landed:
// the window is its own from here, a new landing window is made first,
// and then the blank page it landed beside is closed. A page in a tabbed
// window made after the landing window is one the browser made a window
// for, and a new landing window is made after it. Called in the page's
// conversation's turn, not with m.mu.
func (m *Manager) landed(ctx context.Context, p *process, targetID string, popup bool) {
	m.mu.Lock()
	if m.proc != p {
		m.mu.Unlock()
		return
	}
	if popup {
		m.popups = true
	}
	wanted := m.popups
	landings := make(map[string]int, len(m.landings))
	for target, window := range m.landings {
		landings[target] = window
	}
	m.mu.Unlock()
	if !wanted {
		return
	}
	if len(landings) == 0 {
		// The first popup, or the last landing window could not be made.
		m.land(p)
		return
	}
	if popup {
		// In a window of its own, which no tab lands in.
		return
	}
	window := windowOf(ctx, p.conn, targetID)
	if window == 0 {
		return
	}
	newest := 0
	for target, w := range landings {
		if w == 0 {
			// Made, and its number not yet known to this manager.
			w = windowOf(ctx, p.conn, target)
		}
		if w == window {
			m.mu.Lock()
			_, mine := m.landings[target]
			delete(m.landings, target)
			m.mu.Unlock()
			if mine {
				m.land(p)
				closeTarget(p.conn, target)
			}
			return
		}
		newest = max(newest, w)
	}
	if window > newest {
		m.land(p)
	}
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
		if orphan {
			// And so is a page it opens before its close reaches the
			// browser.
			m.markGone(targetID)
		}
		m.mu.Unlock()
		if orphan {
			go closeTarget(p.conn, targetID)
		}
		return
	}
	m.claim(p, conv, openerID, targetID)
}

// claim claims the page targetID for conv, as one the page from opened,
// and takes its place in conv's queue: see created. Called with m.mu,
// which it lets go.
func (m *Manager) claim(p *process, conv, from, targetID string) {
	m.adopting[targetID] = conv
	q, tk := m.ticket(conv)
	m.mu.Unlock()
	go m.adopt(p, conv, q, tk, from, targetID)
}

// adopt attaches a page created claimed for conv and adds it as a tab of
// conv's, in conv's turn. Opened from any of its tabs, not only the
// active one: a page in a tab the person switched away from can still
// open one. Not added if the conversation was deleted (its claim is
// gone), the browser replaced, or the page already closed; a
// conversation whose last tab closed meanwhile has this one as its tab.
//
// The tab is where the browser put the page. In front of its window, it
// becomes the one the conversation works in. Behind the tab in front (a
// link opened with Ctrl or Cmd held, or with the wheel, and a window.open
// from a click with one of them held), it is added behind, and the
// conversation goes on in the tab it was in. Made the active one all the
// same, as it was at first, such a page was one the browser did not
// draw: the panel showed a picture that did not change. A page that does
// not say where it is becomes the one it works in, as every page a link
// opened did before, and is put in front. Where the browser put it is
// what the page said as it started (see bornAt).
func (m *Manager) adopt(p *process, conv string, q *queue, tk uint64, openerID, targetID string) {
	_ = m.await(context.Background(), q, tk)
	// A page it does not publish is closed after the turn, so a page slow
	// to close holds up nothing of the conversation's that comes after.
	turn := true
	release := func() {
		if turn {
			turn = false
			m.done(q)
		}
	}
	defer release()
	m.mu.Lock()
	_, claimed := m.adopting[targetID]
	stale := m.proc != p || !claimed || m.wasGone(targetID)
	if stale {
		delete(m.adopting, targetID)
		delete(m.born, targetID)
	}
	m.mu.Unlock()
	if stale {
		// Its conversation closed, or it did, while it waited for its
		// turn: nobody's, closed without attaching to it.
		release()
		closeTarget(p.conn, targetID)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	t, err := attachTab(ctx, p.conn, targetID)
	// Where the browser put it is what it said as it started, if it said:
	// where it is by now is something else, since a page opened after it
	// may have covered it, or this manager put another in front. Looked
	// for once the page is attached, which is after it has said it: a
	// page claimed with nothing ahead of it has its turn before it has
	// said anything.
	m.mu.Lock()
	said, early := m.born[targetID]
	delete(m.born, targetID)
	m.mu.Unlock()
	behind, known, popup := false, false, false
	if err == nil {
		if early {
			behind, known, popup = placed(said)
		}
		if !known {
			behind, known, popup = t.where(ctx)
		}
		m.landed(ctx, p, targetID, popup)
	}

	m.mu.Lock()
	_, claimed = m.adopting[targetID]
	delete(m.adopting, targetID)
	if err != nil {
		// Not attached, it can be no conversation's tab: closed rather than
		// left open in the browser where nothing reaches it, and so are the
		// pages it opened before it closed. If it was the last the
		// conversation waited for, what is in front is settled, as after
		// a page that is added.
		m.markGone(targetID)
		m.mu.Unlock()
		after := m.settle(conv)
		release()
		after()
		closeTarget(p.conn, targetID)
		return
	}
	if m.proc != p || !claimed || m.wasGone(targetID) {
		m.mu.Unlock()
		// The page a deleted conversation's tab opened is nobody's, and a
		// page that closed while it attached is not a tab.
		release()
		t.discard()
		return
	}
	t.opener = openerID
	inFront := known && !behind
	c := m.convs[conv]
	switch {
	case c == nil:
		// Its only tab, and so the one it works in, wherever it is.
		c = &convTabs{tabs: []*Tab{t}, changed: make(chan struct{}), unshown: !inFront}
		m.convs[conv] = c
	case known && behind:
		c.addBehind(t)
	case inFront:
		c.add(t)
	default:
		// Not known to be in front: whoever watches is told once it has
		// been brought forward and has painted, not now.
		c.addUntold(t)
	}
	m.mu.Unlock()
	after := m.settle(conv)
	release()
	after()
}

// settle puts the tab conv works in in front, in conv's turn, once no
// more of its pages wait to be added, and returns the rest, for after
// the turn (see showActive).
//
// While more wait, nothing is brought forward: that would cover one of
// them that the browser opened in front, and the last of them settles
// it. Then the tab it works in is put in front, which changes nothing
// when it is there. It may not be: a page opened in front can have been
// covered since by one opened after it that closed before it was
// anybody's tab, after which the browser brings some other tab forward.
// A tab that became the active one without being known to be in front
// (see convTabs.unshown) is brought forward as one switched to, and
// whoever watches told once it has painted.
//
// A tab switch, or a close, that comes while a page waits does bring its
// tab forward. If that page was opened in front and had not said so as it
// started, it then says "hidden" and is listed behind.
func (m *Manager) settle(conv string) (after func()) {
	m.mu.Lock()
	c := m.convs[conv]
	if c == nil || m.waiting(conv) {
		m.mu.Unlock()
		return func() {}
	}
	active, unshown := c.current(), c.unshown
	m.mu.Unlock()
	if unshown {
		return m.showActive(conv, active)
	}
	front(active)
	return func() {}
}

// waiting is whether a page claimed for conv is still to be attached.
// Called with m.mu.
func (m *Manager) waiting(conv string) bool {
	for _, owner := range m.adopting {
		if owner == conv {
			return true
		}
	}
	return false
}

// bornAt is a page saying, as it starts, where the browser put it (see
// whereIs): kept for a page that is claimed and waits to be attached,
// which is asked nothing later if it said. Called from the guard, which
// is attached to every page before it runs.
//
// The page is asked then, and not when its turn comes to be attached,
// because by then the answer is about something else. A page opened in
// front says "hidden" once a page opened after it is in front of it, or
// once this manager has brought another forward; taken for one opened
// behind, the page the browser had put in front was listed behind and
// left there.
func (m *Manager) bornAt(p *process, targetID, said string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, claimed := m.adopting[targetID]; m.proc == p && claimed {
		m.born[targetID] = said
	}
}

// refront settles what is in front for conv, in conv's turn: a page of
// its that was never attached has closed, and if it had been in front
// the browser has brought some other tab of that window forward.
func (m *Manager) refront(conv string, q *queue, tk uint64) {
	_ = m.await(context.Background(), q, tk)
	after := m.settle(conv)
	m.done(q)
	after()
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
	if _, landing := m.landings[targetID]; landing {
		// A landing window's page, closed by something other than this
		// manager, which takes one out of the list before it closes it.
		// If that leaves none, one is made.
		delete(m.landings, targetID)
		m.markGone(targetID)
		none := len(m.landings) == 0
		m.mu.Unlock()
		if none {
			go m.land(p)
		}
		return
	}
	if conv, claimed := m.adopting[targetID]; claimed {
		// Closed before it was attached: never a tab. Its attach finds the
		// claim gone and the page in gone, and adds nothing; a tab of the
		// conversation stays the one it works in, and is put back in
		// front in its turn.
		delete(m.adopting, targetID)
		delete(m.born, targetID)
		m.markGone(targetID)
		q, tk := m.ticket(conv)
		m.mu.Unlock()
		go m.refront(conv, q, tk)
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
	c.closed(next)
	if len(c.tabs) == 0 {
		delete(m.convs, conv)
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

// CloseTab closes every page conv has. Its tabs leave the list at once,
// so nothing lists, streams or drives them once it has returned, and its
// claims go too, so a page one of its tabs opened is closed rather than
// adopted. The pages close in its turn: after the work asked for before
// it, a tab still opening included, whose tab it closes as well, so
// nothing that work opens outlives the conversation. It does not wait
// for that turn, so whoever deletes or archives a conversation waits on
// no page; what it returns is closed once the pages are.
func (m *Manager) CloseTab(conv string) <-chan struct{} {
	m.mu.Lock()
	q, tk := m.ticket(conv)
	now := m.dropLocked(conv)
	m.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = m.await(context.Background(), q, tk)
		m.mu.Lock()
		// What the work ahead of it published in the meantime.
		later := m.dropLocked(conv)
		m.mu.Unlock()
		// The pages close after the turn, and side by side: nothing of the
		// conversation's is listed any more, and a page slow to close holds
		// up no other.
		m.done(q)
		var wg sync.WaitGroup
		for _, c := range []*convTabs{now, later} {
			if c == nil {
				continue
			}
			for _, t := range c.tabs {
				wg.Add(1)
				go func(t *Tab) {
					defer wg.Done()
					t.discard()
				}(t)
			}
		}
		wg.Wait()
	}()
	return done
}

// dropLocked takes conv out of the lists: its tabs, which it returns,
// and its claims, all remembered as gone. Whoever watches is told: the
// conversation has no tab from here on. Called with m.mu.
func (m *Manager) dropLocked(conv string) *convTabs {
	for i := range m.asks {
		if m.asks[i].conv == conv {
			m.asks[i].closed = true
		}
	}
	for target, owner := range m.adopting {
		if owner == conv {
			delete(m.adopting, target)
			delete(m.born, target)
			// A page it opens before its attach closes it is closed too.
			m.markGone(target)
		}
	}
	c := m.convs[conv]
	if c == nil {
		return nil
	}
	delete(m.convs, conv)
	for _, t := range c.tabs {
		m.markGone(t.targetID)
	}
	close(c.changed)
	return c
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
