package hostintegration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeTools installs one shell script under every name in a fresh PATH.
func fakeTools(t *testing.T, script string, names ...string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("Linux desktop dispatch; no real desktop is used")
	}
	dir := t.TempDir()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("DISPLAY", "")
}

func withFailSafe(t *testing.T, d time.Duration) {
	t.Helper()
	old := failSafe
	failSafe = d
	t.Cleanup(func() { failSafe = old })
}

// The whole clipboard read had 3 s: a tool that answers after that now
// completes, and only the fail-safe or the caller stops one that hangs.
func TestClipboardReadOutlastsTheOldLimit(t *testing.T) {
	fakeTools(t, "case \"$*\" in *--list-types*) sleep 3.5; printf 'text/plain\\n';; *) printf hello;; esac\n", "wl-paste")
	if _, err := ReadImage(context.Background()); !errors.Is(err, ErrNoImage) {
		t.Fatalf("slow clipboard: %v, want no image after the answer", err)
	}
}

// A tool stopped by the fail-safe reports the read as stopped, never as an
// empty clipboard that would paste the key as text instead.
func TestClipboardFailSafeIsNotAnEmptyClipboard(t *testing.T) {
	fakeTools(t, "exec sleep 30\n", "wl-paste")
	withFailSafe(t, 300*time.Millisecond)
	start := time.Now()
	_, err := ReadImage(context.Background())
	if err == nil || errors.Is(err, ErrNoImage) || !errors.Is(err, context.DeadlineExceeded) || !strings.HasPrefix(err.Error(), "read host clipboard:") {
		t.Fatalf("hung clipboard: %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("fail-safe took %v", took)
	}
}

func TestClipboardCallerCancelStopsTheTool(t *testing.T) {
	fakeTools(t, "exec sleep 30\n", "wl-paste")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	start := time.Now()
	if _, err := ReadImage(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled clipboard read: %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("cancel took %v", took)
	}
}

// A descendant that keeps stdout open past the tool's exit had 1 s; it now
// has the fail-safe, which still ends it.
func TestClipboardPipeWaitOutlastsTheOldDelay(t *testing.T) {
	fakeTools(t, "case \"$*\" in *--list-types*) (sleep 1.5; printf 'text/plain\\n') & ;; *) printf hello;; esac\nexit 0\n", "wl-paste")
	if _, err := ReadImage(context.Background()); !errors.Is(err, ErrNoImage) {
		t.Fatalf("held pipe: %v, want the descendant's answer", err)
	}
	withFailSafe(t, 300*time.Millisecond)
	fakeTools(t, "(sleep 5) &\nexit 0\n", "wl-paste")
	if _, err := ReadImage(context.Background()); err == nil || errors.Is(err, ErrNoImage) {
		t.Fatalf("pipe held past the fail-safe: %v", err)
	}
}

// The opener's 10 s is now the fail-safe; the caller still cancels it.
func TestOpenURLBoundIsTheFailSafe(t *testing.T) {
	fakeTools(t, "sleep 1\n", "xdg-open")
	withFailSafe(t, 5*time.Second)
	if err := OpenURL(context.Background(), "https://example.org/"); err != nil {
		t.Fatalf("slow opener inside the fail-safe: %v", err)
	}
	withFailSafe(t, 300*time.Millisecond)
	fakeTools(t, "exec sleep 30\n", "xdg-open")
	start := time.Now()
	if err := OpenURL(context.Background(), "https://example.org/"); err == nil || !strings.HasPrefix(err.Error(), "open host browser:") {
		t.Fatalf("hung opener: %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("fail-safe took %v", took)
	}
	withFailSafe(t, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	start = time.Now()
	if err := OpenURL(ctx, "https://example.org/"); err == nil {
		t.Fatal("cancelled opener succeeded")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("cancel took %v", took)
	}
}

// Ctrl+C on a paste reaches a read whose tool already exited while a
// descendant still holds its output: the read returns at once, not when the
// pipe wait ends.
func TestClipboardCancelAfterTheToolExited(t *testing.T) {
	fakeTools(t, "(sleep 5) &\nexit 0\n", "wl-paste")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	if _, err := ReadImage(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel after the tool exited: %v", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("cancel took %v while the descendant held the pipe", took)
	}
}
