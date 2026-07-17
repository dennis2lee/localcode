// Package client is the HTTP+SSE client the TUI (and, in principle, any
// other client) uses to talk to the core daemon. It holds no conversation
// state itself — the daemon is the source of truth; this just translates
// Go calls into HTTP requests and SSE event streams.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"localcode/internal/agent"
	"localcode/internal/events"
	"localcode/internal/session"
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func New(baseURL string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: http.DefaultClient}
}

func (c *Client) doJSON(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		reader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var apiErr struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&apiErr)
		if apiErr.Error != "" {
			return fmt.Errorf("%s %s: %d: %s", method, path, resp.StatusCode, apiErr.Error)
		}
		return fmt.Errorf("%s %s: %d", method, path, resp.StatusCode)
	}

	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *Client) CreateSession(ctx context.Context, agentName string) (session.Session, error) {
	var sess session.Session
	err := c.doJSON(ctx, http.MethodPost, "/api/sessions", map[string]string{"agent": agentName}, &sess)
	return sess, err
}

// ListSessions returns every top-level (visible) session, newest first,
// so a caller can offer the user a choice to resume one instead of
// always creating a new session.
func (c *Client) ListSessions(ctx context.Context) ([]session.Session, error) {
	var out []session.Session
	err := c.doJSON(ctx, http.MethodGet, "/api/sessions", nil, &out)
	return out, err
}

// Version returns the version string of the daemon this client is
// attached to — not necessarily the local binary's own version, since a
// TUI can be pointed at a remote daemon via --server.
func (c *Client) Version(ctx context.Context) (string, error) {
	var out struct {
		Version string `json:"version"`
	}
	err := c.doJSON(ctx, http.MethodGet, "/api/version", nil, &out)
	return out.Version, err
}

func (c *Client) SendMessage(ctx context.Context, sessionID, text string) error {
	return c.doJSON(ctx, http.MethodPost, "/api/sessions/"+sessionID+"/messages", map[string]string{"text": text}, nil)
}

func (c *Client) ResolvePermission(ctx context.Context, sessionID, permID string, allow bool) error {
	path := fmt.Sprintf("/api/sessions/%s/permissions/%s", sessionID, permID)
	return c.doJSON(ctx, http.MethodPost, path, map[string]bool{"allow": allow}, nil)
}

func (c *Client) SpawnTask(ctx context.Context, sessionID, agentName, prompt string) (string, error) {
	var out struct {
		TaskID string `json:"task_id"`
	}
	body := map[string]string{"agent": agentName, "prompt": prompt}
	err := c.doJSON(ctx, http.MethodPost, "/api/sessions/"+sessionID+"/tasks", body, &out)
	return out.TaskID, err
}

func (c *Client) ListTasks(ctx context.Context, sessionID string) ([]agent.SessionSummary, error) {
	var out []agent.SessionSummary
	err := c.doJSON(ctx, http.MethodGet, "/api/sessions/"+sessionID+"/tasks", nil, &out)
	return out, err
}

func (c *Client) CancelTask(ctx context.Context, taskID string) error {
	return c.doJSON(ctx, http.MethodPost, "/api/tasks/"+taskID+"/cancel", map[string]string{}, nil)
}

// SubscribeEvents opens an SSE connection to the session's event stream
// starting after `since`, and returns a channel of decoded events. The
// channel closes when the context is cancelled or the connection ends;
// there is no automatic reconnect (a caller wanting resume-on-drop should
// track the last seq it saw and call SubscribeEvents again with it).
func (c *Client) SubscribeEvents(ctx context.Context, sessionID string, since uint64) (<-chan events.Event, error) {
	url := fmt.Sprintf("%s/api/sessions/%s/events?since=%d", c.BaseURL, sessionID, since)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connect events stream: %w", err)
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, fmt.Errorf("events stream returned %d", resp.StatusCode)
	}

	out := make(chan events.Event, 64)
	go func() {
		defer close(out)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var ev events.Event
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
				continue
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()

	return out, nil
}
