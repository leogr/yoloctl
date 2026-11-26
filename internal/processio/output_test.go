package processio

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestCaptureStopsWaitingForInheritedOutput(t *testing.T) {
	for _, test := range []struct {
		name  string
		drain time.Duration
		want  error
	}{
		{"drain timeout", 15 * time.Millisecond, exec.ErrWaitDelay},
		{"caller deadline", 0, context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			input, release, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = input.Close(); _ = release.Close() }()
			// The foreground child exits immediately. Its descendant keeps
			// stderr open until the test closes the inherited private input.
			cmd := exec.CommandContext(ctx, "sh", "-c", `printf leader >&2; cat <&3 >&2 &`)
			cmd.ExtraFiles = []*os.File{input}
			var out bytes.Buffer
			err = Capture(ctx, cmd, &out, false, test.drain)
			if !errors.Is(err, test.want) || out.String() != "leader" {
				t.Fatalf("output %q, error %v, want %v", out.String(), err, test.want)
			}
		})
	}
}
