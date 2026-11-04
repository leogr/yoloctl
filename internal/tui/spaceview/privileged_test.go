package spaceview

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/spacetelemetry"
)

const privilegedBox = "yolo-dev"

// scrollTo scrolls until want is on screen and returns its first cell.
func scrollTo(t *testing.T, m *Model, want target) (int, int) {
	t.Helper()
	for scroll := 0; scroll < 400; scroll++ {
		m.scroll = scroll
		m.View()
		f := m.last
		for y := range f.c.h {
			for x := range f.c.w {
				if got, ok := f.targetAt(x, y); ok && got == want {
					return x, y
				}
			}
		}
	}
	t.Fatalf("%dx%d: target %+v never on screen", m.w, m.h, want)
	return 0, 0
}

// pillsOf returns the yolo row's labelled segments as kind:text.
func pillsOf(m *Model, name string, inner int) []string {
	b := &builder{m: m, t: m.theme, targets: []target{{}}, index: map[string]int{}}
	box, _ := m.box(name)
	var out []string
	for _, sg := range b.yoloRow(box, inner) {
		if strings.TrimSpace(sg.text) != "" {
			out = append(out, b.targets[sg.hit].kind+":"+strings.TrimSpace(sg.text))
		}
	}
	return out
}

// A privileged box carries a raised warning pill on its card at every card
// width and a chip in the compact list; hovering explains it and a click
// opens box inspect.
func TestPrivilegedBoxIsMarkedOnItsCard(t *testing.T) {
	tg := target{tPrivileged, privilegedBox}
	for _, size := range [][2]int{{200, 60}, {160, 50}, {132, 50}, {120, 45}, {100, 40}, {80, 30}, {64, 30}, {48, 24}, {47, 40}, {40, 20}, {24, 10}} {
		m := privilegedFixtureModel(t, size[0], size[1])
		x, y := scrollTo(t, m, tg)
		row := strings.Split(m.last.c.plain(), "\n")[y]
		if !strings.Contains(row, "▲ privileged") || strings.Contains(row, "· runtime") {
			t.Errorf("%dx%d: pill row %q", size[0], size[1], row)
		}
		if cl := m.last.c.cells[y][x]; cl.style.BG != m.theme.OperatorBG || cl.style.FG != m.theme.OperatorFG {
			t.Errorf("%dx%d: pill drawn %+v, want the operator warning colors", size[0], size[1], cl.style)
		}
		m.Update(tea.MouseMotionMsg{X: x, Y: y})
		m.View()
		rows := strings.Split(m.last.c.plain(), "\n")
		if footer := rows[len(rows)-1]; size[0] >= 100 && !strings.Contains(footer, "privileged: space-VM root, no OpenShell") {
			t.Errorf("%dx%d: hover hint %q", size[0], size[1], footer)
		}
		x, y = scrollTo(t, m, tg)
		click(m, x, y, tea.MouseLeft)
		if m.popup == nil || m.popup.kind != "box" || !slices.Equal(m.popup.command, []string{"box", "inspect", privilegedBox, "--json", "--space", "dev"}) {
			t.Fatalf("%dx%d: click opened %+v", size[0], size[1], m.popup)
		}
	}
	// No other fixture box is marked.
	m := privilegedFixtureModel(t, 132, 50)
	for _, box := range m.d.inv.Boxes {
		if mark := privilegeMark(box); (mark != "") != (box.Name == privilegedBox) {
			t.Errorf("%s: mark %q", box.Name, mark)
		}
	}
}

// The privileged fixture fits every terminal size exactly, as the mockup does.
func TestPrivilegedFixtureFitsEveryWidth(t *testing.T) {
	for _, size := range sizes {
		m := privilegedFixtureModel(t, size[0], size[1])
		m.View()
		rows := strings.Split(m.last.c.render(), "\n")
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

// The pill names the declaration and adds what the running container's
// evidence says only when it disagrees or is missing; an unprivileged box
// with no privileged signal stays unmarked rather than claiming confinement.
func TestPrivilegedPillNamesDeclarationAndRuntimeDisagreement(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(*Model, int)
		want string
	}{
		{"running privileged", func(*Model, int) {}, "▲ privileged"},
		{"stopped", func(m *Model, i int) {
			b := &m.d.inv.Boxes[i]
			b.State, b.Runtime, b.Session, b.RuntimeMode, b.RuntimePrivileged, b.Container = "stopped", "stopped", "none", "", false, ""
		}, "▲ privileged"},
		{"running confined", func(m *Model, i int) {
			m.d.inv.Boxes[i].RuntimeMode, m.d.inv.Boxes[i].RuntimePrivileged = "confined", false
		}, "▲ privileged · running confined"},
		{"evidence unknown", func(m *Model, i int) {
			m.d.inv.Boxes[i].RuntimeMode, m.d.inv.Boxes[i].RuntimeModeReason = "", "labels disagree"
		}, "▲ privileged · runtime ?"},
		{"lookup failed", func(m *Model, i int) {
			m.d.inv.Boxes[i].RuntimeMode, m.d.inv.Boxes[i].RuntimePrivileged, m.d.inv.Boxes[i].Container = "", false, ""
		}, "▲ privileged · runtime ?"},
		{"undeclared privileged runtime", func(m *Model, i int) {
			m.d.inv.Boxes[i].Privileged, m.d.inv.Boxes[i].RuntimeMode = false, ""
		}, "▲ privileged runtime · declared confined"},
		{"undeclared fully privileged runtime", func(m *Model, i int) { m.d.inv.Boxes[i].Privileged = false },
			"▲ privileged runtime · declared confined"},
		{"confined", func(m *Model, i int) {
			b := &m.d.inv.Boxes[i]
			b.Privileged, b.RuntimeMode, b.RuntimePrivileged = false, "confined", false
		}, ""},
		{"undeclared unknown", func(m *Model, i int) {
			b := &m.d.inv.Boxes[i]
			b.Privileged, b.RuntimeMode, b.RuntimeModeReason, b.RuntimePrivileged = false, "", "runtime details unavailable", false
		}, ""},
	} {
		m := privilegedFixtureModel(t, 132, 50)
		i := slices.IndexFunc(m.d.inv.Boxes, func(b spacetelemetry.Box) bool { return b.Name == privilegedBox })
		c.set(m, i)
		var want []string
		if c.want != "" {
			want = []string{"privileged:" + c.want}
		}
		if got := pillsOf(m, privilegedBox, 200); !slices.Equal(got, want) {
			t.Errorf("%s: pills %q, want %q", c.name, got, want)
		}
		// A narrow card keeps the warning and drops only the suffix.
		if c.want != "" {
			if got := pillsOf(m, privilegedBox, 16); len(got) != 1 || got[0] != "privileged:▲ privileged" {
				t.Errorf("%s: short pills %q", c.name, got)
			}
		}
	}
}

// The title row counts privileged boxes beside the operator warning, in
// words from 90 columns when the row has room and otherwise as bare counts,
// never cut off.
func TestPrivilegedCountJoinsTheTitleRow(t *testing.T) {
	for _, w := range []int{200, 132, 90, 89, 64} {
		m := privilegedFixtureModel(t, w, 30)
		m.View()
		row := strings.Split(m.last.c.plain(), "\n")[0]
		words := strings.Contains(row, "▲ 1 operator box") && strings.Contains(row, "▲ 1 privileged")
		bare := strings.Count(row, "▲ 1 ") == 2 && !strings.Contains(row, "privileged")
		if w >= 132 && !words || w < 132 && !bare {
			t.Errorf("%d: title %q", w, row)
		}
		x, y := findTarget(t, m, target{tPrivileged, ""})
		if y != 0 {
			t.Fatalf("%d: title count at row %d", w, y)
		}
		m.Update(tea.MouseMotionMsg{X: x, Y: y})
		m.View()
		rows := strings.Split(m.last.c.plain(), "\n")
		if footer := rows[len(rows)-1]; !strings.Contains(footer, "privileged boxes") {
			t.Errorf("%d: title hint %q", w, footer)
		}
		click(m, x, y, tea.MouseLeft)
		if m.popup != nil {
			t.Errorf("%d: title count opened %+v", w, m.popup)
		}
	}
	m := privilegedFixtureModel(t, 132, 30)
	m.d.inv.Boxes = slices.DeleteFunc(m.d.inv.Boxes, func(b spacetelemetry.Box) bool { return b.Name == privilegedBox })
	m.View()
	if row := strings.Split(m.last.c.plain(), "\n")[0]; strings.Contains(row, "privileged") {
		t.Errorf("title without privileged boxes: %q", row)
	}
}
