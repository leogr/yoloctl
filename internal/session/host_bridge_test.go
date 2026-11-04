package session

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type browserBridgeTest struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	reader *bufio.Reader
	err    bytes.Buffer
	waited bool
}

func browserTestDirectory(t *testing.T) string {
	t.Helper()
	// Keep Unix socket paths short on macOS, and never use the live box path.
	dir, err := os.MkdirTemp("/tmp", "yoloctl-browser-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func browserTestCommand(t *testing.T, directory string) *exec.Cmd {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	return exec.CommandContext(ctx, python, "-u", "-c", browserBridge, directory)
}

func startTestBrowserBridge(t *testing.T, directory string) *browserBridgeTest {
	t.Helper()
	b := &browserBridgeTest{cmd: browserTestCommand(t, directory)}
	var err error
	b.in, err = b.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := b.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	b.cmd.Stderr = &b.err
	if err := b.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = b.in.Close()
		if !b.waited {
			_ = b.cmd.Wait()
		}
	})
	b.reader = bufio.NewReader(out)
	line, err := b.reader.ReadBytes('\n')
	var ready struct {
		Directory string `json:"directory"`
	}
	if err != nil || json.Unmarshal(line, &ready) != nil || ready.Directory != directory {
		_ = b.in.Close()
		_ = b.cmd.Wait()
		b.waited = true
		t.Fatalf("ready=%q %v; %s", line, err, b.err.String())
	}
	return b
}

func (b *browserBridgeTest) request(t *testing.T, value string) {
	t.Helper()
	line, err := b.reader.ReadBytes('\n')
	var request struct {
		URL string `json:"url"`
	}
	if err != nil || json.Unmarshal(line, &request) != nil || request.URL != value {
		t.Fatalf("request=%q %v, want %q", line, err, value)
	}
}

func (b *browserBridgeTest) stop(t *testing.T, crash bool) {
	t.Helper()
	if crash {
		_ = b.cmd.Process.Kill()
	}
	_ = b.in.Close()
	err := b.cmd.Wait()
	b.waited = true
	if !crash && err != nil {
		t.Fatalf("bridge exit=%v; %s", err, b.err.String())
	}
}

// Exercise the actual installed helper with stdio discarded, as browser
// launchers commonly do. These are local processes, not an OpenShell VM test.
func TestBrowserBridgeWithoutTerminalOrInheritedOutput(t *testing.T) {
	dir := browserTestDirectory(t)
	b := startTestBrowserBridge(t, dir)
	value := "https://example.org/?a=$(id)&b='quoted'"
	for _, helper := range []string{"open-url", "xdg-open"} {
		child := exec.CommandContext(t.Context(), filepath.Join(dir, helper), value)
		child.Stdin, child.Stdout, child.Stderr = nil, io.Discard, io.Discard
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		b.request(t, value)
		if _, err := io.WriteString(b.in, "{}\n"); err != nil {
			t.Fatal(err)
		}
		if err := child.Wait(); err != nil {
			t.Fatalf("helper: %v", err)
		}
	}
	b.stop(t, false)
	if _, err := os.Stat(filepath.Join(dir, "browser.sock")); !os.IsNotExist(err) {
		t.Fatal("bridge socket survived EOF")
	}
	for _, name := range []string{"open-url", "xdg-open", "bridge.lock"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("stable entry %s removed: %v", name, err)
		}
	}
}

// The same long-lived process keeps its inherited BROWSER across graceful and
// abrupt bridge restarts. Interrupted requests fail; only new requests run.
func TestBrowserBridgeRestartRetainsDaemonEnvironment(t *testing.T) {
	dir := browserTestDirectory(t)
	b := startTestBrowserBridge(t, dir)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	daemon := exec.CommandContext(ctx, b.cmd.Path, "-u", "-c", `
import json, os, subprocess, sys
for url in sys.stdin:
    result = subprocess.run([os.environ["BROWSER"], url.strip()], capture_output=True)
    print(json.dumps({"status": result.returncode, "error": result.stderr.decode()}), flush=True)
`)
	daemon.Env = append(os.Environ(), "BROWSER="+filepath.Join(dir, "open-url"))
	in, _ := daemon.StdinPipe()
	out, _ := daemon.StdoutPipe()
	reader := bufio.NewReader(out)
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close(); _ = daemon.Wait() }()
	readResult := func(wantError string) {
		t.Helper()
		var result struct {
			Status int
			Error  string
		}
		line, err := reader.ReadBytes('\n')
		if err != nil || json.Unmarshal(line, &result) != nil ||
			(result.Status != 0) != (wantError != "") || !strings.Contains(result.Error, wantError) {
			t.Fatalf("daemon result=%q %v; want error %q", line, err, wantError)
		}
	}
	for _, crash := range []bool{false, true} {
		value := fmt.Sprintf("https://example.org/interrupted-%t", crash)
		_, _ = fmt.Fprintln(in, value)
		b.request(t, value)
		b.stop(t, crash)
		readResult("interrupted")
		_, _ = fmt.Fprintln(in, "https://example.org/no-holder")
		readResult("no active managed session")
		b = startTestBrowserBridge(t, dir)
		value = fmt.Sprintf("https://example.org/new-request-%t", crash)
		_, _ = fmt.Fprintln(in, value)
		b.request(t, value) // no interrupted action was retried ahead of this one
		_, _ = io.WriteString(b.in, "{}\n")
		readResult("")
	}
}

func TestBrowserBridgeExclusiveEndpoint(t *testing.T) {
	dir := browserTestDirectory(t)
	b := startTestBrowserBridge(t, dir)
	competing := browserTestCommand(t, dir)
	if out, err := competing.CombinedOutput(); err == nil || !bytes.Contains(out, []byte("another browser bridge is still active")) {
		t.Fatalf("competing bridge: %v %s", err, out)
	}
	child := exec.CommandContext(t.Context(), filepath.Join(dir, "xdg-open"), "https://example.org/still-live")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	b.request(t, "https://example.org/still-live")
	_, _ = io.WriteString(b.in, "{}\n")
	if err := child.Wait(); err != nil {
		t.Fatalf("competing bridge damaged active endpoint: %v", err)
	}
}

func TestBrowserBridgeRejectsUnsafeEndpoints(t *testing.T) {
	for _, name := range []string{"directory symlink", "public directory", "lock symlink", "lock pipe", "socket symlink"} {
		t.Run(name, func(t *testing.T) {
			root := browserTestDirectory(t)
			dir := filepath.Join(root, "bridge")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(root, "untouched")
			if err := os.WriteFile(outside, []byte("untouched"), 0o600); err != nil {
				t.Fatal(err)
			}
			var err error
			switch name {
			case "directory symlink":
				dir = filepath.Join(root, "link")
				err = os.Symlink(filepath.Join(root, "bridge"), dir)
			case "public directory":
				err = os.Chmod(dir, 0o755)
			case "lock symlink":
				err = os.Symlink(outside, filepath.Join(dir, "bridge.lock"))
			case "lock pipe":
				err = unix.Mkfifo(filepath.Join(dir, "bridge.lock"), 0o600)
			case "socket symlink":
				err = os.Symlink(outside, filepath.Join(dir, "browser.sock"))
			}
			if err != nil {
				t.Fatal(err)
			}
			if out, err := browserTestCommand(t, dir).CombinedOutput(); err == nil {
				t.Fatalf("accepted unsafe %s: %s", name, out)
			}
			if got, err := os.ReadFile(outside); err != nil || string(got) != "untouched" {
				t.Fatalf("changed unrelated file: %q %v", got, err)
			}
		})
	}
}

func TestImageUploadScriptCreatesPrivateFileAndCleansPartialWrites(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux guest filesystem contract (/tmp must not be a symlink)")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	for _, complete := range []bool{true, false} {
		// Use a random basename chosen by the test/holder, never caller input.
		dir, err := os.MkdirTemp("/tmp", "yoloctl-upload-test-")
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(dir)
		if err := os.Remove(dir); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		want := data.Len()
		if !complete {
			want++
		}
		cmd := exec.Command(python, "-c", imageUploadScript, name, strconv.Itoa(want))
		cmd.Stdin = bytes.NewReader(data.Bytes())
		if out, err := cmd.CombinedOutput(); (err == nil) != complete {
			t.Fatalf("complete=%t: %v %s", complete, err, out)
		}
		if !complete {
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatal("partial upload was left behind")
			}
			continue
		}
		path := filepath.Join(dir, "clipboard.png")
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, data.Bytes()) {
			t.Fatalf("stored=%q %v", got, err)
		}
		for path, mode := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != mode {
				t.Fatalf("permissions %s: %v %v", path, info, err)
			}
		}
		// A repeated operation cannot replace the previous file or follow a
		// planted path. Retrying uncertain uploads requires a new user action.
		cmd = exec.Command(python, "-c", imageUploadScript, name, strconv.Itoa(data.Len()))
		cmd.Stdin = bytes.NewReader(data.Bytes())
		if err := cmd.Run(); err == nil {
			t.Fatal("reused existing upload directory")
		}
	}
}

func TestBrowserLaunchQuotesExecutableAndRetainsExplicitChoice(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		cmd := exec.Command("sh", "-c", browserLaunch([]string{"sh", "-c", `printf '%s\n' "$BROWSER" "$PATH"`}, "/tmp/yoloctl-browser-quoted"))
		cmd.Env = []string{"PATH=/usr/bin:/bin"}
		want := "/tmp/yoloctl-browser-quoted/open-url"
		if explicit {
			cmd.Env = append(cmd.Env, "BROWSER=already-chosen")
			want = "already-chosen"
		}
		got, err := cmd.Output()
		if err != nil || string(got) != want+"\n/tmp/yoloctl-browser-quoted:/usr/bin:/bin\n" {
			t.Fatalf("launch=%q %v", got, err)
		}
	}
}

func TestImageReuseOnlyMapsWorkdirDescendants(t *testing.T) {
	for _, tc := range []struct{ root, source, want string }{
		{"/home/repo", "/home/repo/image.png", "/workdir/home/repo/image.png"},
		{"/home/repo", "/home/repo/sub/a b.png", "/workdir/home/repo/sub/a b.png"},
		{"/home/repo", "/home/repo2/image.png", ""},
		{"/home/repo", "/home/repo/../image.png", ""},
		{"/home/repo", "/home/repo", ""},
		{"/home/repo", "/home/repo/image.png\ncommand", ""},
		{"", "/home/repo/image.png", ""},
		{"relative", "/home/repo/image.png", ""},
	} {
		if got := workdirImagePath(tc.root, tc.source); got != tc.want {
			t.Errorf("map(%q,%q)=%q", tc.root, tc.source, got)
		}
	}
}

func TestImageReuseScriptVerifiesBytesWithoutFollowingLinks(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "an image $(id).png")
	if err := os.WriteFile(path, pngData.Bytes(), 0o400); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(pngData.Bytes()))
	for name, target := range map[string]string{"link.png": path, "linkdir": dir} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Mkfifo(filepath.Join(dir, "pipe.png"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "changed.png"), bytes.Repeat([]byte{'x'}, pngData.Len()), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, digest string
		size         int
		hit          bool
	}{
		{path, digest, pngData.Len(), true},
		{path, digest, pngData.Len() + 1, false},
		{path, "0" + digest[1:], pngData.Len(), false},
		{filepath.Join(dir, "missing.png"), digest, pngData.Len(), false},
		{filepath.Join(dir, "link.png"), digest, pngData.Len(), false},
		{filepath.Join(dir, "linkdir", filepath.Base(path)), digest, pngData.Len(), false},
		{filepath.Join(dir, "pipe.png"), digest, pngData.Len(), false},
		{filepath.Join(dir, "changed.png"), digest, pngData.Len(), false},
		{dir, digest, pngData.Len(), false},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		cmd := exec.CommandContext(ctx, python, "-c", imageReuseScript, dir, strings.TrimPrefix(tc.path, dir+"/"), strconv.Itoa(tc.size), tc.digest)
		out, err := cmd.CombinedOutput()
		cancel()
		if tc.hit {
			if err != nil {
				t.Fatalf("read-only reuse: %v %s", err, out)
			}
		} else {
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 3 {
				t.Fatalf("miss %s: %v %s", tc.path, err, out)
			}
		}
		if len(out) != 0 {
			t.Fatalf("probe leaked output: %q", out)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, pngData.Bytes()) {
		t.Fatal("probe changed file")
	}
}

// Model a peer whose stdout stays open even after the SSH close request.
func TestBrowserReadinessCancellationDoesNotWaitForPeerEOF(t *testing.T) {
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { _, err := browserReadyLine(ctx, bufio.NewReader(reader)); result <- err }()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("readiness: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("optional browser startup waited for remote EOF")
	}
}
