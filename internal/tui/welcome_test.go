package tui

import (
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

func TestWelcomeTipCommands(t *testing.T) {
	const hint = "\nClick the command to put it in the input."
	for _, tip := range welcomeTips {
		if tip.command != "" && !strings.HasSuffix(tip.text, hint) {
			t.Errorf("random tip %q is missing the approved hint", tip.command)
		}
	}

	for _, tc := range []struct {
		step    WelcomeStep
		command string
		line    string
	}{
		{WelcomeNoSpace, "space create work --workdir /project --account my.user", "Tip: you have no space yet. A space is the VM your boxes run in. To create one, type:"},
		{WelcomeStopped, "space start work", "Tip: space work is stopped. To start it, type:"},
		{WelcomeUpgrade, "space upgrade work", "  (it restarts sessions and asks before recreating any box)"},
		{WelcomeNoOperator, "box create space-commander --agent claude --operator --agent-extra-args --model,claude-opus-5-5,--effort,medium", "with full authority over this space. To create one, type:"},
		{WelcomeNoChat, "chat ensure", "Tip: your space has no chat room yet. To create it, type:"},
		{WelcomeEnroll, "chat enroll lead --connect --recreate", "\"Join the space chat and keep listening for messages\""},
		{WelcomeOnlyOperator, "chat", "\"@lead create a box for /project with Claude and the go feature\""},
	} {
		got := makeWelcomeTip(WelcomeStatus{Step: tc.step, Operator: "lead"}, "work", "/project", "my.user")
		if got.command != tc.command || !strings.Contains(got.text, "\n  "+tc.command+"\n") || !strings.Contains(got.text, tc.line) || !strings.HasSuffix(got.text, hint) {
			t.Errorf("step %d tip = %+v, want command %q and line %q", tc.step, got, tc.command, tc.line)
		}
	}
	if got := makeWelcomeTip(WelcomeStatus{Step: WelcomeNoSpace}, "", "/my project", "invalid name"); got.command != "space create work --workdir . --account personal" ||
		!strings.Contains(got.text, "space create work --workdir /my project --account personal") {
		t.Fatalf("space name fallback or spaced project path: %+v", got)
	}
}

func TestWelcomeVisitAndStaleResults(t *testing.T) {
	m := newTestModel(t, 100, 24)
	var reads []string
	m.ctxSpace = "work"
	m.welcome = welcomeView{ctx: t.Context(), read: func(_ context.Context, space string) WelcomeStatus {
		reads = append(reads, space)
		return WelcomeStatus{Step: WelcomeReady}
	}}

	firstRead := m.syncWelcome()
	if firstRead == nil || len(reads) != 0 || m.syncWelcome() != nil {
		t.Fatal("entering the welcome screen should schedule one background read")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "Type a yoloctl command and press Enter, for example:") {
		t.Fatal("welcome fallback missing while the read is outstanding")
	}
	first := firstRead().(welcomeMsg)
	_, _ = m.Update(first)
	if m.welcome.tip.text == "" || m.syncWelcome() != nil || m.welcome.tip != first.tip {
		t.Fatal("the visit did not retain the random tip")
	}

	tab := tabs.New(&panes.Pane{})
	m.tabs.Add(tab)
	if m.syncWelcome() != nil || m.welcome.tip.text != "" {
		t.Fatal("opening a tab should leave the welcome screen")
	}
	m.tabs.CloseTab(tab)
	secondRead := m.syncWelcome()
	if secondRead == nil {
		t.Fatal("closing the last tab did not schedule a new read")
	}
	_, _ = m.Update(first)
	if m.welcome.tip.text != "" {
		t.Fatal("the previous visit overwrote the fallback")
	}
	second := secondRead().(welcomeMsg)
	_, _ = m.Update(second)
	if len(reads) != 2 || m.welcome.tip != second.tip {
		t.Fatalf("second visit: reads=%v tip=%+v", reads, m.welcome.tip)
	}

	m.ctxSpace = "other"
	next := m.syncWelcome()
	if next == nil {
		t.Fatal("switching spaces with no tabs did not schedule a new read")
	}
	_, _ = m.Update(second)
	if m.welcome.tip.text != "" {
		t.Fatal("the previous space overwrote the fallback")
	}
	_ = next()
	if len(reads) != 3 || reads[2] != "other" {
		t.Fatalf("checked spaces = %v", reads)
	}
}

func TestWelcomeKeysDoNotFill(t *testing.T) {
	for _, focused := range []bool{false, true} {
		m := newTestModel(t, 140, 24)
		m.welcome.tip = makeWelcomeTip(WelcomeStatus{Step: WelcomeStopped}, "work", "", "")
		if focused {
			m.postNotice(prettylog.KindWarn, "retained")
			m.openToast()
		}
		runCompatibilityInput(t, m, "\x1b[C\t", false)
		if line, _ := m.pal.Line(); line != "" || m.console.Busy() {
			t.Fatalf("focused=%v: Right or Tab filled the input: %q", focused, line)
		}
		if focused && !m.noticeFocused {
			t.Fatal("notice lost focus after Right or Tab")
		}
	}
}

func TestWelcomeClickAndDrag(t *testing.T) {
	for _, tc := range []struct {
		name    string
		focused bool
		row     string
		drag    bool
		want    string
	}{
		{"command click", false, "space start work", false, "space start work"},
		{"command click with notice", true, "space start work", false, "space start work"},
		{"command drag", false, "space start work", true, "space"},
		{"tip text drag with notice", true, "Tip: ", true, "Tip: "},
		{"hint click", false, "Click the command", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t, 140, 24)
			m.welcome.tip = makeWelcomeTip(WelcomeStatus{Step: WelcomeStopped}, "work", "", "")
			if tc.focused {
				m.postNotice(prettylog.KindWarn, "retained")
				m.openToast()
			}
			x, y := 0, -1
			for i, row := range strings.Split(ansi.Strip(m.View().Content), "\n") {
				if at := strings.Index(row, tc.row); at >= 0 {
					x, y = ansi.StringWidth(row[:at]), i
					break
				}
			}
			if y < 0 {
				t.Fatalf("missing row %q", tc.row)
			}
			_, _ = m.Update(click(x, y))
			if line, _ := m.pal.Line(); line != "" {
				t.Fatalf("press filled the input before release: %q", line)
			}
			end := x
			if tc.drag {
				end += 4
				_, _ = m.Update(tea.MouseMotionMsg{X: end, Y: y, Button: tea.MouseLeft})
			}
			_, cmd := m.Update(releaseAt(end, y))
			if line, _ := m.pal.Line(); m.console.Busy() || (!tc.drag && line != tc.want) || (tc.drag && line != "") {
				t.Fatalf("unexpected input after release: %q (busy=%v)", line, m.console.Busy())
			}
			if tc.drag {
				if cmd == nil {
					t.Fatal("drag did not copy")
				}
				msg, ok := cmd().(tea.RawMsg)
				if encoded := base64.StdEncoding.EncodeToString([]byte(tc.want)); !ok || !strings.Contains(fmt.Sprint(msg.Msg), "52;c;"+encoded) {
					t.Fatalf("drag clipboard output = %v, want %q", msg, tc.want)
				}
			}
		})
	}
}

func TestWelcomeFallbackExampleLines(t *testing.T) {
	m := newTestModel(t, 100, 24)
	got := m.welcomeRows()
	for i, row := range got {
		got[i] = ansi.Strip(row)
	}
	want := []string{"", "  Type a yoloctl command and press Enter, for example:", "    box create coder --agent claude", "    box attach coder", "    space ls"}
	if !slices.Equal(got, want) {
		t.Fatalf("fallback rows = %q, want %q", got, want)
	}
}
