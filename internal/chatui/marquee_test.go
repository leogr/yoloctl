package chatui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// marqueeDelays replaces the tick scheduler: each scheduled tick is recorded
// and returned as a command that delivers it at once, so tests step the
// chain explicitly and never sleep.
func marqueeDelays(t *testing.T) *[]time.Duration {
	t.Helper()
	var delays []time.Duration
	old := marqueeAfter
	marqueeAfter = func(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
		delays = append(delays, d)
		return func() tea.Msg { return fn(time.Time{}) }
	}
	t.Cleanup(func() { marqueeAfter = old })
	return &delays
}

const longModel = "gpt-6-astra-preview-long-name"

// marqueeModel shows qa with an observed model too long for the side
// panel, and opus with one that fits, with motion on.
func marqueeModel(t *testing.T, model string) *Model {
	t.Helper()
	m := presenceModel(t)
	m.opts.NoMotion = false
	setRuntime(m, model)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	m.View()
	return m
}

func setRuntime(m *Model, model string) {
	m.Update(AgentsMsg{Agents: map[string]string{"mqa": "codex", "mopus": "claude"}, At: time.Now(), Runtime: map[string]AgentRuntime{
		"mqa":   {Model: RuntimeValue{model, SourceStatusLine}, Effort: RuntimeValue{"xhigh", SourceStatusLine}},
		"mopus": {Model: RuntimeValue{"claude-opus-5-5", SourceStatusLine}, Effort: RuntimeValue{"max", SourceStatusLine}},
	}})
}

// memberRows is the screen position of a participant's two card rows.
func memberRows(t *testing.T, m *Model, account string) (x, name, role int) {
	t.Helper()
	m.View()
	for i, mem := range m.layout.panelMembers {
		if mem.Account == account {
			x, y := hitFor(t, m, hitPanel, i)
			return x, y, y + 1
		}
	}
	t.Fatalf("no panel member %s", account)
	return 0, 0, 0
}

// roleRow is the drawn panel part of a participant's second card row.
func roleRow(t *testing.T, m *Model, account string) string {
	t.Helper()
	_, _, y := memberRows(t, m, account)
	line := strings.Split(view(m), "\n")[y]
	_, panel, ok := strings.Cut(line, "│")
	if !ok {
		t.Fatalf("no panel on row %d: %q", y, line)
	}
	return panel
}

func hover(m *Model, x, y int) tea.Cmd {
	_, cmd := m.Update(tea.MouseMotionMsg{X: x, Y: y})
	return cmd
}

// step delivers the chain's pending tick and returns the next one.
func step(m *Model, cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	_, next := m.Update(cmd())
	return next
}

func TestHoverScrollsOnlyAClippedParticipantRow(t *testing.T) {
	delays := marqueeDelays(t)
	m := marqueeModel(t, longModel)
	static := roleRow(t, m, "mqa")
	full := longModel + " (xhigh)"
	if strings.Contains(static, "(xhigh)") || !strings.Contains(static, "…") {
		t.Fatalf("the test label must be clipped: %q", static)
	}
	ox, oy, _ := memberRows(t, m, "mopus")
	if cmd := hover(m, ox, oy); cmd != nil || m.marquee.target != (marqueeTarget{}) || len(*delays) != 0 {
		t.Fatal("a fitting row schedules nothing")
	}
	x, y, ry := memberRows(t, m, "mqa")
	cmd := hover(m, x, y)
	if cmd == nil || len(*delays) != 1 || (*delays)[0] != marqueeHold || roleRow(t, m, "mqa") != static {
		t.Fatalf("hovering a clipped row starts one chain with a hold, still static: %v %q", *delays, roleRow(t, m, "mqa"))
	}
	width := ansi.StringWidthWc(static)
	var frames []string
	for range 64 {
		cmd = step(m, cmd)
		row := roleRow(t, m, "mqa")
		if ansi.StringWidthWc(row) != width {
			t.Fatalf("the row keeps its width: %q", row)
		}
		if m.marquee.offset == 0 {
			break
		}
		if !strings.HasPrefix(row, "       …") {
			t.Fatalf("an ellipsis marks the text scrolled out: %q", row)
		}
		frames = append(frames, row)
		if len(frames) == 2 {
			// Moving onto the same participant's second row keeps the chain.
			gen, offset := m.marquee.gen, m.marquee.offset
			if next := hover(m, x, ry); next != nil || m.marquee.gen != gen || m.marquee.offset != offset {
				t.Fatal("moving within the participant restarted the chain")
			}
		}
	}
	if len(frames) < 2 || !strings.HasSuffix(strings.TrimRight(frames[len(frames)-1], " "), "(xhigh)") {
		t.Fatalf("the end of the text becomes visible: %q", frames)
	}
	last := strings.TrimRight(frames[len(frames)-1], " ")
	if !strings.HasSuffix(full, strings.TrimPrefix(last, "       …")) {
		t.Fatalf("the end frame shows the true suffix: %q", last)
	}
	if roleRow(t, m, "mqa") != static {
		t.Fatal("the cycle returns to the clipped start")
	}
	// One hold, steps, one end pause, then the hold of the next cycle.
	d := *delays
	if d[0] != marqueeHold || d[len(d)-2] != marqueeEnd || d[len(d)-1] != marqueeHold {
		t.Fatalf("delays: %v", d)
	}
	for _, s := range d[1 : len(d)-2] {
		if s != marqueeStep {
			t.Fatalf("delays: %v", d)
		}
	}
	if cmd == nil {
		t.Fatal("the next cycle is scheduled while the row stays hovered")
	}
}

func TestMarqueeStopsWhenItsRowChanges(t *testing.T) {
	started := func(t *testing.T) (*Model, tea.Cmd) {
		t.Helper()
		marqueeDelays(t)
		m := marqueeModel(t, longModel)
		x, y, _ := memberRows(t, m, "mqa")
		cmd := step(m, step(m, hover(m, x, y)))
		if m.marquee.offset != 2 || cmd == nil {
			t.Fatalf("the row did not scroll: %d", m.marquee.offset)
		}
		return m, cmd
	}
	stopped := func(t *testing.T, m *Model, old tea.Cmd) {
		t.Helper()
		if m.marquee.offset != 0 {
			t.Fatalf("the row returns to its start: %d", m.marquee.offset)
		}
		// The earlier chain's pending tick neither moves nor schedules.
		target, gen := m.marquee.target, m.marquee.gen
		if next := step(m, old); next != nil || m.marquee.offset != 0 || m.marquee.target != target || m.marquee.gen != gen {
			t.Fatal("a tick of an earlier chain acted")
		}
	}
	t.Run("another participant", func(t *testing.T) {
		m, old := started(t)
		x, y, _ := memberRows(t, m, "mopus")
		hover(m, x, y)
		stopped(t, m, old)
		if m.marquee.target != (marqueeTarget{}) {
			t.Fatal("a fitting row has no chain")
		}
	})
	t.Run("press", func(t *testing.T) {
		m, old := started(t)
		x, y, _ := memberRows(t, m, "mqa")
		m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
		stopped(t, m, old)
	})
	t.Run("resize", func(t *testing.T) {
		m, old := started(t)
		m.Update(tea.WindowSizeMsg{Width: 81, Height: 40})
		stopped(t, m, old)
	})
	t.Run("modal", func(t *testing.T) {
		m, old := started(t)
		m.openCard("qa", "mqa")
		m.Update(nil)
		stopped(t, m, old)
		if !strings.Contains(view(m), longModel) {
			t.Fatal("the details card keeps the full value")
		}
	})
	t.Run("label", func(t *testing.T) {
		m, old := started(t)
		_, cmd := m.Update(AgentsMsg{Agents: map[string]string{"mqa": "codex"}, At: time.Now(), Runtime: map[string]AgentRuntime{
			"mqa": {Model: RuntimeValue{longModel + "-2", SourceStatusLine}, Effort: RuntimeValue{"xhigh", SourceStatusLine}},
		}})
		stopped(t, m, old)
		if cmd == nil || m.marquee.target.text != longModel+"-2 (xhigh)" {
			t.Fatalf("a new clipped label starts its own chain: %+v", m.marquee.target)
		}
	})
	t.Run("channel", func(t *testing.T) {
		m, old := started(t)
		m.Update(CatalogMsg{Generation: 1, Default: "#space", Channels: []ChannelInfo{{Name: "#space"}, {Name: "#build"}}, Applied: true})
		m.Update(MembersMsg{Channel: "#build", Members: m.members})
		m.switchTo("#build")
		m.Update(nil)
		stopped(t, m, old)
	})
}

func TestMarqueeNeverStartsWithoutAClippedCard(t *testing.T) {
	t.Run("compact rows", func(t *testing.T) {
		delays := marqueeDelays(t)
		m := marqueeModel(t, longModel)
		m.Update(tea.WindowSizeMsg{Width: 80, Height: 14})
		m.View()
		if m.layout.cardWidth != 0 {
			t.Fatal("the test needs one-row participants")
		}
		for i := range m.layout.panelMembers {
			x, y := hitFor(t, m, hitPanel, i)
			hover(m, x, y)
		}
		if len(*delays) != 0 {
			t.Fatal("compact rows never scroll")
		}
	})
	t.Run("no motion", func(t *testing.T) {
		delays := marqueeDelays(t)
		m := marqueeModel(t, longModel)
		m.opts.NoMotion = true
		x, y, _ := memberRows(t, m, "mqa")
		if cmd := hover(m, x, y); cmd != nil || len(*delays) != 0 {
			t.Fatal("NoMotion schedules no tick")
		}
		var hint strings.Builder
		for _, r := range m.hoverHint() {
			hint.WriteString(r.text)
		}
		if !strings.Contains(hint.String(), longModel+" (xhigh)") || !strings.Contains(hint.String(), "click to attach") {
			t.Fatalf("the footer names the full row: %q", hint.String())
		}
		m.say(levelError, "something failed")
		if !strings.Contains(view(m), "something failed") {
			t.Fatal("a notice keeps priority over the hover hint")
		}
	})
	t.Run("empty row", func(t *testing.T) {
		// sol has no observed model and no away reason: its row is blank.
		delays := marqueeDelays(t)
		m := marqueeModel(t, longModel)
		if got := strings.TrimSpace(roleRow(t, m, "msol")); got != "" {
			t.Fatalf("an unknown model leaves the row empty: %q", got)
		}
		x, y, _ := memberRows(t, m, "msol")
		if cmd := hover(m, x, y); cmd != nil || len(*delays) != 0 || m.marquee.target != (marqueeTarget{}) {
			t.Fatal("an empty row never scrolls")
		}
		m.opts.NoMotion = true
		hover(m, x, y)
		if _, ok := m.hoveredLabel(); ok {
			t.Fatal("an empty row has no label for the footer")
		}
		var hint strings.Builder
		for _, r := range m.hoverHint() {
			hint.WriteString(r.text)
		}
		if name, what := m.participantHint("msol", "sol"); hint.String() != " ▸ "+name+"  "+what {
			t.Fatalf("the footer names nothing before the hint: %q", hint.String())
		}
	})
	t.Run("too narrow", func(t *testing.T) {
		if got := marqueeSteps(ansi.WcWidth, marqueeTarget{text: longModel, width: marqueeMin}); got != 0 {
			t.Fatalf("a row with no legible room keeps static clipping: %d", got)
		}
	})
}

// Scrolled rows never split a grapheme or exceed their cells.
func TestMarqueeWindowKeepsGraphemesWhole(t *testing.T) {
	for _, text := range []string{
		"模型名称很长的模型名称很长的 (xhigh)",
		"mode\u0301le\u0300 combining marks e\u0301e\u0301e\u0301 (high)",
		"agent 👩\u200d💻👨\u200d👩\u200d👧 family 🏳\ufe0f\u200d🌈 (max)",
	} {
		target := marqueeTarget{text: text, width: 10}
		steps := marqueeSteps(ansi.WcWidth, target)
		if steps == 0 {
			t.Fatalf("%q: expected room to scroll", text)
		}
		for offset := 1; offset <= steps; offset++ {
			row := marqueeWindow(ansi.WcWidth, target, offset)
			if w := ansi.StringWidthWc(row); w > target.width {
				t.Fatalf("%q at %d: %d cells: %q", text, offset, w, row)
			}
			middle := strings.TrimSuffix(strings.TrimPrefix(row, "…"), "…")
			if !strings.Contains(text, middle) || strings.HasPrefix(middle, "\u0301") || strings.HasPrefix(middle, "\u0300") || strings.HasPrefix(middle, "\u200d") || strings.HasSuffix(middle, "\u200d") || strings.HasPrefix(middle, "\ufe0f") {
				t.Fatalf("%q at %d split a grapheme: %q", text, offset, row)
			}
		}
		if end := marqueeWindow(ansi.WcWidth, target, steps); !strings.HasSuffix(text, strings.TrimPrefix(end, "…")) {
			t.Fatalf("%q: the last step shows the end: %q", text, end)
		}
	}
}
