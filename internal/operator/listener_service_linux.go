package operator

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

// operatorListenerService is the sole original Accept/Close owner for one
// freshly created listener. It binds every accounting attempt to that exact
// listener's concrete AcceptUnix result. Parent registration/origin/receipt,
// publication permission and final unlink remain separate caller obligations.
// No generic/legacy publisher calls this private service yet.
type operatorListenerService struct {
	mu         sync.Mutex
	listener   *retainedOperatorListener
	concrete   *net.UnixListener
	enrollment *operatorAcceptEnrollment
	handlers   map[*operatorServiceConnection]*operatorServiceHandler
	started    bool
	retiring   bool
	retired    bool
	loopDone   chan struct{}
	startErr   error
	retireErr  error
	// Private deterministic scheduling cuts; production leaves these nil.
	beforeAccept func()
	afterAccept  func()
	afterEnroll  func()
}

type operatorServiceHandler struct {
	returned    bool
	returnedNow chan struct{}
	closeDone   bool
	closeErr    error
}

// A transport Close and a ServeConn return do not settle detached workers,
// admitted requests, responses or complete kernel consumers. Preserve them as
// distinct observations, including handlers whose transport is already closed.
type operatorServiceObservation struct {
	connection *operatorServiceConnection
	returned   bool
	closeDone  bool
	closeErr   error
}

type operatorServiceConnection struct {
	connection net.Conn
	owner      *operatorListenerService
	transport  *operatorAcceptedTransport
	once       sync.Once
	err        error
}

func (c *operatorServiceConnection) Read(p []byte) (int, error)  { return c.connection.Read(p) }
func (c *operatorServiceConnection) Write(p []byte) (int, error) { return c.connection.Write(p) }
func (c *operatorServiceConnection) LocalAddr() net.Addr         { return c.connection.LocalAddr() }
func (c *operatorServiceConnection) RemoteAddr() net.Addr        { return c.connection.RemoteAddr() }
func (c *operatorServiceConnection) SetDeadline(t time.Time) error {
	return c.connection.SetDeadline(t)
}
func (c *operatorServiceConnection) SetReadDeadline(t time.Time) error {
	return c.connection.SetReadDeadline(t)
}
func (c *operatorServiceConnection) SetWriteDeadline(t time.Time) error {
	return c.connection.SetWriteDeadline(t)
}

// ServeConn legitimately closes from cancellation and its final defer. Both
// calls share the ONE actual FD operation and its retained result; subsequent
// calls cannot repair a failed Close or create another acknowledgement.
func (c *operatorServiceConnection) Close() error {
	c.once.Do(func() {
		c.err = c.owner.enrollment.closeTransport(c.transport)
		c.owner.mu.Lock()
		state := c.owner.handlers[c]
		state.closeDone, state.closeErr = true, c.err
		c.owner.mu.Unlock()
	})
	return c.err
}

func newOperatorListenerService(create func() (*net.UnixListener, error)) (*operatorListenerService, error) {
	l, err := newRetainedOperatorListener(create)
	if l == nil {
		return nil, err
	}
	s := &operatorListenerService{listener: l, handlers: map[*operatorServiceConnection]*operatorServiceHandler{}, loopDone: make(chan struct{}), startErr: err}
	if err != nil {
		return s, err // original partial handle remains retained and unexposed
	}
	s.enrollment, err = newOperatorAcceptEnrollment(l)
	s.startErr = err
	if err == nil {
		// Retain the original concrete listener. The wrapper's post-Close
		// refusal is not the actual result of this pending Accept attempt.
		l.mu.Lock()
		s.concrete = l.listener
		l.mu.Unlock()
	}
	return s, err
}

// start runs only after the future publisher has qualified/reserved this exact
// target and captured its strict immutable member snapshot. The first pending
// Accept belongs to this service before its readiness result. Controller
// lifetime belongs to ctx; the creating worker's lifetime must not be used.
// Cancellation does not silently Close/unlink this infrastructure listener.
func (s *operatorListenerService) start(ctx context.Context, serve func(context.Context, net.Conn)) error {
	if s == nil || serve == nil {
		return errors.New("operator listener service lacks its original handler")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.listener == nil || s.concrete == nil || s.enrollment == nil || s.handlers == nil || s.started || s.retiring || s.startErr != nil {
		err := errors.Join(s.startErr, errors.New("operator listener service is unavailable or already started"))
		s.mu.Unlock()
		return err
	}
	a, err := s.enrollment.beginAccept()
	if err != nil {
		s.startErr = err
		s.mu.Unlock()
		return err
	}
	s.started = true
	ready := make(chan struct{})
	s.mu.Unlock()
	go s.acceptLoop(ctx, a, serve, ready)
	select {
	case <-ready:
	case <-ctx.Done():
	}
	if err := ctx.Err(); err != nil {
		s.mu.Lock()
		s.startErr = err
		s.mu.Unlock()
		return err
	}
	return nil
}

func (s *operatorListenerService) acceptLoop(ctx context.Context, a *operatorAcceptAttempt, serve func(context.Context, net.Conn), ready chan struct{}) {
	defer close(s.loopDone)
	close(ready)
	for {
		// Neither a global/publisher mutex nor a native mutation lock spans
		// Accept, FD Close or a handler wait. No supplied FD/result can enter
		// this path: it always comes from this original concrete listener.
		if s.beforeAccept != nil {
			s.beforeAccept()
		}
		// Even if owned Close lands before this call, consume this exact
		// concrete listener's real AcceptUnix result. Enrollment separately
		// requires the original successful Close; generic/mixed failures are
		// never reinterpreted as ErrClosed or a later acknowledgement.
		connection, acceptErr := s.concrete.AcceptUnix()
		if s.afterAccept != nil {
			s.afterAccept()
		}
		e, err := s.enrollment.finishAccept(a, connection, acceptErr)
		if err != nil {
			return // enrollment retains every failed/mixed result and Close
		}
		if s.afterEnroll != nil {
			s.afterEnroll()
		}
		c := &operatorServiceConnection{connection: connection, owner: s, transport: e}
		s.mu.Lock()
		s.handlers[c] = &operatorServiceHandler{returnedNow: make(chan struct{})}
		retiring := s.retiring
		if retiring {
			// This result was enrolled just before the seal but has not been
			// exposed to a handler. Dispose it without admitting a request.
			s.handlers[c].returned = true
			close(s.handlers[c].returnedNow)
		}
		s.mu.Unlock()
		if retiring {
			_ = c.Close() // the actual result remains recorded, including failure
			return
		}
		go func() {
			// This is observation of the actual handler return, not a worker
			// completion ACK. A panic still runs these defers before propagating.
			defer func() {
				_ = c.Close()
				s.mu.Lock()
				s.handlers[c].returned = true
				close(s.handlers[c].returnedNow)
				s.mu.Unlock()
			}()
			serve(ctx, c)
		}()
		a, err = s.enrollment.beginAccept()
		if err != nil {
			return
		}
	}
}

type operatorServiceSettlement struct {
	owner  *operatorListenerService
	accept *operatorListenerAcceptSettlement
}

// retire requires original qualified effect permission from the publisher.
// Seal handler enrollment, then Close and settle the exact Accept loop. Do not
// wait for any handler/worker here: the initiating request may target itself
// while holding the mutation guard that another handler needs. No unlink.
func (s *operatorListenerService) retire(ctx context.Context) (*operatorServiceSettlement, error) {
	if s == nil {
		return nil, errors.New("operator listener service unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.enrollment == nil || s.handlers == nil || !s.started || s.retiring {
		err := errors.Join(s.retireErr, s.startErr, errors.New("operator listener service cannot retire or was already consumed"))
		s.mu.Unlock()
		return nil, err
	}
	s.retiring = true
	s.mu.Unlock()
	settlement, err := s.enrollment.retireListener(ctx)
	if err == nil {
		select {
		case <-s.loopDone:
		case <-ctx.Done():
		}
		err = ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err = errors.Join(err, s.startErr)
	s.retireErr = err
	if err != nil {
		return nil, err
	}
	s.retired = true
	return &operatorServiceSettlement{owner: s, accept: settlement}, nil
}

// observe returns complete service-owned handler observations after its loop
// has actually ended, including closed transports with unreturned handlers.
// It grants no request/worker/response/native-consumer completion authority.
func (s *operatorListenerService) observe(proof *operatorServiceSettlement) ([]operatorServiceObservation, error) {
	if s == nil {
		return nil, errors.New("operator listener service unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if proof == nil || proof.owner != s || !s.retired || s.retireErr != nil || s.startErr != nil || s.handlers == nil {
		return nil, errors.New("operator service observation lacks its original settlement")
	}
	select {
	case <-s.loopDone:
	default:
		return nil, errors.New("operator service Accept loop has not returned")
	}
	if _, err := s.enrollment.observeTransports(proof.accept); err != nil {
		return nil, err
	}
	result := make([]operatorServiceObservation, 0, len(s.handlers))
	for c, state := range s.handlers {
		if c == nil || c.owner != s || c.transport == nil || state == nil {
			return nil, errors.New("operator service handler membership changed")
		}
		result = append(result, operatorServiceObservation{c, state.returned, state.closeDone, state.closeErr})
	}
	return result, nil
}
