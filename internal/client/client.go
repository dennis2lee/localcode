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
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

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
		msg := fmt.Sprintf("%s %s: %d", method, path, resp.StatusCode)
		if apiErr.Error != "" {
			msg += ": " + apiErr.Error
		}
		return &StatusError{Status: resp.StatusCode, Message: msg}
	}

	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// StatusError is a non-2xx API response, keeping the HTTP status
// inspectable so callers can branch on it (409 busy, say) without
// matching substrings of the message.
type StatusError struct {
	Status  int
	Message string
}

func (e *StatusError) Error() string { return e.Message }

// IsBusy reports whether err is the daemon refusing a message because the
// session already has a turn in flight — the one error a client should
// queue-and-retry rather than surface.
func IsBusy(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Status == http.StatusConflict
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
// without exposing that agent's system prompt or tool restrictions. Model
// is the underlying model ID its profile resolves to (e.g.
// "us.anthropic.claude-sonnet-4-6"), for display next to the agent name.
type AgentInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Model       string `json:"model,omitempty"`
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

// SkillInfo is one installed skill, as offered by the daemon's
// GET /api/skills, for a listing or for completing "/<skill name>".
// Invoking a skill still goes through SendMessage like any other text.
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ListSkills returns every skill installed on the daemon, sorted by name.
func (c *Client) ListSkills(ctx context.Context) ([]SkillInfo, error) {
	var out []SkillInfo
	err := c.doJSON(ctx, http.MethodGet, "/api/skills", nil, &out)
	return out, err
}

// SlashCommandInfo is one command the daemon answers itself, from
// GET /api/slash-commands. Clients use it to complete "/<name>" without
// keeping their own copy of the list.
type SlashCommandInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ListSlashCommands returns the daemon's own slash commands.
func (c *Client) ListSlashCommands(ctx context.Context) ([]SlashCommandInfo, error) {
	var out []SlashCommandInfo
	err := c.doJSON(ctx, http.MethodGet, "/api/slash-commands", nil, &out)
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
//
// The endpoint returns a state object per server, not a bare name, so
// decoding straight into []string could never succeed — nothing called
// this, which is the only reason it went unnoticed.
func (c *Client) ListMCPServers(ctx context.Context) ([]string, error) {
	var states []struct {
		Name string `json:"name"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/mcp-servers", nil, &states); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(states))
	for _, s := range states {
		names = append(names, s.Name)
	}
	return names, nil
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

// ResolvePermission answers a pending permission request. scope is one of
// "" or "once" (this call only), "session" (don't ask again this
// session), or "always" (don't ask again ever — writes a rule to
// config.json server-side).
func (c *Client) ResolvePermission(ctx context.Context, sessionID, permID string, allow bool, scope string) error {
	path := fmt.Sprintf("/api/sessions/%s/permissions/%s", sessionID, permID)
	return c.doJSON(ctx, http.MethodPost, path, map[string]any{"allow": allow, "scope": scope}, nil)
}

// CancelTurn stops the turn currently running for a session. Cancelling
// an idle session is a no-op, not an error.
func (c *Client) CancelTurn(ctx context.Context, sessionID string) error {
	path := fmt.Sprintf("/api/sessions/%s/cancel", sessionID)
	return c.doJSON(ctx, http.MethodPost, path, map[string]string{}, nil)
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

// TaskOutput returns everything taskID's model has produced so far —
// readable mid-run, since a task is just a session with an event log.
func (c *Client) TaskOutput(ctx context.Context, taskID string) (string, error) {
	var out struct {
		Output string `json:"output"`
	}
	err := c.doJSON(ctx, http.MethodGet, "/api/tasks/"+taskID+"/output", nil, &out)
	return out.Output, err
}

func (c *Client) CancelTask(ctx context.Context, taskID string) error {
	return c.doJSON(ctx, http.MethodPost, "/api/tasks/"+taskID+"/cancel", map[string]string{}, nil)
}

// StreamEvents follows a session's event stream for as long as ctx lives,
// reconnecting whenever the connection ends and resuming from the last
// seq it saw, so no event is missed and none is delivered twice.
//
// Reconnecting is not a nicety. The daemon deliberately ends a stream
// that has fallen behind — see session.Store.Subscribe — because the
// alternative is silently skipping events, and every event is a piece of
// the model's reply. A client that treats the end of the stream as the
// end of the conversation shows a reply that stops halfway and a turn
// that never finishes.
func (c *Client) StreamEvents(ctx context.Context, sessionID string, since uint64) <-chan events.Event {
	out := make(chan events.Event, 256)
	go func() {
		defer close(out)
		last := since
		for {
			ch, err := c.SubscribeEvents(ctx, sessionID, last)
			if err == nil {
				for ev := range ch {
					if ev.Seq > 0 {
						last = ev.Seq
					}
					select {
					case out <- ev:
					case <-ctx.Done():
						return
					}
				}
			}
			// Either the connection failed to open or it ended. Both are
			// retried: a daemon that is briefly down comes back, and a
			// stream the daemon dropped is exactly the case this exists
			// for. The pause keeps a hard-down daemon from becoming a
			// spin loop.
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}()
	return out
}

// SubscribeEvents opens one SSE connection to the session's event stream
// starting after `since`, and returns a channel of decoded events. The
// channel closes when the context is cancelled or the connection ends.
// Prefer StreamEvents, which reconnects and resumes; this is the single
// attempt underneath it.
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

		// A Reader, not a Scanner, because one event has no length limit
		// worth choosing.
		//
		// This was a Scanner capped at 1MB, and nothing truncates tool
		// output anywhere in internal/tools — so a single `cat` of a
		// large file put a longer-than-1MB line on the wire. Scan then
		// stopped and returned false, which is indistinguishable from a
		// clean EOF without checking Err(), which nothing did. The stream
		// closed, StreamEvents reconnected a second later from the seq
		// *before* the oversized event, the daemon replayed that same
		// event, and it failed again — once a second, forever. The TUI
		// stopped updating at that point and a restart did not clear it,
		// since resume starts at seq 0 and meets the same event on the
		// way back. The browser was unaffected, EventSource having no
		// such cap, so it presented as "the TUI hangs on big output".
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			// ReadString returns what it has along with the error, so a
			// final line with no trailing newline is still processed.
			if line != "" {
				line = strings.TrimRight(line, "\r\n")
				if data, ok := strings.CutPrefix(line, "data: "); ok {
					var ev events.Event
					if jsonErr := json.Unmarshal([]byte(data), &ev); jsonErr == nil {
						select {
						case out <- ev:
						case <-ctx.Done():
							return
						}
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()

	return out, nil
}
