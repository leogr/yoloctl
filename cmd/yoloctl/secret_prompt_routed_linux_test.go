package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// The real routed `account secret set` on a controlling terminal. Enter sends
// exactly the typed value in the private payload and the value is never shown;
// one real Ctrl+C before Enter sends no request, restores the terminal's
// settings, and leaves the next line for the next reader.
func TestSecretSetTerminalRouted(t *testing.T) {
	if mode := os.Getenv("YOLOCTL_TEST_SECRET_PTY"); mode != "" {
		runSecretSetPTYChild(mode)
		return
	}
	for _, mode := range []string{"enter", "ctrlc"} {
		t.Run(mode, func(t *testing.T) { testSecretSetPTY(t, mode) })
	}
}

func testSecretSetPTY(t *testing.T, mode string) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Skipf("PTY unavailable: %v", err)
	}
	defer func() { _ = master.Close(); _ = slave.Close() }()
	before, err := term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	report, childReport, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = report.Close() }()
	child := exec.Command(os.Args[0], "-test.run=^TestSecretSetTerminalRouted$", "-test.timeout=20s")
	child.Env = append(os.Environ(), "YOLOCTL_TEST_SECRET_PTY="+mode, "YOLOCTL_HOME="+t.TempDir(), "YOLOCTL_TUI=", "TERM=xterm-256color")
	child.Stdin, child.Stdout, child.Stderr = slave, slave, slave
	child.ExtraFiles = []*os.File{childReport}
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	_ = childReport.Close()
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	var transcript lockedBuffer
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			_, _ = transcript.Write(buf[:n])
			if err != nil {
				return
			}
		}
	}()
	waitFor := func(text string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !strings.Contains(transcript.String(), text) {
			if time.Now().After(deadline) {
				t.Fatalf("terminal never showed %q; transcript %q", text, transcript.String())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	write := func(b string) {
		t.Helper()
		if _, err := master.Write([]byte(b)); err != nil {
			t.Fatal(err)
		}
	}
	waitFor("Enter the value for TEST (input hidden): ")
	if mode == "enter" {
		write("dummy-secret-123\r")
	} else {
		write("partial\x03")
	}
	waitFor("PUBLIC-DONE-MARKER")
	if mode == "ctrlc" {
		waitFor("READY-NEXT")
		write("next-line\r")
	}
	if err := report.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(report).ReadString('\n')
	if err != nil {
		t.Fatalf("no child report: %v; transcript %q", err, transcript.String())
	}
	switch mode {
	case "enter":
		if strings.TrimSpace(line) != "calls=1 value=dummy-secret-123 err=false canceled=false next=" {
			t.Fatalf("routed payload changed: %q", line)
		}
		if strings.Contains(transcript.String(), "dummy-secret") {
			t.Fatalf("the value was shown on the terminal: %q", transcript.String())
		}
	case "ctrlc":
		if strings.TrimSpace(line) != "calls=0 value= err=true canceled=true next=next-line" {
			t.Fatalf("Ctrl+C sent a request, did not cancel, or ate the next line: %q", line)
		}
	}
	after, err := term.GetState(int(slave.Fd()))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("terminal settings not restored: %v", err)
	}
}

func runSecretSetPTYChild(mode string) {
	report := os.NewFile(3, "secret-pty-report")
	ctx, stop := commandSignalContext()
	defer stop()
	calls, value := 0, ""
	controllerTargetForCommand = func(context.Context, *cobra.Command) (controllerTarget, error) {
		return controllerTarget{Space: "work", Call: func(_ context.Context, req controller.Request, _ controller.ClientIO) error {
			calls++
			var secret controllerSecret
			_ = json.Unmarshal(req.Payload, &secret)
			value = secret.Value
			return nil
		}}, nil
	}
	root := newClientRootCmd()
	root.SetArgs([]string{"account", "secret", "set", "main", "TEST"})
	err := root.ExecuteContext(ctx)
	fmt.Print("\r\nPUBLIC-DONE-MARKER\r\n")
	next := ""
	if mode == "ctrlc" {
		fmt.Print("READY-NEXT\r\n")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		next = strings.TrimRight(line, "\r\n")
	}
	_, _ = fmt.Fprintf(report, "calls=%d value=%s err=%v canceled=%v next=%s\n", calls, value, err != nil, errors.Is(ctx.Err(), context.Canceled), next)
	_ = report.Close()
	os.Exit(0)
}
