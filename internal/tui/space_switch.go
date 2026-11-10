package tui

import (
	"fmt"
	"os"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/operator"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/tui/palette"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
	"github.com/leogr/yoloctl/internal/tui/term"
)

var tuiOperatorClient = operator.LoadClient
var listSwitchSpaces = spaceconfig.List

func (m *model) initSpace() error {
	if native := spaceenv.Current(); native != nil {
		m.ctxSpace, m.spaceBound = native.Space, true
		return nil
	}
	client, err := tuiOperatorClient()
	if err != nil {
		return err
	}
	if client != nil {
		m.ctxSpace, m.spaceBound = client.Space, true
		return nil
	}
	space, _, err := config.ResolveSpace("", false)
	if err != nil {
		return err
	}
	m.ctxSpace = space
	return nil
}

// childEnv is a fresh snapshot for one launch. Never change os.Environ: two
// TUIs (or an in-flight command) can have different pinned defaults.
func (m *model) childEnv() []string {
	base := os.Environ()
	if m.standalone || m.ctxSpace == "" {
		return base
	}
	env := make([]string, 0, len(base)+1)
	for _, value := range base {
		if !strings.HasPrefix(value, config.SpaceEnv+"=") {
			env = append(env, value)
		}
	}
	return append(env, config.SpaceEnv+"="+m.ctxSpace)
}

type spacePicker struct {
	names         []string
	row, scroll   int
	loading       bool
	err           string
	request       uint64
	returnPalette bool
}

type spaceListMsg struct {
	generation, request uint64
	names               []string
	err                 error
}

const switchBusy = "Wait for the command to finish or interrupt it first"

func (m *model) spaceSwitchWhy() string {
	if m.standalone || m.spaceBound {
		return "this client is bound to its current space"
	}
	if m.console != nil && m.console.Busy() {
		return switchBusy
	}
	return ""
}

func (m *model) openSpacePicker() {
	if m.browserDialog != nil || m.confirm != nil {
		return
	}
	if why := m.spaceSwitchWhy(); why != "" {
		m.paneNotice(why)
		return
	}
	m.disarmPrefix()
	m.picker, m.notice = nil, nil
	m.spaceListRequest++
	request, generation := m.spaceListRequest, m.spaceGeneration
	m.spacePicker = &spacePicker{loading: true, request: request, returnPalette: m.focusPalette}
	if m.program != nil {
		// Capture all model state and the inventory function before the goroutine.
		program, list := m.program, listSwitchSpaces
		go func() {
			names, err := list()
			program.Send(spaceListMsg{generation: generation, request: request, names: names, err: err})
		}()
	}
}

func (m *model) receiveSpaceList(msg spaceListMsg) {
	p := m.spacePicker
	if p == nil || msg.generation != m.spaceGeneration || msg.request != p.request {
		return
	}
	p.loading = false
	if msg.err != nil {
		p.err = msg.err.Error()
		return
	}
	for _, name := range msg.names {
		if err := layout.ValidateExistingSpaceName(name); err != nil {
			p.err = err.Error()
			return
		}
	}
	p.names = slices.Clone(msg.names)
	slices.Sort(p.names)
	p.names = slices.Compact(p.names)
	p.row = max(slices.Index(p.names, m.ctxSpace), 0)
}

func (m *model) closeSpacePicker() {
	if p := m.spacePicker; p != nil {
		m.focusPalette = p.returnPalette
		m.spacePicker = nil
	}
}

func (m *model) chooseSpace() {
	p := m.spacePicker
	if p == nil || p.loading || p.err != "" || p.row < 0 || p.row >= len(p.names) {
		return
	}
	target := p.names[p.row]
	m.closeSpacePicker()
	if target == m.ctxSpace {
		return
	}
	if why := m.spaceSwitchWhy(); why != "" {
		m.paneNotice(why)
		return
	}
	m.openConfirm(&closeConfirm{space: target, all: m.switchPanes(), listed: asking(m.allPanes())})
}

func (m *model) spacePickerKey(k tea.Key) {
	mod := k.Mod &^ (tea.ModCapsLock | tea.ModNumLock)
	switch {
	case k.Code == tea.KeyUp && mod == 0:
		m.spacePicker.row = max(m.spacePicker.row-1, 0)
	case k.Code == tea.KeyDown && mod == 0:
		m.spacePicker.row = min(m.spacePicker.row+1, max(len(m.spacePicker.names)-1, 0))
	case k.IsRepeat:
	case k.Code == tea.KeyEscape && mod == 0:
		m.closeSpacePicker()
	case k.Code == tea.KeyEnter && mod == 0:
		m.chooseSpace()
	}
}

func (m *model) spacePickerOverlay() *overlay {
	p := m.spacePicker
	if p == nil {
		return nil
	}
	frame := popup{Title: "Switch space"}
	labels := make([]string, len(p.names))
	want := 40
	for i, name := range p.names {
		labels[i] = name
		if name == m.ctxSpace {
			labels[i] += " · current"
		}
		want = max(want, ansi.StringWidth(labels[i]))
	}
	switch {
	case p.loading:
		labels = []string{"Loading spaces…"}
	case p.err != "":
		labels = []string{"Cannot list spaces: " + p.err}
	case len(labels) == 0:
		labels = []string{"No spaces found"}
	}
	// The dropdown is anchored just below the top-right chip, independent of
	// which pane has focus. Leave the palette/tab strip geometry unchanged.
	w := min(want+4, max(m.width, 1))
	// Wrap words before handing the footer to the popup's cell wrapper.
	// A word wider than a tiny terminal is ellipsized rather than split.
	innerWidth := w
	if w >= 5 {
		innerWidth -= 4
	}
	footer := "Switches only this TUI · yoloctl space use changes the default"
	if ansi.StringWidth(footer) > innerWidth {
		footer = strings.ReplaceAll(footer, " · ", "\n")
	}
	if innerWidth >= ansi.StringWidth("yoloctl space use") {
		footer = strings.ReplaceAll(footer, "yoloctl space use", "yoloctl\u00a0space\u00a0use")
	}
	footer = ansi.Wordwrap(footer, innerWidth, "")
	footer = strings.ReplaceAll(footer, "\u00a0", " ")
	footerLines := strings.Split(footer, "\n")
	for i, line := range footerLines {
		footerLines[i] = ansi.Truncate(line, innerWidth, "…")
	}
	frame.Footer = strings.Join(footerLines, "\n")
	h := min(frame.rowsFor(w, len(labels)), max(m.height-1, 1))
	inner, _, shown := frame.layout(w, h)
	p.scroll = max(min(p.scroll, max(len(labels)-shown, 0)), 0)
	if !p.loading && p.err == "" && len(p.names) > 0 && shown > 0 {
		p.scroll = max(min(p.scroll, p.row), p.row-shown+1)
		p.scroll = max(min(p.scroll, len(labels)-shown), 0)
		frame.Highlight = p.row - p.scroll + 1
	}
	end := min(p.scroll+shown, len(labels))
	for _, label := range labels[p.scroll:end] {
		frame.Lines = append(frame.Lines, ansi.Truncate(label, inner, "…"))
	}
	return &overlay{x: max(m.width-w, 0), y: 1 - m.tabTop(), w: w, h: h, frame: frame, scroll: p.scroll}
}

func (m *model) spacePickerClick(mo tea.Mouse) {
	o := m.spacePickerOverlay()
	x, y := m.overlayPoint(o, mo)
	if mo.Button != tea.MouseLeft {
		return
	}
	_, top, rows := o.frame.layout(o.w, o.h)
	if x >= 0 && x < o.w && y >= top && y < top+rows {
		row := m.spacePicker.scroll + y - top
		if row >= 0 && row < len(m.spacePicker.names) {
			m.spacePicker.row = row
			m.chooseSpace()
			return
		}
	}
	m.closeSpacePicker()
}

// A confirmation binds every pane's identity AND its session. Even safe views
// newly opened/replaced while the dialog is up must be reviewed before closure.
type switchPane struct {
	tab     *tabs.Tab
	pane    *panes.Pane
	session *term.Session
}

func (m *model) switchPanes() []switchPane {
	var out []switchPane
	for _, t := range m.tabs.Tabs {
		for _, p := range t.Layout.Panes() {
			out = append(out, switchPane{t, p, p.Session})
		}
	}
	return out
}
func (m *model) allPanes() []*panes.Pane {
	var out []*panes.Pane
	for _, t := range m.tabs.Tabs {
		out = append(out, t.Layout.Panes()...)
	}
	return out
}

func (m *model) acceptSpaceSwitch() {
	c := m.confirm
	if why := m.spaceSwitchWhy(); why != "" {
		c.close = false
		m.paneNotice(why)
		return
	}
	now := m.switchPanes()
	if !slices.Equal(now, c.all) {
		c.all, c.listed, c.close, c.scroll = now, asking(m.allPanes()), false, 0
		m.paneNotice("the tabs or panes changed: review the list again")
		return
	}
	target := c.space
	m.cancelConfirm()
	for _, t := range slices.Clone(m.tabs.Tabs) {
		m.detachTab(t)
	}
	m.spaceGeneration++
	m.spaceListRequest++
	m.ctxSpace = target
	m.spacePicker, m.picker, m.prefix, m.acting, m.notice = nil, nil, nil, nil, nil
	m.newTab, m.resizeTab, m.placeholderFocus = nil, nil, nil
	m.cancelChrome()
	m.clearTabSelection()
	m.clearSuggestions()
	m.keyOwners = nil
	m.attachPrefix = nil
	m.selectionPointer = nil
	m.pal.Restore(palette.Draft{}) // history belongs to the palette, not the draft
	m.clearConsoleOutput()
	m.focusPalette = true
	m.status = ""
}

func (m *model) switchSummary() []string {
	c := m.confirm
	reviewedTabs := map[*tabs.Tab]struct{}{}
	for _, item := range c.all {
		reviewedTabs[item.tab] = struct{}{}
	}
	return []string{fmt.Sprintf("Close %d tabs / %d panes. Chat drafts and local view state are lost.", len(reviewedTabs), len(c.all)), "Established managed sessions keep running; other remote outcomes may be unknown."}
}

func (m *model) applySpacePicker(lines []string) {
	if m.spacePicker == nil {
		return
	}
	o := m.spacePickerOverlay()
	for i, row := range o.frame.render(o.w, o.h, m.surfaceBg()) {
		if y := m.tabTop() + o.y + i; y >= 0 && y < len(lines) {
			lines[y] = spliceRow(lines[y], row, o.x, o.w, m.width)
		}
	}
}
