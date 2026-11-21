package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"localcode/internal/cdp"
)

// Tab is one page in the browser: the one a conversation works in.
type Tab struct {
	conn     *cdp.Conn
	targetID string
	session  string

	mu      sync.Mutex
	dialogs []string // JavaScript dialogs answered since the model last looked
	viewers map[chan []byte]bool
	casting bool
	frame   []byte // the latest screencast frame, JPEG
	opener  string // the target that opened this tab with a link, or ""

	castMu sync.Mutex // orders screencast starts and stops; see Watch

	opens atomic.Int64 // see opening
}

// blankPage is the address every tab this manager opens starts at. A
// page a conversation's page asks the browser for is known by its
// address (see Manager.ask), and this one is never asked for.
const blankPage = "about:blank"

// openTab creates a page and attaches to it.
//
// In a window of its own. A browser with no window still paints only the
// tab in front of each window, and the screencast the panel shows is
// built from those paints: two conversations' tabs in one window left the
// one behind with no frames at all.
func openTab(ctx context.Context, conn *cdp.Conn) (*Tab, error) {
	// The browser makes the page whether or not the caller still waits
	// for it. Asked for apart from the caller, it is closed once it is
	// there when the caller stopped waiting, rather than left open with
	// nobody told its id.
	type made struct {
		id  string
		err error
	}
	answer := make(chan made, 1)
	go func() {
		var created struct {
			TargetID string `json:"targetId"`
		}
		askCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := conn.Call(askCtx, "", "Target.createTarget", map[string]any{"url": blankPage, "newWindow": true}, &created)
		answer <- made{created.TargetID, err}
	}()
	var page made
	select {
	case page = <-answer:
	case <-ctx.Done():
		go func() {
			if page := <-answer; page.err == nil {
				closeTarget(conn, page.id)
			}
		}()
		return nil, ctx.Err()
	}
	if page.err != nil {
		return nil, page.err
	}
	t, err := attachTab(ctx, conn, page.id)
	if err != nil {
		// The page is there whether or not it could be attached: closed,
		// or it stays open in the browser and in no list.
		closeTarget(conn, page.id)
		return nil, err
	}
	return t, nil
}

// closeTarget closes a page by its target id, with a deadline of its own:
// the caller's may be what ran out.
func closeTarget(conn *cdp.Conn, targetID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = conn.Call(ctx, "", "Target.closeTarget", map[string]any{"targetId": targetID}, nil)
}

// discard drops a tab nobody will list: its event handlers go and its page
// closes.
func (t *Tab) discard() {
	t.conn.Forget(t.session)
	closeTarget(t.conn, t.targetID)
}

// attachTab attaches to an existing page, such as one a link opened.
func attachTab(ctx context.Context, conn *cdp.Conn, targetID string) (*Tab, error) {
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := conn.Call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": targetID, "flatten": true}, &attached); err != nil {
		return nil, err
	}
	t := &Tab{conn: conn, targetID: targetID, session: attached.SessionID, viewers: map[chan []byte]bool{}}
	conn.On(t.session, "Page.javascriptDialogOpening", func(p json.RawMessage) {
		var d struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(p, &d)
		t.mu.Lock()
		t.dialogs = append(t.dialogs, fmt.Sprintf("%s: %s", d.Type, d.Message))
		t.mu.Unlock()
		// Answered at once: a dialog left open blocks the page, and with
		// it every command the model sends. Not from this goroutine, which
		// is the connection's reader.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = conn.Call(ctx, t.session, "Page.handleJavaScriptDialog", map[string]any{"accept": true}, nil)
		}()
	})
	conn.On(t.session, "Page.screencastFrame", func(p json.RawMessage) {
		var f struct {
			Data      string `json:"data"`
			SessionID int    `json:"sessionId"`
		}
		if json.Unmarshal(p, &f) != nil {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = conn.Call(ctx, t.session, "Page.screencastFrameAck", map[string]any{"sessionId": f.SessionID}, nil)
		}()
		jpeg, err := base64.StdEncoding.DecodeString(f.Data)
		if err != nil {
			return
		}
		t.mu.Lock()
		t.frame = jpeg
		for ch := range t.viewers {
			select {
			case ch <- jpeg:
			default: // a slow viewer skips frames rather than holding the page up
			}
		}
		t.mu.Unlock()
	})
	// From here a failure leaves a session with handlers on it: they go.
	for _, m := range []string{"Page.enable", "Runtime.enable"} {
		if err := conn.Call(ctx, t.session, m, nil, nil); err != nil {
			conn.Forget(t.session)
			return nil, err
		}
	}
	if err := conn.Call(ctx, t.session, "Emulation.setDeviceMetricsOverride", map[string]any{
		"width": ViewportWidth, "height": ViewportHeight, "deviceScaleFactor": 1, "mobile": false,
	}, nil); err != nil {
		conn.Forget(t.session)
		return nil, err
	}
	return t, nil
}

// whereIs asks a page where the browser put it: whether it is the tab
// in front of its window, which is the only one the browser draws, or
// behind another. The page says so from the moment it is made (measured:
// a page opened in front or in a window of its own says "visible" at
// once, one opened behind says "hidden").
const whereIs = `document.visibilityState`

// placed reads what a page said to whereIs: behind is that it is behind
// another tab, and known that it said one or the other.
func placed(said string) (behind, known bool) {
	return said == "hidden", said == "hidden" || said == "visible"
}

// where asks the page where it is now: see whereIs. A page that does not
// answer is not known to be anywhere.
func (t *Tab) where(ctx context.Context) (behind, known bool) {
	var said string
	// A page that is still loading can swap the document the question
	// was put to, and the question fails; it is put again.
	for range 3 {
		if err := t.eval(ctx, whereIs, &said); err == nil {
			return placed(said)
		} else if ctx.Err() != nil {
			break
		}
	}
	return false, false
}

// opening is when a click or a key that can open a link in a tab or a
// window of its own was last sent to the page: see clicked and pressed.
// The zero time if none was. See Manager.ask.
func (t *Tab) opening() time.Time {
	if at := t.opens.Load(); at != 0 {
		return time.Unix(0, at)
	}
	return time.Time{}
}

// opensElsewhere are the keys that, held, make a click or a key open a
// link in a tab or a window of its own rather than in its page.
const opensElsewhere = ModCtrl | ModMeta | ModShift

// clicked records a click that can open a link elsewhere, as it is sent:
// one with the wheel, or the left button with one of those keys held.
func (t *Tab) clicked(button string, mods int) {
	if button == "middle" || ((button == "left" || button == "") && mods&opensElsewhere != 0) {
		t.opens.Store(time.Now().UnixNano())
	}
}

// pressed records a key that can open a link elsewhere, as it is sent:
// Enter or Space, which are what a focused link or button takes for a
// click, with one of those keys held. Not any key with one held: Shift
// is held for every capital letter typed, and each would let whatever
// the page's script said in the seconds after pass for a click.
func (t *Tab) pressed(key string, mods int) {
	if (key == "Enter" || key == " ") && mods&opensElsewhere != 0 {
		t.opens.Store(time.Now().UnixNano())
	}
}

// TargetID is the page's id in the browser.
func (t *Tab) TargetID() string { return t.targetID }

// Settle gives an action a moment to take effect: a click or a key that
// starts a navigation has the new page loaded, up to 5s, before the model
// is told where it is.
func (t *Tab) Settle(ctx context.Context) {
	time.Sleep(300 * time.Millisecond)
	t.waitLoaded(ctx, 5*time.Second)
}

// TakeDialogs returns and clears the dialogs answered since the last call.
func (t *Tab) TakeDialogs() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	d := t.dialogs
	t.dialogs = nil
	return d
}

// Location is where the page is.
type Location struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

// Location reports the page's URL and title, from the browser rather
// than from a script in the page, which the page could break.
func (t *Tab) Location(ctx context.Context) (Location, error) {
	var res struct {
		TargetInfo struct {
			URL   string `json:"url"`
			Title string `json:"title"`
		} `json:"targetInfo"`
	}
	if err := t.conn.Call(ctx, "", "Target.getTargetInfo", map[string]any{"targetId": t.targetID}, &res); err != nil {
		return Location{}, err
	}
	return Location{URL: res.TargetInfo.URL, Title: res.TargetInfo.Title}, nil
}

// NormalizeURL is url as Navigate opens it: a bare host gets https://. It
// refuses every scheme but http and https, and about:blank: a file: URL
// would read the disk past the workspace boundary, and a javascript: one
// runs script in whatever page is open, denied or not.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "about:blank" {
		return raw, nil
	}
	if !strings.Contains(raw, "://") && !strings.HasPrefix(strings.ToLower(raw), "javascript:") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("not a URL: %v", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		if u.Host == "" {
			return "", fmt.Errorf("%q has no host", raw)
		}
		return u.String(), nil
	}
	return "", fmt.Errorf("only http and https pages can be opened, not %s:", u.Scheme)
}

// ErrBlocked is a load the guard failed: Options.Block said no to the
// page, or to one a redirect led to.
var ErrBlocked = errors.New("a rule blocks that page, and it was not loaded")

// Navigate loads target and waits for it to finish loading, up to 20s.
func (t *Tab) Navigate(ctx context.Context, target string) error {
	url, err := NormalizeURL(target)
	if err != nil {
		return err
	}
	var res struct {
		ErrorText string `json:"errorText"`
	}
	if err := t.conn.Call(ctx, t.session, "Page.navigate", map[string]any{"url": url}, &res); err != nil {
		return err
	}
	if res.ErrorText == "net::ERR_BLOCKED_BY_CLIENT" {
		// The only client here that blocks is the guard: the copied
		// profile carries no extensions.
		return ErrBlocked
	}
	if res.ErrorText != "" {
		return fmt.Errorf("could not load %s: %s", url, res.ErrorText)
	}
	t.waitLoaded(ctx, 20*time.Second)
	return nil
}

// waitLoaded waits until the document has finished loading, or for at most
// d. A page that never finishes (a long poll, a stream) is not an error:
// the model can look at it as it is.
func (t *Tab) waitLoaded(ctx context.Context, d time.Duration) {
	deadline := time.Now().Add(d)
	time.Sleep(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		// Each look bounded on its own, so a page that does not answer
		// is waited for until the deadline and no longer.
		var state string
		lctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := t.eval(lctx, `document.readyState`, &state)
		cancel()
		if err == nil && state == "complete" {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// History moves back (delta -1) or forward (delta 1).
func (t *Tab) History(ctx context.Context, delta int) error {
	var h struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID int `json:"id"`
		} `json:"entries"`
	}
	if err := t.conn.Call(ctx, t.session, "Page.getNavigationHistory", nil, &h); err != nil {
		return err
	}
	i := h.CurrentIndex + delta
	if i < 0 || i >= len(h.Entries) {
		if delta < 0 {
			return errors.New("there is no page to go back to")
		}
		return errors.New("there is no page to go forward to")
	}
	if err := t.conn.Call(ctx, t.session, "Page.navigateToHistoryEntry", map[string]any{"entryId": h.Entries[i].ID}, nil); err != nil {
		return err
	}
	t.waitLoaded(ctx, 20*time.Second)
	return nil
}

// Reload reloads the page.
func (t *Tab) Reload(ctx context.Context) error {
	if err := t.conn.Call(ctx, t.session, "Page.reload", nil, nil); err != nil {
		return err
	}
	t.waitLoaded(ctx, 20*time.Second)
	return nil
}

// Screenshot is the visible part of the page as PNG.
func (t *Tab) Screenshot(ctx context.Context) ([]byte, error) {
	shot, err := t.Capture(ctx, Shot{})
	return shot.PNG, err
}

// MaxShotHeight bounds a full-page capture. A page that scrolls for ever
// (a feed) would otherwise be an image no model and no disk wants.
const MaxShotHeight = 16384

// Shot is what Capture is asked for: the visible page (the zero value),
// the whole page (FullPage), or one element Read numbered (Ref).
type Shot struct {
	FullPage bool
	Ref      int
}

// Captured is a capture and its size in pixels. Cut is set when a full
// page was taller than MaxShotHeight and only its top was taken.
type Captured struct {
	PNG           []byte
	Width, Height int
	Cut           bool
}

// Capture takes a PNG of the page as s asks.
func (t *Tab) Capture(ctx context.Context, s Shot) (Captured, error) {
	params := map[string]any{"format": "png"}
	out := Captured{Width: ViewportWidth, Height: ViewportHeight}
	switch {
	case s.Ref > 0:
		var r *struct{ X, Y, W, H float64 }
		// The element's box in the page's own coordinates, less any part
		// above or left of the page, which is never drawn: a clip that
		// starts there comes back blank.
		script := `(() => {
  const el = document.querySelector('[data-lc-ref="` + strconv.Itoa(s.Ref) + `"]');
  if (!el) return null;
  el.scrollIntoView({block: 'center', inline: 'center'});
  const r = el.getBoundingClientRect();
  const x = Math.max(r.left + scrollX, 0), y = Math.max(r.top + scrollY, 0);
  return {X: x, Y: y, W: r.right + scrollX - x, H: r.bottom + scrollY - y};
})()`
		if err := t.eval(ctx, script, &r); err != nil {
			return out, err
		}
		if r == nil {
			return out, fmt.Errorf("element %d is not on the page any more; read the page again", s.Ref)
		}
		if r.W < 1 || r.H < 1 {
			return out, fmt.Errorf("element %d has no size to capture", s.Ref)
		}
		if r.H > MaxShotHeight {
			r.H, out.Cut = MaxShotHeight, true
		}
		params["clip"] = map[string]any{"x": r.X, "y": r.Y, "width": r.W, "height": r.H, "scale": 1}
		params["captureBeyondViewport"] = true
		out.Width, out.Height = int(r.W+0.5), int(r.H+0.5)
	case s.FullPage:
		var m struct {
			CSSContentSize struct {
				Width  float64 `json:"width"`
				Height float64 `json:"height"`
			} `json:"cssContentSize"`
		}
		if err := t.conn.Call(ctx, t.session, "Page.getLayoutMetrics", nil, &m); err != nil {
			return out, err
		}
		w, h := m.CSSContentSize.Width, m.CSSContentSize.Height
		if w < ViewportWidth {
			w = ViewportWidth
		}
		if h < 1 {
			h = ViewportHeight
		}
		if h > MaxShotHeight {
			h, out.Cut = MaxShotHeight, true
		}
		params["clip"] = map[string]any{"x": 0, "y": 0, "width": w, "height": h, "scale": 1}
		params["captureBeyondViewport"] = true
		out.Width, out.Height = int(w+0.5), int(h+0.5)
	}
	var res struct {
		Data string `json:"data"`
	}
	if err := t.conn.Call(ctx, t.session, "Page.captureScreenshot", params, &res); err != nil {
		return out, err
	}
	png, err := base64.StdEncoding.DecodeString(res.Data)
	out.PNG = png
	return out, err
}

// eval runs expression in the page and decodes its value into out.
func (t *Tab) eval(ctx context.Context, expression string, out any) error {
	return evalOn(ctx, t.conn, t.session, expression, out)
}

// evalOn runs expression in the page session is attached to, and decodes
// its value into out.
func evalOn(ctx context.Context, conn *cdp.Conn, session, expression string, out any) error {
	var res struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := conn.Call(ctx, session, "Runtime.evaluate", map[string]any{
		"expression": expression, "returnByValue": true, "awaitPromise": true,
	}, &res); err != nil {
		return err
	}
	if res.ExceptionDetails != nil {
		msg := res.ExceptionDetails.Exception.Description
		if msg == "" {
			msg = res.ExceptionDetails.Text
		}
		return errors.New(msg)
	}
	if out == nil || len(res.Result.Value) == 0 {
		return nil
	}
	return json.Unmarshal(res.Result.Value, out)
}

// Watch subscribes to the page's frames for the panel. The first frame is
// the latest one, so a viewer that joins late is not looking at nothing.
// stop ends the subscription; the screencast runs only while somebody is
// watching.
func (t *Tab) Watch(ctx context.Context) (frames <-chan []byte, stop func(), err error) {
	ch := make(chan []byte, 2)
	// Held from the decision to start to the browser's answer, as stop
	// holds it from the decision to stop: a start and a stop then reach
	// the browser in the order they were decided, and a stop from the
	// viewer that left never lands after the start for the one arriving.
	t.castMu.Lock()
	defer t.castMu.Unlock()
	t.mu.Lock()
	t.viewers[ch] = true
	first := t.frame
	needStart := !t.casting
	t.casting = true
	t.mu.Unlock()
	if first != nil {
		// Not blocking: a frame may already have filled the buffer.
		select {
		case ch <- first:
		default:
		}
	}
	if needStart {
		// "already active" is a screencast a viewer that just left has
		// not stopped yet: it is running, which is what was asked for.
		if err := t.conn.Call(ctx, t.session, "Page.startScreencast", map[string]any{
			"format": "jpeg", "quality": 70, "maxWidth": ViewportWidth, "maxHeight": ViewportHeight,
		}, nil); err != nil && !strings.Contains(err.Error(), "already active") {
			t.mu.Lock()
			delete(t.viewers, ch)
			t.casting = false
			t.mu.Unlock()
			return nil, nil, err
		}
	}
	var once sync.Once
	stop = func() {
		once.Do(func() {
			t.castMu.Lock()
			defer t.castMu.Unlock()
			t.mu.Lock()
			delete(t.viewers, ch)
			last := len(t.viewers) == 0 && t.casting
			if last {
				t.casting = false
			}
			t.mu.Unlock()
			if last {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = t.conn.Call(ctx, t.session, "Page.stopScreencast", nil, nil)
			}
		})
	}
	return ch, stop, nil
}
