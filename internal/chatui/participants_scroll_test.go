package chatui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// crowdModel is a room with n participants, some operators and voiced, in
// a window of the given size.
func crowdModel(n, width, height int) *Model {
	m, _ := readyModel()
	var members []Member
	for i := range n {
		members = append(members, Member{Nick: fmt.Sprintf("p%02d", n-1-i), Account: fmt.Sprintf("m%02d", i), Op: i%7 == 0, Voice: i%5 == 0})
	}
	m.Update(MembersMsg{Members: members})
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	if width < 80 {
		press(m, 'p', tea.ModCtrl) // narrow screens open the panel as an overlay
	}
	m.View()
	return m
}

// panelCards returns the members whose name row is visible, in screen
// order, and checks that each visible card shows its role row right below
// unless it is the last row before the "more" mark.
func panelCards(t *testing.T, m *Model) []int {
	t.Helper()
	m.View()
	var rows []hit
	for _, h := range m.hits {
		if h.area == hitPanel {
			rows = append(rows, h)
		}
	}
	var cards []int
	for i, h := range rows {
		if i > 0 && rows[i-1].item == h.item {
			if h.y != rows[i-1].y+1 {
				t.Fatalf("card %d is split: rows %d and %d", h.item, rows[i-1].y, h.y)
			}
			continue
		}
		cards = append(cards, h.item)
	}
	return cards
}

var moreMark = regexp.MustCompile(`▲ (\d+) above|▼ (\d+) more`)

func marks(m *Model) (above, below int) {
	for _, g := range moreMark.FindAllStringSubmatch(view(m), -1) {
		if g[1] != "" {
			above, _ = strconv.Atoi(g[1])
		} else {
			below, _ = strconv.Atoi(g[2])
		}
	}
	return
}

// Many participants on a short screen keep their two-row cards and scroll
// by whole cards, side panel and narrow overlay alike: nothing is dropped
// or reordered, the marks count what is hidden, and the wheel over the
// panel and Alt+PgUp/PgDn move it without touching the transcript.
func TestParticipantsScrollInsteadOfShrinking(t *testing.T) {
	const n = 30
	for _, size := range [][2]int{{120, 12}, {100, 16}, {80, 24}, {60, 14}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m := crowdModel(n, size[0], size[1])
			if m.layout.cardWidth == 0 || m.layout.rosterPage == 0 {
				t.Fatalf("expected scrolled cards: card width %d page %d\n%s", m.layout.cardWidth, m.layout.rosterPage, view(m))
			}
			order := sortedMembers(m.members)
			seen := map[int]bool{}
			prev := -1
			for step := 0; step < 2*n; step++ {
				cards := panelCards(t, m)
				if len(cards) == 0 {
					t.Fatalf("step %d shows no card:\n%s", step, view(m))
				}
				above, below := marks(m)
				if cards[0] != above || above+len(cards)+below != n {
					t.Fatalf("step %d: above %d, cards %v, below %d of %d", step, above, cards, below, n)
				}
				for i, c := range cards {
					if i > 0 && c != cards[i-1]+1 {
						t.Fatalf("cards out of order: %v", cards)
					}
					if memberKey(m.layout.panelMembers[c]) != memberKey(order[c]) {
						t.Fatal("sorting changed")
					}
					seen[c] = true
				}
				if cards[0] == prev {
					break // the end: scrolling further changes nothing
				}
				prev = cards[0]
				x, y := hitFor(t, m, hitPanel, cards[0])
				scroll := m.scroll
				m.Update(tea.MouseWheelMsg{X: x, Y: y, Button: tea.MouseWheelDown})
				if m.scroll != scroll {
					t.Fatal("the wheel over the panel scrolled the transcript")
				}
			}
			if len(seen) != n {
				t.Fatalf("scrolling showed %d of %d participants", len(seen), n)
			}
			// A click on a scrolled card names that member.
			cards := panelCards(t, m)
			x, y := hitFor(t, m, hitPanel, cards[0])
			if h, ok := m.hitAt(x, y); !ok || h.id != memberKey(order[cards[0]]) {
				t.Fatalf("hit %+v for card %d", h, cards[0])
			}
			// The keys page back to the top, one page at a time.
			first := cards[0]
			press(m, tea.KeyPgUp, tea.ModAlt)
			if got := panelCards(t, m)[0]; got != max(first-m.layout.rosterPage, 0) {
				t.Fatalf("Alt+PgUp from %d went to %d (page %d)", first, got, m.layout.rosterPage)
			}
			for range n {
				press(m, tea.KeyPgUp, tea.ModShift)
			}
			if got := panelCards(t, m)[0]; got != 0 || strings.Contains(view(m), "▲") {
				t.Fatalf("the top shows card %d:\n%s", got, view(m))
			}
			press(m, tea.KeyPgDown, tea.ModAlt)
			if got := panelCards(t, m)[0]; got != m.layout.rosterPage {
				t.Fatalf("Alt+PgDn went to %d (page %d)", got, m.layout.rosterPage)
			}
			// Ctrl+P still hides the panel; the keys then do nothing.
			press(m, 'p', tea.ModCtrl)
			m.View()
			scroll := m.rosterScroll
			press(m, tea.KeyPgDown, tea.ModAlt)
			if m.rosterScroll != scroll || strings.Contains(view(m), "PARTICIPANTS") {
				t.Fatal("a hidden panel scrolled or stayed visible")
			}
		})
	}
}

// When every card fits, nothing scrolls and the closing note shows with
// room for it.
func TestParticipantsFitWithoutScrolling(t *testing.T) {
	m := crowdModel(3, 120, 30)
	if m.layout.rosterPage != 0 || strings.ContainsAny(view(m), "▲▼") || !strings.Contains(view(m), "Click a box to attach.") {
		t.Fatalf("three participants fit:\n%s", view(m))
	}
	m.Update(tea.MouseWheelMsg{X: m.layout.panelX0 + 1, Y: m.layout.bodyTop + 3, Button: tea.MouseWheelDown})
	if m.rosterScroll != 0 {
		t.Fatal("a panel that fits does not scroll")
	}
}

// With the channel rail above it, a short screen can leave the
// participants no row at all, or only their heading; shrinking while
// scrolled keeps every frame valid and growing again shows cards.
func TestParticipantsTinyPanelWithChannelRail(t *testing.T) {
	m := crowdModel(30, 120, 30)
	m.view("#second") // two channels: the rail takes rows above the participants
	m.View()
	for range 10 {
		m.scrollRoster(1)
		m.View()
	}
	for _, height := range []int{30, 14, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 13, 20} {
		m.Update(tea.WindowSizeMsg{Width: 120, Height: height})
		func() {
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("height %d panicked: %v", height, p)
				}
			}()
			m.View()
		}()
		if m.rosterScroll < 0 || m.rosterScroll > len(m.members) {
			t.Fatalf("height %d: offset %d out of bounds", height, m.rosterScroll)
		}
	}
	if len(panelCards(t, m)) == 0 {
		t.Fatalf("a grown panel shows cards again:\n%s", view(m))
	}
}

// Page keys alone traverse every participant, down and back up, with no
// gap between consecutive pages, at heights whose odd row budgets once
// made a page larger than the cards shown.
func TestParticipantsPageKeysNeverSkip(t *testing.T) {
	for _, height := range []int{10, 11, 12, 13, 14, 15, 16, 17, 24} {
		t.Run(fmt.Sprint(height), func(t *testing.T) {
			m := crowdModel(30, 120, height)
			for _, dir := range []tea.Key{{Code: tea.KeyPgDown, Mod: tea.ModAlt}, {Code: tea.KeyPgUp, Mod: tea.ModAlt}} {
				seen := map[int]bool{}
				prev := panelCards(t, m)
				for _, c := range prev {
					seen[c] = true
				}
				for range 40 {
					m.Update(tea.KeyPressMsg(dir))
					cards := panelCards(t, m)
					if len(cards) == 0 {
						t.Fatalf("no card shown:\n%s", view(m))
					}
					if dir.Code == tea.KeyPgDown && cards[0] > prev[len(prev)-1]+1 || dir.Code == tea.KeyPgUp && cards[len(cards)-1] < prev[0]-1 {
						t.Fatalf("page gap: %v then %v (page %d)", prev, cards, m.layout.rosterPage)
					}
					for _, c := range cards {
						seen[c] = true
					}
					prev = cards
				}
				if len(seen) != 30 {
					t.Fatalf("paging %v showed %d of 30", dir, len(seen))
				}
			}
		})
	}
}
