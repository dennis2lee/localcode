package browser

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"localcode/internal/cdp"
)

// UserAgentMark is in the User-Agent of every request the model's browser
// makes. The daemon refuses any request that carries it: a page the model
// opens, or the model itself, must not be able to drive localcode's own
// API (turn the permission prompts off, open another conversation's tab)
// on whatever port a daemon listens on. A page cannot take it off: a
// User-Agent set by the browser's own emulation is not one a script can
// change for its requests.
const UserAgentMark = "LocalcodeBrowser"

// The guard: every target the browser opens (a page, a frame in a process
// of its own, a worker) is attached before it runs a line of script, given
// the marked user agent, and has every request it makes checked against
// Options.Block before it is sent. A denied site is then never loaded,
// whatever led there: a navigate, a redirect, a click, a frame, back and
// forward, a tab a link opened, the panel's own address bar.

var autoAttach = map[string]any{"autoAttach": true, "waitForDebuggerOnStart": true, "flatten": true}

// guard starts watching every target p's browser opens.
func (m *Manager) guard(p *process) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var version struct {
		UserAgent string `json:"userAgent"`
	}
	_ = p.conn.Call(ctx, "", "Browser.getVersion", nil, &version)
	p.userAgent = markedUserAgent(version.UserAgent)

	p.conn.On("", "Target.attachedToTarget", func(params json.RawMessage) { go m.guardTarget(p, params) })
	p.conn.On("", "Target.detachedFromTarget", func(params json.RawMessage) { forgetDetached(p.conn, params) })
	_ = p.conn.Call(ctx, "", "Target.setAutoAttach", autoAttach, nil)
}

// markedUserAgent is the browser's user agent as an ordinary browser
// would send it, with the mark appended. "HeadlessChrome" goes: a site
// that sees it often refuses the page, and the browser is not headless to
// anybody watching the panel.
func markedUserAgent(ua string) string {
	ua = strings.Replace(ua, "HeadlessChrome/", "Chrome/", 1)
	if ua == "" {
		ua = "Mozilla/5.0"
	}
	return ua + " " + UserAgentMark
}

// guardTarget sets up one attached target and lets it run.
//
// The setup goes out first and the command that lets the target run right
// behind it, before any answer is waited for: a target paused before its
// first script answers some of its setup only once it runs (a tab a link
// opened does), so waiting for each in turn waited for ever. Commands on
// one session are carried out in the order they arrive, so the target
// still runs with its user agent and its guard in place.
func (m *Manager) guardTarget(p *process, params json.RawMessage) {
	var ev struct {
		SessionID  string `json:"sessionId"`
		TargetInfo struct {
			Type string `json:"type"`
		} `json:"targetInfo"`
		WaitingForDebugger bool `json:"waitingForDebugger"`
	}
	if json.Unmarshal(params, &ev) != nil || ev.SessionID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, sess := p.conn, ev.SessionID
	var pending []<-chan error
	switch ev.TargetInfo.Type {
	case "page", "iframe", "worker", "shared_worker", "service_worker":
		pending = append(pending, conn.Go(ctx, sess, "Network.setUserAgentOverride", map[string]any{"userAgent": p.userAgent}))
		// Every request, not only pages: an allowed page's image, script
		// or fetch pointed at a denied site would reach it with the
		// profile's cookies as surely as a page load.
		conn.On(sess, "Fetch.requestPaused", func(pp json.RawMessage) { go m.decide(p, sess, pp) })
		pending = append(pending, conn.Go(ctx, sess, "Fetch.enable", map[string]any{
			"patterns": []map[string]any{{"urlPattern": "*", "requestStage": "Request"}},
		}))
		// A frame from another site runs in a process of its own, as a
		// target of its own, and is only attached if asked for; so is a
		// worker a page starts.
		conn.On(sess, "Target.attachedToTarget", func(pp json.RawMessage) { go m.guardTarget(p, pp) })
		conn.On(sess, "Target.detachedFromTarget", func(pp json.RawMessage) { forgetDetached(conn, pp) })
		pending = append(pending, conn.Go(ctx, sess, "Target.setAutoAttach", autoAttach))
	}
	if ev.WaitingForDebugger {
		pending = append(pending, conn.Go(ctx, sess, "Runtime.runIfWaitingForDebugger", nil))
	}
	for _, ch := range pending {
		<-ch
	}
}

// forgetDetached drops the handlers of a session whose target is gone: a
// frame removed, a worker ended, a tab closed. Each target the guard
// attaches registers its own, and a long-lived browser attaches many.
func forgetDetached(conn *cdp.Conn, params json.RawMessage) {
	var ev struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(params, &ev) == nil && ev.SessionID != "" {
		conn.Forget(ev.SessionID)
	}
}

// decide lets one paused request go, or fails it.
func (m *Manager) decide(p *process, sess string, params json.RawMessage) {
	var ev struct {
		RequestID string `json:"requestId"`
		Request   struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"request"`
	}
	if json.Unmarshal(params, &ev) != nil || ev.RequestID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if m.opts.Block != nil && m.opts.Block(ev.Request.URL) {
		_ = p.conn.Call(ctx, sess, "Fetch.failRequest", map[string]any{"requestId": ev.RequestID, "errorReason": "BlockedByClient"}, nil)
		return
	}
	cont := map[string]any{"requestId": ev.RequestID}
	// A request sent before its target was attached, such as the fetch of
	// a service worker's own script, went out with the browser's own user
	// agent: it gets the marked one here.
	if ua := userAgentOf(ev.Request.Headers); !strings.Contains(ua, UserAgentMark) && p.userAgent != "" {
		headers := []map[string]string{}
		for name, value := range ev.Request.Headers {
			if !strings.EqualFold(name, "User-Agent") {
				headers = append(headers, map[string]string{"name": name, "value": value})
			}
		}
		cont["headers"] = append(headers, map[string]string{"name": "User-Agent", "value": p.userAgent})
	}
	_ = p.conn.Call(ctx, sess, "Fetch.continueRequest", cont, nil)
}

func userAgentOf(headers map[string]string) string {
	for name, value := range headers {
		if strings.EqualFold(name, "User-Agent") {
			return value
		}
	}
	return ""
}
