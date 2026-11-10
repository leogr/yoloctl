package followview

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

func fixtureDir(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func pythonReader(path string, roots func() []string) VMReader {
	return VMReader{Path: path, Authority: func(context.Context) ([]string, error) { return roots(), nil }, Command: func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, args[0], args[1:]...)
	}}
}
func writeFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestHostAndVMFollowTheNameAndHashEveryRead(t *testing.T) {
	for _, vm := range []bool{false, true} {
		t.Run(map[bool]string{false: "host", true: "vm"}[vm], func(t *testing.T) {
			root := fixtureDir(t)
			path := filepath.Join(root, "file.md")
			writeFixture(t, path, []byte("middle\n"))
			var reader Reader = HostReader{Path: path}
			if vm {
				reader = pythonReader(path, func() []string { return []string{root} })
			}
			read := func(q Query) Snapshot {
				t.Helper()
				s, err := reader.Read(context.Background(), q)
				if err != nil {
					t.Fatal(err)
				}
				return s
			}
			s := read(Query{})
			if !s.Changed || string(s.Data) != "middle\n" {
				t.Fatal(s)
			}
			next := read(Query{Previous: s.Key})
			if next.Changed || next.Data != nil {
				t.Fatal("unchanged read transferred payload", next)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			writeFixture(t, path, []byte("edited\n"))
			if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
				t.Fatal(err)
			}
			next = read(Query{Previous: s.Key})
			if !next.Changed || string(next.Data) != "edited\n" {
				t.Fatal("same-size restored-mtime edit missed", next)
			}
			replacement := filepath.Join(root, "save")
			writeFixture(t, replacement, []byte("replacement\n"))
			if err := os.Rename(replacement, path); err != nil {
				t.Fatal(err)
			}
			next = read(Query{Previous: next.Key})
			if string(next.Data) != "replacement\n" || next.Inode == s.Inode {
				t.Fatal("replacement name not followed", next)
			}
			writeFixture(t, path, nil)
			next = read(Query{Previous: next.Key})
			if !next.Changed || next.Size != 0 || len(next.Data) != 0 {
				t.Fatal("empty shrink was not a change", next)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if _, err := reader.Read(context.Background(), Query{Previous: next.Key}); err == nil {
				t.Fatal("deletion became unchanged success")
			}
			writeFixture(t, path, []byte("back"))
			if s := read(Query{Previous: next.Key}); string(s.Data) != "back" {
				t.Fatal(s)
			}
		})
	}
}

func TestVMReadAndCompletionStayInsideCurrentGrants(t *testing.T) {
	root := fixtureDir(t)
	outside := fixtureDir(t)
	path := filepath.Join(root, "inside")
	writeFixture(t, path, []byte("ok"))
	writeFixture(t, filepath.Join(outside, "secret"), []byte("outside"))
	roots := []string{root}
	r := pythonReader(path, func() []string { return roots })
	if _, err := r.Read(context.Background(), Query{}); err != nil {
		t.Fatal(err)
	}
	roots = nil
	if _, err := r.Read(context.Background(), Query{}); err == nil {
		t.Fatal("removed grant kept authority")
	}
	roots = []string{root}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	r.Path = filepath.Join(root, "link", "secret")
	if _, err := r.Read(context.Background(), Query{}); err == nil || !strings.Contains(err.Error(), "direct path") {
		t.Fatal("symlink traversal", err)
	}
	r.Path = outside + "/secret"
	if _, err := r.Read(context.Background(), Query{}); err == nil {
		t.Fatal("ungranted file read")
	}
	r.Path = root
	if _, err := r.Read(context.Background(), Query{}); err == nil {
		t.Fatal("directory read as regular file")
	}
	pipe := filepath.Join(root, "fifo")
	if err := unix.Mkfifo(pipe, 0600); err != nil {
		t.Fatal(err)
	}
	r.Path = pipe
	if _, err := r.Read(context.Background(), Query{}); err == nil {
		t.Fatal("FIFO read")
	}
	entries, err := r.List(context.Background(), root, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(entries, ",") != "inside" {
		t.Fatal("completion exposed symlink or nonregular file", entries)
	}
	if _, err := r.List(context.Background(), outside, ""); err == nil {
		t.Fatal("completion escaped grant")
	}
}

func TestHostRefusesNonregularWithoutWaitingForAWriter(t *testing.T) {
	path := filepath.Join(fixtureDir(t), "fifo")
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := (HostReader{Path: path}).Read(context.Background(), Query{}); err == nil {
		t.Fatal("FIFO read")
	}
}

func TestLargeWindowsRefreshTheOlderRegionAndRespectUTF8(t *testing.T) {
	for _, vm := range []bool{false, true} {
		t.Run(map[bool]string{false: "host", true: "vm"}[vm], func(t *testing.T) {
			root := fixtureDir(t)
			path := filepath.Join(root, "large.log")
			data := bytes.Repeat([]byte("界"), WholeBytes/3+100)
			writeFixture(t, path, data)
			var r Reader = HostReader{Path: path}
			if vm {
				r = pythonReader(path, func() []string { return []string{root} })
			}
			s, err := r.Read(context.Background(), Query{Tail: true})
			if err != nil {
				t.Fatal(err)
			}
			if !s.Large || len(s.Data) > WindowBytes || !utf8.Valid(s.Data) || s.End != int64(len(data)) {
				t.Fatal("invalid tail window", s.Start, s.End, len(s.Data), err)
			}
			older, err := r.Read(context.Background(), Query{Offset: 1, Previous: s.Key})
			if err != nil {
				t.Fatal(err)
			}
			if older.Start > 4 || !utf8.Valid(older.Data) || len(older.Data) > WindowBytes {
				t.Fatal("invalid older window", older.Start, len(older.Data))
			}
			data[300] = 'X'
			writeFixture(t, path, data)
			updated, err := r.Read(context.Background(), Query{Offset: 1, Previous: older.Key})
			if err != nil || !updated.Changed || updated.Start != older.Start {
				t.Fatal("older-region edit missed", err)
			}
			unchanged, err := r.Read(context.Background(), Query{Offset: 1, Previous: updated.Key})
			if err != nil || unchanged.Changed || len(unchanged.Data) > 0 {
				t.Fatal("older unchanged payload", err)
			}
			writeFixture(t, path, []byte("small again"))
			small, err := r.Read(context.Background(), Query{Offset: older.Start, Previous: older.Key})
			if err != nil || small.Large || string(small.Data) != "small again" {
				t.Fatal("whole mode did not restore", err)
			}
		})
	}
}

func TestVMUnchangedResponseHasNoPayloadOnTheWire(t *testing.T) {
	root := fixtureDir(t)
	path := filepath.Join(root, "file")
	writeFixture(t, path, bytes.Repeat([]byte("z"), WholeBytes))
	r := pythonReader(path, func() []string { return []string{root} })
	first, err := r.Read(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	response, err := r.call(context.Background(), guestRequest{Op: "read", Path: path, Query: Query{Previous: first.Key}})
	if err != nil {
		t.Fatal(err)
	}
	if response.Changed || response.Data != nil || response.ReceivedBytes > 1024 || first.ReceivedBytes < WholeBytes {
		t.Fatal("unchanged response included content")
	}
}

func TestCancellationReapsTheOwnedReader(t *testing.T) {
	root := fixtureDir(t)
	marker := filepath.Join(root, "started")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var cmd *exec.Cmd
	r := VMReader{Path: filepath.Join(root, "file"), Authority: func(context.Context) ([]string, error) { return []string{root}, nil }, Command: func(ctx context.Context, args ...string) *exec.Cmd {
		cmd = exec.CommandContext(ctx, "python3", "-c", "import pathlib,sys,time;pathlib.Path(sys.argv[1]).write_text('ready');time.sleep(30)", marker)
		return cmd
	}}
	done := make(chan error, 1)
	go func() { _, err := r.Read(ctx, Query{}); done <- err }()
	deadline := time.After(3 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatal("reader ended before cancellation", err)
		case <-deadline:
			t.Fatal("reader never started")
		case <-time.After(time.Millisecond * 5):
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || cmd.ProcessState == nil {
			t.Fatal("reader not cancelled and reaped", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reader survived cancellation")
	}
}
