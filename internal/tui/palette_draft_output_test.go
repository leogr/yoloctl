package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/leogr/yoloctl/internal/tui/yoloctlcli"
)

// Editing the palette line hides the shown output: completion that offers
// nothing leaves the area empty instead of showing the previous command's
// output (leogr's report: "box create" over the earlier root help and
// "exit 0"). The output shows again on an empty line, Ctrl+R history still
// shows it over the draft, and a recalled line is an edit. Output that
// appears under a draft (a command run through prefix c, a failure report)
// shows until the next edit that changes the text, and a completion answer
// requested before it cannot cover it.
func TestPaletteEditHidesPreviousOutput(t *testing.T) {
	m := newTestModel(t, 100, 32)
	addExitedTab(t, m, "a")
	m.focusPalette = true
	old := executable
	t.Cleanup(func() { executable = old })
	executable = func() (string, error) { return "/bin/echo", nil }
	typeKeys := func(s string) {
		for _, r := range s {
			_, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
	}
	shows := func(s string) bool { return strings.Contains(m.View().Content, s) }
	submit := func() {
		t.Helper()
		_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		deadline := time.Now().Add(5 * time.Second)
		for m.console.Busy() && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if m.console.Busy() {
			t.Fatal("palette command did not finish")
		}
	}

	typeKeys("ROOT-HELP")
	submit()
	if !shows("ROOT-HELP") {
		t.Fatalf("the finished command's output is not shown:\n%s", m.View().Content)
	}

	// Completion for "box create " offers nothing: the area stays empty.
	typeKeys("box create ")
	_, _ = m.Update(suggestionsMsg{Generation: m.spaceGeneration, Shown: m.console.Shown(), Line: "box create "})
	if shows("ROOT-HELP") || m.menuShown() || m.desiredOverlayContent() != 0 {
		t.Fatalf("an empty completion shows the previous output (rows %d):\n%s", m.desiredOverlayContent(), m.View().Content)
	}
	// Nothing hidden scrolls.
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.scrollOff != 0 || shows("scroll -") {
		t.Fatalf("PgUp scrolled the hidden output: scrollOff %d", m.scrollOff)
	}

	// Ctrl+R shows the history over the draft; an editing key returns to
	// the draft and hides it again.
	_, _ = m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	if !m.consoleHistory || !shows("ROOT-HELP") {
		t.Fatalf("Ctrl+R history hidden by the draft:\n%s", m.View().Content)
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if line, _ := m.pal.Line(); m.consoleHistory || line != "box create" || shows("ROOT-HELP") {
		t.Fatalf("editing key: history=%t line %q:\n%s", m.consoleHistory, line, m.View().Content)
	}

	// Backspacing to an empty line shows the last command again.
	for range len("box create") {
		_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	if line, _ := m.pal.Line(); line != "" || !shows("ROOT-HELP") {
		t.Fatalf("empty line %q does not show the last command again:\n%s", line, m.View().Content)
	}

	// A recalled line is an edit: only the input row names the command.
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if line, _ := m.pal.Line(); line != "ROOT-HELP" || strings.Count(m.View().Content, "ROOT-HELP") != 1 {
		t.Fatalf("recalled line %q shows the previous output:\n%s", line, m.View().Content)
	}

	// prefix c: a command that runs in the palette brings the draft back at
	// once; its output shows under the draft until the next edit.
	prefixed(m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	typeKeys("PREFIX-RUN")
	submit()
	if line, _ := m.pal.Line(); line != "ROOT-HELP" || !shows("PREFIX-RUN") {
		t.Fatalf("the prefix c command's output is hidden under the returned draft %q:\n%s", line, m.View().Content)
	}
	// Delete at the end changes nothing, so the fresh output stays; a
	// character hides it.
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDelete})
	if line, _ := m.pal.Line(); line != "ROOT-HELP" || !shows("PREFIX-RUN") {
		t.Fatalf("Delete at the end of %q hid the fresh output:\n%s", line, m.View().Content)
	}
	_, _ = m.Update(tea.PasteMsg{Content: "x"}) // a paste is an edit over it
	if line, _ := m.pal.Line(); line != "ROOT-HELPx" || shows("PREFIX-RUN") {
		t.Fatalf("a paste kept the output under the draft %q:\n%s", line, m.View().Content)
	}

	// A failure reported under a draft shows until the next edit, and the
	// answer to a completion request made before it is dropped.
	_, _ = m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	requested := m.console.Shown()
	if !m.console.ShowError("ATTACH-FAILED") || !shows("ATTACH-FAILED") {
		t.Fatalf("a failure under a draft is hidden:\n%s", m.View().Content)
	}
	_, _ = m.Update(suggestionsMsg{Generation: m.spaceGeneration, Shown: requested, Line: "ROOT-HELPxy", Items: candidates("ROOT-HELPxyz")})
	if m.menuShown() || !shows("ATTACH-FAILED") {
		t.Fatalf("an answer requested before the failure covered it:\n%s", m.View().Content)
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if line, _ := m.pal.Line(); line != "ROOT-HELPx" || shows("ATTACH-FAILED") {
		t.Fatalf("an edit kept the failure under the draft %q:\n%s", line, m.View().Content)
	}
	_, _ = m.Update(suggestionsMsg{Generation: m.spaceGeneration, Shown: m.console.Shown(), Line: "ROOT-HELPx", Items: candidates("ROOT-HELPxyz")})
	if !m.menuShown() {
		t.Fatal("the answer to the edit after the failure was dropped")
	}
}

// Closing the palette clears the shown output; Ctrl+R then shows history.
// A paste leaves that view like a typed key, so no earlier command shows
// under the pasted line.
func TestPalettePasteLeavesHistory(t *testing.T) {
	m := newTestModel(t, 100, 32)
	runOverlayCommand(t, m, "printf", "ROOT-HELP\\r\\n")
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	_, _ = m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	_, _ = m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	if !m.consoleHistory || !strings.Contains(m.View().Content, "ROOT-HELP") {
		t.Fatalf("setup: Ctrl+R does not show the closed command in history:\n%s", m.View().Content)
	}
	_, _ = m.Update(tea.PasteMsg{Content: "box create "})
	_, _ = m.Update(suggestionsMsg{Generation: m.spaceGeneration, Shown: m.console.Shown(), Line: "box create "})
	if line, _ := m.pal.Line(); m.consoleHistory || line != "box create " || strings.Contains(m.View().Content, "ROOT-HELP") || m.desiredOverlayContent() != 0 {
		t.Fatalf("paste kept history=%t under %q:\n%s", m.consoleHistory, line, m.View().Content)
	}
}

// A key that leaves the draft as it was asks for no completion, so no menu
// replaces output that appeared under the draft. Completion here is the real
// request path with the binary's answer faked: "bo" completes to "box".
func TestPaletteNoOpKeyKeepsFreshOutput(t *testing.T) {
	m := newTestModel(t, 100, 32)
	addExitedTab(t, m, "a")
	m.focusPalette = true
	oldExe, oldComplete, oldAsk := executable, completeCLI, askCompletion
	t.Cleanup(func() { executable, completeCLI, askCompletion = oldExe, oldComplete, oldAsk })
	executable = func() (string, error) { return "/bin/echo", nil }
	completeCLI = func(context.Context, []string, string, []string) ([]yoloctlcli.Candidate, yoloctlcli.Directive) {
		return candidates("box"), 0
	}
	var pending []tea.Cmd
	asked := 0
	askCompletion = func(_ *model, request tea.Cmd) { asked++; pending = append(pending, request) }
	answer := func() { // the event loop delivers every outstanding answer
		for _, request := range pending {
			_, _ = m.Update(request())
		}
		pending = nil
	}
	typeKeys := func(s string) {
		for _, r := range s {
			_, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
	}
	shows := func(s string) bool { return strings.Contains(m.View().Content, s) }

	typeKeys("bo")
	answer()
	if !m.menuShown() || !shows("box") {
		t.Fatalf("setup: \"bo\" shows no menu:\n%s", m.View().Content)
	}
	prefixed(m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	typeKeys("VERSION-RUN")
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	deadline := time.Now().Add(5 * time.Second)
	for m.console.Busy() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	answer()
	if line, _ := m.pal.Line(); m.console.Busy() || line != "bo" || m.menuShown() || !shows("VERSION-RUN") {
		t.Fatalf("setup: the prefix c result is not shown under the draft %q:\n%s", line, m.View().Content)
	}

	// Delete at the end and Backspace at the start change nothing: no
	// request, and the result stays after the event loop runs.
	before := asked
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDelete})
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	answer()
	if line, _ := m.pal.Line(); asked != before || line != "bo" || m.menuShown() || !shows("VERSION-RUN") {
		t.Fatalf("a no-op key asked %d times; draft %q:\n%s", asked-before, line, m.View().Content)
	}

	// A real edit still asks, and its answer replaces the result.
	typeKeys("x")
	answer()
	if line, _ := m.pal.Line(); asked != before+1 || line != "box" || !m.menuShown() || shows("VERSION-RUN") {
		t.Fatalf("the edit to %q asked %d times:\n%s", line, asked-before, m.View().Content)
	}
}
