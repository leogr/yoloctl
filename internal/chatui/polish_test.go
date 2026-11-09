package chatui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// cellOf returns the screen cell showing byte offset of display line line in
// the message with key, from the last frame.
func cellOf(t *testing.T, m *Model, key string, line, offset int) (int, int) {
	t.Helper()
	for i, row := range m.lastTranscript {
		if row.key != key || row.line != line || row.kind != rowBody || offset < row.offset || offset > row.end {
			continue
		}
		if offset == row.end && i+1 < len(m.lastTranscript) && m.lastTranscript[i+1].line == line && m.lastTranscript[i+1].key == key {
			continue // the next row starts here
		}
		x := 0
		for _, r := range row.runs {
			if r.Copy && offset >= r.Start && offset <= r.Start+len(r.Text) {
				return x + displayWidth(ansi.WcWidth, r.Text[:offset-r.Start]), i - m.scroll + m.layout.bodyTop
			}
			x += displayWidth(ansi.WcWidth, r.Text)
		}
	}
	t.Fatalf("no cell for %s line %d offset %d", key, line, offset)
	return 0, 0
}

func drag(m *Model, x0, y0, x1, y1 int) tea.Cmd {
	m.Update(tea.MouseClickMsg{X: x0, Y: y0, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: x1, Y: y1, Button: tea.MouseLeft})
	_, cmd := m.Update(tea.MouseReleaseMsg{X: x1, Y: y1, Button: tea.MouseLeft})
	return cmd
}

func copyingModel(width, height int) (*Model, *[]string) {
	m, _ := readyModel()
	var copied []string
	m.opts.Copy = func(text string) tea.Cmd { copied = append(copied, text); return nil }
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return m, &copied
}

// A drag copies exactly the displayed body text: soft wraps rejoin without
// a break, logical lines keep their newline, messages are separated by one
// blank line, and names, times, gutters and fence frames never appear.
func TestDragCopiesExactDisplayedText(t *testing.T) {
	m, copied := copyingModel(40, 30)
	m.opts.Identities = map[string]Identity{"ma": {Name: "alice"}}
	first := "αβγ 界 e\u0301 one two three four five six seven eight nine\nsecond line"
	m.Update(IncomingMsg{Message: Message{ID: "a", Account: "ma", Nick: "alice", Text: first, Time: time.Unix(0, 0)}})
	m.Update(IncomingMsg{Message: Message{ID: "b", Account: "ma", Nick: "alice", Text: "```\n\tcode  indent 👩‍💻\n```", Time: time.Unix(60, 0)}})
	m.View()
	if wraps := 0; true {
		for _, row := range m.lastTranscript {
			if row.key == "server:a" && row.line == 0 {
				wraps++
			}
		}
		if wraps < 2 {
			t.Fatal("fixture must soft-wrap the first line")
		}
	}
	x0, y0 := cellOf(t, m, "server:a", 0, 0)
	code := "    code  indent 👩‍💻" // displayed: the tab expands to spaces
	x1, y1 := cellOf(t, m, "server:b", 1, strings.Index(code, " 👩"))
	drag(m, x0, y0, x1, y1)
	want := strings.Replace(first, "\n", "\n", 1) + "\n\n" + code[:strings.Index(code, " 👩")+1]
	if len(*copied) != 1 || (*copied)[0] != want {
		t.Fatalf("copied %q\nwant   %q", *copied, want)
	}
	if !strings.Contains(m.notice, "Copied") {
		t.Fatalf("copy is reported: %q", m.notice)
	}
	// Backwards over a wide grapheme includes it whole at both ends.
	*copied = nil
	xa, ya := cellOf(t, m, "server:a", 0, strings.Index(first, "界"))
	xb, yb := cellOf(t, m, "server:a", 0, 0)
	drag(m, xa+1, ya, xb, yb)
	if len(*copied) != 1 || (*copied)[0] != "αβγ 界" {
		t.Fatalf("backwards drag over a wide grapheme: %q", *copied)
	}
}

func TestClickNeverCopiesAndActsOnlyOnTheSameTarget(t *testing.T) {
	m, copied := copyingModel(60, 20)
	var opened []string
	m.opts.OpenLink = func(url string) tea.Cmd { opened = append(opened, url); return nil }
	m.Update(IncomingMsg{Message: Message{ID: "a", Nick: "x", Text: "see https://example.org/doc please"}})
	m.View()
	x, y := cellOf(t, m, "server:a", 0, strings.Index("see https://example.org/doc please", "example"))
	// Press on the link, release elsewhere on the same row: no click.
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: 1, Y: y, Button: tea.MouseLeft})
	if len(opened) != 0 || len(*copied) != 0 {
		t.Fatal("a press and a release on different targets acted")
	}
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	if len(opened) != 1 || opened[0] != "https://example.org/doc" || len(*copied) != 0 {
		t.Fatalf("clicking a link opens it and copies nothing: %v %v", opened, *copied)
	}
	// A plain click on text selects the message for its actions.
	m.Update(tea.MouseClickMsg{X: 3, Y: y, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: 3, Y: y, Button: tea.MouseLeft})
	if !m.picked || m.focus != focusMessages || len(*copied) != 0 {
		t.Fatal("a click must select the message without copying")
	}
}

func TestClipboardOffNeverWritesAndSaysWhy(t *testing.T) {
	m, copied := copyingModel(60, 20)
	m.opts.Clipboard = "off"
	m.Update(IncomingMsg{Message: Message{ID: "a", Nick: "x", Text: "copy me if you can"}})
	m.View()
	x0, y := cellOf(t, m, "server:a", 0, 0)
	x1, _ := cellOf(t, m, "server:a", 0, 7)
	drag(m, x0, y, x1, y)
	press(m, 'y', tea.ModAlt)
	if len(*copied) != 0 || !strings.Contains(m.notice, "seams.clipboard") || m.sel == nil {
		t.Fatalf("clipboard off must not copy and must explain: %v %q", *copied, m.notice)
	}
}

func TestSelectionFollowsTextAcrossResizeAndClearsOnEviction(t *testing.T) {
	m, _ := copyingModel(60, 20)
	m.Update(IncomingMsg{Message: Message{ID: "a", Nick: "x", Text: strings.Repeat("word ", 40)}})
	m.View()
	x0, y0 := cellOf(t, m, "server:a", 0, 5)
	x1, y1 := cellOf(t, m, "server:a", 0, 60)
	drag(m, x0, y0, x1, y1)
	before, _ := m.selectedText(*m.sel)
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 20})
	m.View()
	if after, _ := m.selectedText(*m.sel); after != before || before == "" {
		t.Fatalf("resize changed the selected text: %q -> %q", before, after)
	}
	for i := range maxMessages + 1 {
		m.Update(IncomingMsg{Message: Message{ID: "n" + string(rune('a'+i%26)) + strings.Repeat("x", i/26), Nick: "x", Text: "new"}})
	}
	if m.sel != nil {
		t.Fatal("evicting the selected message must drop the selection")
	}
}

func TestSuggestionsAndSlashCommandsKeepTypingLiteral(t *testing.T) {
	m, sends := readyModel()
	m.Update(MembersMsg{Members: []Member{{Nick: "fable", Account: "mf"}, {Nick: "finn", Account: "mi"}}})
	paste(m, "hi @f")
	press(m, tea.KeyEnter, 0) // accepts the open popup, never sends
	if m.Draft() != "hi @fable " || len(*sends) != 0 {
		t.Fatalf("Enter on an open popup inserts the nick: %q %d", m.Draft(), len(*sends))
	}
	press(m, tea.KeyEnter, 0)
	if len(*sends) != 1 || (*sends)[0].Text != "hi @fable " {
		t.Fatalf("Enter sends once the popup is closed: %v", *sends)
	}
	m.Update(DeliveryMsg{ID: (*sends)[0].ID, MessageID: "s1", State: Confirmed})
	paste(m, "@fable")
	if m.suggest != nil {
		t.Fatal("an exact unique match closes the popup so Enter sends")
	}
	_ = m.SetDraft("")
	paste(m, "@zz")
	if m.suggest != nil {
		t.Fatal("no match must not open a popup")
	}
	for _, tc := range []struct {
		text string
		sent bool
	}{
		{"/workdir/repo is ready", true},
		{"/help me", true},
		{"/help", false},
		{"/topic", false},
		{"/topic new title", false},
		{"/nick bad!", false},
	} {
		m, sends := readyModel()
		paste(m, tc.text)
		m.suggest = nil
		press(m, tea.KeyEnter, 0)
		if (len(*sends) == 1) != tc.sent {
			t.Errorf("%q: sent=%v", tc.text, len(*sends) == 1)
		}
	}
	m, _ = readyModel()
	paste(m, "/he")
	press(m, tea.KeyEnter, 0)
	if m.overlay == nil || m.overlay.kind != overlayHelp || m.Draft() != "" {
		t.Fatal("choosing /help from the popup runs it")
	}
}

// Choosing a command from the popup runs it only when it is the whole
// draft; with other text after it, the choice just completes the word.
func TestChosenCommandRunsOnlyAsTheWholeDraft(t *testing.T) {
	m, sends := readyModel()
	paste(m, "/he more words")
	m.editor.cursor = len("/he")
	m.refreshSuggest()
	if m.suggest == nil {
		t.Fatal("fixture: the command popup must be open")
	}
	press(m, tea.KeyEnter, 0)
	if m.overlay != nil || len(*sends) != 0 || m.Draft() != "/help more words" {
		t.Fatalf("a partial command draft ran or was sent: overlay=%v sends=%d draft=%q", m.overlay != nil, len(*sends), m.Draft())
	}
}

// Message focus offers actions through arrows and Enter only: typed text
// always returns to the composer as text and never fires an action.
func TestMessageFocusTypingIsAlwaysText(t *testing.T) {
	m, _ := readyModel()
	var copied []string
	m.opts.Copy = func(text string) tea.Cmd { copied = append(copied, text); return nil }
	m.Update(IncomingMsg{Message: Message{ID: "a", Nick: "x", Text: "first"}})
	press(m, tea.KeyUp, tea.ModAlt)
	if m.focus != focusMessages || !m.picked {
		t.Fatal("Alt+Up selects a message")
	}
	for _, r := range "copy" {
		m.Update(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
	}
	if m.Draft() != "copy" || len(copied) != 0 || m.focus != focusComposer {
		t.Fatalf("typing from message focus must be text: %q %v", m.Draft(), copied)
	}
	press(m, tea.KeyUp, tea.ModAlt)
	actions := m.messageActions()
	for i, a := range actions {
		if a.id == "copy" {
			m.actionIndex = i
		}
	}
	press(m, tea.KeyEnter, 0)
	if len(copied) != 1 || copied[0] != "first" || !strings.Contains(m.notice, "x's message") {
		t.Fatalf("Enter runs the focused action and names its target: %v %q", copied, m.notice)
	}
	m, _ = readyModel()
	press(m, 'o', tea.ModCtrl)
	if !strings.Contains(m.notice, "No message selected") {
		t.Fatalf("actions without a target explain themselves: %q", m.notice)
	}
}

func TestAttachResolvesByAccountAndConfirmsStarts(t *testing.T) {
	m, _ := readyModel()
	m.opts.Identities = map[string]Identity{"mbox": {Name: "coder", Box: true}, "mh": {Name: "lee", Human: true}}
	var resolved []string
	var attached []AttachTarget
	m.opts.ResolveAttach = func(account string) tea.Cmd { resolved = append(resolved, account); return nil }
	m.opts.Attach = func(t AttachTarget) tea.Cmd { attached = append(attached, t); return nil }
	// Human, unverified and spoofed nicks open a card; nothing is resolved.
	for _, who := range [][2]string{{"lee", "mh"}, {"coder", "mstranger"}, {"coder", ""}} {
		m.overlay = nil
		m.requestAttach(who[0], who[1])
		if m.overlay == nil || m.overlay.kind != overlayCard || len(resolved) != 0 {
			t.Fatalf("%v must open a card without resolving", who)
		}
	}
	m.overlay = nil
	m.requestAttach("anything", "mbox")
	if len(resolved) != 1 || resolved[0] != "mbox" {
		t.Fatalf("a trusted box resolves by account, not nick: %v", resolved)
	}
	running := AttachTarget{Account: "mbox", Box: "coder", BoxID: "b", State: "running", RoomID: "r", MemberID: "m"}
	m.Update(AttachTargetMsg{Target: running})
	if len(attached) != 1 || attached[0] != running {
		t.Fatalf("a running match attaches at once: %v", attached)
	}
	stopped := running
	stopped.State = "stopped"
	m.Update(AttachTargetMsg{Target: stopped})
	if len(attached) != 1 || m.overlay == nil || m.overlay.kind != overlayConfirm {
		t.Fatal("a stopped box asks before starting")
	}
	press(m, tea.KeyEnter, 0)
	if len(attached) != 2 || attached[1].State != "stopped" {
		t.Fatalf("confirmation attaches with consent to start: %v", attached)
	}
	for _, state := range []string{"missing", "revoked"} {
		m.Update(AttachTargetMsg{Target: AttachTarget{State: state, Reason: "its box was " + state}})
		if len(attached) != 2 || !strings.Contains(m.notice, state) {
			t.Fatalf("%s must explain and not attach: %q", state, m.notice)
		}
	}
	m.Update(AttachTargetMsg{Err: errors.New("controller unavailable")})
	if !strings.Contains(m.notice, "controller unavailable") {
		t.Fatal("a lookup failure is visible")
	}
}

func TestSaveAndReconnectKeepsDraftAndNeverHidesAPendingSend(t *testing.T) {
	m, sends := readyModel()
	var saved []string
	reconnects := 0
	m.opts.SaveNick = func(nick string) tea.Cmd { saved = append(saved, nick); return nil }
	m.opts.Reconnect = func() tea.Cmd { reconnects++; return nil }
	paste(m, "pending text")
	press(m, tea.KeyEnter, 0)
	m.saveNick("newnick")
	if len(saved) != 0 || !strings.Contains(m.notice, "unresolved") {
		t.Fatalf("an unresolved send blocks Save and reconnect: %v %q", saved, m.notice)
	}
	m.Update(DeliveryMsg{ID: (*sends)[0].ID, MessageID: "x", State: Confirmed})
	paste(m, "draft kept")
	m.replyTo = "x"
	m.openSettings()
	m.overlay.index = 0
	press(m, tea.KeyEnter, 0)
	press(m, 'u', tea.ModCtrl)
	for _, r := range "1bad" {
		m.Update(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
	}
	press(m, tea.KeyEnter, 0)
	if len(saved) != 0 || !strings.Contains(m.overlay.problem, "letters") {
		t.Fatalf("an invalid nickname is explained before saving: %q", m.overlay.problem)
	}
	_ = m.overlay.nick.replace(0, len(m.overlay.nick.text), "newnick")
	press(m, tea.KeyEnter, 0)
	if len(saved) != 1 || saved[0] != "newnick" {
		t.Fatalf("Enter saves the nickname: %v", saved)
	}
	m.Update(NickSavedMsg{Nick: "newnick"})
	if reconnects != 1 || !m.reconnecting {
		t.Fatal("a saved nickname reconnects explicitly once")
	}
	m.Update(ConnectionMsg{})
	if !strings.Contains(m.notice, "Reconnecting") {
		t.Fatalf("the requested disconnect is not an error: %q", m.notice)
	}
	m.Update(SessionMsg{Nick: "newnick", Identities: map[string]Identity{}})
	m.Update(ConnectionMsg{Connected: true})
	if m.opts.Nick != "newnick" || m.Draft() != "draft kept" || m.replyTo != "x" || len(*sends) != 1 || !strings.Contains(m.notice, "Reconnected as newnick") {
		t.Fatalf("reconnect must keep draft and reply and resend nothing: nick=%q draft=%q reply=%q sends=%d %q", m.opts.Nick, m.Draft(), m.replyTo, len(*sends), m.notice)
	}
}

func TestPresenceNoticesFollowRosterChanges(t *testing.T) {
	m, copied := copyingModel(80, 24)
	m.opts.Identities = map[string]Identity{"mo": {Name: "opus"}}
	m.Update(MembersMsg{Members: []Member{{Nick: "opus", Account: "mo"}, {Nick: "sol", Account: "ms"}}})
	if len(m.presence) != 0 {
		t.Fatal("the first roster is the existing room, not joins")
	}
	m.Update(IncomingMsg{Message: Message{ID: "a", Nick: "opus", Account: "mo", Text: "hello"}})
	m.Update(MembersMsg{Members: []Member{{Nick: "opus2", Account: "mo", Op: true}, {Nick: "qa", Account: "mq"}, {Nick: "fable", Account: "mf"}}})
	out := view(m)
	for _, want := range []string{"opus → opus2", "qa, fable joined", "sol left"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	m.Update(MembersMsg{Members: []Member{{Nick: "opus2", Account: "mo"}, {Nick: "qa", Account: "mq", Voice: true}, {Nick: "fable", Account: "mf"}}})
	if out := view(m); !strings.Contains(out, "is no longer an operator") || !strings.Contains(out, "qa was given voice") {
		t.Fatalf("role changes are visible:\n%s", out)
	}
	m.Update(TopicMsg{Text: "Ship it", By: "opus2", Live: true})
	if out := view(m); !strings.Contains(out, "opus2 set the topic: Ship it") || !strings.Contains(out, "Ship it") {
		t.Fatalf("topic changes are visible:\n%s", out)
	}
	// Notices are display-only: a drag across them copies the message only.
	x0, y0 := cellOf(t, m, "server:a", 0, 0)
	drag(m, x0, y0, 70, m.layout.bodyTop+m.layout.bodyHeight-1)
	if len(*copied) != 1 || (*copied)[0] != "hello" {
		t.Fatalf("presence notices must not be copied: %q", *copied)
	}
}

func TestPresenceKeepsAnAccountWithAnotherConnection(t *testing.T) {
	m, _ := copyingModel(80, 24)
	m.opts.Identities = map[string]Identity{"mh": {Name: "leogr", Human: true}, "mo": {Name: "opus"}}
	m.Update(MembersMsg{Members: []Member{{Nick: "leogr-aaaaa", Account: "mh"}, {Nick: "opus", Account: "mo"}}})
	// A second chat view of the same human connects, then the first closes.
	m.Update(MembersMsg{Members: []Member{{Nick: "leogr-aaaaa", Account: "mh"}, {Nick: "leogr-bbbbb", Account: "mh"}, {Nick: "opus", Account: "mo"}}})
	if len(m.members) != 3 {
		t.Fatalf("the roster must still list each connection: %+v", m.members)
	}
	m.Update(MembersMsg{Members: []Member{{Nick: "leogr-bbbbb", Account: "mh"}, {Nick: "opus", Account: "mo"}}})
	if len(m.presence) != 0 {
		t.Fatalf("another connection of a present account is no arrival or departure: %+v", m.presence)
	}
	m.Update(MembersMsg{Members: []Member{{Nick: "opus", Account: "mo"}}})
	if out := view(m); !strings.Contains(out, "leogr-bbbbb left") {
		t.Fatalf("the account's last connection leaving is a departure:\n%s", out)
	}
}

func TestParticipantsSortOperatorsVoiceThenOthers(t *testing.T) {
	got := sortedMembers([]Member{{Nick: "zed"}, {Nick: "Bob", Voice: true}, {Nick: "amy", Op: true, Voice: true}, {Nick: "Carl"}, {Nick: "al", Voice: true}, {Nick: "Zoe", Op: true}})
	var nicks []string
	for _, m := range got {
		nicks = append(nicks, m.Nick)
	}
	if strings.Join(nicks, " ") != "amy Zoe al Bob Carl zed" {
		t.Fatalf("order: %v", nicks)
	}
}

// Scrolling moves one row per wheel event, as the TUI does for
// high-resolution hosts, and reuses rows built for unchanged content.
func TestWheelScrollsOneRowWithoutRebuildingRows(t *testing.T) {
	m, _ := readyModel()
	for i := range 40 {
		m.Update(IncomingMsg{Message: Message{ID: string(rune('a'+i%26)) + strings.Repeat("z", i/26), Nick: "x", Text: "line"}})
	}
	m.View()
	bottom := m.scroll
	built := &m.built.rows[0]
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	m.View()
	if m.scroll != bottom-1 || &m.built.rows[0] != built {
		t.Fatalf("wheel moved %d rows or rebuilt the transcript", bottom-m.scroll)
	}
	press(m, tea.KeyPgUp, 0)
	m.View()
	if m.scroll != bottom-1-(m.lastBodyHeight-2) {
		t.Fatalf("PgUp moves one screen minus two rows: %d", bottom-m.scroll)
	}
	m.Update(IncomingMsg{Message: Message{ID: "new", Nick: "x", Text: "arrives"}})
	m.View()
	if &m.built.rows[0] == built || m.follow {
		t.Fatal("an arrival rebuilds rows and keeps the reader in place")
	}
}

// Inline code is accent text without a background or padding, and copies
// exactly as shown.
func TestInlineCodeIsUnpaddedAccentText(t *testing.T) {
	m, copied := copyingModel(60, 20)
	m.Update(IncomingMsg{Message: Message{ID: "a", Nick: "x", Text: "run `go test` now"}})
	m.View()
	if out := view(m); !strings.Contains(out, "run go test now") {
		t.Fatalf("inline code renders without padding:\n%s", out)
	}
	code := spaceview.StyleText("go test", spaceview.Style{FG: m.theme.Brand})
	if !strings.Contains(m.View().Content, code) {
		t.Fatal("inline code uses the accent color and no background")
	}
	x0, y := cellOf(t, m, "server:a", 0, 0)
	x1, _ := cellOf(t, m, "server:a", 0, len("run go test no"))
	drag(m, x0, y, x1, y)
	if len(*copied) != 1 || (*copied)[0] != "run go test now" {
		t.Fatalf("inline code copies as shown: %q", *copied)
	}
}

func scrolledModel(t *testing.T) *Model {
	t.Helper()
	m, _ := readyModel()
	for i := range 60 {
		m.Update(IncomingMsg{Message: Message{ID: "m" + string(rune('a'+i%26)) + strings.Repeat("z", i/26), Nick: "x", Text: "line"}})
	}
	m.View()
	return m
}

// Keyboard scrolling, typing and key releases repaint visible rows only;
// a change to what rows show (here a selection mark) rebuilds them.
func TestKeysTypingAndReleasesReuseTranscriptRows(t *testing.T) {
	m := scrolledModel(t)
	built := &m.built.rows[0]
	for _, msg := range []tea.Msg{
		tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp}),
		tea.KeyPressMsg(tea.Key{Code: tea.KeyUp, Mod: tea.ModCtrl}),
		tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}),
		tea.KeyReleaseMsg(tea.Key{Code: 'x', Text: "x"}),
		tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown}),
	} {
		m.Update(msg)
		m.View()
		if &m.built.rows[0] != built {
			t.Fatalf("%#v rebuilt unchanged rows", msg)
		}
	}
	press(m, tea.KeyUp, tea.ModAlt)
	m.View()
	if &m.built.rows[0] == built {
		t.Fatal("selecting a message must rebuild its marked rows")
	}
}

// A tick scheduled by an earlier drag neither scrolls nor re-arms, so one
// gesture never runs two auto-scroll chains.
func TestStaleDragTickIsIgnored(t *testing.T) {
	m := scrolledModel(t)
	m.moveScroll(-20)
	m.View()
	top := m.layout.bodyTop
	m.Update(tea.MouseClickMsg{X: 5, Y: top + 2, Button: tea.MouseLeft})
	_, first := m.Update(tea.MouseMotionMsg{X: 5, Y: top - 1, Button: tea.MouseLeft})
	stale := dragTickMsg{gesture: m.gesture}
	m.Update(tea.MouseReleaseMsg{X: 5, Y: top - 1, Button: tea.MouseLeft})
	m.Update(tea.MouseClickMsg{X: 5, Y: top + 2, Button: tea.MouseLeft})
	_, second := m.Update(tea.MouseMotionMsg{X: 5, Y: top - 1, Button: tea.MouseLeft})
	if first == nil || second == nil {
		t.Fatal("dragging past the edge must schedule auto-scroll")
	}
	before := m.scroll
	if _, cmd := m.Update(stale); cmd != nil || m.scroll != before {
		t.Fatal("a stale tick moved or re-armed auto-scroll")
	}
	if _, cmd := m.Update(dragTickMsg{gesture: m.gesture}); cmd == nil || m.scroll != before-1 {
		t.Fatalf("the current gesture's tick scrolls one row: %d -> %d", before, m.scroll)
	}
}

func TestEveryRouteToLiveClearsUnreadMentions(t *testing.T) {
	m := scrolledModel(t)
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	m.opts.Account = "mhuman"
	m.Update(IncomingMsg{Message: Message{ID: "ping", Nick: "x", Account: "mx", Text: "@human look"}})
	if m.unread != 1 || m.unreadMentions != 1 {
		t.Fatalf("fixture: unread=%d mentions=%d", m.unread, m.unreadMentions)
	}
	m.View()
	for range 10 {
		m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
		m.View()
	}
	if !m.follow || m.unread != 0 || m.unreadMentions != 0 {
		t.Fatalf("scrolling back to live must clear both counters: follow=%v unread=%d mentions=%d", m.follow, m.unread, m.unreadMentions)
	}
}

// Joins, departures and role changes are rows of their own: selecting the
// message before them neither marks nor reaches them.
func TestRoomEventsStandApartFromMessages(t *testing.T) {
	m, copied := copyingModel(80, 30)
	m.opts.Identities = map[string]Identity{"ms": {Name: "sol"}}
	m.Update(MembersMsg{Members: []Member{{Nick: "sol", Account: "ms"}}})
	m.Update(IncomingMsg{Message: Message{ID: "a", Nick: "sol", Account: "ms", Text: "hello @human"}})
	m.Update(MembersMsg{Members: []Member{{Nick: "sol", Account: "ms", Op: true}, {Nick: "qa", Account: "mq"}}})
	press(m, tea.KeyUp, tea.ModAlt)
	if m.selectedKey() != "server:a" {
		t.Fatal("the message is selected")
	}
	lines := strings.Split(view(m), "\n")
	y, body := lineWith(t, m, "hello @human")
	jy, join := lineWith(t, m, "qa joined")
	oy, op := lineWith(t, m, "sol became an operator")
	if !strings.HasPrefix(body, "▌") || strings.HasPrefix(join, "▌") || strings.HasPrefix(op, "▌") {
		t.Fatalf("the selection mark stops at the message:\n%s", view(m))
	}
	if gap, _, _ := strings.Cut(lines[y+1], "│"); jy != y+2 || strings.TrimSpace(gap) != "" || oy != jy+1 {
		t.Fatalf("a blank row separates the events from the message:\n%s", view(m))
	}
	press(m, tea.KeyEscape, 0)
	press(m, tea.KeyEscape, 0)
	m.Update(tea.MouseClickMsg{X: 6, Y: jy, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: 6, Y: jy, Button: tea.MouseLeft})
	if m.selectedKey() != "" {
		t.Fatal("clicking an event does not select the message before it")
	}
	x0, y0 := cellOf(t, m, "server:a", 0, 0)
	drag(m, x0, y0, 70, oy)
	if len(*copied) != 1 || (*copied)[0] != "hello @human" {
		t.Fatalf("a drag into the events copies the message only: %q", *copied)
	}
}
