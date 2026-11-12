package term

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/termio"
	xterm "golang.org/x/term"
)

// Real child processes provide the output and retain the slave descriptor.
// Files coordinate phases, so correctness never depends on a guessed sleep.
func TestPTYLifecycleChild(t *testing.T) {
	mode := os.Getenv("YOLOCTL_TEST_LIFECYCLE")
	if mode == "" {
		return
	}
	dir := os.Getenv("YOLOCTL_TEST_LIFECYCLE_DIR")
	mark := func(name, value string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			os.Exit(91)
		}
	}
	waitFile := func(name string) {
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				return
			}
			if time.Now().After(deadline) {
				os.Exit(92)
			}
			time.Sleep(time.Millisecond)
		}
	}
	switch mode {
	case "retained":
		child := exec.Command(os.Args[0], "-test.run=^TestPTYLifecycleChild$")
		child.Env = forceEnv(os.Environ(), "YOLOCTL_TEST_LIFECYCLE=late")
		child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := child.Start(); err != nil {
			os.Exit(93)
		}
		mark("pid", strconv.Itoa(child.Process.Pid))
		waitFile("ready")
		_, _ = os.Stdout.WriteString("OLD_COMMAND\x1b]52;c;")
		os.Exit(0)
	case "late":
		signal.Ignore(syscall.SIGHUP)
		mark("ready", "")
		waitFile("release")
		// Closing the old master may reject this write. Either way it must
		// never reach the replacement command's display or clipboard parser.
		_, _ = os.Stdout.WriteString("T0xEX1RBSUw=\aOLD_TAIL")
		mark("attempted", "")
		os.Exit(0)
	case "burst":
		if _, err := xterm.MakeRaw(int(os.Stdin.Fd())); err != nil {
			os.Exit(94)
		}
		_, _ = os.Stdout.WriteString("BURST_READY")
		var key [1]byte
		if _, err := io.ReadFull(os.Stdin, key[:]); err != nil {
			os.Exit(95)
		}
		for i := range 8192 {
			_, _ = fmt.Fprintf(os.Stdout, "\r\n%06d abcdefghijklmnopqrstuvwxyz", i)
		}
		_, _ = os.Stdout.WriteString("\r\nBURST_DONE")
		if _, err := io.ReadFull(os.Stdin, key[:]); err != nil {
			os.Exit(96)
		}
		_, _ = os.Stdout.WriteString("\x1b[2J\x1b[HFINAL_OUTPUT")
		os.Exit(7)
	default:
		os.Exit(97)
	}
}

func TestConsoleReuseExcludesRetainedDescendantOutput(t *testing.T) {
	dir := t.TempDir()
	c := NewConsole(6, 60, 32, nil)
	t.Cleanup(func() { _ = c.Close() })
	var effectsMu sync.Mutex
	var copies int
	c.SetOSC52Handler(func(_ termio.OSC52) {
		effectsMu.Lock()
		copies++
		effectsMu.Unlock()
	})
	env := append(os.Environ(), "YOLOCTL_TEST_LIFECYCLE=retained", "YOLOCTL_TEST_LIFECYCLE_DIR="+dir)
	if started, err := c.Run(env, "old command", os.Args[0], "-test.run=^TestPTYLifecycleChild$"); err != nil || !started {
		t.Fatalf("start old command: %t %v", started, err)
	}
	// Always clean up the particular descendant created by this fixture.
	t.Cleanup(func() {
		p, _ := os.ReadFile(filepath.Join(dir, "pid"))
		if pid, err := strconv.Atoi(string(p)); err == nil {
			if process, err := os.FindProcess(pid); err == nil {
				_ = process.Kill()
				_ = process.Release()
			}
		}
	})
	awaitInput(t, "old command completed with retained PTY", func() bool { return !c.Busy() })
	if !strings.Contains(c.GridText(), "OLD_COMMAND") {
		t.Fatal("old command did not write its output")
	}
	oldWriter := c.writer
	select {
	case <-oldWriter.done:
	default:
		t.Fatal("completed command retained its input worker")
	}
	for i := range 4 {
		marker := fmt.Sprintf("NEW_COMMAND_%d", i)
		if started, err := c.Run(nil, "new command", "printf", marker); err != nil || !started {
			t.Fatalf("replacement %d: %t %v", i, started, err)
		}
		if i == 0 {
			if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			awaitInput(t, "late descendant attempted output", func() bool {
				_, err := os.Stat(filepath.Join(dir, "attempted"))
				return err == nil
			})
		}
		awaitInput(t, "replacement complete", func() bool { return !c.Busy() })
		got := strings.Join(append(c.Scrollback(), c.GridText()), "\n")
		if !strings.Contains(got, marker) || !strings.Contains(got, "OLD_COMMAND") || strings.Contains(got, "OLD_TAIL") {
			t.Fatalf("replacement lost retained history or accepted late output: %q", got)
		}
	}
	effectsMu.Lock()
	defer effectsMu.Unlock()
	if copies != 0 {
		t.Fatal("old command's unfinished clipboard control crossed into replacement")
	}
}

func TestPTYOutputResizeAndRenderKeepFinalStatus(t *testing.T) {
	s, err := Start(Options{
		Argv: []string{os.Args[0], "-test.run=^TestPTYLifecycleChild$"},
		Env:  append(os.Environ(), "YOLOCTL_TEST_LIFECYCLE=burst"),
		Rows: 8, Cols: 60, ScrollbackLines: 128,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	awaitInput(t, "burst child ready", func() bool { return strings.Contains(s.GridText(), "BURST_READY") })
	if err := s.SendInput([]byte("s")); err != nil {
		t.Fatal(err)
	}
	resized := make(chan error, 1)
	go func() {
		for i := range 120 {
			if err := s.Resize(6+i%5, 48+i%19); err != nil {
				resized <- err
				return
			}
		}
		resized <- nil
	}()
	awaitInput(t, "burst output drained while rendering", func() bool {
		f := s.Frame()
		if f.Rows < 1 || f.Cols < 1 || len(f.Lines) > f.Rows || f.History > 128 {
			t.Fatalf("invalid frame during resize/output: %+v", f)
		}
		if f.Cursor.Visible && (f.Cursor.X < 0 || f.Cursor.X >= f.Cols || f.Cursor.Y < 0 || f.Cursor.Y >= len(f.Lines)) {
			t.Fatalf("cursor outside captured frame: %+v", f)
		}
		return strings.Contains(strings.Join(f.Lines, "\n"), "BURST_DONE")
	})
	if err := <-resized; err != nil {
		t.Fatal(err)
	}
	// The child waits for the final input, so this is a stable tail. Geometry
	// always stays wider than the numbered lines: no reflow interpretation or
	// emulator-generated golden is needed to check loss, duplication or order.
	// Native history evicts whole pages (Ghostty PageList.Limits.enforce), so
	// the configured line limit is a ceiling, not an exact retained length.
	history := s.Scrollback()
	if len(history) > 128 {
		t.Fatalf("retained history=%d exceeds cap 128", len(history))
	}
	if got := s.Frame().History; got != len(history) {
		t.Fatalf("frame history=%d, retained history=%d", got, len(history))
	}
	lines := append(history, strings.Split(s.GridText(), "\n")...)
	var numbers []int
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || line == "BURST_DONE" {
			continue
		}
		number, text, ok := strings.Cut(line, " ")
		n, err := strconv.Atoi(number)
		if !ok || err != nil || len(number) != 6 || text != "abcdefghijklmnopqrstuvwxyz" {
			t.Fatalf("damaged retained line: %q", line)
		}
		numbers = append(numbers, n)
	}
	if len(numbers) == 0 || len(numbers) < len(history) {
		t.Fatalf("retained tail has only %d numbered lines", len(numbers))
	}
	for i, n := range numbers {
		if want := 8192 - len(numbers) + i; n != want {
			t.Fatalf("retained line %d=%d, want %d (lost, duplicated or reordered output)", i, n, want)
		}
	}
	if err := s.Resize(8, 60); err != nil {
		t.Fatal(err)
	}
	if err := s.SendInput([]byte("f")); err != nil {
		t.Fatal(err)
	}
	var exit *exec.ExitError
	if err := s.Wait(5 * time.Second); !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("final status=%v, want 7", err)
	}
	if got := s.GridText(); !strings.Contains(got, "FINAL_OUTPUT") || strings.Count(got, "exit 7") != 1 {
		t.Fatalf("final output/status lost: %q", got)
	}
	select {
	case <-s.writer.done:
	default:
		t.Fatal("finished session retained its input worker")
	}
}
