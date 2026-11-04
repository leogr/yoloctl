package tui

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func mouseShiftCommand(t *testing.T, cmd tea.Cmd, capture bool) tea.RawMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("missing host Shift-mouse transition")
	}
	msg, ok := cmd().(tea.RawMsg)
	if !ok || msg.Msg != mouseShiftRequest(capture) {
		t.Fatalf("host command = %#v, want capture=%t", msg, capture)
	}
	return msg
}

func TestHostMouseShiftRequestsAreOrdered(t *testing.T) {
	m := newTestModel(t, 40, 8)
	s, _ := mouseCapture(t, m, "\x1b[?1002h\x1b[?1006h\x1b[>1s")
	first := m.syncMouseShift()
	// Change focus before the async Raw command reaches the event loop.
	m.focusPalette = true
	if next := m.syncMouseShift(); next != nil {
		t.Fatal("concurrent host commands could reorder the capture preference")
	}
	_, next := m.Update(mouseShiftCommand(t, first, true))
	_, next = m.Update(mouseShiftCommand(t, next, false))
	if next != nil {
		t.Fatal("settled host preference generated another command")
	}
	m.focusPalette = false
	_, next = m.Update(mouseShiftCommand(t, m.syncMouseShift(), true))
	if next != nil {
		t.Fatal("focused preference was not acknowledged")
	}
	_, _ = s.Write([]byte("\x1b[>0s"))
	_, next = m.Update(mouseShiftCommand(t, m.syncMouseShift(), false))
	if next != nil {
		t.Fatal("application selection override was not acknowledged")
	}
}

func TestHostMouseShiftGestureSurvivesPaletteFocus(t *testing.T) {
	m := newTestModel(t, 40, 8)
	_, capture := mouseCapture(t, m, "\x1b[?1002h\x1b[?1006h\x1b[>1s")
	_, _ = m.Update(mouseShiftCommand(t, m.syncMouseShift(), true))
	_, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 4, Y: 4, Mod: tea.ModShift})
	if cmd != nil {
		t.Fatal("press changed host capture")
	}
	m.focusPalette = true
	if cmd := m.syncMouseShift(); cmd != nil {
		t.Fatal("palette stole a captured gesture's release")
	}
	_, cmd = m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: 4, Y: 0, Mod: tea.ModShift})
	_, _ = m.Update(mouseShiftCommand(t, cmd, false))
	if got, want := capture(), "\x1b[<4;5;3M\x1b[<4;5;1m"; got != want {
		t.Fatalf("Shift gesture = %q, want %q", got, want)
	}
}

func TestHostMouseShiftBackgroundAndNonReportingRequests(t *testing.T) {
	m := newTestModel(t, 40, 8)
	first, _ := mouseCapture(t, m, "\x1b[?1002h\x1b[>1s")
	second, _ := mouseCapture(t, m, "\x1b[>1s")
	if cmd := m.syncMouseShift(); cmd != nil {
		t.Fatal("background or non-reporting application changed host capture")
	}
	// An explicit zero must be forwarded too, even if yoloctl has not yet
	// touched capture: the host may have a configurable default of true.
	_, _ = second.Write([]byte("\x1b[?1000h\x1b[>0s"))
	_, _ = m.Update(mouseShiftCommand(t, m.syncMouseShift(), false))
	m.activateTab(0)
	_, _ = m.Update(mouseShiftCommand(t, m.syncMouseShift(), true))
	_, _ = first.Write([]byte("\x1bc"))
	_, _ = m.Update(mouseShiftCommand(t, m.syncMouseShift(), false))
	var cleanup bytes.Buffer
	m.resetHostMouse(&cleanup)
	if cleanup.String() != "\x1b[>0s" {
		t.Fatalf("exit cleanup = %q", cleanup.String())
	}
}

// Run the actual Bubble Tea renderer and command loop. The child uses a real
// PTY; the simulated host only records output and supplies palette focus input.
// This proves serialized output/cleanup, not a graphical host's override policy.
func TestHostMouseShiftRendererAndExit(t *testing.T) {
	m := newTestModel(t, 40, 8)
	_, _ = mouseCapture(t, m, "\x1b[?1002h\x1b[?1006h\x1b[>1s")
	pr, pw := io.Pipe()
	defer func() { _ = pr.Close(); _ = pw.Close() }()
	out := &observedOutput{ready: make(chan struct{})}
	p := tea.NewProgram(m, tea.WithInput(pr), tea.WithOutput(out),
		tea.WithWindowSize(40, 8), tea.WithEnvironment([]string{"TERM=xterm-256color"}), tea.WithoutSignals())
	done := make(chan error, 1)
	go func() {
		_, err := p.Run()
		m.resetHostMouse(out)
		done <- err
	}()
	joined := false
	t.Cleanup(func() {
		p.Quit()
		if !joined {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("host mouse test failed to stop its program")
			}
		}
	})
	wait := func(sequence string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			out.mu.Lock()
			found := strings.Contains(out.buf.String(), sequence)
			out.mu.Unlock()
			if found {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("host did not receive %q", sequence)
	}
	wait("\x1b[>1s")
	p.Send(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	wait("\x1b[>0s")
	p.Quit()
	select {
	case err := <-done:
		joined = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("frontend failed to exit")
	}
	got := out.buf.String()
	if !strings.HasSuffix(got, "\x1b[>0s") {
		t.Fatalf("renderer cleanup left host capture active: %q", got)
	}
}
