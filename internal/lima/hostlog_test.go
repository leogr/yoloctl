package lima

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/layout"
)

func diagnosticRecord(operation, text, space string) string {
	return "2026-11-14T19:30:00.123Z " + strconv.Quote(operation) + " " + strconv.QuoteToASCII(text) + " space=" + strconv.Quote(space) + "\n"
}

func TestHostDiagnosticParserLegacyAndSpace(t *testing.T) {
	legacy := "2026-11-14T19:30:00.123Z \"limactl start\" \"hello\\n\\x1b\"\n"
	for _, line := range []string{legacy, diagnosticRecord("limactl start", "hello\n\x1b", "alpha")} {
		r, ok := ParseHostDiagnostic(line)
		if !ok || r.Operation != "limactl start" || r.Text != "hello\n\x1b" || r.Time.Nanosecond() != 123000000 {
			t.Fatal(r, ok)
		}
		if line != legacy && r.Space != "alpha" {
			t.Fatal(r)
		}
	}
	for _, line := range []string{"bad", strings.TrimSuffix(legacy, "\n") + " unexpected", "2026-11-14T19:30:00Z \"a\"\"b\"", diagnosticRecord("start", "one", "alpha") + "x"} {
		if _, ok := ParseHostDiagnostic(line); ok {
			t.Fatalf("accepted unknown grammar %q", line)
		}
	}
}

func hostLogFixture(t *testing.T, previous, current string) string {
	t.Helper()
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	path, err := layout.HostDiagnosticsLogPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(current), 0600); err != nil {
		t.Fatal(err)
	}
	if previous != "" {
		if err := os.WriteFile(path+".1", []byte(previous), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestHostDiagnosticsMatchingTailAcrossRotation(t *testing.T) {
	legacy := "2026-11-14T19:30:00Z \"old\" \"alpha text isn't context\"\n"
	a := diagnosticRecord("old alpha", "one", "alpha")
	b := diagnosticRecord("new alpha", "two", "alpha")
	other := diagnosticRecord("other", "alpha", "beta")
	hostLogFixture(t, legacy+a, other+b)
	for _, tc := range []struct {
		space string
		lines int
		want  string
	}{{"alpha", 1, b}, {"alpha", 2, a + b}, {"beta", 10, other}, {"", 10, legacy + a + other + b}, {"alpha", 0, ""}, {"gone", 10, ""}} {
		var got bytes.Buffer
		err := TailHostDiagnostics(t.Context(), tc.space, tc.lines, false, func(raw []byte) error { _, e := got.Write(raw); return e })
		if err != nil || got.String() != tc.want {
			t.Fatalf("%+v: %v %q", tc, err, got.String())
		}
	}
}

func TestHostDiagnosticsFollowDrainsAndChangesGeneration(t *testing.T) {
	first := diagnosticRecord("first", "one", "alpha")
	path := hostLogFixture(t, "", first)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	records := make(chan string, 8)
	done := make(chan error, 1)
	go func() {
		done <- TailHostDiagnostics(ctx, "alpha", 1, true, func(raw []byte) error { records <- string(raw); return nil })
	}()
	next := func(want string) {
		t.Helper()
		select {
		case got := <-records:
			if got != want {
				t.Fatalf("got %q want %q", got, want)
			}
		case err := <-done:
			t.Fatalf("reader ended: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("log follower did not produce record")
		}
	}
	next(first)
	second := diagnosticRecord("retired", "two", "alpha")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(second); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	third := diagnosticRecord("new", "three", "alpha")
	if err := os.WriteFile(path, []byte(third), 0600); err != nil {
		t.Fatal(err)
	}
	next(second)
	next(third)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not cancel")
	}
}

func TestHostDiagnosticsWriterAndUnsafeFiles(t *testing.T) {
	path := hostLogFixture(t, "", diagnosticRecord("start", "one", "alpha"))
	failure := errors.New("sink failed")
	if err := TailHostDiagnostics(t.Context(), "", 1, false, func([]byte) error { return failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := TailHostDiagnostics(t.Context(), "", 1, false, func([]byte) error { t.Fatal("read unsafe current log"); return nil }); err == nil {
		t.Fatal("accepted non-private log")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	if err := TailHostDiagnostics(t.Context(), "", 1, false, func([]byte) error { t.Fatal("read unsafe history"); return nil }); err == nil {
		t.Fatal("accepted linked history")
	}
}

func TestHostDiagnosticsWritesExplicitSpaceWithoutChangingRawBytes(t *testing.T) {
	diagnosticFixture(t, "echo 'warning payload' >&2\nexit 0\n")
	if err := RecordSpaceDiagnostics("alpha", "chat catalog", []byte("line\n\x1b\xff"), nil); err != nil {
		t.Fatal(err)
	}
	line := readDiagnostics(t)
	r, ok := ParseHostDiagnostic(line)
	if !ok || r.Space != "alpha" || r.Operation != "chat catalog" || r.Text != "line\n\x1b\xff" {
		t.Fatal(r, ok, line)
	}
	if strings.ContainsRune(line, '\x1b') {
		t.Fatal("raw terminal control in stored file")
	}
}

func TestHostDiagnosticsLaunchCarriesItsExplicitSpace(t *testing.T) {
	c := diagnosticFixture(t, "echo warning-payload >&2\nexit 0\n")
	if err := c.Start(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
	r, ok := ParseHostDiagnostic(readDiagnostics(t))
	if !ok || r.Space != "alpha" || r.Operation != "limactl start" || !strings.Contains(r.Text, "warning-payload") {
		t.Fatal(r, ok)
	}
}
