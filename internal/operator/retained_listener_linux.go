package operator

import (
	"errors"
	"net"
	"sync"
)

// retainedOperatorListener owns only a freshly created listener's unlink
// policy and its ONE Close attempt. It grants no registration/worker/receipt/
// consumer authority and does not settle Accept loops or accepted requests.
// The native publisher must qualify/reserve its exact target, record effect
// permission before create, serialize publication/enrollment, and preserve
// normal shutdown/stale restoration and unresolved-receipt suppression.
// Nothing calls this primitive from the current generic/legacy publisher.
type retainedOperatorListener struct {
	mu             sync.Mutex
	listener       *net.UnixListener
	creationError  error
	closeAttempted bool
	closeFailure   error
	closeFD        func() error
}

// create belongs to the single publisher owner: it must create a FRESH
// listener without exposing it or sharing another Close owner. Actual target
// binding/partial creation failure and original effect custody remain with
// that publisher. Never supply a copied/adopted listener or pathname lookup.
func newRetainedOperatorListener(create func() (*net.UnixListener, error)) (*retainedOperatorListener, error) {
	if create == nil {
		return nil, errors.New("operator listener requires its original creator")
	}
	l, err := create()
	if l == nil {
		return nil, errors.Join(err, errors.New("operator listener creation did not return an owned listener"))
	}
	// Establish this BEFORE any exposure, Accept or cleanup Close, even when
	// the original creator returns a partial listener with a failed reply.
	l.SetUnlinkOnClose(false)
	p := &retainedOperatorListener{listener: l, creationError: err, closeFD: l.Close}
	return p, err // retain a partial owned handle; never hide/repair its error
}

func (p *retainedOperatorListener) accept() (net.Conn, error) {
	if p == nil {
		return nil, errors.New("original operator listener is unavailable")
	}
	p.mu.Lock()
	if p.listener == nil || p.closeAttempted || p.creationError != nil {
		err := errors.Join(p.creationError, errors.New("operator listener is unavailable for Accept"))
		p.mu.Unlock()
		return nil, err
	}
	l := p.listener
	p.mu.Unlock()
	// No publisher/controller lock is held across blocking I/O. A future
	// publisher barrier must account for an Accept already crossing here;
	// this primitive supplies no accept/enrollment/stream settlement ACK.
	return l.Accept()
}

func (p *retainedOperatorListener) Close() error {
	if p == nil {
		return errors.New("original operator listener is unavailable")
	}
	p.mu.Lock()
	if p.closeAttempted {
		err := errors.Join(p.closeFailure, errors.New("original operator listener Close was already attempted"))
		p.mu.Unlock()
		return err
	}
	p.closeAttempted = true // consume before any actual FD operation
	closeFD := p.closeFD
	p.mu.Unlock()
	var err error
	if closeFD == nil {
		err = errors.New("operator listener lacks its original Close operation")
	} else {
		err = closeFD()
	}
	p.mu.Lock()
	p.closeFailure = err
	p.mu.Unlock()
	// Successful Close leaves the original filesystem socket intact. Only
	// the original owned withdrawal may unlink+fsync it, after the separate
	// actual Close/accept/request/Stop/rm/namespace/consumer proofs. An error
	// remains sticky; later state or another Close is never a new ACK.
	return err
}
