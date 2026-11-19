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

func makeWelcomeTip(status WelcomeStatus, space, cwd, login string) welcomeTip {
	const fill = "\n→ puts it in the input"
	// Cwd is user-owned text. Show control characters as escapes rather than
	// letting a directory name send terminal controls.
	path := cwd
	if strings.ContainsFunc(path, unicode.IsControl) {
		path = strconv.QuoteToGraphic(path)
	}
	operator := status.Operator
	commandTip := func(before, command, after string) welcomeTip {
		return welcomeTip{before + "\n  " + command + after + fill, command}
	}
	switch status.Step {
	case WelcomeNoSpace:
		if account.ValidateName(login) != nil {
			login = "personal"
		}
		command := "space create work --workdir " + path + " --account " + login
		tip := commandTip("No space yet. A space is the VM your boxes run in.", command, "")
		tip.text += " · change the name and account first"
		// The palette splits whitespace. Its subprocesses inherit this
		// directory, so a single dot can carry a path that spans tokens.
		if strings.ContainsFunc(cwd, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			tip.command = "space create work --workdir . --account " + login
		}
		return tip
	case WelcomeStopped:
		return commandTip(fmt.Sprintf("Space %s is stopped.", space), "space start "+space, "")
	case WelcomeUpgrade:
		return commandTip(fmt.Sprintf("Space %s runs an older version than this yoloctl.", space),
			"space upgrade "+space, "\nIt restarts sessions and asks before recreating any box.")
	case WelcomeNoOperator:
		return commandTip("No operator box yet. An operator is an agent that manages\nboxes for you; it gets full authority over this space.",
			"box create operator --agent claude --operator --agent-extra-args --model,claude-opus-5-5,--effort,medium", "")
	case WelcomeNoChat:
		return commandTip("No chat yet. Create the space's chat room:", "chat ensure", "")
	case WelcomeEnroll:
		command := "chat enroll " + operator + " --connect --recreate"
		return welcomeTip{fmt.Sprintf("Add %[1]s to the chat:\n  %[2]s\nThen attach and tell it to start listening:\n  attach %[1]s\n  \"Join the space chat and keep listening for messages\"\n→ puts the enroll command in the input", operator, command), command}
	case WelcomeOnlyOperator:
		return commandTip(fmt.Sprintf("Open the chat and ask %[1]s, for example:\n  \"@%[1]s create a box for %[2]s with Claude and the go feature\"", operator, path), "chat", "")
	case WelcomeReady:
		return welcomeTips[rand.IntN(len(welcomeTips))]
	default:
		return welcomeTip{}
	}
}

var welcomeTips = [...]welcomeTip{
	{"· space view           live, clickable view of boxes, resources and chat", "space view"},
	{"· follow PATH          watch a Markdown or text file live", "follow PATH"},
	{"· attach BOX           join a box's persistent agent session", "attach BOX"},
	{"· box copy SRC NEW     a new box with the same settings (not files)", "box copy SRC NEW"},
	{"· port forward BOX 8080   reach a box's port from the host", "port forward BOX 8080"},
	{"· space storage        what fills the VM's disks", "space storage"},
	{"· Ctrl+Alt ←/→ (⌃⌥ on macOS)  switch tabs", ""},
}

func (m *model) welcomeRows() []string {
	text := m.welcome.tip.text
	if text == "" {
		return []string{"",
			cDim + "  type a yoloctl command and press Enter" + cReset,
			cDim + "  e.g.: box create coder --agent claude · box attach coder · space ls" + cReset,
		}
	}
	lines := []string{""}
	for _, line := range strings.Split(ansi.Wordwrap(text, max(m.width-2, 1), ""), "\n") {
		lines = append(lines, cDim+"  "+line+cReset)
	}
	return lines
}
