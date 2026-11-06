package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/tui/term"
)

// RunAttached uses the same terminal, input, selection and host integration as
// a tab, with no palette or tab strip. The shared CLI still performs all setup.
// Its inherited session channel prevents recursive frontend startup.
func RunAttached(ctx context.Context, args []string) (detached bool, err error) {
	cfg, err := config.LoadTUI()
	if err != nil {
		return false, err
	}
	m := &model{pending: &atomic.Bool{}, cfg: cfg, standalone: true, initialArgs: args}
	defer m.resetHostMouse(os.Stdout)
	// runProgram owns context cancellation and graceful terminal cleanup.
	p := tea.NewProgram(m, m.programOptions()...)
	m.program = p
	m.console = term.NewConsole(24, 80, cfg.ScrollbackLines, m.wake)
	defer func() { _ = m.console.Close() }()
	m.console.SetOSC52Handler(m.handleOSC52Output)
	m.console.SetBellHandler(m.handleBellOutput)
	err = runProgram(ctx, p)
	if p := m.tabs.Focused(); p != nil { // standalone: one tab, one pane
		if p.Exited {
			err = attachedCommandResult(p.Session)
		} else {
			detached = true
		}
		p.Session.Close()
	} else if err == nil {
		err = errors.New(m.status)
	}
	if ctx.Err() != nil {
		return true, nil
	}
	return detached, err
}

func (m *model) chromeRows() int {
	if m.standalone {
		return 0
	}
	return barRows
}

func (m *model) attachedKey(k tea.Key) (tea.Model, tea.Cmd) {
	t := m.tabs.Focused()
	if t == nil {
		return m, nil
	}
	if k.Code >= tea.KeyLeftShift && k.Code <= tea.KeyIsoLevel5Shift {
		m.sendKey(t.Session, k, false)
		return m, nil
	}
	m.status = "" // dismiss a notice overlay on the next non-modifier key
	prefix := isPaletteKey(tea.KeyPressMsg(k), m.cfg.GetPaletteKey())
	if m.attachPrefix != nil {
		previous := *m.attachPrefix
		m.attachPrefix = nil
		if isStandaloneDetachKey(k) {
			return m, tea.Quit
		}
		m.sendKey(t.Session, previous, false)
		m.sendKey(t.Session, previous, true)
		delete(m.keyOwners, keyIdentity(previous))
		if prefix {
			return m, nil
		}
	} else if prefix {
		m.attachPrefix = &k
		return m, nil
	}
	if cmd, copied := m.copySelection(k); copied {
		return m, cmd
	}
	if k.Code < tea.KeyLeftShift || k.Code > tea.KeyIsoLevel5Shift {
		m.clearTabSelection()
	}
	m.scrollToLive()
	m.sendKey(t.Session, k, false)
	return m, nil
}

// isStandaloneDetachKey reports whether the key after the palette prefix
// detaches standalone attachment: Ctrl+W, or tmux-style Ctrl+D and plain d.
// Lock modifiers are ignored; any other key is forwarded with the prefix.
func isStandaloneDetachKey(k tea.Key) bool {
	switch tea.KeyPressMsg(k).Keystroke() {
	case "ctrl+w", "ctrl+d", "d":
		return true
	}
	mod := k.Mod &^ (tea.ModCapsLock | tea.ModNumLock)
	switch {
	case mod == 0 && k.Text == "" && (k.Code == 0x17 || k.Code == 0x04):
		return true // legacy C0 Ctrl+W / Ctrl+D
	case mod == tea.ModCtrl && (k.Code == 'w' || k.Code == 'd'):
		return true
	case mod == 0 && k.Code == 'd':
		return true
	}
	return false
}

// A setup failure has no holder snapshot: its diagnostic was drawn only in
// the temporary standalone screen, which is gone when RunAttached returns.
// Keep that screen's bounded plain-text tail alongside the original status.
// It is display output only, never session protocol or operation evidence.
func attachedCommandResult(s *term.Session) error {
	err := s.Wait(time.Second)
	if err == nil || s.HolderView() {
		return err
	}
	tail := attachedSetupOutput(s.GridText())
	if tail == "" {
		return err
	}
	return fmt.Errorf("%w; last setup output:\n%s", err, tail)
}

const attachedSetupOutputLimit = 4 << 10

func attachedSetupOutput(grid string) string {
	var lines []string
	for _, line := range strings.Split(grid, "\n") {
		line = strings.TrimRight(line, " \t")
		// Session.Wait has already appended the frontend's own exit banner.
		if strings.HasPrefix(line, "✗ exit ") || strings.HasPrefix(line, "✓ exit ") || strings.HasPrefix(line, term.ExitCloseHint) {
			continue
		}
		lines = append(lines, line)
	}
	text := strings.TrimSpace(strings.Join(lines, "\n"))
	if len(text) > attachedSetupOutputLimit {
		text = strings.ToValidUTF8(text[len(text)-attachedSetupOutputLimit:], "")
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, text)
}
