package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/leogr/yoloctl/internal/chatimage"
	"github.com/leogr/yoloctl/internal/tui/term"
)

// frameReader prints one line per image frame it reads from its channel.
const frameReader = `
import os, struct, sys, time
time.sleep(float(sys.argv[1]))
fd = int(os.environ["YOLOCTL_TUI_IMAGE_FD"])
os.set_blocking(fd, True)
f = os.fdopen(fd, "rb", buffering=0)
def exact(n):
    b = b""
    while len(b) < n:
        c = f.read(n - len(b))
        if not c:
            time.sleep(30)
        b += c
    return b
count = 0
while True:
    n = struct.unpack(">I", exact(4))[0]
    exact(n)
    count += 1
    print("frames", count, flush=True)
`

var bigFrame = chatimage.Frame{Op: chatimage.OpReady, Generation: 1, Request: 1, Token: strings.Repeat("t", 64), ID: 1, Cols: 1, Rows: 1}

func startPane(t *testing.T, m *model, opts term.Options) *term.Session {
	t.Helper()
	opts.Rows, opts.Cols = 10, 40
	s, err := term.Start(opts)
	if err != nil {
		if strings.Contains(err.Error(), "/dev/ptmx") {
			t.Skipf("needs a PTY (%v): run with fs.pty.allocate", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// fill writes frames until one meets the full socket of a child that does not
// read; the last write returns at the session's write bound.
func fill(t *testing.T, s *term.Session) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for s.SendImage(bigFrame) == nil {
		if time.Now().After(deadline) {
			t.Fatal("socket never filled")
		}
	}
}

// One pane whose chat stopped reading holds only its own frames: another
// pane's preview frames go through while its writer waits.
func TestAStalledPaneHoldsOnlyItsOwnImages(t *testing.T) {
	m := newTestModel(t, 80, 24)
	stalled := startPane(t, m, term.Options{Argv: []string{"sleep", "30"}, OnImage: m.imageHandler(), ChannelWriteBound: 3 * time.Second})
	healthy := startPane(t, m, term.Options{Argv: []string{"python3", "-c", frameReader, "0"}, OnImage: m.imageHandler()})
	fill(t, stalled)
	if !m.sendImage(stalled, bigFrame) {
		t.Fatal("frame not queued")
	}
	time.Sleep(100 * time.Millisecond) // the stalled pane's writer is in its write
	start := time.Now()
	if !m.sendImage(healthy, bigFrame) {
		t.Fatal("frame not queued")
	}
	waitFor(t, func() bool { return strings.Contains(healthy.GridText(), "frames 1") })
	if took := time.Since(start); took > time.Second {
		t.Fatalf("the healthy pane's frame waited %s behind the stalled pane", took)
	}
}

// A chat stopped for longer than the old 1 s bound (here 3 s, for example
// with Ctrl+Z) keeps its channel: the write that waited completes when it
// reads again.
func TestAPausedChatKeepsItsPreviews(t *testing.T) {
	m := newTestModel(t, 80, 24)
	s := startPane(t, m, term.Options{Argv: []string{"python3", "-c", frameReader, "3"}, OnImage: m.imageHandler()})
	deadline := time.Now().Add(20 * time.Second)
	for {
		start := time.Now()
		err := s.SendImage(bigFrame)
		took := time.Since(start)
		if err != nil {
			t.Fatalf("write failed after %s: %v", took, err)
		}
		if took > 2*time.Second {
			break // this write waited for the paused reader and completed
		}
		if time.Now().After(deadline) {
			t.Fatal("no write ever waited for the paused reader")
		}
	}
	if !m.sendImage(s, bigFrame) {
		t.Fatal("channel closed after a pause")
	}
}

// A step click reaches a pane whose image reader has stopped: the two
// channels have separate writers.
func TestStepClicksPassAStalledImageReader(t *testing.T) {
	m := newTestModel(t, 80, 24)
	s := startPane(t, m, term.Options{StepClicks: true, OnImage: m.imageHandler(), ChannelWriteBound: 3 * time.Second, Argv: []string{"sh", "-c",
		`printf 'a\nb\nc'; while ! read l <&"$YOLOCTL_TUI_STEP_FD" 2>/dev/null; do sleep 0.05; done; printf '\ngot %s\n' "$l"; sleep 5`}})
	tab := testTab(s, "yoloctl box create api", false)
	m.tabs.Add(tab)
	m.layoutTab(tab)
	m.focusPalette = false
	waitFor(t, func() bool { return s.Frame().Cursor.Y == 2 && strings.Contains(paneText(s), "c") })
	fill(t, s)
	if !m.sendImage(s, bigFrame) {
		t.Fatal("frame not queued")
	}
	time.Sleep(100 * time.Millisecond) // the image writer is in its write
	r := tab.Focused().Rect()
	y := m.tabTop() + r.Y
	start := time.Now()
	_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: r.X + 1, Y: y})
	_, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: r.X + 1, Y: y})
	waitFor(t, func() bool { return strings.Contains(paneText(s), "got -2") })
	if took := time.Since(start); took > time.Second {
		t.Fatalf("the step click waited %s behind the image writer", took)
	}
}

// A click on a pane whose command stopped reading its step channel never
// holds the event loop: it waits in that pane's queue.
func TestAStepClickNeverHoldsTheEventLoop(t *testing.T) {
	m := newTestModel(t, 80, 24)
	s := startPane(t, m, term.Options{StepClicks: true, ChannelWriteBound: 3 * time.Second, Argv: []string{"sh", "-c", `printf 'a\nb\nc'; sleep 30`}})
	tab := testTab(s, "yoloctl box create api", false)
	m.tabs.Add(tab)
	m.layoutTab(tab)
	m.focusPalette = false
	waitFor(t, func() bool { return s.Frame().Cursor.Y == 2 && strings.Contains(paneText(s), "c") })
	deadline := time.Now().Add(20 * time.Second)
	for s.SendStepClick(-1) == nil {
		if time.Now().After(deadline) {
			t.Fatal("step socket never filled")
		}
	}
	r := tab.Focused().Rect()
	y := m.tabTop() + r.Y
	start := time.Now()
	_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: r.X + 1, Y: y})
	_, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: r.X + 1, Y: y})
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("the click held the event loop for %s", took)
	}
}
