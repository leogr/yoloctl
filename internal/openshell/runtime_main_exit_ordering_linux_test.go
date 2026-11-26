package openshell

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/boxendpoint"
	"golang.org/x/sys/unix"
)

func TestRuntimeMainExitDrainDeadlineAndEarlyCompletion(t *testing.T) {
	now := time.Date(2026, 11, 14, 1, 0, 0, 0, time.UTC)
	code := 7
	d := runtimeMainExitDrain{exit: &runtimeMainExit{&code}, deadline: now.Add(runtimeMainExitWait)}
	// The main-exit drain stays 4 s, independent of the endpoint's
	// two-minute close fail-safe: its end fails nothing (yolo-cla's ruling).
	if runtimeMainExitWait != 4*time.Second || runtimeMainExitWait >= boxendpoint.DefaultCloseTimeout {
		t.Fatal("main-exit drain no longer the independent 4 s")
	}
	for _, elapsed := range []time.Duration{0, time.Second, runtimeMainExitWait - time.Nanosecond} {
		if d.complete(now.Add(elapsed), false) {
			t.Fatalf("ended before the bound: %v", elapsed)
		}
	}
	if !d.complete(now.Add(runtimeMainExitWait), false) {
		t.Fatal("did not end at the bound")
	}
	if !d.complete(now.Add(time.Second), true) {
		t.Fatal("late attachment completion did not end the wait")
	}
	if !d.complete(now, true) {
		t.Fatal("both markers did not end immediately")
	}
	d.exit = &runtimeMainExit{}
	if !d.complete(now, false) {
		t.Fatal("invalid main marker gained an attachment wait")
	}
}

func TestRuntimeMainAttachmentMarkerCannotInventCompletion(t *testing.T) {
	for _, kind := range []string{"absent", "valid", "truncated", "trailing", "long", "owner", "group", "link", "directory", "fifo", "socket", "unavailable"} {
		t.Run(kind, func(t *testing.T) {
			fd, err := os.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = fd.Close() }()
			path := fmt.Sprintf("/proc/self/fd/%d/%s", fd.Fd(), runtimeMainAttachmentsEndedName)
			uid, gid := os.Geteuid(), os.Getegid()
			switch kind {
			case "valid", "owner", "group":
				err = os.WriteFile(path, []byte("ended\n"), 0o600)
			case "truncated":
				err = os.WriteFile(path, []byte("ended"), 0o600)
			case "trailing":
				err = os.WriteFile(path, []byte("ended\nextra"), 0o600)
			case "long":
				err = os.WriteFile(path, []byte(strings.Repeat("x", 1000)), 0o600)
			case "link":
				err = os.Symlink("missing", path)
			case "directory":
				err = os.Mkdir(path, 0o700)
			case "fifo":
				err = unix.Mkfifo(path, 0o600)
			case "socket":
				var listener *net.UnixListener
				listener, err = net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
				if err == nil {
					defer func() { _ = listener.Close() }()
				}
			case "unavailable":
				err = fd.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind == "owner" {
				uid++
			}
			if kind == "group" {
				gid++
			}
			complete, err := readRuntimeMainAttachmentsEndedAt(fd, uid, gid)
			if complete != (kind == "valid") || (err != nil) != (kind == "unavailable") {
				t.Fatalf("complete=%v err=%v", complete, err)
			}
		})
	}
}

func TestRuntimeMainAttachmentStaleRemovalAndCleanupScope(t *testing.T) {
	for _, kind := range []string{"file", "owner", "link", "directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			fd, err := os.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = fd.Close() }()
			path := filepath.Join(dir, runtimeMainAttachmentsEndedName)
			uid, gid := os.Geteuid(), os.Getegid()
			switch kind {
			case "file", "owner":
				err = os.WriteFile(path, []byte("stale partial"), 0o600)
			case "link":
				err = os.Symlink("missing", path)
			case "directory":
				err = os.Mkdir(path, 0o700)
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind == "owner" {
				uid++
			}
			err = clearRuntimeMainExitAt(fd, uid, gid)
			if (err == nil) != (kind == "file") {
				t.Fatal(err)
			}
			_, observed := os.Lstat(path)
			if kind == "file" && !errors.Is(observed, os.ErrNotExist) {
				t.Fatal("stale marker survived", observed)
			}
			if kind != "file" && observed != nil {
				t.Fatal("foreign object removed", observed)
			}
		})
	}
	prefix := "generations/g" + strings.Repeat("a", 48) + "/status/runtime-supervisor/"
	for _, name := range []string{runtimeMainAttachmentsEndedName, ".main-attachments-ended.tmp-1", ".main-attachments-ended.tmp-77"} {
		rule, ok := runtimeReservationMember(prefix + name)
		if !ok || rule.kind != unix.S_IFREG || rule.permissions != 0o600 || rule.mask != 0o7777 {
			t.Fatal(name, rule)
		}
	}
	for _, name := range []string{"main-attachments-ended.other", ".main-attachments-ended.tmp-", ".main-attachments-ended.tmp-0", ".main-attachments-ended.tmp-07", ".main-attachments-ended.tmp-+7", ".main-attachments-ended.tmp-7/x"} {
		if _, ok := runtimeReservationMember(prefix + name); ok {
			t.Fatal("lookalike admitted", name)
		}
	}
}
