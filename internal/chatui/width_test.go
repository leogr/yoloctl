package chatui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/config"
)

// The renderer paints StyledString into a width-method-aware ultraviolet
// screen. Use the same cell path, with extra room to expose clipping.
func bufferRow(text string, width int, method ansi.Method) *uv.Window {
	w := uv.NewWindow(width, 1, method)
	uv.NewStyledString(text).Draw(w, w.Bounds())
	return w
}

func emojiTableModel() *Model {
	m, _ := readyModel()
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 32})
	m.Update(IncomingMsg{Message: Message{ID: "emoji-table", Nick: "coordinator", Text: "| Ticket | Stage | Note |\n|---|---|---|\n| redraw | ▶️ Build | width audit |\n| done | ✅ Merge | aligned |\n\nprose ▶️ Build"}})
	return m
}

func TestRendererWidthBaseline(t *testing.T) {
	m := emojiTableModel()
	content := m.View().Content
	tw := m.layout.transcriptWidth
	found := 0
	for y, row := range strings.Split(content, "\n") {
		if !strings.Contains(row, "▶️") {
			continue
		}
		found++
		buf := bufferRow(row, m.width+8, ansi.GraphemeWidth)
		divider := -1
		for x := tw; x < m.width+8; x++ {
			if c := buf.CellAt(x, 0); c != nil && c.Content == "│" {
				divider = x
				break
			}
		}
		t.Logf("baseline row=%d divider=%d expected=%d extent=%d viewport=%d", y, divider, tw, ansi.GraphemeWidth.StringWidth(row), m.width)
		if divider != tw+1 || ansi.GraphemeWidth.StringWidth(row) != m.width+1 {
			t.Fatalf("baseline mismatch did not reproduce: divider=%d, want %d", divider, tw+1)
		}
	}
	if found != 2 {
		t.Fatalf("table and prose reproduction rows=%d, want 2", found)
	}
	reportWidth(m, ansi.ModeReset)
	checkWidthFrame(t, m, true)
	for y, row := range strings.Split(m.View().Content, "\n") {
		if strings.Contains(row, "▶️") {
			t.Logf("fixed row=%d divider=%d extent=%d viewport=%d", y, m.layout.transcriptWidth, m.widthMethod.StringWidth(row), m.width)
		}
	}
}

func reportWidth(m *Model, value ansi.ModeSetting) {
	m.Update(tea.ModeReportMsg{Mode: ansi.ModeUnicodeCore, Value: value})
}

// Verify real buffer cells, the unclipped extent and a sentinel immediately
// after every composed row. This catches a shifted divider and right clipping.
func checkWidthFrame(t *testing.T, m *Model, panel bool) {
	t.Helper()
	for y, row := range strings.Split(m.View().Content, "\n") {
		buf := bufferRow(row+"\x1b[0m§", m.width+16, m.widthMethod)
		if c := buf.CellAt(m.width, 0); c == nil || c.Content != "§" {
			t.Fatalf("row %d extent != %d (method %v): %q", y, m.width, m.widthMethod, ansi.Strip(row))
		}
		if panel && y >= m.layout.bodyTop && y < m.layout.bodyTop+m.layout.bodyHeight {
			if c := buf.CellAt(m.layout.transcriptWidth, 0); c == nil || c.Content != "│" {
				t.Fatalf("row %d divider != %d: %q", y, m.layout.transcriptWidth, ansi.Strip(row))
			}
		}
	}
}

func TestWidthModeReports(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value ansi.ModeSetting
		want  ansi.Method
	}{
		{"unknown", ansi.ModeNotRecognized, ansi.WcWidth},
		{"permanently-reset", ansi.ModePermanentlyReset, ansi.WcWidth},
		{"set", ansi.ModeSet, ansi.GraphemeWidth},
		{"reset", ansi.ModeReset, ansi.GraphemeWidth},
		{"permanently-set", ansi.ModePermanentlySet, ansi.GraphemeWidth},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := emojiTableModel()
			m.View()
			if m.widthMethod != ansi.WcWidth {
				t.Fatal("default method differs from renderer")
			}
			m.Update(tea.ModeReportMsg{Mode: ansi.ModeMouseExtSgr, Value: tc.value})
			if m.widthMethod != ansi.WcWidth {
				t.Fatal("unrelated mode changed width")
			}
			reportWidth(m, tc.value)
			if m.widthMethod != tc.want {
				t.Fatalf("got %v want %v", m.widthMethod, tc.want)
			}
			checkWidthFrame(t, m, true)
			gen := m.generation
			reportWidth(m, tc.value)
			if m.generation != gen {
				t.Fatal("repeat invalidated caches")
			}
			if tc.want == ansi.GraphemeWidth {
				for _, ignored := range []ansi.ModeSetting{ansi.ModeNotRecognized, ansi.ModePermanentlyReset} {
					reportWidth(m, ignored)
				}
				if m.widthMethod != tc.want || m.generation != gen {
					t.Fatal("ignored report reverted/invalidated method")
				}
			}
		})
	}
}

var widthSymbols = []string{"▶", "▶️", "⏸", "⏸️", "⚠️", "✔️", "👩‍💻", "🇫🇷", "e\u0301", "界", "ASCII"}

func TestWidthSymbolFrames(t *testing.T) {
	for _, method := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		for _, symbol := range widthSymbols {
			for _, width := range []int{140, 58, 23} {
				for _, panel := range []bool{false, true} {
					t.Run(fmt.Sprintf("%v/%s/%d/panel=%v", method, symbol, width, panel), func(t *testing.T) {
						m, _ := readyModel()
						m.Update(tea.WindowSizeMsg{Width: width, Height: 42})
						if method == ansi.GraphemeWidth {
							reportWidth(m, ansi.ModeReset)
						}
						m.rosterOpen = panel
						m.topic = strings.Repeat(symbol, 30)
						m.opts.Identities = map[string]Identity{"agent": {Name: strings.Repeat(symbol, 12), Box: true, Agent: "codex"}}
						m.opts.Icons = map[string]config.AgentIcon{"codex": {Icon: symbol}}
						m.Update(MembersMsg{Members: []Member{{Nick: "agent", Account: "agent", Away: true, AwayReason: strings.Repeat(symbol, 16)}}})
						m.Update(IncomingMsg{Message: Message{ID: "symbols", Nick: symbol, Text: "| Stage | Note |\n|---|---|\n| " + symbol + " Build | long enough to wrap in narrow views |\n\nprose " + symbol + " after\n```" + symbol + "\n" + symbol + "\tcode\n```"}})
						checkWidthFrame(t, m, panel && panelWidth(width) > 0)
						m.openHelp()
						checkWidthFrame(t, m, false)
						m.openCard("agent", "agent")
						checkWidthFrame(t, m, false)
						m.overlay = nil
						m.linkPrompt = &linkPrompt{url: "https://example.com/" + strings.Repeat(symbol, 12)}
						checkWidthFrame(t, m, false)
					})
				}
			}
		}
	}
}

func TestWidthClusterInteraction(t *testing.T) {
	for _, method := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		for _, symbol := range widthSymbols {
			t.Run(fmt.Sprintf("%v/%s", method, symbol), func(t *testing.T) {
				if symbol == "ASCII" {
					symbol = "A"
				}
				text := "a" + symbol + "z"
				w := method.StringWidth(symbol)
				runs := []run{{Text: "a", Copy: true}, {Text: symbol, Copy: true, Start: 1, Act: action{Kind: actLink, Value: "https://example.com"}}, {Text: "z", Copy: true, Start: 1 + len(symbol)}}
				for x := 1; x < 1+w; x++ {
					lo, hi := hitRun(method, runs, x, len(text))
					if lo != 1 || hi != 1+len(symbol) || actionAt(method, runs, x).Kind != actLink {
						t.Fatalf("cluster hit %d = [%d,%d)", x, lo, hi)
					}
				}
				for width := 1; width <= w+2; width++ {
					got := truncate(method, text, width, "")
					if method.StringWidth(got) > width || !slices.Contains(clusters(text), len(got)) {
						t.Fatalf("truncation split cluster: %q", got)
					}
					for _, r := range wrapRanges(method, text, width, false) {
						if !slices.Contains(clusters(text), r[0]) || !slices.Contains(clusters(text), r[1]) {
							t.Fatalf("wrap split cluster: %v", r)
						}
					}
				}
				// Tab expansion and its source map use the same columns.
				var src srcMap
				expanded := expandTabsMap(method, symbol+"\tZ", &src)
				if want := symbol + strings.Repeat(" ", 4-w%4) + "Z"; expanded != want {
					t.Fatalf("tab=%q want %q", expanded, want)
				}
				rows, row, col := draftLayout(method, symbol+"\tZ", nil, len(symbol), 12)
				if row != 0 || col != w || rows[0].stopAt(w) != len(symbol) {
					t.Fatalf("cursor row=%d col=%d", row, col)
				}
				e := editor{editState: editState{text: symbol + "x\nabcd", cursor: len(symbol)}, limit: 100}
				e.vertical(method, 1, 12)
				if e.cursor != len(symbol+"x\n")+w {
					t.Fatalf("vertical cursor=%d width=%d", e.cursor, w)
				}
			})
		}
	}
}

func TestWidthLateReportInvalidatesEveryChannel(t *testing.T) {
	m := emojiTableModel()
	m.opts.Copy = func(string) tea.Cmd { t.Fatal("late report copied"); return nil }
	m.view("#other")
	text := "| Stage | Note |\n|---|---|\n| ▶️\tBuild | 👩‍💻 |\n\n▶️\tprose"
	for _, channel := range []string{"#space", "#other"} {
		m.within(channel, func() {
			m.messages = []Message{{ID: channel, Nick: "x", Text: text}, {ID: channel + "-tail", Nick: "x", Text: strings.Repeat("later\n", 60)}}
			_ = m.SetDraft("▶️\tdraft\nnext")
			m.editor.cursor = len("▶️")
			column := 7
			m.editor.column = &column
			m.replyTo = channel
			m.follow = false
			m.anchor = &viewAnchor{key: "server:" + channel, line: 2, offset: 7}
			m.sel = &selection{anchor: selPoint{key: "server:" + channel, line: 2}, focus: selPoint{key: "server:" + channel, line: 2, hi: 4}}
			m.body(&m.messages[0], 70)
			m.layoutDraft()
			m.built.rows = m.transcript(70)
			m.built.generation = m.generation
		})
	}
	m.drag = dragState{active: true, moved: true, pressOK: true, area: hitTranscript}
	gesture := m.gesture
	m.marquee = marquee{gen: 9, offset: 2}
	reportWidth(m, ansi.ModeReset)
	if p, ok := m.pointAt(bodyIndent+1, m.layout.bodyTop); !ok || p.key != "server:#space" || p.line != 2 || p.lo != 0 || p.hi != len("▶️") {
		t.Fatalf("hit before next View uses old geometry: %+v", p)
	}
	if m.views["#other"].draft.rows != nil || m.views["#other"].built.rows != nil || len(m.views["#other"].bodyCache) != 0 {
		t.Fatal("inactive geometry caches survived")
	}
	if m.drag.active || m.marquee.offset != 0 || m.marquee.gen <= 9 {
		t.Fatal("old geometry gesture/marquee survived")
	}
	if _, cmd := m.Update(tea.MouseReleaseMsg{X: bodyIndent, Y: m.layout.bodyTop, Button: tea.MouseLeft}); cmd != nil {
		t.Fatal("old release acted")
	}
	if m.dragStep(dragTickMsg{gesture}) != nil {
		t.Fatal("old drag tick acted")
	}
	for _, channel := range []string{"#space", "#other"} {
		m.switchTo(channel)
		checkWidthFrame(t, m, true)
		if m.sel != nil || m.editor.column != nil || m.Draft() != "▶️\tdraft\nnext" || m.editor.cursor != len("▶️") || m.replyTo != channel || m.follow {
			t.Fatal("late report changed retained state or kept coordinates")
		}
		body := m.body(&m.messages[0], 70)
		fresh := displayLines(ansi.GraphemeWidth, text, false, m.theme, m.self)
		for i := range fresh {
			if body.lines[i].Text != fresh[i].Text {
				t.Fatalf("%s stale line %d: %q != %q", channel, i, body.lines[i].Text, fresh[i].Text)
			}
		}
		r := m.lastTranscript[m.scroll]
		if r.key != "server:"+channel || r.line != 2 || r.offset != 0 {
			t.Fatalf("reading anchor lost: %+v", r)
		}
	}
	// Independent models retain the renderer fallback and separate caches.
	other := emojiTableModel()
	if other.widthMethod != ansi.WcWidth {
		t.Fatal("model width leaked")
	}
	checkWidthFrame(t, other, true)
}

func TestWidthCopyQuoteBytes(t *testing.T) {
	for _, method := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		m := quoteModel(t, "▶️\t**bold**\n\n| Stage | Note |\n|---|---|\n| ▶️\tBuild | 🇫🇷 |")
		if method == ansi.GraphemeWidth {
			reportWidth(m, ansi.ModeSet)
		}
		highlightText(t, m, "q", 0, "bold")
		if got := quoted(m); got != "> **bold**\n\n" {
			t.Fatalf("source quote %q: %s", got, m.notice)
		}
		highlightSpan(t, m, "q", 4, "▶️", 4, "🇫🇷")
		if got := quoted(m); got != "> ▶️"+strings.Repeat(" ", 4-method.StringWidth("▶️")%4)+"Build\t🇫🇷\n\n" {
			t.Fatalf("table quote %q: %s", got, m.notice)
		}
		if m.messages[m.indexOf("server:q")].Text != "▶️\t**bold**\n\n| Stage | Note |\n|---|---|\n| ▶️\tBuild | 🇫🇷 |" {
			t.Fatal("source mutated")
		}
		// Source view still copies original tabs and Markdown, while display
		// copy uses the current expansion and excludes layout-only padding.
		m.source = true
		m.bodyCache = map[string]cachedBody{}
		highlightSpan(t, m, "q", 0, "▶️", 0, "**bold**")
		if got := quoted(m); got != "> ▶️\t**bold**\n\n" {
			t.Fatalf("tab source map quote %q: %s", got, m.notice)
		}
	}
}

func TestWidthReferencePreviewCopy(t *testing.T) {
	for _, method := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		m, copies, _ := filterModel(t)
		id := m.opts.Identities["mo"]
		id.Nick = strings.Repeat("▶️", 12)
		m.opts.Identities["mo"] = id
		arrive(m, refTarget, "mo", "opus", "a loaded target")
		arrive(m, "q", "mq", "qa", "▶️\tsee "+sharedTarget+" end")
		m.View() // prime the preview before negotiation
		if method == ansi.GraphemeWidth {
			reportWidth(m, ansi.ModeSet)
		}
		m.View()
		body := m.body(&m.messages[m.indexOf("server:q")], 70)
		wantName := method.Truncate(id.Nick, previewName, "…")
		if !strings.Contains(body.lines[0].Text, "↗ "+wantName+": a loaded target") {
			t.Fatalf("preview uses old name width: %q", body.lines[0].Text)
		}
		// The buffer and click ranges agree even after a width-changing emoji.
		for y := m.layout.bodyTop; y < m.layout.bodyTop+m.layout.bodyHeight; y++ {
			row, ok := m.rowAt(y)
			if !ok || row.key != "server:q" || row.kind != rowBody {
				continue
			}
			buf := bufferRow(paint(method, row.runs, m.layout.transcriptWidth, row.fill, nil, m.selectionStyle()), m.width, method)
			for x := 0; x < m.layout.transcriptWidth; x++ {
				if c := buf.CellAt(x, 0); c != nil && c.Content == "↗" && actionAt(method, row.runs, x).Kind != actRef {
					t.Fatal("preview hit shifted")
				}
			}
		}
		highlight(m, "q", 0, 0, 0, len(body.lines[0].Text))
		m.copySelection()
		want := "▶️" + strings.Repeat(" ", 4-method.StringWidth("▶️")%4) + "see " + sharedTarget + " end"
		if len(*copies) != 1 || (*copies)[0] != want {
			t.Fatalf("display copy %v want %q", *copies, want)
		}
		if got := quoted(m); got != "> ▶️\tsee "+sharedTarget+" end\n\n" {
			t.Fatalf("reference quote %q: %s", got, m.notice)
		}
		m.pick("server:q")
		m.runAction("copy")
		if (*copies)[len(*copies)-1] != "▶️\tsee "+sharedTarget+" end" {
			t.Fatal("Source copy changed bytes")
		}
	}
}

func TestWidthTableFloorFallback(t *testing.T) {
	for _, method := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		m, _ := readyModel()
		if method == ansi.GraphemeWidth {
			reportWidth(m, ansi.ModeReset)
		}
		text := "| First | Second | Third | Fourth |\n|---|---|---|---|\n| ▶️ Build | 👩‍💻 work | 🇫🇷 flag | ⚠️ notice |"
		lines := displayLines(method, text, false, m.theme, "")
		if lines[0].Table == nil || fitTable(method, lines, 0, 15, maxBodyRows, m.theme) != nil {
			t.Fatal("fixture must take the floor fallback")
		}
		m.Update(tea.WindowSizeMsg{Width: 20, Height: 40})
		m.Update(IncomingMsg{Message: Message{ID: "fallback", Nick: "x", Text: text}})
		checkWidthFrame(t, m, false)
	}
}

func TestWidthMarqueeFrames(t *testing.T) {
	marqueeDelays(t)
	for _, method := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		m := marqueeModel(t, strings.Repeat("▶️🇫🇷👩‍💻", 4))
		if method == ansi.GraphemeWidth {
			reportWidth(m, ansi.ModeSet)
		}
		x, _, y := memberRows(t, m, "mqa")
		cmd := hover(m, x+4, y)
		for range 12 {
			checkWidthFrame(t, m, true)
			if cmd == nil {
				t.Fatal("clipped participant stopped scrolling")
			}
			cmd = step(m, cmd)
		}
	}
}

func TestWidthOverlayEdges(t *testing.T) {
	for _, method := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		for _, symbol := range widthSymbols {
			base := "\x1b[31m" + fit(method, strings.Repeat("a"+symbol, 40), 40) + "\x1b[0m"
			for x := 0; x <= 33; x++ {
				row := overlayLine(method, base, x, "box    ", 40)
				buf := bufferRow(row+"§", 50, method)
				if c := buf.CellAt(40, 0); c == nil || c.Content != "§" {
					t.Fatalf("%v/%s x=%d extent=%d: %q", method, symbol, x, method.StringWidth(row), ansi.Strip(row))
				}
				if c := buf.CellAt(x, 0); c == nil || c.Content != "b" {
					t.Fatal("overlay shifted")
				}
			}
		}
	}
}

// This string fits a 54-cell grapheme budget but exceeds a wcwidth budget.
// Pin the library fast-path failure, exact tails, whole clusters and style end.
func TestWidthTruncationZWJAndTail(t *testing.T) {
	text := strings.Repeat("👩‍💻", 16)
	styled := "\x1b[31m" + text + "\x1b[0m"
	for _, method := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		for _, tail := range []string{"", "…"} {
			got := truncate(method, styled, 54, tail)
			want := text
			if method.StringWidth(text) > 54 {
				want = strings.Repeat("👩‍💻", (54-method.StringWidth(tail))/method.StringWidth("👩‍💻")) + tail
			}
			if ansi.Strip(got) != want || !strings.HasSuffix(got, "\x1b[0m") {
				t.Fatalf("method=%v tail=%q got %q want %q with reset", method, tail, got, want)
			}
			buf := bufferRow(got+"§", 80, method)
			if c := buf.CellAt(method.StringWidth(want), 0); c == nil || c.Content != "§" {
				t.Fatal("truncated cells exceed selected method budget")
			}
		}
		if got := truncate(method, text, 1, "…"); got != "…" {
			t.Fatalf("narrow tail=%q", got)
		}
		if got := truncate(method, text, 0, "…"); got != "" {
			t.Fatalf("oversize tail=%q", got)
		}
	}
}
