package browser

import (
	"context"
	"encoding/json"
	"strings"
	"time"
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

// autoAttach attaches every new target the browser opens, paused until
// the guard has set it up, except the browser's own: its interface pages
// ("browser_ui", such as the address bar's suggestion popup each new window
// makes) load no web content, so there is nothing to guard and no reason
// to hold them. The rest of the filter is the protocol's default.
var autoAttach = map[string]any{
	"autoAttach": true, "waitForDebuggerOnStart": true, "flatten": true,
	"filter": []map[string]any{
		{"type": "browser_ui", "exclude": true},
		{"type": "browser", "exclude": true},
		{"type": "tab", "exclude": true},
		{},
	},
}

// guard starts watching every target p's browser opens.
func (m *Manager) guard(p *process) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var version struct {
		UserAgent string `json:"userAgent"`
	}
	_ = p.conn.Call(ctx, "", "Browser.getVersion", nil, &version)
	p.userAgent = markedUserAgent(version.UserAgent)

	p.conn.On("", "Target.attachedToTarget", func(params json.RawMessage) { m.attached(p, params, "") })
	p.conn.On("", "Target.detachedFromTarget", func(params json.RawMessage) { forgetDetached(p, params) })
	_ = p.conn.Call(ctx, "", "Target.setAutoAttach", autoAttach, nil)
}

// attached is the browser saying a session was attached to a target, and
// sets the target up through it: see guardTarget. Called in the
// connection's read loop.
//
// A page is set up through one session: the first attached to it, which
// is the one the browser attached itself before the page ran, and which
// lasts as long as the page. A conversation's tab is attached a second
// time, for the tab's own work (attachTab), and that session was set up
// too. So every request of the page was held and let go twice; each
// frame in it from another site was attached twice, the second time
// through the tab's session, and when the tab closed that session's
// handlers went before the browser said those frames' sessions had gone,
// so theirs stayed for good (measured: four handlers for each such
// frame, on every tab closed); and a link the page asked the browser to
// open was told twice (see Manager.ask).
//
// Only a page: a worker can be attached through more than one session
// that each end at a time of their own, and is set up through each.
func (m *Manager) attached(p *process, params json.RawMessage, within string) {
	var ev struct {
		SessionID  string `json:"sessionId"`
		TargetInfo struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
		} `json:"targetInfo"`
	}
	if json.Unmarshal(params, &ev) != nil || ev.SessionID == "" {
		return
	}
	if ev.TargetInfo.Type == "page" {
		p.guardMu.Lock()
		_, already := p.guarded[ev.TargetInfo.TargetID]
		if !already {
			if p.guarded == nil {
				p.guarded = map[string]string{}
			}
			p.guarded[ev.TargetInfo.TargetID] = ev.SessionID
		}
		p.guardMu.Unlock()
		if already {
			return
		}
	}
	go m.guardTarget(p, params, within)
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
//
// within is the page the target was attached through, "" for one the
// browser attached itself. A frame from another site is a target of its
// own, and what it asks the browser for, the page it is in asks for (see
// Manager.ask).
func (m *Manager) guardTarget(p *process, params json.RawMessage, within string) {
	var ev struct {
		SessionID  string `json:"sessionId"`
		TargetInfo struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
		} `json:"targetInfo"`
		WaitingForDebugger bool `json:"waitingForDebugger"`
	}
	if json.Unmarshal(params, &ev) != nil || ev.SessionID == "" {
		return
	}
	page := within
	if ev.TargetInfo.Type == "page" {
		page = ev.TargetInfo.TargetID
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
		// A page's requests skip its service worker. A worker can answer
		// a navigation from a request the browser makes for it (navigation
		// preload) or from its cache, and neither is a request the guard
		// sees; without the worker, each is the page's own.
		if ev.TargetInfo.Type == "page" || ev.TargetInfo.Type == "iframe" {
			pending = append(pending,
				conn.Go(ctx, sess, "Network.enable", map[string]any{"maxTotalBufferSize": 0, "maxResourceBufferSize": 0}),
				conn.Go(ctx, sess, "Network.setBypassServiceWorker", map[string]any{"bypass": true}))
			// A link opened in a tab or a window of its own is told here,
			// before the browser makes that page: on the session of the
			// frame the link is in, which for a frame from another site is
			// this one and not its page's.
			if page != "" {
				conn.On(sess, "Page.frameRequestedNavigation", func(pp json.RawMessage) { m.ask(p, page, pp) })
				// With Page on, a dialog the page opens is told to this
				// session, and the page waits for its answer. A tab
				// answers the dialogs of its page (attachTab), but a page
				// can open one before a tab is attached to it, on load,
				// and then nothing answered: the page stood still, its
				// tab could not be attached, and it was closed (so it
				// was before Page was on here, too: nothing was told of
				// that dialog at all). Answered here as a tab answers.
				conn.On(sess, "Page.javascriptDialogOpening", func(json.RawMessage) {
					go func() {
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						_ = conn.Call(ctx, sess, "Page.handleJavaScriptDialog", map[string]any{"accept": true}, nil)
					}()
				})
				pending = append(pending, conn.Go(ctx, sess, "Page.enable", nil))
			}
		}
		// A frame from another site runs in a process of its own, as a
		// target of its own, and is only attached if asked for; so is a
		// worker a page starts.
		conn.On(sess, "Target.attachedToTarget", func(pp json.RawMessage) { m.attached(p, pp, page) })
		conn.On(sess, "Target.detachedFromTarget", func(pp json.RawMessage) { forgetDetached(p, pp) })
		pending = append(pending, conn.Go(ctx, sess, "Target.setAutoAttach", autoAttach))
	}
	if ev.WaitingForDebugger {
		pending = append(pending, conn.Go(ctx, sess, "Runtime.runIfWaitingForDebugger", nil))
		if ev.TargetInfo.Type == "page" {
			// A page just made is asked, as it starts, where the browser
			// put it: see Manager.born.
			target := ev.TargetInfo.TargetID
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				var said string
				if evalOn(ctx, conn, sess, whereIs, &said) == nil {
					m.bornAt(p, target, said)
				}
			}()
		}
	}
	for _, ch := range pending {
		<-ch
	}
}

// forgetDetached drops the handlers of a session whose target is gone: a
// frame removed, a worker ended, a tab closed. Each target the guard
// attaches registers its own, and a long-lived browser attaches many.
func forgetDetached(p *process, params json.RawMessage) {
	var ev struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(params, &ev) != nil || ev.SessionID == "" {
		return
	}
	p.conn.Forget(ev.SessionID)
	p.guardMu.Lock()
	defer p.guardMu.Unlock()
	for page, sess := range p.guarded {
		if sess == ev.SessionID {
			delete(p.guarded, page)
		}
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
