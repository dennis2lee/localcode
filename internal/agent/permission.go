package agent

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/session"
	"localcode/internal/tools"
)

type ctxKey int

const sessionIDKey ctxKey = 0

func WithSessionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sessionIDKey, id)
}

func SessionIDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(sessionIDKey).(string)
	return v, ok
}

// Permission scopes an approval can carry, answering "and don't ask me
// again for..." with three different amounts of again.
const (
	// ScopeOnce approves exactly this call. The default, and what a bare
	// allow with no scope means.
	ScopeOnce = "once"
	// ScopeSession approves matching calls for the rest of this session,
	// in memory only. Forgotten on daemon restart.
	ScopeSession = "session"
	// ScopeAlways approves matching calls permanently by writing a rule
	// into config.json, and also covers the current session.
	ScopeAlways = "always"

	// The two answers a workspace-boundary question accepts, and the
	// reason it is a different question from every other one.
	//
	// An ordinary permission asks about a tool call. This one asks about
	// a place, and a place is answered at one of two sizes: this
	// directory, or anywhere. Offering only "once" and "for the session"
	// would have made the useful answer impossible to give — a model told
	// to read the sibling repository reads forty files in it, and forty
	// prompts is one decision and thirty-nine keystrokes, which is how a
	// permission prompt stops being read.
	//
	// ScopeOutsideDir remembers the directory for the rest of the
	// session, in memory. ScopeOutsideAll turns this session's read- or
	// write-outside switch on, which is the same thing the Permissions
	// window and /read-outside do, so the answer is visible afterwards
	// rather than being an invisible state only the broker knows about.
	ScopeOutsideDir = "outside-dir"
	ScopeOutsideAll = "outside-all"
)

// PermissionBroker turns a blocking tool-permission check into a
// request/response pair of session events (permission.request /
// permission.resolved), so any subscribed client (TUI, web) can answer it.
// The tool call goroutine blocks on Func's returned call until Resolve is
// invoked with the matching id, or the context is cancelled.
type PermissionBroker struct {
	store *session.Store

	// ConfigPath is where ScopeAlways writes its rule. Empty disables
	// permanent approvals, leaving only once and session, which is the
	// right behavior when the daemon was started against a config file it
	// has no business rewriting.
	ConfigPath string

	mu      sync.Mutex
	counter int
	pending map[string]chan resolution
	// granted remembers ScopeSession (and ScopeAlways) approvals, keyed by
	// session, then by the rule pattern that was approved. Keeping it here
	// rather than in the config Resolver is deliberate: these grants are
	// per session and must not leak into other sessions or outlive the
	// process.
	granted map[string]map[string]bool
	// outside remembers the directories a session has approved leaving
	// the workspace for, per class. Separate from granted because it is
	// matched by containment rather than by an exact pattern, and keyed
	// by class rather than by tool: approving a directory for reading
	// approves it for read_file, grep and glob alike, since what was
	// approved is the place, not the tool that happened to reach it.
	//
	// In memory, and per session, like every other session-scoped grant.
	// A directory approved for one conversation is not approved for the
	// next one, and nothing here survives a restart.
	outside map[string]map[tools.OutsideClass][]string
	// policy is consulted for the "yes, anywhere" answer, which turns a
	// session switch on rather than remembering something privately.
	policy *PermissionPolicy
	// onChanged, if set, tells the daemon a session's switches moved, so
	// a window showing them follows an answer given at the prompt.
	onChanged func(sessionID string)
}

// resolution is one answer to a permission request: whether to allow, and
// how long that answer lasts.
type resolution struct {
	allow bool
	scope string
}

func NewPermissionBroker(store *session.Store) *PermissionBroker {
	return &PermissionBroker{
		store:   store,
		pending: map[string]chan resolution{},
		granted: map[string]map[string]bool{},
		outside: map[string]map[tools.OutsideClass][]string{},
	}
}

// SetPolicy gives the broker the session switches, so a "yes, anywhere"
// answer can turn one on. onChanged is told which session moved.
func (b *PermissionBroker) SetPolicy(p *PermissionPolicy, onChanged func(sessionID string)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.policy = p
	b.onChanged = onChanged
}

func (b *PermissionBroker) Func() tools.PermissionFunc {
	return func(ctx context.Context, ask tools.Ask) (bool, error) {
		toolName, subject, description := ask.Tool, ask.Subject, ask.Description
		sessionID, ok := SessionIDFromContext(ctx)
		if !ok {
			return false, fmt.Errorf("permission check has no session context")
		}

		// An earlier "allow for this session" (or "always") already covers
		// this call, so don't ask again.
		rule := config.PermissionRuleFor(toolName, subject)
		if b.isGranted(sessionID, toolName, rule.Match) {
			return true, nil
		}
		// A directory this conversation already approved leaving the
		// workspace for. Checked before anything is written to the log,
		// so a remembered answer produces no prompt and no event at all.
		if ask.Outside != tools.OutsideNone && b.outsideGranted(sessionID, ask.Outside, ask.Subject, ask.Dir) {
			return true, nil
		}

		b.mu.Lock()
		b.counter++
		id := fmt.Sprintf("p%d", b.counter)
		ch := make(chan resolution, 1)
		b.pending[id] = ch
		b.mu.Unlock()

		// Where this question has to appear for someone to answer it. For
		// an ordinary session that is the session itself; for a background
		// task it is also the conversation that spawned it, since nothing
		// streams a task's own log.
		where := b.audience(sessionID)

		for _, target := range where.mirrors {
			b.store.Append(target, events.TypePermissionRequest, outsideFields(ask, map[string]any{
				"id":   id,
				"tool": toolName,
				// Prefixed rather than carried in a separate field so both
				// clients say where it came from without needing to know
				// about tasks: the request is for work the user started
				// but is not watching, and answering it as if it came from
				// the conversation on screen would be misleading.
				"description":  fmt.Sprintf("[background task %s] %s", sessionID, description),
				"rule":         rule.Match,
				"can_always":   b.ConfigPath != "",
				"task_session": sessionID,
			}))
		}

		if _, err := b.store.Append(sessionID, events.TypePermissionRequest, outsideFields(ask, map[string]any{
			"id":          id,
			"tool":        toolName,
			"description": description,
			// The pattern a session/always approval would grant, so clients
			// can show what is actually being widened rather than making
			// the user guess.
			"rule":       rule.Match,
			"can_always": b.ConfigPath != "",
		})); err != nil {
			return false, err
		}

		// A turn nobody is watching cannot wait forever, and until this
		// existed it did: a request has no timeout, so a scheduled turn
		// that asked at three in the morning blocked on a channel nothing
		// would ever send to, held its session busy, and was found having
		// done nothing. Not zero, because the common case is that
		// somebody is at the desk and the question is mirrored into the
		// conversation that booked the work.
		var giveUp <-chan time.Time
		if Unattended(ctx) {
			t := time.NewTimer(unattendedPermissionWait)
			defer t.Stop()
			giveUp = t.C
		}

		select {
		case res := <-ch:
			if res.allow {
				b.applyScope(sessionID, toolName, rule, ask, res.scope)
			}
			for _, target := range where.all() {
				b.store.Append(target, events.TypePermissionResolved, map[string]any{
					"id": id, "allow": res.allow, "scope": res.scope,
				})
			}
			return res.allow, nil
		case <-giveUp:
			b.mu.Lock()
			delete(b.pending, id)
			b.mu.Unlock()
			// The question goes off screen with the same event an answer
			// produces, so a mirrored prompt does not sit in the parent
			// conversation refusing every message for a turn that has
			// already moved on.
			for _, target := range where.all() {
				b.store.Append(target, events.TypePermissionResolved, map[string]any{
					"id": id, "allow": false, "scope": ScopeOnce, "unanswered": true,
				})
			}
			return false, &tools.RefusedError{Reason: unattendedRefusal(ask)}
		case <-ctx.Done():
			b.mu.Lock()
			delete(b.pending, id)
			b.mu.Unlock()
			// The question is gone, so the modal asking it has to go too —
			// otherwise cancelling a stuck task leaves its request on
			// screen in the parent conversation, refusing every message
			// for a task that no longer exists.
			for _, target := range where.all() {
				b.store.Append(target, events.TypePermissionResolved, map[string]any{
					"id": id, "allow": false, "scope": ScopeOnce, "cancelled": true,
				})
			}
			return false, ctx.Err()
		}
	}
}

// applyScope is what an allow means beyond this one call.
//
// Split out because there are five answers now and the branching had
// outgrown the select. The two boundary answers are handled first and
// exclusively: a question raised by the boundary is about a place, so
// answering it must not also widen a tool rule that was never the
// subject. "Allow reads under /Users/me/other" is not "allow read_file
// /Users/me/other/x.go for the session", and granting both would leave a
// rule behind that outlives the reason it was written.
func (b *PermissionBroker) applyScope(sessionID, toolName string, rule config.PermissionRule, ask tools.Ask, scope string) {
	if ask.Outside != tools.OutsideNone {
		switch scope {
		case ScopeOutsideDir:
			b.rememberOutside(sessionID, ask.Outside, ask.Dir)
			return
		case ScopeOutsideAll:
			b.allowOutside(sessionID, ask.Outside)
			return
		}
	}
	if scope == ScopeOnce {
		return
	}
	b.grant(sessionID, toolName, rule.Match)
	if scope == ScopeAlways {
		if err := b.persist(toolName, rule); err != nil {
			// The approval still stands for this session; only the
			// permanence failed, which is worth logging but not worth
			// refusing the tool call the user just approved.
			log.Printf("permission: could not persist %q rule %q: %v", toolName, rule.Match, err)
		}
	}
}

// outsideFields adds what a boundary question needs to explain itself,
// and nothing at all to an ordinary one, so a client can tell the two
// apart by whether "outside" is there.
func outsideFields(ask tools.Ask, data map[string]any) map[string]any {
	if ask.Outside == tools.OutsideNone {
		return data
	}
	data["outside"] = ask.Outside.String()
	data["outside_dir"] = ask.Dir
	data["workspace"] = ask.Workspace
	return data
}

// audience is where one permission request has to be written: the
// session that raised it, plus any session a person is actually looking
// at.
type audience struct {
	origin  string
	mirrors []string
}

func (a audience) all() []string { return append([]string{a.origin}, a.mirrors...) }

// maxParentWalk bounds the climb from a task to the conversation that
// started it. Task delegation is capped at three levels, so this is
// already generous; it exists so a corrupted parent chain cannot loop
// forever inside a tool call.
const maxParentWalk = 8

// audience finds the user-facing session above sessionID, if it is not
// one itself.
//
// A background task is a session with no client attached to it: the
// parent's log gets task.spawned and task.status, and nothing else. A
// permission request raised inside one was therefore written somewhere
// nobody reads, and the tool call blocked on an answer that could not
// arrive — indefinitely, since a request has no timeout. The task held
// one of the TaskManager's concurrency slots the whole time, and the only
// visible symptom was "1 background task" that never finished.
//
// The ids the broker hands out are process-global, so a mirrored request
// is answerable from wherever it is shown, with no routing: the resolve
// endpoint looks the id up in one table.
func (b *PermissionBroker) audience(sessionID string) audience {
	a := audience{origin: sessionID}
	id := sessionID
	for range maxParentWalk {
		sess, err := b.store.Get(id)
		if err != nil || sess.ParentID == "" {
			break
		}
		id = sess.ParentID
		if id == sessionID {
			break // a cycle; the origin already has it
		}
		// Only sessions someone can open. An intermediate task in a chain
		// of delegations is as unwatched as the one that asked, so writing
		// the request there would add log noise and no answer.
		if parent, err := b.store.Get(id); err == nil && parent.Visible {
			a.mirrors = append(a.mirrors, id)
		}
	}
	return a
}

// grantKey namespaces a remembered approval by tool, so allowing the bash
// pattern "npm *" never also allows a write_file path that happens to
// glob the same way.
func grantKey(toolName, pattern string) string { return toolName + "\x00" + pattern }

func (b *PermissionBroker) grant(sessionID, toolName, pattern string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.granted[sessionID] == nil {
		b.granted[sessionID] = map[string]bool{}
	}
	b.granted[sessionID][grantKey(toolName, pattern)] = true
}

func (b *PermissionBroker) isGranted(sessionID, toolName, pattern string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.granted[sessionID][grantKey(toolName, pattern)]
}

// rememberOutside records a directory this session may leave the
// workspace for. Idempotent, and it drops a directory that is already
// covered by one remembered earlier, so approving a tree and then a leaf
// inside it does not grow the list with an entry that changes nothing.
func (b *PermissionBroker) rememberOutside(sessionID string, class tools.OutsideClass, dir string) {
	if dir == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.outside[sessionID] == nil {
		b.outside[sessionID] = map[tools.OutsideClass][]string{}
	}
	for _, had := range b.outside[sessionID][class] {
		if tools.UnderDir(had, dir) {
			return
		}
	}
	b.outside[sessionID][class] = append(b.outside[sessionID][class], dir)
}

// outsideGranted reports whether this session, or a conversation above
// it, has already approved the directory a call is reaching into.
//
// The walk up matches the switches (see PermissionPolicy): a background
// task works in its parent's project on work the parent authorized, so a
// directory the parent approved is one the task may use. Asking again
// would put the question in a log nobody is reading.
func (b *PermissionBroker) outsideGranted(sessionID string, class tools.OutsideClass, subject, dir string) bool {
	target := dir
	if target == "" {
		target = subject
	}
	if target == "" {
		return false
	}
	id := sessionID
	for range maxParentWalk {
		b.mu.Lock()
		dirs := append([]string(nil), b.outside[id][class]...)
		b.mu.Unlock()
		for _, had := range dirs {
			if tools.UnderDir(had, target) {
				return true
			}
		}
		sess, err := b.store.Get(id)
		if err != nil || sess.ParentID == "" || sess.ParentID == sessionID {
			return false
		}
		id = sess.ParentID
	}
	return false
}

// RememberedOutside lists the directories a session has approved, for a
// client that wants to show what it is currently trusting.
func (b *PermissionBroker) RememberedOutside(sessionID string, class tools.OutsideClass) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.outside[sessionID][class]...)
}

// ForgetOutside drops a session's remembered directories for one class —
// what "/read-outside mem-clear" does.
//
// Its own operation rather than a side effect of turning a switch off,
// because the two are different retractions: the switch is about the
// blanket answer, and this is about the individual places. Somebody who
// approved one directory by mistake needs a way to take that back
// without also changing a switch they never touched.
func (b *PermissionBroker) ForgetOutside(sessionID string, class tools.OutsideClass) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(b.outside[sessionID][class])
	if b.outside[sessionID] != nil {
		delete(b.outside[sessionID], class)
		if len(b.outside[sessionID]) == 0 {
			delete(b.outside, sessionID)
		}
	}
	return n
}

// allowOutside is the "yes, anywhere" answer: it turns this session's own
// switch on, the same one the Permissions window and /read-outside set.
//
// Through the session rather than into a private map, so the answer is
// visible afterwards. An approval that only the broker knew about would
// be a setting with no off switch and nowhere to see it.
func (b *PermissionBroker) allowOutside(sessionID string, class tools.OutsideClass) {
	b.mu.Lock()
	policy, onChanged := b.policy, b.onChanged
	b.mu.Unlock()
	sw, ok := OutsideSwitch(class)
	if !ok || policy == nil {
		return
	}
	yes := true
	if err := policy.Set(sessionID, sw, &yes); err != nil {
		log.Printf("permission: could not set %s for session %s: %v", sw, sessionID, err)
		return
	}
	if onChanged != nil {
		onChanged(sessionID)
	}
}

func (b *PermissionBroker) persist(toolName string, rule config.PermissionRule) error {
	if b.ConfigPath == "" {
		return fmt.Errorf("no config path configured for permanent permissions")
	}
	return config.AddPermissionRuleToFile(b.ConfigPath, toolName, rule)
}

// ForgetSession drops a session's remembered approvals, so a deleted
// session doesn't keep granting permissions to an id that could later be
// reused, and so the map doesn't grow forever.
func (b *PermissionBroker) ForgetSession(sessionID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.granted, sessionID)
	delete(b.outside, sessionID)
}

// Resolve answers a pending permission request. It is a no-op if id is
// unknown (already resolved or timed out). scope is one of ScopeOnce,
// ScopeSession, or ScopeAlways; anything else is treated as ScopeOnce, so
// an older client that only sends {"allow":true} keeps working unchanged.
// Resolve answers a pending request and reports whether there was one.
//
// The answer matters to the caller: an id that is unknown or already
// answered used to be reported as a success, so a client resolving a
// stale prompt — a replayed one, or a second client a moment late — was
// told it worked while nothing happened.
func (b *PermissionBroker) Resolve(id string, allow bool, scope string) bool {
	switch scope {
	case ScopeSession, ScopeAlways, ScopeOutsideDir, ScopeOutsideAll:
	default:
		scope = ScopeOnce
	}

	b.mu.Lock()
	ch, ok := b.pending[id]
	if ok {
		delete(b.pending, id)
	}
	b.mu.Unlock()
	if ok {
		ch <- resolution{allow: allow, scope: scope}
	}
	return ok
}
