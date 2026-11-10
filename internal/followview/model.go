package followview

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"sort"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/markdownview"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

type Format string

const (
	Auto     Format = "auto"
	Text     Format = "text"
	Markdown Format = "markdown"
)

func ParseFormat(value string) (Format, error) {
	switch Format(value) {
	case Auto, Text, Markdown:
		return Format(value), nil
	}
	return "", fmt.Errorf("unknown format %q; use auto, text or markdown", value)
}

type Options struct {
	Reader Reader
	Path   string               // already absolute and pinned by the CLI
	Space  string               // empty is explicit host mode
	Format Format               // auto has already been resolved from the selected name
	Copy   func(string) tea.Cmd // caller applies the existing host clipboard policy
}

type rendered struct {
	doc      markdownview.Document
	rows     []markdownview.BodyRow
	starts   []int
	text     string
	markdown bool
}

func renderFile(raw []byte, large, source bool, format Format, width int, method ansi.Method, theme spaceview.Theme) *rendered {
	text := markdownview.Plain(string(raw))
	isMarkdown := format == Markdown && !large
	options := markdownview.ParseOptions{Method: method, Theme: theme, Source: source || !isMarkdown}
	if !isMarkdown {
		options.Decorate = func(_ string, pieces []markdownview.Piece, _ spaceview.Theme, _ bool) []markdownview.Piece {
			return pieces
		}
	}
	doc := markdownview.Parse(text, options)
	layout := markdownview.Layout(doc, max(1, width), markdownview.LayoutOptions{Method: method, Theme: theme, UnlimitedRows: true})
	starts := []int{0}
	for i, c := range text {
		if c == '\n' {
			starts = append(starts, i+1)
		}
	}
	return &rendered{doc: doc, rows: layout.Rows, starts: starts, text: text, markdown: isMarkdown}
}

// jobs joins owned reads/renders on exit. A queued Bubble Tea command that
// starts after close cannot add work, and never reaches the reader.
type jobs struct {
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

func newJobs(ctx context.Context) *jobs {
	ctx, cancel := context.WithCancel(ctx)
	return &jobs{ctx: ctx, cancel: cancel}
}
func (j *jobs) run(fn func(context.Context) tea.Msg) tea.Msg {
	j.mu.Lock()
	if j.closed {
		j.mu.Unlock()
		return nil
	}
	j.wg.Add(1)
	j.mu.Unlock()
	defer j.wg.Done()
	return fn(j.ctx)
}
func (j *jobs) cancelWork() { j.mu.Lock(); j.closed = true; j.cancel(); j.mu.Unlock() }
func (j *jobs) close()      { j.cancelWork(); j.wg.Wait() }

type pollMsg struct{}
type resultMsg struct {
	read     bool
	gen      uint64
	snapshot Snapshot
	raw      []byte
	view     *rendered
	err      error
}
type CopyResultMsg struct{ Err error }
type point struct{ line, offset int }
type selection struct{ anchor, focus point }

type Model struct {
	opts                            Options
	jobs                            *jobs
	width, height                   int
	method                          ansi.Method
	theme                           spaceview.Theme
	bg                              color.Color
	dark                            bool
	source, following, busy, closed bool
	gen                             uint64
	pendingRead, pendingRender      bool
	query                           Query
	snapshot                        Snapshot
	raw                             []byte
	view                            *rendered
	top                             int
	anchor                          int64
	stale, notice                   string
	selection                       *selection
	drag                            bool
	pressed                         markdownview.Action
}

func New(ctx context.Context, opts Options) *Model {
	return &Model{opts: opts, jobs: newJobs(ctx), dark: true, theme: spaceview.NewTheme(true, nil, nil), following: true, pendingRead: true, query: Query{Tail: true}}
}
func (m *Model) Init() tea.Cmd {
	return tea.Batch(tea.RequestBackgroundColor, m.poll(), m.work(false, false))
}
func (m *Model) Close() { m.closed = true; m.jobs.close() }
func (m *Model) poll() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return pollMsg{} })
}
func (m *Model) work(read, render bool) tea.Cmd {
	if m.closed {
		return nil
	}
	m.pendingRead = m.pendingRead || read
	m.pendingRender = m.pendingRender || render
	if m.busy || m.width <= 0 || (!m.pendingRead && !m.pendingRender) {
		return nil
	}
	m.busy = true
	read, render = m.pendingRead || m.snapshot.Key == "", m.pendingRender
	m.pendingRead, m.pendingRender = false, false
	gen, q, raw, snapshot := m.gen, m.query, m.raw, m.snapshot
	q.Previous = snapshot.Key
	width, method, theme, source, format := m.width, m.method, m.theme, m.source, m.opts.Format
	return func() tea.Msg {
		return m.jobs.run(func(ctx context.Context) tea.Msg {
			result := resultMsg{gen: gen, read: read, snapshot: snapshot, raw: raw}
			if read {
				next, err := m.opts.Reader.Read(ctx, q)
				result.err = err
				if err == nil {
					result.snapshot = next
					if next.Changed {
						result.raw = next.Data
						render = true
					}
				}
			}
			if ctx.Err() != nil {
				return resultMsg{gen: gen, read: read, err: ctx.Err()}
			}
			if render && (result.err == nil || snapshot.Key != "") {
				result.view = renderFile(result.raw, result.snapshot.Large, source, format, width, method, theme)
			}
			return result
		})
	}
}
func (m *Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if m.closed {
		return m, nil
	}
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.captureAnchor()
		m.width, m.height = max(0, msg.Width), max(0, msg.Height)
		m.gen++
		return m, m.work(false, true)
	case tea.BackgroundColorMsg:
		m.dark, m.bg = msg.IsDark(), msg.Color
		m.theme = spaceview.NewTheme(m.dark, m.bg, nil)
		m.gen++
		return m, m.work(false, true)
	case tea.ModeReportMsg:
		if msg.Mode == ansi.ModeUnicodeCore && (msg.Value == ansi.ModeReset || msg.Value == ansi.ModeSet || msg.Value == ansi.ModePermanentlySet) && m.method != ansi.GraphemeWidth {
			m.captureAnchor()
			m.method = ansi.GraphemeWidth
			m.selection = nil
			m.gen++
			return m, m.work(false, true)
		}
	case pollMsg:
		return m, tea.Batch(m.poll(), m.work(true, false))
	case resultMsg:
		m.busy = false
		if msg.gen != m.gen {
			return m, m.work(false, false)
		}
		// Cached rerendering says nothing about source availability. Only an
		// actual read (including an unchanged success) can change that state.
		if msg.read {
			if msg.err != nil {
				m.stale = markdownview.Plain(msg.err.Error())
			} else {
				m.stale = ""
				m.snapshot = msg.snapshot
				m.raw = msg.raw
				if msg.snapshot.Changed {
					m.selection = nil
					m.drag = false
				}
			}
		}
		if msg.view != nil {
			m.view = msg.view
			m.placeAnchor()
		}
		return m, m.work(false, false)
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.closed = true
			m.jobs.cancelWork()
			return m, tea.Quit
		case "r":
			return m, m.work(true, false)
		case "ctrl+s":
			m.captureAnchor()
			m.source = !m.source
			m.selection = nil
			m.gen++
			return m, m.work(false, true)
		case "f", "end":
			m.following = true
			m.query.Tail = true
			m.top = m.maxTop()
			m.gen++
			return m, m.work(true, false)
		case "home":
			m.pause()
			m.anchor = 0
			m.query.Offset = 0
			m.top = 0
			m.gen++
			return m, m.work(true, false)
		case "up", "k":
			return m, m.scroll(-1)
		case "down", "j":
			return m, m.scroll(1)
		case "pgup", "ctrl+u":
			return m, m.scroll(-max(1, m.bodyHeight()-1))
		case "pgdown", "ctrl+d":
			return m, m.scroll(max(1, m.bodyHeight()-1))
		case "y", "ctrl+shift+c":
			return m, m.copySelection()
		case "esc":
			m.selection = nil
			m.notice = ""
		}
	case tea.MouseMsg:
		return m, m.mouse(msg)
	case CopyResultMsg:
		if msg.Err != nil {
			m.notice = markdownview.Plain(msg.Err.Error())
		} else {
			m.notice = "Copied displayed text"
		}
	}
	return m, nil
}
func (m *Model) bodyHeight() int { return max(0, m.height-2) }
func (m *Model) maxTop() int {
	if m.view == nil {
		return 0
	}
	return max(0, len(m.view.rows)-max(1, m.bodyHeight()))
}
func (m *Model) captureAnchor() {
	if m.view == nil || len(m.view.rows) == 0 {
		return
	}
	r := m.view.rows[min(m.top, len(m.view.rows)-1)]
	if r.Line >= 0 && r.Line < len(m.view.starts) {
		m.anchor = m.snapshot.Start + int64(m.view.starts[r.Line]+r.Start)
	}
}
func (m *Model) placeAnchor() {
	if m.following {
		m.top = m.maxTop()
		return
	}
	if m.view == nil {
		return
	}
	relative := max(0, m.anchor-m.snapshot.Start)
	line := max(0, sort.Search(len(m.view.starts), func(i int) bool { return int64(m.view.starts[i]) > relative })-1)
	target := 0
	for i, row := range m.view.rows {
		if row.Line > line {
			break
		}
		target = i
		if row.Line == line && int64(m.view.starts[line]+row.End) >= relative {
			break
		}
	}
	m.top = min(target, m.maxTop())
}
func (m *Model) pause() {
	if m.following {
		m.following = false
		m.query.Tail = false
		m.query.Offset = m.snapshot.Start
		if m.busy {
			m.gen++
			m.pendingRead = true
		}
	}
}
func (m *Model) scroll(delta int) tea.Cmd {
	m.pause()
	old := m.top
	m.top = min(max(0, m.top+delta), m.maxTop())
	m.captureAnchor()
	if m.snapshot.Large && delta < 0 && old == 0 && m.snapshot.Start > 0 {
		m.anchor = m.snapshot.Start
		m.query.Offset = max(0, m.snapshot.Start-WindowBytes)
		m.gen++
		return m.work(true, false)
	}
	if m.snapshot.Large && delta > 0 && old == m.maxTop() && m.snapshot.End < m.snapshot.Size {
		m.anchor = m.snapshot.End
		m.query.Offset = m.snapshot.End
		m.gen++
		return m.work(true, false)
	}
	return nil
}
func less(a, b point) bool { return a.line < b.line || a.line == b.line && a.offset < b.offset }
func (m *Model) selectedBounds() (point, point, bool) {
	if m.selection == nil {
		return point{}, point{}, false
	}
	a, b := m.selection.anchor, m.selection.focus
	if less(b, a) {
		a, b = b, a
	}
	return a, b, a != b
}
func (m *Model) selectedText() string {
	a, b, ok := m.selectedBounds()
	if !ok || m.view == nil {
		return ""
	}
	var lines []string
	for n := a.line; n <= b.line && n < len(m.view.doc.Lines); n++ {
		line := m.view.doc.Lines[n]
		if line.Decor {
			continue
		}
		lo, hi := 0, len(line.Text)
		if n == a.line {
			lo = min(a.offset, hi)
		}
		if n == b.line {
			hi = min(b.offset, hi)
		}
		lines = append(lines, line.CopyText(min(lo, hi), hi))
	}
	return strings.Join(lines, "\n")
}
func (m *Model) copyText(text string) tea.Cmd {
	if text == "" {
		return nil
	}
	if m.opts.Copy == nil {
		m.notice = "Host clipboard is off (seams.clipboard)"
		return nil
	}
	if len(text) > 1<<20 {
		m.notice = "Selection is larger than 1 MiB; nothing was copied"
		return nil
	}
	m.notice = "Copied displayed text"
	return m.opts.Copy(text)
}
func (m *Model) copySelection() tea.Cmd { return m.copyText(m.selectedText()) }
func (m *Model) pointAt(x, y int) (point, markdownview.Action, bool) {
	if m.view == nil || len(m.view.rows) == 0 {
		return point{}, markdownview.Action{}, false
	}
	index := min(max(m.top+y-1, 0), len(m.view.rows)-1)
	row := m.view.rows[index]
	line := min(max(row.Line, 0), len(m.view.doc.Lines)-1)
	lo, hi := markdownview.HitRun(m.method, row.Runs, max(0, x), row.End)
	_ = hi
	return point{line: line, offset: min(lo, len(m.view.doc.Lines[line].Text))}, markdownview.ActionAt(m.method, row.Runs, max(0, x)), true
}
func (m *Model) mouse(msg tea.MouseMsg) tea.Cmd {
	mouse := msg.Mouse()
	switch msg.(type) {
	case tea.MouseWheelMsg:
		if mouse.Button == tea.MouseWheelUp {
			return m.scroll(-1)
		}
		if mouse.Button == tea.MouseWheelDown {
			return m.scroll(1)
		}
	case tea.MouseClickMsg:
		if mouse.Button != tea.MouseLeft || mouse.Y < 1 || mouse.Y >= m.height-1 {
			return nil
		}
		p, act, ok := m.pointAt(mouse.X, mouse.Y)
		if !ok {
			return nil
		}
		m.pause()
		m.drag = true
		m.pressed = act
		m.selection = &selection{anchor: p, focus: p}
	case tea.MouseMotionMsg:
		if m.drag {
			if p, _, ok := m.pointAt(mouse.X, mouse.Y); ok {
				m.selection.focus = p
			}
		}
	case tea.MouseReleaseMsg:
		if m.drag {
			p, act, ok := m.pointAt(mouse.X, mouse.Y)
			m.drag = false
			if ok {
				m.selection.focus = p
				if act.Kind == markdownview.IntentCopyCode && act == m.pressed && m.selection.anchor == p {
					return m.copyText(act.Value)
				}
			}
		}
	}
	return nil
}
func (m *Model) View() tea.View {
	if m.width == 0 || m.height == 0 {
		return tea.NewView("")
	}
	title := "host"
	if m.opts.Space != "" {
		title = m.opts.Space
	}
	title = "follow · " + title + " · " + strings.ReplaceAll(markdownview.Plain(m.opts.Path), "\n", " ")
	titleStyle := spaceview.Style{FG: m.theme.Brand, BG: spaceview.FirstRowBG, Bold: true}
	rows := []string{markdownview.Paint(m.method, []markdownview.Run{markdownview.Deco(title, titleStyle)}, m.width, spaceview.FirstRowBG, nil, spaceview.Style{})}
	a, b, selected := m.selectedBounds()
	for y := 0; y < m.bodyHeight(); y++ {
		i := m.top + y
		var runs []markdownview.Run
		line := -1
		if m.view != nil && i < len(m.view.rows) {
			runs = m.view.rows[i].Runs
			line = m.view.rows[i].Line
		}
		highlight := func(run markdownview.Run) (int, int) {
			if !selected || line < a.line || line > b.line {
				return 0, 0
			}
			lo, hi := run.Start, run.Start+len(run.Text)
			if line == a.line {
				lo = max(lo, a.offset)
			}
			if line == b.line {
				hi = min(hi, b.offset)
			}
			if lo >= hi {
				return 0, 0
			}
			return lo - run.Start, hi - run.Start
		}
		rows = append(rows, markdownview.Paint(m.method, runs, m.width, m.theme.Surface, highlight, spaceview.Style{FG: m.theme.Text, BG: m.theme.Raised}))
	}
	mode := "text"
	if m.view != nil && m.view.markdown {
		mode = "Markdown"
		if m.source {
			mode = "Markdown source"
		}
	}
	state := "reading"
	if m.following {
		state = "following"
	}
	status := fmt.Sprintf("bytes %d–%d/%d · %s · %s · r refresh · f/End follow · Ctrl+S source · y copy · q quit", m.snapshot.Start, m.snapshot.End, m.snapshot.Size, state, mode)
	if m.snapshot.Large {
		status = fmt.Sprintf("bytes %d–%d/%d · large file: plain 1 MiB window · other bytes omitted · %s · r refresh · f/End follow · y copy · q quit", m.snapshot.Start, m.snapshot.End, m.snapshot.Size, state)
	}
	if m.snapshot.Key == "" {
		status = "Loading · " + status
	}
	if m.notice != "" {
		status = m.notice + " · " + status
	}
	style := spaceview.Style{FG: m.theme.Dim, BG: m.theme.Surface}
	if m.stale != "" {
		status = "Unavailable: " + m.stale
		if m.snapshot.Key != "" {
			status = "Unavailable (last display retained): " + m.stale
		}
		style.FG = m.theme.Bad
	}
	rows = append(rows, markdownview.Paint(m.method, []markdownview.Run{markdownview.Deco(strings.ReplaceAll(status, "\n", " "), style)}, m.width, m.theme.Surface, nil, spaceview.Style{}))
	v := tea.NewView(strings.Join(rows[:min(len(rows), m.height)], "\n"))
	v.AltScreen = true
	v.MouseMode = tea.MouseModeAllMotion
	v.WindowTitle = title
	return v
}
func Run(ctx context.Context, opts Options) error {
	m := New(ctx, opts)
	defer m.Close()
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithoutSignalHandler())
	_, err := p.Run()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return nil
	}
	return err
}
