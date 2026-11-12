//go:build linux

package rotatelog

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCaptureStdioChild(t *testing.T) {
	if os.Getenv("YOLO_TEST_ROTATE_STDIO") != "1" {
		return
	}
	path := os.Getenv("YOLO_TEST_ROTATE_PATH")
	w, err := Open(path, 64<<10)
	if err != nil {
		os.Exit(11)
	}
	finish, done, err := CaptureStdio(w.BestEffort())
	if err != nil {
		os.Exit(12)
	}
	// More than a pipe buffer: stdout/stderr must remain blocking after dup2.
	data := bytes.Repeat([]byte("external raw\x00\x1b\r\n"), 32768)
	if _, err := os.Stdout.Write(data); err != nil {
		os.Exit(13)
	}
	if _, err := os.Stderr.Write([]byte("last child line\n")); err != nil {
		os.Exit(14)
	}
	// A live inherited writer must not extend the helper's bounded retirement
	// decision. Closing our descriptors leaves its relay open until real EOF.
	child := exec.Command("sh", "-c", "IFS= read -r _; printf 'late external line\\n'")
	input, err := child.StdinPipe()
	if err != nil {
		os.Exit(17)
	}
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		os.Exit(18)
	}
	finish()
	select {
	case <-done:
		os.Exit(19)
	default:
	}
	if _, err := fmt.Fprintln(input, "release"); err != nil {
		os.Exit(20)
	}
	_ = input.Close()
	if err := child.Wait(); err != nil {
		os.Exit(21)
	}
	<-done
	// Finish leaves dst open. This direct write tests that generic API, not
	// the VM helper route: its own records stay on captured stderr.
	if _, err := fmt.Fprintln(w, "final diagnostic"); err != nil {
		os.Exit(15)
	}
	if w.Err() != nil {
		os.Exit(16)
	}
	_ = w.Close()
	os.Exit(0)
}

func TestCaptureStdioDrainsBeforeHelperExit(t *testing.T) {
	dir := t.TempDir()
	startup, err := os.OpenFile(filepath.Join(dir, "startup.log"), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = startup.Close() }()
	path := filepath.Join(dir, "vm.log")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCaptureStdioChild$")
	cmd.Env = append(os.Environ(), "YOLO_TEST_ROTATE_STDIO=1", "YOLO_TEST_ROTATE_PATH="+path)
	cmd.Stdout, cmd.Stderr = startup, startup
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	got, err := ReadTail(path, 32<<10)
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Repeat([]byte("external raw\x00\x1b\r\n"), 32768)
	want = append(want, []byte("last child line\nlate external line\nfinal diagnostic\n")...)
	if !bytes.Equal(got, want[len(want)-(32<<10):]) {
		t.Fatal("helper exit lost or changed the retained raw tail")
	}
	if raw := readLog(t, filepath.Join(dir, "startup.log")); len(raw) != 0 {
		t.Fatalf("wrote the old startup inode: %q", raw)
	}
}
