package tui

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/terminal"
)

func TestPixelMouseRoutesOffsetsOwnersAndDifferentFonts(t *testing.T) {
	m := newTestModel(t, 80, 24)
	s, first := mouseCapture(t, m, "\x1b[?1002h\x1b[?1016h")
	_, second := mouseCapture(t, m, "\x1b[?1002h\x1b[?1006h")
	m.activateTab(0)
	_, _ = m.Update(uv.CellSizeEvent{Width: 19, Height: 42})
	m.hostMouseEncoding.pixels = true
	// Wire coordinates are zero-based; UV still subtracts one.
	_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: -2, Y: 99}) // raw -1,100 -> child -1,16
	m.activateTab(1)
	if err := s.ResizeGeometry(terminal.Geometry{Cols: 80, Rows: 22, CellWidth: 10, CellHeight: 20}); err != nil {
		t.Fatal(err)
	}
	_, _ = m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: 37, Y: 125}) // raw38,126 -> child 20,20
	_, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: -2, Y: 0})  // raw-1,1 -> child -1,-40
	if got, want := first(), "\x1b[<0;-1;16M\x1b[<32;20;20M\x1b[<0;-1;-40m"; got != want {
		t.Fatalf("pixel gesture %q want %q", got, want)
	}
	if got := second(); got != "" {
		t.Fatalf("release leaked to current tab: %q", got)
	}
}

// The actual Bubble Tea reader/renderer plus a scripted outer terminal and a
// real PTY application. This checks the unit-switch boundary on the input
// stream, not just the model's fields or a command's queued return value.
func TestPixelMouseThroughRealHostNegotiation(t *testing.T) {
	m := newTestModel(t, 80, 24)
	m.hostGeometry = hostGeometry{} // Init/WindowSize now run in the real program
	_, capture := mouseCapture(t, m, "\x1b[?1002h\x1b[?1016h")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	r, w := io.Pipe()
	defer func() { _ = r.Close(); _ = w.Close() }()
	out := &observedOutput{ready: make(chan struct{})}
	p := tea.NewProgram(paletteKeyboardModel{m}, tea.WithContext(ctx), tea.WithInput(r), tea.WithOutput(out),
		tea.WithWindowSize(80, 24), tea.WithEnvironment([]string{"TERM=xterm-256color"}), tea.WithoutSignals())
	done := make(chan error, 1)
	go func() { _, err := p.Run(); m.resetHostMouse(out); done <- err }()
	joined := false
	t.Cleanup(func() {
		p.Quit()
		if !joined {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("mouse negotiation program did not exit")
			}
		}
	})
	waitOutput := func(want string) {
		t.Helper()
		for ctx.Err() == nil {
			out.mu.Lock()
			found := strings.Contains(out.buf.String(), want)
			out.mu.Unlock()
			if found {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("host never received %q", want)
	}
	send := func(input string) {
		t.Helper()
		if _, err := io.WriteString(w, input); err != nil {
			t.Fatal(err)
		}
	}
	waitOutput("\x1b[?1016$p")
	waitOutput("\x1b[16t")
	send("\x1b[6;42;19t\x1b[?1016;2$y")
	set := mouseEncodingRequest(true).String()
	waitOutput(set)
	// Acknowledgment precedes zero-origin signed reports on the same pipe.
	send("\x1b[?1016;1$y\x1b[<0;-1;100M\x1b[<0;-1;100m\x19")
	reset := mouseEncodingRequest(false).String()
	waitOutput(reset)
	// Old pixel motion before the boundary remains a pixel event, not keys.
	send("\x1b[<35;-1;100M\x1b[?1016;2$y\x1b[<0;1;1M\x1b[<0;1;1m\x1b[24~")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	joined = true
	if got := capture(); got != "\x1b[<0;-1;16M\x1b[<0;-1;16m" {
		t.Fatalf("end-to-end pixel bytes=%q", got)
	}
	output := out.buf.String()
	start, end := strings.Index(output, set), strings.Index(output, reset)
	if end <= start || strings.Contains(output[start+len(set):end], "\x1b[?1006h") {
		t.Fatal("renderer replaced pixel encoding between negotiations")
	}
	if !strings.HasSuffix(output, mouseTrackingOff+mouseEncodingOff) {
		t.Fatal("host encoding/tracking not reset at exit")
	}
}

func TestMouseEncodingChangesRequireHostAcknowledgment(t *testing.T) {
	m := newTestModel(t, 80, 24)
	_, capture := mouseCapture(t, m, "\x1b[?1000h\x1b[?1016h")
	_, _ = m.Update(uv.CellSizeEvent{Width: 10, Height: 20})
	_, _ = m.Update(m.initHostMouse()())
	_, cmd := m.Update(tea.ModeReportMsg{Mode: ansi.ModeMouseExtSgrPixel, Value: ansi.ModeReset})
	if cmd == nil || !m.hostMouseEncoding.negotiating || m.hostMouseEncoding.pixels {
		t.Fatal("encoding changed without acknowledgment")
	}
	_, _ = m.Update(tea.RawMsg{Msg: mouseEncodingRequest(true)})
	if m.hostMouseEncoding.pixels {
		t.Fatal("queued write was treated as host acknowledgment")
	}
	_, cmd = m.Update(tea.ModeReportMsg{Mode: ansi.ModeMouseExtSgrPixel, Value: ansi.ModeSet})
	if cmd == nil || !m.hostMouseEncoding.pixels || m.hostMouseEncoding.negotiating {
		t.Fatal("host acknowledgment did not activate pixel routing")
	}
	_, _ = m.Update(cmd()) // acknowledge serialized tracking enable
	_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 9, Y: 49})
	m.focusPalette = true
	if cmd := m.syncHostMouseEncoding(); cmd != nil {
		t.Fatal("encoding changed during a captured gesture")
	}
	_, cmd = m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: 9, Y: 49})
	if cmd == nil || !m.hostMouseEncoding.negotiating || !m.hostMouseEncoding.pixels {
		t.Fatal("gesture completion did not start bounded return to cells")
	}
	_, cmd = m.Update(tea.ModeReportMsg{Mode: ansi.ModeMouseExtSgrPixel, Value: ansi.ModeReset})
	if m.hostMouseEncoding.pixels || cmd == nil {
		t.Fatal("cell report did not restore cell routing")
	}
	if got := capture(); got != "\x1b[<0;10;10M\x1b[<0;10;10m" {
		t.Fatalf("pixel gesture = %q", got)
	}
	if m.View().MouseMode != tea.MouseModeNone {
		t.Fatal("Bubble Tea can overwrite our encoding")
	}
}

func TestUnacknowledgedMouseEncodingCannotMisrouteInput(t *testing.T) {
	m := newTestModel(t, 80, 24)
	_, capture := mouseCapture(t, m, "\x1b[?1000h\x1b[?1016h")
	_, _ = m.Update(uv.CellSizeEvent{Width: 10, Height: 20})
	_, _ = m.Update(m.initHostMouse()())
	_, _ = m.Update(tea.ModeReportMsg{Mode: ansi.ModeMouseExtSgrPixel, Value: ansi.ModeReset})
	_, cmd := m.Update(mouseEncodingTimeout(m.hostMouseEncoding.epoch))
	if !m.hostMouseEncoding.failed || cmd == nil || m.status == "" {
		t.Fatal("missing bounded negotiation failure")
	}
	_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 100, Y: 200})
	if got := capture(); got != "" {
		t.Fatalf("unconfirmed units reached application: %q", got)
	}
}
