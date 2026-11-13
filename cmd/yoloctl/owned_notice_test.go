package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/termio"
	"github.com/leogr/yoloctl/pkg/prettylog"
	"golang.org/x/term"
)

func TestAttachedWarningUsesRawTerminalLogBoundary(t *testing.T) {
	for _, level := range []prettylog.Verbosity{prettylog.Normal, prettylog.Errors} {
		master, slave, err := pty.Open()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = master.Close(); _ = slave.Close() })
		state, err := term.MakeRaw(int(slave.Fd()))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = term.Restore(int(slave.Fd()), state) })
		var got string
		captured := make(chan string, 1)
		go func() { text, _ := bufio.NewReader(master).ReadString('\x04'); captured <- text }()
		logger := prettylog.New(slave, prettylog.Options{Format: prettylog.FormatPlain, Verbosity: level})
		restore := logging.SetLogger(logger)
		t.Cleanup(restore)
		restoreRoute := logger.Route(&attachLogWriter{rawTerminalStderr{out: slave}}, nil)
		cliWarn(logging.Session, "clipboard notice")
		restoreRoute()
		_ = logger.Close()
		_, _ = io.WriteString(slave, "\x04")
		select {
		case got = <-captured:
		case <-time.After(5 * time.Second):
			t.Fatal("raw notice did not complete")
		}
		_ = term.Restore(int(slave.Fd()), state)
		_ = slave.Close()
		_ = master.Close()
		want := "\x04"
		if level == prettylog.Normal {
			want = "\r\nwarning: session: clipboard notice\r\n\x04"
		}
		if got != want {
			t.Fatalf("level%d output=%q want=%q", level, got, want)
		}
	}
}

func TestAttachedWarningWireHasNoRawBoundaryBytes(t *testing.T) {
	wire, logger := captureCLIRecords(t)
	var raw bytes.Buffer
	restore := logger.Route(&attachLogWriter{rawTerminalStderr{out: &raw}}, nil)
	defer restore()
	cliWarn(logging.Session, "clipboard notice")
	records := readCLIRecords(t, wire)
	if raw.Len() != 0 || len(records) != 1 || prettylog.StructureOf(records[0]).Actor != logging.Session {
		t.Fatalf("wire records=%v raw=%q", records, raw.String())
	}
}

func TestControllerClipboardNoticesKeepCallerWriterAndStream(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(termio.ViewFDEnv, "")
	t.Setenv("YOLOCTL_TUI", "")
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = master.Close(); _ = slave.Close() }()
	previous := os.Stdout
	os.Stdout = slave
	defer func() { os.Stdout = previous }()
	captured := make(chan string, 1)
	go func() { text, _ := bufio.NewReader(master).ReadString('\x04'); captured <- text }()
	wire, _ := captureCLIRecords(t)
	cmd := newAgentboxExecCmd()
	cmd.SetContext(context.Background())
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(slave)
	var notices bytes.Buffer
	cmd.SetErr(&notices)
	target := controllerTarget{Call: func(_ context.Context, _ controller.Request, streams controller.ClientIO) error {
		_, err := io.WriteString(streams.Out, "before\x1b]52;c;?\aafter\x04")
		return err
	}}
	if err := callControllerTerminal(cmd, target, controller.Request{Method: "box.exec"}); err != nil {
		t.Fatal(err)
	}
	select {
	case text := <-captured:
		if text != "beforeafter\x04" {
			t.Fatalf("clipboard/stream bytes %q", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not complete")
	}
	if wire.Len() != 0 || !strings.Contains(notices.String(), "warning: session: ") || !strings.Contains(notices.String(), "OSC 52 clipboard read blocked; yoloctl never exposes the host clipboard to a box") {
		t.Fatalf("notice=%q processwire=%d", notices.String(), wire.Len())
	}
}
