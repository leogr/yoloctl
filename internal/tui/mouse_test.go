package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/terminal"
	"github.com/leogr/yoloctl/internal/tui/term"
)

func testSGRMouse(t *testing.T, mo tea.Mouse, motion, release bool, rows int) string {
	t.Helper()
	vt, err := terminal.New(rows, 80, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vt.Close() })
	_, _ = vt.Write([]byte("\x1b[?1003h\x1b[?1006h"))
	action := terminal.MousePress
	if motion {
		action = terminal.MouseMotion
	} else if release {
		action = terminal.MouseRelease
	}
	event, ok := mouseEvent(mo, barRows, 0, action)
	if !ok {
		return ""
	}
	p, err := vt.EncodeMouse(event)
	if err != nil {
		t.Fatal(err)
	}
	return string(p)
}

// An actual raw PTY peer records exact input, including the absence of reports.
// The sentinel goes through the same ordered queue after the tested events.
const captureMouseScript = `import os,sys,tty
tty.setraw(0)
os.write(1,b'READY')
data=b''
end=b'\x00YOLOCTL_MOUSE_END\x00'
while not data.endswith(end):
 data+=os.read(0,65536)
open(sys.argv[1],'wb').write(data[:-len(end)])
`

func mouseCapture(t *testing.T, m *model, modes string) (*term.Session, func() string) {
	t.Helper()
	path := t.TempDir() + "/input"
	s, err := term.Start(term.Options{Rows: m.viewRows(), Cols: m.width, Argv: []string{
		"python3", "-c", captureMouseScript, path}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(s.GridText(), "READY") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(s.GridText(), "READY") {
		t.Fatal("mouse PTY did not become ready")
	}
	_, _ = s.Write([]byte(modes))
	m.tabs.Add(testTab(s, "mouse-test", false))
	m.focusPalette = false
	return s, func() string {
		t.Helper()
		if err := s.SendInput([]byte("\x00YOLOCTL_MOUSE_END\x00")); err != nil {
			t.Fatal(err)
		}
		if err := s.Wait(5 * time.Second); err != nil {
			t.Fatal(err)
		}
		p, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(p)
	}
}

func TestMouseGestureOwnership(t *testing.T) {
	const mode = "\x1b[?1002h\x1b[?1006h"
	t.Run("consumed press owns drag and release", func(t *testing.T) {
		m := newTestModel(t, 40, 8)
		_, capture := mouseCapture(t, m, mode)
		m.focusPalette = true
		_, _ = m.Update(click(4, 6))
		_, _ = m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: 5, Y: 6})
		_, _ = m.Update(releaseAt(5, 6))
		if got := capture(); got != "" {
			t.Fatalf("orphan drag reached child: %q", got)
		}
	})
	t.Run("release above child", func(t *testing.T) {
		m := newTestModel(t, 40, 8)
		_, capture := mouseCapture(t, m, mode)
		_, _ = m.Update(click(4, 4))
		_, _ = m.Update(releaseAt(4, 0))
		if got, want := capture(), "\x1b[<0;5;3M\x1b[<0;5;1m"; got != want {
			t.Fatalf("gesture = %q, want %q", got, want)
		}
	})
	t.Run("tab switch retains owner", func(t *testing.T) {
		m := newTestModel(t, 40, 8)
		_, first := mouseCapture(t, m, mode)
		_, second := mouseCapture(t, m, mode)
		m.activateTab(0)
		_, _ = m.Update(click(4, 4))
		m.activateTab(1)
		_, _ = m.Update(releaseAt(5, 4))
		if got := first(); got != "\x1b[<0;5;3M\x1b[<0;6;3m" {
			t.Fatalf("first tab = %q", got)
		}
		if got := second(); got != "" {
			t.Fatalf("release leaked to second tab: %q", got)
		}
	})
	t.Run("resize clamps release to owner", func(t *testing.T) {
		m := newTestModel(t, 40, 8)
		_, capture := mouseCapture(t, m, mode)
		_, _ = m.Update(click(39, 7))
		_, _ = m.Update(tea.WindowSizeMsg{Width: 20, Height: 5})
		_, _ = m.Update(releaseAt(39, 0))
		if got := capture(); got != "\x1b[<0;40;6M\x1b[<0;20;1m" {
			t.Fatalf("resized gesture = %q", got)
		}
	})
}

func TestMouseLegacyClient(t *testing.T) {
	m := newTestModel(t, 40, 8)
	_, capture := mouseCapture(t, m, "\x1b[?1000h")
	_, _ = m.Update(click(4, 4))
	_, _ = m.Update(releaseAt(4, 4))
	// xterm normal tracking, zero-based x=4/y=2: Cb+32, Cx+33, Cy+33.
	if got, want := capture(), "\x1b[M %#\x1b[M#%#"; got != want {
		t.Fatalf("legacy mouse = %q, want %q", got, want)
	}
}

func TestMouseExtraButtonsKeepTheirGesture(t *testing.T) {
	m := newTestModel(t, 40, 8)
	_, first := mouseCapture(t, m, "\x1b[?1002h\x1b[?1006h")
	_, second := mouseCapture(t, m, "\x1b[?1002h\x1b[?1006h")
	m.activateTab(0)
	_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseButton10, X: 4, Y: 4})
	_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseButton11, X: 5, Y: 4})
	m.activateTab(1)
	_, _ = m.Update(tea.MouseMotionMsg{Button: tea.MouseButton10, X: 6, Y: 4})
	_, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseNone, X: 7, Y: 4})
	const want = "\x1b[<130;5;3M\x1b[<131;6;3M\x1b[<162;7;3M\x1b[<130;8;3m\x1b[<131;8;3m"
	if got := first(); got != want {
		t.Fatalf("extra button gesture = %q, want %q", got, want)
	}
	if got := second(); got != "" {
		t.Fatalf("gesture redirected to current tab: %q", got)
	}
}

func TestMouseButtonsRetainIndependentOwners(t *testing.T) {
	m := newTestModel(t, 40, 8)
	_, first := mouseCapture(t, m, "\x1b[?1002h\x1b[?1006h")
	_, second := mouseCapture(t, m, "\x1b[?1002h\x1b[?1006h")
	m.activateTab(0)
	_, _ = m.Update(click(1, 3))
	m.activateTab(1)
	_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseRight, X: 2, Y: 3})
	// Legacy host releases omit the button; each original owner is completed.
	_, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseNone, X: 3, Y: 3})
	if got := first(); got != "\x1b[<0;2;2M\x1b[<0;4;2m" {
		t.Fatalf("left owner = %q", got)
	}
	if got := second(); got != "\x1b[<2;3;2M\x1b[<2;4;2m" {
		t.Fatalf("right owner = %q", got)
	}
}

func TestClosedMouseOwnerDoesNotRetargetRelease(t *testing.T) {
	m := newTestModel(t, 40, 8)
	old, _ := mouseCapture(t, m, "\x1b[?1002h\x1b[?1006h")
	_, second := mouseCapture(t, m, "\x1b[?1002h\x1b[?1006h")
	m.activateTab(0)
	_, _ = m.Update(click(1, 3))
	old.Close()
	m.activateTab(1)
	_, _ = m.Update(releaseAt(3, 3))
	if got := second(); got != "" {
		t.Fatalf("closed owner's release leaked: %q", got)
	}
}

func TestMouseBusyConsole(t *testing.T) {
	for _, output := range []string{"short", strings.Repeat("old\r\n", 40) + "last", strings.Repeat("old\r\n", 40) + "\x1b[2J\x1b[Hshort"} {
		t.Run(output, func(t *testing.T) {
			m := newTestModel(t, 40, 12)
			path := t.TempDir() + "/input"
			started, err := m.console.Run(nil, "$ test", "python3", "-c", captureMouseScript, path)
			if err != nil || !started {
				t.Fatalf("console start = %v, %v", started, err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for !strings.Contains(m.console.GridText(), "READY") && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if !strings.Contains(m.console.GridText(), "READY") {
				t.Fatal("console PTY not ready")
			}
			_, _ = m.console.Write([]byte("\x1b[2J\x1b[H" + output + "\x1b[?1003h\x1b[?1006h"))
			if got := m.wantedMouseTracking(); got != 1003 {
				t.Fatalf("busy console mode = %v", got)
			}
			liveY := 1
			shrink := strings.Contains(output, "\x1b[2J")
			if shrink {
				liveY = m.console.Frame().Rows
			} // history fills the preceding rows
			_, _ = m.Update(click(4, liveY))
			_, _ = m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: 5, Y: liveY})
			want := "\x1b[<0;5;1M\x1b[<32;6;1M\x1b[<0;6;1m"
			if shrink {
				_, _ = m.console.Write([]byte("\r\nnew")) // grid origin moves up one displayed row
				_, _ = m.Update(releaseAt(5, liveY))
				want = "\x1b[<0;5;1M\x1b[<32;6;1M\x1b[<0;6;2m"
			} else {
				m.focusPalette = false // leaving the overlay must still complete its gesture
				_, _ = m.Update(releaseAt(5, 0))
			}
			if err := m.console.SendInput([]byte("\x00YOLOCTL_MOUSE_END\x00")); err != nil {
				t.Fatal(err)
			}
			deadline = time.Now().Add(5 * time.Second)
			for m.console.Busy() && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if m.console.Busy() {
				t.Fatal("console PTY did not exit")
			}
			p, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(p); got != want {
				t.Fatalf("console mouse = %q, want %q", got, want)
			}
		})
	}
}
