package chatui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

func view(m *Model) string { return ansi.Strip(m.View().Content) }

func TestCtrlCRequiresConfirmationAndKeepsTheDraft(t *testing.T) {
	m, _ := readyModel()
	paste(m, "unsent work")
	if cmd := press(m, 'c', tea.ModCtrl); cmd != nil {
		t.Fatal("a single Ctrl+C quit")
	}
	if m.Draft() != "unsent work" || !strings.Contains(m.notice, "again") || !strings.Contains(m.notice, "draft") {
		t.Fatalf("first Ctrl+C must warn and keep the draft: %q", m.notice)
	}
	press(m, 'x', 0) // any other key disarms
	if cmd := press(m, 'c', tea.ModCtrl); cmd != nil {
		t.Fatal("disarmed Ctrl+C quit")
	}
	if cmd := press(m, 'c', tea.ModCtrl); cmd == nil {
		t.Fatal("confirmed Ctrl+C did not quit")
	}
}

// Real printable key events carry Text and take the committed-data path, which
// must disarm quit like any other key.
func TestTypedComposedAndPastedInputDisarmQuit(t *testing.T) {
	for _, input := range []struct {
		name, text string
		msgs       []tea.Msg
	}{
		{"typed", "y", []tea.Msg{tea.KeyPressMsg(tea.Key{Code: 'y', Text: "y"})}},
		{"shifted", "Y", []tea.Msg{tea.KeyPressMsg(tea.Key{Code: 'y', ShiftedCode: 'Y', Mod: tea.ModShift, Text: "Y"})}},
		{"composed", "語", []tea.Msg{tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "語"})}},
		{"paste", "a\nb", []tea.Msg{tea.PasteMsg{Content: "a\nb"}}},
		{"bracketed keys", "a\nb", []tea.Msg{tea.PasteStartMsg{}, tea.KeyPressMsg(tea.Key{Code: 'a', Text: "a"}), tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}), tea.KeyPressMsg(tea.Key{Code: 'b', Text: "b"}), tea.PasteEndMsg{}}},
	} {
		t.Run(input.name, func(t *testing.T) {
			m, _ := readyModel()
			paste(m, "draft ")
			if cmd := press(m, 'c', tea.ModCtrl); cmd != nil {
				t.Fatal("a single Ctrl+C quit")
			}
			for _, msg := range input.msgs {
				m.Update(msg)
			}
			if m.Draft() != "draft "+input.text {
				t.Fatalf("input was not committed to the draft: %q", m.Draft())
			}
			if cmd := press(m, 'c', tea.ModCtrl); cmd != nil {
				t.Fatal("Ctrl+C after new input quit and lost the changed draft")
			}
			if cmd := press(m, 'c', tea.ModCtrl); cmd == nil {
				t.Fatal("two consecutive Ctrl+C presses must still quit")
			}
		})
	}
}

func TestIdentityMapNamesAuthorsAndSourceShowsAccounts(t *testing.T) {
	m, _ := readyModel()
	m.opts.Identities = map[string]Identity{"mhuman": {Name: "leogr", Human: true}, "mopus": {Name: "opus"}}
	m.Update(IncomingMsg{Message: Message{ID: "a", Account: "mopus", Nick: "opus", Text: "verified", Time: time.Unix(0, 0)}})
	m.Update(IncomingMsg{Message: Message{ID: "b", Account: "mstranger", Nick: "opus", Text: "same nick", Time: time.Unix(0, 0), ReplyTo: "a"}})
	out := view(m)
	// Trusted names carry their glyph; an unknown account shows only its
	// dim plain nick, with no glyph, mark or qualifier label.
	clock := time.Unix(0, 0).Local().Format("15:04")
	if !strings.Contains(out, "●  opus  "+clock) || !strings.Contains(out, "   opus  "+clock) || strings.Contains(out, "?  opus") || strings.Contains(out, "unverified") || strings.Contains(out, "✓") {
		t.Fatalf("trusted and untrusted authors must differ:\n%s", out)
	}
	if !strings.Contains(out, "↳ opus: verified") || strings.Contains(out, "mopus") {
		t.Fatalf("reply label should name its author without raw accounts:\n%s", out)
	}
	press(m, 's', tea.ModCtrl)
	if out := view(m); !strings.Contains(out, "opus(mopus)") || !strings.Contains(out, "reply a") {
		t.Fatalf("source mode shows raw identity and reference:\n%s", out)
	}
	if got := time.Unix(0, 0).Local().Format("15:04"); !strings.Contains(view(m), got) {
		t.Fatal("timestamps render in local time")
	}
}

func TestRosterAndMentionCompletion(t *testing.T) {
	m, _ := readyModel()
	m.opts.Identities = map[string]Identity{"mopus": {Name: "opus"}}
	m.Update(MembersMsg{Members: []Member{{Nick: "opus", Account: "mopus", Op: true}, {Nick: "orchestrator"}, {Nick: "sol"}}})
	out := view(m)
	if !strings.Contains(out, "3 online") {
		t.Fatal("header shows the roster count")
	}
	// Participants are open by default at 80 columns: operators first, then
	// others alphabetically; unknown accounts carry no glyph or qualifier.
	if !strings.Contains(out, "PARTICIPANTS") || strings.Index(out, "opus") > strings.Index(out, "orchestrator") || !strings.Contains(out, "@opus") || strings.Contains(out, "@ operator") || strings.Contains(out, "unverified") || strings.Contains(out, "?") {
		t.Fatalf("roster panel:\n%s", out)
	}
	paste(m, "ping @so")
	if m.suggest == nil || m.suggest.items[0].label != "@sol" {
		t.Fatal("mention suggestions must open while typing, before Tab")
	}
	press(m, tea.KeyTab, 0)
	if m.Draft() != "ping @sol " {
		t.Fatalf("unique completion: %q", m.Draft())
	}
	paste(m, "@o")
	if m.suggest == nil || len(m.suggest.items) != 2 || m.suggest.items[0].label != "@opus" {
		t.Fatalf("ambiguous mentions list operators first: %+v", m.suggest)
	}
	press(m, tea.KeyDown, 0)
	press(m, tea.KeyTab, 0)
	if m.Draft() != "ping @sol @orchestrator " {
		t.Fatalf("keyboard choice inserts the highlighted nick: %q", m.Draft())
	}
	if err := m.SetDraft("\tcode"); err != nil {
		t.Fatal(err)
	}
	m.editor.cursor = 0
	press(m, tea.KeyTab, 0)
	if m.Draft() != "\t\tcode" {
		t.Fatalf("Tab still indents outside an @mention: %q", m.Draft())
	}
}

func TestPeoplePanelKeepsTheReadingPosition(t *testing.T) {
	m, _ := readyModel() // 80 columns: the default side panel narrows the transcript.
	m.Update(MembersMsg{Members: []Member{{Nick: "opus"}, {Nick: "sol"}}})
	m.Update(IncomingMsg{Message: Message{ID: "wrapped", Text: strings.Repeat("reading ", 600)}})
	m.Update(IncomingMsg{Message: Message{ID: "newest", Text: "newest"}})
	m.View()
	m.moveScroll(-10)
	m.View()
	reading := m.lastTranscript[m.scroll]
	if m.follow || reading.key != "server:wrapped" || reading.offset == 0 {
		t.Fatalf("fixture must read inside wrapped text: %+v", reading)
	}
	press(m, 'p', tea.ModCtrl)
	m.View()
	row := m.lastTranscript[m.scroll]
	if m.rosterOpen || row.key != reading.key || row.line != reading.line || row.offset > reading.offset {
		t.Fatalf("closing the panel moved the reading position: before=%+v after=%+v", reading, row)
	}
	if next := m.scroll + 1; next < len(m.lastTranscript) && m.lastTranscript[next].key == reading.key && m.lastTranscript[next].line == reading.line && m.lastTranscript[next].offset <= reading.offset {
		t.Fatalf("the top row must be the one containing the reading position: %+v", m.lastTranscript[next])
	}
	press(m, 'p', tea.ModCtrl)
	m.View()
	if row := m.lastTranscript[m.scroll]; !m.rosterOpen || row.key != reading.key || row.line != reading.line || row.offset != reading.offset {
		t.Fatalf("reopening the panel must restore the reading row: before=%+v after=%+v", reading, row)
	}
}

func TestOlderPagesInsertBeforeHistoryWithoutUnread(t *testing.T) {
	var requested []string
	m, _ := readyModel()
	m.opts.LoadOlder = func(r HistoryRequest) tea.Cmd { requested = append(requested, r.Before); return nil }
	m.Update(IncomingMsg{Message: Message{ID: "c", Nick: "a", Text: "third", Replay: true}})
	m.Update(IncomingMsg{Message: Message{ID: "d", Nick: "a", Text: "fourth"}})
	press(m, tea.KeyPgUp, 0)
	if len(requested) != 1 || requested[0] != "c" || !m.loadingOlder {
		t.Fatalf("PgUp at the top requests the page before the oldest ID: %v", requested)
	}
	press(m, tea.KeyPgUp, 0)
	if len(requested) != 1 {
		t.Fatal("only one older page is outstanding")
	}
	for _, id := range []string{"a", "b"} {
		m.Update(IncomingMsg{Message: Message{ID: id, Nick: "a", Text: id, Replay: true, Older: true}})
	}
	m.Update(HistoryPageMsg{Count: 2})
	var order []string
	for _, msg := range m.Messages() {
		order = append(order, msg.ID)
	}
	if strings.Join(order, "") != "abcd" || m.unread != 0 || m.loadingOlder {
		t.Fatalf("older page order/unread: %v unread=%d", order, m.unread)
	}
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if len(requested) != 2 || requested[1] != "a" {
		t.Fatalf("wheel at the top requests the next page: %v", requested)
	}
	m.Update(HistoryPageMsg{Count: 0})
	press(m, tea.KeyPgUp, 0)
	if len(requested) != 2 || !strings.Contains(view(m), "Beginning of retained history") {
		t.Fatal("an empty page ends paging visibly")
	}
}

func TestOlderPagesKeepTheNewestContiguousWithinDisplayBounds(t *testing.T) {
	for _, bound := range []struct {
		name  string
		count int
		body  string
	}{
		{"logical lines", 34, strings.Repeat("x\n", 1023) + "x"},
		{"messages", maxMessages + 40, "x"},
	} {
		t.Run(bound.name, func(t *testing.T) {
			var requested []string
			m, _ := readyModel()
			m.opts.LoadOlder = func(r HistoryRequest) tea.Cmd { requested = append(requested, r.Before); return nil }
			m.Update(IncomingMsg{Message: Message{ID: "latest", Text: "newest displayed"}})
			press(m, tea.KeyPgUp, 0)
			for i := range bound.count {
				m.Update(IncomingMsg{Message: Message{ID: fmt.Sprintf("older-%03d", i), Text: bound.body, Replay: true, Older: true}})
			}
			m.Update(HistoryPageMsg{Count: bound.count})
			shown := m.Messages()
			lines := 0
			for _, msg := range shown {
				lines += logicalLines(msg.Text)
			}
			if len(shown) < 2 || shown[len(shown)-1].ID != "latest" || lines > maxLogicalLines || len(shown) > maxMessages {
				t.Fatalf("older pages must respect display bounds without displacing newer messages: %d messages, %d lines", len(shown), lines)
			}
			// The page's oldest messages give way, so no hole precedes "latest".
			skipped := bound.count - (len(shown) - 1)
			for i, msg := range shown[:len(shown)-1] {
				if msg.ID != fmt.Sprintf("older-%03d", skipped+i) {
					t.Fatalf("shown older history is not contiguous at %d: %s", i, msg.ID)
				}
			}
			if want := fmt.Sprintf("%d older messages were not shown", skipped); skipped == 0 || !strings.Contains(m.notice, want) {
				t.Fatalf("page completion must keep the skip notice %q: %q", want, m.notice)
			}
			press(m, tea.KeyPgUp, 0)
			if len(requested) != 1 {
				t.Fatalf("paging must stop once the display is full: %v", requested)
			}
		})
	}
}

// Live arrivals can evict a page's inserted messages while it loads; the
// insertion point must follow, so overflow never evicts a newer message.
func TestOlderInsertionTracksDisplayEvictions(t *testing.T) {
	ids := func(m *Model) string {
		var out []string
		for _, msg := range m.Messages() {
			out = append(out, msg.ID)
		}
		return strings.Join(out, " ")
	}
	live := func(m *Model, id string) { m.Update(IncomingMsg{Message: Message{ID: id, Text: id}}) }
	older := func(m *Model, id string) {
		m.Update(IncomingMsg{Message: Message{ID: id, Text: id, Replay: true, Older: true}})
	}
	var want []string

	// A full display: the only inserted older message is evicted by a live one.
	m, _ := readyModel()
	for i := range maxMessages - 1 {
		live(m, fmt.Sprintf("n%03d", i))
		want = append(want, fmt.Sprintf("n%03d", i))
	}
	m.loadingOlder = true
	older(m, "o1")
	live(m, "x")
	older(m, "o2")
	if got := ids(m); got != strings.Join(append(want, "x"), " ") || !m.historyEnd {
		t.Fatalf("an older message displaced newer history: %.80s…", got)
	}

	// A requested page interleaved with live arrivals stays in order.
	m, _ = readyModel()
	m.opts.LoadOlder = func(HistoryRequest) tea.Cmd { return nil }
	want = nil
	for i := range maxMessages - 50 {
		live(m, fmt.Sprintf("n%03d", i))
	}
	press(m, tea.KeyPgUp, 0)
	for i := range 49 {
		older(m, fmt.Sprintf("o%03d", i))
	}
	live(m, "x0")
	live(m, "x1")    // evicts o000
	older(m, "o049") // the display is full: o001 gives way
	for i := 2; i < 50; i++ {
		want = append(want, fmt.Sprintf("o%03d", i))
	}
	for i := range maxMessages - 50 {
		want = append(want, fmt.Sprintf("n%03d", i))
	}
	if got := ids(m); got != strings.Join(append(want, "x0", "x1"), " ") {
		t.Fatalf("older insertion lost its position after a live eviction:\n%s", got)
	}
}

func TestNegotiatedLimitsRefuseOverlongDraftsBeforeSending(t *testing.T) {
	m, sends := readyModel()
	m.Update(ConnectionMsg{Connected: true, MaxBytes: 64, MaxLines: 3})
	if m.editor.limit != 64 {
		t.Fatalf("negotiated byte limit not applied: %d", m.editor.limit)
	}
	paste(m, "a\nb\nc\nd")
	press(m, tea.KeyEnter, 0)
	if len(*sends) != 0 || !strings.Contains(m.notice, "accepts 3") || m.Draft() != "a\nb\nc\nd" {
		t.Fatalf("too many lines must be refused with the draft kept: %q", m.notice)
	}
	if err := m.SetDraft(strings.Repeat("x", 65)); err == nil {
		t.Fatal("draft exceeded the negotiated byte limit")
	}
}

func TestDisconnectExplainsReconnectAndUncertainty(t *testing.T) {
	m, _ := readyModel()
	paste(m, "in flight")
	press(m, tea.KeyEnter, 0)
	m.Update(ConnectionMsg{Connected: false})
	if !strings.Contains(m.notice, "rerun yoloctl chat") || !strings.Contains(m.notice, "uncertain") || m.Messages()[0].Delivery != Uncertain {
		t.Fatalf("disconnect guidance: %q", m.notice)
	}
}

func TestInlineMarkdownStylesWithoutChangingTheOriginal(t *testing.T) {
	theme := New(Options{}).theme
	for _, tc := range []struct{ in, want string }{
		{"use `go test ./...` **now** and *carefully*", "use go test ./... now and carefully"},
		{"***both*** and ~~gone~~ and ~single~", "both and gone and ~single~"},
		{"keep snake_case and a * b literal", "keep snake_case and a * b literal"},
		{"see [docs](https://example.com/x)", "see docs (https://example.com/x)"},
		{"`unclosed and **open", "`unclosed and **open"},
		{`escaped \*stars\*`, "escaped *stars*"},
	} {
		got, _ := inlineMarkdown(tc.in, spaceview.Style{FG: theme.Text}, theme)
		if got != tc.want {
			t.Errorf("%q rendered %q, want %q", tc.in, got, tc.want)
		}
	}
	m, _ := readyModel()
	original := "- item with `code`\n```\n**not bold in code**\n```"
	m.Update(IncomingMsg{Message: Message{ID: "x", Nick: "a", Text: original}})
	out := view(m)
	if !strings.Contains(out, "• item with code") || !strings.Contains(out, "**not bold in code**") {
		t.Fatalf("lists render; fenced code stays literal:\n%s", out)
	}
	if m.Messages()[0].Text != original {
		t.Fatal("rendering changed the original text")
	}
}
