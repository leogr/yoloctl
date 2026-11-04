package spaceview

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/spacetelemetry"
)

var sizes = [][2]int{{200, 60}, {160, 50}, {132, 50}, {120, 45}, {100, 40}, {80, 30}, {64, 30}, {48, 24}, {40, 20}, {24, 10}, {20, 7}, {19, 6}, {1, 1}, {80, 3}}

// Every layout must fit the terminal exactly: no horizontal scrolling, ever.
func TestRenderFitsEveryWidth(t *testing.T) {
	for _, size := range sizes {
		m := fixtureModel(t, size[0], size[1])
		m.View()
		out := m.last.c.render()
		rows := strings.Split(out, "\n")
		if len(rows) != size[1] {
			t.Fatalf("%dx%d: %d rows", size[0], size[1], len(rows))
		}
		for i, row := range rows {
			if w := ansi.StringWidthWc(row); w != size[0] {
				t.Errorf("%dx%d row %d is %d columns: %q", size[0], size[1], i, w, ansi.Strip(row))
			}
		}
	}
}

// Every box stays reachable: visible on screen or below it by scrolling.
func TestEveryBoxReachableByScrolling(t *testing.T) {
	for _, size := range sizes[:9] {
		m := fixtureModel(t, size[0], size[1])
		seen := map[string]bool{}
		for scroll := 0; scroll < 400; scroll += 5 {
			m.scroll = scroll
			m.View()
			for _, row := range strings.Split(m.last.c.plain(), "\n") {
				for _, b := range fixtureInventory().Boxes {
					if strings.Contains(row, b.Name) {
						seen[b.Name] = true
					}
				}
			}
		}
		for _, b := range fixtureInventory().Boxes {
			if !seen[b.Name] {
				t.Errorf("%dx%d: box %s never visible", size[0], size[1], b.Name)
			}
		}
	}
}

func findTarget(t *testing.T, m *Model, want target) (int, int) {
	t.Helper()
	return findTargetFrom(t, m, want, 0)
}

// findTargetFrom finds want's first cell at row y0 or below.
func findTargetFrom(t *testing.T, m *Model, want target, y0 int) (int, int) {
	t.Helper()
	f := m.last
	for y := y0; y < f.c.h; y++ {
		for x := range f.c.w {
			if got, ok := f.targetAt(x, y); ok && got == want {
				return x, y
			}
		}
	}
	t.Fatalf("target %+v not on screen", want)
	return 0, 0
}

func click(m *Model, x, y int, button tea.MouseButton) tea.Cmd {
	_, cmd := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: button})
	m.View()
	return cmd
}

func TestClickRunningBoxOpensTabInsideTUI(t *testing.T) {
	m := fixtureModel(t, 132, 50)
	var opened []string
	m.opts.OpenTab = func(box string) error { opened = append(opened, box); return nil }
	m.View()
	x, y := findTarget(t, m, target{tBox, "api-refactor"})
	click(m, x, y, tea.MouseLeft)
	if !slices.Equal(opened, []string{"api-refactor"}) {
		t.Fatalf("opened %v", opened)
	}
	if m.toast == nil || !strings.Contains(m.toast.text, "new tab") {
		t.Fatalf("toast = %+v", m.toast)
	}
}

func TestClickRunningBoxExecsAttachOutsideTUI(t *testing.T) {
	m := fixtureModel(t, 132, 50)
	var built []string
	m.opts.AttachCmd = func(box string) *exec.Cmd {
		built = append(built, box)
		return exec.Command("true")
	}
	m.View()
	x, y := findTarget(t, m, target{tBox, "docs-bot"})
	if cmd := click(m, x, y, tea.MouseLeft); cmd == nil {
		t.Fatal("no exec command returned")
	}
	if !slices.Equal(built, []string{"docs-bot"}) {
		t.Fatalf("attach built for %v", built)
	}
	m.Update(attachedMsg{box: "docs-bot"})
	if !strings.Contains(m.toast.text, "detached from docs-bot") {
		t.Fatalf("toast after detach = %q", m.toast.text)
	}
}

// Attaching to a stopped box or an exited session starts something, so the
// view asks first; nothing runs until the user confirms.
func TestStartingAttachNeedsConfirmation(t *testing.T) {
	for _, name := range []string{"scratch", "web-preview"} {
		m := fixtureModel(t, 132, 50)
		var opened []string
		m.opts.OpenTab = func(box string) error { opened = append(opened, box); return nil }
		m.View()
		for m.last.boxRows[name][0] >= m.last.scroll+m.last.bodyH {
			m.scroll += 5
			m.View()
		}
		x, y := findTarget(t, m, target{tBox, name})
		click(m, x, y, tea.MouseLeft)
		if len(opened) != 0 || m.popup == nil || m.popup.kind != "confirm" {
			t.Fatalf("%s: opened=%v popup=%+v", name, opened, m.popup)
		}
		m.View()
		ax, ay := findTarget(t, m, target{tAction, "0"})
		click(m, ax, ay, tea.MouseLeft)
		if !slices.Equal(opened, []string{name}) || m.popup != nil {
			t.Fatalf("%s: after confirm opened=%v popup=%v", name, opened, m.popup != nil)
		}
	}
}

func TestEscapeCancelsConfirmation(t *testing.T) {
	m := fixtureModel(t, 132, 50)
	called := false
	m.opts.OpenTab = func(string) error { called = true; return nil }
	m.openConfirm(mustBox(t, m, "scratch"))
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.popup != nil || called {
		t.Fatalf("popup=%v called=%v", m.popup != nil, called)
	}
}

func mustBox(t *testing.T, m *Model, name string) spacetelemetry.Box {
	t.Helper()
	b, ok := m.box(name)
	if !ok {
		t.Fatalf("no box %s", name)
	}
	return b
}

func TestClickAccountShowsAccountShowData(t *testing.T) {
	m := fixtureModel(t, 132, 50)
	var ran [][]string
	m.opts.Run = func(_ context.Context, args ...string) ([]byte, error) {
		ran = append(ran, args)
		return []byte(`{"space":"dev","state_available":true,"name":"personal","manifest_path":"/disk/config/accounts/personal.yaml",
"state_home":"/var/lib/yoloctl/account-state/personal","secrets":["ANTHROPIC_API_KEY"],
"state_leaves":[{"agent":"claude","state_path":"/var/lib/yoloctl/account-state/personal/claude"}]}`), nil
	}
	m.View()
	x, y := findTarget(t, m, target{tAccount, "personal"})
	cmd := click(m, x, y, tea.MouseLeft)
	if m.popup == nil || cmd == nil {
		t.Fatal("account click did not open a loading popup")
	}
	m.Update(cmd())
	m.View()
	want := []string{"account", "show", "personal", "--json", "--space", "dev"}
	if len(ran) != 1 || !slices.Equal(ran[0], want) {
		t.Fatalf("ran %v, want %v", ran, want)
	}
	screen := m.last.c.plain()
	for _, s := range []string{"personal", "ANTHROPIC_API_KEY", "names only", "/var/lib/yoloctl/account-state/personal/claude",
		"space default", "api-refactor", "web-preview", "conductor", "yoloctl account show personal --json --space dev"} {
		if !strings.Contains(screen, s) {
			t.Errorf("account popup lacks %q:\n%s", s, screen)
		}
	}
	if strings.Contains(screen, "docs-bot ●") {
		t.Error("account popup lists a box of another account")
	}
	// Clicking outside the popup closes it without acting on the dashboard.
	click(m, 0, m.h-1, tea.MouseLeft)
	if m.popup != nil {
		t.Fatal("outside click did not close the popup")
	}
}

func TestPopupShowsCommandFailure(t *testing.T) {
	m := fixtureModel(t, 100, 40)
	m.opts.Run = func(context.Context, ...string) ([]byte, error) { return nil, errors.New("account \"x\" not found") }
	m.openAccount("x")
	m.Update(m.popup.cmd())
	m.View()
	if !strings.Contains(m.last.c.plain(), "not found") {
		t.Fatalf("error not shown:\n%s", m.last.c.plain())
	}
}

func TestYoloItemsAreDistinctAndClickable(t *testing.T) {
	m := fixtureModel(t, 132, 50)
	m.View()
	// The title row also links the operator capability, as warning text on
	// its black first row; the pills are on the cards below it.
	for _, tg := range []target{{tFeature, "yolo.chat.skill"}, {tCapability, "yolo.space.operator"}} {
		x, y := findTargetFrom(t, m, tg, 1)
		cell := m.last.c.cells[y][x]
		if cell.style.BG == 0 {
			t.Errorf("%s is not drawn as a raised pill", tg.id)
		}
	}
	x, y := findTargetFrom(t, m, target{tCapability, "yolo.space.operator"}, 1)
	if m.last.c.cells[y][x].style.BG != m.theme.OperatorBG {
		t.Error("operator capability is not drawn with the warning color")
	}
	// The feature grants the same-named capability; the pill shows once, as
	// the capability.
	b := &builder{m: m, t: m.theme, targets: []target{{}}, index: map[string]int{}}
	var pills []string
	for _, sg := range b.yoloRow(m.d.inv.Boxes[1], 200) {
		if strings.TrimSpace(sg.text) != "" {
			pills = append(pills, b.targets[sg.hit].kind+":"+strings.TrimSpace(sg.text))
		}
	}
	if want := []string{"feature:❯ yolo.chat.skill", "capability:▲ yolo.space.operator"}; !slices.Equal(pills, want) {
		t.Errorf("conductor pills = %q, want %q", pills, want)
	}
	if !strings.Contains(m.last.c.plain(), "▲ 1 operator box") {
		t.Error("title bar does not warn about the operator box")
	}
	m.opts.Run = func(context.Context, ...string) ([]byte, error) {
		return []byte(`{"name":"yolo.space.operator","source":"built-in","description":"Operate this space."}`), nil
	}
	click(m, x, y, tea.MouseLeft)
	m.Update(m.popup.cmd())
	m.View()
	if !strings.Contains(m.last.c.plain(), "Authority over every box in this space") {
		t.Fatalf("operator popup lacks the warning:\n%s", m.last.c.plain())
	}
}

func TestChatRoomMembersShareTheRoomMarker(t *testing.T) {
	m := fixtureModel(t, 132, 50)
	m.View()
	// cardTop finds the top-left corner cell of a box's card.
	cardTop := func(name string) (int, int) {
		y := m.last.boxRows[name][0] - m.last.scroll + m.last.bodyY
		for x := range m.last.c.w {
			if tg, ok := m.last.targetAt(x, y); ok && tg == (target{tBox, name}) && m.last.c.cells[y][x].text == "╭" {
				return x, y
			}
		}
		t.Fatalf("no card corner for %s", name)
		return 0, 0
	}
	corner := func(name string) string {
		x, y := cardTop(name)
		if tg, ok := m.last.targetAt(x+1, y); ok && tg.kind == tRoom {
			return tg.id
		}
		return ""
	}
	for _, name := range []string{"api-refactor", "conductor", "docs-bot", "room"} {
		if got := corner(name); got != "room" {
			t.Errorf("%s card corner links to %q", name, got)
		}
	}
	if got := corner("web-preview"); got != "" {
		t.Errorf("non-member web-preview links to %q", got)
	}
	// Hovering the room spotlights members and dims the rest.
	x, y := findTarget(t, m, target{tRoom, "room"})
	m.Update(tea.MouseMotionMsg{X: x, Y: y})
	m.View()
	border := func(name string) Color {
		x, y := cardTop(name)
		return m.last.c.cells[y][x].style.FG
	}
	if border("docs-bot") != m.theme.RoomHi || border("web-preview") != m.theme.Faint {
		t.Errorf("spotlight borders: member %v non-member %v", border("docs-bot"), border("web-preview"))
	}
}

func TestKeyboardFocusFilterAndSort(t *testing.T) {
	m := fixtureModel(t, 132, 50)
	m.View()
	if m.focus != "api-refactor" {
		t.Fatalf("initial focus %q", m.focus)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m.View()
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m.View()
	if m.focus != "scratch" { // grid of three: conductor → (down) scratch
		t.Fatalf("focus after right, down = %q", m.focus)
	}
	m.Update(tea.KeyPressMsg{Text: "/", Code: '/'})
	for _, r := range "docs" {
		m.Update(tea.KeyPressMsg{Text: string(r), Code: r})
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.View()
	if m.focus != "docs-bot" || len(m.last.order) != 1 {
		t.Fatalf("filtered focus %q order %v", m.focus, m.last.order)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m.Update(tea.KeyPressMsg{Text: "s", Code: 's'})
	m.View()
	if m.last.order[0] != "api-refactor" || m.sortMode != sortCPU {
		t.Fatalf("CPU sort order %v", m.last.order)
	}
}

func TestStoppedSpaceIsExplicit(t *testing.T) {
	m := New(context.Background(), Options{Space: "dev"})
	m.w, m.h = 100, 30
	m.Update(lostMsg{err: errors.New("space dev is not running; start it with: yoloctl space start dev"), retryAt: time.Now().Add(time.Second)})
	m.View()
	screen := m.last.c.plain()
	for _, s := range []string{"is not available", "never starts a VM", "yoloctl space start dev", "SIGNAL LOST"} {
		if !strings.Contains(screen, s) {
			t.Errorf("stopped screen lacks %q:\n%s", s, screen)
		}
	}
}

func TestInventoryTransitionsBecomeToasts(t *testing.T) {
	m := fixtureModel(t, 132, 50)
	inv := fixtureInventory()
	inv.Boxes[2].Session = "exited"
	inv.Boxes[2].ExitCode = ptr(0)
	m.Update(frameMsg(spacetelemetry.Frame{Kind: spacetelemetry.KindInventory, At: fixtureStart.Add(time.Minute), Inventory: inv}))
	if m.toast == nil || m.toast.text != "docs-bot session exited (0)" || m.toast.bad {
		t.Fatalf("toast = %+v", m.toast)
	}
	s := fixtureSample(41)
	s.Pool.OOMKills = 1
	m.Update(frameMsg(spacetelemetry.Frame{Kind: spacetelemetry.KindSample, At: fixtureStart.Add(41 * time.Second), Sample: s}))
	if m.toast == nil || !m.toast.bad || !strings.Contains(m.toast.text, "OOM kill") {
		t.Fatalf("oom toast = %+v", m.toast)
	}
}

func TestClickAgentNameShowsAgent(t *testing.T) {
	m := fixtureModel(t, 132, 50)
	var ran []string
	m.opts.Run = func(_ context.Context, args ...string) ([]byte, error) {
		ran = args
		return []byte(`{"name":"codex","source":"built-in","image":"img","command":"codex","npm_package":"@openai/codex","requires_features":["codex"]}`), nil
	}
	m.View()
	x, y := findTarget(t, m, target{tAgent, "codex"})
	cmd := click(m, x, y, tea.MouseLeft)
	m.Update(cmd())
	m.View()
	if !slices.Equal(ran, []string{"agent", "show", "codex", "--json"}) {
		t.Fatalf("ran %v", ran)
	}
	if screen := m.last.c.plain(); !strings.Contains(screen, "npm @openai/codex@latest") || !strings.Contains(screen, "docs-bot") {
		t.Fatalf("agent popup:\n%s", screen)
	}
}

func TestPopupIsSquareAndEndsAtItsBorder(t *testing.T) {
	m := fixtureModel(t, 132, 50)
	m.Update(tea.KeyPressMsg{Text: "?", Code: '?'})
	m.View()
	c := m.last.c
	top, bottom, x0 := -1, -1, -1
	for y := range c.h {
		for x := range c.w {
			switch c.cells[y][x].text {
			case "┌":
				top, x0 = y, x
			case "└":
				bottom = y
			}
		}
	}
	if top < 0 || bottom < 0 || c.cells[top][x0].style.BG != m.theme.Popup {
		t.Fatalf("no square popup frame:\n%s", c.plain())
	}
	if below := c.cells[bottom+1][x0]; below.style.BG == m.theme.Popup {
		t.Errorf("popup fills a row below its bottom border:\n%s", c.plain())
	}
}

func TestDashboardIsInertBehindPopup(t *testing.T) {
	m := fixtureModel(t, 132, 50)
	var opened []string
	m.opts.OpenTab = func(box string) error { opened = append(opened, box); return nil }
	m.View()
	ax, ay := findTarget(t, m, target{tAccount, "personal"})
	m.Update(tea.MouseMotionMsg{X: ax, Y: ay})
	click(m, ax, ay, tea.MouseLeft)
	m.View()
	if m.popup == nil || m.hover != "" {
		t.Fatalf("popup=%v hover=%q", m.popup != nil, m.hover)
	}
	// Boxes and chat rooms still visible beside the popup ignore the pointer.
	for _, tg := range []target{{tBox, "api-refactor"}, {tBox, "room"}} {
		x, y := findTarget(t, m, tg)
		m.Update(tea.MouseMotionMsg{X: x, Y: y})
		m.View()
		if m.hover != "" || m.spotlight() != "" {
			t.Errorf("%s reacts to the pointer behind the popup", tg.id)
		}
	}
	rows := strings.Split(m.last.c.plain(), "\n")
	if footer := rows[len(rows)-1]; !strings.Contains(footer, "esc or a click outside closes") || strings.Contains(footer, "attach") {
		t.Errorf("footer describes the dashboard, not the popup: %q", footer)
	}
	cx, cy := findTarget(t, m, target{tPopupClose, ""})
	m.Update(tea.MouseMotionMsg{X: cx, Y: cy})
	if m.hover != (target{tPopupClose, ""}).key() {
		t.Error("the popup's close control does not react to the pointer")
	}
	// A click on a box behind the popup only closes it.
	x, y := findTarget(t, m, target{tBox, "api-refactor"})
	click(m, x, y, tea.MouseLeft)
	if m.popup != nil || len(opened) != 0 {
		t.Fatalf("popup=%v opened=%v", m.popup != nil, opened)
	}
}

func TestCanvasOverlayErasesWholeWideGraphemes(t *testing.T) {
	for _, tc := range []struct {
		name string
		x    int
		fill bool
		want string
	}{
		{name: "text over continuation", x: 1, want: " x  "},
		{name: "fill over continuation", x: 1, fill: true, want: "    "},
		{name: "text over leader", x: 0, want: "x   "},
		{name: "fill over leader", x: 0, fill: true, want: "    "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCanvas(4, 1)
			c.put(0, 0, line{{text: "界"}})
			if tc.fill {
				c.fill(tc.x, 0, 1, 1, Style{}, 0)
			} else {
				c.put(tc.x, 0, line{{text: "x"}})
			}
			if got := ansi.Strip(c.render()); got != tc.want {
				t.Fatalf("overlay = %q (%d cells), want %q", got, ansi.StringWidthWc(got), tc.want)
			}
		})
	}
}

func plainRows(m *Model) []string {
	m.View()
	return strings.Split(m.last.c.plain(), "\n")
}

func firstRow(rows []string, sub string) int {
	return slices.IndexFunc(rows, func(r string) bool { return strings.Contains(r, sub) })
}

// Icons get a two-column slot: a pictograph such as ✳ drawn wider by the font
// cannot cover the gap, and names line up across agents.
func TestAgentIconsKeepATwoColumnSlot(t *testing.T) {
	m := fixtureModel(t, 160, 50)
	for agent, icon := range config.DefaultAgentIcons {
		got := m.agentIcon(agent)
		if !strings.HasPrefix(got, icon.Icon) || textWidth(got) != max(2, textWidth(icon.Icon)) {
			t.Errorf("%s icon slot = %q", agent, got)
		}
	}
	if got := m.agentIcon("none"); got != "$ " {
		t.Errorf("agentless slot = %q", got)
	}
	rows := plainRows(m)
	offset := func(name string) int {
		y := slices.IndexFunc(rows, func(r string) bool { return strings.Contains(r, "╭") && strings.Contains(r, " "+name+" ") })
		if y < 0 {
			t.Fatalf("no card title for %s", name)
		}
		row := []rune(rows[y])
		at := strings.Index(rows[y], " "+name+" ")
		col := len([]rune(rows[y][:at])) + 1
		for x := col; x >= 0; x-- {
			if row[x] == '╭' {
				return col - x
			}
		}
		t.Fatalf("no card corner before %s", name)
		return 0
	}
	if claude, codex := offset("api-refactor"), offset("docs-bot"); claude != codex {
		t.Fatalf("names misaligned: ✳ card at %d, >_ card at %d", claude, codex)
	}
}

// The frame names the space and its backend, right-aligned, and stays the VM
// details target.
func TestVMFrameNamesTheSpaceRightAligned(t *testing.T) {
	for _, c := range []struct {
		backend, space string
		w              int
		suffix         string
	}{
		{"firecracker", "dev", 200, " dev · firecracker VM ═╗"},
		{"firecracker", "dev", 80, " dev · firecracker VM ═╗"},
		{"lima", "dev", 120, " dev · lima VM ═╗"},
		{"", "dev", 120, " dev · VM ═╗"},
		{"firecracker", strings.Repeat("long-space-", 12), 80, "… · firecracker VM ═╗"},
	} {
		// Tall enough that no scrollbar takes the frame's last column.
		m := fixtureModel(t, c.w, 200)
		m.opts.VM.Backend, m.opts.Space = c.backend, c.space
		rows := plainRows(m)
		y := firstRow(rows, "╔")
		if y < 0 || !strings.HasSuffix(rows[y], c.suffix) {
			t.Fatalf("%s/%d frame top = %q, want suffix %q", c.backend, c.w, rows[y], c.suffix)
		}
		sep := strings.LastIndex(rows[y], " · ")
		start := strings.LastIndex(rows[y][:sep], "═") + len("═ ")
		nameX, sepX := ansi.StringWidthWc(rows[y][:start]), ansi.StringWidthWc(rows[y][:sep])
		for x := nameX; x < c.w-2; x++ {
			if got, want := m.last.c.cells[y][x].style.Bold, x < sepX; got != want {
				t.Errorf("%s/%d cell %d bold=%v; want %v", c.backend, c.w, x, got, want)
			}
		}
		x := len([]rune(rows[y])) - len([]rune(c.suffix)) + 2
		if got, ok := m.last.targetAt(x, y); !ok || got != (target{tVMCPU, ""}) {
			t.Errorf("%s/%d label is not the VM target: %+v", c.backend, c.w, got)
		}
		if c.w == 200 && !strings.Contains(rows[y], "▼") {
			t.Errorf("pipes lost from the frame top: %q", rows[y])
		}
	}
}

func TestLegendPrecedesTheCPUGraph(t *testing.T) {
	for _, w := range []int{200, 120, 80} {
		rows := plainRows(fixtureModel(t, w, 60))
		legend, cpu := firstRow(rows, "■ api-refactor"), firstRow(rows, "║ CPU  ")
		if legend < 0 || cpu < 0 || legend > cpu {
			t.Errorf("%d: legend row %d, CPU row %d", w, legend, cpu)
		}
		if legend >= 0 && cpu >= 0 {
			graphX := ansi.StringWidthWc(rows[cpu][:strings.Index(rows[cpu], "▕")+len("▕")])
			for _, row := range rows[legend:cpu] {
				if at := strings.Index(row, "■"); at >= 0 && ansi.StringWidthWc(row[:at]) != graphX {
					t.Errorf("%d: legend starts at %d, graph fill at %d", w, ansi.StringWidthWc(row[:at]), graphX)
				}
			}
		}
	}
}

// The space summary joins the title row when it fits, in words and then as
// bare counts, and opens the body otherwise. The VM size is never repeated.
func TestHeaderMergesTheSummaryWhenItFits(t *testing.T) {
	for _, c := range []struct {
		w          int
		bar, notIn string
	}{
		{200, "3 host workdirs", ""},
		{160, "⌂3", "host workdirs"},
		{132, "⌂3", "host workdirs"},
		{120, "", "⌂3"},
		{64, "", "⌂3"},
	} {
		rows := plainRows(fixtureModel(t, c.w, 50))
		all := strings.Join(rows, "\n")
		if !strings.Contains(rows[0], "LIVE") || strings.Contains(all, "vCPU") || strings.Contains(all, "firecracker 8") {
			t.Errorf("%d: title %q", c.w, rows[0])
		}
		if c.bar != "" && (!strings.Contains(rows[0], c.bar) || !strings.Contains(rows[0], "running") || !strings.Contains(rows[0], "up ")) {
			t.Errorf("%d: summary not merged: %q", c.w, rows[0])
		}
		if c.notIn != "" && strings.Contains(all, c.notIn) {
			t.Errorf("%d: unexpected %q in\n%s", c.w, c.notIn, all)
		}
		if c.bar == "" && (strings.Contains(rows[0], "account") || !strings.Contains(rows[1], "3 host workdirs")) {
			t.Errorf("%d: two-row fallback missing: %q / %q", c.w, rows[0], rows[1])
		}
		if n := strings.Count(all, "host workdir"); n > 1 {
			t.Errorf("%d: summary repeated %d times", c.w, n)
		}
	}
}
