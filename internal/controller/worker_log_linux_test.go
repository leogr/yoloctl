package controller

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/leogr/yoloctl/pkg/prettylog"
)

func runLogWorker(t *testing.T, mode, verbosity string) (int, []string, time.Duration, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var log recordLog
	started := time.Now()
	code, err := RunWorker(ctx, Principal{}, Request{Method: "test.log", Args: []string{mode}, Log: verbosity}, Streams{
		In: bytes.NewReader(nil), Out: io.Discard, Err: io.Discard, Log: &log, Release: func() error { return nil },
	})
	var lines []string
	for _, r := range log.records {
		st := prettylog.StructureOf(r)
		lines = append(lines, r.Message+"|"+st.State+"|"+st.Outcome)
	}
	return code, lines, time.Since(started), err
}

func TestWorkerLogRecordsArriveBeforeTheResult(t *testing.T) {
	code, lines, _, err := runLogWorker(t, "records", "")
	want := []string{"Building image test|running|", "tool output||", "Building image test|ok|", "Box dev failed||failed"}
	if code != 3 || err != nil || strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("code=%d err=%v records:\n%s", code, err, strings.Join(lines, "\n"))
	}
	// The host's verbosity reaches the worker's filter.
	_, lines, _, _ = runLogWorker(t, "records", LogDebug)
	if !strings.Contains(strings.Join(lines, "\n"), "debug detail") {
		t.Fatal(lines)
	}
}

func TestWorkerLogReportsAPartialRecord(t *testing.T) {
	code, lines, _, err := runLogWorker(t, "partial", "")
	if code != 0 || err != nil || strings.Join(lines, "\n") != "complete record||\n[1 incomplete log record discarded]||" {
		t.Fatalf("code=%d err=%v %q", code, err, lines)
	}
}

func TestWorkerLogStopsWaitingForAnEscapedHolder(t *testing.T) {
	code, lines, took, err := runLogWorker(t, "escaped", "")
	if code != 0 || err != nil || strings.Join(lines, "\n") != "before exit||\n[log stream incomplete]||" {
		t.Fatalf("code=%d err=%v %q", code, err, lines)
	}
	if took < workerLogJoin || took > workerLogJoin+3*time.Second {
		t.Fatalf("join took %s", took)
	}
}

func TestOpenWorkerLogAcceptsOnlyTheRecordedPipe(t *testing.T) {
	// Raw descriptors: an *os.File owner would close a reused number later.
	pipe := func() (int, int) {
		var fds [2]int
		if err := unix.Pipe2(fds[:], unix.O_CLOEXEC); err != nil {
			t.Fatal(err)
		}
		return fds[0], fds[1]
	}
	read, write := pipe()
	otherRead, otherWrite := pipe()
	defer func() { _ = unix.Close(read); _ = unix.Close(otherRead); _ = unix.Close(otherWrite) }()
	other, _ := nativePhasePipeIdentity(otherWrite)
	if f := OpenWorkerLog(WorkerInput{LogFD: write, LogPipe: other}); f != nil {
		t.Fatal("claimed a descriptor that is not the recorded pipe")
	}
	if _, err := unix.Write(write, []byte("x")); err != nil {
		t.Fatal("an unowned descriptor was modified or closed:", err)
	}
	if f := OpenWorkerLog(WorkerInput{LogFD: read, LogPipe: other}); f != nil {
		t.Fatal("claimed a read end")
	}
	_ = unix.SetNonblock(write, false)
	flags, _ := unix.FcntlInt(uintptr(write), unix.F_GETFD, 0)
	_, _ = unix.FcntlInt(uintptr(write), unix.F_SETFD, flags&^unix.FD_CLOEXEC)
	id, _ := nativePhasePipeIdentity(write)
	f := OpenWorkerLog(WorkerInput{LogFD: write, LogPipe: id})
	if f == nil {
		t.Fatal("refused the recorded pipe")
	}
	if flags, err := unix.FcntlInt(f.Fd(), unix.F_GETFD, 0); err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatal("claimed pipe is inheritable", err)
	}
	_ = f.Close() // the sole owner of write
}

func TestRelayWorkerLogDrainsACorruptStream(t *testing.T) {
	stream := append([]byte{0xff, 0xff, 0xff, 0xff}, bytes.Repeat([]byte("x"), 100)...)
	r := bytes.NewReader(stream)
	var log recordLog
	err := relayWorkerLog(context.Background(), r, &log)
	if err == nil || r.Len() != 0 || len(log.records) != 0 {
		t.Fatal(err, r.Len(), len(log.records))
	}
	if errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
}
