package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
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
}

// openTab creates a page and attaches to it.
//
// In a window of its own. A browser with no window still paints only the
// tab in front of each window, and the screencast the panel shows is
// built from those paints: two conversations' tabs in one window left the
// one behind with no frames at all.
func openTab(ctx context.Context, conn *cdp.Conn) (*Tab, error) {
	var created struct {
		TargetID string `json:"targetId"`
	}
	if err := conn.Call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank", "newWindow": true}, &created); err != nil {
		return nil, err
	}
	return attachTab(ctx, conn, created.TargetID)
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
	for _, m := range []string{"Page.enable", "Runtime.enable"} {
		if err := conn.Call(ctx, t.session, m, nil, nil); err != nil {
			return nil, err
		}
	}
	if err := conn.Call(ctx, t.session, "Emulation.setDeviceMetricsOverride", map[string]any{
		"width": ViewportWidth, "height": ViewportHeight, "deviceScaleFactor": 1, "mobile": false,
	}, nil); err != nil {
		return nil, err
	}
	return t, nil
}

// TargetID is the page's id in the browser.
func (t *Tab) TargetID() string { return t.targetID }

// close closes the page.
func (t *Tab) close(ctx context.Context) {
	_ = t.conn.Call(ctx, "", "Target.closeTarget", map[string]any{"targetId": t.targetID}, nil)
	t.conn.Forget(t.session)
}

// takeDialogs returns and clears the dialogs answered since the last call.
func (t *Tab) takeDialogs() []string {
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

// Location reports the page's URL and title.
func (t *Tab) Location(ctx context.Context) (Location, error) {
	var loc Location
	err := t.eval(ctx, `({url: location.href, title: document.title})`, &loc)
	return loc, err
}

// Navigate loads url and waits for it to finish loading, up to 20s.
func (t *Tab) Navigate(ctx context.Context, url string) error {
	if !strings.Contains(url, "://") && !strings.HasPrefix(url, "about:") && !strings.HasPrefix(url, "data:") {
		url = "https://" + url
	}
	var res struct {
		ErrorText string `json:"errorText"`
	}
	if err := t.conn.Call(ctx, t.session, "Page.navigate", map[string]any{"url": url}, &res); err != nil {
		return err
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
		var state string
		if t.eval(ctx, `document.readyState`, &state) == nil && state == "complete" {
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
	var res struct {
		Data string `json:"data"`
	}
	if err := t.conn.Call(ctx, t.session, "Page.captureScreenshot", map[string]any{"format": "png"}, &res); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(res.Data)
}

// eval runs expression in the page and decodes its value into out.
func (t *Tab) eval(ctx context.Context, expression string, out any) error {
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
	if err := t.conn.Call(ctx, t.session, "Runtime.evaluate", map[string]any{
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
	t.mu.Lock()
	t.viewers[ch] = true
	first := t.frame
	needStart := !t.casting
	t.casting = true
	t.mu.Unlock()
	if first != nil {
		ch <- first
	}
	if needStart {
		if err := t.conn.Call(ctx, t.session, "Page.startScreencast", map[string]any{
			"format": "jpeg", "quality": 70, "maxWidth": ViewportWidth, "maxHeight": ViewportHeight,
		}, nil); err != nil {
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
			t.mu.Lock()
			delete(t.viewers, ch)
			last := len(t.viewers) == 0
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
