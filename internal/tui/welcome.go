package tui

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/user"
	"strconv"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/account"
)

// WelcomeStep is the first missing setup step confirmed by the dashboard's reader.
type WelcomeStep uint8

const (
	WelcomeUnknown WelcomeStep = iota
	WelcomeNoSpace
	WelcomeStopped
	WelcomeUpgrade
	WelcomeNoOperator
	WelcomeNoChat
	WelcomeEnroll
	WelcomeOnlyOperator
	WelcomeReady
)

type WelcomeStatus struct {
	Step     WelcomeStep
	Operator string
}

type welcomeTip struct {
	text, command string
}

type welcomeView struct {
	ctx      context.Context
	read     func(context.Context, string) WelcomeStatus
	cancel   context.CancelFunc
	visiting bool
	space    string
	visit    uint64
	tip      welcomeTip
}

type welcomeMsg struct {
	visit uint64
	tip   welcomeTip
}

// syncWelcome starts one read upon entering the idle no-tabs screen. The
// event loop only schedules it. Leaving or switching spaces cancels its work.
func (m *model) syncWelcome() tea.Cmd {
	w := &m.welcome
	if w.read == nil {
		return nil
	}
	visiting := len(m.tabs.Tabs) == 0 && !m.console.Busy()
	if w.visiting == visiting && (!visiting || w.space == m.ctxSpace) {
		return nil
	}
	if w.cancel != nil {
		w.cancel()
	}
	w.visiting, w.space = visiting, m.ctxSpace
	w.tip = welcomeTip{}
	w.visit++
	if !visiting {
		return nil
	}
	ctx, cancel := context.WithCancel(w.ctx)
	w.cancel = cancel
	read, space, visit := w.read, w.space, w.visit
	return func() tea.Msg {
		defer cancel()
		status := read(ctx, space)
		if ctx.Err() != nil {
			return nil
		}
		var cwd, login string
		if status.Step == WelcomeNoSpace || status.Step == WelcomeOnlyOperator {
			var err error
			cwd, err = os.Getwd()
			if err != nil {
				return nil
			}
		}
		if status.Step == WelcomeNoSpace {
			if u, err := user.Current(); err == nil {
				login = u.Username
			}
		}
		return welcomeMsg{visit, makeWelcomeTip(status, space, cwd, login)}
	}
}

func welcomeCommandTip(before, command, after string) welcomeTip {
	return welcomeTip{before + "\n  " + command + after + "\n→ puts it in the input", command}
}

func makeWelcomeTip(status WelcomeStatus, space, cwd, login string) welcomeTip {
	// Cwd is user-owned text. Show control characters as escapes rather than
	// letting a directory name send terminal controls.
	path := cwd
	if strings.ContainsFunc(path, unicode.IsControl) {
		path = strconv.QuoteToGraphic(path)
	}
	operator := status.Operator
	switch status.Step {
	case WelcomeNoSpace:
		if account.ValidateName(login) != nil {
			login = "personal"
		}
		command := "space create work --workdir " + path + " --account " + login
		tip := welcomeCommandTip("Tip: you have no space yet. A space is the VM your boxes run in. To create one, type:",
			command, "\n  (change the name and account first)")
		// The palette splits whitespace. Its subprocesses inherit this
		// directory, so a single dot can carry a path that spans tokens.
		if strings.ContainsFunc(cwd, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			tip.command = "space create work --workdir . --account " + login
		}
		return tip
	case WelcomeStopped:
		return welcomeCommandTip(fmt.Sprintf("Tip: space %s is stopped. To start it, type:", space), "space start "+space, "")
	case WelcomeUpgrade:
		return welcomeCommandTip(fmt.Sprintf("Tip: space %s runs an older version than this yoloctl. To upgrade it, type:", space),
			"space upgrade "+space, "\n  (it restarts sessions and asks before recreating any box)")
	case WelcomeNoOperator:
		return welcomeCommandTip("Tip: an operator is an agent that manages boxes for you, with full authority over this space. To create one, type:",
			"box create space-commander --agent claude --operator --agent-extra-args --model,claude-opus-5-5,--effort,medium", "")
	case WelcomeNoChat:
		return welcomeCommandTip("Tip: your space has no chat room yet. To create it, type:", "chat ensure", "")
	case WelcomeEnroll:
		command := "chat enroll " + operator + " --connect --recreate"
		return welcomeTip{fmt.Sprintf(`Tip: %[1]s is not in the chat yet. To add it, type:
  %[2]s
Then attach to it and ask it to listen to the chat. Type:
  attach %[1]s
and say: "Join the space chat and keep listening for messages"
→ puts the enroll command in the input`, operator, command), command}
	case WelcomeOnlyOperator:
		return welcomeCommandTip(fmt.Sprintf("Tip: ask %s for what you need in the chat. To open it, type:", operator), "chat",
			fmt.Sprintf("\nand say, for example: \"@%s create a box for %s with Claude and the go feature\"", operator, path))
	case WelcomeReady:
		return welcomeTips[rand.IntN(len(welcomeTips))]
	default:
		return welcomeTip{}
	}
}

var welcomeTips = [...]welcomeTip{
	welcomeCommandTip("Tip: have you ever tried a live, clickable view of your space? Type:", "space view", ""),
	welcomeCommandTip("Tip: have you ever tried watching a Markdown or text file live? Type:", "follow PATH", ""),
	welcomeCommandTip("Tip: have you ever tried joining a box's agent session? Type:", "attach BOX", ""),
	welcomeCommandTip("Tip: have you ever tried making a new box with another box's settings (not its files)? Type:", "box copy SRC NEW", ""),
	welcomeCommandTip("Tip: have you ever tried reaching a box's port from the host? Type:", "port forward BOX 8080", ""),
	welcomeCommandTip("Tip: have you ever tried seeing what fills the VM's disks? Type:", "space storage", ""),
	{text: "Tip: have you ever tried switching tabs from the keyboard? Press Ctrl+Alt ←/→ (⌃⌥←/→ on macOS)."},
}

const welcomeFallback = `Type a yoloctl command and press Enter, for example:
  box create coder --agent claude
  box attach coder
  space ls`

func (m *model) canFillWelcome() bool {
	line, _ := m.pal.Line()
	return !m.standalone && len(m.tabs.Tabs) == 0 && !m.console.Busy() &&
		line == "" && m.welcome.tip.command != ""
}

func (m *model) fillWelcome() {
	m.closeToast()
	m.focusPalette = true
	m.pal.Insert(m.welcome.tip.command)
	m.clearSuggestions()
}

func (m *model) welcomeCommandHit(mo tea.Mouse) bool {
	if !m.canFillWelcome() || mo.Y >= m.height || mo.X >= m.width {
		return false
	}
	rows, command := m.welcomeLayout()
	i := mo.Y - m.tabTop()
	if command == 0 || i < command || i >= len(rows) {
		return false
	}
	// The first non-underlined row after the command belongs to the help text.
	row := rows[i]
	if !strings.Contains(row, cUnderline) {
		return false
	}
	plain := ansi.Strip(row)
	return mo.X >= len(plain)-len(strings.TrimLeft(plain, " ")) &&
		mo.X < ansi.StringWidth(strings.TrimRight(plain, " "))
}

func (m *model) welcomeRows() []string {
	rows, _ := m.welcomeLayout()
	return rows
}

func (m *model) welcomeLayout() ([]string, int) {
	text := m.welcome.tip.text
	if text == "" {
		text = welcomeFallback
	}
	lines := []string{""}
	command := 0
	for i, logical := range strings.Split(text, "\n") {
		style := cDim
		if i == 1 && m.canFillWelcome() {
			command = len(lines)
			style += cUnderline
		}
		for _, line := range strings.Split(ansi.Wordwrap(logical, max(m.width-2, 1), ""), "\n") {
			lines = append(lines, style+"  "+line+cReset)
		}
	}
	return lines, command
}
