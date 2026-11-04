package term

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/terminal"
	"golang.org/x/sys/unix"
	xterm "golang.org/x/term"
)

func TestPTYGeometryChild(t *testing.T) {
	if os.Getenv("YOLOCTL_TEST_GEOMETRY") != "1" {
		return
	}
	if _, err := xterm.MakeRaw(int(os.Stdin.Fd())); err != nil {
		os.Exit(81)
	}
	for {
		size, err := unix.IoctlGetWinsize(int(os.Stdin.Fd()), unix.TIOCGWINSZ)
		if err != nil {
			os.Exit(82)
		}
		_, _ = fmt.Fprintf(os.Stdout, "\r\nSIZE %d %d %d %d\r\n", size.Row, size.Col, size.Xpixel, size.Ypixel)
		var key [1]byte
		if _, err := io.ReadFull(os.Stdin, key[:]); err != nil || key[0] == 'q' {
			os.Exit(0)
		}
	}
}

func TestSessionPTYUsesMeasuredGridPixels(t *testing.T) {
	s, err := Start(Options{
		Argv: []string{os.Args[0], "-test.run=^TestPTYGeometryChild$"},
		Env:  append(os.Environ(), "YOLOCTL_TEST_GEOMETRY=1"),
		Rows: 24, Cols: 80, CellWidth: 19, CellHeight: 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	awaitInput(t, "initial measured PTY", func() bool { return strings.Contains(s.GridText(), "SIZE 24 80 1520 1008") })
	if err := s.ResizeGeometry(terminal.Geometry{Rows: 24, Cols: 80, CellWidth: 20, CellHeight: 43}); err != nil {
		t.Fatal(err)
	}
	_ = s.SendInput([]byte("s"))
	awaitInput(t, "font-only PTY resize", func() bool { return strings.Contains(s.GridText(), "SIZE 24 80 1600 1032") })
	if err := s.ResizeGeometry(terminal.Geometry{Rows: 25, Cols: 81}); err != nil {
		t.Fatal(err)
	}
	_ = s.SendInput([]byte("s"))
	awaitInput(t, "unknown pixel dimensions", func() bool { return strings.Contains(s.GridText(), "SIZE 25 81 0 0") })
	_ = s.SendInput([]byte("q"))
	if err := s.Wait(3 * time.Second); err != nil {
		t.Fatal(err)
	}
}

// A resize while a session completes never fails: the pump closes the PTY
// after the child exits but before Done, and a resize in that window is a
// local one, as for a completed tab.
func TestResizeWhileTheSessionCompletesIsLocal(t *testing.T) {
	for range 20 {
		s, err := Start(Options{Argv: []string{"true"}, Rows: 24, Cols: 80})
		if err != nil {
			t.Fatal(err)
		}
		<-s.exited
		for rows := 10; ; rows = 10 + (rows+1)%10 {
			if err := s.ResizeGeometry(terminal.Geometry{Rows: rows, Cols: 80}); err != nil {
				t.Fatalf("resize while completing: %v", err)
			}
			select {
			case <-s.Done():
			default:
				continue
			}
			break
		}
		if got := s.Frame().Rows; got < 10 || got > 19 {
			t.Fatalf("a completed session keeps resizing locally: %d rows", got)
		}
		s.Close()
	}
}
