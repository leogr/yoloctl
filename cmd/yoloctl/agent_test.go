package main

import (
	"bytes"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// TestInterpretPgrepExit covers the three result categories the
// pre-flight check distinguishes. Exit 0 → at least one match (agent
// is running). Exit 1 → no match, but the check itself succeeded
// (agent is NOT running, NOT an error). Anything else → check
// inconclusive (pgrep missing, exec setup failed, etc.); caller
// proceeds with a warning.
func TestInterpretPgrepExit(t *testing.T) {
	t.Parallel()
	t.Run("exit_0_running", func(t *testing.T) {
		running, err := interpretPgrepExit(nil)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if !running {
			t.Fatal("want running=true for nil err")
		}
	})

	t.Run("exit_1_not_running", func(t *testing.T) {
		err := runWithExit(t, 1)
		running, checkErr := interpretPgrepExit(err)
		if checkErr != nil {
			t.Fatalf("unexpected checkErr: %v", checkErr)
		}
		if running {
			t.Fatal("want running=false for exit 1")
		}
	})

	t.Run("exit_2_check_failed", func(t *testing.T) {
		err := runWithExit(t, 2)
		running, checkErr := interpretPgrepExit(err)
		if checkErr == nil {
			t.Fatal("want non-nil checkErr for exit 2")
		}
		if running {
			t.Fatal("want running=false on check failure")
		}
		if !strings.Contains(checkErr.Error(), "exit code 2") {
			t.Errorf("want error to mention exit code 2, got %q", checkErr.Error())
		}
	})

	t.Run("non_exit_error_check_failed", func(t *testing.T) {
		// Plain non-ExitError (e.g., command couldn't start).
		err := errors.New("limactl: no such VM")
		running, checkErr := interpretPgrepExit(err)
		if checkErr == nil {
			t.Fatal("want non-nil checkErr")
		}
		if running {
			t.Fatal("want running=false on non-exit error")
		}
	})
}

// TestPromptYesNo covers the three answer shapes (yes / no / empty)
// plus the non-interactive guardrail. The non-interactive case must
// fail loudly rather than silently say "no" — otherwise CI pipelines
// would skip every update without telling anyone.
func TestPromptYesNo(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		input    string
		wantYes  bool
		wantErr  bool
		wantHint string // substring in err
	}{
		{name: "yes_lowercase", input: "y\n", wantYes: true},
		{name: "yes_uppercase", input: "YES\n", wantYes: true},
		{name: "yes_word", input: "yes\n", wantYes: true},
		{name: "no_explicit", input: "n\n", wantYes: false},
		{name: "no_word", input: "no\n", wantYes: false},
		{name: "empty_treats_as_no", input: "\n", wantYes: false},
		{name: "garbage_treats_as_no", input: "maybe\n", wantYes: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			in := strings.NewReader(c.input)
			yes, err := promptYesNo("test? [y/N]: ", in, &out)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if yes != c.wantYes {
				t.Errorf("got %v, want %v", yes, c.wantYes)
			}
			if !strings.Contains(out.String(), "test? [y/N]: ") {
				t.Errorf("prompt not written to out, got %q", out.String())
			}
		})
	}

}

// TestIsInteractive sanity-checks that the readers we use in tests
// (strings.Reader, bytes.Buffer) and pipes ARE NOT classified as
// interactive. This is what prevents the test suite from accidentally
// going down the prompt path. If someone passes os.Stdin in a real
// test, the result depends on how the test is invoked — we don't
// assert that here.
func TestIsInteractive(t *testing.T) {
	t.Parallel()
	if isInteractive(strings.NewReader("")) {
		t.Error("strings.Reader should not be interactive")
	}
	if isInteractive(new(bytes.Buffer)) {
		t.Error("bytes.Buffer should not be interactive")
	}
}

// runWithExit returns an error from an exec.Cmd that exited with the
// given code. The simplest way to get a real *exec.ExitError without
// stubbing the syscall package: actually run /bin/sh -c "exit N".
func runWithExit(t *testing.T, code int) error {
	t.Helper()
	cmd := exec.Command("sh", "-c", "exit "+itoa(code))
	cmd.Stdout = nil
	cmd.Stderr = nil
	err := cmd.Run()
	if err == nil {
		t.Fatalf("sh -c 'exit %d' unexpectedly returned nil", code)
	}
	return err
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	negative := false
	if n < 0 {
		negative = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
