package chatui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// clickAt is one left click (press and release) on a cell; it returns the
// command the release produced.
func clickAt(m *Model, x, y int) tea.Cmd {
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	_, cmd := m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	return cmd
}

func hitWith(m *Model, area hitArea, id string) (hit, bool) {
	for _, h := range m.hits {
		if h.area == area && h.id == id {
			return h, true
		}
	}
	return hit{}, false
}

// frameLines renders and checks that the frame fits the window exactly.
func frameLines(t *testing.T, m *Model) []string {
	t.Helper()
	lines := strings.Split(view(m), "\n")
	if len(lines) != m.height {
		t.Fatalf("frame has %d rows for a height of %d:\n%s", len(lines), m.height, strings.Join(lines, "\n"))
	}
	for i, line := range lines {
		if w := ansi.StringWidthWc(line); w != m.width {
			t.Fatalf("row %d is %d cells for a width of %d: %q", i, w, m.width, line)
		}
	}
	return lines
}

func uiModel(t *testing.T) (*Model, *[]string) {
	t.Helper()
	m := presenceModel(t)
	var copied []string
	m.opts.Clipboard = "on"
	m.opts.Copy = func(text string) tea.Cmd { copied = append(copied, text); return nil }
	at := time.Date(2026, 10, 22, 10, 0, 0, 0, time.Local)
	m.Update(IncomingMsg{Message: Message{ID: "a", Account: "mopus", Nick: "opus", Text: "first from opus", Time: at}})
	m.Update(IncomingMsg{Message: Message{ID: "b", Account: "mfable", Nick: "fable", Text: "second from fable", Time: at.Add(time.Minute)}})
	m.View()
	return m, &copied
}

func pickMessage(t *testing.T, m *Model, id string) {
	t.Helper()
	m.View()
	x, y := cellOf(t, m, "server:"+id, 0, 0)
	clickAt(m, x, y)
	if !m.picked || m.selectedMessage() == nil || m.selectedMessage().ID != id {
		t.Fatalf("clicking message %s did not pick it", id)
	}
}

// The selected message's actions stay in the footer, act on that message
// and survive a Participants click, composer focus, typing and arrivals; a
// warning keeps its own row above them.
func TestSelectedActionsFollowTheSelectionNotTheComposer(t *testing.T) {
	m, copied := uiModel(t)
	var resolved []string
	m.opts.ResolveAttach = func(account string) tea.Cmd {
		resolved = append(resolved, account)
		return func() tea.Msg { return nil }
	}
	m.View()
	panel, ok := hitWith(m, hitPanel, memberKey(Member{Nick: "opus", Account: "mopus"}))
	if !ok {
		t.Fatal("no participant target")
	}
	clickAt(m, panel.x0+3, panel.y)
	if len(resolved) != 1 || !strings.Contains(m.notice, "Checking opus") {
		t.Fatalf("participant click: %v %q", resolved, m.notice)
	}
	pickMessage(t, m, "a")
	lines := frameLines(t, m)
	footer := lines[len(lines)-1]
	if m.notice != "" || !strings.Contains(footer, "Reply") || !strings.Contains(footer, "Copy") || !strings.Contains(footer, "@opus") {
		t.Fatalf("an explicit pick shows its actions and drops the stale notice %q:\n%s", m.notice, footer)
	}
	// Composer focus, empty and with drafts, keeps the actions and target.
	for _, draft := range []string{"", "typed", "line one\nline two"} {
		m.View()
		composer, ok := hitWith(m, hitComposer, "")
		if !ok {
			t.Fatal("no composer target")
		}
		clickAt(m, composer.x0+6, composer.y)
		if draft != "" {
			paste(m, draft)
		}
		m.Update(IncomingMsg{Message: Message{ID: "late-" + strings.Fields(draft + " x")[0], Account: "mfable", Nick: "fable", Text: "arrives"}})
		lines = frameLines(t, m)
		if m.focus != focusComposer || !strings.Contains(lines[len(lines)-1], "Copy") || m.selectedMessage().ID != "a" {
			t.Fatalf("draft %q: focus=%v target=%q footer %q", draft, m.focus, m.selectedMessage().ID, lines[len(lines)-1])
		}
		copyChip, ok := hitWith(m, hitFooter, "copy")
		if !ok {
			t.Fatal("no copy chip")
		}
		clickAt(m, copyChip.x0+1, copyChip.y)
		if got := (*copied)[len(*copied)-1]; got != "first from opus" {
			t.Fatalf("draft %q: the chip copied %q", draft, got)
		}
		if !strings.HasPrefix(m.Draft(), draft) {
			t.Fatalf("an action changed the draft: %q", m.Draft())
		}
		press(m, 'u', tea.ModCtrl)
		press(m, 'u', tea.ModCtrl)
		press(m, 'k', tea.ModCtrl)
	}
	// A disconnect error coexists: its own row above the actions.
	m.Update(ConnectionMsg{Err: errors.New("stream ended")})
	lines = frameLines(t, m)
	status, footer := lines[len(lines)-2], lines[len(lines)-1]
	if !strings.Contains(status, "Disconnected") || !strings.Contains(footer, "Copy") {
		t.Fatalf("error and actions:\n%s\n%s", status, footer)
	}
	if blank := lines[len(lines)-3]; strings.TrimSpace(strings.TrimLeft(blank, "▌")) != "" {
		t.Fatalf("the composer's bottom row is not blank: %q", blank)
	}
	// Ending the selection brings back the ordinary footer and the error.
	press(m, tea.KeyEscape, 0) // clears the notice with the jump or selection
	for m.picked {
		press(m, tea.KeyEscape, 0)
	}
	lines = frameLines(t, m)
	if strings.Contains(lines[len(lines)-1], "Copy") {
		t.Fatalf("actions without a selection: %q", lines[len(lines)-1])
	}
	m.Update(ConnectionMsg{Err: errors.New("again")})
	lines = frameLines(t, m)
	if !strings.Contains(lines[len(lines)-1], "Disconnected") {
		t.Fatalf("no selection: the notice owns the footer: %q", lines[len(lines)-1])
	}
}

// Arrivals never retarget a picked message, and an evicted target ends the
// selection instead of moving its actions to a neighbour.
func TestPickedTargetSurvivesArrivalsAndEndsOnEviction(t *testing.T) {
	m, _ := readyModel()
	for i := range 3 {
		m.Update(IncomingMsg{Message: Message{ID: string(rune('a' + i)), Nick: "x", Text: "m"}})
	}
	m.View()
	pickMessage(t, m, "b")
	m.Update(IncomingMsg{Message: Message{ID: "d", Nick: "x", Text: "new"}})
	if m.selectedMessage().ID != "b" || !m.picked {
		t.Fatalf("an arrival retargeted the actions to %q", m.selectedMessage().ID)
	}
	m.evict(m.selected)
	if m.picked || m.focus != focusComposer {
		t.Fatal("the evicted target left its actions on a neighbour")
	}
	// Each channel keeps its own selection.
	m.Update(CatalogMsg{Generation: 1, Default: "#space", Channels: []ChannelInfo{{Name: "#space"}, {Name: "#build"}}, Applied: true})
	pickMessage(t, m, "c")
	m.switchTo("#build")
	if m.actionsShown() {
		t.Fatal("another channel shows this channel's actions")
	}
	m.switchTo("#space")
	if !m.actionsShown() || m.selectedMessage().ID != "c" {
		t.Fatal("returning lost the channel's selection")
	}
}

// The jump is centered on the whole screen, with the panel open or closed
// and with uneven room and count widths, and shrinks before overlapping.
func TestJumpIsCenteredAndNeverOverflows(t *testing.T) {
	for _, tc := range []struct {
		width   int
		channel string
		draft   string
		panel   bool
	}{
		{80, "#space", "", true}, {81, "#space", "", true}, {80, "#space", "", false},
		{101, "#a-much-longer-channel-name", "draft text", true}, {60, "#space", strings.Repeat("x", 3000), false},
	} {
		m := scrolledModel(t)
		m.rosterOpen = tc.panel
		m.Update(tea.WindowSizeMsg{Width: tc.width, Height: 24})
		if tc.channel != "#space" {
			m.Update(CatalogMsg{Generation: 1, Default: "#space", Channels: []ChannelInfo{{Name: "#space"}, {Name: tc.channel}}, Applied: true})
			for i := range 60 {
				m.Update(IncomingMsg{Message: Message{ID: "x" + string(rune('a'+i%26)) + strings.Repeat("y", i/26), Channel: tc.channel, Nick: "x", Text: "line"}})
			}
			m.switchTo(tc.channel)
			m.View()
		}
		paste(m, tc.draft)
		press(m, tea.KeyPgUp, 0)
		h, row, ok := jumpHit(m)
		if !ok {
			t.Fatalf("%+v: no jump", tc)
		}
		label := strings.TrimSpace(ansi.Strip(row)[h.x0:])
		pw := h.x1 - h.x0
		if d := 2*h.x0 + pw - tc.width; d < -1 || d > 1 {
			t.Fatalf("%+v: jump [%d,%d) is not centered on %d cells", tc, h.x0, h.x1, tc.width)
		}
		if got := ansi.StringWidthWc(row); got != tc.width || label == "" {
			t.Fatalf("%+v: divider %d cells: %q", tc, got, row)
		}
		// The hit covers exactly the drawn label, which hover underlines.
		if cells := ansi.Cut(row, h.x0, h.x1); !strings.HasPrefix(cells, " ↓") || !strings.HasSuffix(cells, " ") {
			t.Fatalf("%+v: hit covers %q", tc, cells)
		}
		move(m, h.x0+2, h.y)
		if raw := m.View().Content; !strings.Contains(raw, "\x1b[1;4") {
			t.Fatalf("%+v: hovering the jump does not mark it", tc)
		}
	}
	// Too narrow to center: the jump gives way, the divider still fits.
	m := scrolledModel(t)
	m.Update(tea.WindowSizeMsg{Width: 20, Height: 12})
	paste(m, strings.Repeat("x", 3000))
	press(m, tea.KeyPgUp, 0)
	lines := frameLines(t, m)
	if _, _, ok := jumpHit(m); ok {
		for _, line := range lines {
			if strings.Contains(line, "Jump") {
				t.Fatalf("narrow jump overflowed: %q", line)
			}
		}
	}
}

// A click, Alt+End and Esc all return to the latest messages the same way,
// keeping the draft, reply, staged images and picked message; Esc first
// leaves message focus, then jumps, then cancels the reply.
func TestJumpByClickAltEndAndEscKeepsTheDraft(t *testing.T) {
	type outcome struct {
		follow       bool
		scroll       int
		unread       int
		draft, reply string
		picked       bool
		images       int
	}
	setup := func() (*Model, *imageHost) {
		m, _, h := imageModel(t)
		m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		for i := range 60 {
			m.Update(IncomingMsg{Message: Message{ID: "m" + string(rune('a'+i%26)) + strings.Repeat("z", i/26), Nick: "x", Text: "line"}})
		}
		m.View()
		paste(m, "draft ")
		pasteImg(t, m, h, "png")
		m.replyTo, m.replyRevision = "ma", m.replyRevision+1
		press(m, tea.KeyPgUp, 0)
		m.Update(IncomingMsg{Message: Message{ID: "fresh", Nick: "x", Text: "new"}})
		m.View()
		return m, h
	}
	result := func(m *Model) outcome {
		m.View()
		return outcome{m.follow, m.scroll, m.unread, m.Draft(), m.replyTo, m.picked, len(m.editor.folds)}
	}
	m, _ := setup()
	h, _, _ := jumpHit(m)
	clickAt(m, h.x0+1, h.y)
	clicked := result(m)
	if !clicked.follow || clicked.unread != 0 || clicked.reply != "ma" || clicked.images != 1 || !strings.HasPrefix(clicked.draft, "draft ") {
		t.Fatalf("click: %+v", clicked)
	}
	if hint := strings.Split(view(m), "\n"); strings.Contains(hint[len(hint)-1], "Jump to latest") {
		t.Fatal("the used jump is still named under the pointer")
	}
	m, _ = setup()
	press(m, tea.KeyEnd, tea.ModAlt)
	if got := result(m); got != clicked {
		t.Fatalf("Alt+End %+v, click %+v", got, clicked)
	}
	m, _ = setup()
	press(m, tea.KeyEscape, 0)
	if got := result(m); got != clicked {
		t.Fatalf("Esc %+v, click %+v", got, clicked)
	}
	press(m, tea.KeyEscape, 0)
	if m.replyTo != "" || !strings.HasPrefix(m.Draft(), "draft ") {
		t.Fatal("at the tail Esc cancels the reply and keeps the draft")
	}
	// Message focus: Esc leaves it first, then the next Esc jumps.
	m, _ = setup()
	press(m, tea.KeyUp, tea.ModAlt)
	if m.focus != focusMessages {
		t.Fatal("no message focus")
	}
	press(m, tea.KeyEscape, 0)
	if m.focus != focusComposer || m.picked || m.follow {
		t.Fatal("the first Esc must only leave the selection")
	}
	press(m, tea.KeyEscape, 0)
	if !m.follow || m.replyTo != "ma" {
		t.Fatal("the next Esc jumps without touching the reply")
	}
	// A picked message survives the jump.
	m, _ = setup()
	m.pick("server:fresh")
	m.View()
	composer, _ := hitWith(m, hitComposer, "")
	clickAt(m, composer.x0+6, composer.y)
	m.follow = false
	m.unread = 1
	press(m, tea.KeyEscape, 0)
	if !m.follow || !m.picked || m.selectedMessage().ID != "fresh" {
		t.Fatal("the jump dropped the picked message")
	}
	// Suggestions close before anything else.
	m, _ = setup()
	paste(m, " @")
	m.refreshSuggest()
	if m.suggest != nil {
		press(m, tea.KeyEscape, 0)
		if m.suggest != nil || m.follow {
			t.Fatal("Esc must close suggestions before jumping")
		}
	}
}

// The composer always ends with one blank raised row below the last
// visible draft line; the prompt is ❯, one cell wide.
func TestComposerKeepsOneBlankRowBelowTheDraft(t *testing.T) {
	if w := ansi.StringWidthWc(composerPrompt); w != 2 || ansi.StringWidthWc("❯") != 1 {
		t.Fatalf("prompt width %d", w)
	}
	for _, tc := range []struct {
		width, height int
		draft         string
		reply         bool
	}{
		{80, 24, "", false}, {80, 24, "one", false}, {80, 24, "one\ntwo\nthree", true},
		{80, 24, strings.Repeat("row\n", 14) + "last", false}, {60, 12, "a\nb", true}, {20, 7, "x", false}, {20, 7, "x\ny", true},
	} {
		m, _ := readyModel()
		m.Update(tea.WindowSizeMsg{Width: tc.width, Height: tc.height})
		m.Update(IncomingMsg{Message: Message{ID: "r", Nick: "x", Text: "to reply"}})
		paste(m, tc.draft)
		if tc.reply {
			m.replyTo = "r"
		}
		lines := frameLines(t, m)
		v := m.View()
		last := m.layout.inputTop + m.layout.inputRows - 1
		blank := lines[last+1]
		if !strings.HasPrefix(blank, "▌") || strings.TrimSpace(strings.TrimLeft(blank, "▌")) != "" || last+2 != len(lines)-1 {
			t.Fatalf("%+v: rows after the draft:\n%s", tc, strings.Join(lines[m.layout.inputTop:], "\n"))
		}
		if v.Cursor == nil || v.Cursor.Y < m.layout.inputTop || v.Cursor.Y > last {
			t.Fatalf("%+v: cursor %+v outside the draft rows", tc, v.Cursor)
		}
		if tc.draft == "one" && v.Cursor.X != composerIndent+3 {
			t.Fatalf("cursor column %d", v.Cursor.X)
		}
		// A click on the blank row focuses the composer without moving the cursor.
		cursor := m.editor.cursor
		clickAt(m, composerIndent+1, last+1)
		if m.editor.cursor != cursor || m.focus != focusComposer {
			t.Fatalf("%+v: the blank row moved the cursor", tc)
		}
	}
	// At the minimum size, replying with a selection and an error, every
	// row still fits: the transcript gives way first.
	m, _ := readyModel()
	m.Update(tea.WindowSizeMsg{Width: 20, Height: 7})
	m.Update(IncomingMsg{Message: Message{ID: "r", Nick: "x", Text: "to reply"}})
	m.selectMessage(0)
	m.replyTo = "r"
	m.say(levelError, "disconnected")
	lines := frameLines(t, m)
	if m.layout.bodyHeight != 0 || !strings.Contains(lines[len(lines)-2], "disconnected") || !m.picked {
		t.Fatalf("minimum size:\n%s", strings.Join(lines, "\n"))
	}
	// The suggestion popup still sits on the divider above the composer.
	m, _ = readyModel()
	m.Update(MembersMsg{Channel: "#space", Members: []Member{{Nick: "alice", Account: "ma"}, {Nick: "albert", Account: "mb"}}})
	paste(m, "@al")
	m.refreshSuggest()
	lines = frameLines(t, m)
	if m.suggest == nil {
		t.Fatal("no suggestions")
	}
	divider := m.layout.inputTop - 2 // the pad row sits between them at this height
	if !strings.Contains(lines[divider], "#space") || !strings.Contains(lines[divider-1], "@all") || !strings.Contains(lines[divider-3], "albert") {
		t.Fatalf("popup is not directly above the divider:\n%s", strings.Join(lines[divider-4:divider+1], "\n"))
	}
}

// Participant lookups: one at a time, correlated with their request, and
// ignored once superseded, canceled or for a changed identity.
func TestAttachLookupIsSingleAndCorrelated(t *testing.T) {
	m, _ := readyModel()
	m.opts.Identities = map[string]Identity{"mbox": {Name: "coder", Box: true}, "mtwo": {Name: "two", Box: true}}
	type call struct {
		account string
		answer  chan AttachTargetMsg
	}
	var calls []call
	m.opts.ResolveAttach = func(account string) tea.Cmd {
		c := call{account, make(chan AttachTargetMsg, 1)}
		calls = append(calls, c)
		return func() tea.Msg { return <-c.answer }
	}
	var attached []AttachTarget
	m.opts.Attach = func(t AttachTarget) tea.Cmd { attached = append(attached, t); return nil }
	running := func(account string) AttachTargetMsg {
		return AttachTargetMsg{Target: AttachTarget{Account: account, Box: "coder", BoxID: "b", State: "running", RoomID: "r", MemberID: "m"}}
	}
	first := m.requestAttach("coder", "mbox")
	if m.requestAttach("coder", "mbox") != nil || m.requestAttach("two", "mtwo") != nil || len(calls) != 1 {
		t.Fatalf("repeated clicks queued lookups: %d", len(calls))
	}
	if !strings.Contains(m.notice, "Still checking coder") {
		t.Fatalf("a second participant waits for the first: %q", m.notice)
	}
	// A bare answer cannot stand in for the pending one.
	m.Update(running("mbox"))
	if len(attached) != 0 {
		t.Fatal("a bare answer attached while a lookup was pending")
	}
	calls[0].answer <- running("mbox")
	answer := first()
	m.Update(answer)
	if len(attached) != 1 || m.attaching != nil {
		t.Fatalf("the answer opens once: %v", attached)
	}
	m.Update(answer) // a duplicate delivery of the same answer
	if len(attached) != 1 {
		t.Fatal("a repeated answer opened again")
	}
	// A session change cancels; the late answer opens nothing.
	late := m.requestAttach("coder", "mbox")
	m.Update(SessionMsg{Nick: "human", Identities: m.opts.Identities})
	calls[1].answer <- running("mbox")
	m.Update(late())
	if len(attached) != 1 || m.attaching != nil {
		t.Fatal("an answer from the old session attached")
	}
	// Revocation through a catalog refresh cancels too.
	revoked := m.requestAttach("coder", "mbox")
	m.Update(CatalogMsg{Generation: 2, Default: "#space", Channels: []ChannelInfo{{Name: "#space"}}, Identities: map[string]Identity{"mtwo": {Name: "two", Box: true}}})
	if m.attaching != nil || !strings.Contains(m.notice, "membership changed") {
		t.Fatalf("revocation kept the lookup: %q", m.notice)
	}
	calls[2].answer <- running("mbox")
	m.Update(revoked())
	if len(attached) != 1 {
		t.Fatal("a revoked participant attached")
	}
	// An answer naming another participant, failures and a stopped box.
	next := m.requestAttach("two", "mtwo")
	calls[3].answer <- running("mbox")
	m.Update(next())
	if len(attached) != 1 || !strings.Contains(m.notice, "another participant") {
		t.Fatalf("a mismatched answer: %q", m.notice)
	}
	next = m.requestAttach("two", "mtwo")
	calls[4].answer <- AttachTargetMsg{Err: errors.New("the controller did not answer within 10 s; try again")}
	m.Update(next())
	if m.attaching != nil || !strings.Contains(m.notice, "within 10 s") {
		t.Fatalf("a deadline must end the lookup visibly: %q", m.notice)
	}
	next = m.requestAttach("two", "mtwo")
	stopped := running("mtwo")
	stopped.Target.State = "stopped"
	calls[5].answer <- stopped
	m.Update(next())
	if len(attached) != 1 || m.overlay == nil || m.overlay.kind != overlayConfirm {
		t.Fatal("a stopped box must ask first")
	}
}

// cardRole is a participant's rendered card second row, without its indent.
func cardRole(m *Model, account string) string {
	for _, mem := range m.members {
		if mem.Account == account {
			_, role := m.memberCard(mem, 80, 0)
			var b strings.Builder
			for _, r := range role {
				b.WriteString(r.text)
			}
			return strings.TrimSpace(b.String())
		}
	}
	panic("no member " + account)
}

// A box card's second row is exactly "Model (effort) · away reason", each
// part left out when it is not certain: never the agent type, a qualifier, a
// stale value, the context window or an effort without its model. A trusted
// human's row reads "human", then any away reason. The details card keeps
// Identity and Agent and shows only observed values, with the context window
// beside an observed model.
func TestParticipantsShowObservedModelAndEffortOnly(t *testing.T) {
	m := New(Options{Space: "trial", Channel: "#space", Nick: "leo", Account: "mleo", NoMotion: true, Identities: map[string]Identity{
		"mleo": {Name: "leo", Human: true}, "mlee": {Name: "lee", Human: true}, "mkim": {Name: "kim", Human: true},
		"mopus": {Name: "opus", Box: true}, "mrev": {Name: "rev", Box: true}, "mson": {Name: "son", Box: true},
		"msol": {Name: "sol", Box: true}, "mqa": {Name: "qa", Box: true}, "mfable": {Name: "fable", Box: true},
		"mnew": {Name: "new", Box: true}, "mcx": {Name: "cx", Box: true}, "mshell": {Name: "shell", Box: true},
	}})
	m.Update(ConnectionMsg{Connected: true})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 60})
	m.Update(MembersMsg{Channel: "#space", Members: []Member{
		{Nick: "leo", Account: "mleo"}, {Nick: "lee", Account: "mlee", Away: true, AwayReason: "afk"}, {Nick: "kim", Account: "mkim"},
		{Nick: "opus", Account: "mopus"}, {Nick: "rev", Account: "mrev", Away: true, AwayReason: "reviewing C"},
		{Nick: "son", Account: "mson"}, {Nick: "sol", Account: "msol"}, {Nick: "qa", Account: "mqa"}, {Nick: "fable", Account: "mfable"},
		{Nick: "new", Account: "mnew"}, {Nick: "cx", Account: "mcx", Away: true, AwayReason: "lunch"},
		{Nick: "shell", Account: "mshell"}, {Nick: "ghost", Account: "mghost"},
		{Nick: "spy", Account: "mspy", Away: true, AwayReason: "brb"},
	}})
	ctx := &RuntimeContext{Size: 200000, Used: 84000, Percent: 42}
	now := time.Now()
	m.Update(AgentsMsg{At: now, Agents: map[string]string{
		"mopus": "claude", "mrev": "claude", "mson": "claude", "msol": "claude", "mqa": "codex",
		"mfable": "claude", "mnew": "claude", "mcx": "codex", "mshell": "none",
	}, Runtime: map[string]AgentRuntime{
		"mopus":  {Model: RuntimeValue{"claude-opus-5-5", SourceStatusLine}, Effort: RuntimeValue{"max", SourceStatusLine}, Context: ctx},
		"mrev":   {Model: RuntimeValue{"claude-opus-5-5", SourceStatusLine}, Effort: RuntimeValue{"max", SourceHook}, Context: ctx},
		"mson":   {Model: RuntimeValue{"claude-sonnet-5-5", SourceHook}, Note: "the agent has not reported every value in this launch"},
		"msol":   {Effort: RuntimeValue{"low", SourceHook}, Context: ctx, Note: "the agent has not reported every value in this launch"},
		"mqa":    {Note: "no agent reports for this box"},
		"mfable": {Model: RuntimeValue{"claude-fable-5-1", ""}, Context: &RuntimeContext{Size: 1000000, Used: -1, Percent: 4}},
		"mleo":   {Model: RuntimeValue{"forged", SourceStatusLine}},
	}})
	summary := func(account string) string { return m.runtimeSummary(m.identify("", account), now) }
	cases := []struct{ account, summary, row string }{
		{"mopus", "Opus 5.5 (max)", "Opus 5.5 (max)"},              // 1
		{"mrev", "Opus 5.5 (max)", "Opus 5.5 (max) · reviewing C"}, // 2
		{"mson", "Sonnet 5.5", "Sonnet 5.5"},                       // 3
		{"msol", "", ""},                                           // 4: effort only
		{"mqa", "", ""},                                            // 5: no reports
		{"mfable", "Fable 5.1", "Fable 5.1"},                       // 6: model and context, no source
		{"mnew", "", ""},                                           // 7: nothing known
		{"mcx", "", "lunch"},                                       // 8
		{"mleo", "", "human"},                                      // 10: human, even with a forged value
		{"mkim", "", "human"},                                      // 10
		{"mlee", "", "human · afk"},                                // 11
		{"mghost", "", ""},                                         // 12: unverified
		{"mspy", "", "brb"},                                        // 12: unverified, away
		{"mshell", "", ""},                                         // 13: agent none
	}
	for _, c := range cases {
		if got := summary(c.account); got != c.summary {
			t.Errorf("%s summary: %q, want %q", c.account, got, c.summary)
		}
		if got := cardRole(m, c.account); got != c.row {
			t.Errorf("%s row: %q, want %q", c.account, got, c.row)
		}
	}
	out := view(m)
	if !strings.Contains(out, "Opus 5.5 (max) · re…") || !strings.Contains(out, "Fable 5.1") {
		t.Fatalf("panel:\n%s", out)
	}
	// Only the three trusted humans' rows read "human"; no box row does.
	if n := strings.Count(out, "human"); n != 3 {
		t.Fatalf("panel shows %d human labels:\n%s", n, out)
	}
	for _, banned := range []string{"cfg", "stale", "unverified", "effort ", "claude", "codex", "agy", "none", "agent", "forged", "%", "84k", "200k"} {
		if strings.Contains(out, banned) {
			t.Fatalf("panel shows %q:\n%s", banned, out)
		}
	}
	// A failed refresh or an old read is not certain: the card leaves the
	// model out, keeping only an away reason (case 9), while the details
	// card states the evidence.
	m.Update(AgentsMsg{Err: errors.New("controller busy")})
	if summary("mopus") != "" || cardRole(m, "mopus") != "" || cardRole(m, "mrev") != "reviewing C" {
		t.Fatalf("failed refresh: %q %q", cardRole(m, "mopus"), cardRole(m, "mrev"))
	}
	m.openCard("opus", "mopus")
	if card := view(m); !strings.Contains(card, "claude-opus-5-5 (reported by the agent's status line") || !strings.Contains(card, "stale: the last refresh failed: controller busy") {
		t.Fatalf("stale card:\n%s", card)
	}
	m.overlay = nil
	m.Update(AgentsMsg{At: now.Add(-3 * time.Minute), Runtime: map[string]AgentRuntime{
		"mopus": {Model: RuntimeValue{"claude-opus-5-5", SourceStatusLine}},
	}})
	if got := summary("mopus"); got != "" {
		t.Fatalf("old read: %q", got)
	}
	if out := view(m); strings.Contains(out, "Opus 5.5") || strings.Contains(out, "stale") {
		t.Fatalf("panel shows a stale value:\n%s", out)
	}
	// A fresh read with an old event time stays current; removed members
	// lose their values; unknown IDs stay raw, sanitized and bounded.
	m.Update(AgentsMsg{At: time.Now(), Runtime: map[string]AgentRuntime{
		"mopus":  {Model: RuntimeValue{"claude-opus-5-5", SourceStatusLine}, Effort: RuntimeValue{"max", SourceHook}, Context: ctx, Conversation: "0b8f6e3c… · resumed"},
		"mfable": {Model: RuntimeValue{"exp\x1b[31m-model\n" + strings.Repeat("x", 200), SourceStatusLine}},
		"msol":   {Effort: RuntimeValue{"low", SourceHook}, Context: ctx, Note: "the agent has not reported every value in this launch"},
		"mqa":    {Note: "no agent reports for this box"},
	}})
	got := summary("mfable")
	if strings.Contains(got, "\x1b") || strings.Contains(got, "\n") || ansi.StringWidthWc(got) > maxRuntimeText || strings.HasPrefix(got, "stale") || !strings.HasPrefix(got, "exp") {
		t.Fatalf("raw model: %q", got)
	}
	if summary("mson") != "" {
		t.Fatal("a member missing from the read kept its values")
	}
	// The details card keeps Identity and Agent and shows observed raw
	// values with the report that carried them, and the context window
	// (case 15).
	m.openCard("opus", "mopus")
	card := view(m)
	for _, want := range []string{"Identity      agent", "Agent         claude", "Opus 5.5 · claude-opus-5-5 (reported by the agent's status line",
		"max (reported by an agent hook", "Context       84k of 200k, 42%", "Conversation  0b8f6e3c… · resumed", "Evidence"} {
		if !strings.Contains(card, want) {
			t.Fatalf("card lacks %q:\n%s", want, card)
		}
	}
	if line := summary("mopus"); line != "Opus 5.5 (max)" {
		t.Fatalf("the conversation stays off the participant line: %q", line)
	}
	m.overlay = nil
	// Effort alone has its own row there, though not on the panel.
	m.openCard("sol", "msol")
	card = view(m)
	if !strings.Contains(card, "Effort        low (reported by an agent hook") || strings.Contains(card, "Model ") || strings.Contains(card, "unknown") ||
		strings.Contains(card, "Context") || !strings.Contains(card, "has not reported every value") {
		t.Fatalf("effort-only card, and never a context without a model:\n%s", card)
	}
	m.overlay = nil
	// A box without reports has no Model, Effort, Context or Evidence rows
	// and no configured wording; Identity and Agent stay.
	m.openCard("qa", "mqa")
	card = view(m)
	for _, banned := range []string{"Model ", "Effort ", "Context", "Conversation", "Evidence", "configured", "launch argument", "unknown"} {
		if strings.Contains(card, banned) {
			t.Fatalf("card without reports shows %q:\n%s", banned, card)
		}
	}
	if !strings.Contains(card, "Identity      agent") || !strings.Contains(card, "Agent         codex") || !strings.Contains(card, "Channel role  member") {
		t.Fatalf("card without reports lost its identity rows:\n%s", card)
	}
	m.overlay = nil
	m.openCard("ghost", "mghost")
	// An unconfirmed identity has no Identity row and no qualifier.
	if card := view(m); strings.Contains(card, "Identity") || strings.Contains(card, "unverified") || strings.Contains(card, "?") || !strings.Contains(card, "ghost") {
		t.Fatalf("unverified card:\n%s", card)
	}
	m.overlay = nil
	// Someone not connected has a presence but no channel role row.
	m.openCard("gone", "mgone")
	if card := ansi.Strip(strings.Join(m.cardRows(60), "\n")); !strings.Contains(card, "not connected") || strings.Contains(card, "Channel role") || strings.Contains(card, "—") {
		t.Fatalf("disconnected card:\n%s", card)
	}
	m.overlay = nil
	// A failed first read leaves nothing to qualify: still no rows.
	m.runtime, m.runtimeAt, m.runtimeErr = nil, time.Time{}, ""
	m.Update(AgentsMsg{Err: errors.New("controller busy")})
	m.openCard("new", "mnew")
	if card := view(m); strings.Contains(card, "Evidence") || strings.Contains(card, "not read yet") || strings.Contains(card, "refresh failed") {
		t.Fatalf("card without values kept evidence:\n%s", card)
	}
	m.overlay = nil
}

func TestRuntimeContextText(t *testing.T) {
	for _, c := range []struct {
		ctx  RuntimeContext
		want string
	}{
		{RuntimeContext{Size: 200000, Used: 84000, Percent: 42}, "84k of 200k, 42%"},
		{RuntimeContext{Size: 1000000, Used: 39763, Percent: 4}, "40k of 1M, 4%"},
		{RuntimeContext{Size: 1500000, Used: 812, Percent: 0}, "812 of 1.5M, 0%"},
		{RuntimeContext{Size: 1000000, Used: 999_600, Percent: 100}, "1M of 1M, 100%"},
		{RuntimeContext{Size: 200000, Used: -1, Percent: 42}, "42% of 200k"},
		{RuntimeContext{Size: 0, Used: 39763, Percent: -1}, "40k used"},
	} {
		if got := c.ctx.text(); got != c.want {
			t.Errorf("%+v: %q, want %q", c.ctx, got, c.want)
		}
	}
}

// A Codex daemon read is named as such on the card; it is not a hook.
func TestReportedByNamesTheCodexQuery(t *testing.T) {
	if got := reportedBy(SourceQuery); got != "read from the agent's own server for the current launch" {
		t.Fatalf("reportedBy(query) = %q", got)
	}
}
