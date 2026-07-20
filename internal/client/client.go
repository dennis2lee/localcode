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
	"mime/multipart"
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

// AgentInfo is one configured agent, as offered by the daemon's agent
// picker (GET /api/agents) — enough to build a Tab-cycle or dropdown
// without exposing that agent's system prompt or tool restrictions.
type AgentInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ListAgents returns every agent configured on the daemon, sorted by name.
func (c *Client) ListAgents(ctx context.Context) ([]AgentInfo, error) {
	var out []AgentInfo
	err := c.doJSON(ctx, http.MethodGet, "/api/agents", nil, &out)
	return out, err
}

// SwitchAgent changes which agent sessionID sends future messages as.
// Conversation history is untouched — only the model/system prompt/tool
// scope used for the next message changes. This is what backs Tab-cycling
// between e.g. "plan" and "build" mid-conversation.
func (c *Client) SwitchAgent(ctx context.Context, sessionID, agentName string) (session.Session, error) {
	var sess session.Session
	err := c.doJSON(ctx, http.MethodPost, "/api/sessions/"+sessionID+"/agent", map[string]string{"agent": agentName}, &sess)
	return sess, err
}

// RenameSession sets sessionID's cosmetic Title (session picker display
// only — resolution/resumption is always by ID).
func (c *Client) RenameSession(ctx context.Context, sessionID, title string) (session.Session, error) {
	var sess session.Session
	err := c.doJSON(ctx, http.MethodPost, "/api/sessions/"+sessionID+"/rename", map[string]string{"title": title}, &sess)
	return sess, err
}

// DeleteSession removes sessionID (and its persisted log, if any)
// entirely. Fails with a conflict error if the session has a turn in
// progress.
func (c *Client) DeleteSession(ctx context.Context, sessionID string) error {
	return c.doJSON(ctx, http.MethodDelete, "/api/sessions/"+sessionID, nil, nil)
}

// DeleteAllSessions removes every session on the daemon — visible sessions
// and background-task children alike. Fails with a conflict error if any
// session has a turn in progress (nothing is deleted in that case).
func (c *Client) DeleteAllSessions(ctx context.Context) error {
	return c.doJSON(ctx, http.MethodDelete, "/api/sessions", nil, nil)
}

// CommandInfo is one loaded custom slash command, as offered by the
// daemon's GET /api/commands — for a /help listing or autocomplete.
// Running the command still goes through SendMessage like any other text.
type CommandInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ListCommands returns every custom command loaded on the daemon, sorted
// by name.
func (c *Client) ListCommands(ctx context.Context) ([]CommandInfo, error) {
	var out []CommandInfo
	err := c.doJSON(ctx, http.MethodGet, "/api/commands", nil, &out)
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

// Settings is the daemon's current live "/config" settings.
type Settings struct {
	AutoCompactEnabled bool `json:"auto_compact_enabled"`
	ShowTPS            bool `json:"show_tps"`
}

// GetSettings fetches the daemon's current process-global settings — for
// a client that just opened to know the current state without waiting for
// a config.changed event.
func (c *Client) GetSettings(ctx context.Context) (Settings, error) {
	var out Settings
	err := c.doJSON(ctx, http.MethodGet, "/api/settings", nil, &out)
	return out, err
}

// ListMCPServers returns the names of every MCP server currently
// connected to the daemon (empty if none are configured).
func (c *Client) ListMCPServers(ctx context.Context) ([]string, error) {
	var out []string
	err := c.doJSON(ctx, http.MethodGet, "/api/mcp-servers", nil, &out)
	return out, err
}

// UploadFile uploads a file's contents to sessionID (drag-and-drop
// attachments), returning its absolute path on the daemon's machine — the
// caller then splices a reference to that path into the next chat
// message so the model can read it with its own file tools.
func (c *Client) UploadFile(ctx context.Context, sessionID, filename string, data io.Reader) (string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return "", fmt.Errorf("build multipart form: %w", err)
	}
	if _, err := io.Copy(part, data); err != nil {
		return "", fmt.Errorf("copy file data: %w", err)
	}
	if err := mw.Close(); err != nil {
		return "", fmt.Errorf("close multipart form: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/sessions/"+sessionID+"/uploads", &buf)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var apiErr struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&apiErr)
		return "", fmt.Errorf("upload %s: %d: %s", sessionID, resp.StatusCode, apiErr.Error)
	}

	var out struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	return out.Path, nil
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
