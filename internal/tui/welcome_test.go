package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
)

func TestWelcomeTipCommands(t *testing.T) {
	for _, tc := range []struct {
		step    WelcomeStep
		command string
		line    string
	}{
		{WelcomeNoSpace, "space create work --workdir /project --account my.user", "No space yet. A space is the VM your boxes run in."},
		{WelcomeStopped, "space start work", "Space work is stopped."},
		{WelcomeUpgrade, "space upgrade work", "It restarts sessions and asks before recreating any box."},
		{WelcomeNoOperator, "box create space-commander --agent claude --operator --agent-extra-args --model,claude-opus-5-5,--effort,medium", "boxes for you; it gets full authority over this space."},
		{WelcomeNoChat, "chat ensure", "No chat yet. Create the space's chat room:"},
		{WelcomeEnroll, "chat enroll lead --connect --recreate", "\"Join the space chat and keep listening for messages\""},
		{WelcomeOnlyOperator, "chat", "\"@lead create a box for /project with Claude and the go feature\""},
	} {
		got := makeWelcomeTip(WelcomeStatus{Step: tc.step, Operator: "lead"}, "work", "/project", "my.user")
		if got.command != tc.command || !strings.Contains(got.text, "\n  "+tc.command+"\n") || !strings.Contains(got.text, tc.line) {
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
	if !strings.Contains(ansi.Strip(m.View().Content), "type a yoloctl command and press Enter") {
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

func TestWelcomeRightFillsWithoutRunning(t *testing.T) {
	m := newTestModel(t, 100, 24)
	m.welcome.tip = makeWelcomeTip(WelcomeStatus{Step: WelcomeStopped}, "work", "", "")
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModAlt})
	if line, _ := m.pal.Line(); line != "" {
		t.Fatalf("modified Right inserted %q", line)
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if line, _ := m.pal.Line(); line != "space start work" || m.console.Busy() || len(m.tabs.Tabs) != 0 {
		t.Fatalf("Right did more than fill the input: line=%q busy=%v tabs=%d", line, m.console.Busy(), len(m.tabs.Tabs))
	}
	m.pal.Home()
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if line, cursor := m.pal.Line(); line != "space start work" || cursor != 1 {
		t.Fatalf("Right changed the nonempty draft: line=%q cursor=%d", line, cursor)
	}
}
