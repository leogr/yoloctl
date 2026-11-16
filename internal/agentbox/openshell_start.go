package agentbox

import (
	"bytes"
	"context"
	"errors"
)

// NativePreparedStart retains the saved desired policy qualified against a
// private native checkpoint. It is not admission, receipt or launch authority:
// actual Start repeats current ownership/artifact checks and mints new
// credentials through the native SDK. Zero values cannot launch. The enclosing
// native mutation serializes access; it must defer Close.
type NativePreparedStart struct {
	space, name, boxID string
	check              func(context.Context) error
	start              func(context.Context) error
	close              func() error
	consumed           bool
}

func (*NativePreparedStart) String() string     { return "native saved-policy start (private)" }
func (s *NativePreparedStart) GoString() string { return s.String() }
func (s *NativePreparedStart) Check(ctx context.Context, c *Config) error {
	if s == nil || s.check == nil || s.consumed || c == nil || c.Space != s.space || c.Name != s.name || c.ID != s.boxID {
		return errors.New("native prepared Start lacks its original box checkpoint")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.check(ctx)
}
func (s *NativePreparedStart) Start(ctx context.Context, c *Config) error {
	if err := s.Check(ctx, c); err != nil {
		return err
	}
	if s.start == nil {
		return errors.New("native prepared Start has no launcher")
	}
	s.consumed = true // never retry this checkpoint after any attempted launch
	return s.start(ctx)
}
func (s *NativePreparedStart) Close() error {
	if s == nil {
		return nil
	}
	close := s.close
	s.check, s.start, s.close = nil, nil, nil
	s.consumed = true
	if close != nil {
		return close()
	}
	return nil
}

// PrepareNativeStart qualifies the box's saved desired policy against its
// selected native generation before caller session, forward or path effects:
// a new run always launches the saved policy first, never the recorded one.
// observedForwards selects revive's captured observed ports; explicit start
// launches without forwards, then performs its existing desired reconcile.
func (a LiveApplier) PrepareNativeStart(ctx context.Context, c *Config, observedForwards bool) (*NativePreparedStart, bool, error) {
	var prepared *NativePreparedStart
	native, err := a.qualifyNativeRuntimeRoute(ctx, c, func(check context.Context) error {
		var err error
		prepared, err = a.prepareNativeSavedStart(check, c, observedForwards)
		return err
	})
	if err != nil {
		_ = prepared.Close()
		return nil, native, err
	}
	return prepared, native, nil
}

// The checkpoint is original-call provenance and generation/storage/CID
// authority only; the launched policy is the caller's saved desired policy.
// No public request or native admission is supplied by this core. The Linux
// constructor alone captures actual SDK authority.
type nativeStartCheckpoint interface {
	Close() error
}
type nativePreparedStartActions struct {
	qualify  func(context.Context, *Config, []byte) (*Config, error)
	preserve func(context.Context) error
	launch   func(context.Context, *Config, []byte) error
}

// retainNativePreparedStart composes the saved desired policy exactly as
// create and apply compose it; the checkpoint's recorded policy is never read.
func retainNativePreparedStart(ctx context.Context, c *Config, observed bool, checkpoint nativeStartCheckpoint, a nativePreparedStartActions) (*NativePreparedStart, error) {
	if checkpoint == nil {
		return nil, errors.New("prepared Start lacks its original checkpoint")
	}
	retained := false
	defer func() {
		if !retained {
			_ = checkpoint.Close()
		}
	}()
	if c == nil || a.qualify == nil || a.preserve == nil || a.launch == nil {
		return nil, errors.New("prepared Start lacks complete original-call wiring")
	}
	policy, err := ComposePolicy(c)
	if err != nil {
		return nil, err
	}
	if len(policy) == 0 {
		return nil, errors.New("prepared Start lacks the saved policy")
	}
	ports, err := a.qualify(ctx, c, bytes.Clone(policy))
	if err != nil {
		return nil, err
	}
	if ports == nil {
		return nil, errors.New("prepared Start lacks its original observed forward snapshot")
	}
	if err := a.preserve(ctx); err != nil {
		return nil, err
	}
	boot := *c
	boot.Ports = nil
	if observed {
		boot.Ports = append([]Port(nil), ports.Ports...)
	}
	prepared := &NativePreparedStart{space: c.Space, name: c.Name, boxID: c.ID, close: checkpoint.Close}
	prepared.check = func(check context.Context) error { _, err := a.qualify(check, &boot, bytes.Clone(policy)); return err }
	prepared.start = func(start context.Context) error { return a.launch(start, &boot, bytes.Clone(policy)) }
	retained = true
	return prepared, nil
}
