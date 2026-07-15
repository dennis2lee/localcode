// Package tui implements a Bubble Tea front-end that talks to the core
// daemon over HTTP + SSE via internal/client — it is a client like any
// other (a Web UI is the other one), holding no conversation state beyond
// what's needed to render the current screen.
package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"localcode/internal/client"
	"localcode/internal/events"
)

var (
	userStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	toolStyle   = lipgloss.NewStyle().Faint(true)
	errorStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
	modalStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
	statusStyle = lipgloss.NewStyle().Faint(true)
)

type pendingPermission struct {
	id, tool, description string
}

type Model struct {
	client    *client.Client
	sessionID string

	viewport viewport.Model
	input    textinput.Model
	events   <-chan events.Event

	transcript strings.Builder
	pending    *pendingPermission
	waiting    bool
	errMsg     string
}

func New(c *client.Client, sessionID string, eventCh <-chan events.Event) Model {
	ti := textinput.New()
	ti.Placeholder = "메시지를 입력하세요 (Enter로 전송, Ctrl+C로 종료)"
	ti.Focus()

	vp := viewport.New(80, 20)

	return Model{
		client:    c,
		sessionID: sessionID,
		viewport:  vp,
		input:     ti,
		events:    eventCh,
	}
}

func (m Model) Init() tea.Cmd {
	return listenForEvent(m.events)
}

type eventMsg events.Event
type turnDoneMsg struct{ err error }
type permissionResolvedMsg struct{ err error }

func listenForEvent(ch <-chan events.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return eventMsg(ev)
	}
}

func (m Model) sendMessage(text string) tea.Cmd {
	return func() tea.Msg {
		err := m.client.SendMessage(context.Background(), m.sessionID, text)
		return turnDoneMsg{err: err}
	}
}

func (m Model) resolvePermission(id string, allow bool) tea.Cmd {
	return func() tea.Msg {
		err := m.client.ResolvePermission(context.Background(), m.sessionID, id, allow)
		return permissionResolvedMsg{err: err}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.viewport.Width = msg.Width
		m.viewport.Height = msg.Height - 4
		m.input.Width = msg.Width - 2
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit

		case "y", "n":
			if m.pending != nil {
				allow := msg.String() == "y"
				id := m.pending.id
				m.pending = nil
				return m, m.resolvePermission(id, allow)
			}

		case "enter":
			if m.pending != nil {
				return m, nil // waiting for y/n, ignore stray enters
			}
			text := strings.TrimSpace(m.input.Value())
			if text == "" || m.waiting {
				return m, nil
			}
			m.input.SetValue("")
			// The user line itself renders from the message.user event (see
			// applyEvent), not optimistically here, so a resumed/replayed
			// session shows the same transcript a live one did.
			m.waiting = true
			return m, m.sendMessage(text)
		}

	case eventMsg:
		m.applyEvent(events.Event(msg))
		return m, listenForEvent(m.events)

	case turnDoneMsg:
		if msg.err != nil {
			m.waiting = false
			m.errMsg = msg.err.Error()
		}
		// On success we leave m.waiting as-is: the daemon accepted the
		// message and is streaming the actual turn via events; waiting
		// clears when a message.part.end / error event arrives.
		return m, nil

	case permissionResolvedMsg:
		if msg.err != nil {
			m.errMsg = msg.err.Error()
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) applyEvent(ev events.Event) {
	switch ev.Type {
	case events.TypeUserMessage:
		if text, ok := ev.Data["text"].(string); ok {
			m.transcript.WriteString(userStyle.Render("You: ") + text + "\n\n")
		}
	case events.TypeMessagePartDelta:
		if text, ok := ev.Data["text"].(string); ok {
			m.transcript.WriteString(text)
		}
	case events.TypeMessagePartEnd:
		m.transcript.WriteString("\n\n")
		m.waiting = false
	case events.TypeToolStart:
		name, _ := ev.Data["name"].(string)
		m.transcript.WriteString(toolStyle.Render(fmt.Sprintf("[tool] %s 실행 중...\n", name)))
	case events.TypeToolEnd:
		isErr, _ := ev.Data["is_error"].(bool)
		status := "완료"
		if isErr {
			status = "실패"
		}
		m.transcript.WriteString(toolStyle.Render(fmt.Sprintf("[tool] %s\n\n", status)))
	case events.TypePermissionRequest:
		id, _ := ev.Data["id"].(string)
		tool, _ := ev.Data["tool"].(string)
		desc, _ := ev.Data["description"].(string)
		m.pending = &pendingPermission{id: id, tool: tool, description: desc}
	case events.TypeTaskSpawned:
		taskID, _ := ev.Data["task_id"].(string)
		agentName, _ := ev.Data["agent"].(string)
		m.transcript.WriteString(toolStyle.Render(fmt.Sprintf("[task] %s (%s) 백그라운드 실행 시작\n", taskID, agentName)))
	case events.TypeTaskStatus:
		taskID, _ := ev.Data["task_id"].(string)
		status, _ := ev.Data["status"].(string)
		m.transcript.WriteString(toolStyle.Render(fmt.Sprintf("[task] %s: %s\n", taskID, status)))
	case events.TypeError:
		if msg, ok := ev.Data["error"].(string); ok {
			m.waiting = false
			m.errMsg = msg
		}
	}
	m.viewport.SetContent(m.transcript.String())
	m.viewport.GotoBottom()
}

func (m Model) View() string {
	var b strings.Builder
	b.WriteString(m.viewport.View())
	b.WriteString("\n")

	if m.pending != nil {
		b.WriteString(modalStyle.Render(fmt.Sprintf("권한 요청 [%s]: %s  (y/n)", m.pending.tool, m.pending.description)))
		b.WriteString("\n")
	} else if m.waiting {
		b.WriteString(statusStyle.Render("응답 대기 중..."))
		b.WriteString("\n")
	} else if m.errMsg != "" {
		b.WriteString(errorStyle.Render("오류: " + m.errMsg))
		b.WriteString("\n")
	}

	b.WriteString(m.input.View())
	return b.String()
}
