package chatui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// threadRoom is the proposal's example between unrelated messages: leogr
// asks, opus answers, qa replies to leogr and devOpUs to qa.
func threadRoom(m *Model) {
	arrive(m, "before", "mo", "opus", "unrelated status")
	arrive(m, "ask", "mh", "leogr-aaaaa", "ship v2 today?")
	replyArrive(m, "yes", "mo", "opus", "yes, after tests", "ask")
	arrive(m, "noise", "mq", "qa", "@opus unrelated")
	replyArrive(m, "smoke", "mq", "qa", "not before the smoke", "ask")
	replyArrive(m, "other", "mo", "opus", "a reply elsewhere", "before")
	replyArrive(m, "ops", "mx", "stranger", "smoke is green", "smoke")
}

// threadOf selects the message with server ID id and runs its Thread action.
func threadOf(t *testing.T, m *Model, id string) {
	t.Helper()
	m.pick("server:" + id)
	if !slices.ContainsFunc(m.messageActions(), func(a messageAction) bool { return a.id == "thread" && a.label == "Thread" }) {
		t.Fatalf("%s offers no Thread action: %+v", id, m.messageActions())
	}
	m.runAction("thread")
}

// Any message of a thread shows the same whole tree: its top-most loaded
// message and every reply under it, branches included, in time order, and
// nothing else. The chip reads Thread and the scope row says it is whole.
func TestThreadViewShowsTheWholeTreeFromAnyMessage(t *testing.T) {
	want := []string{"ask", "yes", "smoke", "ops"}
	for _, from := range []string{"ops", "ask", "yes", "smoke"} {
		m, _, _ := filterModel(t)
		threadRoom(m)
		threadOf(t, m, from)
		if got := shownIDs(m); !slices.Equal(got, want) {
			t.Fatalf("Thread from %s shows %v, want %v", from, got, want)
		}
		if !m.picked || m.selectedMessage().ID != from {
			t.Fatalf("the selected message stays picked: %+v", m.selectedMessage())
		}
	}
	m, _, _ := filterModel(t)
	threadRoom(m)
	threadOf(t, m, "yes")
	out := view(m)
	for _, s := range []string{"─ #space  Thread ✕ ─", "Only the thread of your message from", "with all its replies", "4 of 7 loaded messages · from the start of the thread"} {
		if !strings.Contains(out, s) {
			t.Fatalf("frame lacks %q:\n%s", s, out)
		}
	}
	if strings.Contains(out, "Page Up loads older history") {
		t.Fatalf("a whole thread does not ask for older history:\n%s", out)
	}
	if !strings.Contains(m.notice, "Showing only the thread of your message from") {
		t.Fatalf("notice: %q", m.notice)
	}
	h, ok := hitWith(m, hitFilter, "pick")
	if !ok {
		t.Fatal("no Thread chip target")
	}
	m.hover = hoverKey{area: hitFilter, id: h.id}
	var hint strings.Builder
	for _, r := range m.hoverHint() {
		hint.WriteString(r.Text)
	}
	if !strings.Contains(hint.String(), "Thread  click or Alt+F to choose people instead") {
		t.Fatalf("chip hint: %q", hint.String())
	}
	m.hover = hoverKey{}
	h, _ = hitWith(m, hitFilter, "clear")
	clickAt(m, h.x0, h.y)
	if m.filter != nil || len(shownIDs(m)) != 7 {
		t.Fatal("✕ shows every message again")
	}
}

// A thread whose top replies to a message not loaded shows its loaded part
// and says it may start earlier; nothing is fetched until Page Up, and the
// page bringing the earlier part moves the top up, with its other branches.
func TestThreadViewMayStartEarlier(t *testing.T) {
	m, _, _ := filterModel(t)
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	var requests []HistoryRequest
	m.opts.LoadOlder = func(r HistoryRequest) tea.Cmd { requests = append(requests, r); return nil }
	replyArrive(m, "mid", "mq", "qa", "continuing yesterday's question", "start")
	arrive(m, "noise", "mo", "opus", "unrelated")
	replyArrive(m, "leaf", "mo", "opus", "agreed", "mid")
	threadOf(t, m, "leaf")
	if got := shownIDs(m); !slices.Equal(got, []string{"mid", "leaf"}) || len(requests) != 0 {
		t.Fatalf("the loaded part shows and nothing is fetched: %v %v", got, requests)
	}
	if out := view(m); !strings.Contains(out, "2 of 3 loaded messages · the thread may start earlier: Page Up loads older history") {
		t.Fatalf("scope row:\n%s", out)
	}
	press(m, tea.KeyPgUp, 0)
	press(m, tea.KeyPgUp, 0)
	if len(requests) != 1 {
		t.Fatalf("Page Up requests one page: %+v", requests)
	}
	m.Update(IncomingMsg{Message: Message{ID: "start", Account: "mh", Nick: "leogr", Text: "yesterday's question", Older: true}})
	m.Update(IncomingMsg{Message: Message{ID: "branch", Account: "mo", Nick: "opus", Text: "another branch", ReplyTo: "start", Older: true}})
	m.Update(HistoryPageMsg{ID: requests[0].ID, Count: 2})
	if got := shownIDs(m); !slices.Equal(got, []string{"start", "branch", "mid", "leaf"}) || len(requests) != 1 {
		t.Fatalf("the older page extends the thread upward: %v, %d requests", got, len(requests))
	}
	if out := view(m); !strings.Contains(out, "4 of 5 loaded messages · from the start of the thread") || !strings.Contains(out, "Only the thread of your message from") {
		t.Fatalf("the top moved up:\n%s", out)
	}
}

// A thread chosen before its top was evicted keeps the replies to that top;
// a full display history says so rather than offering Page Up.
func TestThreadViewSurvivesItsTopsEviction(t *testing.T) {
	m, _, _ := filterModel(t)
	arrive(m, "top", "mh", "leogr-aaaaa", "question")
	replyArrive(m, "a1", "mq", "qa", "answer one", "top")
	replyArrive(m, "a2", "mo", "opus", "answer two", "top")
	replyArrive(m, "a21", "mq", "qa", "under two", "a2")
	threadOf(t, m, "a21")
	for i := range maxMessages - 3 {
		arrive(m, fmt.Sprintf("n%d", i), "mo", "opus", "filler")
	}
	if m.indexOf("server:top") >= 0 || m.indexOf("server:a1") < 0 {
		t.Fatal("the fixture evicts exactly the top")
	}
	if got := shownIDs(m); !slices.Equal(got, []string{"a1", "a2", "a21"}) {
		t.Fatalf("the replies to the evicted top stay: %v", got)
	}
	if out := view(m); !strings.Contains(out, "3 of 256 loaded messages · display history is full") || !strings.Contains(out, "Only the loaded part of a thread") {
		t.Fatalf("scope rows:\n%s", out)
	}
}

// Reply IDs are participant-supplied: a cycle or a self-reply ends the walk
// to the top, and an unknown ID is a thread of the loaded part only.
func TestThreadViewTerminatesForgedChains(t *testing.T) {
	m, _, _ := filterModel(t)
	replyArrive(m, "a", "mq", "qa", "cycle one", "b")
	replyArrive(m, "b", "mo", "opus", "cycle two", "a")
	replyArrive(m, "self", "mq", "qa", "replying to myself", "self")
	replyArrive(m, "into", "mq", "qa", "into the cycle", "a")
	replyArrive(m, "lost", "mq", "qa", "to nowhere", "no-such-id")
	replyArrive(m, "under", "mo", "opus", "under lost", "lost")
	threadOf(t, m, "into")
	if got := shownIDs(m); !slices.Equal(got, []string{"a", "b", "into"}) {
		t.Fatalf("a cycle is one thread: %v", got)
	}
	threadOf(t, m, "self")
	if got := shownIDs(m); !slices.Equal(got, []string{"self"}) {
		t.Fatalf("a self-reply is its own thread: %v", got)
	}
	if out := view(m); !strings.Contains(out, "from the start of the thread") {
		t.Fatalf("a self-reply has nothing earlier:\n%s", out)
	}
	threadOf(t, m, "under")
	if got := shownIDs(m); !slices.Equal(got, []string{"lost", "under"}) {
		t.Fatalf("an unknown parent ID: %v", got)
	}
	if out := view(m); !strings.Contains(out, "the thread may start earlier") {
		t.Fatalf("an unknown parent may be older:\n%s", out)
	}
}

// One filter at a time: Thread replaces For me or people, and For me,
// people, a card or the picker replace Thread.
func TestThreadViewReplacesAndIsReplaced(t *testing.T) {
	m, _, _ := filterModel(t)
	threadRoom(m)
	m.setFilterPicks([]filterPick{{"mo", "opus"}, {"mq", "qa"}})
	threadOf(t, m, "smoke")
	if m.filter.thread != "ask" || len(m.filter.picks) != 0 {
		t.Fatalf("Thread replaces people: %+v", m.filter)
	}
	press(m, 'm', tea.ModAlt)
	if !m.selfOnly() || m.filter.thread != "" {
		t.Fatalf("Alt+M replaces Thread with For me: %+v", m.filter)
	}
	threadOf(t, m, "ops")
	paste(m, "/filter qa")
	m.suggest = nil
	press(m, tea.KeyEnter, 0)
	if !slices.Equal(filterAccounts(m), []string{"mq"}) || m.filter.thread != "" {
		t.Fatalf("/filter NAME replaces Thread: %+v", m.filter)
	}

	// The picker shows the visible thread checked; a person unchecks it,
	// and Enter on the thread keeps it.
	threadOf(t, m, "yes")
	press(m, 'f', tea.ModAlt)
	o := m.overlay
	if o == nil || o.kind != overlayFilter || !o.stagedThread || o.choices[o.index].label != "Thread" {
		t.Fatalf("picker on the thread row: %+v", o)
	}
	if rows := ansi.Strip(strings.Join(m.filterPickerRows(100), "\n")); !strings.Contains(rows, "[x] Thread") || !strings.Contains(rows, "[ ] All messages") || !strings.Contains(rows, "with all its replies · shown") {
		t.Fatalf("picker rows:\n%s", rows)
	}
	press(m, tea.KeyEnter, 0)
	if m.overlay != nil || m.filter == nil || m.filter.thread != "ask" {
		t.Fatalf("Enter on the thread keeps it: %+v", m.filter)
	}
	press(m, 'f', tea.ModAlt)
	for m.overlay.choices[m.overlay.index].label != "@qa" {
		press(m, tea.KeyDown, 0)
	}
	press(m, tea.KeySpace, 0)
	if m.overlay.stagedThread {
		t.Fatal("checking a person unchecks the thread")
	}
	press(m, tea.KeyEnter, 0)
	if !slices.Equal(filterAccounts(m), []string{"mq"}) || m.filter.thread != "" {
		t.Fatalf("the picker's person replaces Thread: %+v", m.filter)
	}
	if slices.ContainsFunc(m.filterChoices(), func(c filterChoice) bool { return c.thread }) {
		t.Fatal("no thread row without a thread")
	}
	threadOf(t, m, "yes")
	press(m, 'f', tea.ModAlt)
	m.overlay.index = 0
	press(m, tea.KeySpace, 0)
	press(m, tea.KeyEnter, 0)
	if m.filter != nil {
		t.Fatalf("All messages clears Thread: %+v", m.filter)
	}
}

// New replies anywhere in the thread show at once; unrelated arrivals are
// counted as hidden. The thread is per channel.
func TestThreadViewArrivalsAndChannels(t *testing.T) {
	m, _, _ := filterModel(t)
	threadRoom(m)
	m.Update(MembershipMsg{Channel: "#build", State: "joined"})
	m.Update(IncomingMsg{Message: Message{ID: "elsewhere", Channel: "#build", Account: "mq", Nick: "qa", Text: "other channel", ReplyTo: "ask"}})
	threadOf(t, m, "ask")
	m.follow = false
	replyArrive(m, "late", "mo", "opus", "late answer on a branch", "ops")
	arrive(m, "unrelated", "mq", "qa", "unrelated")
	if m.filter.hiddenNew != 1 {
		t.Fatalf("only the unrelated arrival is hidden: %d", m.filter.hiddenNew)
	}
	if got := shownIDs(m); !slices.Equal(got, []string{"ask", "yes", "smoke", "ops", "late"}) {
		t.Fatalf("a live reply in the thread shows: %v", got)
	}
	if line := divider(t, m); !strings.HasPrefix(line, "─ #space  Thread +1 ✕ ─") {
		t.Fatalf("chip: %q", line)
	}
	m.switchTo("#build")
	if m.filter != nil || len(shownIDs(m)) != 1 {
		t.Fatalf("another channel keeps its own filter: %+v", m.filter)
	}
	m.switchTo("#space")
	if m.filter == nil || m.filter.thread != "ask" {
		t.Fatal("the thread stays with its channel")
	}
}

// /thread acts on the selected message, else on the latest shown one, and
// takes no arguments; an unconfirmed message offers no Thread action.
func TestThreadCommandAndAction(t *testing.T) {
	m, _, sends := filterModel(t)
	threadRoom(m)
	paste(m, "/thread")
	m.suggest = nil
	press(m, tea.KeyEnter, 0)
	if m.filter == nil || m.filter.thread != "ask" || m.Draft() != "" {
		t.Fatalf("/thread uses the latest message: %+v %q", m.filter, m.Draft())
	}
	m.setFilter("", "")
	m.pick("server:other")
	paste(m, "/thread")
	m.suggest = nil
	press(m, tea.KeyEnter, 0)
	if got := shownIDs(m); !slices.Equal(got, []string{"before", "other"}) {
		t.Fatalf("/thread uses the selected message: %v", got)
	}
	_ = m.SetDraft("/thread now")
	m.suggest = nil
	press(m, tea.KeyEnter, 0)
	if m.Draft() != "/thread now" || m.noticeLevel != levelWarn || len(*sends) != 0 {
		t.Fatalf("/thread with arguments is refused and kept: %q %q", m.Draft(), m.notice)
	}
	_ = m.SetDraft("pending")
	m.focus, m.picked = focusComposer, false
	press(m, tea.KeyEnter, 0)
	if len(*sends) != 1 {
		t.Fatalf("the draft is sent: %v", *sends)
	}
	m.pick(messageKey(m.messages[len(m.messages)-1]))
	if m.selectedMessage().ID != "" {
		t.Fatalf("the pick is the unconfirmed send: %+v", m.selectedMessage())
	}
	if slices.ContainsFunc(m.messageActions(), func(a messageAction) bool { return a.id == "thread" }) {
		t.Fatal("an unconfirmed message offers Thread")
	}
	m.runAction("thread")
	if m.noticeLevel != levelWarn || !strings.Contains(m.notice, "Not confirmed yet") {
		t.Fatalf("Thread on an unconfirmed message: %q", m.notice)
	}
}

// Help, suggestions and the picker footer describe the thread view.
func TestThreadViewWording(t *testing.T) {
	m, _, _ := filterModel(t)
	help := ""
	for _, section := range helpSections {
		for _, row := range section.rows {
			help += row[0] + " " + row[1] + "\n"
		}
	}
	for _, want := range []string{"Thread  /thread Show only the selected message's thread", "/thread Show only the thread of the selected message"} {
		if !strings.Contains(help, want) {
			t.Fatalf("help lacks %q:\n%s", want, help)
		}
	}
	if !slices.ContainsFunc(commands, func(c command) bool { return c.name == "/thread" }) {
		t.Fatal("/thread is not suggested")
	}
	m.openFilterPicker()
	if rows := ansi.Strip(strings.Join(m.filterPickerRows(80), "\n")); !strings.Contains(rows, "A selected message's Thread or /thread shows its whole thread instead") {
		t.Fatalf("picker footer:\n%s", rows)
	}
}

// divider returns the frame's composer divider.
func divider(t *testing.T, m *Model) string {
	t.Helper()
	for _, line := range frameLines(t, m) {
		if strings.HasPrefix(line, "─ #space") {
			return line
		}
	}
	t.Fatal("no divider")
	return ""
}
