package chatui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestChannelsRemainVisibleAcrossWidths(t *testing.T) {
	for _, width := range []int{160, 120, 100, 99, 80, 79, 60, 40, 24, 20, 19, 10, 3, 1} {
		for _, roster := range []bool{true, false} {
			for _, overlay := range []bool{true, false} {
				t.Run(fmt.Sprintf("w%d/roster%v/overlay%v", width, roster, overlay), func(t *testing.T) {
					m, _, actions, _ := selectionModel(t)
					m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
					m.rosterOpen, m.rosterOverlay = roster, overlay
					content := m.View().Content
					hits := make(map[int]hit)
					for _, h := range m.hits {
						if h.area == hitChannel {
							hits[h.item] = h
						}
					}
					for i := range m.order {
						h, ok := hits[i]
						if !ok {
							if !strings.Contains(ansi.Strip(content), "+") {
								t.Fatalf("channel %s and overflow disappeared: %q", m.order[i], ansi.Strip(content))
							}
							continue
						}
						if h.x0 < 0 || h.x1 > width || h.x0 >= h.x1 || h.y < 0 || h.y >= 30 {
							t.Fatalf("invalid channel hit: %+v", h)
						}
					}
					for _, row := range strings.Split(content, "\n") {
						if ansi.StringWidth(row) > width {
							t.Fatalf("overflow at width%d: %q", width, row)
						}
					}
					h, visible := hits[1]
					if !visible {
						for _, target := range m.hits {
							if target.area == hitTop && target.id == "channels" && target.y > 0 {
								clickAt(m, target.x0, target.y)
								break
							}
						}
						m.View()
						for _, target := range m.hits {
							if target.area == hitOverlayRow && target.item == 1 {
								h = target
							}
						}
					}
					cmd := clickAt(m, h.x0, h.y)
					if cmd == nil || len(*actions) != 1 || (*actions)[0].Kind != "join" || (*actions)[0].Channel != "#build" {
						t.Fatalf("channel click lost at width%d: %+v", width, *actions)
					}
				})
			}
		}
	}
}

func TestCompactChannelsKeepCurrentAtMinimumHeight(t *testing.T) {
	m, _, _, _ := selectionModel(t)
	m.switchTo("#other")
	for _, size := range [][2]int{{20, 7}, {10, 7}, {1, 1}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.View()
		found := false
		for _, h := range m.hits {
			if h.area == hitChannel && m.order[h.item] == "#other" {
				found = true
			}
		}
		if !found {
			t.Fatalf("current channel hidden at %v", size)
		}
	}
}

func TestChannelStripBoundsAndBadges(t *testing.T) {
	for _, size := range [][2]int{{79, 24}, {60, 20}, {40, 16}, {120, 30}, {20, 7}} {
		for n := 2; n <= 16; n++ {
			m, _ := readyModel()
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m.rosterOpen = false
			m.View()
			baseline := m.layout.bodyHeight
			for i := 1; i < n; i++ {
				m.view(fmt.Sprintf("#channel%d", i))
			}
			m.views[m.order[1]].unread = 3
			m.views[m.order[1]].unreadMentions = 1
			m.View()
			if baseline-m.layout.bodyHeight != 1 {
				t.Fatalf("%v/%d channels costs %d rows", size, n, baseline-m.layout.bodyHeight)
			}
			strip := m.channelStrip(size[0], 2)
			plain := ansi.Strip(strip)
			if !strings.Contains(plain, "@1 3") {
				t.Fatalf("badges lost: %q", plain)
			}
			m.switchTo(m.order[n-1])
			m.View()
			found := false
			for _, h := range m.hits {
				if h.area == hitChannel && h.item == n-1 {
					found = true
				}
			}
			if !found {
				t.Fatalf("current channel hidden: %v/%d", size, n)
			}
			for selected := 0; selected < n; selected++ {
				m.switchTo(m.order[selected])
				frame := m.View().Content
				last := -1
				visible := 0
				currentShown := false
				for _, h := range m.hits {
					if h.area != hitChannel {
						continue
					}
					if h.item <= last {
						t.Fatalf("strip reordered channels: %v/%d", size, n)
					}
					last = h.item
					row := strings.Split(frame, "\n")[h.y]
					buf := bufferRow(row, m.width, m.widthMethod)
					var label strings.Builder
					for x := h.x0; x < h.x1; x++ {
						if c := buf.CellAt(x, 0); c != nil {
							label.WriteString(c.Content)
						}
					}
					name := strings.Fields(label.String())[0]
					if name == "…" || name == "#…" {
						t.Fatalf("unreadable channel stub: %q", label.String())
					}
					visible++
					currentShown = currentShown || h.item == selected
				}
				if !currentShown {
					t.Fatalf("channel %d not visible", selected)
				}
				if visible < n {
					var overflowHit *hit
					for _, h := range m.hits {
						if h.area == hitTop && h.id == "channels" && h.y > 0 {
							v := h
							overflowHit = &v
							break
						}
					}
					if overflowHit == nil {
						t.Fatal("hidden channels lack picker")
					}
					clickAt(m, overflowHit.x0, overflowHit.y)
					if m.overlay == nil || m.overlay.kind != overlayChannels {
						t.Fatal("overflow did not open picker")
					}
					m.overlay = nil
				}
			}
		}
	}
}

func TestShortChannelStripsNeedNoOverflow(t *testing.T) {
	for _, tc := range []struct {
		width int
		names []string
		badge bool
	}{
		{20, []string{"#space", "#falco"}, true},
		{24, []string{"#space", "#falco", "#ux"}, false},
		{30, []string{"#space", "#falco", "#ux"}, true},
		{20, []string{"#a", "#b", "#c", "#d"}, false},
		{24, []string{"#a", "#b", "#c", "#d"}, false},
		{30, []string{"#one", "#two", "#three", "#four"}, false},
	} {
		m, _ := readyModel()
		m.order = nil
		for _, name := range tc.names {
			m.view(name)
		}
		// Existing default channel may already be present; pin the test order.
		m.order = tc.names
		m.switchTo(tc.names[0])
		if tc.badge {
			m.views[tc.names[1]].unread = 3
			m.views[tc.names[1]].unreadMentions = 1
		}
		row := ansi.Strip(m.channelStrip(tc.width, 0))
		for _, name := range tc.names {
			if !strings.Contains(row, name) {
				t.Fatalf("width%d: %s hidden in %q", tc.width, name, row)
			}
		}
		if strings.Contains(row, "+") || strings.Contains(row, "…") {
			t.Fatalf("unneeded overflow/ellipsis: %q", row)
		}
	}
}
