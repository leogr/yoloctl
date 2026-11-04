package agentbox

import (
	"bytes"
	"context"
	"errors"
)

// NativeRecordedStart retains a private original-policy/environment checkpoint
// and the caller's pre-withdrawal forward intent. It is not admission, receipt
// or launch authority: actual Start repeats current ownership/artifact checks
// and mints new credentials through the native SDK. Zero values cannot launch.
// The enclosing native mutation serializes access; it must defer Close.
type NativeRecordedStart struct {
	space, name, boxID string
	check              func(context.Context) error
	start              func(context.Context) error
	close              func() error
	consumed           bool
}

func (*NativeRecordedStart) String() string     { return "native recorded-policy start (private)" }
func (s *NativeRecordedStart) GoString() string { return s.String() }
func (s *NativeRecordedStart) Check(ctx context.Context, c *Config) error {
	if s == nil || s.check == nil || s.consumed || c == nil || c.Space != s.space || c.Name != s.name || c.ID != s.boxID {
		return errors.New("native recorded Start lacks its original box checkpoint")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.check(ctx)
}
func (s *NativeRecordedStart) Start(ctx context.Context, c *Config) error {
	if err := s.Check(ctx, c); err != nil {
		return err
	}
	if s.start == nil {
		return errors.New("native recorded Start has no original launcher")
	}
	s.consumed = true // never retry this checkpoint after any attempted launch
	return s.start(ctx)
}
func (s *NativeRecordedStart) Close() error {
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

// PrepareNativeStart reads/qualifies the recorded policy before caller session,
// forward or path effects. Legacy is lazy and acquires no new policy requirement.
// observedForwards selects revive's captured observed ports; explicit start
// launches without forwards, then performs its existing desired reconcile.
func (a LiveApplier) PrepareNativeStart(ctx context.Context, c *Config, observedForwards bool) (*NativeRecordedStart, bool, error) {
	var prepared *NativeRecordedStart
	native, err := a.qualifyNativeRuntimeRoute(ctx, c, func(check context.Context) error {
		var err error
		prepared, err = a.prepareNativeRecordedStart(check, c, observedForwards)
		return err
	})
	if err != nil {
		_ = prepared.Close()
		return nil, native, err
	}
	return prepared, native, nil
}

// WithDeclaredForwardRestoration carries explicit start/restart's declaration
// captured BEFORE withdrawal. Ordinary running-policy reconcile leaves this
// unset and retains its original observed inventory. This is copied data only.
func (a LiveApplier) WithDeclaredForwardRestoration(c *Config) LiveApplier {
	if c != nil {
		a.nativeForwardRestoration = &nativeForwardRestoration{space: c.Space, name: c.Name, boxID: c.ID, ports: append([]Port(nil), c.Ports...)}
	}
	return a
}

type nativeForwardRestoration struct {
	space, name, boxID string
	ports              []Port
}

func (a LiveApplier) nativeRollbackConfig(c, observed *Config) (*Config, error) {
	intent := a.nativeForwardRestoration
	if intent == nil {
		return observed, nil
	}
	if c == nil || c.Space != intent.space || c.Name != intent.name || c.ID != intent.boxID {
		return nil, errors.New("native rollback forward intent belongs to another box")
	}
	result := *c
	result.Ports = append([]Port(nil), intent.ports...)
	return &result, nil
}

// Original-call wiring only; no public request or native admission is supplied
// by this core. The Linux constructor alone captures actual SDK authority.
type nativeRecordedCheckpoint interface {
	OriginalPolicy() []byte
	Close() error
}
type nativeRecordedStartActions struct {
	qualify  func(context.Context, *Config, []byte) (*Config, error)
	preserve func(context.Context) error
	launch   func(context.Context, *Config, []byte) error
}

func retainNativeRecordedStart(ctx context.Context, c *Config, observed bool, checkpoint nativeRecordedCheckpoint, a nativeRecordedStartActions) (*NativeRecordedStart, error) {
	if checkpoint == nil {
		return nil, errors.New("recorded Start lacks its original checkpoint")
	}
	retained := false
	defer func() {
		if !retained {
			_ = checkpoint.Close()
		}
	}()
	if c == nil || a.qualify == nil || a.preserve == nil || a.launch == nil {
		return nil, errors.New("recorded Start lacks complete original-call wiring")
	}
	policy := bytes.Clone(checkpoint.OriginalPolicy())
	if len(policy) == 0 {
		return nil, errors.New("recorded Start lacks original policy")
	}
	ports, err := a.qualify(ctx, c, bytes.Clone(policy))
	if err != nil {
		return nil, err
	}
	if ports == nil {
		return nil, errors.New("recorded Start lacks its original observed forward snapshot")
	}
	if err := a.preserve(ctx); err != nil {
		return nil, err
	}
	boot := *c
	boot.Ports = nil
	if observed {
		boot.Ports = append([]Port(nil), ports.Ports...)
	}
	prepared := &NativeRecordedStart{space: c.Space, name: c.Name, boxID: c.ID, close: checkpoint.Close}
	prepared.check = func(check context.Context) error { _, err := a.qualify(check, &boot, bytes.Clone(policy)); return err }
	prepared.start = func(start context.Context) error { return a.launch(start, &boot, bytes.Clone(policy)) }
	retained = true
	return prepared, nil
}
