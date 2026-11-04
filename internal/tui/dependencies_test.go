package tui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// These tests cover the host library boundary independently of the child VT.
// Correct decoding here is necessary but does not prove that yoloctl forwards
// enhanced events correctly (that has its own acceptance cases).
func TestHostDecodesKittyFunctionRelease(t *testing.T) {
	var decoder uv.EventDecoder
	const encoded = "\x1b[15;1:3~" // Kitty: F5, no modifiers, release.
	n, event := decoder.Decode([]byte(encoded))
	key, ok := event.(uv.KeyReleaseEvent)
	if !ok || n != len(encoded) || key.Code != uv.KeyF5 {
		t.Fatalf("F5 release decoded as %T %+v (%d bytes)", event, event, n)
	}
}

func TestHostRendererKeepsCombiningTextAndHyperlink(t *testing.T) {
	buf := uv.NewScreenBuffer(20, 1)
	uv.NewStyledString("e\u0301X\x1b]8;;https://example.org/a;b\x1b\\L\x1b]8;;\x1b\\").Draw(buf, buf.Bounds())
	if c := buf.CellAt(0, 0); c.Content != "e\u0301" || c.Width != 1 {
		t.Errorf("combined cell = %+v", c)
	}
	if c := buf.CellAt(1, 0); c.Content != "X" {
		t.Errorf("following cell = %+v", c)
	}
	if c := buf.CellAt(2, 0); c.Content != "L" || c.Link.URL != "https://example.org/a;b" {
		t.Errorf("hyperlink cell = %+v", c)
	}
}

type hostKeyboardModel struct{}

func (hostKeyboardModel) Init() tea.Cmd                         { return nil }
func (m hostKeyboardModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (hostKeyboardModel) View() tea.View {
	v := tea.NewView("keyboard-ready")
	v.AltScreen = true
	v.KeyboardEnhancements.ReportEventTypes = true
	return v
}

type observedOutput struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	once  sync.Once
	ready chan struct{}
}

func (w *observedOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	if strings.Contains(w.buf.String(), "keyboard-ready") {
		w.once.Do(func() { close(w.ready) })
	}
	return n, err
}

// Exiting the host UI must restore the caller's keyboard stack, including when
// that caller was already using Kitty flags. Setting flags to zero is not a
// substitute for restoring the previous entry.
func TestHostKeyboardStackRestoredOnExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	pr, pw := io.Pipe()
	defer func() { _ = pr.Close(); _ = pw.Close() }()
	out := &observedOutput{ready: make(chan struct{})}
	p := tea.NewProgram(hostKeyboardModel{}, tea.WithContext(ctx),
		tea.WithInput(pr), tea.WithOutput(out), tea.WithWindowSize(30, 5),
		tea.WithEnvironment([]string{"TERM=xterm-256color"}), tea.WithoutSignals())
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	select {
	case <-out.ready:
		p.Quit()
	case <-ctx.Done():
		t.Fatal("host renderer did not produce its first frame")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got := out.buf.String() // program and renderer have stopped
	push, pop := strings.Index(got, "\x1b[>3u"), strings.LastIndex(got, "\x1b[<1u")
	if push < 0 || pop <= push || strings.Contains(got, "\x1b[=0;1u") {
		t.Fatalf("host did not preserve the keyboard stack: %q", got)
	}
}

// Use Bubble Tea's real input loop in addition to direct decoder tests: a
// negotiated report must reach the palette as a key, and its release must not
// accept the following suggestion. F12 ends only this test harness.
type paletteKeyboardModel struct{ *model }

func (m paletteKeyboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok && k.Code == tea.KeyF12 {
		return m, tea.Quit
	}
	_, cmd := m.model.Update(msg)
	return m, cmd
}

func TestPaletteCompletionThroughHostInputLoop(t *testing.T) {
	for _, input := range []string{"\t", "\x1b[9u\x1b[9;1:3u", "\x1b[9;129u\x1b[9;129:3u"} {
		t.Run(fmt.Sprintf("%q", input), func(t *testing.T) {
			m := newTestModel(t, 80, 24)
			m.pal.Insert("box a")
			m.sugg = candidates("apply", "attach")
			m.suggSel = 1
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			p := tea.NewProgram(paletteKeyboardModel{m}, tea.WithContext(ctx),
				tea.WithInput(strings.NewReader("\x1b[?31u"+input+"\x1b[24~")),
				tea.WithOutput(io.Discard), tea.WithWindowSize(80, 24),
				tea.WithEnvironment([]string{"TERM=xterm-256color"}), tea.WithoutSignals())
			if _, err := p.Run(); err != nil {
				t.Fatal(err)
			}
			if line, _ := m.pal.Line(); line != "box attach " {
				t.Fatalf("palette = %q, want selected completion", line)
			}
		})
	}
}
