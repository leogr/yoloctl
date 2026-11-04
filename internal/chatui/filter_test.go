package chatui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// filterModel is the room as human mh (saved nick leogr, name Leo) sees it
// through the live alias leogr-aaaaa while a second view holds leogr-bbbbb,
// with agents opus and qa and one unverified session.
func filterModel(t *testing.T) (*Model, *[]string, *[]Outgoing) {
	t.Helper()
	var copies []string
	var sends []Outgoing
	m := New(Options{Space: "trial", Nick: "leogr-aaaaa", Account: "mh", Identities: map[string]Identity{
		"mh": {Name: "Leo", Nick: "leogr", Human: true},
		"mo": {Name: "opus", Nick: "opus", Box: true},
		"mq": {Name: "qa", Nick: "qa", Box: true},
	}, Send: func(out Outgoing) tea.Cmd {
		sends = append(sends, out)
		return nil
	}, Copy: func(text string) tea.Cmd {
		copies = append(copies, text)
		return nil
	}})
	m.Update(ConnectionMsg{Connected: true})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m.Update(MembersMsg{Members: []Member{{Nick: "leogr-aaaaa", Account: "mh"}, {Nick: "leogr-bbbbb", Account: "mh"},
		{Nick: "opus", Account: "mo"}, {Nick: "qa", Account: "mq"}, {Nick: "stranger", Account: "mx"}}})
	return m, &copies, &sends
}

// filterAccounts lists the visible view's filtered accounts, in the order chosen.
func filterAccounts(m *Model) []string {
	if m.filter == nil {
		return nil
	}
	var out []string
	for _, p := range m.filter.picks {
		out = append(out, p.account)
	}
	return out
}

func arrive(m *Model, id, account, nick, text string) {
	m.Update(IncomingMsg{Message: Message{ID: id, Account: account, Nick: nick, Text: text, Time: time.Unix(1000, 0)}})
}

// shownIDs renders a frame and lists the messages whose rows it built.
func shownIDs(m *Model) []string {
	m.View()
	var ids []string
	for _, row := range m.lastTranscript {
		if i := m.indexOf(row.key); row.kind == rowHeader && i >= 0 {
			id := m.messages[i].ID
			if id == "" {
				id = "client:" + m.messages[i].ClientID
			}
			ids = append(ids, id)
		}
	}
	return ids
}

func roomOfMentions(m *Model) {
	arrive(m, "a", "mo", "opus", "status from opus")
	arrive(m, "b", "mq", "qa", "@opus please check")
	arrive(m, "c", "mq", "qa", "@leogr ready")
	arrive(m, "d", "mh", "leogr-bbbbb", "sent from my other view")
	arrive(m, "e", "mq", "qa", "@leogr-aaaaa this view")
	arrive(m, "f", "mq", "qa", "hi @LEO by name")
	arrive(m, "g", "mq", "qa", "by account @mh")
	arrive(m, "h", "mq", "qa", "noise @opusx me@opus @all @here @leogr_x `x` @289")
	arrive(m, "i", "mq", "qa", "(@opus, @leogr.)")
	arrive(m, "j", "mx", "stranger", "@leogr from an unverified session")
}

// For me shows what I sent from any view and every exact mention of my
// saved nick, live aliases, trusted name or account; a participant's filter
// shows what they sent or that mentions them. Token boundaries are the
// mention boundaries the transcript already uses; @all is no one's name.
func TestFilterShowsMessagesSentByOrMentioningTheAccount(t *testing.T) {
	m, _, _ := filterModel(t)
	roomOfMentions(m)
	before := m.Messages()
	m.toggleForMe()
	if got, want := shownIDs(m), []string{"c", "d", "e", "f", "g", "i", "j"}; !slices.Equal(got, want) {
		t.Fatalf("For me shows %v, want %v", got, want)
	}
	m.setFilter("mo", "opus")
	if got, want := shownIDs(m), []string{"a", "b", "i"}; !slices.Equal(got, want) {
		t.Fatalf("opus filter shows %v, want %v", got, want)
	}
	m.setFilter("", "")
	if got := shownIDs(m); len(got) != 10 {
		t.Fatalf("All messages shows %v", got)
	}
	if !slices.EqualFunc(before, m.Messages(), func(a, b Message) bool { return a.ID == b.ID && a.Text == b.Text && a.Delivery == b.Delivery }) {
		t.Fatal("filtering changed the underlying messages")
	}
}

// A nick two accounts used names neither, and an unverified session cannot
// take a trusted name: the catalog binds it first. A nick only seen on a
// session with an unknown account still opens that session's card.
func TestFilterNeverGuessesAmbiguousOrUntrustedNames(t *testing.T) {
	m, _, _ := filterModel(t)
	m.Update(MembersMsg{Members: append(slices.Clone(m.members), Member{Nick: "leogr", Account: "mz"}, Member{Nick: "ghost"})})
	arrive(m, "k", "m1", "bob", "first bob")
	arrive(m, "l", "m2", "bob", "second bob")
	arrive(m, "n", "mq", "qa", "@bob which one?")
	arrive(m, "o", "mq", "qa", "@leogr catalog name")
	arrive(m, "p", "mz", "leogr", "an impostor speaks")
	m.setFilter("m1", "bob")
	if got := shownIDs(m); !slices.Equal(got, []string{"k"}) {
		t.Fatalf("an ambiguous nick was attributed: %v", got)
	}
	m.setFilter("mz", "leogr")
	if got := shownIDs(m); !slices.Equal(got, []string{"p"}) {
		t.Fatalf("an unverified session took a trusted name: %v", got)
	}
	m.toggleForMe() // another selection becomes For me
	if got := shownIDs(m); !slices.Contains(got, "o") || slices.Contains(got, "p") {
		t.Fatalf("the catalog nick names the human: %v", got)
	}
	m.setFilter("", "")
	m.mentionCard("bob")
	if m.overlay != nil || !strings.Contains(m.notice, "more than one") {
		t.Fatalf("an ambiguous mention opened a card: %+v %q", m.overlay, m.notice)
	}
	m.mentionCard("leogr")
	if m.overlay == nil || m.overlay.who.account != "mh" {
		t.Fatalf("the saved nick must open the human's card: %+v", m.overlay)
	}
	m.overlay = nil
	m.mentionCard("leogr-bbbbb")
	if m.overlay == nil || m.overlay.who.account != "mh" {
		t.Fatalf("an exact live alias must stay recognized: %+v", m.overlay)
	}
	m.overlay = nil
	m.mentionCard("ghost")
	if m.overlay == nil || m.overlay.member == nil || m.overlay.member.Nick != "ghost" {
		t.Fatalf("an unbound nick keeps the connected-session card: %+v", m.overlay)
	}
}

// Each channel keeps its own filter across switches; an arrival elsewhere
// never counts against it.
func TestFilterStaysWithItsChannel(t *testing.T) {
	m, _, _ := filterModel(t)
	m.Update(MembershipMsg{Channel: "#build", State: "joined"})
	roomOfMentions(m)
	m.Update(IncomingMsg{Message: Message{ID: "x1", Channel: "#build", Account: "mo", Nick: "opus", Text: "build news"}})
	m.toggleForMe()
	m.switchTo("#build")
	if m.filter != nil || !slices.Equal(shownIDs(m), []string{"x1"}) {
		t.Fatal("another channel must show all of its messages")
	}
	m.switchTo("#space")
	m.Update(IncomingMsg{Message: Message{ID: "x2", Channel: "#build", Account: "mo", Nick: "opus", Text: "more news"}})
	if m.filter == nil || !slices.Equal(filterAccounts(m), []string{"mh"}) || m.filter.hiddenNew != 0 || !slices.Contains(shownIDs(m), "c") {
		t.Fatalf("the channel's filter must come back unchanged: %+v", m.filter)
	}
}

// With nothing shown, Page Up still asks once for the page before the
// oldest underlying message and states the loaded scope; the page arriving
// never chains another request.
func TestFilteredPagingLoadsOlderOnlyOnRequest(t *testing.T) {
	m, _, _ := filterModel(t)
	var requests []HistoryRequest
	m.opts.LoadOlder = func(r HistoryRequest) tea.Cmd { requests = append(requests, r); return nil }
	arrive(m, "q1", "mq", "qa", "unrelated one")
	arrive(m, "q2", "mq", "qa", "unrelated two")
	m.setFilter("mo", "opus")
	if out := view(m); len(shownIDs(m)) != 0 || !strings.Contains(out, "0 of 2 loaded messages · Page Up loads older history") || !strings.Contains(out, "✕ All messages") {
		t.Fatalf("an empty filter must state its scope:\n%s", out)
	}
	press(m, tea.KeyPgUp, 0)
	if len(requests) != 1 || requests[0].Before != "q1" {
		t.Fatalf("page before the oldest underlying message: %+v", requests)
	}
	for _, id := range []string{"o1", "o2"} {
		m.Update(IncomingMsg{Message: Message{ID: id, Account: "mq", Nick: "qa", Text: "older noise", Older: true}})
	}
	m.Update(HistoryPageMsg{ID: requests[0].ID, Count: 2})
	if len(requests) != 1 || !strings.Contains(m.notice, "Loaded 2 older messages; 0 shown by the filter") {
		t.Fatalf("a page must not chain: %d requests, %q", len(requests), m.notice)
	}
	if out := view(m); !strings.Contains(out, "0 of 4 loaded messages") {
		t.Fatalf("scope counts the loaded page:\n%s", out)
	}
	press(m, tea.KeyPgUp, 0)
	if len(requests) != 2 || requests[1].Before != "o1" {
		t.Fatalf("the next explicit Page Up pages on: %+v", requests)
	}
	m.Update(IncomingMsg{Message: Message{ID: "o0", Account: "mo", Nick: "opus", Text: "older match", Older: true}})
	m.Update(HistoryPageMsg{ID: requests[1].ID, Count: 1})
	if got := shownIDs(m); !slices.Equal(got, []string{"o0"}) || !strings.Contains(m.notice, "1 shown by the filter") {
		t.Fatalf("an older match: %v %q", got, m.notice)
	}
	press(m, tea.KeyPgUp, 0)
	m.Update(HistoryPageMsg{ID: requests[2].ID, Count: 0})
	if out := view(m); !strings.Contains(out, "1 of 5 loaded messages · from the beginning of retained history") {
		t.Fatalf("history end:\n%s", out)
	}
}

// Hidden arrivals count on the filter chip without moving the reading
// position or the unread count. Implicit and stepped targets, copied
// references and a drag copy never reach a hidden message.
func TestFilterArrivalsAndTargetsStayOnShownMessages(t *testing.T) {
	const a = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	m, copies, _ := filterModel(t)
	arrive(m, a, "mo", "opus", "first from opus")
	arrive(m, "b", "mq", "qa", "hidden middle")
	arrive(m, "c", "mq", "qa", "@opus second")
	m.setFilter("mo", "opus")
	arrive(m, "d", "mq", "qa", "hidden latest")
	if m.filter.hiddenNew != 1 || m.unread != 0 || !strings.Contains(view(m), "@opus +1 ✕") {
		t.Fatalf("hidden arrival: %+v unread=%d\n%s", m.filter, m.unread, view(m))
	}
	press(m, 'r', tea.ModCtrl)
	if m.replyTo != "c" {
		t.Fatalf("Ctrl+R must target the latest shown message, got %q", m.replyTo)
	}
	press(m, tea.KeyUp, tea.ModAlt)
	if msg := m.selectedMessage(); msg == nil || msg.ID != a {
		t.Fatalf("Alt+Up must step over hidden messages: %+v", msg)
	}
	m.runAction("reference")
	if ref, _ := compactRef(a); len(*copies) != 1 || (*copies)[0] != ref {
		t.Fatalf("copy reference: %v", *copies)
	}
	m.View()
	x0, y0 := cellOf(t, m, "server:"+a, 0, 0)
	x1, y1 := cellOf(t, m, "server:c", 0, len("@opus second"))
	drag(m, x0, y0, x1, y1)
	if got := (*copies)[len(*copies)-1]; got != "first from opus\n\n@opus second" {
		t.Fatalf("a drag copy included a hidden message: %q", got)
	}
}

// A send keeps its outcome visible under any filter, and an uncertain one
// keeps its draft; nothing about it changes when the filter does.
func TestFilterKeepsSendOutcomesAndDrafts(t *testing.T) {
	m, _, sends := filterModel(t)
	m.setFilter("mo", "opus")
	paste(m, "not about opus")
	press(m, tea.KeyEnter, 0)
	if len(*sends) != 1 || !slices.Contains(shownIDs(m), "client:"+(*sends)[0].ID) {
		t.Fatal("a pending send must stay visible")
	}
	m.Update(DeliveryMsg{ID: (*sends)[0].ID, State: Uncertain})
	if !slices.Contains(shownIDs(m), "client:"+(*sends)[0].ID) || m.Draft() != "not about opus" {
		t.Fatalf("uncertain send hidden or draft lost: %q", m.Draft())
	}
	m.setFilter("", "")
	m.toggleForMe()
	if m.Draft() != "not about opus" || m.inflight == "" || m.Messages()[0].Delivery != Uncertain {
		t.Fatal("changing the filter touched the unresolved send")
	}
}

// A reply label or msgid reference to a loaded message the filter hides
// shows all messages again and selects that same message.
func TestHiddenReferenceTargetClearsTheFilter(t *testing.T) {
	m, _, _ := filterModel(t)
	arrive(m, "11111111-1111-4111-8111-111111111111", "mq", "qa", "the hidden original")
	m.Update(IncomingMsg{Message: Message{ID: "r", Account: "mo", Nick: "opus", Text: "replying", ReplyTo: "11111111-1111-4111-8111-111111111111"}})
	m.setFilter("mo", "opus")
	m.View()
	cells := targetCells(m, actReply, "")
	if len(cells) != 1 {
		t.Fatalf("reply label cells: %v", cells)
	}
	clickCell(m, cells[0])
	if m.filter != nil || m.selectedMessage() == nil || m.selectedMessage().ID != "11111111-1111-4111-8111-111111111111" || !m.picked {
		t.Fatalf("a hidden target must clear the filter and select the same ID: %+v", m.filter)
	}
}

// The composer divider's chip, right after the channel name, toggles For
// me; its ✕ and the scope row's All messages clear a filter, and another
// selection's name opens the picker. The top band gains no button, and the
// frame fits from the widest to the narrowest view, which leaves out a chip
// with no room.
func TestFilterChipAndScopeRowToggle(t *testing.T) {
	m, _, _ := filterModel(t)
	roomOfMentions(m)
	divider := func(lines []string) string {
		for _, line := range lines {
			if strings.HasPrefix(line, "─ #space") {
				return line
			}
		}
		t.Fatalf("no divider:\n%s", strings.Join(lines, "\n"))
		return ""
	}
	if line := divider(frameLines(t, m)); !strings.HasPrefix(line, "─ #space  For me ─") {
		t.Fatalf("the divider must show For me after the channel: %q", line)
	}
	h, ok := hitWith(m, hitFilter, "toggle")
	if !ok {
		t.Fatal("no For me target")
	}
	clickAt(m, h.x0, h.y)
	lines := frameLines(t, m)
	if !slices.Equal(filterAccounts(m), []string{"mh"}) || !strings.HasPrefix(divider(lines), "─ #space  For me ✕ ─") || strings.Contains(lines[0], "For me") {
		t.Fatalf("For me chip:\n%s", strings.Join(lines, "\n"))
	}
	paste(m, "draft")
	if line := divider(frameLines(t, m)); !strings.HasPrefix(line, "─ #space  For me ✕ ─") || !strings.HasSuffix(line, "─ 5 B ─") {
		t.Fatalf("the chip follows the channel; the byte count stays right: %q", line)
	}
	cells := targetCells(m, actFilter, "")
	if len(cells) != 1 {
		t.Fatalf("scope row: %v", cells)
	}
	clickCell(m, cells[0])
	if m.filter != nil {
		t.Fatal("All messages must clear the filter")
	}
	m.setFilter("mx", "a-rather-long-nickname")
	for _, tc := range []struct {
		width int
		chip  string
	}{{160, "─ #space  @a-rather-long-… ✕ ─"}, {20, ""}} {
		m.Update(tea.WindowSizeMsg{Width: tc.width, Height: 12})
		line := divider(frameLines(t, m))
		if _, ok := hitWith(m, hitFilter, "clear"); ok != (tc.chip != "") || !strings.HasPrefix(line, tc.chip+"─") || !strings.HasSuffix(line, "─ 5 B ─") {
			t.Fatalf("%d columns: %q", tc.width, line)
		}
	}
	_ = m.SetDraft("")
	m.Update(tea.WindowSizeMsg{Width: 20, Height: 12})
	if line := divider(frameLines(t, m)); !strings.HasPrefix(line, "─ #space  @a-r… ✕ ─") {
		t.Fatalf("a narrow divider shortens the name: %q", line)
	}
	h, ok = hitWith(m, hitFilter, "pick")
	if !ok {
		t.Fatal("no shortened chip target")
	}
	clickAt(m, h.x0, h.y)
	if m.overlay == nil || m.overlay.kind != overlayFilter || !slices.Equal(filterAccounts(m), []string{"mx"}) {
		t.Fatal("another selection's name opens the picker and keeps the filter")
	}
	m.overlay = nil
	m.View()
	h, ok = hitWith(m, hitFilter, "clear")
	if !ok {
		t.Fatal("no shortened chip ✕")
	}
	clickAt(m, h.x0, h.y)
	if m.filter != nil {
		t.Fatal("the shortened chip's ✕ still clears the filter")
	}
}

// /filter opens a picker of All messages, For me and each connected account
// once; Space checks several, Enter applies them. Names choose directly,
// several at once; an unknown or ambiguous one refuses the whole command and
// keeps the draft.
func TestFilterPickerAndCommand(t *testing.T) {
	m, _, _ := filterModel(t)
	arrive(m, "k", "m1", "bob", "first bob")
	arrive(m, "l", "m2", "bob", "second bob")
	paste(m, "/filter")
	m.suggest = nil
	press(m, tea.KeyEnter, 0)
	if m.overlay == nil || m.overlay.kind != overlayFilter {
		t.Fatal("/filter must open the picker")
	}
	var labels []string
	for _, c := range m.overlay.choices {
		labels = append(labels, c.label)
	}
	if !slices.Equal(labels, []string{"All messages", "For me", "@opus", "@qa", "@stranger"}) {
		t.Fatalf("picker rows: %v", labels)
	}
	press(m, tea.KeyDown, 0)
	press(m, tea.KeyDown, 0)
	press(m, tea.KeySpace, 0)
	press(m, tea.KeyDown, 0)
	press(m, tea.KeySpace, 0)
	press(m, tea.KeyEnter, 0)
	if m.overlay != nil || !slices.Equal(filterAccounts(m), []string{"mo", "mq"}) {
		t.Fatalf("picker choice: %+v", m.filter)
	}
	for _, tc := range []struct {
		draft    string
		accounts []string
	}{{"/filter qa", []string{"mq"}}, {"/filter @Leogr", []string{"mh"}}, {"/filter me", []string{"mh"}}, {"/filter all", nil},
		{"/filter qa @opus", []string{"mq", "mo"}}, {"/filter qa @Leogr leogr-bbbbb mh", []string{"mq", "mh"}}} {
		paste(m, tc.draft)
		m.suggest = nil
		press(m, tea.KeyEnter, 0)
		if got := filterAccounts(m); !slices.Equal(got, tc.accounts) || m.Draft() != "" {
			t.Fatalf("%s: filter %q, draft %q", tc.draft, got, m.Draft())
		}
	}
	m.setFilter("", "")
	for _, draft := range []string{"/filter bob", "/filter nobody", "/filter a b", "/filter qa nobody", "/filter qa bob", "/filter me qa", "/filter qa all"} {
		_ = m.SetDraft(draft)
		m.suggest = nil
		press(m, tea.KeyEnter, 0)
		if m.Draft() != draft || m.filter != nil || m.noticeLevel != levelWarn {
			t.Fatalf("%s must be refused and kept: %+v %q", draft, m.filter, m.notice)
		}
	}
}

// A participant's card offers Filter messages for its authenticated account.
func TestCardFiltersByItsAccount(t *testing.T) {
	m, _, _ := filterModel(t)
	m.openCard("qa", "mq")
	actions := m.cardActions()
	i := slices.Index(actions, cardAction("filter"))
	if i < 0 || !strings.Contains(view(m), "Filter messages") {
		t.Fatalf("card actions: %v", actions)
	}
	m.overlayActivate(i)
	if m.overlay != nil || !slices.Equal(filterAccounts(m), []string{"mq"}) {
		t.Fatalf("card filter: %+v", m.filter)
	}
	m.openCard("ghost", "")
	if slices.Contains(m.cardActions(), cardAction("filter")) {
		t.Fatal("a session without an account has nothing to filter by")
	}
}

// Suggestions, completion and Mention actions use a trusted account's saved
// nick once, however many aliases it has; an unverified session keeps its
// own nick and never takes a trusted row. A catalog refresh renames.
func TestMentionsUseTheSavedNickPerAccount(t *testing.T) {
	m, _, _ := filterModel(t)
	m.opts.Identities["mo"] = Identity{Name: "opus-box", Nick: "opus", Box: true}
	m.Update(MembersMsg{Members: append(slices.Clone(m.members), Member{Nick: "qa", Account: "ma"})})
	paste(m, "@le")
	if m.suggest == nil || len(m.suggest.items) != 1 || m.suggest.items[0].label != "@leogr" || m.suggest.items[0].detail != "you" {
		t.Fatalf("aliases must give one saved-nick row: %+v", m.suggest)
	}
	press(m, tea.KeyTab, 0)
	if m.Draft() != "@leogr " {
		t.Fatalf("insertion: %q", m.Draft())
	}
	_ = m.SetDraft("@opus-b")
	m.refreshSuggest()
	if m.suggest == nil || m.suggest.items[0].label != "@opus" || m.suggest.items[0].detail != "agent" {
		t.Fatalf("a name prefix suggests the saved nick: %+v", m.suggest)
	}
	_ = m.SetDraft("@qa")
	m.refreshSuggest()
	if m.suggest != nil {
		t.Fatalf("the trusted qa row is the only @qa and complete: %+v", m.suggest)
	}
	_ = m.SetDraft("@q")
	m.refreshSuggest()
	if m.suggest == nil || len(m.suggest.items) != 1 || m.suggest.items[0].member.Account != "mq" {
		t.Fatalf("an unverified session must not take a trusted row: %+v", m.suggest)
	}
	_ = m.SetDraft("@leogr-a")
	if !m.complete() || m.Draft() != "@leogr-a" || !strings.Contains(m.notice, "No connected participant") {
		t.Fatalf("Tab completion never inserts a live alias: %q %q", m.Draft(), m.notice)
	}
	_ = m.SetDraft("@leo")
	if !m.complete() || m.Draft() != "@leogr " {
		t.Fatalf("Tab completion of the saved nick: %q", m.Draft())
	}
	arrive(m, "a", "mh", "leogr-bbbbb", "from my other view")
	arrive(m, "b", "mo", "opus", "from opus")
	m.selected, m.picked = 1, true
	if a := m.messageActions(); a[len(a)-1].label != "@opus" {
		t.Fatalf("message mention action: %+v", a)
	}
	m.Update(CatalogMsg{Generation: 1, Identities: map[string]Identity{"mh": {Name: "Leo", Nick: "leo", Human: true}, "mo": {Name: "opus", Nick: "opus", Box: true}}})
	_ = m.SetDraft("@le")
	m.refreshSuggest()
	if m.suggest == nil || m.suggest.items[0].label != "@leo" {
		t.Fatalf("a catalog refresh renames the suggestion: %+v", m.suggest)
	}
}

// My saved nick and other live aliases mark a message for me and count as
// unread mentions, as the For me filter does; another nick does not.
func TestSelfMentionsFollowTheAccountNotTheAlias(t *testing.T) {
	m, _, _ := filterModel(t)
	arrive(m, "seed", "mq", "qa", "seed")
	m.follow = false
	arrive(m, "a", "mq", "qa", "@leogr look")
	arrive(m, "b", "mq", "qa", "@leogr-bbbbb other view")
	arrive(m, "c", "mq", "qa", "@opus not me")
	if m.unreadMentions != 2 || m.unread != 3 {
		t.Fatalf("unread mentions %d of %d", m.unreadMentions, m.unread)
	}
	m.View()
	fg, bg := mentionColors(m.theme)
	marked := map[string]bool{}
	for _, row := range m.lastTranscript {
		for _, r := range row.runs {
			if r.act.kind == actMention && r.style.FG == fg && r.style.BG == bg {
				marked[r.act.value] = true
			}
		}
	}
	if !marked["leogr"] || !marked["leogr-bbbbb"] || marked["opus"] {
		t.Fatalf("self highlight: %v", marked)
	}
}

// The jump keeps its labels and centre; the filter chip takes only the cells
// the jump and the count leave, so a narrow divider drops it first.
func TestFilterChipGivesWayToTheJump(t *testing.T) {
	m := scrolledModel(t)
	press(m, tea.KeyPgUp, 0)
	_, row, ok := jumpHit(m)
	if _, chip := hitWith(m, hitFilter, "toggle"); !ok || !chip || !strings.Contains(row, "↓ Jump to latest") || !strings.HasPrefix(row, "─ #space  For me ─") {
		t.Fatalf("wide divider: %q", row)
	}
	m.Update(tea.WindowSizeMsg{Width: 26, Height: 20})
	_, row, ok = jumpHit(m)
	if _, chip := hitWith(m, hitFilter, "toggle"); !ok || chip || !strings.Contains(row, "↓ Latest") {
		t.Fatalf("narrow divider: %q", row)
	}
}

// Settings show, pre-fill and compare the saved nickname, never this view's
// live alias: submitting the unchanged field or /nick of the saved name
// neither saves nor reconnects, and another name still saves.
func TestSettingsUseTheSavedNickNotTheAlias(t *testing.T) {
	m, _, _ := filterModel(t)
	var saved []string
	reconnects := 0
	m.opts.SaveNick = func(nick string) tea.Cmd { saved = append(saved, nick); return nil }
	m.opts.Reconnect = func() tea.Cmd { reconnects++; return nil }
	m.openSettings()
	if out := view(m); !strings.Contains(out, "leogr · this view: @leogr-aaaaa") {
		t.Fatalf("settings row:\n%s", out)
	}
	press(m, tea.KeyEnter, 0)
	if m.overlay == nil || m.overlay.nick == nil || m.overlay.nick.text != "leogr" {
		t.Fatalf("the field must start from the saved nick: %+v", m.overlay)
	}
	press(m, tea.KeyEnter, 0)
	if len(saved) != 0 || reconnects != 0 || m.overlay.problem != "That is already your nickname" {
		t.Fatalf("unchanged field: saved %v, reconnects %d, %q", saved, reconnects, m.overlay.problem)
	}
	m.overlay = nil
	paste(m, "/nick leogr")
	press(m, tea.KeyEnter, 0)
	if len(saved) != 0 || reconnects != 0 || m.Draft() != "/nick leogr" {
		t.Fatalf("/nick of the saved name: saved %v, draft %q", saved, m.Draft())
	}
	_ = m.SetDraft("/nick leo")
	press(m, tea.KeyEnter, 0)
	if !slices.Equal(saved, []string{"leo"}) || reconnects != 0 {
		t.Fatalf("another name saves, then waits for the host: %v %d", saved, reconnects)
	}
}

// selfMarked reports whether the last frame marks the message with key as
// mentioning me: the header's mention bar and a highlighted mention run.
func selfMarked(m *Model, key string) (bar, run bool) {
	fg, bg := mentionColors(m.theme)
	for _, row := range m.lastTranscript {
		if row.key != key {
			continue
		}
		if row.kind == rowHeader && len(row.runs) > 0 && row.runs[0].text == "▌" && row.runs[0].style.FG == m.theme.Capability {
			bar = true
		}
		for _, r := range row.runs {
			if r.act.kind == actMention && r.style.FG == fg && r.style.BG == bg {
				run = true
			}
		}
	}
	return bar, run
}

// Identity evidence that arrives in a hidden channel reaches the visible one
// on its next frame, without a switch or resize: a name that becomes
// ambiguous there stops matching the filter and stops marking me here.
func TestEvidenceFromAnotherChannelRepaintsTheView(t *testing.T) {
	m, _, _ := filterModel(t)
	m.Update(MembershipMsg{Channel: "#build", State: "joined"})
	arrive(m, "k", "m1", "bob", "first bob")
	arrive(m, "n", "mq", "qa", "@bob are you there?")
	arrive(m, "s", "mq", "qa", "@leogr-bbbbb other view")
	m.setFilter("m1", "bob")
	if got := shownIDs(m); !slices.Equal(got, []string{"k", "n"}) {
		t.Fatalf("before: %v", got)
	}
	m.Update(IncomingMsg{Message: Message{ID: "x1", Channel: "#build", Account: "m2", Nick: "bob", Text: "another bob"}})
	if got := shownIDs(m); !slices.Equal(got, []string{"k"}) || m.name != "#space" {
		t.Fatalf("a conflict in #build must hide the stale match here: %v", got)
	}
	m.setFilter("", "")
	m.View()
	if bar, run := selfMarked(m, "server:s"); !bar || !run {
		t.Fatalf("my other alias is marked for me: bar=%v run=%v", bar, run)
	}
	m.Update(IncomingMsg{Message: Message{ID: "x2", Channel: "#build", Account: "mf", Nick: "leogr-bbbbb", Text: "reused alias"}})
	m.View()
	if bar, run := selfMarked(m, "server:s"); bar || run {
		t.Fatalf("a reused alias must no longer mark me: bar=%v run=%v", bar, run)
	}
}

// An explicit pick that the filter comes to hide ends instead of acting on a
// hidden message or moving to a neighbour: a confirmed send outside the
// filter, and a mention whose name becomes ambiguous elsewhere.
func TestFilterEndsAPickItComesToHide(t *testing.T) {
	m, copies, sends := filterModel(t)
	m.opts.Clipboard = "on"
	m.setFilter("mo", "opus")
	paste(m, "not about opus")
	press(m, tea.KeyEnter, 0)
	id := (*sends)[0].ID
	m.pick("client:" + id)
	if !m.picked || m.selectedMessage() == nil || m.selectedMessage().ClientID != id {
		t.Fatal("the pending send must be pickable while shown")
	}
	m.Update(DeliveryMsg{ID: id, MessageID: "sent1", State: Confirmed})
	if m.picked || m.focus != focusComposer || slices.Contains(shownIDs(m), "sent1") {
		t.Fatalf("a confirmed send outside the filter must end its pick: picked=%v", m.picked)
	}
	m.runAction("copy")
	if len(*copies) != 0 {
		t.Fatalf("an action reached the hidden message: %v", *copies)
	}
	m.Update(MembershipMsg{Channel: "#build", State: "joined"})
	arrive(m, "k", "m1", "bob", "first bob")
	arrive(m, "n", "mq", "qa", "@bob are you there?")
	m.setFilter("m1", "bob")
	m.pick("server:n")
	m.Update(IncomingMsg{Message: Message{ID: "x1", Channel: "#build", Account: "m2", Nick: "bob", Text: "another bob"}})
	if m.picked {
		t.Fatal("a mention hidden by new evidence must end its pick")
	}
	if msg := m.selectedMessage(); msg == nil || msg.ID != "k" {
		t.Fatalf("the implicit target is the latest shown message: %+v", msg)
	}
}

// This view's live alias is evidence, not an override: an earlier use of it
// by another account makes it name nobody for the filter, clicks and marks.
func TestLiveAliasIsEvidenceNotAnOverride(t *testing.T) {
	m, _, _ := filterModel(t)
	arrive(m, "f1", "mf", "leogr-aaaaa", "an earlier holder of this alias")
	arrive(m, "q1", "mq", "qa", "@leogr-aaaaa which one?")
	arrive(m, "q2", "mq", "qa", "@leogr still me")
	m.toggleForMe()
	if got := shownIDs(m); !slices.Equal(got, []string{"q2"}) {
		t.Fatalf("a reused live alias must not be attributed to me: %v", got)
	}
	m.setFilter("", "")
	m.View()
	if bar, run := selfMarked(m, "server:q1"); bar || run {
		t.Fatal("a reused live alias must not mark me")
	}
	m.follow = false
	arrive(m, "q3", "mq", "qa", "@leogr-aaaaa again")
	if m.unreadMentions != 0 {
		t.Fatalf("a reused live alias counted as for me: %d", m.unreadMentions)
	}
	m.mentionCard("leogr-aaaaa")
	if m.overlay != nil || !strings.Contains(m.notice, "more than one") {
		t.Fatalf("a reused live alias opened a card: %+v %q", m.overlay, m.notice)
	}
}

// replyArrive delivers a live message replying to the server ID parent.
func replyArrive(m *Model, id, account, nick, text, parent string) {
	m.Update(IncomingMsg{Message: Message{ID: id, Account: account, Nick: nick, Text: text, ReplyTo: parent, Time: time.Unix(1000, 0)}})
}

// For me also shows the replies descending from a message I sent or that
// mentions me, at any depth, without their mentioning me. A sibling branch
// under a root I neither sent nor am mentioned in stays hidden although I
// replied elsewhere in that thread, and the root itself stays hidden.
func TestForMeShowsRepliesUnderMyMessages(t *testing.T) {
	m, _, _ := filterModel(t)
	arrive(m, "root", "mq", "qa", "plan ready for review")
	replyArrive(m, "mine", "mh", "leogr-aaaaa", "looks good", "root")
	replyArrive(m, "r1", "mq", "qa", "thanks, applying", "mine")
	replyArrive(m, "r2", "mo", "opus", "applied on my side", "r1")
	replyArrive(m, "r3", "mq", "qa", "confirmed", "r2")
	replyArrive(m, "sib", "mo", "opus", "another branch", "root")
	arrive(m, "men", "mq", "qa", "@leogr ready")
	replyArrive(m, "men1", "mo", "opus", "noted", "men")
	m.toggleForMe()
	if got, want := shownIDs(m), []string{"mine", "r1", "r2", "r3", "men", "men1"}; !slices.Equal(got, want) {
		t.Fatalf("For me shows %v, want %v", got, want)
	}
	if out := view(m); !strings.Contains(out, "6 of 8 loaded messages") {
		t.Fatalf("the scope counts the shown replies:\n%s", out)
	}
	m.setFilter("", "")
	if len(shownIDs(m)) != 8 {
		t.Fatal("all messages must show every message again")
	}
}

// A reply stays hidden until its parent is loaded with exactly that ID; an
// older page delivering the parent shows it on the next frame, and nothing
// is requested but the one explicit Page Up.
func TestThreadRepliesNeedTheirLoadedParent(t *testing.T) {
	m, _, _ := filterModel(t)
	var requests []HistoryRequest
	m.opts.LoadOlder = func(r HistoryRequest) tea.Cmd { requests = append(requests, r); return nil }
	replyArrive(m, "child", "mq", "qa", "answering your older question", "parent")
	replyArrive(m, "prefix", "mq", "qa", "a near miss", "paren")
	m.toggleForMe()
	if got := shownIDs(m); len(got) != 0 || len(requests) != 0 {
		t.Fatalf("an unloaded parent shows nothing and fetches nothing: %v %v", got, requests)
	}
	press(m, tea.KeyPgUp, 0)
	if len(requests) != 1 {
		t.Fatalf("Page Up requests one page: %+v", requests)
	}
	m.Update(IncomingMsg{Message: Message{ID: "parent", Account: "mh", Nick: "leogr", Text: "my older question", Older: true}})
	m.Update(HistoryPageMsg{ID: requests[0].ID, Count: 1})
	if got := shownIDs(m); !slices.Equal(got, []string{"parent", "child"}) || len(requests) != 1 {
		t.Fatalf("the loaded parent shows its reply: %v, %d requests", got, len(requests))
	}
	if !strings.Contains(m.notice, "Loaded 1 older messages; 1 shown by the filter") {
		t.Fatalf("the page notice counts its own messages: %q", m.notice)
	}
}

// A parent ID loaded only in another channel shows nothing here.
func TestThreadRuleStaysInItsChannel(t *testing.T) {
	m, _, _ := filterModel(t)
	m.Update(MembershipMsg{Channel: "#build", State: "joined"})
	m.Update(IncomingMsg{Message: Message{ID: "p", Channel: "#build", Account: "mh", Nick: "leogr", Text: "mine elsewhere"}})
	replyArrive(m, "c", "mq", "qa", "a reply naming it", "p")
	m.toggleForMe()
	if got := shownIDs(m); len(got) != 0 {
		t.Fatalf("a parent in another channel must not show the reply: %v", got)
	}
	m.switchTo("#build")
	m.toggleForMe()
	if got := shownIDs(m); !slices.Equal(got, []string{"p"}) {
		t.Fatalf("the other channel shows only its own: %v", got)
	}
}

// Reply IDs are participant-supplied: a self-reply, a cycle and an unknown
// ID terminate and show nothing, while a forged reply to my real message
// still shows, as any reply to it does.
func TestForgedReplyChainsTerminate(t *testing.T) {
	m, _, _ := filterModel(t)
	replyArrive(m, "self", "mq", "qa", "replying to myself", "self")
	replyArrive(m, "a", "mq", "qa", "cycle one", "b")
	replyArrive(m, "b", "mo", "opus", "cycle two", "a")
	replyArrive(m, "lost", "mq", "qa", "to nowhere", "no-such-id")
	arrive(m, "mine", "mh", "leogr-aaaaa", "my status")
	replyArrive(m, "loop", "mq", "qa", "into the cycle", "a")
	replyArrive(m, "ok", "mx", "stranger", "under yours", "mine")
	m.toggleForMe()
	if got := shownIDs(m); !slices.Equal(got, []string{"mine", "ok"}) {
		t.Fatalf("forged chains: %v", got)
	}
}

// A live reply under a shown message is shown, so it is not a hidden
// arrival; an unrelated reply is. Neither adds an unread mention: the
// thread rule changes what the filter shows, not what is for me.
func TestThreadRepliesAreNotHiddenArrivals(t *testing.T) {
	m, _, _ := filterModel(t)
	arrive(m, "mine", "mh", "leogr-aaaaa", "my status")
	arrive(m, "other", "mq", "qa", "their status")
	m.toggleForMe()
	m.follow = false
	replyArrive(m, "r1", "mq", "qa", "about yours", "mine")
	if m.filter.hiddenNew != 0 || m.unreadMentions != 0 || m.unread != 1 {
		t.Fatalf("a shown reply: hidden %d, mentions %d, unread %d", m.filter.hiddenNew, m.unreadMentions, m.unread)
	}
	replyArrive(m, "r2", "mo", "opus", "about theirs", "other")
	if m.filter.hiddenNew != 1 || m.unreadMentions != 0 {
		t.Fatalf("an unrelated reply: hidden %d, mentions %d", m.filter.hiddenNew, m.unreadMentions)
	}
	for _, msg := range m.messages {
		if msg.ID == "r1" && mentions(msg.Text, m.selfNames()) {
			t.Fatal("a reply without a mention is not marked for me")
		}
	}
}

// Every surface describing a filter states the reply clause, and the
// scope row keeps the selected name readable before the clause.
func TestFilterWordingNamesReplies(t *testing.T) {
	m, _, _ := filterModel(t)
	roomOfMentions(m)
	var forMe string
	for _, c := range m.filterChoices() {
		if c.label == "For me" {
			forMe = c.detail
		}
	}
	if forMe != "messages from or mentioning you, and replies to those" {
		t.Fatalf("picker row: %q", forMe)
	}
	m.toggleForMe()
	if !strings.Contains(m.notice, "from or mentioning you, and replies to those") {
		t.Fatalf("info line: %q", m.notice)
	}
	plainRow := func(width int) string {
		var b strings.Builder
		for _, r := range m.filterScopeRows(0, width)[0].runs {
			b.WriteString(r.text)
		}
		return b.String()
	}
	if row := plainRow(100); !strings.Contains(row, "Only messages from or mentioning you, and replies to those") {
		t.Fatalf("scope row: %q", row)
	}
	for _, width := range []int{20, 40, 70} {
		if row := plainRow(width); ansi.StringWidthWc(row) > width || width >= 70 && !strings.Contains(row, "mentioning you") {
			t.Fatalf("%d columns: the scope row must fit, the name before the clause: %q", width, row)
		}
	}
	m.openFilterPicker()
	if rows := strings.Join(m.filterPickerRows(80), "\n"); !strings.Contains(ansi.Strip(rows), "or when it replies to a loaded message shown") {
		t.Fatalf("picker footer:\n%s", rows)
	}
	help := ""
	for _, section := range helpSections {
		for _, row := range section.rows {
			help += row[0] + " " + row[1] + "\n"
		}
	}
	for _, want := range []string{"@mention you, and loaded replies to those", "is @mentioned in it, or when it replies to a loaded message shown"} {
		if !strings.Contains(help, want) {
			t.Fatalf("help lacks %q:\n%s", want, help)
		}
	}
}

// An account outside the identity map is offered under its plain nick with
// no kind or qualifier, in mentions and the filter alike; only its away
// state is said.
func TestUnknownAccountsCarryNoQualifier(t *testing.T) {
	m, _, _ := filterModel(t)
	paste(m, "@str")
	if m.suggest == nil || len(m.suggest.items) != 1 || m.suggest.items[0].label != "@stranger" || m.suggest.items[0].detail != "" {
		t.Fatalf("mention suggestion: %+v", m.suggest)
	}
	members := slices.Clone(m.members)
	for i := range members {
		if members[i].Account == "mx" {
			members[i].Away = true
		}
	}
	m.Update(MembersMsg{Members: members})
	m.refreshSuggest()
	if m.suggest == nil || m.suggest.items[0].detail != "away" {
		t.Fatalf("away suggestion: %+v", m.suggest)
	}
	var found bool
	for _, c := range m.filterChoices() {
		switch c.account {
		case "mx":
			found = true
			if c.label != "@stranger" || c.detail != "" {
				t.Fatalf("filter choice: %+v", c)
			}
		case "mo":
			if c.detail != "agent" {
				t.Fatalf("a trusted box keeps its detail: %+v", c)
			}
		}
	}
	if !found {
		t.Fatal("the unknown account is not offered")
	}
	m.openFilterPicker()
	if out := view(m); strings.Contains(out, "unverified") {
		t.Fatalf("filter picker:\n%s", out)
	}
}
