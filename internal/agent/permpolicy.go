package agent

import (
	"context"

	"localcode/internal/config"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// Who answers the four permission switches for a given tool call.
//
// The switches are per session (see session.Permissions), so answering
// one means knowing which conversation is asking, and a conversation
// that has not been asked follows the daemon's default from config.json.
// Neither of those facts belongs in internal/tools, which knows about
// paths and deliberately nothing else, so this is the piece in between.
//
// A background task follows its parent. It runs in the parent's
// workspace, on work the parent authorized, and a task that started
// asking questions the conversation above it had already answered would
// be asking them somewhere nobody is looking. The walk is live rather
// than copied at spawn, because the direction that matters is turning a
// switch off: saying "stop skipping" has to reach the work already
// running, not just the next thing started.
type PermissionPolicy struct {
	store *session.Store
	cfg   *config.Config
}

func NewPermissionPolicy(store *session.Store, cfg *config.Config) *PermissionPolicy {
	return &PermissionPolicy{store: store, cfg: cfg}
}

// On answers one switch for the session this call belongs to.
func (p *PermissionPolicy) On(ctx context.Context, sw session.Switch) bool {
	id, _ := SessionIDFromContext(ctx)
	return p.OnFor(id, sw)
}

// OnFor is On for a session named directly.
func (p *PermissionPolicy) OnFor(sessionID string, sw session.Switch) bool {
	if v, _ := p.lookup(sessionID, sw); v != nil {
		return *v
	}
	return p.byDefault(sw)
}

// Source says where a session's effective answer comes from: its own
// setting, an ancestor's, or the daemon default. Clients show it so a
// checkbox that is on can say whether this conversation turned it on.
type Source string

const (
	SourceSession Source = "session"
	SourceParent  Source = "parent"
	SourceDefault Source = "default"
)

// Effective returns the answer and where it came from.
func (p *PermissionPolicy) Effective(sessionID string, sw session.Switch) (bool, Source) {
	v, own := p.lookup(sessionID, sw)
	if v == nil {
		return p.byDefault(sw), SourceDefault
	}
	if own {
		return *v, SourceSession
	}
	return *v, SourceParent
}

// lookup walks from sessionID up to the conversation that started it,
// returning the first answer anybody gave and whether it was this
// session's own.
func (p *PermissionPolicy) lookup(sessionID string, sw session.Switch) (v *bool, own bool) {
	if sessionID == "" || p.store == nil {
		return nil, false
	}
	id := sessionID
	for i := range maxParentWalk {
		sess, err := p.store.Get(id)
		if err != nil {
			return nil, false
		}
		if got := sess.Permissions.Get(sw); got != nil {
			return got, i == 0
		}
		if sess.ParentID == "" || sess.ParentID == sessionID {
			return nil, false
		}
		id = sess.ParentID
	}
	return nil, false
}

// byDefault is the daemon's answer, from config.json.
func (p *PermissionPolicy) byDefault(sw session.Switch) bool {
	if p.cfg == nil {
		return false
	}
	switch sw {
	case session.SwitchSkipAll:
		return p.cfg.PermissionsSkipped()
	case session.SwitchSkipTools:
		return p.cfg.ToolPermissionsSkipped()
	case session.SwitchReadOutside:
		return p.cfg.ReadOutsideAllowed()
	case session.SwitchWriteOutside:
		return p.cfg.WriteOutsideAllowed()
	}
	return false
}

// Set records one switch for one session, or clears it (v nil) so the
// daemon default applies again.
func (p *PermissionPolicy) Set(sessionID string, sw session.Switch, v *bool) error {
	_, err := p.store.SetPermission(sessionID, sw, v)
	return err
}

// OutsideSwitch is the switch that governs a boundary class, so a "yes,
// all of them" answer knows which one to turn on.
func OutsideSwitch(class tools.OutsideClass) (session.Switch, bool) {
	switch class {
	case tools.OutsideRead:
		return session.SwitchReadOutside, true
	case tools.OutsideWrite:
		return session.SwitchWriteOutside, true
	}
	return "", false
}

// ToolsPolicy is this policy in the shape internal/tools asks for.
func (p *PermissionPolicy) ToolsPolicy() tools.Policy {
	return tools.Policy{
		SkipAll:   func(ctx context.Context) bool { return p.On(ctx, session.SwitchSkipAll) },
		SkipTools: func(ctx context.Context) bool { return p.On(ctx, session.SwitchSkipTools) },
		OutsideAllowed: func(ctx context.Context, class tools.OutsideClass) bool {
			sw, ok := OutsideSwitch(class)
			return ok && p.On(ctx, sw)
		},
	}
}
