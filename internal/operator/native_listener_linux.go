package operator

import (
	"context"
	"errors"
	"net"
	"sync"
)

// NativeListener is the controller-lifetime single owner of ONE native
// registration's freshly created listener. It composes the private retained
// listener (Close without pathname unlink), the original Accept enrollment and
// the handler service into the public settlement bridge that enabled-operator
// removal needs. It grants no request authority: ServeConn still rechecks the
// live grant and incarnation for every connection.
//
// The native publisher creates it, starts it once with the controller's
// lifetime, and binds it to its original publication claim. Only the original
// removal execution may Retire it, once. Final socket unlink/fsync stays with
// the owned registration withdrawal after the original Stop/rm ACKs.
type NativeListener struct {
	identity RegistrationIdentity
	service  *operatorListenerService

	mu        sync.Mutex
	cancels   map[*operatorServiceConnection]context.CancelFunc
	exempt    *operatorServiceConnection
	retiring  bool
	retired   *NativeListenerRetirement
	retireErr error
}

// NativeListenerRetirement acknowledges the listener Close, the settled
// pending Accept and the return plus successful transport Close of every
// accepted connection except the exact self-target request. It is not a
// Stop/rm/registration-withdrawal/consumer ACK.
type NativeListenerRetirement struct {
	owner    *NativeListener
	proof    *operatorServiceSettlement
	self     *operatorServiceConnection
	selfDone chan struct{}
}

func (*NativeListener) String() string   { return "<native operator listener>" }
func (*NativeListener) GoString() string { return "<native operator listener>" }

// NewNativeListener creates the listener through the publisher's original
// creator. A partial handle is retained (never exposed) with its error.
func NewNativeListener(identity RegistrationIdentity, create func() (*net.UnixListener, error)) (*NativeListener, error) {
	if identity.Mode != RegistrationOpenShellNative {
		return nil, errors.New("native operator listener requires a native registration identity")
	}
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	s, err := newOperatorListenerService(create)
	if s == nil {
		return nil, err
	}
	return &NativeListener{identity: identity, service: s, cancels: map[*operatorServiceConnection]context.CancelFunc{}}, err
}

// Identity is metadata of the original registration, never authority.
func (l *NativeListener) Identity() RegistrationIdentity {
	if l == nil {
		return RegistrationIdentity{}
	}
	return l.identity
}

// Start begins the single original Accept loop. Each handler runs under its
// own cancelable context so retirement can revoke this box's other requests
// without touching the exact self-target execution.
func (l *NativeListener) Start(ctx context.Context, serve func(context.Context, net.Conn)) error {
	if l == nil || serve == nil {
		return errors.New("native operator listener lacks its original handler")
	}
	return l.service.start(ctx, func(parent context.Context, conn net.Conn) {
		c, ok := conn.(*operatorServiceConnection)
		if !ok || c.owner != l.service {
			return
		}
		handler, cancel := context.WithCancel(parent)
		defer cancel()
		l.mu.Lock()
		if l.retiring && c != l.exempt {
			cancel() // enrolled before the seal, registered after the revocation sweep
		}
		l.cancels[c] = cancel
		l.mu.Unlock()
		defer func() {
			l.mu.Lock()
			delete(l.cancels, c)
			l.mu.Unlock()
		}()
		serve(handler, conn)
	})
}

// Retire runs once, for the original removal execution, before the box's
// Stop. It seals enrollment, Closes the listener without unlinking its socket,
// settles the pending Accept, cancels every other accepted request of this
// box and waits for each to return and Close successfully. self, when not nil,
// must be the exact connection that carries the removal request: it is
// neither cancelled nor awaited and may finish its authorized transaction.
// No lock is held across Close or a wait. Failure is sticky and retained.
func (l *NativeListener) Retire(ctx context.Context, self net.Conn) (*NativeListenerRetirement, error) {
	if l == nil {
		return nil, errors.New("native operator listener unavailable")
	}
	var exempt *operatorServiceConnection
	if self != nil {
		c, ok := self.(*operatorServiceConnection)
		if !ok || c.owner != l.service {
			return nil, errors.New("self-target connection does not belong to this native listener")
		}
		exempt = c
	}
	l.mu.Lock()
	if l.retiring {
		err := errors.Join(l.retireErr, errors.New("native operator listener retirement was already attempted"))
		l.mu.Unlock()
		return nil, err
	}
	l.retiring, l.exempt = true, exempt
	l.mu.Unlock()
	r, err := l.retire(ctx, exempt)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.retired, l.retireErr = r, err
	return r, err
}

func (l *NativeListener) retire(ctx context.Context, exempt *operatorServiceConnection) (*NativeListenerRetirement, error) {
	proof, err := l.service.retire(ctx)
	if err != nil {
		return nil, err
	}
	// The Accept loop has returned: handler membership is now complete.
	l.mu.Lock()
	for c, cancel := range l.cancels {
		if c != exempt {
			cancel()
		}
	}
	l.mu.Unlock()
	waits, selfDone, err := l.service.handlerWaits(proof, exempt)
	if err != nil {
		return nil, err
	}
	for _, done := range waits {
		select {
		case <-done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	observations, err := l.service.observe(proof)
	if err != nil {
		return nil, err
	}
	for _, o := range observations {
		if o.connection == exempt {
			continue
		}
		if !o.returned || !o.closeDone || o.closeErr != nil {
			return nil, errors.Join(o.closeErr, errors.New("native operator request did not settle"))
		}
	}
	return &NativeListenerRetirement{owner: l, proof: proof, self: exempt, selfDone: selfDone}, nil
}

// SelfPending reports whether the exact self-target request is still running.
func (r *NativeListenerRetirement) SelfPending() bool {
	if r == nil || r.selfDone == nil {
		return false
	}
	select {
	case <-r.selfDone:
		return false
	default:
		return true
	}
}

// WaitSelf waits for the self-target handler's return and checks its one
// transport Close. Controller shutdown uses it; removal never waits on itself.
func (r *NativeListenerRetirement) WaitSelf(ctx context.Context) error {
	if r == nil || r.owner == nil {
		return errors.New("native operator retirement unavailable")
	}
	if r.selfDone == nil {
		return nil
	}
	select {
	case <-r.selfDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	observations, err := r.owner.service.observe(r.proof)
	if err != nil {
		return err
	}
	for _, o := range observations {
		if o.connection == r.self && (!o.returned || !o.closeDone || o.closeErr != nil) {
			return errors.Join(o.closeErr, errors.New("native operator self-target request did not settle"))
		}
	}
	return nil
}

// handlerWaits returns the return channels of every handler except exempt,
// plus exempt's own channel. The exempt connection must be a current member.
func (s *operatorListenerService) handlerWaits(proof *operatorServiceSettlement, exempt *operatorServiceConnection) ([]chan struct{}, chan struct{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if proof == nil || proof.owner != s || !s.retired || s.handlers == nil {
		return nil, nil, errors.New("operator handler waits lack their original settlement")
	}
	var waits []chan struct{}
	var self chan struct{}
	for c, state := range s.handlers {
		if c == nil || state == nil || state.returnedNow == nil {
			return nil, nil, errors.New("operator service handler membership changed")
		}
		if c == exempt {
			self = state.returnedNow
			continue
		}
		waits = append(waits, state.returnedNow)
	}
	if exempt != nil && self == nil {
		return nil, nil, errors.New("self-target connection is not an accepted request of this listener")
	}
	return waits, self, nil
}

// RetireListener is Retire behind the portable controller interface.
func (l *NativeListener) RetireListener(ctx context.Context, self net.Conn) (NativeRetirement, error) {
	r, err := l.Retire(ctx, self)
	if r == nil {
		return nil, err
	}
	return r, err
}

var _ NativeListenerOwner = (*NativeListener)(nil)
