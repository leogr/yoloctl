package main

import (
	"bufio"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
)

// promptProbe records whether echo was already off on the terminal when the
// prompt was written.
type promptProbe struct {
	fd      int
	mu      sync.Mutex
	written strings.Builder
	echoOff chan bool
}

func (p *promptProbe) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.written.Write(b)
	if strings.Contains(string(b), "input hidden") {
		tios, err := unix.IoctlGetTermios(p.fd, unix.TCGETS)
		p.echoOff <- err == nil && tios.Lflag&unix.ECHO == 0
	}
	return len(b), nil
}

func secretPromptTerminal(t *testing.T) (*cobra.Command, *promptProbe, func([]byte), func() string) {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Skip("no pty:", err)
	}
	t.Cleanup(func() { _ = master.Close(); _ = slave.Close() })
	probe := &promptProbe{fd: int(slave.Fd()), echoOff: make(chan bool, 1)}
	cmd := &cobra.Command{}
	cmd.SetIn(slave)
	cmd.SetErr(probe)
	echoed := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		var all strings.Builder
		for {
			n, err := master.Read(buf)
			all.Write(buf[:n])
			if err != nil {
				echoed <- all.String()
				return
			}
		}
	}()
	write := func(b []byte) {
		if _, err := master.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	// readEchoed closes the terminal and returns everything it echoed.
	readEchoed := func() string {
		_ = slave.Close()
		_ = master.Close()
		select {
		case s := <-echoed:
			return s
		case <-time.After(2 * time.Second):
			t.Fatal("terminal output was not collected; the no-echo check cannot pass on an empty transcript")
			return ""
		}
	}
	if tios, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS); err != nil || tios.Lflag&unix.ECHO == 0 {
		t.Fatal("fixture terminal does not start with echo on", err)
	}
	return cmd, probe, write, readEchoed
}

func echoOn(t *testing.T, cmd *cobra.Command) bool {
	t.Helper()
	tios, err := unix.IoctlGetTermios(int(cmd.InOrStdin().(interface{ Fd() uintptr }).Fd()), unix.TCGETS)
	return err == nil && tios.Lflag&unix.ECHO != 0
}

// On a terminal the value is read after a prompt, with echo already off when
// the prompt appears: the exact typed value is returned, never echoed, and the
// terminal's settings are restored afterwards.
func TestSecretValueOnTerminalPromptsWithEchoOff(t *testing.T) {
	cmd, probe, write, readEchoed := secretPromptTerminal(t)
	cmd.SetContext(t.Context())
	fd := int(cmd.InOrStdin().(interface{ Fd() uintptr }).Fd())
	flagsBefore, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		value string
		err   error
	}
	done := make(chan result, 1)
	go func() {
		v, err := readCredentialValue(cmd, "", "TEST")
		done <- result{v, err}
	}()
	select {
	case off := <-probe.echoOff:
		if !off {
			t.Fatal("echo was still on when the prompt was written")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no prompt")
	}
	write([]byte("dummy-secret-123\r"))
	var r result
	select {
	case r = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("value not read")
	}
	if r.err != nil || r.value != "dummy-secret-123" {
		t.Fatalf("value changed: %q %v", r.value, r.err)
	}
	if !echoOn(t, cmd) {
		t.Fatal("terminal echo was not restored")
	}
	if flagsAfter, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0); err != nil || flagsAfter != flagsBefore {
		t.Fatalf("caller's file flags changed: %#x -> %#x %v", flagsBefore, flagsAfter, err)
	}
	probe.mu.Lock()
	shown := probe.written.String()
	probe.mu.Unlock()
	if !strings.Contains(shown, "Enter the value for TEST (input hidden): ") || strings.Contains(shown, "dummy-secret") {
		t.Fatalf("prompt missing or value shown on stderr: %q", shown)
	}
	if echoed := readEchoed(); strings.Contains(echoed, "dummy-secret") {
		t.Fatalf("typed value was echoed: %q", echoed)
	}
}

// A line typed right after the command was cancelled (Ctrl+C handled, poll
// still pending) is not taken as the secret: the read returns the
// cancellation, the terminal is restored, and the next reader gets the whole
// line.
func TestSecretValueOnTerminalCancelBeforeNextLineKeepsIt(t *testing.T) {
	for range 5 {
		cmd, probe, write, _ := secretPromptTerminal(t)
		ctx, cancel := context.WithCancel(t.Context())
		cmd.SetContext(ctx)
		done := make(chan error, 1)
		go func() {
			_, err := readCredentialValue(cmd, "", "TEST")
			done <- err
		}()
		select {
		case <-probe.echoOff:
		case <-time.After(5 * time.Second):
			t.Fatal("no prompt")
		}
		cancel() // the CLI's SIGINT handler, already run
		write([]byte("next-line\r"))
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal("a line after cancellation was taken as the secret", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("the read outlived the cancellation")
		}
		if !echoOn(t, cmd) {
			t.Fatal("terminal echo was not restored")
		}
		line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if err != nil || strings.TrimRight(line, "\r\n") != "next-line" {
			t.Fatalf("the next reader did not get the whole line: %q %v", line, err)
		}
	}
}
