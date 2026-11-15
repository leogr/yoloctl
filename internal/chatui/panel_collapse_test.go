package chatui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// panelModel is a wide two-channel view whose side panel lists both
// channels and three participants.
func panelModel(t *testing.T) *Model {
	t.Helper()
	m, _, _ := channelModel(t)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(MembersMsg{Channel: "#space", Members: []Member{{Nick: "human", Account: "mh"}, {Nick: "opus", Account: "mo"}, {Nick: "sol", Account: "ms"}}})
	return m
}

// panelText is the side panel's text, one row per screen row.
func panelText(m *Model) []string {
	tw := m.transcriptWidth()
	var rows []string
	for _, line := range strings.Split(view(m), "\n") {
		if _, side, ok := strings.Cut(line, "│"); ok && len([]rune(line)) > tw {
			rows = append(rows, side)
		}
	}
	return rows
}

// clickHeading clicks the side panel heading that starts with title.
func clickHeading(t *testing.T, m *Model, title string) {
	t.Helper()
	view(m)
	for _, h := range m.hits {
		if h.area == hitPanelHead && h.id == strings.ToLower(title) {
			click(m, h.x0+1, h.y)
			return
		}
	}
	t.Fatalf("no %s heading to click:\n%s", title, view(m))
}

func panelHas(m *Model, text string) bool {
	return strings.Contains(strings.Join(panelText(m), "\n"), text)
}

// Clicking a list's heading collapses that list to its heading, keeping its
// count; clicking again restores it. The other list is unaffected, and the
// settings panel toggles each list too.
func TestSidePanelListsCollapseFromTheirHeadings(t *testing.T) {
	m := panelModel(t)
	if !panelHas(m, "▾ CHANNELS") || !panelHas(m, "#build") || !panelHas(m, "▾ PARTICIPANTS") || !panelHas(m, "opus") {
		t.Fatalf("both lists start expanded:\n%s", strings.Join(panelText(m), "\n"))
	}

	clickHeading(t, m, "CHANNELS")
	if !panelHas(m, "▸ CHANNELS") || panelHas(m, "#build") || !panelHas(m, "opus") {
		t.Fatalf("the channels list collapses to its heading only:\n%s", strings.Join(panelText(m), "\n"))
	}
	for _, h := range m.hits {
		if h.area == hitChannel && h.x0 > 0 {
			t.Fatal("a collapsed channels list leaves no channel target in the panel")
		}
	}

	clickHeading(t, m, "PARTICIPANTS")
	if !panelHas(m, "▸ PARTICIPANTS") || !strings.Contains(strings.Join(panelText(m), "\n"), "3") || panelHas(m, "opus") {
		t.Fatalf("the participants list collapses to its heading and count:\n%s", strings.Join(panelText(m), "\n"))
	}
	for _, h := range m.hits {
		if h.area == hitPanel {
			t.Fatal("a collapsed participants list leaves no participant target")
		}
	}

	clickHeading(t, m, "CHANNELS")
	clickHeading(t, m, "PARTICIPANTS")
	if !panelHas(m, "#build") || !panelHas(m, "opus") {
		t.Fatalf("a second click expands each list again:\n%s", strings.Join(panelText(m), "\n"))
	}

	for _, id := range []string{"channels", "participants"} {
		m.activateSetting(id)
	}
	if panelHas(m, "#build") || panelHas(m, "opus") {
		t.Fatalf("the settings rows collapse each list:\n%s", strings.Join(panelText(m), "\n"))
	}
}

// The heading names what a click does.
func TestPanelHeadingHoverHint(t *testing.T) {
	m := panelModel(t)
	view(m)
	for _, h := range m.hits {
		if h.area == hitPanelHead && h.id == "participants" {
			m.Update(tea.MouseMotionMsg{X: h.x0 + 1, Y: h.y})
		}
	}
	if !strings.Contains(view(m), "click to collapse the list") {
		t.Fatalf("hovering a heading explains the click:\n%s", view(m))
	}
}
