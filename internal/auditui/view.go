// Package auditui is a read-only audit view. Opening, filtering or following it
// never changes desired state, installs tools or starts a VM/service.
package auditui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/audit"
)

type Batch struct {
	Events  []audit.Event
	Summary audit.Summary
	Status  audit.Status
}
type Fetch func(context.Context, audit.Query) (Batch, error)
type loaded struct {
	Batch      Batch
	Err        error
	Generation uint64
}
type tick struct{}

type Model struct {
	ctx                        context.Context
	space                      string
	fetch                      Fetch
	width, height              int
	events                     []audit.Event
	status                     audit.Status
	summary                    audit.Summary
	after                      uint64
	selected, offset           int
	paused, details, filtering bool
	filter, severity           string
	draft                      string
	fetching                   bool
	generation                 uint64
	err                        string
}

func New(ctx context.Context, space string, fetch Fetch) Model {
	return Model{ctx: ctx, space: audit.Clean(space), fetch: fetch, width: 80, height: 24, fetching: true}
}
func (m Model) Init() tea.Cmd { return m.load() }
func (m Model) load() tea.Cmd {
	generation, query := m.generation, audit.Query{After: m.after, Box: m.filter, Limit: 500, Tail: m.after == 0}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()
		batch, err := m.fetch(ctx, query)
		return loaded{batch, err, generation}
	}
}
func next() tea.Cmd { return tea.Tick(time.Second, func(time.Time) tea.Msg { return tick{} }) }
func (m Model) visible() []audit.Event {
	var result []audit.Event
	for _, e := range m.events {
		if m.severity == "" || strings.EqualFold(e.Severity, m.severity) {
			result = append(result, e)
		}
	}
	return result
}
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case loaded:
		m.fetching = false
		if msg.Generation != m.generation || m.paused {
			if !m.paused {
				m.fetching = true
				return m, m.load()
			}
			return m, nil
		}
		if msg.Err != nil {
			m.err = audit.Clean(msg.Err.Error())
			return m, next()
		}
		m.err = ""
		m.status, m.summary = msg.Batch.Status, msg.Batch.Summary
		m.after = msg.Batch.Summary.Last
		m.events = append(m.events, msg.Batch.Events...)
		if len(m.events) > 500 {
			m.events = m.events[len(m.events)-500:]
		}
		m.selected = max(0, len(m.visible())-1)
		return m, next()
	case tick:
		if !m.paused && !m.fetching {
			m.fetching = true
			return m, m.load()
		}
	case tea.KeyPressMsg:
		key := msg.String()
		if m.filtering {
			switch key {
			case "esc":
				m.filtering = false
			case "enter":
				m.filter = m.draft
				m.filtering = false
				m.events = nil
				m.after = 0
				m.generation++
				if m.fetching || m.paused {
					return m, nil
				}
				m.fetching = true
				return m, m.load()
			case "backspace":
				if len(m.draft) > 0 {
					r := []rune(m.draft)
					m.draft = string(r[:len(r)-1])
				}
			default:
				if len(key) == 1 && len(m.draft) < 63 && strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-", rune(key[0])) {
					m.draft += key
				}
			}
			return m, nil
		}
		switch key {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case " ", "space":
			m.paused = !m.paused
			m.generation++
			if !m.paused && !m.fetching {
				m.fetching = true
				return m, m.load()
			}
		case "enter":
			m.details = !m.details
		case "up", "k":
			if m.selected > 0 {
				m.selected--
			}
		case "down", "j":
			if m.selected+1 < len(m.visible()) {
				m.selected++
			}
		case "b":
			m.draft = m.filter
			m.filtering = true
		case "v":
			switch m.severity {
			case "":
				m.severity = "warning"
			case "warning":
				m.severity = "error"
			case "error":
				m.severity = "notice"
			default:
				m.severity = ""
			}
			m.selected, m.offset = 0, 0
		case "r":
			if m.paused || m.fetching {
				return m, nil
			}
			m.err = ""
			m.generation++
			m.fetching = true
			return m, m.load()
		}
	}
	return m, nil
}
func EventLine(e audit.Event) string {
	box := audit.Clean(e.Box)
	if !e.MetadataAvailable {
		box = "?"
	}
	return fmt.Sprintf("%s  %-9s  %-14s  %s", e.Time.UTC().Format("15:04:05"), audit.Clean(e.Severity), box, audit.Clean(e.Rule))
}
func (m Model) View() tea.View {
	width, height := max(1, m.width), max(1, m.height)
	mode := "FOLLOW"
	if m.paused {
		mode = "PAUSED"
	}
	driverDrops := "unknown"
	if m.status.DriverDrops != nil {
		driverDrops = fmt.Sprint(*m.status.DriverDrops)
	}
	lines := []string{"AUDIT  /  " + m.space + "  " + mode,
		fmt.Sprintf("desired=%t  service=%s  driver=%s  Falco=%s", m.status.Desired.Enabled, audit.Clean(m.status.State), audit.Clean(m.status.Driver), audit.Clean(m.status.Desired.Version)),
		fmt.Sprintf("collector drops=%d  driver drops=%s  metadata-gaps=%d  retention-gap=%t", m.summary.Drops, driverDrops, m.summary.MetadataGaps, m.summary.Gap),
		"Box: " + m.filter + "   Severity: " + m.severity}
	if m.filtering {
		lines[3] = "Box filter: " + m.draft + "_  Enter applies · Esc cancels"
	}
	if m.err != "" {
		lines = append(lines, "Unavailable: "+m.err)
	} else if m.status.Error != "" {
		lines = append(lines, audit.Clean(m.status.Error))
	}
	events := m.visible()
	if len(events) == 0 {
		lines = append(lines, "No matching retained events.")
		if !m.status.Desired.Enabled && m.status.State != "unsupported" {
			lines = append(lines, "Enable with yoloctl audit enable.")
		}
	}
	rows := max(0, height-len(lines)-2)
	selected := min(m.selected, max(0, len(events)-1))
	offset := max(0, min(m.offset, selected))
	if selected >= offset+rows {
		offset = max(0, selected-rows+1)
	}
	for i := offset; i < len(events) && i < offset+rows; i++ {
		prefix := "  "
		if i == selected {
			prefix = "> "
		}
		lines = append(lines, prefix+EventLine(events[i]))
	}
	if m.details && len(events) > 0 {
		e := events[selected]
		details := fmt.Sprintf("Executable: %s · Path: %s · Endpoint: %s · Result: %s", audit.Clean(e.Executable), audit.Clean(e.Path), audit.Clean(e.Endpoint), audit.Clean(e.Result))
		if len(lines) >= height-1 && len(lines) > 4 {
			lines = lines[:height-2]
		}
		lines = append(lines, details)
	}
	lines = append(lines, "↑↓ select · Enter details · Space pause/follow · b box · v severity · r retry · q close")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "…")
	}
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	return view
}
