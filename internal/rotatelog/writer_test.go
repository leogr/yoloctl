package rotatelog

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func openTestLog(t *testing.T, limit int64) (*Writer, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.log")
	w, err := Open(path, limit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w, path
}

func readLog(t *testing.T, path string) []byte {
	t.Helper()
	p, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestWriterRetainsRawSuffixAcrossRotations(t *testing.T) {
	w, path := openTestLog(t, 31)
	input := bytes.Repeat([]byte("line\x00\x1b\r\n"), 101)
	if n, err := w.Write(input); err != nil || n != len(input) {
		t.Fatal(n, err)
	}
	if err := w.Err(); err != nil {
		t.Fatal(err)
	}
	before, current := readLog(t, path+".1"), readLog(t, path)
	if len(before) > 31 || len(current) > 31 {
		t.Fatalf("unbounded generations: %d %d", len(before), len(current))
	}
	retained := append(before, current...)
	if !bytes.HasSuffix(input, retained) || len(before) != 31 || len(current) == 0 {
		t.Fatalf("raw tail changed: %q", retained)
	}
}

func TestWriterBoundsPreviouslyUnboundedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.log")
	old := bytes.Repeat([]byte("old raw log\n"), 101)
	if err := os.WriteFile(path, old, 0600); err != nil {
		t.Fatal(err)
	}
	w, err := Open(path, 31)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	if !bytes.Equal(readLog(t, path+".1"), old[len(old)-31:]) {
		t.Fatal("old tail changed")
	}
	if n, err := w.Write([]byte("new raw log\n")); n != 12 || err != nil || w.Err() != nil {
		t.Fatal(n, err, w.Err())
	}
	if got := string(readLog(t, path)); got != "new raw log\n" {
		t.Fatalf("new bytes: %q", got)
	}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".*.rotate-*"))
	if err != nil || len(files) != 0 {
		t.Fatal(files, err)
	}
}

func TestWriterRefusesUnsafeCurrentWithoutFailingProducer(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "mode"} {
		t.Run(kind, func(t *testing.T) {
			w, path := openTestLog(t, 1024)
			foreign := filepath.Join(filepath.Dir(path), "sentinel")
			if err := os.WriteFile(foreign, []byte("untouched"), 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "mode" {
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				var err error
				if kind == "symlink" {
					err = os.Symlink(foreign, path)
				} else {
					err = os.Link(foreign, path)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := w.Write([]byte("strict failure")); err == nil {
				t.Fatal("structured sink hid its failure")
			}
			if n, err := w.BestEffort().Write([]byte("do not append")); n != 13 || err != nil {
				t.Fatal("log failure reached producer", n, err)
			}
			if w.Err() == nil {
				t.Fatal("missing private-file failure")
			}
			if string(readLog(t, foreign)) != "untouched" {
				t.Fatal("sentinel modified")
			}
			if kind == "mode" {
				if err := os.Chmod(path, 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := w.Write([]byte("recovered")); err != nil {
					t.Fatal(err)
				}
				if string(readLog(t, path)) != "recovered" {
					t.Fatal("later write was not attempted")
				}
			}
		})
	}
}

func TestWriterClose(t *testing.T) {
	w, _ := openTestLog(t, 32)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("closed")); !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}
}

type acknowledgedOutput struct {
	io.Writer
	copied  chan []byte
	stopped <-chan struct{}
}

func (a acknowledgedOutput) Write(p []byte) (int, error) {
	n, err := a.Writer.Write(p)
	select {
	case a.copied <- bytes.Clone(p[:n]):
	case <-a.stopped:
	}
	return n, err
}

// The external producer keeps one stdout pipe while its supervisor's Writer
// rotates twice. No sleeps/retries: each stdin line permits the next output,
// and the copy acknowledgement establishes that the previous bytes arrived.
func TestExternalProducerKeepsWritingAcrossTwoRotations(t *testing.T) {
	w, path := openTestLog(t, 17)
	copied := make(chan []byte, 8)
	stopped := make(chan struct{})
	cmd := exec.Command("sh", "-c", `i=1; while [ "$i" -le 3 ]; do read -r go || exit; printf '%016d\n' "$i"; i=$((i+1)); done`)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, drained, err := Pipe(acknowledgedOutput{Writer: w.BestEffort(), copied: copied, stopped: stopped}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = output.Close() }()
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = output.Close()
	waited := false
	t.Cleanup(func() {
		close(stopped)
		_ = in.Close()
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	for i := 1; i <= 3; i++ {
		if _, err := io.WriteString(in, "next\n"); err != nil {
			t.Fatal(err)
		}
		var got []byte
		deadline := time.NewTimer(5 * time.Second)
		for len(got) < 17 {
			select {
			case p := <-copied:
				got = append(got, p...)
			case <-deadline.C:
				t.Fatal("external output did not arrive")
			}
		}
		deadline.Stop()
		if want := fmt.Sprintf("%016d\n", i); string(got) != want {
			t.Fatalf("output %q, want %q", got, want)
		}
	}
	err = cmd.Wait()
	waited = true
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not drain")
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Err(); err != nil {
		t.Fatal(err)
	}
	if got := string(readLog(t, path+".1")) + string(readLog(t, path)); got != fmt.Sprintf("%016d\n%016d\n", 2, 3) {
		t.Fatalf("retained child bytes %q", got)
	}
}

func TestReadTailPreservesRawMarkerAcrossRotation(t *testing.T) {
	w, path := openTestLog(t, 64)
	marker := []byte("checked: disk-id from=10 to=20\n")
	input := append(bytes.Repeat([]byte("p"), 50), marker...)
	input = append(input, bytes.Repeat([]byte("n"), 32)...)
	for _, p := range [][]byte{input[:50], marker, input[50+len(marker):]} {
		if _, err := w.Write(p); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ReadTail(path, 64)
	if err != nil || !bytes.Equal(got, input[len(input)-64:]) || !bytes.Contains(got, marker) {
		t.Fatalf("tail %q, err %v", got, err)
	}
	if _, err := w.Write(bytes.Repeat([]byte("z"), 40)); err != nil {
		t.Fatal(err)
	}
	input = append(input, bytes.Repeat([]byte("z"), 40)...)
	got, err = ReadTail(path, 64)
	if err != nil || !bytes.Equal(got, input[len(input)-64:]) {
		t.Fatalf("last window %q, err %v", got, err)
	}
}

func TestReadTailRejectsForeignHistory(t *testing.T) {
	w, path := openTestLog(t, 64)
	if _, err := w.Write([]byte("current")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(path), "outside")
	if err := os.WriteFile(outside, []byte("foreign history"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path+".1"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTail(path, 64); err == nil {
		t.Fatal("followed a planted history link")
	}
}
