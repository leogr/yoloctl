package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/leogr/yoloctl/internal/spaceenv"
)

// verifierLockMarker is the first byte a verifier writes on its result
// descriptor, once it holds the space lock and its observation starts.
const verifierLockMarker byte = 0x06

// errObservationNotStarted: the caller's context ended while the verifier was
// still waiting for the space lock, so nothing was observed and nothing is
// recorded; a verifier that ends by itself before the lock is an ordinary error.
var errObservationNotStarted = errors.New("observation not started: the recovery pass ended while the verifier waited for the space lock")

// verifierCommand is the verifier's fixed argv after the executable; tests
// replace it to reach stand-in behaviours that never read their input.
var verifierCommand = []string{"controller-internal", "verify"}

// verifierStarted runs between the verifier's start and its input write; tests
// use it to order a cancellation before that write.
var verifierStarted = func() {}

// MarkVerifierObservation is written by the verifier right after it acquires
// the space lock: the controller's fail-safe for one observation starts there.
func MarkVerifierObservation(out io.Writer) error {
	_, err := out.Write([]byte{verifierLockMarker})
	return err
}

// DocumentRef names one document a verifier reads back.
type DocumentRef struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
}

// VerifierInput is the verifier worker's typed descriptor, sent only over a
// private inherited pipe. It carries no fingerprint, key or payload.
type VerifierInput struct {
	Space     string        `json:"space"`
	Verifier  string        `json:"verifier"`
	Documents []DocumentRef `json:"documents"`
	Boxes     []string      `json:"boxes,omitempty"`
	DiskID    string        `json:"disk_id,omitempty"`
}

// Validate admits only the reviewed verifier allowlist.
func (in VerifierInput) Validate(space string) error {
	if in.Space != space || len(in.Documents)+len(in.Boxes) == 0 || len(in.Documents) > maxProofs || len(in.Boxes) > maxScopeResources {
		return errors.New("invalid verifier request")
	}
	if len(in.Boxes) != 0 {
		if in.DiskID == "" || len(in.Documents) != 0 {
			return errors.New("invalid container observation request")
		}
		for _, name := range in.Boxes {
			if err := (Resource{Kind: ResourceBox, Name: name}).validate(); err != nil {
				return err
			}
		}
	}
	for _, d := range in.Documents {
		if err := (DocumentIntent{Kind: d.Kind, Name: d.Name, Present: true}).validate(in.Verifier); err != nil {
			return err
		}
	}
	return nil
}

// verifierInput describes what a receipt's proofs require to be read.
func verifierInput(space string, r Receipt) VerifierInput {
	in := VerifierInput{Space: space, Verifier: r.Verifier}
	for _, p := range r.Proofs {
		in.Documents = append(in.Documents, DocumentRef{Kind: p.Kind, Name: p.Name})
	}
	return in
}

// RunVerifier executes the exact-build read-only verifier with fixed argv.
// It returns the observed documents in proof order. The worker receives its
// descriptor and returns its observations over private descriptors; it is
// not a registered operation and has no public RPC. Its process group is
// killed when ctx ends or, once it holds the space lock, when the 2-minute
// observation fail-safe expires.
func RunVerifier(ctx context.Context, space string, r Receipt) ([]DocumentIntent, error) {
	raw, err := runVerifier(ctx, space, verifierInput(space, r))
	if err != nil {
		return nil, err
	}
	var observed []DocumentIntent
	if err := decodeStrict(raw, &observed); err != nil || len(observed) != len(r.Proofs) {
		return nil, errors.New("invalid verifier output")
	}
	return observed, nil
}

// RunBoxObserver uses the same bounded private verifier transport. The controller
// has already checked the recorded disk/scope; the worker rechecks the disk.
func RunBoxObserver(ctx context.Context, space string, r Receipt) ([]BoxObservation, error) {
	names, reason := observationBoxes(r, r.DiskID)
	if reason != "" {
		return nil, errors.New(reason)
	}
	raw, err := runVerifier(ctx, space, VerifierInput{Space: space, Boxes: names, DiskID: r.DiskID})
	if err != nil {
		return nil, err
	}
	var boxes []BoxObservation
	if err := decodeStrict(raw, &boxes); err != nil || len(boxes) != len(names) {
		return nil, errors.New("invalid container observation output")
	}
	return boxes, nil
}

func runVerifier(ctx context.Context, space string, in VerifierInput) ([]byte, error) {
	if err := in.Validate(space); err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	inRead, inWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer func() { _ = inRead.Close(); _ = inWrite.Close() }()
	outRead, outWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer func() { _ = outRead.Close(); _ = outWrite.Close() }()
	// The lock wait follows ctx alone. The fail-safe for this one observation
	// starts at the verifier's marker, once it holds the space lock: a long
	// operation that owns the lock says nothing about the interrupted one.
	child, cancelChild := context.WithCancel(ctx)
	defer cancelChild()
	cmd := exec.CommandContext(child, executable, verifierCommand...)
	cmd.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root", "LANG=C.UTF-8", spaceenv.ContextEnv + "=" + spaceenv.ContextPath,
	}
	cmd.ExtraFiles = []*os.File{inRead, outWrite}
	var diagnostics bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &diagnostics, n: 4096}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(errObservationNotStarted, err)
	}
	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return nil, errors.Join(errObservationNotStarted, err)
		}
		return nil, err
	}
	_ = inRead.Close()
	_ = outWrite.Close()
	verifierStarted()
	encodeErr := json.NewEncoder(inWrite).Encode(in)
	_ = inWrite.Close()
	var mark [1]byte
	n, _ := io.ReadFull(outRead, mark[:])
	started := n == 1 && mark[0] == verifierLockMarker
	var expired atomic.Bool
	var raw []byte
	var readErr error
	var failSafe *time.Timer
	if started {
		// Armed until the verifier is reaped: it holds the space lock until it ends.
		failSafe = time.AfterFunc(recoveryObservationFailSafe, func() { expired.Store(true); cancelChild() })
		raw, readErr = io.ReadAll(io.LimitReader(outRead, MaxFrame+1))
		if readErr != nil || len(raw) > MaxFrame {
			// Nothing more is read: end a verifier that may still be writing,
			// and holding the lock, before reaping it.
			cancelChild()
		}
	}
	waitErr := cmd.Wait()
	if failSafe != nil {
		failSafe.Stop()
	}
	// A verifier's own children never outlive its check.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	switch {
	case !started && ctx.Err() != nil && killedBySignal(waitErr):
		// Cancelled while it waited for the lock, possibly before it read its
		// input (a broken pipe then): nothing was observed.
		return nil, errors.Join(errObservationNotStarted, ctx.Err())
	case encodeErr != nil:
		return nil, encodeErr
	case expired.Load():
		return nil, fmt.Errorf("observation did not finish within its %s fail-safe", recoveryObservationFailSafe)
	case len(raw) > MaxFrame:
		return nil, errors.New("verifier output exceeds its limit")
	case readErr != nil:
		return nil, readErr
	case waitErr != nil:
		return nil, errors.Join(waitErr, errors.New(diagnostics.String()))
	case !started:
		return nil, errors.New("verifier output lacks its observation marker")
	}
	return raw, nil
}

// killedBySignal reports a verifier ended by a signal, as its cancellation does.
func killedBySignal(err error) bool {
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return false
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	return ok && status.Signaled()
}

// limitedWriter keeps a bounded prefix of diagnostics.
type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		_, _ = l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}
