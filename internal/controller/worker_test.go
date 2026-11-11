package controller

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/term"

	"github.com/leogr/yoloctl/internal/termio"
)

// The subprocess exercises the real private-descriptor and PTY transport,
// without loading VM state or contacting a real container.
func TestMain(m *testing.M) {
	if socket := os.Getenv(ownedClientEnv); socket != "" {
		ownedSetupClient(socket)
	}
	if len(os.Args) == 3 && os.Args[1] == "controller-internal" && os.Args[2] == "verify" {
		var in VerifierInput
		if err := json.NewDecoder(os.NewFile(3, "verifier-request")).Decode(&in); err != nil {
			os.Exit(91)
		}
		out := os.NewFile(4, "verifier-result")
		switch in.Documents[0].Name {
		case "hang":
			time.Sleep(time.Minute)
		case "garbage":
			_, _ = out.Write([]byte("[{\"unknown\":1}]"))
			os.Exit(0)
		}
		observed := []DocumentIntent{{Kind: in.Documents[0].Kind, Name: in.Documents[0].Name, Present: true, Data: []byte("observed"), Mode: 0o600}}
		_ = json.NewEncoder(out).Encode(observed)
		os.Exit(0)
	}
	if len(os.Args) == 3 && os.Args[1] == "controller-internal" && os.Args[2] == "worker" {
		input := os.NewFile(3, "request")
		var work WorkerInput
		if err := json.NewDecoder(input).Decode(&work); err != nil {
			os.Exit(91)
		}
		pidNamespace, namespaceErr := ClaimWorkerPIDNamespace(work)
		if namespaceErr != nil {
			os.Exit(91)
		}
		released := os.NewFile(4, "release")
		finishExit := func(code int) { _, _ = released.Write([]byte{checkpointFinish}); os.Exit(code) }
		switch work.Request.Method {
		case "test.native-phase":
			nativePhaseTestWorker(released, work)
		case "test.pid-namespace":
			workerPIDNamespaceTestWorker(released, work, pidNamespace)
		case "test.native-nonce":
			nativeNonceTestWorker(released, work.Request)
		case "test.image-job":
			checkpoints := NewWorkerCheckpoints(released)
			checkpoints.Gate()
			reply, err := checkpoints.Image(context.Background(), ImageRequest{Action: "acquire", Recipe: recipe(work.Request.Args[0])})
			if err != nil {
				finishExit(2)
			}
			intent := EffectIntent{Scope: &Scope{Writes: []Resource{{Kind: ResourceImages, Name: work.Request.Args[1]}}}}
			if err := checkpoints.BeginEffects(context.Background(), intent); err != nil {
				finishExit(2)
			}
			if _, err := checkpoints.Image(context.Background(), ImageRequest{Action: "publish", JobID: reply.Pin.JobID, Reference: work.Request.Args[1]}); err != nil {
				finishExit(2)
			}
			_, _ = fmt.Fprintln(os.Stdout, reply.Pin.JobID)
			if err := checkpoints.CompleteEffects(context.Background(), 0); err != nil {
				finishExit(2)
			}
			finishExit(0)
		case "box.attach":
			if len(work.Request.Args) == 2 && strings.HasPrefix(work.Request.Args[0], "owned") {
				ownedSetupWorker(released, work.Request)
			}
			if work.Request.Terminal {
				if _, err := term.MakeRaw(int(os.Stdin.Fd())); err != nil {
					finishExit(103)
				}
			}
			view, err := termio.ViewFile(5)
			if err != nil {
				finishExit(97)
			}
			_, _ = released.Write([]byte{1})
			_, _ = os.Stdout.Write([]byte("stdout-only\x00tail"))
			_, _ = os.Stderr.Write([]byte("stderr-only"))
			_, _ = view.Write([]byte("PRIVATE_READY"))
			if len(work.Request.Args) > 0 && work.Request.Args[0] == "wait" {
				time.Sleep(time.Minute)
				finishExit(98)
			}
			var header [4]byte
			if _, err := io.ReadFull(view, header[:]); err != nil {
				finishExit(99)
			}
			n := binary.BigEndian.Uint32(header[:])
			if n > 2<<20 {
				finishExit(100)
			}
			raw := make([]byte, int(n))
			if _, err := io.ReadFull(view, raw); err != nil {
				finishExit(101)
			}
			if err := writeAll(view, raw); err != nil {
				finishExit(102)
			}
			if len(work.Request.Args) > 0 && work.Request.Args[0] == "with-stdin" {
				want := "stdin\x00\r\n\xfforder"
				got := make([]byte, len(want))
				if _, err := io.ReadFull(os.Stdin, got); err != nil || string(got) != want {
					finishExit(104)
				}
			}
			_ = view.Close()
			finishExit(17)
		case "test.gated", "chat.stream", "box.exec":
			checkpoints := NewWorkerCheckpoints(released)
			action := work.Request.Args[0]
			if action == "waiting" {
				fd, err := syscall.Open(work.Request.Args[1], syscall.O_RDWR, 0)
				if err != nil {
					finishExit(91)
				}
				_, _ = os.Stdout.Write([]byte("waiting"))
				_ = syscall.Flock(fd, syscall.LOCK_EX)
				os.Exit(95)
			}
			if action == "malformed" {
				_, _ = released.Write([]byte{255})
				os.Exit(2)
			}
			if action == "malformed-wait" {
				signal.Ignore(syscall.SIGTERM)
				_, _ = released.Write([]byte{255})
				time.Sleep(time.Minute)
				os.Exit(2)
			}
			if action == "child-fd" {
				if err := exec.Command("/bin/sh", "-c", "test ! -e /dev/fd/4").Run(); err != nil {
					os.Exit(2)
				}
			}
			if action == "stdout" {
				_, _ = os.Stdout.Write([]byte{checkpointBegin, checkpointFinish})
				os.Exit(2)
			}
			if err := checkpoints.BeginEffects(context.Background(), EffectIntent{}); err != nil {
				finishExit(2)
			}
			// Appending makes every executed effect countable.
			effect, err := os.OpenFile(work.Request.Args[1], os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
			if err != nil {
				finishExit(2)
			}
			if _, err := effect.Write([]byte("effect\n")); err != nil {
				finishExit(2)
			}
			_ = effect.Close()
			switch action {
			case "hold":
				// A released, visibly running command whose client goes away.
				_ = checkpoints.Release()
				_, _ = os.Stdout.Write([]byte("running"))
				time.Sleep(time.Minute)
			case "kill":
				_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			case "panic":
				panic("synthetic worker panic after effect")
			case "setup-kill":
				_ = checkpoints.Release()
				_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			case "uncertain":
				_ = checkpoints.FinishUncertain()
				os.Exit(2)
			case "malformed-started":
				_, _ = released.Write([]byte{255})
				os.Exit(2)
			}
			finishExit(7)
		case "test.evidence":
			// Recovery evidence: a scoped gate, then each action's checkpoints.
			checkpoints := NewWorkerCheckpoints(released)
			checkpoints.Gate()
			action := work.Request.Args[0]
			scope := &Scope{Writes: []Resource{{Kind: ResourceAccount, Name: "personal"}}}
			verifier := ""
			if action == "proof-kill" {
				verifier = VerifierAccountDocument
			}
			if action == "complete-ungated" {
				checkpoints = NewWorkerCheckpoints(released)
			} else if err := checkpoints.BeginEffects(context.Background(), EffectIntent{Scope: scope, Verifier: verifier}); err != nil {
				finishExit(2)
			}
			switch action {
			case "complete-kill", "complete-ungated":
				if err := checkpoints.CompleteEffects(context.Background(), 3); err != nil {
					finishExit(2)
				}
				_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			case "complete-uncertain", "complete-uncertain-hold":
				if err := checkpoints.CompleteEffects(context.Background(), 0); err != nil {
					finishExit(2)
				}
				_ = checkpoints.FinishUncertain()
				if action == "complete-uncertain-hold" {
					// Stays alive until the controller's checkpoint reader
					// terminates it with a signal (bounded by the sleep).
					time.Sleep(30 * time.Second)
				}
				os.Exit(2)
			case "proof-kill":
				d := DocumentIntent{Kind: DocumentAccount, Name: "personal", Present: true, Data: []byte(work.Request.Args[1]), Mode: 0o600}
				if err := checkpoints.RecordDocument(context.Background(), d); err != nil {
					finishExit(2)
				}
				_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			case "expand-kill":
				if err := checkpoints.ExpandEffects(context.Background(), []Resource{{Kind: ResourceImages}}); err != nil {
					finishExit(2)
				}
				_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			case "proof-refused":
				// Without a verifier the controller refuses the proof; the
				// worker reports it and completes normally.
				d := DocumentIntent{Kind: DocumentAccount, Name: "personal", Present: true, Mode: 0o600}
				if checkpoints.RecordDocument(context.Background(), d) == nil {
					finishExit(2)
				}
				finishExit(0)
			}
			finishExit(95)
		case "test.signal":
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			finishExit(96)
		case "test.echo":
			_, _ = io.Copy(os.Stdout, os.Stdin)
			_, _ = os.Stderr.Write([]byte("stderr\x00tail"))
			finishExit(17)
		case "test.unread-ack":
			// A worker that announces setup with the raw byte, never reads the
			// acknowledgment, then completes normally.
			_, _ = released.Write([]byte{checkpointSetup})
			time.Sleep(100 * time.Millisecond) // the acknowledgment is queued unread
			finishExit(0)
		case "test.exit":
			_, _ = os.Stdout.Write([]byte("final bytes"))
			finishExit(0)
		case "test.pty":
			c, r, err := term.GetSize(int(os.Stdin.Fd()))
			if err != nil {
				finishExit(92)
			}
			fmt.Printf("%dx%d\n", c, r)
			_, _ = released.Write([]byte{1})
			var line [1]byte
			_, _ = os.Stdin.Read(line[:])
			c, r, err = term.GetSize(int(os.Stdin.Fd()))
			if err != nil {
				finishExit(93)
			}
			fmt.Printf("%dx%d\n%s", c, r, strings.Repeat("tail", 16384))
			finishExit(0)
		case "test.cancel":
			_, _ = released.Write([]byte{1})
			time.Sleep(time.Minute)
			finishExit(94)
		case "test.log":
			logWorker(work, finishExit)
		default:
			finishExit(95)
		}
	}
	os.Exit(m.Run())
}

// Closing a Unix socket with unread data resets its peer: a completed worker
// that left the setup acknowledgment unread must not read as interrupted.
func TestCompletedWorkerWithUnreadSetupAcknowledgment(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	released := false
	code, err := RunWorker(ctx, Principal{}, Request{Method: "test.unread-ack"}, Streams{In: strings.NewReader(""), Out: io.Discard, Err: io.Discard,
		Release: func() error { released = true; return nil }})
	if code != 0 || err != nil || !released {
		t.Fatal(code, err, released)
	}
}

func TestWorkerSignalExitStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	code, err := RunWorker(ctx, Principal{}, Request{Method: "test.signal"}, Streams{
		In: bytes.NewReader(nil), Out: io.Discard, Err: io.Discard, Release: func() error { return nil },
	})
	var interrupted *WorkerInterrupted
	if !errors.As(err, &interrupted) || code != 128+int(syscall.SIGKILL) {
		t.Fatalf("signal exit: %d, %v", code, err)
	}
}

func TestWorkerByteStreamsAndExitWithOpenStdin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw := bytes.Repeat([]byte("a\x00\r\n\xff"), 100000)
	var out, stderr bytes.Buffer
	code, err := RunWorker(ctx, Principal{}, Request{Method: "test.echo"}, Streams{In: bytes.NewReader(raw), Out: &out, Err: &stderr, Release: func() error { return nil }})
	if err != nil || code != 17 || !bytes.Equal(out.Bytes(), raw) || stderr.String() != "stderr\x00tail" {
		t.Fatalf("code=%d err=%v out=%d stderr=%q", code, err, out.Len(), stderr.String())
	}
	read, write := io.Pipe()
	defer func() { _ = read.Close(); _ = write.Close() }()
	out.Reset()
	started := time.Now()
	code, err = RunWorker(ctx, Principal{}, Request{Method: "test.exit"}, Streams{In: read, Out: &out, Err: &stderr, Release: func() error { return nil }})
	if err != nil || code != 0 || out.String() != "final bytes" || time.Since(started) > 3*time.Second {
		t.Fatalf("exit waited for unused stdin: code=%d err=%v time=%s out=%q", code, err, time.Since(started), out.String())
	}
}

func TestWorkerPTYResizeAndFinalOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	input, write := io.Pipe()
	defer func() { _ = input.Close(); _ = write.Close() }()
	sizes := make(chan Size, 1)
	var out bytes.Buffer
	ready := make(chan struct{})
	var once sync.Once
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ready:
		case <-ctx.Done():
			return
		}
		sizes <- Size{120, 40}
		// The subsequent input is independent of SIGWINCH delivery. Wait for
		// the test worker's next read so both paths are exercised separately.
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(write, "\n")
	}()
	code, err := RunWorker(ctx, Principal{}, Request{Method: "test.pty", Terminal: true, Cols: 80, Rows: 24, Term: "xterm-256color"},
		Streams{In: input, Out: &out, Err: io.Discard, Resize: sizes, Release: func() error { once.Do(func() { close(ready) }); return nil }})
	cancel()
	<-done
	if err != nil || code != 0 {
		t.Fatalf("PTY worker: %d %v", code, err)
	}
	if !strings.Contains(out.String(), "80x24\r\n") || !strings.Contains(out.String(), "120x40\r\n") || !strings.HasSuffix(out.String(), strings.Repeat("tail", 16384)) {
		t.Fatalf("lost PTY geometry or trailing output (%d bytes)", out.Len())
	}
}

func TestWorkerCancellationStopsProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	code, err := RunWorker(ctx, Principal{}, Request{Method: "test.cancel"}, Streams{In: strings.NewReader(""), Out: io.Discard, Err: io.Discard, Release: func() error { cancel(); return nil }})
	if code == 0 && err == nil {
		t.Fatal("cancelled worker succeeded")
	}
}
