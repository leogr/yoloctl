package tui

import (
	"fmt"
	"image/color"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/chatimage"
	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/operator"
	"github.com/leogr/yoloctl/internal/termio"
	"github.com/leogr/yoloctl/internal/tui/term"
	"github.com/leogr/yoloctl/internal/tui/yoloctlcli"
	"github.com/mattn/go-runewidth"
)

// newTestModel builds a model without a tea.Program (wake degrades to
// a no-op), console sized via a WindowSizeMsg like the real startup.
func newTestModel(t testing.TB, width, height int) *model {
	t.Helper()
	m := &model{pending: &atomic.Bool{}, focusPalette: true, cfg: &config.TUI{}, imageLedger: chatimage.NewLedger()}
	m.imageRegistry = chatimage.NewRegistry(m.imageLedger)
	m.console = term.NewConsole(24, 80, 0, m.wake)
	t.Cleanup(func() { _ = m.console.Close() })
	_, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return m
}

func TestPaletteTypingWithLockModifiers(t *testing.T) {
	m := newTestModel(t, 80, 24)
	_, _ = m.Update(tea.KeyPressMsg{Code: 'a', Text: "A", Mod: tea.ModCapsLock})
	_, _ = m.Update(tea.KeyPressMsg{Code: 'b', Text: "b", Mod: tea.ModNumLock})
	if line, _ := m.pal.Line(); line != "Ab" {
		t.Fatalf("palette = %q, want Ab", line)
	}
}

func TestHostColorsReachConsole(t *testing.T) {
	m := newTestModel(t, 80, 24)
	_, _ = m.Update(tea.ForegroundColorMsg{Color: color.White})
	_, _ = m.Update(tea.BackgroundColorMsg{Color: color.Black})
	if m.foreground != color.White || m.background != color.Black || !m.bgDetected {
		t.Fatal("host defaults not retained for new tabs")
	}
	// A nil timeout response must not erase measured colors.
	_, _ = m.Update(tea.BackgroundColorMsg{})
	if m.background != color.Black {
		t.Fatal("timeout erased background")
	}
}

func TestSGRMousePreservesModifiers(t *testing.T) {
	for _, tc := range []struct {
		mouse           tea.Mouse
		motion, release bool
		want            string
	}{
		{tea.Mouse{Button: tea.MouseLeft, X: 3, Y: barRows + 1, Mod: tea.ModShift | tea.ModAlt | tea.ModCtrl}, false, false, "\x1b[<28;4;2M"},
		{tea.Mouse{Button: tea.MouseLeft, X: 3, Y: barRows + 1, Mod: tea.ModCtrl}, true, false, "\x1b[<48;4;2M"},
		{tea.Mouse{Button: tea.MouseLeft, X: 3, Y: barRows + 1, Mod: tea.ModAlt}, false, true, "\x1b[<8;4;2m"},
		{tea.Mouse{Button: tea.MouseWheelLeft, X: 3, Y: barRows + 1}, false, false, "\x1b[<66;4;2M"},
		{tea.Mouse{Button: tea.MouseWheelRight, X: 3, Y: barRows + 1}, false, false, "\x1b[<67;4;2M"},
	} {
		if got := testSGRMouse(t, tc.mouse, tc.motion, tc.release, 20); got != tc.want {
			t.Errorf("mouse %#v: %q, want %q", tc.mouse, got, tc.want)
		}
	}
}

func TestPasteGoesToRunningOverlay(t *testing.T) {
	m := newTestModel(t, 80, 24)
	started, err := m.console.Run(nil, "", "sh", "-c", "read answer; printf 'answer:%s' \"$answer\"")
	if !started || err != nil {
		t.Fatalf("start confirmation: %v, %v", started, err)
	}
	t.Cleanup(m.console.Kill)
	_, _ = m.Update(tea.PasteMsg{Content: "yes"})
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	deadline := time.Now().Add(5 * time.Second)
	for m.console.Busy() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if m.console.Busy() {
		t.Fatal("pasted confirmation never reached child")
	}
	if line, _ := m.pal.Line(); line != "" {
		t.Fatalf("paste entered palette: %q", line)
	}
	if got := m.console.GridText(); !strings.Contains(got, "answer:yes") {
		t.Fatalf("child output: %q", got)
	}
}

func TestOversizedPasteIsReportedAndPaletteStillResponds(t *testing.T) {
	m := newTestModel(t, 80, 24)
	s, err := term.Start(term.Options{Argv: []string{"sh", "-c", "read answer"}, Rows: 8, Cols: 40})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	m.tabs.Add(testTab(s, "input test", false))
	m.focusPalette = false
	_, _ = m.Update(tea.PasteMsg{Content: strings.Repeat("x", 5<<20)})
	if !strings.Contains(m.status, "not queued") {
		t.Fatalf("rejected paste was silent: %q", m.status)
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	if !m.focusPalette {
		t.Fatal("palette could not take focus after rejected paste")
	}
}

func TestCustomPaletteKeyDoubleTap(t *testing.T) {
	m := newTestModel(t, 80, 24)
	m.cfg.PaletteKey = "ctrl+x"
	s, err := term.Start(term.Options{Argv: []string{"sh", "-c", "stty raw -echo; printf ready; dd bs=1 count=1 2>/dev/null | od -An -tu1"}, Rows: 20, Cols: 80})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	m.tabs.Add(testTab(s, "", false))
	m.focusPalette = false
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(s.GridText(), "ready") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(s.GridText(), "ready") {
		t.Fatal("child did not reach raw mode")
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	_, _ = m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	if err := s.Wait(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if got := s.GridText(); !strings.Contains(got, "24") {
		t.Fatalf("double ctrl+x should send byte 24: %q", got)
	}
}

func wheel(btn tea.MouseButton) tea.MouseWheelMsg {
	return tea.MouseWheelMsg{Button: btn, X: 4, Y: 4}
}

func runOverlayCommand(t *testing.T, m *model, argv ...string) {
	t.Helper()
	started, err := m.console.Run(nil, "", argv...)
	if !started || err != nil {
		t.Fatalf("console run: %v %v", started, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for m.console.Busy() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if m.console.Busy() {
		t.Fatal("overlay command did not finish")
	}
}

// The overlay chain: command output overflowing the overlay buffer →
// wheel-up while the palette has focus scrolls it; the live overlay
// shows the output tail; unfocus hides the overlay while preserving history.
func TestOverlayWheelScrollAndHide(t *testing.T) {
	m := newTestModel(t, 60, 20)
	runOverlayCommand(t, m, "seq", "1", "40")

	live := m.View().Content
	if !strings.Contains(live, "40") {
		t.Errorf("overlay does not show output tail:\n%s", live)
	}
	if len(m.console.Scrollback()) == 0 {
		t.Fatal("no scrollback after overflowing output")
	}

	// One line per wheel event (high-resolution wheels emit bursts).
	_, _ = m.Update(wheel(tea.MouseWheelUp))
	_, _ = m.Update(wheel(tea.MouseWheelUp))
	if m.scrollOff != 2 {
		t.Fatalf("scrollOff after two wheel-ups = %d, want 2", m.scrollOff)
	}
	scrolled := m.View().Content
	if scrolled == live {
		t.Error("view unchanged after wheel-up")
	}
	if !strings.Contains(scrolled, "scroll -2") {
		t.Errorf("scroll indicator missing:\n%s", scrolled)
	}
	_, _ = m.Update(wheel(tea.MouseWheelDown))
	_, _ = m.Update(wheel(tea.MouseWheelDown))
	if m.scrollOff != 0 {
		t.Errorf("scrollOff after wheel-downs = %d, want 0", m.scrollOff)
	}

	// Esc hides the overlay; refocusing shows the retained history.
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.focusPalette {
		t.Fatal("Esc should unfocus the palette")
	}
	hidden := m.View().Content
	if strings.Contains(hidden, "40") {
		t.Errorf("overlay content still visible after unfocus:\n%s", hidden)
	}
	if len(m.console.Scrollback()) == 0 {
		t.Error("history lost on unfocus")
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	if !m.focusPalette || !strings.Contains(m.View().Content, "40") {
		t.Fatal("retained console did not reappear")
	}
}

// The console shows the last command alone; Ctrl+R toggles the history of
// every retained command, newest last, and Esc or a new command leaves it.
func TestOverlayShowsOneCommandAndCtrlRShowsHistory(t *testing.T) {
	m := newTestModel(t, 60, 20)
	runOverlayCommand(t, m, "printf", "FIRST-RUN\\r\\n")
	runOverlayCommand(t, m, "printf", "SECOND-RUN\\r\\n")
	view := m.View().Content
	if !strings.Contains(view, "SECOND-RUN") || strings.Contains(view, "FIRST-RUN") || !strings.Contains(view, "^R history") {
		t.Fatalf("console does not show the last command alone:\n%s", view)
	}
	ctrlR := tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}
	_, _ = m.Update(ctrlR)
	view = m.View().Content
	if !m.consoleHistory || !strings.Contains(view, "FIRST-RUN") || !strings.Contains(view, "SECOND-RUN") ||
		strings.Index(view, "FIRST-RUN") > strings.Index(view, "SECOND-RUN") || !strings.Contains(view, "console history") {
		t.Fatalf("Ctrl+R does not show the history, newest last:\n%s", view)
	}
	_, _ = m.Update(ctrlR)
	if m.consoleHistory || strings.Contains(m.View().Content, "FIRST-RUN") {
		t.Fatal("Ctrl+R again does not return to the last command")
	}
	_, _ = m.Update(ctrlR)
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.consoleHistory {
		t.Fatal("Esc kept the history view")
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	_, _ = m.Update(ctrlR)
	old := executable
	t.Cleanup(func() { executable = old })
	executable = func() (string, error) { return "/bin/echo", nil }
	_ = m.dispatch([]string{"THIRD-RUN"})
	if m.consoleHistory {
		t.Fatal("a new command did not leave the history view")
	}
}

// The console grid stays at the max overlay-content height and keeps
// evicting across runs: the grid never grows past the configured max.
func TestConsoleGridStaysBoundedAcrossRuns(t *testing.T) {
	m := newTestModel(t, 200, 50)
	for range 3 {
		runOverlayCommand(t, m, "seq", "1", "45")
	}
	if rows := m.console.Rows(); rows != m.overlayMaxContentRows() {
		t.Errorf("console grid = %d rows, want max overlay content %d", rows, m.overlayMaxContentRows())
	}
	if sb := len(m.console.Scrollback()); sb == 0 {
		t.Error("scrollback empty after overflowing commands")
	}
}

func TestResolveHeightSpec(t *testing.T) {
	cases := []struct {
		spec        string
		height, def int
		want        int
	}{
		{"", 40, 7, 7},         // empty -> default
		{"10", 40, 7, 10},      // absolute rows
		{"75%", 40, 7, 30},     // percent of height
		{"50%", 20, 7, 10},     // percent of height
		{"0", 40, 7, 7},        // non-positive -> default
		{"-3", 40, 7, 7},       // negative -> default
		{"0%", 40, 7, 7},       // non-positive percent -> default
		{"nonsense", 40, 7, 7}, // unparseable -> default
	}
	for _, c := range cases {
		if got := resolveHeightSpec(c.spec, c.height, c.def); got != c.want {
			t.Errorf("resolveHeightSpec(%q,%d,%d) = %d, want %d", c.spec, c.height, c.def, got, c.want)
		}
	}
}

// overlayBounds enforces the hard rails (area >= 1 row, area <= 75% of
// the terminal height) and resolves rows-or-% config within them.
func TestOverlayBoundsRails(t *testing.T) {
	m := newTestModel(t, 80, 40) // viewRows 38; 75% of 40 = 30

	// Defaults: min area 1 -> 0 content rows (collapses); max area 30 -> 29.
	m.cfg.PaletteMinHeight, m.cfg.PaletteMaxHeight = "", ""
	if minC, maxC := m.overlayBounds(); minC != 0 || maxC != 29 {
		t.Errorf("default bounds = (%d,%d), want (0,29)", minC, maxC)
	}

	// Over the ceiling is clamped: max capped at 75% of the height.
	m.cfg.PaletteMinHeight, m.cfg.PaletteMaxHeight = "1", "200%"
	if minC, maxC := m.overlayBounds(); minC != 0 || maxC != 29 {
		t.Errorf("clamped bounds = (%d,%d), want (0,29)", minC, maxC)
	}

	// Absolute rows and a percentage within the rails pass through.
	m.cfg.PaletteMinHeight, m.cfg.PaletteMaxHeight = "8", "50%"
	if minC, maxC := m.overlayBounds(); minC != 7 || maxC != 19 {
		t.Errorf("explicit bounds = (%d,%d), want (7,19)", minC, maxC)
	}
}

// The overlay's display height fits its content plus one trailing blank
// row (the gap before the tab strip): command output shows its used rows
// + 1, a long one saturates at the max, and a suggestion list sizes to
// its length + 1 within the bounds.
func TestOverlayFitsContent(t *testing.T) {
	m := newTestModel(t, 80, 40) // default bounds (0, 29)
	_, maxC := m.overlayBounds()

	// Short output: used rows + the trailing blank, comfortably below max.
	runOverlayCommand(t, m, "printf", "one\\r\\ntwo\\r\\n")
	if got, want := m.overlayContentRows(), m.consoleContentRows()+1; got != want || got >= maxC {
		t.Errorf("short output overlay = %d rows, want %d (< max %d)", got, want, maxC)
	}

	// Long output saturates at the max.
	runOverlayCommand(t, m, "seq", "1", "100")
	if got := m.overlayContentRows(); got != maxC {
		t.Errorf("long output overlay = %d rows, want max %d", got, maxC)
	}

	// Suggestions size to the list plus the trailing blank (non-empty line).
	m.pal.Insert("x")
	m.sugg = candidates("a", "b", "c", "d", "e", "f")
	if got := m.overlayContentRows(); got != 7 {
		t.Errorf("6 suggestions overlay = %d rows, want 7 (6 + trailing blank)", got)
	}
}

// Bar layout: brand on row 0; the tab strip sits on row 1 when the
// palette is unfocused, and is pushed below the overlay while it's open
// with content (the push-down effect).
func TestBarRendersBrandAndTabs(t *testing.T) {
	m := newTestModel(t, 80, 20)
	runOverlayCommand(t, m, "printf", "hello\\r\\n") // open the overlay with output
	lines := strings.Split(m.View().Content, "\n")
	if !strings.Contains(lines[0], "yoloctl") {
		t.Errorf("row 0 missing brand: %q", lines[0])
	}
	stripAt := 1 + m.overlayContentRows() // palette focused: pushed below the overlay
	if stripAt <= 1 {
		t.Fatalf("overlay did not open with content (stripAt=%d)", stripAt)
	}
	if !strings.Contains(lines[stripAt], "(no tabs)") {
		t.Errorf("tab strip not below the open overlay (row %d): %q", stripAt, lines[stripAt])
	}

	// Esc hides the overlay and drops the strip to row 1, retaining history.
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	lines = strings.Split(m.View().Content, "\n")
	if !strings.Contains(lines[1], "(no tabs)") {
		t.Errorf("tab strip not on row 1 when overlay closed: %q", lines[1])
	}
}

func TestBgSGRParsing(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"235", "\x1b[48;5;235m", true},
		{"0", "\x1b[48;5;0m", true},
		{"255", "\x1b[48;5;255m", true},
		{"#1c1c1c", "\x1b[48;2;28;28;28m", true},
		{"#ffffff", "\x1b[48;2;255;255;255m", true},
		{"256", "", false},    // out of 256-color range
		{"-1", "", false},     // negative
		{"#12345", "", false}, // short hex
		{"nonsense", "", false},
	}
	for _, c := range cases {
		got, ok := bgSGR(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("bgSGR(%q) = %q,%v; want %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// paintRow must re-assert the bg after every interior reset (a bare
// cReset clears it) and end with a clean reset; "" is a pass-through.
func TestPaintRowReassertsBg(t *testing.T) {
	bg := "\x1b[48;5;235m"
	got := paintRow("a"+cReset+"b", bg)
	want := bg + "a" + cReset + bg + "b" + cReset
	if got != want {
		t.Errorf("paintRow = %q, want %q", got, want)
	}
	if paintRow("x"+cReset, "") != "x"+cReset {
		t.Error("paintRow with empty bg must pass through unchanged")
	}
}

// surfaceBg resolves the palette_bg spec: keywords, explicit values,
// and the auto path before/after OSC 11 detection (nudged per polarity).
func TestSurfaceBgModes(t *testing.T) {
	m := newTestModel(t, 80, 20)

	m.cfg.PaletteBg = "off"
	if got := m.surfaceBg(); got != "" {
		t.Errorf(`palette_bg "off" = %q, want ""`, got)
	}

	m.cfg.PaletteBg = "123"
	if got := m.surfaceBg(); got != "\x1b[48;5;123m" {
		t.Errorf(`palette_bg "123" = %q`, got)
	}

	// Auto, before any reply: assume a near-black surface.
	m.cfg.PaletteBg = ""
	if got := m.surfaceBg(); got != "\x1b[48;5;235m" {
		t.Errorf("auto (undetected) = %q, want the dark fallback", got)
	}

	// Auto, dark terminal detected → nudge channels lighter.
	_, _ = m.Update(tea.BackgroundColorMsg{Color: color.RGBA{R: 0, G: 0, B: 0, A: 0xff}})
	if !m.bgDetected || !m.bgIsDark {
		t.Fatalf("black bg not detected as dark (detected=%v dark=%v)", m.bgDetected, m.bgIsDark)
	}
	if got := m.surfaceBg(); got != "\x1b[48;2;38;38;38m" {
		t.Errorf("auto (black) = %q, want a lighter nudge", got)
	}

	// Auto, light terminal detected → nudge channels darker.
	_, _ = m.Update(tea.BackgroundColorMsg{Color: color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}})
	if m.bgIsDark {
		t.Fatal("white bg detected as dark")
	}
	if got := m.surfaceBg(); got != "\x1b[48;2;217;217;217m" {
		t.Errorf("auto (white) = %q, want a darker nudge", got)
	}
}

// "off" must leave no background SGR (48;...) anywhere in the frame;
// an explicit index must paint one into the chrome.
func TestSurfaceBgRenderToggle(t *testing.T) {
	m := newTestModel(t, 60, 20)

	m.cfg.PaletteBg = "off"
	if strings.Contains(m.View().Content, "\x1b[48;") {
		t.Error(`palette_bg "off" still emitted a background SGR`)
	}

	m.cfg.PaletteBg = "235"
	view := m.View().Content
	if !strings.Contains(view, "\x1b[48;5;235m") {
		t.Error("enabled surface did not paint the chrome background")
	}
	// Row 0 (the palette input) carries it.
	if row0 := strings.SplitN(view, "\n", 2)[0]; !strings.Contains(row0, "\x1b[48;5;235m") {
		t.Errorf("palette input row missing surface bg: %q", row0)
	}
}

// Every command closes with an exit-code line: green ✓ on success, red
// ✗ with the code on failure.
func TestExitCodeLine(t *testing.T) {
	m := newTestModel(t, 80, 20)

	runOverlayCommand(t, m, "true")
	if got := m.console.GridText(); !strings.Contains(got, "✓ exit 0") {
		t.Errorf("success exit line missing:\n%s", got)
	}

	runOverlayCommand(t, m, "sh", "-c", "exit 3")
	if got := m.console.GridText(); !strings.Contains(got, "✗ exit 3") {
		t.Errorf("failure exit line missing:\n%s", got)
	}
}

// opensLiveSurface is the full palette-vs-tab mapping: tabs only for
// argv that hands the user an ongoing terminal context (attach/exec,
// VM terminal, login, $EDITOR, --follow stream); everything else — help,
// list/show, config mutations, lifecycle, and the FINITE forms of
// logs — stays in the palette. Aliases mirror their canonical
// command.
func TestOpensLiveSurface(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		// Tabs: agent/session attach (root `attach` + its `box`
		// canonical commands).
		{"removed run", []string{"run", "--agent", "claude"}, false},
		{"box run with agent help past --", []string{"box", "run", "--agent", "claude", "--", "claude", "--help"}, false},
		{"box run", []string{"box", "run", "--agent", "claude"}, false},
		{"box attach", []string{"box", "attach", "coder"}, true},
		{"root attach alias", []string{"attach", "coder"}, true},

		// Tabs: sandbox/VM terminal.
		{"box exec", []string{"box", "exec", "coder", "--", "bash"}, true},
		{"space ssh", []string{"space", "ssh"}, true},
		{"space ssh with name", []string{"space", "ssh", "prod"}, true},
		{"space shell", []string{"space", "shell"}, true},

		// Tabs: the live space view, unless help was asked for.
		{"space view", []string{"space", "view"}, true},
		{"space view with name and interval", []string{"space", "view", "prod", "--interval", "2s"}, true},
		{"sp alias view", []string{"sp", "view"}, true},
		{"space view help", []string{"space", "view", "--help"}, false},
		{"space view -h", []string{"space", "view", "-h"}, false},
		{"space view help disabled", []string{"space", "view", "--help=false"}, true},

		// Tabs: stdin-reading secret set.
		{"account secret set reads stdin", []string{"account", "secret", "set", "default", "ANTHROPIC_API_KEY"}, true},

		// Tabs: editor-backed commands.
		{"box edit", []string{"box", "edit", "coder"}, true},
		{"space edit", []string{"space", "edit", "dev"}, true},
		{"space edit help", []string{"space", "edit", "--help"}, false},
		{"sp edit", []string{"sp", "edit", "dev"}, true},
		{"space set", []string{"space", "set", "dev", "--memory", "auto"}, false},
		{"account edit", []string{"account", "edit", "default"}, true},
		// Catalog edit opens $EDITOR in a tab.
		{"agent edit", []string{"agent", "edit", "my-claude"}, true},
		{"feature edit", []string{"feature", "edit", "my-github"}, true},
		{"capability edit", []string{"capability", "edit", "my-net"}, true},

		// Tabs: streaming (--follow / -f) forms.
		{"box logs --follow", []string{"box", "logs", "coder", "--follow"}, true},
		{"box logs -f", []string{"box", "logs", "coder", "-f"}, true},
		{"space logs --follow", []string{"space", "logs", "--follow"}, true},
		{"space logs name -f", []string{"space", "logs", "prod", "-f"}, true},

		// Aliases mirror their canonical command.
		{"sp alias ssh", []string{"sp", "ssh"}, true},
		{"sp alias logs -f", []string{"sp", "logs", "-f"}, true},
		{"sp alias finite logs", []string{"sp", "logs"}, false},

		// Palette: help in every spelling (no more help-tab special case).
		{"top-level --help", []string{"--help"}, false},
		{"top-level -h", []string{"-h"}, false},
		{"group --help", []string{"space", "--help"}, false},
		{"leaf -h", []string{"box", "create", "-h"}, false},
		{"help subcommand", []string{"help"}, false},
		{"help subcommand with arg", []string{"help", "box"}, false},

		// Palette: finite forms of the streaming-capable commands.
		{"box logs finite", []string{"box", "logs", "coder"}, false},
		{"space logs finite", []string{"space", "logs"}, false},
		{"space logs name finite", []string{"space", "logs", "prod"}, false},

		// Tabs: lifecycle verbs auto-attach by default…
		{"box create", []string{"box", "create", "coder"}, true},
		{"box copy", []string{"box", "copy", "coder", "coder-2"}, true},
		{"box restart", []string{"box", "restart", "coder"}, true},
		{"box start", []string{"box", "start", "coder"}, true},
		{"box recreate", []string{"box", "recreate", "coder"}, true},
		{"box recreate -f is --force, not a suppressor", []string{"box", "recreate", "coder", "-f"}, true},
		{"box recreate -fh=false leaves help disabled", []string{"box", "recreate", "coder", "-fh=false"}, true},
		{"box create --no-attach=false", []string{"box", "create", "coder", "--no-attach=false"}, true},
		{"box create --no-attach overridden back on (last wins)", []string{"box", "create", "coder", "--no-attach", "--no-attach=false"}, true},
		{"box create --manifest-only overridden off (last wins)", []string{"box", "create", "coder", "--manifest-only", "--manifest-only=false"}, true},

		// …palette when the argv suppresses the attach.
		{"box create --no-attach", []string{"box", "create", "coder", "--no-attach"}, false},
		{"box copy --no-attach", []string{"box", "copy", "coder", "coder-2", "--no-attach"}, false},
		{"box start --no-attach", []string{"box", "start", "coder", "--no-attach"}, false},
		{"box restart --no-attach", []string{"box", "restart", "coder", "--no-attach"}, false},
		{"box recreate --no-attach", []string{"box", "recreate", "coder", "--no-attach"}, false},
		{"box recreate clustered -fh requests help", []string{"box", "recreate", "coder", "-fh"}, false},
		{"box recreate -hf=false still requests help", []string{"box", "recreate", "coder", "-hf=false"}, false},
		{"box create --manifest-only", []string{"box", "create", "coder", "--manifest-only"}, false},
		{"box copy --manifest-only", []string{"box", "copy", "coder", "coder-2", "--manifest-only"}, false},
		{"box create --no-attach off then on (last wins)", []string{"box", "create", "coder", "--no-attach=false", "--no-attach"}, false},
		{"box create --no-attach=false with --manifest-only", []string{"box", "create", "coder", "--no-attach=false", "--manifest-only"}, false},
		{"box create invalid bool stays palette (cobra flag error)", []string{"box", "create", "coder", "--no-attach=nope"}, false},
		{"box create invalid bool is latched past a later valid value", []string{"box", "create", "coder", "--no-attach=nope", "--no-attach=false"}, false},

		// Palette: finite list/show/lifecycle/config, and bare groups.
		{"box list", []string{"box", "list"}, false},
		{"box show", []string{"box", "show", "coder"}, false},
		{"box inspect", []string{"box", "inspect", "coder"}, false},
		{"box destroy", []string{"box", "destroy", "coder"}, false},
		{"box stop", []string{"box", "stop", "coder"}, false},
		{"removed ps", []string{"ps"}, false},
		{"box ls", []string{"box", "ls"}, false},
		{"space list", []string{"space", "list"}, false},
		{"space use", []string{"space", "use"}, false},
		{"space mount list", []string{"space", "mount", "list"}, false},
		{"account auth import is finite", []string{"account", "auth", "import", "default", "claude"}, false},
		{"account auth status", []string{"account", "auth", "status", "default", "claude"}, false},
		{"credential list", []string{"credential", "list"}, false},
		{"account list", []string{"account", "list"}, false},
		{"account show", []string{"account", "show", "default"}, false},
		{"account create", []string{"account", "create", "personal"}, false},
		{"account destroy", []string{"account", "destroy", "personal"}, false},
		{"account secret set --from-env stays finite", []string{"account", "secret", "set", "default", "ANTHROPIC_API_KEY", "--from-env"}, false},
		{"account secret list", []string{"account", "secret", "list", "default"}, false},
		{"version", []string{"version"}, false},
		{"bare auth", []string{"auth"}, false},
		{"bare space", []string{"space"}, false},
		{"bare box", []string{"box"}, false},
		{"bare account", []string{"account"}, false},
		// Catalog non-edit verbs stay in the palette (finite).
		{"agent ls", []string{"agent", "ls"}, false},
		{"agent show", []string{"agent", "show", "claude"}, false},
		{"agent copy", []string{"agent", "copy", "claude", "my-claude"}, false},
		{"agent destroy", []string{"agent", "destroy", "my-claude"}, false},
		{"agent versions still finite", []string{"agent", "versions"}, false},
		{"bare agent", []string{"agent"}, false},
		{"feature ls", []string{"feature", "ls"}, false},
		{"feature show", []string{"feature", "show", "github"}, false},
		{"feature copy", []string{"feature", "copy", "github", "my-github"}, false},
		{"feature destroy", []string{"feature", "destroy", "my-github"}, false},
		{"bare feature", []string{"feature"}, false},
		{"capability ls", []string{"capability", "ls"}, false},
		{"capability show", []string{"capability", "show", "network.github"}, false},
		{"capability copy", []string{"capability", "copy", "network.github", "my-net"}, false},
		{"capability destroy", []string{"capability", "destroy", "my-net"}, false},
		{"bare capability", []string{"capability"}, false},
		{"empty", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := opensLiveSurface(tc.args); got != tc.want {
				t.Errorf("opensLiveSurface(%q) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

// A focused tab whose child exited shows the close hint and dismisses
// on Enter (a plain close, not a palette submit); closing the last tab
// hands focus back to the palette.
func TestExitedTabClosesOnEnter(t *testing.T) {
	m := newTestModel(t, 80, 20)
	s, err := term.Start(term.Options{
		Argv: []string{"true"}, Rows: m.viewRows(), Cols: 80, OnData: m.wake,
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	defer s.Close()
	_ = s.Wait(5 * time.Second) // child exits; the pump echoes status + hint
	m.tabs.Add(testTab(s, "yoloctl true", true))
	m.focusPalette = false

	if !strings.Contains(s.GridText(), "Press Enter to close this tab") {
		t.Errorf("exited tab missing close hint:\n%s", s.GridText())
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.tabs.Current() != nil {
		t.Errorf("Enter did not close the exited tab; %d tab(s) remain", len(m.tabs.Tabs))
	}
	if !m.focusPalette {
		t.Error("closing the last tab should return focus to the palette")
	}
}

var sgrRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripSGR(s string) string { return sgrRE.ReplaceAllString(s, "") }

// The tab strip carries a leading margin before the first tab. It must
// always be present, and it lives in the strip — not baked into the tab
// title (which stays the child/command title verbatim).
func TestTabStripLeadingMargin(t *testing.T) {
	m := newTestModel(t, 80, 20)
	s, err := term.Start(term.Options{
		Argv: []string{"true"}, Rows: m.viewRows(), Cols: 80, OnData: m.wake,
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	defer s.Close()
	_ = s.Wait(5 * time.Second)
	m.tabs.Add(testTab(s, "yoloctl true", true))

	// The title is untouched: the margin is not prepended to it.
	if got := m.tabs.Tabs[0].Title(); got != "true" {
		t.Fatalf("tab title = %q, want %q (margin must not leak into the title)", got, "true")
	}
	// In the rendered strip the circled number sits at column 2: the
	// always-present 1-column strip margin + the tab's 1-column left padding.
	visible := stripSGR(m.tabStrip())
	if i := strings.Index(visible, circledNumber(1)); i != 2 {
		t.Errorf("first tab number at column %d, want 2 (1 strip margin + 1 tab pad):\n%q", i, visible)
	}
	// The margin must carry the strip's non-tab style — underlined, on the
	// palette surface bg — even though the only tab is active and its own
	// cell carries neither. So the strip opens on the surface bg, and the
	// run before the label is underlined (both come only from the margin).
	raw := m.tabStrip()
	if !strings.HasPrefix(raw, m.surfaceBg()) {
		t.Errorf("leading margin must open on the palette surface bg; strip = %q", raw)
	}
	if pre := raw[:strings.Index(raw, circledNumber(1))]; !strings.Contains(pre, cUnderline) {
		t.Errorf("leading margin must be underlined: %q", pre)
	}
}

// Typing lets suggestions take the display without erasing native history;
// backspacing to an empty line resurfaces earlier command output.
func TestTypingPreservesConsoleHistory(t *testing.T) {
	m := newTestModel(t, 80, 20)
	runOverlayCommand(t, m, "printf", "OUTPUT\\r\\n")
	if !strings.Contains(m.console.GridText(), "OUTPUT") {
		t.Fatal("precondition: output not in console")
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"}) // a printable key
	if !strings.Contains(m.console.GridText(), "OUTPUT") {
		t.Fatal("typing erased history")
	}
	m.sugg = candidates("candidate")
	if strings.Contains(m.View().Content, "OUTPUT") || !strings.Contains(m.View().Content, "candidate") {
		t.Fatal("suggestions did not replace the display")
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if !strings.Contains(m.View().Content, "OUTPUT") {
		t.Fatal("empty palette did not restore history")
	}
}

// Empty palette collapses to just the input line; once content shows, a
// trailing blank row separates it from the tab strip below.
func TestOverlayTrailingBlank(t *testing.T) {
	m := newTestModel(t, 80, 20) // focused, empty line, no command
	if got := m.overlayContentRows(); got != 0 {
		t.Errorf("empty overlay = %d rows, want 0 (collapsed)", got)
	}

	runOverlayCommand(t, m, "printf", "HELLO\\r\\n")
	lines := strings.Split(m.View().Content, "\n")
	stripAt := 1 + m.overlayContentRows() // the tab strip row
	// Content starts right below the input row (no leading blank)...
	if !strings.Contains(lines[1], "printf") {
		t.Errorf("content not directly below the input row: %q", lines[1])
	}
	// ...and the row just above the tab strip is a blank separator.
	if gap := lines[stripAt-1]; strings.Contains(gap, "printf") || strings.Contains(gap, "HELLO") || strings.Contains(gap, "exit") {
		t.Errorf("no blank separator above the tab strip: %q", gap)
	}
	if !strings.Contains(lines[stripAt], "(no tabs)") {
		t.Errorf("tab strip not at row %d: %q", stripAt, lines[stripAt])
	}
}

// PgUp/PgDown page the overlay's scrollback while the palette is focused.
func TestPageScroll(t *testing.T) {
	m := newTestModel(t, 80, 20)
	runOverlayCommand(t, m, "seq", "1", "100") // overflow -> scrollback
	if len(m.console.Scrollback()) == 0 {
		t.Fatal("precondition: no scrollback")
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	up := m.scrollOff
	if up <= 1 {
		t.Errorf("PgUp scrolled %d rows, want a page (>1)", up)
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.scrollOff >= up {
		t.Errorf("PgDown did not reduce scrollOff (%d -> %d)", up, m.scrollOff)
	}
}

// startOverlayCommand launches a command in the console without waiting
// for it (used with commands that block on input).
func startOverlayCommand(t *testing.T, m *model, argv ...string) {
	t.Helper()
	started, err := m.console.Run(nil, "", argv...)
	if !started || err != nil {
		t.Fatalf("console run: %v %v", started, err)
	}
}

func waitConsoleIdle(t *testing.T, m *model) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for m.console.Busy() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if m.console.Busy() {
		t.Fatal("console command did not finish")
	}
}

// waitConsoleRows blocks until the console has at least n content rows —
// used to wait for a command's own output below the prompt-echo row
// (more robust than matching text, which the prompt echo also carries).
func waitConsoleRows(t *testing.T, m *model, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for m.consoleGridRows() < n && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if m.consoleGridRows() < n {
		t.Fatalf("console did not reach %d rows (command not producing output)", n)
	}
}

// A running command owns the palette's input: keystrokes are fed to its
// stdin (the PTY echoes and the command consumes them).
func TestRunningCommandStdin(t *testing.T) {
	m := newTestModel(t, 80, 20)
	startOverlayCommand(t, m, "sh", "-c", "read x; echo got:$x")
	if !m.console.Busy() {
		t.Fatal("command not running")
	}
	for _, r := range "hi" {
		_, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitConsoleIdle(t, m)
	if got := m.console.GridText(); !strings.Contains(got, "got:hi") {
		t.Errorf("stdin not delivered to the command:\n%s", got)
	}
}

// Ctrl+C interrupts a running command (SIGINT -> conventional exit 130).
func TestRunningCommandCtrlCInterrupts(t *testing.T) {
	m := newTestModel(t, 80, 20)
	startOverlayCommand(t, m, "sleep", "5")
	_, _ = m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	waitConsoleIdle(t, m)
	if got := m.console.GridText(); !strings.Contains(got, "exit 130") {
		t.Errorf("Ctrl+C did not interrupt (want exit 130):\n%s", got)
	}
}

// A second Ctrl+C force-kills a command that ignores SIGINT (SIGKILL ->
// exit 137).
func TestRunningCommandDoubleCtrlCKills(t *testing.T) {
	m := newTestModel(t, 80, 20)
	startOverlayCommand(t, m, "sh", "-c", "trap '' INT; echo ready; read x")
	// Wait until the command's own output appears below the prompt-echo
	// row (gridRows >= 2): the trap runs before that echo, so by now the
	// first ^C can't race shell startup. (Don't match the echoed text —
	// the prompt echo contains the whole command line.)
	waitConsoleRows(t, m, 2)
	_, _ = m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}) // SIGINT (ignored)
	_, _ = m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}) // force-kill
	waitConsoleIdle(t, m)
	if got := m.console.GridText(); !strings.Contains(got, "exit 137") {
		t.Errorf("double Ctrl+C did not force-kill (want exit 137):\n%s", got)
	}
}

// Detaching (Ctrl+Y) from a running command keeps it running and
// preserves its output while busy.
func TestRunningCommandDetachPreservesOutput(t *testing.T) {
	m := newTestModel(t, 80, 20)
	startOverlayCommand(t, m, "sh", "-c", "echo live; read x")
	waitConsoleRows(t, m, 2)                                      // prompt echo + the command's output line
	_, _ = m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl}) // detach
	if m.focusPalette {
		t.Error("Ctrl+Y did not detach from the running command")
	}
	if !m.console.Busy() {
		t.Error("command should keep running after detach")
	}
	if m.consoleContentRows() == 0 {
		t.Error("output cleared on detach (must be preserved while running)")
	}
	m.console.Kill() // cleanup
	waitConsoleIdle(t, m)
}

// startSpinner runs exactly one tick loop: the first call starts it, a
// second (while still spinning) does not stack a second loop.
func TestSpinnerLoopGuard(t *testing.T) {
	m := newTestModel(t, 80, 20)
	if cmd := m.startSpinner(); cmd == nil || !m.spinning {
		t.Fatal("startSpinner should start a loop and set spinning")
	}
	if cmd := m.startSpinner(); cmd != nil {
		t.Error("startSpinner must not start a second concurrent loop")
	}
}

// The left brand becomes a fixed-width wave that advances on each tick while
// a command is busy and stops once it's idle.
func TestSpinnerAnimatesWhileBusy(t *testing.T) {
	m := newTestModel(t, 80, 20)
	startOverlayCommand(t, m, "sh", "-c", "read x") // blocks on stdin
	waitConsoleRows(t, m, 1)

	row0 := strings.SplitN(m.View().Content, "\n", 2)[0]
	if !strings.Contains(row0, "running") {
		t.Errorf("busy legend missing 'running': %q", row0)
	}
	if !strings.HasPrefix(ansi.Strip(row0), "❯ "+spinnerFrames[m.spinnerFrame]+" ") {
		t.Errorf("spinner did not replace the brand: %q", row0)
	}

	for range spinnerFrames {
		before := m.spinnerFrame
		_, cmd := m.Update(spinnerTickMsg{})
		if m.spinnerFrame == before || cmd == nil {
			t.Fatal("spinner must advance and reschedule while busy")
		}
		row := ansi.Strip(m.inputLine())
		if ansi.StringWidth(row) != m.width || row[len("❯ ")+len("yoloctl"):] != ansi.Strip(row0)[len("❯ ")+len("yoloctl"):] {
			t.Fatalf("wave moved the command or right-hand status: %q", row)
		}
	}

	m.console.Kill()
	waitConsoleIdle(t, m)
	if _, cmd := m.Update(spinnerTickMsg{}); cmd != nil {
		t.Error("spinner tick must stop when idle")
	}
	if !strings.HasPrefix(ansi.Strip(m.inputLine()), "❯ yoloctl ") {
		t.Fatal("idle prompt did not restore the brand")
	}
}

// Context remains bold and at the far right, including under width pressure.
func TestContextChipInBar(t *testing.T) {
	for _, width := range []int{24, 40, 100} {
		m := newTestModel(t, width, 20)
		m.ctxSpace = "prod"
		row := strings.SplitN(m.View().Content, "\n", 2)[0]
		if got := ansi.Strip(row); !strings.HasSuffix(got, "prod ▾ ") || ansi.StringWidth(got) != width {
			t.Fatalf("space is not right aligned at width %d: %q", width, got)
		}
		if !strings.Contains(row, cBold+"prod ▾") {
			t.Fatalf("space is not bold: %q", row)
		}
	}
}

func TestPaletteCommandEchoOmitsWorkingDirectory(t *testing.T) {
	if got := ansi.Strip(promptLine([]string{"box", "list"})); got != "❯ yoloctl box list" {
		t.Fatalf("command echo = %q", got)
	}
}

// Each model resolves once; changing the global default never retargets it.
func TestTUIPinsStartupSpace(t *testing.T) {
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	t.Setenv(config.SpaceEnv, "")
	old := tuiOperatorClient
	tuiOperatorClient = func() (*operator.Client, error) { return nil, nil }
	t.Cleanup(func() { tuiOperatorClient = old })
	m := newTestModel(t, 80, 24)
	if err := m.initSpace(); err != nil {
		t.Fatal(err)
	}
	c, _ := config.LoadContext()
	c.Space = "prod"
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if m.ctxSpace != "default" {
		t.Fatal(m.ctxSpace)
	}
	other := newTestModel(t, 80, 24)
	if err := other.initSpace(); err != nil {
		t.Fatal(err)
	}
	if other.ctxSpace != "prod" {
		t.Fatal(other.ctxSpace)
	}
}

// candidates builds a value-only suggestion list (no descriptions).
func candidates(vals ...string) []yoloctlcli.Candidate {
	cs := make([]yoloctlcli.Candidate, len(vals))
	for i, v := range vals {
		cs[i] = yoloctlcli.Candidate{Value: v}
	}
	return cs
}

// Up/Down move the selection while the menu is up (clamped at the ends);
// with no menu they fall through to history.
func TestSuggestionMenuArrowSelection(t *testing.T) {
	m := newTestModel(t, 80, 20)
	m.pal.Insert("box a")
	m.sugg = candidates("apply", "attach")

	down := func() { _, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) }
	up := func() { _, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp}) }

	down()
	if m.suggSel != 1 {
		t.Fatalf("Down → suggSel %d, want 1", m.suggSel)
	}
	down() // clamps at the last item
	if m.suggSel != 1 {
		t.Fatalf("Down at end → suggSel %d, want 1", m.suggSel)
	}
	up()
	up() // clamps at the first item
	if m.suggSel != 0 {
		t.Fatalf("Up to top → suggSel %d, want 0", m.suggSel)
	}

	// No menu: Up browses history instead of moving a (nonexistent) selection.
	m2 := newTestModel(t, 80, 20)
	m2.pal.Insert("space ls")
	m2.pal.Submit() // record history, clear the line
	_, _ = m2.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if line, _ := m2.pal.Line(); line != "space ls" {
		t.Fatalf("Up with no menu = %q, want history recall %q", line, "space ls")
	}
}

// Special case: ↓ on an empty palette reveals the whole command menu (a
// shell's TAB-on-empty) and arrives in menu-steering. Modelled here by the
// async result the keypress would produce (the fetch needs an event loop).
func TestEmptyDownRevealsAllOptions(t *testing.T) {
	m := newTestModel(t, 80, 20) // empty, focused

	_, _ = m.Update(suggestionsMsg{
		Line:  "",
		Items: candidates("agent", "box", "image", "space"),
		Nav:   true,
	})
	if len(m.sugg) != 4 || !m.suggNav {
		t.Fatalf("reveal: sugg=%d suggNav=%v, want 4 + steering", len(m.sugg), m.suggNav)
	}

	// The menu renders even though the line is empty.
	out := m.View().Content
	if !strings.Contains(out, "box") || !strings.Contains(out, "space") {
		t.Errorf("empty-line menu not shown:\n%s", out)
	}

	// And the arrows now steer it (Enter would pick, per suggNav).
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.suggSel != 1 {
		t.Errorf("Down after reveal: suggSel=%d, want 1", m.suggSel)
	}
}

// The inline ghost is the selected suggestion's tail past the typed token
// — and only while mid-token (a trailing space shows the menu but no ghost).
func TestSuggestionGhost(t *testing.T) {
	m := newTestModel(t, 80, 20)
	m.pal.Insert("box att")
	m.sugg = candidates("attach")
	if got := m.ghostSuffix(); got != "ach" {
		t.Fatalf("ghostSuffix = %q, want %q", got, "ach")
	}
	row0 := strings.SplitN(m.View().Content, "\n", 2)[0]
	if !strings.Contains(row0, cDim+"ach"+cReset) {
		t.Errorf("input row missing dim ghost %q:\n%q", "ach", row0)
	}

	// Trailing space → empty token → no ghost (menu still lists options).
	m.pal.Insert(" ")
	if got := m.ghostSuffix(); got != "" {
		t.Errorf("ghostSuffix after space = %q, want empty", got)
	}
}

// While steering the menu with the arrows, Enter picks the selection
// (like TAB) rather than submitting — and only after an arrow press.
func TestEnterPicksWhileNavigating(t *testing.T) {
	m := newTestModel(t, 80, 20)
	m.pal.Insert("box a")
	m.sugg = candidates("apply", "attach")

	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // select "attach", enter nav
	if !m.suggNav {
		t.Fatal("Down should enter menu-steering")
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // picks, does not submit
	if line, _ := m.pal.Line(); line != "box attach " {
		t.Fatalf("Enter while navigating = %q, want pick %q", line, "box attach ")
	}
	if m.suggNav {
		t.Error("picking should exit menu-steering")
	}

	// Without an arrow press the menu is just shown (suggNav stays false),
	// so Enter submits the typed line rather than picking.
	m2 := newTestModel(t, 80, 20)
	m2.pal.Insert("space ls")
	m2.sugg = candidates("ls")
	if m2.suggNav {
		t.Fatal("a shown menu must not imply navigation")
	}
}

// TAB inserts the selected suggestion (the ghosted one) + a space, so
// what you see is what you get.
func TestTabAcceptsSelectedSuggestion(t *testing.T) {
	m := newTestModel(t, 80, 20)
	m.pal.Insert("box a")
	m.sugg = candidates("apply", "attach")
	m.suggSel = 1 // selected "attach"
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if line, _ := m.pal.Line(); line != "box attach " {
		t.Fatalf("TAB applied = %q, want %q", line, "box attach ")
	}
}

// TAB on a directory-prefix candidate (path completion arrives with the
// NoSpace directive and a trailing "/") keeps the token open — no space —
// so the next TAB completes deeper into the path.
func TestTabKeepsPathTokenOpen(t *testing.T) {
	m := newTestModel(t, 80, 20)
	m.pal.Insert("box create --workdir /tm")
	m.sugg = candidates("/tmp/proj/")
	m.suggNoSpace = true
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if line, _ := m.pal.Line(); line != "box create --workdir /tmp/proj/" {
		t.Fatalf("TAB applied = %q, want %q", line, "box create --workdir /tmp/proj/")
	}
	// Either signal alone suffices: a "/"-suffixed value keeps the token
	// open even if the directive bit was lost (defensive redundancy).
	if got := completionTerminator("/tmp/proj/", false); got != "" {
		t.Errorf("completionTerminator(dir, false) = %q, want \"\"", got)
	}
	if got := completionTerminator("attach", false); got != " " {
		t.Errorf("completionTerminator(value, false) = %q, want single space", got)
	}
}

// In a mixed path list (files + dirs) cobra's response-wide NoSpace reflects
// only the directories, so it must not govern the file candidates: accepting
// a FILE terminates its token with a space, accepting a DIR keeps it open.
func TestTabMixedPathListIsPerCandidate(t *testing.T) {
	mixed := candidates("/w/auth.json", "/w/src/")
	if noSpaceGoverns(mixed, 6 /* NoSpace|NoFileComp */) {
		t.Fatal("noSpaceGoverns(mixed path list) = true, want false")
	}
	if !noSpaceGoverns(candidates("part-a", "part-b"), 6) {
		t.Fatal("noSpaceGoverns(homogeneous no-slash list) = false, want true")
	}

	m := newTestModel(t, 80, 20)
	m.pal.Insert("account auth import default claude --file /w/a")
	m.sugg = mixed
	m.suggNoSpace = noSpaceGoverns(mixed, 6)
	m.suggSel = 0 // the file
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if line, _ := m.pal.Line(); line != "account auth import default claude --file /w/auth.json " {
		t.Fatalf("TAB on file = %q, want trailing space", line)
	}
}

// The menu shows cobra's descriptions inline and marks the selected row.
func TestSuggestionDescriptionsRendered(t *testing.T) {
	m := newTestModel(t, 80, 20)
	m.pal.Insert("box a")
	m.sugg = []yoloctlcli.Candidate{
		{Value: "attach", Desc: "Attach to the agent session"},
		{Value: "create", Desc: "Create a confined agentbox"},
	}
	out := m.View().Content
	if !strings.Contains(out, "attach") || !strings.Contains(out, "Attach to the agent session") {
		t.Errorf("menu missing value+description:\n%s", out)
	}
	if !strings.Contains(out, "▸") {
		t.Errorf("menu missing selected-row marker:\n%s", out)
	}
}

// visibleLine windows a single-line buffer into avail columns, keeping the
// cursor visible and preferring to reveal the text to its left (the
// rightmost slice — typing at the end), reporting hidden-text on each edge.
func TestVisibleLineWindow(t *testing.T) {
	for _, c := range []struct {
		name          string
		line          string
		cursor, avail int
		text          string
		col           int
		left, right   bool
	}{
		{"fits whole", "hello", 5, 10, "hello", 5, false, false},
		{"home of long", "abcdefghij", 0, 5, "abcde", 0, false, true},
		{"end of long", "abcdefghij", 10, 5, "ghij", 4, true, false},
		{"mid of long", "abcdefghij", 6, 5, "cdefg", 4, true, true},
		{"exact fit, cursor mid", "abcde", 2, 5, "abcde", 2, false, false},
		{"exact fit, cursor end scrolls one", "abcde", 5, 5, "bcde", 4, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			text, col, left, right := visibleLine(c.line, c.cursor, c.avail)
			if text != c.text || col != c.col || left != c.left || right != c.right {
				t.Errorf("visibleLine(%q,%d,%d) = (%q,%d,%v,%v), want (%q,%d,%v,%v)",
					c.line, c.cursor, c.avail, text, col, left, right, c.text, c.col, c.left, c.right)
			}
		})
	}
}

// stampEdges overlays the ‹ › overflow markers onto the window's edge
// cells, but the cursor cell always wins (a marker on the cursor's own
// column is skipped) so the cursor is never hidden.
func TestStampEdges(t *testing.T) {
	for _, c := range []struct {
		name        string
		text        string
		col         int
		left, right bool
		want        string
	}{
		{"left marker", "abcd", 3, true, false, "‹bcd"},
		{"right marker", "abcd", 0, false, true, "abc›"},
		{"both markers", "abcd", 2, true, true, "‹bc›"},
		{"right skipped on cursor", "abcd", 3, false, true, "abcd"},
		{"left skipped on cursor", "abcd", 0, true, false, "abcd"},
		{"none", "abcd", 2, false, false, "abcd"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := stampEdges(c.text, c.col, c.left, c.right); got != c.want {
				t.Errorf("stampEdges(%q,%d,%v,%v) = %q, want %q", c.text, c.col, c.left, c.right, got, c.want)
			}
		})
	}
}

// A palette line longer than the editable region slides to keep the cursor
// visible: the row never overflows the width, the rightmost slice (with the
// cursor) shows behind a ‹ marker, the head is hidden, and the host cursor
// stays on-screen. Home then shows the head behind a › marker.
func TestInputLineScrollsToCursor(t *testing.T) {
	m := newTestModel(t, 40, 20)
	long := "box create coder --agent claude --workdir /srv/projects/very-deep/repository"
	m.pal.Insert(long)

	vis := stripSGR(strings.SplitN(m.View().Content, "\n", 2)[0])
	if w := runewidth.StringWidth(vis); w > 40 {
		t.Errorf("input row overflows width: %d cols > 40:\n%q", w, vis)
	}
	if !strings.Contains(vis, "repository") {
		t.Errorf("rightmost (cursor) part of the line not visible:\n%q", vis)
	}
	if strings.Contains(vis, "box create coder --agent") {
		t.Errorf("head of the long line should be scrolled off:\n%q", vis)
	}
	if !strings.Contains(vis, "‹") {
		t.Errorf("left overflow marker ‹ missing:\n%q", vis)
	}
	if cur := m.hostCursor(); cur == nil {
		t.Fatal("no host cursor with palette focused")
	} else if cur.X < 0 || cur.X >= 40 {
		t.Errorf("cursor X=%d out of [0,40)", cur.X)
	}

	// Home: the head shows behind a › right marker; no overflow.
	m.pal.Home()
	vis = stripSGR(strings.SplitN(m.View().Content, "\n", 2)[0])
	if w := runewidth.StringWidth(vis); w > 40 {
		t.Errorf("input row overflows after Home: %d cols:\n%q", w, vis)
	}
	if !strings.Contains(vis, "box create coder --agent") {
		t.Errorf("head not visible after Home:\n%q", vis)
	}
	if !strings.Contains(vis, "›") {
		t.Errorf("right overflow marker › missing after Home:\n%q", vis)
	}
}

// The Del key forward-deletes the rune at the cursor in the palette (it
// was unhandled — only Backspace was wired).
func TestDeleteKeyForwardDeletes(t *testing.T) {
	m := newTestModel(t, 80, 20)
	m.pal.Insert("abcd")
	m.pal.Home() // cursor at the start
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDelete})
	if line, _ := m.pal.Line(); line != "bcd" {
		t.Fatalf("Del key = %q, want %q", line, "bcd")
	}
}

// Up walks back through the WHOLE history (the "stuck after one press" bug:
// recalling a line used to repopulate the menu, so the next Up navigated it
// instead of history), Down walks forward to the empty draft, and from the
// draft the arrows resume history / reveal the menu like a shell.
func TestHistoryArrowsNotStuck(t *testing.T) {
	m := newTestModel(t, 80, 20)
	for _, c := range []string{"space ls", "agentbox ls", "box create coder --agent claude"} {
		m.pal.Insert(c)
		m.pal.Submit()
	}
	up := func() { _, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp}) }
	down := func() { _, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) }
	line := func() string { l, _ := m.pal.Line(); return l }

	// Up advances through every history item — not stuck after the first.
	up()
	if line() != "box create coder --agent claude" {
		t.Fatalf("Up 1 = %q, want newest", line())
	}
	up()
	if line() != "agentbox ls" {
		t.Fatalf("Up 2 = %q, want 2nd-newest (this is where it used to stick)", line())
	}
	up()
	if line() != "space ls" {
		t.Fatalf("Up 3 = %q, want oldest", line())
	}
	up() // clamps at the oldest
	if line() != "space ls" {
		t.Fatalf("Up past oldest = %q, want oldest held", line())
	}

	// Down walks forward through history, landing on the empty draft.
	down()
	if line() != "agentbox ls" {
		t.Fatalf("Down 1 = %q, want 2nd-newest", line())
	}
	down()
	if line() != "box create coder --agent claude" {
		t.Fatalf("Down 2 = %q, want newest", line())
	}
	down()
	if line() != "" {
		t.Fatalf("Down 3 = %q, want the empty draft", line())
	}

	// On the empty draft Down reveals the menu (showAllSuggestions, async)
	// rather than recalling history: the line stays empty, and Up resumes
	// history — the full shell-like cycle.
	down()
	if line() != "" {
		t.Fatalf("Down on the empty draft = %q, want empty (menu reveal, not history)", line())
	}
	up()
	if line() != "box create coder --agent claude" {
		t.Fatalf("Up from the draft = %q, want history resumed at newest", line())
	}
}

// When the tabs overrun the width the strip windows to a run containing the
// active tab (with one tab of right look-ahead), never overflows, and marks
// hidden tabs with ‹ / › on the side(s) that have them.
func TestTabStripScrollsToActive(t *testing.T) {
	// 24 cols: with circled-number labels each "❶ tN" tab is ~6 cols, so 8
	// tabs overrun the width and only ~3 show — enough that an active tab in
	// the middle has hidden tabs on both sides.
	m := newTestModel(t, 24, 20)
	for i := 1; i <= 8; i++ {
		s, err := term.Start(term.Options{Argv: []string{"true"}, Rows: m.viewRows(), Cols: 80, OnData: m.wake})
		if err != nil {
			t.Fatalf("start session %d: %v", i, err)
		}
		defer s.Close()
		_ = s.Wait(5 * time.Second)
		m.tabs.Add(testTab(s, fmt.Sprintf("yoloctl t%d", i), true))
	}
	width := func(s string) int { return runewidth.StringWidth(stripSGR(s)) }

	// Active last: ‹ shows (tabs hidden left), no › (nothing hidden right).
	m.tabs.Active = 7
	vis := stripSGR(m.tabStrip())
	if w := width(m.tabStrip()); w > 24 {
		t.Errorf("strip overflows width (active last): %d > 24:\n%q", w, vis)
	}
	if !strings.Contains(vis, circledNumber(8)) {
		t.Errorf("active (last) tab not visible:\n%q", vis)
	}
	if !strings.Contains(vis, "‹") || strings.Contains(vis, "›") {
		t.Errorf("active-last markers wrong (want ‹, not ›):\n%q", vis)
	}
	if strings.Contains(vis, circledNumber(1)) {
		t.Errorf("leftmost tabs should be scrolled off:\n%q", vis)
	}

	// Active first: › shows, no ‹.
	m.tabs.Active = 0
	vis = stripSGR(m.tabStrip())
	if width(m.tabStrip()) > 24 {
		t.Errorf("strip overflows width (active first):\n%q", vis)
	}
	if !strings.Contains(vis, circledNumber(1)) {
		t.Errorf("active (first) tab not visible:\n%q", vis)
	}
	if !strings.Contains(vis, "›") || strings.Contains(vis, "‹") {
		t.Errorf("active-first markers wrong (want ›, not ‹):\n%q", vis)
	}

	// Active middle: both markers, and the look-ahead tab to its right shows.
	m.tabs.Active = 4 // tab number ❺
	vis = stripSGR(m.tabStrip())
	if width(m.tabStrip()) > 24 {
		t.Errorf("strip overflows width (active middle):\n%q", vis)
	}
	if !strings.Contains(vis, circledNumber(5)) {
		t.Errorf("active (middle) tab not visible:\n%q", vis)
	}
	if !strings.Contains(vis, circledNumber(6)) {
		t.Errorf("look-ahead tab right of active not visible:\n%q", vis)
	}
	if !strings.Contains(vis, "‹") || !strings.Contains(vis, "›") {
		t.Errorf("active-middle should show both ‹ and ›:\n%q", vis)
	}
}

// The active tab's title gets more room than inactive tabs by default,
// and tui.yaml tab_title_max_active / tab_title_max_inactive override
// both caps.
func TestTabTitleActiveWiderThanInactive(t *testing.T) {
	activeMax, inactiveMax := (&config.TUI{}).GetTabTitleMaxActive(), (&config.TUI{}).GetTabTitleMaxInactive()
	if activeMax <= inactiveMax {
		t.Fatalf("default active cap %d must exceed inactive %d", activeMax, inactiveMax)
	}
	for _, c := range []struct {
		name                  string
		cfg                   *config.TUI
		wantActive, wantInact int
	}{
		{"defaults", &config.TUI{}, activeMax, inactiveMax},
		{"configured", &config.TUI{TabTitleMaxActive: 14, TabTitleMaxInactive: 9}, 14, 9},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := newTestModel(t, 200, 20) // wide: no window truncation, the caps decide
			m.cfg = c.cfg
			long := strings.Repeat("x", c.wantActive+20)
			for i := 0; i < 2; i++ {
				s, err := term.Start(term.Options{Argv: []string{"true"}, Rows: m.viewRows(), Cols: 80, OnData: m.wake})
				if err != nil {
					t.Fatalf("start session %d: %v", i, err)
				}
				defer s.Close()
				_ = s.Wait(5 * time.Second)
				m.tabs.Add(testTab(s, "yoloctl "+long, true))
			}
			m.tabs.Active = 0 // ❶ active, ❷ inactive

			vis := stripSGR(m.tabStrip())
			split := strings.Index(vis, circledNumber(2))
			if split < 0 {
				t.Fatalf("second tab not rendered:\n%q", vis)
			}
			// Truncate(s, cap, "…") shows cap-1 title chars + the ellipsis.
			if got := strings.Count(vis[:split], "x"); got != c.wantActive-1 {
				t.Errorf("active title shows %d chars, want %d", got, c.wantActive-1)
			}
			if got := strings.Count(vis[split:], "x"); got != c.wantInact-1 {
				t.Errorf("inactive title shows %d chars, want %d", got, c.wantInact-1)
			}
		})
	}
}

// --- Clickable chrome: palette / tab strip / view click-to-focus ---

func click(x, y int) tea.MouseClickMsg {
	return tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y}
}

func releaseAt(x, y int) tea.MouseReleaseMsg {
	return tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x, Y: y}
}

// addExitedTab appends a cheap, already-exited tab (a `true` child) so focus
// and geometry tests have real Sessions without a long-lived process.
func addExitedTab(t *testing.T, m *model, name string) {
	t.Helper()
	s, err := term.Start(term.Options{Argv: []string{"true"}, Rows: m.viewRows(), Cols: 80, OnData: m.wake})
	if err != nil {
		t.Fatalf("start session %s: %v", name, err)
	}
	t.Cleanup(s.Close)
	_ = s.Wait(5 * time.Second)
	m.tabs.Add(testTab(s, "yoloctl "+name, true))
}

// tabClickX returns the mid-segment column of the strip segment for absolute
// tab index i (the inverse of tabAt), or -1 when i isn't currently visible.
func tabClickX(m *model, i int) int {
	segs, _, _, firstX := m.tabStripLayout()
	col := firstX
	for _, s := range segs {
		if s.index == i {
			return col + s.width/2
		}
		col += s.width
	}
	return -1
}

// TestTabSegmentsWindowing exercises the pure geometry the renderer and the
// hit-tester share: windowing around the active tab, the oversize clamp, and
// the empty case.
func TestTabSegmentsWindowing(t *testing.T) {
	// Nine 3-col labels → 7-col segments (1 pad each side, " ✕"). At width
	// 24 they overrun, so the strip windows around the active tab.
	labels := make([]string, 9)
	for i := range labels {
		labels[i] = "lbl" // StringWidth 3 → segW 7
	}
	segs, hidLeft, hidRight, firstX := tabSegments(labels, 7, 24, ansi.WcWidth)

	// budget = 24-1-2 = 21; window fills [6..8] (active 7 + look-ahead 8, then
	// left to 6): 3 segments × 7 = 21 ≤ 21.
	gotIdx := make([]int, len(segs))
	for i, s := range segs {
		gotIdx[i] = s.index
		if s.width != 7 {
			t.Errorf("seg %d width = %d, want 7", s.index, s.width)
		}
	}
	if fmt.Sprint(gotIdx) != fmt.Sprint([]int{6, 7, 8}) {
		t.Fatalf("visible indices = %v, want [6 7 8]", gotIdx)
	}
	if !hidLeft || hidRight {
		t.Errorf("hidLeft=%v hidRight=%v, want true/false", hidLeft, hidRight)
	}
	if firstX != 2 { // margin(1) + ‹(1)
		t.Errorf("firstX = %d, want 2", firstX)
	}
	// Hit-test the windowed segments: cols [2,9)=6, [9,16)=7, [16,23)=8;
	// margin/arrow/pad → not found.
	at := func(x int) int {
		col := firstX
		for _, s := range segs {
			if x >= col && x < col+s.width {
				return s.index
			}
			col += s.width
		}
		return -1
	}
	for _, c := range []struct{ x, want int }{
		{0, -1}, {1, -1}, {2, 6}, {8, 6}, {9, 7}, {16, 8}, {22, 8}, {23, -1},
	} {
		if got := at(c.x); got != c.want {
			t.Errorf("hit x=%d → %d, want %d", c.x, got, c.want)
		}
	}

	// Single oversized tab clamps to the budget and still yields one segment.
	wide := []string{strings.Repeat("w", 100)}
	one, _, _, _ := tabSegments(wide, 0, 24, ansi.WcWidth)
	if len(one) != 1 {
		t.Fatalf("oversized: got %d segments, want 1", len(one))
	}
	if one[0].width > 24 || one[0].width-ansi.StringWidth(one[0].label) != 2+tabCloseW {
		t.Errorf("oversized tab width %d exceeds strip width 24 or lost its ✕", one[0].width)
	}

	// No labels → no segments (the (no tabs) case); firstX is the margin.
	none, _, _, fx := tabSegments(nil, 0, 80, ansi.WcWidth)
	if len(none) != 0 || fx != 1 {
		t.Errorf("empty: segs=%d firstX=%d, want 0/1", len(none), fx)
	}
}

// TestTabAtMapsClick maps a click column back to the tab under it over a real
// model.
func TestTabAtMapsClick(t *testing.T) {
	m := newTestModel(t, 80, 20)
	for i := 1; i <= 3; i++ {
		addExitedTab(t, m, fmt.Sprintf("t%d", i))
	}
	for j := 0; j < 3; j++ {
		x := tabClickX(m, j)
		if x < 0 {
			t.Fatalf("tab %d not visible", j)
		}
		if got := m.tabAt(x); got != j {
			t.Errorf("tabAt(%d) = %d, want %d", x, got, j)
		}
	}
	if got := m.tabAt(0); got != -1 { // column 0 is the leading margin
		t.Errorf("tabAt(0) = %d, want -1 (margin)", got)
	}
}

// TestTabStripRowMatchesView confirms tabStripRow mirrors View(): row 1
// unfocused, pushed down by the overlay when the palette is focused — and the
// rendered strip really sits at that row.
func TestTabStripRowMatchesView(t *testing.T) {
	m := newTestModel(t, 80, 20)
	addExitedTab(t, m, "t1")

	m.focusPalette = false
	if got := m.tabStripRow(); got != 1 {
		t.Errorf("unfocused tabStripRow = %d, want 1", got)
	}
	lines := strings.Split(m.View().Content, "\n")
	if stripSGR(lines[1]) != stripSGR(m.tabStrip()) {
		t.Errorf("unfocused: strip not at row 1:\n%q", lines[1])
	}

	// Focus + an overlay command pushes the strip down by overlayContentRows.
	m.focusPalette = true
	runOverlayCommand(t, m, "seq", "1", "5")
	want := 1 + m.overlayContentRows()
	if got := m.tabStripRow(); got != want {
		t.Fatalf("focused tabStripRow = %d, want %d", got, want)
	}
	lines = strings.Split(m.View().Content, "\n")
	if stripSGR(lines[want]) != stripSGR(m.tabStrip()) {
		t.Errorf("focused: strip not at row %d:\n%q", want, lines[want])
	}
}

// TestClickPaletteFocuses: clicking row 0 while a tab is focused returns focus
// to the palette.
func TestClickPaletteFocuses(t *testing.T) {
	m := newTestModel(t, 80, 20)
	addExitedTab(t, m, "t1")
	m.focusPalette = false

	_, _ = m.Update(click(3, 0))
	if !m.focusPalette {
		t.Error("click on row 0 did not focus the palette")
	}
}

// TestClickTabActivatesAndFocuses: clicking a tab in the strip activates and
// focuses that tab.
func TestClickTabActivatesAndFocuses(t *testing.T) {
	m := newTestModel(t, 80, 20)
	for i := 1; i <= 3; i++ {
		addExitedTab(t, m, fmt.Sprintf("t%d", i))
	}
	m.focusPalette = true // strip at row 1 (no overlay content)
	x := tabClickX(m, 1)

	_, _ = m.Update(click(x, m.tabStripRow()))
	_, _ = m.Update(releaseAt(x, m.tabStripRow())) // a tab activates on release
	if m.tabs.Active != 1 {
		t.Errorf("clicked tab 2 (index 1); Active = %d", m.tabs.Active)
	}
	if m.focusPalette {
		t.Error("clicking a tab must move focus to the view")
	}
}

// TestClickViewFocusesView: clicking the view body while the palette is
// focused returns focus to the active tab without switching tabs.
func TestClickViewFocusesView(t *testing.T) {
	m := newTestModel(t, 80, 20)
	addExitedTab(t, m, "t1")
	addExitedTab(t, m, "t2")
	m.tabs.Active = 1
	m.focusPalette = true

	_, _ = m.Update(click(10, m.tabStripRow()+1)) // a row below the strip = the view
	if m.focusPalette {
		t.Error("click in the view did not return focus to the view")
	}
	if m.tabs.Active != 1 {
		t.Errorf("view click must not switch tabs; Active = %d", m.tabs.Active)
	}
}

// TestClickTabWhileBusyPreservesOutput: clicking a tab while a console command
// runs detaches (output preserved), not wipes.
func TestClickTabWhileBusyPreservesOutput(t *testing.T) {
	m := newTestModel(t, 80, 20)
	addExitedTab(t, m, "t1")
	m.focusPalette = true

	started, err := m.console.Run(nil, "", "sleep", "5")
	if !started || err != nil {
		t.Fatalf("console run: %v %v", started, err)
	}
	t.Cleanup(func() {
		m.console.Kill()
		for i := 0; i < 250 && m.console.Busy(); i++ {
			time.Sleep(20 * time.Millisecond)
		}
	})
	if !m.console.Busy() {
		t.Fatal("console should be busy after starting sleep")
	}

	_, _ = m.Update(click(tabClickX(m, 0), m.tabStripRow()))
	_, _ = m.Update(releaseAt(tabClickX(m, 0), m.tabStripRow())) // a tab activates on release
	if m.focusPalette {
		t.Error("clicking a tab should leave the palette")
	}
	if !strings.Contains(m.console.GridText(), "sleep 5") {
		t.Errorf("busy detach wiped the console output:\n%q", m.console.GridText())
	}
}

// TestClickPaletteWhileFocusedKeepsOutput: clicking the input row while the
// palette is already focused must NOT wipe output nor jump the scroll.
func TestClickPaletteWhileFocusedKeepsOutput(t *testing.T) {
	m := newTestModel(t, 80, 20)
	runOverlayCommand(t, m, "seq", "1", "40")
	m.scrollOff = 3 // user scrolled back into the overlay
	before := m.console.GridText()

	_, _ = m.Update(click(3, 0))
	if !m.focusPalette {
		t.Error("palette lost focus")
	}
	if m.scrollOff != 3 {
		t.Errorf("scrollOff changed to %d (must stay 3 — hard no-op)", m.scrollOff)
	}
	if m.console.GridText() != before {
		t.Error("re-focusing the palette wiped the overlay output")
	}
}

// Clicking a tab hides the palette without erasing its command history.
func TestClickTabIdlePreservesConsoleHistory(t *testing.T) {
	m := newTestModel(t, 80, 20)
	addExitedTab(t, m, "t1")
	runOverlayCommand(t, m, "echo", "hello-overlay")
	if !strings.Contains(m.console.GridText(), "hello-overlay") {
		t.Fatal("precondition: overlay should hold output")
	}
	m.focusPalette = true

	_, _ = m.Update(click(tabClickX(m, 0), m.tabStripRow()))
	_, _ = m.Update(releaseAt(tabClickX(m, 0), m.tabStripRow())) // a tab activates on release
	if m.focusPalette {
		t.Error("clicking a tab should leave the palette")
	}
	if !strings.Contains(m.console.GridText(), "hello-overlay") {
		t.Fatal("tab-click erased history")
	}
	if strings.Contains(m.View().Content, "hello-overlay") {
		t.Fatal("hidden palette is still visible")
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	if !strings.Contains(m.View().Content, "hello-overlay") {
		t.Fatal("refocus did not show history")
	}
}

// TestConsumedClickSwallowsRelease: the release paired with a consumed chrome
// click is swallowed, so it can't leak into a now-focused mouse-reporting
// child.
func TestConsumedClickSwallowsRelease(t *testing.T) {
	m := newTestModel(t, 80, 20)
	addExitedTab(t, m, "t1")
	addExitedTab(t, m, "t2")
	m.focusPalette = true
	row := m.tabStripRow()
	x := tabClickX(m, 1)

	_, _ = m.Update(click(x, row))
	if _, ok := m.mouseOwners[tea.MouseLeft]; !ok {
		t.Fatal("consumed tab click did not retain gesture ownership")
	}
	_, _ = m.Update(releaseAt(x, row))
	if _, ok := m.mouseOwners[tea.MouseLeft]; ok {
		t.Error("paired release was not swallowed (would leak to the child)")
	}
}

// Clicking an exited tab's content leaves focus and tab selection alone,
// without giving a dead application ownership of the gesture.
func TestExitedTabContentClickKeepsFocus(t *testing.T) {
	m := newTestModel(t, 80, 20)
	addExitedTab(t, m, "t1")
	addExitedTab(t, m, "t2")
	m.tabs.Active = 1
	m.focusPalette = false // a tab is focused

	_, _ = m.Update(click(10, m.tabStripRow()+2)) // a content row
	if m.focusPalette {
		t.Error("content click must not change focus when a tab is focused")
	}
	if m.tabs.Active != 1 {
		t.Errorf("content click must not switch tabs; Active = %d", m.tabs.Active)
	}
	if owner, ok := m.mouseOwners[tea.MouseLeft]; ok && owner.surface != nil && !owner.selection {
		t.Error("exited tab must not own an application gesture")
	}
}

// TestNonLeftChromeClickIgnored: right-click and wheel on the chrome don't
// drive focus/tab activation.
func TestNonLeftChromeClickIgnored(t *testing.T) {
	m := newTestModel(t, 80, 20)
	addExitedTab(t, m, "t1")
	addExitedTab(t, m, "t2")
	m.tabs.Active = 0 // tabs.Add focuses the last; pin tab 0 so a change is visible
	m.focusPalette = true

	_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseRight, X: tabClickX(m, 1), Y: m.tabStripRow()})
	if !m.focusPalette || m.tabs.Active != 0 {
		t.Errorf("right-click changed state: focusPalette=%v Active=%d", m.focusPalette, m.tabs.Active)
	}
	_, _ = m.Update(wheel(tea.MouseWheelUp)) // wheel at {4,4} — not a chrome action
	if !m.focusPalette || m.tabs.Active != 0 {
		t.Errorf("wheel changed focus/active: focusPalette=%v Active=%d", m.focusPalette, m.tabs.Active)
	}
}

// --- Mouse motion / hover passthrough ---

// TestSGRMouseMotion pins the SGR encoding for motion (the hover/drag bit) and
// confirms press/release/wheel still encode as before. Child row = Y-barRows+1,
// child col = X+1; the motion bit is +32; a no-button hover is button 3.
func TestSGRMouseMotion(t *testing.T) {
	const viewRows = 20
	cases := []struct {
		name            string
		mo              tea.Mouse
		motion, release bool
		want            string // "" means nil (not forwarded)
	}{
		{"left click", tea.Mouse{X: 4, Y: 5, Button: tea.MouseLeft}, false, false, "\x1b[<0;5;4M"},
		{"left release", tea.Mouse{X: 4, Y: 5, Button: tea.MouseLeft}, false, true, "\x1b[<0;5;4m"},
		{"bare hover", tea.Mouse{X: 4, Y: 5, Button: tea.MouseNone}, true, false, "\x1b[<35;5;4M"},
		{"left drag", tea.Mouse{X: 4, Y: 5, Button: tea.MouseLeft}, true, false, "\x1b[<32;5;4M"},
		{"wheel up", tea.Mouse{X: 4, Y: 5, Button: tea.MouseWheelUp}, false, false, "\x1b[<64;5;4M"},
		{"no-button non-motion → nil", tea.Mouse{X: 4, Y: 5, Button: tea.MouseNone}, false, false, ""},
		{"hover over bar chrome → nil", tea.Mouse{X: 4, Y: 1, Button: tea.MouseNone}, true, false, ""},
	}
	for _, c := range cases {
		if got := testSGRMouse(t, c.mo, c.motion, c.release, viewRows); got != c.want {
			t.Errorf("%s: sgrMouse = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestAlternateScrollWheelTranslation reproduces Codex's mouse setup:
// alternate screen + DECSET 1007, without mouse reporting. In that mode a
// terminal translates wheel events to cursor keys instead of trying to scroll
// its own (empty) alternate-screen scrollback.
func TestAlternateScrollWheelTranslation(t *testing.T) {
	m := newTestModel(t, 80, 20)
	capture := t.TempDir() + "/wheel-input"
	script := `printf '\033[?1049h\033[?1007h'
stty raw -echo
dd bs=1 count=6 of="$1" 2>/dev/null
printf '\033[?1007l\033[?1049l'`
	s, err := term.Start(term.Options{
		Argv: []string{"sh", "-c", script, "sh", capture},
		Rows: m.viewRows(), Cols: 80, OnData: m.wake,
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	t.Cleanup(s.Close)
	m.tabs.Add(testTab(s, "codex", false))
	m.focusPalette = false

	deadline := time.Now().Add(5 * time.Second)
	for (!s.IsAlt() || !s.AlternateScroll()) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !s.IsAlt() || !s.AlternateScroll() {
		t.Fatalf("child modes not tracked: alt=%v alternate-scroll=%v",
			s.IsAlt(), s.AlternateScroll())
	}

	_, _ = m.Update(wheel(tea.MouseWheelUp))
	_, _ = m.Update(wheel(tea.MouseWheelDown))
	if err := s.Wait(5 * time.Second); err != nil {
		t.Fatalf("child wait: %v", err)
	}
	got, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read captured input: %v", err)
	}
	if want := "\x1b[A\x1b[B"; string(got) != want {
		t.Errorf("captured wheel input = %q, want cursor keys %q", got, want)
	}
}

// Codex's normal conversation uses an inline viewport, not the alternate
// screen. It commits finalized transcript rows by scrolling a top-anchored
// partial region; once the emulator records that native history, an
// unclaimed wheel event must move yoloctl's owned scrollback viewport.
func TestCodexInlineHistoryWheelScrollsOwnedScrollback(t *testing.T) {
	m := newTestModel(t, 80, 20)
	script := `printf '\033[1;1Hcodex-oldest\033[2;1Hcodex-second\033[1;6r\033[6;1H\r\ncodex-new\033[r'
sleep 30`
	s, err := term.Start(term.Options{
		Argv: []string{"sh", "-c", script},
		Rows: m.viewRows(), Cols: 80, OnData: m.wake,
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	t.Cleanup(s.Close)
	m.tabs.Add(testTab(s, "codex", false))
	m.focusPalette = false

	deadline := time.Now().Add(5 * time.Second)
	for len(s.Scrollback()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if sb := s.Scrollback(); len(sb) != 1 ||
		strings.TrimSpace(sb[0]) != "codex-oldest" {
		t.Fatalf("Codex-style inline history not captured: %q", sb)
	}

	live := m.View().Content
	if strings.Contains(live, "codex-oldest") {
		t.Fatalf("evicted history unexpectedly visible in live viewport:\n%s", live)
	}
	_, _ = m.Update(wheel(tea.MouseWheelUp))
	if m.scrollOff != 1 {
		t.Fatalf("scrollOff after wheel-up = %d, want 1", m.scrollOff)
	}
	if scrolled := m.View().Content; !strings.Contains(scrolled, "codex-oldest") {
		t.Errorf("wheel did not reveal Codex history:\n%s", scrolled)
	}
}

// TestHostMouseTrackingFollowsChild confirms host tracking mirrors the
// focused child: a child that requested any-motion (1003) makes the focused
// view ask the host for all-motion (so hover moves arrive), while the palette
// stays on cell-motion (we don't forward motion to a child while it's blurred).
func TestHostMouseTrackingFollowsChild(t *testing.T) {
	m := newTestModel(t, 80, 20)
	s, err := term.Start(term.Options{
		Argv: []string{"sh", "-c", "printf '\\033[?1003h\\033[?1006h'; sleep 5"},
		Rows: m.viewRows(), Cols: 80, OnData: m.wake,
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	t.Cleanup(s.Close)
	m.tabs.Add(testTab(s, "yoloctl hover", false))

	// Wait for the child's DECSET 1003 to be tracked (pump is async).
	deadline := time.Now().Add(5 * time.Second)
	for !s.MouseAnyMotion() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !s.MouseAnyMotion() {
		t.Fatal("child any-motion (1003) was not tracked")
	}

	m.focusPalette = false
	if got := m.wantedMouseTracking(); got != 1003 {
		t.Errorf("focused-on-1003-child tracking = %v, want 1003", got)
	}
	m.focusPalette = true
	if got := m.wantedMouseTracking(); got != 1002 {
		t.Errorf("palette-focused tracking = %v, want 1002", got)
	}
}

func TestOSC52PolicyAtHostTUIBoundary(t *testing.T) {
	event := termio.OSC52{Selection: "c", Payload: "Y29waWVk"}

	blocked := newTestModel(t, 80, 20)
	disabled := false
	blocked.cfg.Seams.Clipboard = &disabled
	blockedMsg, ok := blocked.osc52HostMsg(event).(osc52NoticeMsg)
	if !ok {
		t.Fatalf("disabled route returned %T, want osc52NoticeMsg", blocked.osc52HostMsg(event))
	}
	_, _ = blocked.Update(blockedMsg)
	if blocked.status != termio.OSC52DisabledNotice {
		t.Fatalf("disabled status = %q", blocked.status)
	}

	allowed := newTestModel(t, 80, 20)
	raw, ok := allowed.osc52HostMsg(event).(tea.RawMsg)
	if !ok {
		t.Fatalf("enabled route returned %T, want tea.RawMsg", allowed.osc52HostMsg(event))
	}
	if got := fmt.Sprint(raw.Msg); got != event.Sequence() {
		t.Fatalf("raw sequence = %q, want %q", got, event.Sequence())
	}
}

// The history view owns the overlay over an open or late completion menu and
// keeps the draft; the next editing key returns to the draft and its menu.
func TestConsoleHistoryWinsOverCompletionMenu(t *testing.T) {
	for _, late := range []bool{false, true} {
		name := "existing"
		if late {
			name = "arrives_after_toggle"
		}
		t.Run(name, func(t *testing.T) {
			m := newTestModel(t, 100, 32)
			m.console.ShowError("EARLIER_OUTPUT")
			m.console.ShowError("LATEST_OUTPUT")
			m.pal.Insert("bo")
			msg := suggestionsMsg{Generation: m.spaceGeneration, Line: "bo", Items: candidates("box"), Nav: true}
			if !late {
				_, _ = m.Update(msg)
			}
			_, _ = m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
			if late {
				_, _ = m.Update(msg)
			}
			view := m.View().Content
			if !m.consoleHistory || !strings.Contains(view, "EARLIER_OUTPUT") || !strings.Contains(view, "LATEST_OUTPUT") ||
				!strings.Contains(view, "console history") {
				t.Fatalf("Ctrl+R history hidden by completion (history=%t):\n%s", m.consoleHistory, view)
			}
			if line, _ := m.pal.Line(); line != "bo" {
				t.Fatalf("draft changed: %q", line)
			}
			// Scrolling stays in the view; an editing key returns to the
			// draft, which it edits, and to completion.
			_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
			if !m.consoleHistory {
				t.Fatal("PgUp left the history view")
			}
			_, _ = m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
			if line, _ := m.pal.Line(); m.consoleHistory || line != "box" {
				t.Fatalf("editing key: history=%t line %q", m.consoleHistory, line)
			}
			_, _ = m.Update(suggestionsMsg{Generation: m.spaceGeneration, Line: "box", Items: candidates("box"), Nav: true})
			if view := m.View().Content; !m.menuShown() || strings.Contains(view, "EARLIER_OUTPUT") {
				t.Fatalf("menu did not return after editing:\n%s", view)
			}
		})
	}
}
