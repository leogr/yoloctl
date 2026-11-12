package audit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRuleValidationSingleProcessAndDiagnostics(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "falco")
	// The fixed argv is recorded, no collector or supplementary validation pass.
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$0.args\"\nprintf '1 Warnings:\\nunused macro\\n'\nprintf '2 Warnings:\\nunknown source\\n' >&2\nexit 0\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	result, err := ValidateRuleSet(t.Context(), binary, "/fixed config.json")
	if err != nil || result.Warnings != 3 || !strings.Contains(result.Diagnostics, "unknown source") || !strings.Contains(result.Diagnostics, "unused macro") {
		t.Fatal(result, err)
	}
	args, err := os.ReadFile(binary + ".args")
	if err != nil || string(args) != "-c\n/fixed config.json\n--dry-run\n" {
		t.Fatalf("argv %q: %v", args, err)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf 'LOAD_ERR invalid field' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	result, err = ValidateRuleSet(t.Context(), binary, "/fixed.json")
	if err == nil || !strings.Contains(result.Diagnostics, "LOAD_ERR invalid field") {
		t.Fatal("lost rejected diagnostic", result, err)
	}
}

func TestRuleValidationCancellationReapsProcess(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "falco")
	// Only an exec'd direct process: no sleep/retry layer in production.
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s' \"$$\" > \"$0.pid\"\nexec sleep 60\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := ValidateRuleSet(ctx, binary, "/fixed.json"); done <- err }()
	var pid int
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for pid == 0 {
		select {
		case <-deadline.C:
			t.Fatal("validator fixture did not start")
		case <-tick.C:
			raw, _ := os.ReadFile(binary + ".pid")
			if len(raw) > 0 {
				for _, c := range raw {
					pid = pid*10 + int(c-'0')
				}
			}
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-deadline.C:
		t.Fatal("cancelled validator was not reaped")
	}
	if err := unix.Kill(pid, 0); !errors.Is(err, unix.ESRCH) {
		t.Fatalf("validator remains after cancellation: %v", err)
	}
}
