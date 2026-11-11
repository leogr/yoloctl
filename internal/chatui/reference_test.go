package chatui

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	refTarget  = "0f3c2a4e-9b1d-4c8e-a7f6-1234567890ab"
	refMissing = "7d1e9f20-3c4b-4a5d-8e6f-abcdef012345"
	// Their shared references, as AIRC prints them.
	sharedTarget  = "m:DzwqTpsdTI6n9hI0VniQqw"
	sharedMissing = "m:fR6fIDxLSl2Ob6vN7wEjRQ"
)

// The codec agrees byte for byte with AIRC's: testdata/message-references.json
// is a copy of AIRC's tests/data/message-references.json.
func TestSharedReferenceVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/message-references.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		Valid []struct {
			Msgid, Reference string
		}
		Invalid, Uncompactable []string
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors.Valid) == 0 || len(vectors.Invalid) == 0 || len(vectors.Uncompactable) == 0 {
		t.Fatal("empty vector set")
	}
	for _, v := range vectors.Valid {
		if ref, ok := compactRef(v.Msgid); !ok || ref != v.Reference {
			t.Errorf("compactRef(%q) = %q, %v; want %q", v.Msgid, ref, ok, v.Reference)
		}
		if id, ok := refID(v.Reference); !ok || id != v.Msgid {
			t.Errorf("refID(%q) = %q, %v; want %q", v.Reference, id, ok, v.Msgid)
		}
	}
	for _, ref := range vectors.Invalid {
		if id, ok := refID(ref); ok {
			t.Errorf("refID(%q) accepted %q", ref, id)
		}
	}
	for _, id := range vectors.Uncompactable {
		if ref, ok := compactRef(id); ok {
			t.Errorf("compactRef(%q) = %q", id, ref)
		}
	}
	// Each of the final character's unused-bit patterns is refused; only
	// its two data bits vary, each naming a different ID.
	var accepted string
	for c := 0; c < 256; c++ {
		if nickByte(byte(c)) {
			if _, ok := refID(sharedTarget[:len(sharedTarget)-1] + string(rune(c))); ok {
				accepted += string(rune(c))
			}
		}
	}
	if accepted != "AQgw" {
		t.Errorf("accepted final characters %q, want AQgw", accepted)
	}
}

// targets lists the click targets of text as displayed: each target's
// shown text with its action, adjacent pieces of one target joined.
func targets(m *Model, text string, source bool) [][2]string {
	var out [][2]string
	var last action
	for _, dl := range displayLines(ansi.WcWidth, text, source, m.theme, m.opts.Nick) {
		last = action{}
		for _, p := range dl.Pieces {
			if p.Act.Kind == actNone {
				last = action{}
				continue
			}
			kind := map[actionKind]string{actLink: "link", actMention: "mention", actRef: "ref"}[p.Act.Kind] + " " + p.Act.Value
			if p.Act == last {
				out[len(out)-1][1] += dl.Text[p.Start:p.End]
				continue
			}
			out, last = append(out, [2]string{kind, dl.Text[p.Start:p.End]}), p.Act
		}
	}
	return out
}

// A shared reference is m: and 22 base64url characters naming a server ID,
// or msgid: and a full lowercase server ID, standing alone in prose.
// Anything partial, glued, padded, aliased, quoted as code, in source mode
// or already a link target stays plain text. An all-digit @N is plain text
// too: neither a mention nor anything that can be followed.
func TestMessageReferenceTokens(t *testing.T) {
	m, _ := readyModel()
	ref := "msgid:" + refTarget
	shared := sharedTarget
	for _, c := range []struct {
		text   string
		source bool
		want   [][2]string
	}{
		{"see " + shared + " please", false, [][2]string{{"ref " + refTarget, shared}}},
		{"(" + shared + ").", false, [][2]string{{"ref " + refTarget, shared}}},
		{"- " + shared + ", then **" + sharedMissing + "**:", false, [][2]string{{"ref " + refTarget, shared}, {"ref " + refMissing, sharedMissing}}},
		{shared + "." + sharedMissing, false, [][2]string{{"ref " + refTarget, shared}, {"ref " + refMissing, sharedMissing}}},
		{shared + " " + ref, false, [][2]string{{"ref " + refTarget, shared}, {"ref " + refTarget, ref}}},
		{shared[:len(shared)-1], false, nil},
		{shared[:len(shared)-1] + " x", false, nil},
		{shared + "A", false, nil},
		{shared + "-", false, nil},
		{shared + "=", false, nil},
		{"x" + shared, false, nil},
		{"M" + shared[1:], false, nil},
		{shared[:len(shared)-1] + "x", false, nil},
		{"m:DzwqTpsdTI6n9hI0Vni+qw", false, nil},
		{"`" + shared + "`", false, nil},
		{"```\n" + shared + "\n```", false, nil},
		{shared, true, nil},
		{"https://example.org/" + shared, false, [][2]string{{"link https://example.org/" + shared, "https://example.org/" + shared}}},
		{"[" + shared + "](https://example.org/x)", false, [][2]string{{"link https://example.org/x", shared + " (https://example.org/x)"}}},
		{"see " + ref + " please", false, [][2]string{{"ref " + refTarget, ref}}},
		{"(" + ref + ").", false, [][2]string{{"ref " + refTarget, ref}}},
		{"- " + ref + ", then **" + ref + "**", false, [][2]string{{"ref " + refTarget, ref}, {"ref " + refTarget, ref}}},
		{"msgid:" + strings.ToUpper(refTarget), false, nil},
		{ref[:len(ref)-1], false, nil},
		{"x" + ref, false, nil},
		{ref + "-x", false, nil},
		{ref + "a", false, nil},
		{"msgid:{" + refTarget + "}", false, nil},
		{"`" + ref + "`", false, nil},
		{"```\n" + ref + "\n```", false, nil},
		{ref + " @289 @opus", true, nil},
		{"https://example.org/" + ref, false, [][2]string{{"link https://example.org/" + ref, "https://example.org/" + ref}}},
		{"[" + ref + "](https://example.org/x)", false, [][2]string{{"link https://example.org/x", ref + " (https://example.org/x)"}}},
		{"My @289 overstated it; @opus and @human, see @12b", false, [][2]string{{"mention opus", "@opus"}, {"mention human", "@human"}, {"mention 12b", "@12b"}}},
		{"mail me@289 or @_9", false, [][2]string{{"mention _9", "@_9"}}},
	} {
		if got := targets(m, c.text, c.source); !equalTargets(got, c.want) {
			t.Errorf("%q (source %v): %q, want %q", c.text, c.source, got, c.want)
		}
	}
	// A number is styled as plain text, never as a mention or one of me.
	for _, dl := range displayLines(ansi.WcWidth, "@289 @human", false, m.theme, "human") {
		for _, p := range dl.Pieces {
			switch p.Act.Kind {
			case actNone:
				if p.Start < 4 && (p.Style.Bold || p.Style.Underline || p.Style.FG == m.theme.Dim) {
					t.Errorf("a number is styled: %+v", p.Style)
				}
			case actMention:
				if fg, bg := mentionColors(m.theme); p.Style.FG != fg || p.Style.BG != bg || !p.Style.Bold {
					t.Errorf("self-mention lost its marker: %+v", p.Style)
				}
			}
		}
	}
}

func equalTargets(a, b [][2]string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// targetCells lists, one per transcript row from the last frame, the first
// cell of a run with this kind of action and, if value is not empty, that
// value.
func targetCells(m *Model, kind actionKind, value string) [][2]int {
	var out [][2]int
	for i, row := range m.lastTranscript {
		x := 0
		for _, r := range row.runs {
			if r.Act.Kind == kind && (value == "" || r.Act.Value == value) {
				out = append(out, [2]int{x, i - m.scroll + m.layout.bodyTop})
				break
			}
			x += displayWidth(ansi.WcWidth, r.Text)
		}
	}
	return out
}

func clickCell(m *Model, c [2]int) {
	m.Update(tea.MouseClickMsg{X: c[0], Y: c[1], Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: c[0], Y: c[1], Button: tea.MouseLeft})
}

// A click on a reference selects exactly the loaded message with that ID,
// in either spelling, also from a wrapped row. A missing target or a drag
// leaves the position alone; a number such as @289 is inert text.
func TestMessageReferencesNavigateToLoadedMessages(t *testing.T) {
	m, _ := readyModel()
	// Narrower than one reference, so it wraps over two rows.
	m.Update(tea.WindowSizeMsg{Width: 44, Height: 40})
	m.Update(IncomingMsg{Message: Message{ID: refTarget, Nick: "opus", Text: "the original claim"}})
	for _, text := range []string{"filler one", "filler two"} {
		m.Update(IncomingMsg{Message: Message{ID: "f-" + text, Nick: "x", Text: text}})
	}
	m.Update(IncomingMsg{Message: Message{ID: "r1", Nick: "opus", Text: "My @289 overstated it; see msgid:" + refTarget + " and " + sharedMissing + " or " + sharedTarget}})
	m.View()
	refs := targetCells(m, actRef, refTarget)
	if len(refs) < 2 || len(targetCells(m, actRef, refMissing)) == 0 {
		t.Fatalf("all references, the first wrapped over two rows, expected: %v", refs)
	}
	m.Update(tea.MouseMotionMsg{X: refs[0][0], Y: refs[0][1]})
	if out := view(m); !strings.Contains(out, "Reference  click to show it") {
		t.Fatalf("hovering a reference lacks its hint:\n%s", out)
	}
	// A number such as @289 offers nothing: no hint, no action, no notice.
	number := [2]int{-1, -1}
	for i, row := range m.lastTranscript {
		x := 0
		for _, r := range row.runs {
			if at := strings.Index(r.Text, "@289"); at >= 0 && number[0] < 0 {
				if r.Act.Kind != actNone {
					t.Fatalf("@289 has an action: %+v", r.Act)
				}
				number = [2]int{x + displayWidth(ansi.WcWidth, r.Text[:at]), i - m.scroll + m.layout.bodyTop}
			}
			x += displayWidth(ansi.WcWidth, r.Text)
		}
	}
	if number[0] < 0 {
		t.Fatal("@289 is not shown")
	}
	m.Update(tea.MouseMotionMsg{X: number[0], Y: number[1]})
	if out := view(m); strings.Contains(out, "@289  ") || strings.Contains(out, "own client") {
		t.Fatalf("hovering a number advertises it:\n%s", out)
	}
	m.notice = ""
	clickCell(m, number)
	if m.overlay != nil || strings.Contains(m.notice, "289") {
		t.Fatalf("a number acted: overlay=%v notice=%q", m.overlay, m.notice)
	}
	m.picked, m.selected, m.follow, m.anchor = false, 0, true, nil
	// A drag from a reference selects text and never jumps: it selects the
	// message it starts in (as any drag does), never the referenced target.
	drag(m, refs[0][0], refs[0][1], refs[0][0]+4, refs[0][1])
	target := m.indexOf("server:" + refTarget)
	if m.selected == target || !m.follow || m.anchor != nil && m.anchor.key == "server:"+refTarget {
		t.Fatal("a drag from a reference navigated")
	}
	// Every row of each wrapped reference jumps to the target.
	for _, c := range refs {
		m.picked, m.selected, m.follow, m.anchor = false, 0, true, nil
		m.View()
		clickCell(m, c)
		if !m.picked || m.selected != 0 || m.follow || m.anchor == nil || m.anchor.key != "server:"+refTarget || m.focus != focusMessages {
			t.Fatalf("click at %v: picked=%v selected=%d follow=%v anchor=%+v", c, m.picked, m.selected, m.follow, m.anchor)
		}
	}
	// A target that is not loaded keeps the selection and position.
	m.View()
	missing := targetCells(m, actRef, refMissing)
	if len(missing) == 0 {
		t.Fatal("the missing reference is no longer shown")
	}
	scroll := m.scroll
	clickCell(m, missing[0])
	m.View()
	if !m.picked || m.selected != 0 || m.follow || m.scroll != scroll || !strings.Contains(m.notice, "not loaded in any open channel; Page Up") {
		t.Fatalf("a missing target moved the view: selected=%d scroll=%d/%d notice=%q", m.selected, m.scroll, scroll, m.notice)
	}
}

// Reply labels share the same exact, loaded-only navigation.
func TestReplyLabelsStillNavigate(t *testing.T) {
	m, _ := readyModel()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m.Update(IncomingMsg{Message: Message{ID: refTarget, Nick: "opus", Text: "original"}})
	m.Update(IncomingMsg{Message: Message{ID: "r1", Nick: "x", Text: "answer", ReplyTo: refTarget}})
	m.Update(IncomingMsg{Message: Message{ID: "r2", Nick: "x", Text: "answer to the unloaded", ReplyTo: refMissing}})
	m.View()
	replies := targetCells(m, actReply, "")
	if len(replies) != 2 {
		t.Fatalf("reply labels: %v", replies)
	}
	clickCell(m, replies[1])
	if m.picked || !strings.Contains(m.notice, "not loaded in any open channel") {
		t.Fatalf("an unloaded reply target: picked=%v notice=%q", m.picked, m.notice)
	}
	clickCell(m, replies[0])
	if !m.picked || m.selected != 0 || m.follow {
		t.Fatalf("reply label: picked=%v selected=%d follow=%v", m.picked, m.selected, m.follow)
	}
}

// Copy reference writes the confirmed server ID's shared m: reference, only
// through the explicit action and the clipboard policy. Messages without such
// an ID offer no reference.
func TestCopyReference(t *testing.T) {
	m, copied := copyingModel(80, 24)
	m.Update(IncomingMsg{Message: Message{ID: refTarget, Nick: "opus", Text: "cite me"}})
	has := func() bool {
		for _, a := range m.messageActions() {
			if a.id == "reference" {
				return a.label == "Copy reference"
			}
		}
		return false
	}
	press(m, tea.KeyUp, tea.ModAlt)
	if !has() || len(*copied) != 0 {
		t.Fatalf("a confirmed message offers Copy reference, and selecting copies nothing: %v", *copied)
	}
	m.runAction("reference")
	if len(*copied) != 1 || (*copied)[0] != sharedTarget || !strings.Contains(m.notice, "Copied a reference to opus's message") {
		t.Fatalf("copied %q, notice %q", *copied, m.notice)
	}
	// The copied text is itself a reference to that message.
	if got := targets(m, (*copied)[0], false); !equalTargets(got, [][2]string{{"ref " + refTarget, sharedTarget}}) {
		t.Fatalf("copied reference does not resolve: %q", got)
	}
	m.opts.Clipboard = "off"
	m.runAction("reference")
	if len(*copied) != 1 || !strings.Contains(m.notice, "clipboard is off") {
		t.Fatalf("clipboard off copied: %v %q", *copied, m.notice)
	}
	// A pending message and a non-server ID have no reference to copy.
	m.opts.Clipboard = ""
	m.Update(IncomingMsg{Message: Message{ID: "legacy-id", Nick: "x", Text: "old"}})
	paste(m, "mine, still pending")
	press(m, tea.KeyEnter, 0)
	for i, want := range []string{"legacy-id", ""} {
		m.selected, m.picked = len(m.messages)-2+i, true
		if msg := m.selectedMessage(); msg == nil || msg.ID != want || has() {
			t.Fatalf("message %d (%+v) offers Copy reference", i, msg)
		}
		m.runAction("reference")
		if len(*copied) != 1 || !strings.Contains(m.notice, "Not confirmed yet") {
			t.Fatalf("message %d: copied %v, notice %q", i, *copied, m.notice)
		}
	}
}

// A reference to a message loaded in another open channel previews it with
// that channel's name and, clicked, switches there the way selecting the
// channel does and selects it, requesting no history and sending nothing.
// A deleted channel's kept history still navigates without joining.
func TestReferencesReachOtherOpenChannels(t *testing.T) {
	m, sends, actions := channelModel(t)
	m.opts.LoadOlder = func(HistoryRequest) tea.Cmd { t.Fatal("a reference requested history"); return nil }
	m.Update(IncomingMsg{Message: Message{ID: refTarget, Channel: "#space", Nick: "x", Text: "space message"}})
	alt(m, '2')
	m.Update(IncomingMsg{Message: Message{ID: refMissing, Channel: "#build", Nick: "x", Text: "build message"}})
	m.Update(IncomingMsg{Message: Message{ID: "b2", Channel: "#build", Nick: "x", Text: sharedTarget + " and msgid:" + refMissing}})
	m.View()
	if !strings.Contains(view(m), "↗ #space · x: space message") {
		t.Fatalf("no cross-channel preview:\n%s", view(m))
	}
	if len(targetCells(m, actRef, refTarget)) == 0 || len(targetCells(m, actRef, refMissing)) == 0 {
		t.Fatalf("references: %v", targetCells(m, actRef, ""))
	}
	clickCell(m, targetCells(m, actRef, refTarget)[0])
	if m.name != "#space" || !m.picked || m.messages[m.selected].ID != refTarget || len(*sends) != 0 || len(*actions) != 0 {
		t.Fatalf("cross-channel click: %s picked=%v sends=%v actions=%v %q", m.name, m.picked, *sends, *actions, m.notice)
	}
	alt(m, '2')
	m.Update(MembershipMsg{Channel: "#build", State: "deleted"})
	m.View()
	clickCell(m, targetCells(m, actRef, refMissing)[0])
	if m.name != "#build" || !m.picked || m.messages[m.selected].ID != refMissing || len(*sends) != 0 || len(*actions) != 0 || !m.views["#build"].deleted {
		t.Fatalf("deleted view: %s picked=%v sends=%v actions=%v", m.name, m.picked, *sends, *actions)
	}
}

// When the referenced message's channel was left, the click uses the
// ordinary typed join of a deliberate selection, and shows the message once
// that join switches to the channel. A preview appears as soon as another
// channel loads the message, without touching the visible view.
func TestCrossChannelReferenceJoinsLeftChannel(t *testing.T) {
	m, sends, actions := channelModel(t)
	m.Update(IncomingMsg{Message: Message{ID: refMissing, Channel: "#build", Nick: "x", Text: "build message"}})
	m.Update(MembershipMsg{Channel: "#build", State: "left"})
	m.Update(IncomingMsg{Message: Message{ID: "s2", Channel: "#space", Nick: "x", Text: "see msgid:" + refMissing + " and msgid:" + refTarget}})
	m.View()
	if !strings.Contains(view(m), "↗ #build · x: build message") || strings.Contains(view(m), "↗ #build · x: later") {
		t.Fatalf("preview of a left channel's loaded message:\n%s", view(m))
	}
	// Loaded later in another channel: the visible view picks it up.
	m.Update(IncomingMsg{Message: Message{ID: refTarget, Channel: "#build", Nick: "x", Text: "later"}})
	if out := view(m); strings.Count(out, "↗ #build · x:") != 2 || !strings.Contains(out, "later") {
		t.Fatalf("a message loaded elsewhere later is not previewed:\n%s", view(m))
	}
	clickCell(m, targetCells(m, actRef, refMissing)[0])
	if m.name != "#space" || len(*actions) != 1 || (*actions)[0].Kind != "join" || (*actions)[0].Channel != "#build" || len(*sends) != 0 {
		t.Fatalf("left channel: %s actions=%v sends=%v", m.name, *actions, *sends)
	}
	a := (*actions)[0]
	m.Update(MembershipMsg{Channel: "#build", State: "joined", ID: a.ID})
	m.Update(ControlMsg{ID: a.ID, Kind: "join", Channel: a.Channel, State: "confirmed"})
	if m.name != "#build" || !m.picked || m.messages[m.selected].ID != refMissing {
		t.Fatalf("after the join: %s picked=%v %q", m.name, m.picked, m.notice)
	}
}

// A reference click that needs a join shows its message only through that
// join's own switch. A refused or uncertain join, a part, a move elsewhere
// before confirmation, a join that never started, or a new stream drops it
// with the join intent: a later ordinary selection of the channel keeps its
// reading position. (Cases after astra's review probe.)
func TestCrossChannelReferenceEndsWithItsJoin(t *testing.T) {
	for _, why := range []string{"refused", "uncertain", "part", "moved", "not-started", "new-stream"} {
		t.Run(why, func(t *testing.T) {
			m, _, actions := channelModel(t)
			m.Update(IncomingMsg{Message: Message{ID: refTarget, Channel: "#build", Nick: "x", Text: "old target"}})
			m.Update(IncomingMsg{Message: Message{ID: refMissing, Channel: "#build", Nick: "x", Text: "newer message"}})
			m.Update(MembershipMsg{Channel: "#build", State: "left"})
			if why == "not-started" {
				m.connected = false
			}
			m.showMessage(refTarget)
			switch why {
			case "refused":
				a := (*actions)[0]
				m.Update(ControlMsg{ID: a.ID, Kind: "join", Channel: a.Channel, State: "refused", Code: CodeACLDenied})
			case "uncertain":
				a := (*actions)[0]
				m.Update(ControlMsg{ID: a.ID, Kind: "join", Channel: a.Channel, State: "uncertain"})
				m.pending = nil
			case "part":
				a := (*actions)[0]
				m.Update(ControlMsg{ID: a.ID, Kind: "join", Channel: a.Channel, State: "confirmed"})
				m.control("part", "#build", nil)
			case "moved":
				m.view("#third")
				m.switchTo("#third")
				a := (*actions)[0]
				m.Update(MembershipMsg{Channel: "#build", State: "joined", ID: a.ID})
				m.Update(ControlMsg{ID: a.ID, Kind: "join", Channel: a.Channel, State: "confirmed"})
			case "new-stream":
				m.Update(ConnectionMsg{Connected: false})
			}
			if m.joinSwitch.ref != "" {
				t.Fatalf("the reference outlived its join: %+v", m.joinSwitch)
			}
			m.connected = true
			m.views["#build"].membership = "joined"
			m.selectChannel("#build")
			if m.name != "#build" || m.picked || !m.follow {
				t.Fatalf("stale reference moved a later ordinary selection: %s picked=%v follow=%v selected=%d", m.name, m.picked, m.follow, m.selected)
			}
		})
	}
}
