package lima

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/layout"
	"golang.org/x/sys/unix"
)

const noInstanceWarning = "time=\"2026-11-05T18:02:52+01:00\" level=warning msg=\"No instance found. Run \x60limactl create\x60 to create an instance.\""

func diagnosticFixture(t *testing.T, body string) *Client {
	t.Helper()
	base := socketTestRoot(t)
	t.Setenv("YOLOCTL_HOME", base)
	binary := filepath.Join(base, "limactl")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return &Client{binary: binary, stdout: io.Discard}
}

func readDiagnostics(t *testing.T) string {
	t.Helper()
	path, err := layout.HostDiagnosticsLogPath()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func withoutLimaTerminalWrites(t *testing.T, f func()) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan []byte, 1)
	go func() { raw, _ := io.ReadAll(r); done <- raw }()
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr; _ = w.Close(); _ = r.Close() }()
	os.Stdout, os.Stderr = w, w
	f()
	os.Stdout, os.Stderr = oldOut, oldErr
	_ = w.Close()
	if raw := <-done; len(raw) != 0 {
		t.Fatalf("raw terminal output: %q", raw)
	}
}

func TestListWarningNeverWritesTerminal(t *testing.T) {
	for _, debug := range []string{"", "1"} {
		t.Run("debug="+debug, func(t *testing.T) {
			t.Setenv("YOLOCTL_DEBUG", debug)
			t.Setenv("LIMA_TEST_WARNING", noInstanceWarning)
			c := diagnosticFixture(t, "printf '%s\\n' \"$LIMA_TEST_WARNING\" >&2\n")
			home, _ := layout.LimaHome()
			// A stale directory can disappear between enumeration and limactl's
			// empty response. That warning must be captured even after a probe.
			if err := os.MkdirAll(filepath.Join(home, "yolo-fixture"), 0700); err != nil {
				t.Fatal(err)
			}
			withoutLimaTerminalWrites(t, func() {
				client := New() // bind the real default sinks while terminal capture is active
				client.binary = c.binary
				instances, err := client.List(t.Context())
				if err != nil || len(instances) != 0 {
					t.Fatalf("empty response: %v %v", instances, err)
				}
			})
			if logged := readDiagnostics(t); !strings.Contains(logged, "No instance found") || !strings.Contains(logged, "limactl list") {
				t.Fatalf("missing logged warning: %q", logged)
			}
		})
	}
}

func TestListSkipsHomesWithoutInstances(t *testing.T) {
	for _, kind := range []string{"absent", "empty", "infrastructure"} {
		t.Run(kind, func(t *testing.T) {
			c := diagnosticFixture(t, "printf called >&2\nexit 39\n")
			home, _ := layout.LimaHome()
			if kind != "absent" {
				if err := os.MkdirAll(home, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "infrastructure" {
				for _, name := range []string{"_disks", "_networks", ".cache"} {
					if err := os.Mkdir(filepath.Join(home, name), 0700); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(home, "calls"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			withoutLimaTerminalWrites(t, func() {
				instances, err := c.List(t.Context())
				if err != nil || len(instances) != 0 {
					t.Fatalf("unexpected execution: %v %v", instances, err)
				}
			})
			path, _ := layout.HostDiagnosticsLogPath()
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("skipped limactl produced a log: %v", err)
			}
		})
	}
	t.Run("uncertain-is-error", func(t *testing.T) {
		c := diagnosticFixture(t, "exit 0\n")
		home, _ := layout.LimaHome()
		if err := os.WriteFile(home, nil, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := c.List(t.Context()); err == nil || !strings.Contains(err.Error(), "inspect Lima home") {
			t.Fatalf("invalid home treated as empty: %v", err)
		}
	})
}

func TestLimaOperationsLogDiagnosticsAndPreserveStdout(t *testing.T) {
	ops := []struct {
		name string
		run  func(context.Context, *Client) error
	}{
		{"create", func(ctx context.Context, c *Client) error { return c.Create(ctx, "fixture", nil) }},
		{"start", func(ctx context.Context, c *Client) error { return c.Start(ctx, "fixture") }},
		{"stop", func(ctx context.Context, c *Client) error { return c.Stop(ctx, "fixture") }},
		{"delete", func(ctx context.Context, c *Client) error { return c.Delete(ctx, "fixture", true) }},
		{"copy", func(ctx context.Context, c *Client) error { return c.CopyTo(ctx, "fixture", "source", "/tmp/target") }},
		{"disk-create", func(ctx context.Context, c *Client) error {
			return c.CreateAccountDisk(ctx, "0123456789abcdef0123456789abcdef", 1<<30)
		}},
		{"disk-delete", func(ctx context.Context, c *Client) error {
			return c.DeleteAccountDisk(ctx, "0123456789abcdef0123456789abcdef")
		}},
		{"disk-list", func(ctx context.Context, c *Client) error {
			_, err := c.AccountDisk(ctx, "0123456789abcdef0123456789abcdef")
			return err
		}},
	}
	for _, op := range ops {
		t.Run(op.name, func(t *testing.T) {
			c := diagnosticFixture(t, "printf 'launcher warning\\n' >&2\nif [ \"$1 $2\" != 'disk list' ]; then printf progress; fi\n")
			var output bytes.Buffer
			c.stdout = &output
			withoutLimaTerminalWrites(t, func() {
				if err := op.run(t.Context(), c); err != nil {
					t.Fatal(err)
				}
			})
			if !strings.Contains(readDiagnostics(t), "launcher warning") {
				t.Fatal("warning lost")
			}
			if op.name != "disk-list" && output.String() != "progress" {
				t.Fatalf("stdout lost: %q", output.String())
			}
		})
	}
}

func TestLimaFailureReturnsSanitizedDiagnosticsAndExitStatus(t *testing.T) {
	c := diagnosticFixture(t, "printf 'Error: denied\\033[31m\\000\\n' >&2\nexit 23\n")
	withoutLimaTerminalWrites(t, func() {
		err := c.Stop(t.Context(), "fixture")
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 23 || !strings.Contains(err.Error(), "denied") || strings.ContainsAny(err.Error(), "\x1b\x00\r\n") {
			t.Fatalf("failure lost/unsafe: %v", err)
		}
	})
	if raw := readDiagnostics(t); !strings.Contains(raw, "denied") || strings.ContainsAny(raw, "\x1b\x00") {
		t.Fatalf("missing/unsafe log: %q", raw)
	}
}

func TestLimaDiagnosticsDoNotWaitForDaemonStderr(t *testing.T) {
	t.Setenv("YOLOCTL_DEBUG", "1")
	c := diagnosticFixture(t, "echo before-daemon >&2\nsleep 5 >/dev/null &\nexit 0\n")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := c.Stop(ctx, "fixture"); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil || !strings.Contains(readDiagnostics(t), "before-daemon") {
		t.Fatal("waited for descendant or lost stderr")
	}
}

func TestHostDiagnosticsRefusePlantedLogAndReportLogFailures(t *testing.T) {
	for _, kind := range []string{"file-link", "directory-link"} {
		t.Run(kind, func(t *testing.T) {
			c := diagnosticFixture(t, "echo warning >&2\n")
			path, _ := layout.HostDiagnosticsLogPath()
			target := filepath.Join(t.TempDir(), "target")
			if kind == "directory-link" {
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Dir(path)); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, []byte("unchanged"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			withoutLimaTerminalWrites(t, func() {
				// The refused log never fails a stop that succeeded, so
				// callers do not fall back to a forced stop.
				if err := c.Stop(t.Context(), "fixture"); err != nil {
					t.Fatalf("log failure failed a successful stop: %v", err)
				}
			})
			failingBinary := filepath.Join(filepath.Dir(c.binary), "limactl-failing")
			if err := os.WriteFile(failingBinary, []byte("#!/bin/sh\necho warning >&2\nexit 7\n"), 0700); err != nil {
				t.Fatal(err)
			}
			failing := &Client{binary: failingBinary, stdout: io.Discard}
			withoutLimaTerminalWrites(t, func() {
				err := failing.Stop(t.Context(), "fixture")
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 7 || !strings.Contains(err.Error(), "recording diagnostics also failed") {
					t.Fatalf("failed stop must keep its error and report the log: %v", err)
				}
			})
			if kind == "file-link" {
				if raw, err := os.ReadFile(target); err != nil || string(raw) != "unchanged" {
					t.Fatalf("followed link: %q %v", raw, err)
				}
			}
		})
	}
}

// host.log keeps at most one previous generation; concurrent writers from
// separate opens never rotate twice or lose the bound, and records stay whole.
func TestHostDiagnosticsLogRotatesWithinItsBound(t *testing.T) {
	base := socketTestRoot(t)
	t.Setenv("YOLOCTL_HOME", base)
	path, err := layout.HostDiagnosticsLogPath()
	if err != nil {
		t.Fatal(err)
	}
	record := bytes.Repeat([]byte("w"), 60<<10)
	done := make(chan error, 8)
	for w := range 8 {
		go func() {
			var err error
			for range 10 {
				if e := LogDiagnostics("writer "+string(rune('a'+w)), record); e != nil && err == nil {
					err = e
				}
			}
			done <- err
		}()
	}
	for range 8 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	total := 0
	for _, name := range []string{path, path + ".1"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(raw) > hostLogLimit {
			t.Fatalf("%s is %d bytes, over %d", name, len(raw), hostLogLimit)
		}
		for _, line := range strings.SplitAfter(string(raw), "\n") {
			if line != "" && (!strings.HasSuffix(line, "\n") || !strings.Contains(line, `"writer `) || len(line) < len(record)) {
				t.Fatalf("%s holds a partial record of %d bytes", name, len(line))
			}
		}
		total += len(raw)
	}
	if entries, err := os.ReadDir(filepath.Dir(path)); err != nil || len(entries) != 2 {
		t.Fatalf("rotation kept %d files: %v", len(entries), err)
	}
	if total < hostLogLimit {
		t.Fatalf("rotation dropped the current generation: %d bytes", total)
	}
}

// A spurious ENOENT from creating host.log (macOS, O_CREAT racing another
// writer's rotation) is retried within the bound; any other open error is
// returned at once, and ENOENT on every attempt is reported.
func TestHostDiagnosticsRetryOnlySpuriousENOENT(t *testing.T) {
	base := socketTestRoot(t)
	t.Setenv("YOLOCTL_HOME", base)
	saved := openHostLog
	t.Cleanup(func() { openHostLog = saved })
	for _, tc := range []struct {
		name  string
		fail  []error
		calls int
		ok    bool
	}{
		{"once", []error{unix.ENOENT}, 2, true},
		{"always", []error{unix.ENOENT, unix.ENOENT, unix.ENOENT, unix.ENOENT}, hostLogAttempts, false},
		{"other", []error{unix.EACCES}, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			openHostLog = func(dir int, name string) (int, error) {
				calls++
				if calls <= len(tc.fail) {
					return -1, tc.fail[calls-1]
				}
				return saved(dir, name)
			}
			err := LogDiagnostics(tc.name, []byte("record "+tc.name))
			if calls != tc.calls || (err == nil) != tc.ok {
				t.Fatalf("calls %d err %v", calls, err)
			}
			if !tc.ok && !errors.Is(err, tc.fail[len(tc.fail)-1]) {
				t.Fatalf("lost the open error: %v", err)
			}
			if tc.ok && !strings.Contains(readDiagnostics(t), "record "+tc.name) {
				t.Fatal("retried append lost the record")
			}
		})
	}
}
