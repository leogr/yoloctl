package prettylog

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestGroupedStepLinesKeepLiveOutputAndPublishParentFirst(t *testing.T) {
	var out strings.Builder
	l := New(&out, Options{Format: FormatPretty, Verbosity: Trace, GroupStepCompletions: true})
	root := l.StartStep("Starting space")
	parent := root.Step("Starting controller")
	install := parent.Step("Installing controller binary")
	deploy := parent.Step("Deploying controller")
	_, _ = io.WriteString(install.Detail(), "streamed debug detail\n")
	deploy.Done()
	install.Done()
	parent.Step("Cleaning old binary").Skip("already removed")
	parent.Done()
	l.Slog().Warn("streamed warning")
	got := out.String()
	if !strings.Contains(got, "Installing controller binary") || !strings.Contains(got, "streamed debug detail") || !strings.Contains(got, "streamed warning") || strings.Contains(got, "✓") || strings.Contains(got, "already removed") {
		t.Fatalf("unexpected progress before the root ends:\n%s", got)
	}
	out.Reset()
	root.Done()
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	want := []string{"✓ Starting space", "  ✓ Starting controller", "    ✓ Deploying controller", "    ✓ Installing controller binary", "    " + DefaultGlyphs.Skipped + " Cleaning old binary"}
	if len(lines) != len(want) {
		t.Fatalf("expected one line per completion:\n%s", out.String())
	}
	for i, prefix := range want {
		if !strings.HasPrefix(lines[i], prefix) {
			t.Fatalf("expected line %d to start with %q:\n%s", i, prefix, out.String())
		}
	}
}

func TestGroupedStepLinesShowFailureAndUncertaintyBeforeParentEnds(t *testing.T) {
	var out strings.Builder
	l := New(&out, Options{Format: FormatPlain, Verbosity: Errors, GroupStepCompletions: true})
	root := l.StartStep("Upgrading space")
	root.Step("Pulling image").Fail(errors.New("image pull refused"))
	root.Step("Checking receipt").Unknown(errors.New("receipt unavailable"))
	got := out.String()
	for _, want := range []string{"error: Pulling image: failed", "image pull refused", "uncertain: Checking receipt", "receipt unavailable"} {
		if strings.Count(got, want) != 1 {
			t.Fatalf("missing or repeated %q before the parent ends:\n%s", want, got)
		}
	}
	root.Done()
	if out.String() != got {
		t.Fatalf("parent completion repeated the child diagnostics:\n%s", out.String())
	}
}

func TestGroupedStepLinesCloseKeepsIncompleteGroupOutput(t *testing.T) {
	var out strings.Builder
	l := New(&out, Options{Format: FormatPlain, Verbosity: Normal, GroupStepCompletions: true})
	root := l.StartStep("Parent still running")
	child := root.Step("Completed child")
	child.Done()
	if got := strings.Count(out.String(), "Completed child"); got != 1 {
		t.Fatalf("expected only the progress line before close: %s", out.String())
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(out.String(), "Completed child"); got != 2 {
		t.Fatalf("completion lost at close: %s", out.String())
	}
}
