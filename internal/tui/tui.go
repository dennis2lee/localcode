// Package tui implements a minimal Bubble Tea front-end that drives an
// agent.Loop in-process (MVP: single binary, no HTTP split yet — see
// internal/agent for the interface the daemon/HTTP split will later sit
// behind).
package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"localcode/internal/agent"
	"localcode/internal/events"
	"localcode/internal/session"
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
	loop      *agent.Loop
	store     *session.Store
	broker    *agent.PermissionBroker
	sessionID string
	agentName string

	viewport viewport.Model
	input    textinput.Model
	events   <-chan events.Event

	transcript strings.Builder
	pending    *pendingPermission
	waiting    bool
	errMsg     string
}

func New(loop *agent.Loop, store *session.Store, broker *agent.PermissionBroker, sessionID, agentName string, eventCh <-chan events.Event) Model {
	ti := textinput.New()
	ti.Placeholder = "메시지를 입력하세요 (Enter로 전송, Ctrl+C로 종료)"
	ti.Focus()

	vp := viewport.New(80, 20)

	return Model{
		loop:      loop,
		store:     store,
		broker:    broker,
		sessionID: sessionID,
		agentName: agentName,
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
		err := m.loop.SendMessage(context.Background(), m.sessionID, m.agentName, text)
		return turnDoneMsg{err: err}
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
				m.broker.Resolve(m.pending.id, allow)
				m.pending = nil
				return m, nil
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
			m.transcript.WriteString(userStyle.Render("You: ") + text + "\n\n")
			m.viewport.SetContent(m.transcript.String())
			m.viewport.GotoBottom()
			m.waiting = true
			return m, m.sendMessage(text)
		}

	case eventMsg:
		m.applyEvent(events.Event(msg))
		return m, listenForEvent(m.events)

	case turnDoneMsg:
		m.waiting = false
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
	case events.TypeMessagePartDelta:
		if text, ok := ev.Data["text"].(string); ok {
			m.transcript.WriteString(text)
		}
	case events.TypeMessagePartEnd:
		m.transcript.WriteString("\n\n")
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
	case events.TypeError:
		if msg, ok := ev.Data["error"].(string); ok {
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
