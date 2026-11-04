package tui

import (
	"context"
	"io"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/terminal"
)

func TestBellsReachOuterTerminalWithoutRedrawEffects(t *testing.T) {
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
	case <-ctx.Done():
		t.Fatal("host renderer did not produce its first frame")
	}
	m := &model{program: p}
	m.handleBellOutput(2)
	m.handleBellOutput(1)
	m.handleBellOutput(0)
	p.Quit()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	vt, err := terminal.New(5, 30, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vt.Close() }()
	if _, err := vt.Write(out.buf.Bytes()); err != nil {
		t.Fatal(err)
	}
	if vt.Bells() != 3 {
		t.Fatalf("outer terminal received %d bells, want three", vt.Bells())
	}
}
