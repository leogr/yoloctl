package chatui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// These cover For me beside the channel name, Alt+M, Alt+F and filters for
// several people through behavior only, so they also show what the earlier
// single-account chip at the divider's right did differently.

func altKey(m *Model, r rune, mod tea.KeyMod, repeat bool) {
	m.Update(tea.KeyPressMsg(tea.Key{Code: r, Mod: tea.ModAlt | mod, IsRepeat: repeat}))
}

func dividerRow(t *testing.T, m *Model) (string, int) {
	t.Helper()
	for y, line := range frameLines(t, m) {
		if strings.HasPrefix(line, "─ #space") {
			return line, y
		}
	}
	t.Fatal("no composer divider")
	return "", 0
}

// The chip sits right after the channel name and its targets cover exactly
// its visible cells, ending at least one frame cell before the centered jump
// and the byte count, which keep their places.
func TestForMeChipFollowsTheChannelName(t *testing.T) {
	m, _, _ := filterModel(t)
	for i := range 60 {
		arrive(m, "n"+strings.Repeat("x", i), "mo", "opus", "line")
	}
	const nameCells = 9 // "─ #space "
	line, y := dividerRow(t, m)
	h, ok := hitWith(m, hitFilter, "toggle")
	if !ok || !strings.HasPrefix(line, "─ #space  For me ─") || h.y != y || h.x0 != nameCells || h.x1 != nameCells+len(" For me ") {
		t.Fatalf("For me after the channel: %+v %q", h, line)
	}
	press(m, tea.KeyPgUp, 0)
	paste(m, "draft")
	m.setFilter("mo", "opus")
	line, _ = dividerRow(t, m)
	jump, _, jumped := jumpHit(m)
	pick, ok1 := hitWith(m, hitFilter, "pick")
	clear, ok2 := hitWith(m, hitFilter, "clear")
	if !jumped || !ok1 || !ok2 || pick.x0 != nameCells || pick.x1 != clear.x0 || clear.x1 >= jump.x0 || !strings.HasPrefix(line, "─ #space  @opus ✕ ─") || !strings.HasSuffix(line, "─ 5 B ─") {
		t.Fatalf("chip, jump and count: pick=%+v clear=%+v jump=%+v %q", pick, clear, jump, line)
	}
	if count := len([]rune(line)) - len([]rune(" 5 B ─")); clear.x1 >= count {
		t.Fatalf("the chip reaches the count: %q", line)
	}
	// Narrow: the jump shortens and the chip gives way before it does.
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 20})
	line, _ = dividerRow(t, m)
	if _, chip := hitWith(m, hitFilter, "clear"); chip || !strings.Contains(line, "↓ Latest") {
		t.Fatalf("a narrow divider keeps the jump, not the chip: %q", line)
	}
}

// Alt+M switches exactly For me with all messages, and any other selection
// to For me. A repeat does nothing, lock keys do not matter, committed text
// is typing, and the draft never changes.
func TestAltMTogglesForMe(t *testing.T) {
	m, _, _ := filterModel(t)
	roomOfMentions(m)
	paste(m, "draft stays")
	forMe := []string{"c", "d", "e", "f", "g", "i", "j"}
	altKey(m, 'm', 0, false)
	if got := shownIDs(m); !slices.Equal(got, forMe) {
		t.Fatalf("Alt+M shows %v, want For me", got)
	}
	altKey(m, 'm', 0, true)
	if got := shownIDs(m); !slices.Equal(got, forMe) {
		t.Fatal("a repeated Alt+M toggled again")
	}
	altKey(m, 'm', tea.ModCapsLock|tea.ModNumLock, false)
	if m.filter != nil {
		t.Fatal("Alt+M with lock keys must return to all messages")
	}
	m.setFilter("mo", "opus")
	altKey(m, 'm', 0, false)
	if got := shownIDs(m); !slices.Equal(got, forMe) {
		t.Fatalf("Alt+M from another selection shows %v, want For me", got)
	}
	m.Update(tea.KeyPressMsg(tea.Key{Text: "µ"}))
	if got := shownIDs(m); !slices.Equal(got, forMe) || m.Draft() != "draft staysµ" {
		t.Fatalf("committed text acted: %v %q", got, m.Draft())
	}
	m2, _ := readyModel()
	altKey(m2, 'm', 0, false)
	if m2.filter != nil || !strings.Contains(m2.notice, "not known") {
		t.Fatalf("without an account Alt+M changes nothing: %q", m2.notice)
	}
}

// Alt+F opens a picker on a copy of the selection: Space checks several
// people, Enter shows what any of them sent or is mentioned in, and Esc or
// Alt+F again changes nothing. Text and pastes never toggle; All messages
// clears; a selected account that left stays removable.
func TestAltFPicksSeveralPeople(t *testing.T) {
	m, _, _ := filterModel(t)
	roomOfMentions(m)
	paste(m, "draft stays")
	altKey(m, 'f', 0, false)
	if m.overlay == nil || m.overlay.kind != overlayFilter {
		t.Fatal("Alt+F must open the picker")
	}
	press(m, tea.KeyDown, 0)
	press(m, tea.KeyDown, 0)
	press(m, tea.KeySpace, 0) // @opus
	press(m, tea.KeyDown, 0)
	paste(m, "pasted")
	m.Update(tea.KeyPressMsg(tea.Key{Text: "x", Code: 'x'}))
	press(m, tea.KeySpace, 0) // @qa
	if m.filter != nil || !strings.Contains(view(m), "Space select · Enter apply · Esc cancel") {
		t.Fatalf("checking applies nothing before Enter:\n%s", view(m))
	}
	press(m, tea.KeyEnter, 0)
	union := []string{"a", "b", "c", "e", "f", "g", "h", "i"}
	if got := shownIDs(m); m.overlay != nil || !slices.Equal(got, union) || m.Draft() != "draft stays" {
		t.Fatalf("opus or qa shows %v, want %v (draft %q)", got, union, m.Draft())
	}
	if line, _ := dividerRow(t, m); !strings.HasPrefix(line, "─ #space  2 people ✕ ─") {
		t.Fatalf("divider for two people: %q", line)
	}
	for _, cancel := range []func(){
		func() { press(m, tea.KeyEscape, 0) },
		func() { altKey(m, 'f', 0, false) },
	} {
		altKey(m, 'f', 0, false)
		press(m, tea.KeySpace, 0)
		cancel()
		if got := shownIDs(m); m.overlay != nil || !slices.Equal(got, union) {
			t.Fatalf("a canceled picker changed the filter: %v", got)
		}
	}
	altKey(m, 'f', 0, false)
	for range 4 {
		press(m, tea.KeyUp, 0)
	}
	press(m, tea.KeySpace, 0) // All messages
	press(m, tea.KeyEnter, 0)
	if m.filter != nil {
		t.Fatal("All messages and Enter must clear the filter")
	}
	_ = m.SetDraft("/filter stranger")
	m.suggest = nil
	press(m, tea.KeyEnter, 0)
	m.Update(MembersMsg{Members: []Member{{Nick: "leogr-aaaaa", Account: "mh"}, {Nick: "opus", Account: "mo"}}})
	altKey(m, 'f', 0, false)
	if out := view(m); !strings.Contains(out, "@stranger") || !strings.Contains(out, "not connected") {
		t.Fatalf("a selected account that left must stay listed:\n%s", out)
	}
	press(m, tea.KeySpace, 0)
	press(m, tea.KeyEnter, 0)
	if m.filter != nil {
		t.Fatal("removing the last selected account shows all messages")
	}
	m.openHelp()
	altKey(m, 'f', 0, false)
	if m.overlay == nil || m.overlay.kind != overlayHelp {
		t.Fatal("Alt+F must not take over another panel")
	}
}

// A filter for several people keeps the rules of one: each channel its own,
// hidden arrivals counted, unresolved sends shown, and a hidden reference
// target clears the whole selection and selects that message.
func TestSeveralPeopleKeepTheFilterRules(t *testing.T) {
	m, _, sends := filterModel(t)
	m.Update(MembershipMsg{Channel: "#build", State: "joined"})
	arrive(m, "11111111-1111-4111-8111-111111111111", "mx", "stranger", "the hidden original")
	m.Update(IncomingMsg{Message: Message{ID: "r", Account: "mo", Nick: "opus", Text: "replying", ReplyTo: "11111111-1111-4111-8111-111111111111"}})
	_ = m.SetDraft("/filter opus qa")
	m.suggest = nil
	press(m, tea.KeyEnter, 0)
	arrive(m, "s", "mx", "stranger", "not for them")
	if line, _ := dividerRow(t, m); !strings.HasPrefix(line, "─ #space  2 people +1 ✕ ─") {
		t.Fatalf("hidden arrival: %q", line)
	}
	m.Update(IncomingMsg{Message: Message{ID: "under", Account: "mx", Nick: "stranger", Text: "answering opus", ReplyTo: "r"}})
	if line, _ := dividerRow(t, m); !strings.HasPrefix(line, "─ #space  2 people +1 ✕ ─") || !slices.Contains(shownIDs(m), "under") {
		t.Fatalf("a reply under a selected account's message shows: %q %v", line, shownIDs(m))
	}
	m.switchTo("#build")
	if m.filter != nil {
		t.Fatal("another channel keeps its own filter")
	}
	m.switchTo("#space")
	paste(m, "about nobody")
	press(m, tea.KeyEnter, 0)
	m.Update(DeliveryMsg{ID: (*sends)[0].ID, State: Uncertain})
	if !slices.Contains(shownIDs(m), "client:"+(*sends)[0].ID) {
		t.Fatal("an uncertain send must stay visible")
	}
	m.View()
	cells := targetCells(m, actReply, "11111111-1111-4111-8111-111111111111")
	if len(cells) != 1 {
		t.Fatalf("reply label cells: %v", cells)
	}
	clickCell(m, cells[0])
	if m.filter != nil || m.selectedMessage() == nil || m.selectedMessage().ID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("a hidden target must clear the whole filter: %+v", m.filter)
	}
}
