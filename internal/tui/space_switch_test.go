package tui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"os"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/operator"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/termio"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
	"github.com/leogr/yoloctl/internal/tui/term"
	"github.com/leogr/yoloctl/internal/tui/yoloctlcli"
)

// Actual subprocess fixtures for all three TUI launch paths. They never invoke
// the real controller or alter a running space.
func TestMain(m *testing.M) {
	if os.Getenv("YOLOCTL_SPACE_SWITCH_CHILD") == "1" {
		space := os.Getenv(config.SpaceEnv)
		if len(os.Args) > 1 && os.Args[1] == "__complete" {
			fmt.Printf("%s\tpinned space\n:4\n", space)
		} else {
			fmt.Println("pin=" + space)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func selectSpaceForTest(m *model, name string) {
	m.openSpacePicker()
	m.receiveSpaceList(spaceListMsg{generation: m.spaceGeneration, request: m.spacePicker.request, names: []string{name, m.ctxSpace}})
	m.spacePicker.row = slices.Index(m.spacePicker.names, name)
	m.chooseSpace()
}

func TestSpaceSwitchConfirmationAndWarnings(t *testing.T) {
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	global, _ := config.LoadContext()
	global.Space = "global"
	if err := global.Save(); err != nil {
		t.Fatal(err)
	}
	m, _, _, _ := confirmModel(t)
	m.ctxSpace = "first"
	m.pal.Insert("draft command")
	m.pal.Keep(m.pal.Stash())
	m.pal.Insert("keep this")
	selectSpaceForTest(m, "second")
	if m.confirm == nil || m.confirm.close || m.ctxSpace != "first" {
		t.Fatal("must confirm with Keep selected")
	}
	text := ansi.Strip(m.View().Content)
	for _, s := range []string{"Switch from first to second", "2 panes", "drafts", "unsaved changes", "unknown", "Close tabs & switch"} {
		if !strings.Contains(text, s) {
			t.Fatalf("missing %q in %q", s, text)
		}
	}
	_, _ = m.Update(key(tea.KeyEnter, 0, ""))
	if len(m.tabs.Tabs) != 1 || m.ctxSpace != "first" {
		t.Fatal("default Enter changed the space")
	}
	if line, _ := m.pal.Line(); line != "keep this" {
		t.Fatal("cancel lost the palette draft")
	}
	selectSpaceForTest(m, "second")
	_, _ = m.Update(key('y', 0, "y"))
	if len(m.tabs.Tabs) != 0 || m.ctxSpace != "second" || !m.focusPalette || m.confirm != nil {
		t.Fatal("confirmed switch did not close all tabs")
	}
	if line, _ := m.pal.Line(); line != "" {
		t.Fatal("switch retained draft")
	}
	globalAfter, err := config.LoadContext()
	if err != nil || globalAfter.Space != "global" {
		t.Fatal("switch changed global context", globalAfter, err)
	}
	m.pal.HistPrev()
	if line, _ := m.pal.Line(); line != "draft command" {
		t.Fatal("history was lost")
	}
}

func TestSpaceSwitchRevalidatesAllPaneIdentities(t *testing.T) {
	m, _, a, _ := confirmModel(t)
	m.ctxSpace = "a"
	selectSpaceForTest(m, "b")
	added := paneSession(t, 10, 40, "sleep", "30")
	m.tabs.Add(testTab(added, "chat", false)) // even an otherwise-safe new view requires review
	m.acceptConfirm()
	if m.confirm == nil || m.confirm.close || len(m.tabs.Tabs) != 2 || m.ctxSpace != "a" {
		t.Fatal("closed an unreviewed pane")
	}
	if !strings.Contains(m.confirm.notice, "review") {
		t.Fatal("missing changed-list notice")
	}
	// Replacement of a session in the SAME Pane is also a new client.
	replacement := paneSession(t, 10, 40, "sleep", "30")
	old := a.Session
	a.Session = replacement
	m.acceptConfirm()
	if m.ctxSpace != "a" || m.confirm == nil {
		t.Fatal("same-pane session replacement escaped revalidation")
	}
	a.Session = old
	m.acceptConfirm() // refresh after restoring the original fixture
	m.acceptConfirm()
	if m.ctxSpace != "b" || len(m.tabs.Tabs) != 0 {
		t.Fatal("reviewed set did not close")
	}
}

func TestSpaceSwitchEmptySameBusyAndBound(t *testing.T) {
	m := newTestModel(t, 80, 24)
	m.ctxSpace = "a"
	selectSpaceForTest(m, "a")
	if m.confirm != nil || m.spacePicker != nil {
		t.Fatal("same space must be a no-op")
	}
	selectSpaceForTest(m, "b")
	if m.confirm == nil {
		t.Fatal("even zero tabs must confirm")
	}
	m.cancelConfirm()
	started, err := m.console.Run(nil, "", "sh", "-c", "read line")
	if !started || err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.console.Kill)
	m.openSpacePicker()
	if m.spacePicker != nil || !strings.Contains(shownNotice(m), "finish") {
		t.Fatal("busy console allowed a switch")
	}
	m.console.Kill()
	m.notice = nil
	m.spaceBound = true
	m.openSpacePicker()
	if m.spacePicker != nil {
		t.Fatal("operator/native client offered cross-space choices")
	}
}

func TestSpacePickerKeyboardMouseAndLateLists(t *testing.T) {
	m := newTestModel(t, 52, 18)
	m.ctxSpace = "a"
	_, _ = m.Update(ctrlQ)
	_, _ = m.Update(key('s', 0, "s"))
	if m.spacePicker == nil {
		t.Fatal("prefix-s did not open picker")
	}
	req := m.spacePicker.request
	m.receiveSpaceList(spaceListMsg{request: req, names: []string{"c", "a", "b"}})
	o := m.spacePickerOverlay()
	text := ansi.Strip(m.View().Content)
	if !strings.Contains(text, "Switch space") || o.x+o.w != m.width || m.tabTop()+o.y != 1 {
		t.Fatal("dropdown not below top-right chip")
	}
	_, _ = m.Update(tea.PasteMsg{Content: "do not type"})
	if line, _ := m.pal.Line(); line != "" {
		t.Fatal("paste leaked through picker")
	}
	_, _ = m.Update(key(tea.KeyDown, 0, ""))
	_, _ = m.Update(key(tea.KeyEnter, 0, ""))
	if m.confirm == nil || m.confirm.space != "b" {
		t.Fatal("keyboard list selection")
	}
	m.cancelConfirm()
	m.openSpacePicker()
	current := m.spacePicker.request
	m.receiveSpaceList(spaceListMsg{request: req, names: []string{"stale"}})
	if !m.spacePicker.loading {
		t.Fatal("old list repopulated reopened menu")
	}
	m.receiveSpaceList(spaceListMsg{request: current, names: []string{"a", "b"}})
	o = m.spacePickerOverlay()
	_, top, _ := o.frame.layout(o.w, o.h)
	_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: o.x + 3, Y: m.tabTop() + o.y + top + 1})
	if m.confirm == nil || m.confirm.space != "b" {
		t.Fatal("mouse list selection")
	}
	m.cancelConfirm()
	_, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: 5, Y: 5})
	_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: m.width - 3, Y: 0})
	if m.spacePicker == nil {
		t.Fatal("chip click did not open")
	}
	_, _ = m.Update(key(tea.KeyEscape, 0, ""))
	for _, size := range [][2]int{{10, 4}, {25, 9}, {80, 24}} {
		_, _ = m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.openSpacePicker()
		m.receiveSpaceList(spaceListMsg{request: m.spacePicker.request, names: []string{"a", "b"}})
		o = m.spacePickerOverlay()
		if o.x < 0 || o.x+o.w > m.width || m.tabTop()+o.y+o.h > m.height {
			t.Fatal("picker outside viewport")
		}
		_ = m.View()
		m.closeSpacePicker()
	}
}

func TestSpaceSwitchRejectsLateCompletionRequestsAndExits(t *testing.T) {
	m := newTestModel(t, 80, 24)
	m.ctxSpace = "a"
	s := paneSession(t, 10, 40, "sleep", "30")
	m.tabs.Add(testTab(s, "space view", false))
	selectSpaceForTest(m, "b")
	m.acceptConfirm()
	m.pal.Insert("box ")
	_, _ = m.Update(suggestionsMsg{Generation: 0, Line: "box ", Items: candidates("old")})
	if len(m.sugg) > 0 {
		t.Fatal("old space completion applied")
	}
	// Including a nil source: early-launch requests are scoped by generation.
	_, _ = m.Update(tabRequestMsg{req: termio.TabRequest{Op: "attach", Space: "a", Box: "coder"}})
	if len(m.tabs.Tabs) > 0 {
		t.Fatal("old request reopened a tab")
	}
	_, _ = m.Update(tabExitMsg{session: s})
	if len(m.tabs.Tabs) > 0 || m.ctxSpace != "b" {
		t.Fatal("old exit changed the new context")
	}
	_, _ = m.Update(suggestionsMsg{Generation: m.spaceGeneration, Line: "box ", Items: candidates("new")})
	if len(m.sugg) != 1 || m.sugg[0].Value != "new" {
		t.Fatal("new generation reply lost")
	}
}

func TestSpaceSwitchDetachesEstablishedHolder(t *testing.T) {
	sock, _ := startManagedHolder(t)
	view := managedView(t, sock, 20, 80)
	waitFor(t, view.HolderView)
	m := newTestModel(t, 100, 26)
	m.ctxSpace = "a"
	m.tabs.Add(testTab(view, "box attach coder --space a", false))
	selectSpaceForTest(m, "b")
	m.acceptConfirm()
	// Reattach to the SAME fixture holder: local switch did not stop it.
	again := managedView(t, sock, 20, 80)
	waitFor(t, again.HolderView)
}

func TestSpacePinActualChildrenAndCompletion(t *testing.T) {
	t.Setenv("YOLOCTL_SPACE_SWITCH_CHILD", "1")
	t.Setenv(config.SpaceEnv, "inherited")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	old := executable
	executable = func() (string, error) { return self, nil }
	t.Cleanup(func() { executable = old })
	m := newTestModel(t, 80, 24)
	m.ctxSpace = "first"
	p := m.startPane([]string{"space", "view"}, 10, 60)
	if p == nil {
		t.Fatal(m.status)
	}
	t.Cleanup(p.Session.Close)
	waitFor(t, func() bool { return strings.Contains(p.Session.GridText(), "pin=first") })
	m.dispatch([]string{"box", "list"})
	waitFor(t, func() bool { return !m.console.Busy() })
	if !strings.Contains(m.console.GridText(), "pin=first") {
		t.Fatal("console environment differs")
	}
	items, _ := yoloctlcli.Complete(context.Background(), nil, "", m.childEnv())
	if len(items) != 1 || items[0].Value != "first" {
		t.Fatalf("completion environment differs: %v", items)
	}
	m.pal.Insert("f")
	m.completePalette()
	if line, _ := m.pal.Line(); line != "first " {
		t.Fatal("synchronous completion missed pin", line)
	}
	for _, nav := range []bool{false, true} {
		m.ctxSpace = "first"
		request := m.suggestionsCommand("", nil, "", nav)
		m.ctxSpace = "changed-after-launch"
		msg := request().(suggestionsMsg)
		if len(msg.Items) != 1 || msg.Items[0].Value != "first" || msg.Nav != nav {
			t.Fatal("async completion did not capture launch env", msg)
		}
	}
	other := newTestModel(t, 80, 24)
	other.ctxSpace = "second"
	if !slices.Contains(other.childEnv(), config.SpaceEnv+"=second") || os.Getenv(config.SpaceEnv) != "inherited" {
		t.Fatal("models mutated each other/global env")
	}
	m.standalone = true
	if !slices.Contains(m.childEnv(), config.SpaceEnv+"=inherited") {
		t.Fatal("standalone override changed inherited selection")
	}
}

func TestSpacePinOperatorBoundary(t *testing.T) {
	t.Setenv(config.SpaceEnv, "untrusted")
	old := tuiOperatorClient
	tuiOperatorClient = func() (*operator.Client, error) { return &operator.Client{Space: "bound"}, nil }
	t.Cleanup(func() { tuiOperatorClient = old })
	m := newTestModel(t, 80, 24)
	if err := m.initSpace(); err != nil {
		t.Fatal(err)
	}
	if m.ctxSpace != "bound" || !m.spaceBound {
		t.Fatal("env overrode endpoint metadata")
	}
	m.openSpacePicker()
	if m.spacePicker != nil {
		t.Fatal("bound client can switch")
	}
}

func TestSpacePickerLoadingEmptyErrorAndBrowserPriority(t *testing.T) {
	m := newTestModel(t, 80, 24)
	m.ctxSpace = "a"
	for _, failure := range []error{nil, errors.New("fixture inventory unavailable")} {
		m.openSpacePicker()
		if !strings.Contains(ansi.Strip(m.View().Content), "Loading spaces") {
			t.Fatal("no loading state")
		}
		m.receiveSpaceList(spaceListMsg{request: m.spacePicker.request, err: failure})
		want := "No spaces found"
		if failure != nil {
			want = "Cannot list spaces"
		}
		if !strings.Contains(ansi.Strip(m.View().Content), want) {
			t.Fatal("missing inventory state", want)
		}
		m.chooseSpace()
		if m.confirm != nil {
			t.Fatal("empty/failed inventory chose a target")
		}
		m.closeSpacePicker()
	}
	view := paneSession(t, 10, 40, "sleep", "30")
	m.tabs.Add(testTab(view, "chat", false))
	m.openSpacePicker()
	m.browserPrompts = map[*term.Session]browserPrompt{view: {surface: view, request: session.BrowserRequest{ID: "1", URL: "https://example.test"}, done: make(chan struct{})}}
	m.showBrowserDialog()
	if m.spacePicker != nil || m.browserDialog == nil {
		t.Fatal("browser did not preempt picker")
	}
	m.openSpacePicker()
	if m.spacePicker != nil {
		t.Fatal("picker opened over browser modal")
	}
}

func TestSpacePickerPreservesGestureOwner(t *testing.T) {
	m, _, right, _, readInput := twoCapturePanes(t)
	m.ctxSpace = "a"
	r, top := right.Rect(), m.tabTop()
	_, _ = m.Update(click(r.X+1, top+1))
	m.openSpacePicker()
	_, _ = m.Update(releaseAt(r.X+1, top+1))
	if m.spacePicker == nil {
		t.Fatal("release answered the picker")
	}
	if _, ok := m.mouseOwners[tea.MouseLeft]; ok {
		t.Fatal("old gesture did not finish")
	}
	m.closeSpacePicker()
	if got := readInput(); !strings.HasPrefix(got, "\x1b[<0;2;2M") || !strings.HasSuffix(got, "m") || strings.Count(got, "\x1b") != 2 {
		t.Fatalf("gesture retargeted: %q", got)
	}
}

func TestSpaceSwitchConfirmationMouseAction(t *testing.T) {
	m, _, _, _ := confirmModel(t)
	m.ctxSpace = "a"
	selectSpaceForTest(m, "b")
	o := m.confirmOverlay()
	rows := o.frame.render(o.w, o.h, "")
	for y, line := range rows {
		plain := ansi.Strip(line)
		if x := strings.Index(plain, "[ Close tabs & switch ]"); x >= 0 {
			_, _ = m.Update(click(o.x+ansi.StringWidth(plain[:x])+2, m.tabTop()+o.y+y))
			if m.ctxSpace != "b" || len(m.tabs.Tabs) != 0 {
				t.Fatal("new confirmation action did not switch")
			}
			return
		}
	}
	t.Fatal("switch action not visible")
}

func TestSpacePickerCancelKeepsNewTabDraft(t *testing.T) {
	m := newTestModel(t, 80, 24)
	m.ctxSpace = "a"
	m.pal.Insert("saved draft")
	m.startNewTab()
	intent := m.newTab
	m.openSpacePicker()
	m.closeSpacePicker()
	if m.newTab != intent {
		t.Fatal("picker cancellation ended the new-tab intent")
	}
	if line, _ := m.pal.Line(); line != "" {
		t.Fatal("picker cancellation changed the empty command draft", line)
	}
	selectSpaceForTest(m, "b")
	m.cancelConfirm()
	if m.newTab != intent {
		t.Fatal("confirmation cancellation ended the new-tab intent")
	}
	m.cancelNewTab()
	if line, _ := m.pal.Line(); line != "saved draft" {
		t.Fatal("saved draft was lost", line)
	}
}

func TestSpaceReviewKeepsFocusAcrossValidTabRequests(t *testing.T) {
	t.Setenv("YOLOCTL_SPACE_SWITCH_CHILD", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	saved := executable
	executable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { executable = saved })
	m, tab, a, _ := confirmModel(t)
	m.ctxSpace = "a"
	selectSpaceForTest(m, "b")
	_, _ = m.Update(tabRequestMsg{generation: m.spaceGeneration, from: a.Session, req: termio.TabRequest{Op: "attach", Space: "a", Box: "coder"}})
	for _, t2 := range m.tabs.Tabs {
		for _, p := range t2.Layout.Panes() {
			t.Cleanup(p.Session.Close)
		}
	}
	if m.tabs.Current() != tab || !m.focusPalette || m.confirm == nil {
		t.Fatal("async attach stole modal focus")
	}
	m.acceptConfirm()
	if m.confirm == nil || m.ctxSpace != "a" || len(m.tabs.Tabs) != 2 {
		t.Fatal("async addition was not reviewed")
	}
	m.cancelConfirm()
	if m.tabs.Current() != tab || !m.focusPalette {
		t.Fatal("cancel did not retain original focus")
	}
}

func TestSpaceReviewCancelRetainsFocusedPaneOnSameTabReuse(t *testing.T) {
	for _, confirm := range []bool{false, true} {
		t.Run(fmt.Sprint("confirmation=", confirm), func(t *testing.T) {
			m, tab, a, b := confirmModel(t)
			m.ctxSpace = "a"
			a.Command = "space view"
			b.Command = "yoloctl box attach coder --space a"
			tab.Layout.SetFocus(a)
			m.focusPalette = false
			if confirm {
				selectSpaceForTest(m, "b")
			} else {
				m.openSpacePicker()
			}
			_, _ = m.Update(tabRequestMsg{generation: m.spaceGeneration, from: a.Session, req: termio.TabRequest{Op: "attach", Space: "a", Box: "coder"}})
			if (confirm && m.confirm == nil) || (!confirm && m.spacePicker == nil) || tab.Focused() != a || m.focusPalette {
				t.Fatal("queued attach stole pane focus during modal review")
			}
			if confirm {
				m.cancelConfirm()
			} else {
				m.closeSpacePicker()
			}
			if m.tabs.Current() != tab || tab.Focused() != a || m.focusPalette {
				t.Fatal("cancel returned to the wrong pane")
			}
		})
	}
}

func TestSpacePickerLabels(t *testing.T) {
	var snapshots strings.Builder
	for _, width := range []int{80, 44, 24, 10} {
		m := newTestModel(t, width, 40)
		m.ctxSpace = "yolo"
		m.spacePicker = &spacePicker{names: []string{"ftry", "yolo"}, row: 1}
		o := m.spacePickerOverlay()
		footer := o.frame.Footer
		if strings.Contains(footer, "Esc") {
			t.Fatalf("obsolete escape hint: %q", footer)
		}
		if width >= 24 && strings.Join(strings.Fields(footer), " ") != "Switches only this TUI yoloctl space use changes the default" {
			t.Fatalf("footer words lost or split at width %d: %q", width, footer)
		}
		inner, _, _ := o.frame.layout(o.w, o.h)
		if inner >= ansi.StringWidth("yoloctl space use") && !slices.ContainsFunc(strings.Split(footer, "\n"), func(row string) bool {
			return strings.Contains(row, "yoloctl space use")
		}) {
			t.Fatalf("command split at width %d: %q", width, footer)
		}
		fmt.Fprintf(&snapshots, "width=%d\n", width)
		for _, row := range o.frame.render(o.w, o.h, "") {
			if ansi.StringWidth(row) > width {
				t.Fatalf("row exceeds width %d: %q", width, row)
			}
			fmt.Fprintln(&snapshots, ansi.Strip(row))
		}
	}
	path := "testdata/space-picker.golden"
	if *updateMenu {
		if err := os.WriteFile(path, []byte(snapshots.String()), 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if snapshots.String() != string(want) {
		t.Fatalf("space picker differs from golden:\n%s", snapshots.String())
	}
}

func TestSpacePickerFooterStyle(t *testing.T) {
	var snapshots strings.Builder
	for _, dark := range []bool{true, false} {
		for _, width := range []int{80, 44, 24, 16, 10} {
			m := newTestModel(t, width, 40)
			m.bgDetected, m.bgIsDark = true, dark
			if dark {
				m.background = color.Black
			} else {
				m.background = color.White
			}
			m.ctxSpace = "yolo"
			m.spacePicker = &spacePicker{names: []string{"ftry", "yolo"}, row: 1}
			o := m.spacePickerOverlay()
			theme := spaceview.NewTheme(dark, m.background, nil)
			expected := uv.NewScreenBuffer(2, 1)
			uv.NewStyledString(spaceview.StyleText("h", spaceview.Style{FG: theme.Dim})+spaceview.StyleText("c", spaceview.Style{FG: theme.Brand})).Draw(expected, expected.Bounds())
			rows := o.frame.render(o.w, o.h, m.surfaceBg())
			count := len(strings.Split(o.frame.Footer, "\n"))
			start := len(rows) - 1 - count
			fmt.Fprintf(&snapshots, "dark=%v width=%d\n", dark, width)
			codeSeen := false
			for _, row := range rows[start : len(rows)-1] {
				fmt.Fprintf(&snapshots, "%q\n", row)
				buf := uv.NewScreenBuffer(o.w, 1)
				uv.NewStyledString(row).Draw(buf, buf.Bounds())
				for x := 2; x < o.w-2; x++ {
					cell := buf.CellAt(x, 0)
					if strings.TrimSpace(cell.Content) == "" {
						continue
					}
					if cell.Style.Attrs&uv.AttrBold != 0 {
						t.Fatalf("footer is bold: %q", row)
					}
					fg := cell.Style.Fg
					if fg == nil {
						t.Fatalf("footer foreground missing: %q", row)
					}
					r, g, b, a := fg.RGBA()
					hr, hg, hb, ha := expected.CellAt(0, 0).Style.Fg.RGBA()
					cr, cg, cb, ca := expected.CellAt(1, 0).Style.Fg.RGBA()
					if r == cr && g == cg && b == cb && a == ca {
						codeSeen = true
					} else if r != hr || g != hg || b != hb || a != ha {
						t.Fatalf("unexpected footer colour: %v", fg)
					}
				}
			}
			if !codeSeen {
				t.Fatalf("command has no code colour at width%d", width)
			}
			if width >= 24 {
				if !strings.Contains(strings.Join(rows, "\n"), spaceview.StyleText("Switches only this TUI", spaceview.Style{FG: theme.Dim})) && width >= 44 {
					t.Fatal("first footer clause is not normal hint text")
				}
				styled := spaceview.StyleText("yoloctl space use", spaceview.Style{FG: theme.Brand})
				if !strings.Contains(strings.Join(rows, "\n"), styled) {
					t.Fatalf("whole command lacks inline-code style: %q", rows)
				}
				if width >= 44 && !strings.Contains(strings.Join(rows, "\n"), spaceview.StyleText(" changes the default", spaceview.Style{FG: theme.Dim})) {
					t.Fatal("prose following command did not restore hint style")
				}

			}
		}
	}
	path := "testdata/space-picker-footer-style.golden"
	if *updateMenu {
		if err := os.WriteFile(path, []byte(snapshots.String()), 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if snapshots.String() != string(want) {
		t.Fatalf("footer style differs from golden:\n%s", snapshots.String())
	}
}
