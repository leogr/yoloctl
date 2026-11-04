package lima

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Regression: a command that exits but leaves a daemonized grandchild
// inheriting (and holding open) stderr must NOT keep RunWithFilteredStderr
// blocked. This reproduces the `limactl start` host-agent hang that left
// `yoloctl space start` stuck "running" in the TUI: completion must gate on
// the DIRECT child exiting, not on stderr reaching EOF. `sh` writes a line
// to stderr, backgrounds a long `sleep` (which inherits fd 2), and exits 0;
// the old `cmd.Stderr = filter` path made os/exec spawn a copier that
// cmd.Wait() joined, blocking until the sleep ended.
func TestRunWithFilteredStderrCompletesDespiteDaemonizedGrandchild(t *testing.T) {
	t.Setenv("YOLOCTL_DEBUG", "") // exercise the filtered (pipe) path, not the *os.File passthrough
	var buf bytes.Buffer
	cmd := exec.Command("sh", "-c", "echo hello-stderr 1>&2; sleep 30 & exit 0")

	errc := make(chan error, 1)
	start := time.Now()
	go func() { errc <- RunWithFilteredStderr(cmd, &buf) }()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("RunWithFilteredStderr: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunWithFilteredStderr never returned: a daemonized grandchild holding stderr blocked completion")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("completion took %v; should finish at the direct child's exit, not the grandchild's", elapsed)
	}
	// The direct child's stderr is still captured (and filtered) before the
	// reader is force-stopped.
	if !strings.Contains(buf.String(), "hello-stderr") {
		t.Errorf("captured stderr = %q, want it to contain the child's line", buf.String())
	}
}

func TestFilterStderrTextSuppressesKnownLimaWarning(t *testing.T) {
	t.Setenv("YOLOCTL_DEBUG", "")
	raw := strings.Join([]string{
		`WARN[0000] treating lima version "218b564" as very latest release`,
		"real error: containerd unavailable",
		"",
	}, "\n")
	got := FilterStderrText(raw)
	if strings.Contains(got, "treating lima version") {
		t.Fatalf("known warning was not filtered: %q", got)
	}
	if !strings.Contains(got, "real error: containerd unavailable") {
		t.Fatalf("non-warning stderr was filtered: %q", got)
	}
}

func TestFilterStderrTextDebugPreservesKnownWarning(t *testing.T) {
	t.Setenv("YOLOCTL_DEBUG", "1")
	raw := `WARN[0000] treating lima version "218b564" as very latest release` + "\n"
	if got := FilterStderrText(raw); got != raw {
		t.Fatalf("debug filter = %q, want raw %q", got, raw)
	}
}

func TestStderrLineFilterSuppressesStreamingWarning(t *testing.T) {
	t.Setenv("YOLOCTL_DEBUG", "")
	var out bytes.Buffer
	filter := &stderrLineFilter{dst: &out}
	_, _ = filter.Write([]byte(`time="now" level=warning msg="treating lima version "218b564" as very latest release"` + "\n"))
	_, _ = filter.Write([]byte("real error"))
	filter.Flush()
	got := out.String()
	if strings.Contains(got, "treating lima version") {
		t.Fatalf("streaming warning was not filtered: %q", got)
	}
	if got != "real error" {
		t.Fatalf("streaming non-warning output got %q want %q", got, "real error")
	}
}
