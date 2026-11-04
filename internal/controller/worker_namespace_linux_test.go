package controller

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/termio"
	"golang.org/x/sys/unix"
)

func workerPIDNamespaceTestWorker(released *os.File, work WorkerInput, namespace *WorkerPIDNamespace) {
	exit := func(code int) { _, _ = released.Write([]byte{checkpointFinish}); os.Exit(code) }
	if namespace == nil || namespace.file == nil {
		exit(93)
	}
	flags, err := unix.FcntlInt(namespace.file.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		exit(94)
	}
	checkpoint := NewWorkerCheckpoints(released)
	checkpoint.Gate()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := checkpoint.BeginEffects(ctx, EffectIntent{Scope: &Scope{Writes: []Resource{{Kind: ResourceBox, Name: "box"}}}}); err != nil {
		exit(95)
	}
	if err := checkpoint.CheckPIDAnchor(ctx, "box"); err != nil {
		exit(96)
	}
	// No raw fork, sandbox or VM launch: ordinary project child verifies this
	// inherited observation descriptor does not pass through a later exec.
	if err := exec.Command("/bin/sh", "-c", "test ! -e /proc/self/fd/"+strconv.Itoa(work.PIDNamespaceFD)).Run(); err != nil {
		exit(97)
	}
	_, _ = fmt.Fprintf(os.Stdout, "namespace-index=%d close-on-exec=yes", work.PIDNamespaceFD)
	_ = namespace.Close()
	exit(0)
}

func TestWorkerPIDNamespaceRealPrivateTransportUsesDynamicIndexAndOriginalCheck(t *testing.T) {
	for _, withView := range []bool{false, true} {
		t.Run(strconv.FormatBool(withView), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			var checked atomic.Int32
			// Actual calling-process namespace validates only descriptor
			// transport here. It is explicitly NOT independent VM admission.
			anchor := fixtureWorkerPIDAnchor{check: func() error { return nil }, duplicate: func() (*os.File, error) { return os.Open("/proc/self/ns/pid") }}
			var out, stderr bytes.Buffer
			streams := Streams{In: strings.NewReader(""), Out: &out, Err: &stderr, PIDAnchor: anchor,
				BeginEffects: func(context.Context, EffectIntent) error { return nil },
				PIDAnchorCheck: func(_ context.Context, box string) error {
					if box != "box" {
						t.Error("box scope changed")
					}
					checked.Add(1)
					return nil
				},
			}
			if withView {
				client, worker, err := termio.ViewPair()
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = client.Close(); _ = worker.Close() }()
				streams.View = worker
				go func() { _, _ = io.Copy(io.Discard, client) }()
			}
			code, err := RunWorker(ctx, Principal{}, Request{Method: "test.pid-namespace", View: withView}, streams)
			index := 5
			if withView {
				index = 6
			}
			if err != nil || code != 0 || checked.Load() != 1 || out.String() != fmt.Sprintf("namespace-index=%d close-on-exec=yes", index) {
				t.Fatal(code, err, checked.Load(), out.String(), stderr.String())
			}
		})
	}
}

func TestWorkerPIDNamespaceUnavailableOptionalProofPreservesOrdinaryResult(t *testing.T) {
	for _, mode := range []string{"check", "duplicate", "foreign-file"} {
		t.Run(mode, func(t *testing.T) {
			anchor := fixtureWorkerPIDAnchor{check: func() error {
				if mode == "check" {
					return fmt.Errorf("source unavailable")
				}
				return nil
			}, duplicate: func() (*os.File, error) {
				if mode == "duplicate" {
					return nil, fmt.Errorf("duplicate unavailable")
				}
				return os.Open("/dev/null")
			}}
			var out, stderr bytes.Buffer
			code, err := RunWorker(t.Context(), Principal{}, Request{Method: "test.exit"}, Streams{In: strings.NewReader(""), Out: &out, Err: &stderr, PIDAnchor: anchor})
			if err != nil || code != 0 || out.String() != "final bytes" {
				t.Fatal("optional observation rewrote original command result", code, err, out.String())
			}
		})
	}
}

func TestWorkerPIDNamespaceRawRefusalLeavesUnrelatedFDOpen(t *testing.T) {
	file, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := ClaimWorkerPIDNamespace(WorkerInput{PIDNamespaceFD: int(file.Fd())}); err == nil {
		t.Fatal("unrelated file claimed")
	}
	if _, err := file.Stat(); err != nil {
		t.Fatal("raw refusal closed unrelated descriptor", err)
	}
	if _, err := ClaimWorkerPIDNamespace(WorkerInput{PIDNamespaceFD: 3}); err == nil {
		t.Fatal("private request slot could be claimed as namespace")
	}
}
