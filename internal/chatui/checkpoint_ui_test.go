package chatui

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/config"
)

// underlined reports whether text is drawn with the hover style (bold and
// underlined) somewhere in a raw frame.
func underlined(raw, text string) bool {
	return regexp.MustCompile(`\x1b\[1;4(;[0-9;]*)?m` + regexp.QuoteMeta(text)).MatchString(raw)
}

func hitFor(t *testing.T, m *Model, area hitArea, item int) (int, int) {
	t.Helper()
	for _, h := range m.hits {
		if h.area == area && h.item == item {
			return h.x0, h.y
		}
	}
	t.Fatalf("no hit for area %d item %d", area, item)
	return 0, 0
}

func lineWith(t *testing.T, m *Model, text string) (int, string) {
	t.Helper()
	for y, line := range strings.Split(view(m), "\n") {
		if strings.Contains(line, text) {
			return y, line
		}
	}
	t.Fatalf("no line shows %q:\n%s", text, view(m))
	return 0, ""
}

func move(m *Model, x, y int) { m.Update(tea.MouseMotionMsg{X: x, Y: y}) }

// The operator mark touches the nick on the name row, follows the observed
// channel role only, and survives truncation.
func TestOperatorMarkIsAttachedToTheNick(t *testing.T) {
	m, _ := readyModel()
	m.opts.Account = "mleo"
	m.opts.Identities = map[string]Identity{"mleo": {Name: "leogr", Human: true}, "msol": {Name: "sol", Box: true}, "mqa": {Name: "qa", Human: true}}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.Update(MembersMsg{Members: []Member{{Nick: "leogr", Account: "mleo", Op: true}, {Nick: "sol", Account: "msol", Voice: true}, {Nick: "qa", Account: "mqa"}}})
	out := view(m)
	if !strings.Contains(out, "@leogr · you") || !strings.Contains(out, "+sol") || strings.Contains(out, "@ operator") || strings.Contains(out, "+ voice") {
		t.Fatalf("card rows must attach the role mark to the nick:\n%s", out)
	}
	_, leo := lineWith(t, m, "@leogr")
	_, qa := lineWith(t, m, " qa")
	if strings.Index(leo, "leogr")-strings.Index(leo, humanGlyph) != strings.Index(qa, "qa")-strings.Index(qa, humanGlyph) {
		t.Fatalf("names must stay aligned whether or not they carry a mark:\n%s\n%s", leo, qa)
	}
	if strings.Contains(out, "@qa") {
		t.Fatal("a trusted human without +o must not get an operator mark")
	}
	m.Update(MembersMsg{Members: []Member{{Nick: "leogr", Account: "mleo"}, {Nick: "sol", Account: "msol", Op: true}}})
	if out := view(m); strings.Contains(out, "@leogr") || !strings.Contains(out, "@sol") {
		t.Fatalf("MODE changes move the mark:\n%s", out)
	}

	// Compact rows (a short screen) and a narrow panel keep mark and dot.
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	long := strings.Repeat("n", 30)
	m.Update(MembersMsg{Members: []Member{{Nick: long, Account: "mx", Op: true}, {Nick: "a"}, {Nick: "b"}, {Nick: "c"}}})
	_, row := lineWith(t, m, "@nnn")
	if !strings.Contains(row, "…") || strings.Contains(row, "?") {
		t.Fatalf("a truncated compact row keeps the mark and ellipsis, with no trust mark: %q", row)
	}
}

func TestHumanGlyphAndIconSlots(t *testing.T) {
	for glyph, want := range map[string]string{"✳": "✳  ", ">_": ">_ ", humanGlyph: humanGlyph + " ", "界": "界 "} {
		if got := iconSlot(ansi.WcWidth, glyph); got != want {
			t.Errorf("iconSlot(ansi.WcWidth, %q) = %q, want %q", glyph, got, want)
		}
	}
	if ansi.StringWidthWc(humanGlyph) != 2 {
		t.Fatal("the human glyph fills the two-cell icon slot")
	}
	m, _ := readyModel()
	m.opts.Identities = map[string]Identity{"mleo": {Name: "leogr", Human: true}}
	m.Update(IncomingMsg{Message: Message{ID: "a", Account: "mleo", Nick: "leogr", Text: "hi"}})
	if out := view(m); !strings.Contains(out, humanGlyph+" leogr") {
		t.Fatalf("humans use the bust glyph in the two-cell slot:\n%s", out)
	}
	// Panel rows keep names aligned across one- and two-cell glyphs.
	m.opts.Identities["mopus"] = Identity{Name: "opus", Box: true, Agent: "claude"}
	m.opts.Icons = map[string]config.AgentIcon{"claude": {Icon: "✳"}}
	m.Update(MembersMsg{Members: []Member{{Nick: "leogr", Account: "mleo"}, {Nick: "opus", Account: "mopus"}}})
	for _, height := range []int{30, 9} { // cards, then compact rows
		m.Update(tea.WindowSizeMsg{Width: 100, Height: height})
		panel := func(name string) string {
			for _, line := range strings.Split(view(m), "\n") {
				if _, side, ok := strings.Cut(line, "│"); ok && strings.Contains(side, " "+name) {
					return side
				}
			}
			t.Fatalf("no panel row shows %s:\n%s", name, view(m))
			return ""
		}
		leo, opus := panel("leogr"), panel("opus")
		if c, d := ansi.StringWidthWc(leo[:strings.Index(leo, "leogr")]), ansi.StringWidthWc(opus[:strings.Index(opus, "opus")]); c != d {
			t.Fatalf("at height %d the human and agent names start at cells %d and %d:\n%s\n%s", height, c, d, leo, opus)
		}
	}
}

func TestHoverHighlightsTargetsLikeSpaceView(t *testing.T) {
	m, _ := readyModel()
	m.opts.Identities = map[string]Identity{"mopus": {Name: "opus", Box: true}}
	m.Update(MembersMsg{Members: []Member{{Nick: "opus", Account: "mopus"}}})
	m.Update(IncomingMsg{Message: Message{ID: "l", Account: "mopus", Nick: "opus", Text: "see https://example.com/x now"}})
	if v := m.View(); v.MouseMode != tea.MouseModeAllMotion {
		t.Fatalf("buttonless hover needs all-motion reporting: %v", v.MouseMode)
	}
	x, y := hitFor(t, m, hitPanel, 0)
	move(m, x+8, y)
	raw := m.View().Content
	if !underlined(raw, "opus") || !strings.Contains(view(m), "▸ opus  click to attach · right-click for details") {
		t.Fatalf("a hovered box participant is highlighted and explained:\n%s", view(m))
	}
	move(m, 0, m.layout.bodyTop) // empty transcript space
	if underlined(m.View().Content, "opus") || strings.Contains(view(m), "▸ opus") {
		t.Fatal("moving off a target clears its highlight")
	}

	// A link in the transcript, then the top band's channel target.
	ly, line := lineWith(t, m, "https://example.com/x")
	move(m, ansi.StringWidthWc(line[:strings.Index(line, "https")])+2, ly)
	if !underlined(m.View().Content, "https://example.com/x") || !strings.Contains(view(m), "opening links is off") {
		t.Fatalf("a hovered link is underlined and its policy named:\n%s", view(m))
	}
	hx, hy := 0, 0
	for _, h := range m.hits {
		if h.area == hitTop && h.id == "channels" {
			hx, hy = h.x0, h.y
		}
	}
	move(m, hx+1, hy)
	if !underlined(m.View().Content, m.name) {
		t.Fatal("the channel target underlines its label")
	}

	// Overlays capture the pointer; resizing and pressing clear the hover.
	m.openHelp()
	m.View()
	move(m, x+8, y)
	if m.activeHover() != (hoverKey{}) {
		t.Fatal("background targets must not react while an overlay is open")
	}
	m.overlay = nil
	m.View()
	move(m, x+8, y)
	m.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	if m.hover != (hoverKey{}) {
		t.Fatal("a resize moves every target, so the hover is dropped")
	}
	m.View()
	move(m, x+18, y)
	m.Update(tea.MouseClickMsg{X: 2, Y: ly, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: 9, Y: ly, Button: tea.MouseLeft})
	if m.hover != (hoverKey{}) || m.sel == nil {
		t.Fatal("a drag selects text and shows no hover")
	}
}

// Each Settings row click target is the row that shows the setting, also
// below the selected row's wrapped hint.
func TestSettingsRowsMapToTheirSettings(t *testing.T) {
	m, _ := readyModel()
	m.openSettings()
	m.View()
	y, _ := lineWith(t, m, "Participants panel")
	for _, h := range m.hits {
		if h.area == hitOverlayRow && h.y == y && h.item != 2 {
			t.Fatalf("the Participants panel row targets setting %d", h.item)
		}
	}
	open := m.rosterOpen
	x, _ := hitFor(t, m, hitOverlayRow, 2)
	m.Update(tea.MouseClickMsg{X: x + 3, Y: y, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: x + 3, Y: y, Button: tea.MouseLeft})
	if m.rosterOpen == open {
		t.Fatal("clicking the Participants panel row must toggle it")
	}
}

func TestLongPasteCollapsesWithoutChangingThePayload(t *testing.T) {
	m, sends := readyModel()
	long := strings.Repeat("line\n", 7)
	paste(m, "before ")
	paste(m, long)
	paste(m, " after")
	want := "before " + long + " after"
	if m.Draft() != want || len(m.editor.folds) != 1 {
		t.Fatalf("draft %q folds %+v", m.Draft(), m.editor.folds)
	}
	if out := view(m); !strings.Contains(out, "before [Pasted #1 · 7 lines · 35 B] after") || strings.Contains(out, "❯ before line") {
		t.Fatalf("the paste shows as one chip:\n%s", out)
	}
	for range len(" after") {
		press(m, tea.KeyLeft, 0)
	}
	f := m.editor.folds[0]
	if m.editor.cursor != f.end {
		t.Fatalf("cursor %d, fold end %d", m.editor.cursor, f.end)
	}
	press(m, tea.KeyLeft, 0)
	if m.editor.cursor != f.start {
		t.Fatal("the chip moves as one unit")
	}
	press(m, tea.KeyRight, 0)
	press(m, tea.KeyBackspace, 0)
	if m.Draft() != "before  after" || len(m.editor.folds) != 0 {
		t.Fatalf("Backspace removes the whole paste: %q", m.Draft())
	}
	press(m, 'z', tea.ModCtrl)
	if m.Draft() != want || len(m.editor.folds) != 1 {
		t.Fatal("undo restores the paste and its chip")
	}

	// Expanding keeps the text and revision, and undo collapses it again.
	rev := m.editor.revision
	press(m, 'p', tea.ModAlt)
	if m.Draft() != want || len(m.editor.folds) != 0 || m.editor.revision != rev || !strings.Contains(view(m), "❯ before line") {
		t.Fatalf("Alt+P shows the text in place: folds %+v", m.editor.folds)
	}
	press(m, 'z', tea.ModCtrl)
	if m.Draft() != want || len(m.editor.folds) != 1 {
		t.Fatal("undo collapses an expanded paste")
	}

	// A click on the chip expands it too.
	m.View()
	x, y := hitFor(t, m, hitFold, 0)
	m.Update(tea.MouseClickMsg{X: x + 1, Y: y, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: x + 1, Y: y, Button: tea.MouseLeft})
	if len(m.editor.folds) != 0 {
		t.Fatal("clicking the chip expands it")
	}
	press(m, 'z', tea.ModCtrl)

	press(m, tea.KeyEnter, 0)
	if len(*sends) != 1 || (*sends)[0].Text != want {
		t.Fatalf("the full payload is sent once: %+v", *sends)
	}
	m.Update(DeliveryMsg{ID: (*sends)[0].ID, MessageID: "m1", State: Confirmed})
	if m.Draft() != "" || len(m.editor.folds) != 0 {
		t.Fatal("confirmation clears the draft and its chips")
	}
}

func TestCollapsedPasteEditingBoundaries(t *testing.T) {
	m, sends := readyModel()
	m.Update(MembersMsg{Members: []Member{{Nick: "sol"}}})
	// A long single line folds by size; a collapsed draft is never a
	// command, even when its text is one.
	paste(m, "/topic set "+strings.Repeat("x", 1100))
	press(m, tea.KeyEnter, 0)
	if len(*sends) != 1 || !strings.HasPrefix((*sends)[0].Text, "/topic set ") {
		t.Fatalf("a collapsed draft is sent as a message: %+v (%q)", *sends, m.notice)
	}
	m.Update(DeliveryMsg{ID: (*sends)[0].ID, MessageID: "t", State: Confirmed})

	// Mentions are not suggested from inside a paste.
	paste(m, strings.Repeat("row\n", 6)+"ping @so")
	if m.suggest != nil {
		t.Fatal("text inside a collapsed paste must not open suggestions")
	}
	_ = m.SetDraft("")

	// Home/End skip newlines inside the paste; a combining mark typed after
	// the chip keeps the chip and its own cursor stop.
	paste(m, "a")
	paste(m, strings.Repeat("row\n", 6))
	paste(m, "b")
	press(m, tea.KeyHome, 0)
	if m.editor.cursor != 0 {
		t.Fatalf("Home goes to the draft line start, not into the paste: %d", m.editor.cursor)
	}
	press(m, tea.KeyEnd, 0)
	if m.editor.cursor != len(m.Draft()) {
		t.Fatal("End goes past the paste")
	}
	press(m, tea.KeyLeft, 0)
	m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "\u0301"}))
	if len(m.editor.folds) != 1 {
		t.Fatal("a combining mark after the chip keeps it")
	}
	press(m, tea.KeyLeft, 0)
	if m.editor.cursor != m.editor.folds[0].end {
		t.Fatal("the chip end stays a cursor stop")
	}

	// Vertical movement and clicks never land inside the paste.
	paste(m, "\nzz")
	press(m, tea.KeyUp, 0)
	if _, inside := m.editor.foldAt(m.editor.cursor); inside {
		t.Fatal("Up moved the cursor into the paste")
	}

	// Labels shrink to fit; oversized pastes are still refused whole.
	f := fold{start: 0, end: 35, n: 2}
	text := strings.Repeat("line\n", 7)
	for width, want := range map[int]string{40: "[Pasted #2 · 7 lines · 35 B]", 22: "[Pasted #2 · 7 lines]", 12: "[Pasted #2]", 5: "[…]"} {
		if got := chipLabel(ansi.WcWidth, f, text, width); got != want {
			t.Errorf("chipLabel at %d = %q, want %q", width, got, want)
		}
	}
	_ = m.SetDraft("")
	paste(m, strings.Repeat("y", DefaultMessageBytes+1))
	if m.Draft() != "" || len(m.editor.folds) != 0 || m.noticeLevel != levelError {
		t.Fatal("an oversized paste is refused atomically")
	}
	paste(m, strings.Repeat("one\n", 6))
	paste(m, strings.Repeat("two\n", 6))
	if len(m.editor.folds) != 2 || m.editor.folds[1].n != 2 || !strings.Contains(view(m), "[Pasted #2") {
		t.Fatalf("pastes are numbered: %+v", m.editor.folds)
	}
}

func TestJoinWaitsForTheServerAndRecoversFromRefusal(t *testing.T) {
	m, sends, actions := channelModel(t)
	paste(m, "/join nope")
	press(m, tea.KeyEnter, 0)
	id := (*actions)[0].ID
	m.Update(ControlMsg{ID: id, Channel: "#nope", Kind: "join", State: "refused", Code: CodeUnknownChannel, Err: errors.New("invalid managed channel/action identity")})
	if !strings.Contains(m.notice, "yoloctl chat channel create nope, then /join #nope") || m.views["#nope"] != nil || m.name != "#space" || m.pending != nil {
		t.Fatalf("an unknown channel gets a create-first refusal and no view: %q", m.notice)
	}
	paste(m, "still here")
	press(m, tea.KeyEnter, 0)
	if len(*sends) != 1 {
		t.Fatal("a refused join leaves the current channel usable")
	}
	m.Update(DeliveryMsg{ID: (*sends)[0].ID, MessageID: "s", State: Confirmed})

	// The stream reported joining and then refused (left): the view this
	// attempt created goes away, with no departure notice.
	paste(m, "/join ops")
	press(m, tea.KeyEnter, 0)
	id = (*actions)[1].ID
	m.Update(MembershipMsg{Channel: "#ops", State: "joining", ID: id})
	m.Update(MembershipMsg{Channel: "#ops", State: "left", ID: id})
	m.Update(ControlMsg{ID: id, Channel: "#ops", Kind: "join", State: "refused", Code: CodeACLDenied})
	if m.views["#ops"] != nil || !strings.Contains(m.notice, "does not give you access to #ops") {
		t.Fatalf("an ACL refusal names the access problem and leaves no view: %q", m.notice)
	}

	// An existing view keeps its draft and history after a refusal.
	alt(m, '2')
	paste(m, "keep")
	m.Update(MembershipMsg{Channel: "#build", State: "left"})
	alt(m, '1')
	paste(m, "/join build")
	press(m, tea.KeyEnter, 0)
	id = (*actions)[2].ID
	m.Update(ControlMsg{ID: id, Channel: "#build", Kind: "join", State: "refused", Err: errors.New("AIRC refused the channel action (474)")})
	if v := m.views["#build"]; v == nil || v.editor.text != "keep" || !strings.Contains(m.notice, "(474)") {
		t.Fatalf("an existing channel keeps its draft: %q", m.notice)
	}

	// Uncertain leaves membership unknown and blocks; a user who moved on is
	// not pulled back by a late confirmation.
	paste(m, "/join extra")
	press(m, tea.KeyEnter, 0)
	id = (*actions)[3].ID
	m.Update(MembershipMsg{Channel: "#extra", State: "joining", ID: id})
	m.Update(ControlMsg{ID: id, Channel: "#extra", Kind: "join", State: "uncertain"})
	if v := m.views["#extra"]; v == nil || v.membership != "unknown" || m.pending == nil || !m.pending.uncertain {
		t.Fatal("an uncertain join is unknown, not joining")
	}
	m.Update(ConnectionMsg{})
	m.Update(ConnectionMsg{Connected: true})
	// Reconnect reports a joined navigation target; selecting an unjoined
	// one would correctly request another JOIN instead of switching.
	m.Update(MembershipMsg{Channel: "#build", State: "joined"})
	paste(m, "/join late")
	press(m, tea.KeyEnter, 0)
	id = (*actions)[4].ID
	alt(m, '2')
	m.Update(MembershipMsg{Channel: "#late", State: "joined", ID: id})
	m.Update(ControlMsg{ID: id, Channel: "#late", Kind: "join", State: "confirmed"})
	if m.name != "#build" || m.views["#late"].membership != "joined" {
		t.Fatalf("a late confirmation must not move a user who switched: %s", m.name)
	}
}

// Overlay rows and buttons sit above the panel's own click target, so they
// can be clicked and hovered.
func TestOverlayControlsReceiveClicksAndHover(t *testing.T) {
	m, _ := readyModel()
	m.opts.Identities = map[string]Identity{"mqa": {Name: "qa", Human: true}}
	m.Update(MembersMsg{Members: []Member{{Nick: "qa", Account: "mqa"}}})
	m.openSettings()
	m.View()
	x, y := hitFor(t, m, hitOverlayRow, 3)
	move(m, x+3, y)
	if raw := m.View().Content; !strings.Contains(raw, "\x1b[4m") || m.activeHover().item != 3 {
		t.Fatal("a hovered Settings row is underlined")
	}
	m.openCard("qa", "mqa")
	m.View()
	bx, by := hitFor(t, m, hitOverlayButton, len(m.cardActions())-1)
	move(m, bx+1, by)
	if !underlined(m.View().Content, "Close") || underlined(m.View().Content, "Mention @qa") {
		t.Fatalf("a hovered panel button underlines its label:\n%s", view(m))
	}
	m.Update(tea.MouseClickMsg{X: bx + 1, Y: by, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: bx + 1, Y: by, Button: tea.MouseLeft})
	if m.overlay != nil {
		t.Fatal("clicking a card's Close button closes it")
	}
}

// A paste starting with a combining mark keeps the merged grapheme as text and
// still collapses the rest (QA phase-1 finding 1).
func TestPasteStartingWithACombiningMarkStillCollapses(t *testing.T) {
	m, _ := readyModel()
	paste(m, "e")
	paste(m, "\u0301"+strings.Repeat("x", foldBytes))
	if len(m.editor.folds) != 1 || m.editor.folds[0].start != len("e\u0301") {
		t.Fatalf("the chip starts after the merged grapheme: %+v", m.editor.folds)
	}
	if out := view(m); !strings.Contains(out, "e\u0301[Pasted #1") || strings.Contains(out, "xxxx") {
		t.Fatalf("the paste renders as a chip:\n%s", out)
	}
	press(m, tea.KeyBackspace, 0)
	if m.Draft() != "e\u0301" {
		t.Fatalf("Backspace removes the collapsed part only: %q", m.Draft())
	}
}

// Undoing an expansion is not an edit: a pending send still clears on
// confirmation and cannot be sent twice (QA phase-1 finding 2).
func TestUndoingAnExpansionKeepsThePendingSendMatched(t *testing.T) {
	m, sends := readyModel()
	paste(m, strings.Repeat("row\n", foldLines))
	press(m, tea.KeyEnter, 0)
	press(m, 'p', tea.ModAlt)
	press(m, 'z', tea.ModCtrl)
	if len(m.editor.folds) != 1 {
		t.Fatal("undo collapses the paste again")
	}
	m.Update(DeliveryMsg{ID: (*sends)[0].ID, MessageID: "m", State: Confirmed})
	if m.Draft() != "" {
		t.Fatalf("the confirmed send must clear the unchanged draft: %q", m.Draft())
	}
	press(m, tea.KeyEnter, 0)
	if len(*sends) != 1 {
		t.Fatal("the confirmed text was sent twice")
	}
}
