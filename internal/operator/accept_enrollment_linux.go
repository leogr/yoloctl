package operator

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
)

// operatorAcceptEnrollment accounts for one original publisher's single
// Accept loop and every returned transport. It is not a registration, RPC
// admission, request/worker/response lifetime or complete consumer proof.
// Actual native lease/effect permission must precede retireListener. The
// future publisher must be this listener's sole Accept/Close owner.
type operatorAcceptEnrollment struct {
	mu             sync.Mutex
	listener       *retainedOperatorListener
	pending        *operatorAcceptAttempt
	transports     map[*operatorAcceptedTransport]bool
	connections    map[*net.UnixConn]*operatorAcceptedTransport
	sealed         bool
	closeDone      bool
	closeACK       bool
	retired        bool
	failure        error
	sealedNow      chan struct{}
	listenerClosed chan struct{}
	settled        chan struct{}
	signalled      bool
}

type operatorAcceptAttempt struct {
	owner    *operatorAcceptEnrollment
	consumed bool
}

type operatorAcceptedTransport struct {
	owner          *operatorAcceptEnrollment
	connection     *net.UnixConn
	closeAttempted bool
	closeDone      bool
	closeFailure   error
}

// This token covers the listener Close and original pending Accept results
// ONLY. The separately observed transports and their actual request/worker/
// stream disposition remain required; even an empty slice grants no authority.
type operatorListenerAcceptSettlement struct{ owner *operatorAcceptEnrollment }

func newOperatorAcceptEnrollment(l *retainedOperatorListener) (*operatorAcceptEnrollment, error) {
	if l == nil {
		return nil, errors.New("operator enrollment lacks its original listener")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.listener == nil || l.closeFD == nil || l.creationError != nil || l.closeAttempted {
		return nil, errors.New("operator enrollment requires its fresh unexposed listener")
	}
	return &operatorAcceptEnrollment{listener: l, transports: map[*operatorAcceptedTransport]bool{}, connections: map[*net.UnixConn]*operatorAcceptedTransport{}, sealedNow: make(chan struct{}), listenerClosed: make(chan struct{}), settled: make(chan struct{})}, nil
}

func (p *operatorAcceptEnrollment) beginAccept() (*operatorAcceptAttempt, error) {
	if p == nil {
		return nil, errors.New("operator accept owner is unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.listener == nil || p.transports == nil || p.connections == nil || p.sealed || p.failure != nil || p.pending != nil {
		return nil, errors.Join(p.failure, errors.New("operator accept owner is closed/unavailable or already accepting"))
	}
	a := &operatorAcceptAttempt{owner: p}
	p.pending = a
	return a, nil
}

// finishAccept serializes enrollment with retirement. A result crossing the
// seal is never exposed: retain its original pending attempt until the actual
// returned connection's Close finishes. Unknown/replayed tokens never close a
// supplied foreign connection. Actual source must pair each begin with its
// concrete listener's one AcceptUnix result, not helper-reported metadata.
func (p *operatorAcceptEnrollment) finishAccept(a *operatorAcceptAttempt, c *net.UnixConn, acceptErr error) (*operatorAcceptedTransport, error) {
	if p == nil {
		return nil, errors.New("operator accept owner is unavailable")
	}
	p.mu.Lock()
	if a == nil || a.owner != p || a.consumed || p.pending != a {
		p.mu.Unlock()
		return nil, errors.New("operator Accept result lacks its original pending attempt")
	}
	a.consumed = true
	if c == nil {
		p.pending = nil
		if acceptErr == nil {
			acceptErr = errors.New("operator Accept returned neither connection nor error")
		}
		if !p.sealed || !originalOperatorAcceptClosed(acceptErr) {
			p.failure = errors.Join(p.failure, acceptErr)
		}
		p.signal()
		p.mu.Unlock()
		return nil, acceptErr
	}
	if p.connections[c] != nil {
		// This pointer already belongs to an earlier original Accept. Do not
		// dispose somebody else's enrolled transport under the new attempt.
		p.pending = nil
		err := errors.New("operator Accept repeated an already owned transport")
		p.failure = errors.Join(p.failure, err)
		p.signal()
		p.mu.Unlock()
		return nil, err
	}
	e := &operatorAcceptedTransport{owner: p, connection: c}
	if p.sealed || p.failure != nil || acceptErr != nil {
		// This temporary enrolled record retains the concrete FD while it is
		// being disposed. No observer can mistake a pending Close for absence.
		p.transports[e] = true
		p.connections[c] = e
		e.closeAttempted = true
		p.mu.Unlock()
		closeErr := c.Close()
		p.mu.Lock()
		e.closeDone = true
		e.closeFailure = closeErr
		p.pending = nil
		if closeErr == nil {
			delete(p.transports, e)
			delete(p.connections, c)
		}
		p.failure = errors.Join(p.failure, acceptErr, closeErr)
		p.signal()
		p.mu.Unlock()
		return nil, errors.Join(acceptErr, closeErr, errors.New("operator Accept was not enrolled before retirement"))
	}
	p.transports[e] = true
	p.connections[c] = e
	p.pending = nil
	p.signal()
	p.mu.Unlock()
	return e, nil
}

// Only the actual Unix Accept's ordinary single error chain is expected after
// the owned listener Close. Joined errors or custom Is matches cannot conceal
// another failed result behind ErrClosed. The same compiled Go implementation
// supplies these wrappers; unexpected shapes withhold settlement.
func originalOperatorAcceptClosed(err error) bool {
	for range 4 {
		if err == net.ErrClosed {
			return true
		}
		switch e := err.(type) {
		case *net.OpError:
			if e == nil {
				return false
			}
			err = e.Err
		case *os.SyscallError:
			if e == nil {
				return false
			}
			err = e.Err
		default:
			return false
		}
	}
	return false
}

// signal runs under mu. Failure completion is never success evidence; it only
// lets the original waiter observe the sticky error without deadlocking.
func (p *operatorAcceptEnrollment) signal() {
	if !p.signalled && p.closeDone && (p.pending == nil || p.failure != nil) {
		p.signalled = true
		close(p.settled)
	}
}

// retireListener requires original qualified effect permission from the future
// native publisher/worker lease. It changes service state and must never be
// called as read-only reservation. No global/publisher lock spans Close or a
// wait, and no accepted request/worker is waited on here.
func (p *operatorAcceptEnrollment) retireListener(ctx context.Context) (*operatorListenerAcceptSettlement, error) {
	if p == nil {
		return nil, errors.New("operator accept owner is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.listener == nil || p.transports == nil || p.connections == nil || p.sealed || p.failure != nil {
		err := errors.Join(p.failure, errors.New("operator listener retirement is unavailable or already attempted"))
		p.mu.Unlock()
		return nil, err
	}
	p.sealed = true
	close(p.sealedNow)
	p.mu.Unlock()
	err := p.listener.Close()
	p.mu.Lock()
	p.closeDone = true
	p.closeACK = err == nil
	p.failure = errors.Join(p.failure, err)
	p.signal()
	close(p.listenerClosed) // Close result is recorded; success still needs all Accept dispositions.
	p.mu.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case <-p.settled:
	case <-ctx.Done():
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		p.failure = errors.Join(p.failure, err)
		p.signal()
		return nil, err
	}
	if p.failure != nil {
		return nil, p.failure
	}
	if !p.closeDone || !p.closeACK || p.pending != nil {
		return nil, errors.New("operator listener/Accept results remain unsettled")
	}
	p.retired = true
	return &operatorListenerAcceptSettlement{owner: p}, nil
}

func (p *operatorAcceptEnrollment) observeTransports(s *operatorListenerAcceptSettlement) ([]*operatorAcceptedTransport, error) {
	if p == nil {
		return nil, errors.New("operator accept owner is unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if s == nil || s.owner != p || !p.retired || !p.closeACK || !p.closeDone || p.pending != nil || p.failure != nil || p.transports == nil || p.connections == nil {
		return nil, errors.Join(p.failure, errors.New("operator transports lack their original listener/Accept settlement"))
	}
	result := make([]*operatorAcceptedTransport, 0, len(p.transports))
	for e := range p.transports {
		if e == nil || e.owner != p || e.connection == nil || p.connections[e.connection] != e || e.closeDone && e.closeFailure == nil {
			return nil, errors.New("operator transport observation has inconsistent original membership")
		}
		result = append(result, e)
	}
	return result, nil
}

// closeTransport acknowledges only the concrete transport FD Close. It does
// not prove a handler, detached worker, request, response or application effect
// completed. The future actual server owner must track those lifetimes too.
func (p *operatorAcceptEnrollment) closeTransport(e *operatorAcceptedTransport) error {
	if p == nil {
		return errors.New("operator accept owner is unavailable")
	}
	p.mu.Lock()
	if e == nil || e.owner != p || !p.transports[e] || e.connection == nil || p.connections[e.connection] != e || e.closeAttempted {
		p.mu.Unlock()
		return errors.New("operator transport lacks its original unconsumed Close")
	}
	e.closeAttempted = true
	c := e.connection
	p.mu.Unlock()
	err := c.Close()
	p.mu.Lock()
	defer p.mu.Unlock()
	e.closeDone = true
	e.closeFailure = err
	if err == nil {
		delete(p.transports, e)
		delete(p.connections, c)
	} else {
		p.failure = errors.Join(p.failure, err)
	}
	p.signal()
	return err
}
