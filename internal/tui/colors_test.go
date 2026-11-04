package tui

import (
	"context"
	"fmt"
	"image/color"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/leogr/yoloctl/internal/tui/term"
)

func TestHostPaletteQueriesAreBoundedAndReadOnly(t *testing.T) {
	out := requestHostPalette().(tea.RawMsg).Msg.(string)
	var decoder uv.EventDecoder
	count := 0
	for out != "" {
		n, event := decoder.Decode([]byte(out))
		if n <= 0 {
			t.Fatal("unterminated palette request")
		}
		sequence, ok := event.(uv.UnknownOscEvent)
		if !ok || !strings.HasSuffix(string(sequence), ";?\x1b\\") {
			t.Fatalf("startup emitted a non-query %T %q", event, event)
		}
		out = out[n:]
		count++
	}
	if count != 256 {
		t.Fatalf("palette query count: %d", count)
	}
}

func TestHostPaletteDecodedReportsKeepChildOverrides(t *testing.T) {
	m := newTestModel(t, 40, 10)
	addExitedTab(t, m, "one")
	m.tabs.Tabs[0].Focused().Session.Clear()
	_, _ = m.tabs.Tabs[0].Focused().Session.Write([]byte("\x1b]4;1;#abcdef\a\x1b[31moverride"))
	_, _ = m.console.Write([]byte("\x1b[31mhost"))
	var decoder uv.EventDecoder
	sequence := "\x1b]4;1;rgb:1234/5678/9abc\x1b\\"
	_, event := decoder.Decode([]byte(sequence))
	_, _ = m.Update(event)
	want := color.RGBA{0x12, 0x56, 0x9a, 255}
	if m.hostPalette[1] != want {
		t.Fatalf("host color not cached for new tabs: %v", m.hostPalette[1])
	}
	if got := m.console.RenderLine(0); !strings.Contains(got, "\x1b[38;5;1m") {
		t.Fatalf("console theme flattened to RGB: %q", got)
	}
	if got := m.tabs.Tabs[0].Focused().Session.RenderLine(0); !strings.Contains(got, "\x1b[38;2;171;205;239m") {
		t.Fatalf("host query clobbered child color: %q", got)
	}
	_, _ = m.Update(uv.UnknownOscEvent("\x1b]4;1;rgb:gg/0/0\a"))
	if m.hostPalette[1] != want {
		t.Fatal("malformed host color changed the palette")
	}
}

// Bubble Tea's real loop runs Update and View for each admitted event. A host
// palette burst must reach Update as one batch while other input keeps order.
type paletteBurstModel struct {
	*model
	seen  *[]string
	views *int
}

func (w paletteBurstModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if msg.Code == tea.KeyF12 {
			*w.seen = append(*w.seen, "quit")
			return w, tea.Quit
		}
	case tea.FocusMsg:
		*w.seen = append(*w.seen, "focus")
	case tea.MouseClickMsg:
		*w.seen = append(*w.seen, "press")
	case tea.MouseReleaseMsg:
		*w.seen = append(*w.seen, "release")
	case uv.UnknownOscEvent:
		*w.seen = append(*w.seen, fmt.Sprintf("osc %q", string(msg)))
	case hostPaletteStart:
		*w.seen = append(*w.seen, "start")
	case hostPaletteFlush:
		*w.seen = append(*w.seen, "flush")
	}
	_, cmd := w.model.Update(msg)
	return w, cmd
}

func (w paletteBurstModel) View() tea.View {
	*w.views++
	return w.model.View()
}

func paletteReport(index int, rgb string) string {
	return fmt.Sprintf("\x1b]4;%d;rgb:%s\x1b\\", index, rgb)
}

func TestHostPaletteBurstThroughHostInputLoop(t *testing.T) {
	// Only completion may flush here, however slowly the burst is decoded; the
	// deadline path has its own strict test below.
	window := hostPaletteWindow
	hostPaletteWindow = time.Hour
	t.Cleanup(func() { hostPaletteWindow = window })
	m := newTestModel(t, 80, 24)
	addExitedTab(t, m, "one")
	// Out of order, with a duplicate: index 255 is not the completion signal.
	order := make([]int, 0, 257)
	for i := range 256 {
		order = append(order, (i*97+13)%256)
	}
	order = append(order[:50], append([]int{order[10]}, order[50:]...)...)
	var input strings.Builder
	input.WriteString("\x1b[I")
	for n, index := range order {
		switch n {
		case 100:
			input.WriteString("\x1b[<0;3;3M\x1b]4;1;rgb:gg/0/0\a\x1b]777;notify;x\x1b\\")
		case 200:
			input.WriteString("\x1b[<0;3;3m")
		}
		input.WriteString(paletteReport(index, fmt.Sprintf("%02x%02x/0000/%02x%02x", index, index, 255-index, 255-index)))
	}
	input.WriteString("\x1b[24~")
	var seen []string
	views := 0
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	opts := append(m.programOptions(), tea.WithContext(ctx),
		tea.WithInput(strings.NewReader(input.String())), tea.WithOutput(io.Discard),
		tea.WithWindowSize(80, 24), tea.WithEnvironment([]string{"TERM=xterm-256color"}), tea.WithoutSignals())
	if _, err := tea.NewProgram(paletteBurstModel{m, &seen, &views}, opts...).Run(); err != nil {
		t.Fatal(err)
	}
	want := []string{"focus", "start", "press", `osc "\x1b]4;1;rgb:gg/0/0\a"`, `osc "\x1b]777;notify;x\x1b\\"`, "release", "flush", "quit"}
	if !slices.Equal(seen, want) {
		t.Fatalf("events reaching Update:\n got %q\nwant %q", seen, want)
	}
	// Unbatched, the 257 reports alone rendered 257 views.
	if views > 64 {
		t.Fatalf("palette burst still renders per report: %d views", views)
	}
	for i := range 256 {
		if want := (color.RGBA{uint8(i), 0, uint8(255 - i), 255}); m.hostPalette[i] != want {
			t.Fatalf("palette[%d] = %v, want %v", i, m.hostPalette[i], want)
		}
	}
	if m.paletteBatch.open {
		t.Fatal("complete batch left open")
	}
}

// Partial replies flush once at the deadline armed by the first report; later
// reports cannot extend it, and stale deadlines never flush another batch.
func TestHostPalettePartialRepliesFlushAtFixedDeadline(t *testing.T) {
	m := newTestModel(t, 40, 10)
	capture := t.TempDir() + "/palette-reply"
	s, err := term.Start(term.Options{
		Argv: []string{"sh", "-c", `stty raw -echo
printf '\033]4;7;?\033\\ready'
exec dd bs=1 count=26 of="$1" 2>/dev/null`, "sh", capture},
		Rows: m.viewRows(), Cols: m.width,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	m.tabs.Add(testTab(s, "palette-test", false))
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(s.RenderLine(0), "ready") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(s.RenderLine(0), "ready") {
		t.Fatal("child query was not processed")
	}
	filter := func(msg tea.Msg) tea.Msg { return m.filterHostPalette(m, msg) }
	if got := filter(uv.UnknownOscEvent(paletteReport(255, "ffff/0000/0000"))); got != hostPaletteStart(1) {
		t.Fatalf("first report admitted %#v", got)
	}
	_, cmd := m.Update(hostPaletteStart(1))
	for _, msg := range []string{paletteReport(7, "1212/3434/5656"), "\x1b]4;200;rgb:0/0/0;201;rgb:1/1/1\a", paletteReport(7, "1212/3434/5656")} {
		if got := filter(uv.UnknownOscEvent(msg)); got != nil {
			t.Fatalf("continuing report %q admitted %#v", msg, got)
		}
	}
	for _, msg := range []tea.Msg{
		uv.UnknownOscEvent("\x1b]4;1;rgb:gg/0/0\a"), uv.UnknownOscEvent("\x1b]4;7;?\x1b\\"),
		uv.UnknownOscEvent("\x1b]777;notify;x\x1b\\"), tea.MouseClickMsg{X: 1, Y: 1},
	} {
		if got := filter(msg); got != msg {
			t.Fatalf("filter changed %#v to %#v", msg, got)
		}
	}
	if m.hostPalette[7] != nil || m.paletteBatch.count != 4 {
		t.Fatalf("reports applied before the batch: %v count=%d", m.hostPalette[7], m.paletteBatch.count)
	}
	if got := runPaletteCmd(cmd); !slices.Contains(got, tea.Msg(hostPaletteFlush(1))) {
		t.Fatalf("start did not arm its deadline: %#v", got)
	}
	_, _ = m.Update(hostPaletteFlush(1))
	for index, want := range map[int]color.Color{7: color.RGBA{0x12, 0x34, 0x56, 255}, 200: color.RGBA{0, 0, 0, 255}, 201: color.RGBA{0x11, 0x11, 0x11, 255}, 255: color.RGBA{0xff, 0, 0, 255}} {
		if m.hostPalette[index] != want {
			t.Fatalf("palette[%d] = %v, want %v", index, m.hostPalette[index], want)
		}
	}
	if err := s.Wait(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(capture); err != nil || string(got) != "\x1b]4;7;rgb:1212/3434/5656\x1b\\" {
		t.Fatalf("pending child query reply = %q, %v", got, err)
	}
	// A repeated deadline is ignored; the next report opens a new batch whose
	// own deadline is the only one that flushes it.
	_, _ = m.Update(hostPaletteFlush(1))
	if got := filter(uv.UnknownOscEvent(paletteReport(3, "0303/0303/0303"))); got != hostPaletteStart(2) {
		t.Fatalf("later report admitted %#v", got)
	}
	_, _ = m.Update(hostPaletteFlush(1))
	if m.hostPalette[3] != nil || !m.paletteBatch.open {
		t.Fatal("stale deadline flushed the next batch")
	}
	_, _ = m.Update(hostPaletteFlush(2))
	if m.hostPalette[3] != (color.RGBA{3, 3, 3, 255}) || m.paletteBatch.open {
		t.Fatal("second batch was not applied at its deadline")
	}
}

func runPaletteCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, runPaletteCmd(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}
