package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const relayArtifact = "01234567-89ab-cdef-0123-456789abcdef"

// relayUnderTest runs the embedded relay with a fake AIRC client and short
// timings; the program text is otherwise unchanged.
func relayUnderTest(t *testing.T, fakeAirc string, deadline string) string {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	dir := t.TempDir()
	airc := filepath.Join(dir, "airc")
	if err := os.WriteFile(airc, []byte("#!/bin/sh\n"+fakeAirc), 0o700); err != nil {
		t.Fatal(err)
	}
	script := strings.Replace(chatArtifactRelay, `AIRC = "/usr/local/bin/airc"`, `AIRC = "`+airc+`"`, 1)
	script = strings.Replace(script, "DEADLINE = 15.0", "DEADLINE = "+deadline, 1)
	if script == chatArtifactRelay {
		t.Fatal("substitution failed")
	}
	return script
}

type relayRun struct {
	stdout, stderr []byte
	code           int
	took           time.Duration
}

// runRelay runs the relay; closeInput ends its cancel channel after delay
// (negative: keep it open until the relay exits).
func runRelay(t *testing.T, script string, args []string, closeInput time.Duration) relayRun {
	t.Helper()
	cmd := exec.Command("python3", append([]string{"-c", script}, args...)...)
	cmd.Dir = t.TempDir()
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if closeInput >= 0 {
		go func() { time.Sleep(closeInput); _ = in.Close() }()
	}
	err = cmd.Wait()
	_ = in.Close()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return relayRun{out.Bytes(), errOut.Bytes(), code, time.Since(start)}
}

func relayArgs(data []byte) []string {
	sum := sha256.Sum256(data)
	return []string{"/config/client.toml", relayArtifact, intString(len(data)), hex.EncodeToString(sum[:])}
}

func intString(n int) string { return strconv.Itoa(n) }

// fakeDownload writes data to the destination (argument 7: PATH) as AIRC
// does and prints its JSON reply.
func fakeDownload(data string, extra string) string {
	sum := sha256.Sum256([]byte(data))
	return `umask 077; [ "$1 $2 $3" = "artifact download --config" ] || exit 9
printf '%s' '` + data + `' > "$6"
` + extra + `
printf '{"bytes":` + intString(len(data)) + `,"sha256":"` + hex.EncodeToString(sum[:]) + `","filename":"a.png"}\n'
`
}

func TestRelayReturnsVerifiedBytesAndCleansUp(t *testing.T) {
	data := []byte("PNG-ish bytes")
	script := relayUnderTest(t, fakeDownload(string(data), ""), "5.0")
	run := runRelay(t, script, relayArgs(data), -1)
	if run.code != 0 {
		t.Fatalf("exit %d: %s", run.code, run.stderr)
	}
	if len(run.stdout) < 4 || binary.BigEndian.Uint32(run.stdout) != uint32(len(data)) || !bytes.Equal(run.stdout[4:], data) {
		t.Fatalf("frame %q", run.stdout)
	}
	if matches, _ := filepath.Glob("/tmp/yolo-artifact-*"); len(matches) > 0 {
		for _, m := range matches {
			if fi, err := os.Stat(m); err == nil && time.Since(fi.ModTime()) < time.Minute {
				t.Fatalf("private download left: %s", m)
			}
		}
	}
}

func TestRelayRefusesAndNeverWritesAFrame(t *testing.T) {
	data := "good"
	for name, c := range map[string]struct {
		airc  string
		args  []string
		input time.Duration
	}{
		"hash mismatch":     {fakeDownload("evil", ""), relayArgs([]byte(data)), -1},
		"size mismatch":     {fakeDownload(data+"x", ""), relayArgs([]byte(data)), -1},
		"symlink":           {fakeDownload(data, `rm "$6"; ln -s /etc/passwd "$6"`), relayArgs([]byte(data)), -1},
		"wrong mode":        {fakeDownload(data, `chmod 0644 "$6"`), relayArgs([]byte(data)), -1},
		"child failure":     {"exit 3\n", relayArgs([]byte(data)), -1},
		"output flood":      {"yes x | head -c 20000\nsleep 30\n", relayArgs([]byte(data)), -1},
		"canceled by input": {"sleep 30\n", relayArgs([]byte(data)), 200 * time.Millisecond},
		"bad artifact id":   {fakeDownload(data, ""), []string{"/c", "https://x", "4", strings.Repeat("a", 64)}, -1},
		"oversize request":  {fakeDownload(data, ""), []string{"/c", relayArtifact, intString(4<<20 + 1), strings.Repeat("a", 64)}, -1},
	} {
		script := relayUnderTest(t, c.airc, "5.0")
		run := runRelay(t, script, c.args, c.input)
		if run.code == 0 || len(run.stdout) != 0 || !bytes.Contains(run.stderr, []byte("artifact relay:")) {
			t.Errorf("%s: exit %d stdout %q stderr %q", name, run.code, run.stdout, run.stderr)
		}
		if run.took > 4*time.Second {
			t.Errorf("%s: took %s", name, run.took)
		}
	}
}

func TestRelayDeadlineStopsAStuckChildEvenIfItIgnoresTerm(t *testing.T) {
	script := relayUnderTest(t, "trap '' TERM\nwhile :; do sleep 1; done\n", "0.5")
	run := runRelay(t, script, relayArgs([]byte("x")), -1)
	if run.code == 0 || len(run.stdout) != 0 || !bytes.Contains(run.stderr, []byte("deadline")) {
		t.Fatalf("exit %d stdout %q stderr %q", run.code, run.stdout, run.stderr)
	}
	if run.took > 4*time.Second {
		t.Fatalf("relay took %s", run.took)
	}
}
