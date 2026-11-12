// Package auditui is a read-only audit view. Opening, filtering or following it
// never changes desired state, installs tools or starts a VM/service.
package auditui

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/audit"
	"github.com/leogr/yoloctl/internal/logging/logtheme"
	"github.com/leogr/yoloctl/pkg/prettylog"
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
type rowsReady struct{ sink *rowSink }

type Model struct {
	ctx                        context.Context
	space                      string
	fetch                      Fetch
	width, height              int
	sink                       *rowSink
	stream                     *eventStream
	active                     *atomic.Pointer[eventStream]
	verbosity                  prettylog.Verbosity
	renderer                   *eventRenderer
	selectedOwner              uint64
	selectedKind               prettylog.EmissionKind
	status                     audit.Status
	summary                    audit.Summary
	after                      uint64
	loadedOnce                 bool
	color                      bool
	selected, offset           int
	paused, details, filtering bool
	filter, severity           string
	draft                      string
	fetching                   bool
	generation                 uint64
	err                        string
}

func New(ctx context.Context, space string, fetch Fetch) Model {
	m := Model{ctx: ctx, space: audit.Clean(space), fetch: fetch, width: 80, height: 24, fetching: true, verbosity: prettylog.Normal}
	m.active = new(atomic.Pointer[eventStream])
	m.renderer = newEventRenderer(EventOptions{})
	m.resetStream()
	return m
}

func (m Model) WithColor(enabled bool) Model {
	m.color = enabled
	m.renderer = newEventRenderer(EventOptions{Color: enabled})
	return m
}

// WithVerbosity must be applied before Init; health/refusal feedback remains
// visible independently of the event log filter.
func (m Model) WithVerbosity(v prettylog.Verbosity) Model {
	_ = m.stream.close()
	m.verbosity = v
	m.resetStream()
	return m
}
func (m *Model) resetStream() {
	m.sink = newRowSink(retainedRows)
	m.stream = newEventStream(m.ctx, m.verbosity, m.sink, m.sink.close)
	m.active.Store(m.stream)
	m.selectedOwner, m.selectedKind, m.selected, m.offset = 0, 0, 0, 0
}

// Close also works on the initial value retained by the program's caller:
// filter changes replace value-model streams, but share this lifetime slot.
func (m Model) Close() error  { return m.active.Load().close() }
func (m Model) Init() tea.Cmd { return tea.Batch(tea.RequestBackgroundColor, m.load(), m.waitRows()) }
func (m Model) waitRows() tea.Cmd {
	sink, ctx := m.sink, m.ctx
	return func() tea.Msg {
		select {
		case <-sink.wake:
		case <-sink.done:
		case <-ctx.Done():
			return nil
		}
		return rowsReady{sink}
	}
}
func (m Model) load() tea.Cmd {
	generation, query := m.generation, audit.Query{After: m.after, Box: m.filter, Limit: 500, Tail: !m.loadedOnce}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()
		batch, err := m.fetch(ctx, query)
		return loaded{batch, err, generation}
	}
}
func next() tea.Cmd { return tea.Tick(time.Second, func(time.Time) tea.Msg { return tick{} }) }
func (m Model) visible() []alertRow {
	var result []alertRow
	for _, row := range m.sink.snapshot() {
		if m.severity == "" || strings.EqualFold(row.event.Severity, m.severity) {
			result = append(result, row)
		}
	}
	return result
}
func (m Model) selectedIndex(rows []alertRow) int {
	for i, row := range rows {
		if row.owner == m.selectedOwner && row.kind == m.selectedKind {
			return i
		}
	}
	return min(m.selected, max(0, len(rows)-1))
}
func (m *Model) selectRow(rows []alertRow, index int) {
	m.selected = min(max(0, index), max(0, len(rows)-1))
	if len(rows) > 0 {
		m.selectedOwner, m.selectedKind = rows[m.selected].owner, rows[m.selected].kind
	}
}
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		bg := prettylog.BackgroundFrom(msg.Color)
		if bg.Known {
			logtheme.SetBackground(msg.Color)
			m.renderer = newEventRenderer(EventOptions{Color: m.color, Palette: logtheme.Palette(bg.Dark, msg.Color)})
		}
	case rowsReady:
		if msg.sink != m.sink {
			return m, m.waitRows()
		}
		rows := m.visible()
		m.selectRow(rows, m.selectedIndex(rows))
		select {
		case <-m.sink.done:
			return m, nil
		default:
		}
		return m, m.waitRows()
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
		m.loadedOnce = true
		if len(msg.Batch.Events) != 0 {
			for _, event := range msg.Batch.Events {
				if err := m.stream.add(m.ctx, event); err != nil {
					m.err = audit.Clean(err.Error())
					break
				}
			}
			m.selectRow(m.visible(), len(m.visible())-1)
		}
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
				_ = m.stream.close()
				m.sink.discard()
				m.resetStream()
				m.after = 0
				m.loadedOnce = false
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
			_ = m.Close()
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
			rows := m.visible()
			m.selectRow(rows, m.selectedIndex(rows)-1)
		case "down", "j":
			rows := m.visible()
			m.selectRow(rows, m.selectedIndex(rows)+1)
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
			m.selectRow(m.visible(), 0)
			m.offset = 0
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
	healthKind := prettylog.KindInfo
	if m.summary.Drops > 0 || m.summary.MetadataGaps > 0 || m.summary.Gap || (m.status.DriverDrops != nil && *m.status.DriverDrops > 0) {
		healthKind = prettylog.KindWarn
	}
	if m.status.State == "failed" {
		healthKind = prettylog.KindError
	}
	lines[2] = decorate(prettylog.DefaultGlyphs.For(healthKind)+" "+lines[2], m.renderer.roles[healthKind])
	if m.filtering {
		lines[3] = "Box filter: " + m.draft + "_  Enter applies · Esc cancels"
	}
	if m.err != "" {
		lines = append(lines, decorate(prettylog.DefaultGlyphs.Error+" Unavailable: "+m.err, m.renderer.roles[prettylog.KindError]))
	} else if m.status.Error != "" {
		lines = append(lines, decorate(prettylog.DefaultGlyphs.Error+" "+audit.Clean(m.status.Error), m.renderer.roles[prettylog.KindError]))
	}
	events := m.visible()
	if len(events) == 0 {
		lines = append(lines, "No matching retained events.")
		if !m.status.Desired.Enabled && m.status.State != "unsupported" {
			lines = append(lines, "Enable with yoloctl audit enable.")
		}
	}
	selected := m.selectedIndex(events)
	var details []string
	if len(events) > 0 {
		row := events[selected]
		e := *row.event
		text := m.renderer.message(e)
		if row.kind == prettylog.EmissionRepeatSummary {
			text = "First displayed event in this repeat run:\n" + text
		}
		if m.details {
			text += "\n" + strings.Join(m.renderer.fields(e), "\n")
		}
		if text != "" {
			details = strings.Split(ansi.Wrap(text, max(1, width-4), ""), "\n")
			limit := max(0, min(8, height-len(lines)-3))
			if len(details) > limit {
				details = details[:limit]
				if limit > 0 {
					details[limit-1] = ansi.Truncate(details[limit-1], max(1, width-3), "") + "…"
				}
			}
		}
	}
	rows := max(0, height-len(lines)-len(details)-1)
	offset := max(0, min(m.offset, selected))
	if selected >= offset+rows {
		offset = max(0, selected-rows+1)
	}
	for i := offset; i < len(events) && i < offset+rows; i++ {
		prefix := "  "
		if i == selected {
			prefix = "> "
		}
		row := events[i]
		text := m.renderer.heading(*row.event)
		if row.kind == prettylog.EmissionRepeatSummary {
			text = "    " + m.renderer.summary(row.repeat, row.until)
		}
		lines = append(lines, prefix+text)
	}
	for _, detail := range details {
		lines = append(lines, "    "+detail)
	}
	lines = append(lines, "↑↓ select · Enter fields · Space pause/follow · b box · v severity · r retry · q close")
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
