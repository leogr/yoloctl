package tui

import (
	"slices"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// A palette `space create NAME` that succeeds moves this host TUI to NAME:
// at once when its own space does not exist and no tab is open, otherwise
// through the switch dialog, whose default keeps the current space. The
// outcome belongs to the command that produced it: it is read once, when that
// command ends, and a failed create never moves the TUI.

// spaceRecordExists reports a saved space record; a seam for tests.
var spaceRecordExists = spaceconfig.Exists

// consoleRun is the palette command running in the console, bound to the
// space it runs in until it ends.
type consoleRun struct {
	run, generation uint64
	args            []string
}

// createdSpaceMsg is the off-loop answer for one ended command: it created
// name, and current says whether this TUI's own space has a record.
type createdSpaceMsg struct {
	run, generation uint64
	name            string
	current         bool
}

// recordConsoleRun replaces any earlier command: only the newest one can
// move the TUI. A command whose start failed is not followed.
func (m *model) recordConsoleRun(args []string, started bool) {
	m.consoleRuns++
	m.created = nil
	if started && m.createdSpace != nil && !m.spaceBound && !m.standalone {
		m.created = &consoleRun{run: m.consoleRuns, generation: m.spaceGeneration, args: slices.Clone(args)}
	}
}

// syncCreated consumes the recorded command once it has ended. Only a
// success starts the check, off the event loop.
func (m *model) syncCreated() tea.Cmd {
	c := m.created
	if c == nil || m.console.Busy() {
		return nil
	}
	m.created = nil
	if m.console.LastExit() != 0 || c.generation != m.spaceGeneration {
		return nil
	}
	parse, current := m.createdSpace, m.ctxSpace
	return func() tea.Msg {
		name := parse(c.args)
		if name == "" || name == current {
			return nil
		}
		if created, err := spaceRecordExists(name); err != nil || !created {
			return nil
		}
		exists, err := spaceRecordExists(current)
		if err != nil {
			return nil
		}
		return createdSpaceMsg{run: c.run, generation: c.generation, name: name, current: exists}
	}
}

// followCreated rechecks that the answer is still about the newest command
// and the same space, then switches or asks.
func (m *model) followCreated(msg createdSpaceMsg) {
	if msg.run != m.consoleRuns || msg.generation != m.spaceGeneration || m.spaceSwitchWhy() != "" {
		return
	}
	if !msg.current && len(m.tabs.Tabs) == 0 {
		m.switchSpace(msg.name, true)
		m.welcome.saveDefault = msg.name
		m.postNotice(prettylog.KindInfo, "This TUI now uses space "+msg.name)
		return
	}
	// The dialog takes every key, and y accepts it. It opens only on an idle
	// palette, never over a tab, a draft or a popup the next key belongs to.
	if !m.idlePalette() {
		m.postNotice(prettylog.KindInfo, "Space "+msg.name+" was created. To switch this TUI to it, click "+m.ctxSpace+" ▾ or press "+m.panePrefix().String()+" then s")
		return
	}
	m.openConfirm(&closeConfirm{space: msg.name, created: true, all: m.switchPanes(), listed: asking(m.allPanes())})
}

// idlePalette is a focused palette waiting for a new command: an empty
// draft, and no suggestion menu (Down opens it on an empty line), popup,
// armed prefix, pane resize window, notice history, history view or new-tab
// line owns the next key.
func (m *model) idlePalette() bool {
	line, _ := m.pal.Line()
	return m.focusPalette && line == "" && !m.menuShown() && !m.resizing() && m.newTab == nil && !m.consoleHistory && !m.noticeFocused && !m.toastSuppressed()
}
