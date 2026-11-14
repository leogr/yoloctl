package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/leogr/yoloctl/internal/spaceenv"
)

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
// killed when ctx ends.
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
	cmd := exec.CommandContext(ctx, executable, "controller-internal", "verify")
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
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	_ = inRead.Close()
	_ = outWrite.Close()
	encodeErr := json.NewEncoder(inWrite).Encode(in)
	_ = inWrite.Close()
	raw, readErr := io.ReadAll(io.LimitReader(outRead, MaxFrame+1))
	waitErr := cmd.Wait()
	// A verifier's own children never outlive its check.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	switch {
	case encodeErr != nil:
		return nil, encodeErr
	case readErr != nil:
		return nil, readErr
	case waitErr != nil:
		return nil, errors.Join(waitErr, errors.New(diagnostics.String()))
	case len(raw) > MaxFrame:
		return nil, errors.New("verifier output exceeds its limit")
	}
	return raw, nil
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
