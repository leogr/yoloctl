package controller

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func nativePhaseWriterPipe(t *testing.T) (*os.File, int, NativePhasePipeID) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Dup(int(write.Fd()))
	_ = write.Close()
	if err != nil {
		_ = read.Close()
		t.Fatal(err)
	}
	identity, ok := nativePhasePipeIdentity(fd)
	if !ok {
		_ = unix.Close(fd)
		_ = read.Close()
		t.Fatal("owned write pipe failed validation")
	}
	t.Cleanup(func() { _ = read.Close() })
	return read, fd, *identity
}

func TestNativePhaseWriterRejectsUnownedFDWithoutChangingIt(t *testing.T) {
	read, fd, identity := nativePhaseWriterPipe(t)
	defer func() { _ = unix.Close(fd) }()
	before, _ := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	wrong := identity
	wrong.Inode++
	if nativePhaseOpenWriter(fd, wrong) != nil {
		t.Fatal("foreign inherited pipe identity was accepted")
	}
	after, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil || before != after {
		t.Fatal("invalid metadata closed or changed an unowned descriptor", err)
	}
	if nativePhaseOpenWriter(int(read.Fd()), identity) != nil {
		t.Fatal("read end became a diagnostic writer")
	}
	for _, work := range []WorkerInput{
		{NativePhaseFD: 3, NativePhasePipe: &identity},
		{NativePhaseFD: 4, NativePhasePipe: &identity},
		{Request: Request{View: true}, NativePhaseFD: 5, NativePhasePipe: &identity},
	} {
		if OpenNativePhaseWriter(work) != nil {
			t.Fatal("request/authority/view descriptor accepted as diagnostics")
		}
	}
}

func TestNativePhaseWriterBrokenAndBlockedPipesNeverWait(t *testing.T) {
	for _, mode := range []string{"broken", "blocked"} {
		t.Run(mode, func(t *testing.T) {
			read, fd, identity := nativePhaseWriterPipe(t)
			writer := nativePhaseOpenWriter(fd, identity)
			if writer == nil {
				t.Fatal("owned pipe writer unavailable")
			}
			defer writer.Close()
			if mode == "broken" {
				_ = read.Close()
			} else {
				full := false
				for range 1024 {
					_, err := unix.Write(fd, bytes.Repeat([]byte("x"), 4096))
					if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
						full = true
						break
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				if !full {
					t.Fatal("failed to stage finite blocked-writer control")
				}
			}
			done := make(chan struct{})
			go func() { writer.Flush(nativePhaseTestBody("coder", false)); close(done) }()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("diagnostic flush waited for pipe readiness")
			}
		})
	}
}

func TestNativePhaseRunWorkerPreservesAuthorityAndDoesNotInheritDiagnostics(t *testing.T) {
	for _, mode := range []string{"enabled", "disabled", "authority-fault", "pty", "view"} {
		t.Run(mode, func(t *testing.T) {
			s := NewNativePhaseStore()
			parent := nativePhaseTestParent(1)
			parent.Method = "test.native-phase"
			observation := s.Reserve(parent)
			if mode == "disabled" {
				observation = nil
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			var out bytes.Buffer
			gates := 0
			req := Request{ID: parent.ExecutionID, Method: parent.Method, Space: parent.Hello.Space, Dash: -1}
			if mode == "authority-fault" {
				req.Args = []string{mode}
			}
			if mode == "pty" {
				req.Terminal, req.Cols, req.Rows = true, 80, 24
			}
			streams := Streams{
				In: strings.NewReader(""), Out: &out, Err: io.Discard, NativePhase: observation,
				BeginEffects: func(context.Context, EffectIntent) error { gates++; return nil },
			}
			if mode == "view" {
				req.View = true
				view, peer := net.Pipe()
				defer func() { _ = peer.Close() }()
				streams.View = view
			}
			code, err := RunWorker(ctx, Principal{}, req, streams)
			if gates != 1 || !bytes.Contains(out.Bytes(), []byte("original-effect")) {
				t.Fatal("diagnostics changed original effect order", code, err, gates, out.String())
			}
			if mode == "authority-fault" {
				var interrupted *WorkerInterrupted
				if !errors.As(err, &interrupted) {
					t.Fatal("diagnostic isolation swallowed authority failure", code, err)
				}
			} else if code != 7 || err != nil {
				t.Fatal("diagnostics changed original worker result", code, err)
			}
			if observation != nil {
				observation.Result(code, NativePhaseOutcomeFailed, parent.Scope)
				value := waitNativePhaseRead(t, s, parent.ExecutionID)
				if mode != "authority-fault" && (value.State != NativePhaseStateComplete || value.WorkerPID <= 1) {
					t.Fatal("actual worker descriptor/custody path lost transcript", value)
				}
			}
		})
	}
}

func TestNativePhaseRunWorkerCanceledStartCreatesNoProducerProvenance(t *testing.T) {
	s := NewNativePhaseStore()
	parent := nativePhaseTestParent(1)
	parent.Method = "test.native-phase"
	observation := s.Reserve(parent)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	code, err := RunWorker(ctx, Principal{}, Request{ID: parent.ExecutionID, Method: parent.Method, Space: parent.Hello.Space, Dash: -1}, Streams{
		In: strings.NewReader(""), Out: io.Discard, Err: io.Discard, NativePhase: observation,
	})
	if err == nil || code != 2 {
		t.Fatal("canceled launch unexpectedly ran", code, err)
	}
	observation.Result(code, NativePhaseOutcomeCanceled, parent.Scope)
	value := waitNativePhaseRead(t, s, parent.ExecutionID)
	if value.State != NativePhaseStateUnavailable || value.WorkerPID != 0 || value.Transcript != nil {
		t.Fatal("failed Start created successful worker provenance", value)
	}
}
