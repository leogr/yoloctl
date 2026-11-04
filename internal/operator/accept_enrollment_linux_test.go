package operator

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func operatorEnrollmentFixture(t *testing.T) (*operatorAcceptEnrollment, string) {
	t.Helper()
	root, e, l := retainedOperatorListenerFixture(t)
	p, err := newOperatorAcceptEnrollment(l)
	if err != nil {
		t.Fatal(err)
	}
	return p, filepath.Join(root, e.Incarnation, "control.sock")
}

func operatorEnrollmentPair(t *testing.T, p *operatorAcceptEnrollment, name string) (*operatorAcceptAttempt, *net.UnixConn, *net.UnixConn) {
	t.Helper()
	a, err := p.beginAccept()
	if err != nil {
		t.Fatal(err)
	}
	dial := net.Dialer{Timeout: 2 * time.Second}
	client, err := dial.DialContext(t.Context(), "unix", name)
	if err != nil {
		t.Fatal(err)
	}
	u, ok := client.(*net.UnixConn)
	if !ok {
		t.Fatal("fixture did not create actual Unix transport")
	}
	t.Cleanup(func() { _ = u.Close() })
	if err := p.listener.listener.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	server, err := p.listener.listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return a, server, u
}

type operatorRetireResult struct {
	proof *operatorListenerAcceptSettlement
	err   error
}

func operatorRetireAsync(t *testing.T, p *operatorAcceptEnrollment) (context.CancelFunc, <-chan operatorRetireResult) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	t.Cleanup(cancel)
	done := make(chan operatorRetireResult, 1)
	go func() { s, err := p.retireListener(ctx); done <- operatorRetireResult{s, err} }()
	select {
	case <-p.listenerClosed:
	case <-ctx.Done():
		t.Fatal("owned listener Close result was not recorded")
	}
	return cancel, done
}

func operatorRetireWait(t *testing.T, done <-chan operatorRetireResult) operatorRetireResult {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-time.After(4 * time.Second):
		t.Fatal("original listener/Accept result did not settle")
	}
	return operatorRetireResult{}
}

func TestOperatorAcceptEnrollmentAccountsForReturnedButUnenrolledTransport(t *testing.T) {
	p, name := operatorEnrollmentFixture(t)
	a, server, client := operatorEnrollmentPair(t, p, name)
	_, done := operatorRetireAsync(t, p)
	select {
	case <-p.settled:
		t.Fatal("listener Close signalled settlement with an original Accept still pending")
	default:
	}
	select {
	case <-done:
		t.Fatal("listener Close ignored original pending Accept result")
	default:
	}
	if entry, err := p.finishAccept(a, server, nil); err == nil || entry != nil {
		t.Fatal("Accept crossing seal was exposed to a handler", err)
	}
	r := operatorRetireWait(t, done)
	if r.err != nil || r.proof == nil {
		t.Fatal("owned rejected transport Close did not settle", r.err)
	}
	entries, err := p.observeTransports(r.proof)
	if err != nil || len(entries) != 0 {
		t.Fatal("rejected transport was lost or falsely enrolled", err)
	}
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if n, err := client.Read(b[:]); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatal("rejected actual Unix transport remained exposed", n, err)
	}
	if _, err := p.beginAccept(); err == nil {
		t.Fatal("sealed owner began another Accept")
	}
}

func TestOperatorAcceptEnrollmentRetainsEarlierTransportsWithoutClaimingRequestsDone(t *testing.T) {
	p, name := operatorEnrollmentFixture(t)
	a, server, client := operatorEnrollmentPair(t, p, name)
	e, err := p.finishAccept(a, server, nil)
	if err != nil || e == nil {
		t.Fatal("original pre-seal transport not enrolled", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	proof, err := p.retireListener(ctx)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := p.observeTransports(proof)
	if err != nil || len(entries) != 1 || entries[0] != e {
		t.Fatal("listener settlement silently discharged accepted stream", err)
	}
	entries[0] = nil
	again, err := p.observeTransports(proof)
	if err != nil || len(again) != 1 || again[0] != e {
		t.Fatal("caller changed original transport membership", err)
	}
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Write([]byte{'x'}); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err := client.Read(b[:]); err != nil || b[0] != 'x' {
		t.Fatal("listener retirement closed an independently live stream", err)
	}
	if err := p.closeTransport(e); err != nil {
		t.Fatal(err)
	}
	if entries, err := p.observeTransports(proof); err != nil || len(entries) != 0 {
		t.Fatal("actual transport Close did not update its own observation", err)
	}
	if err := p.closeTransport(e); err == nil {
		t.Fatal("transport Close was replayed")
	}
	// Empty transport observation is deliberately not request/worker/receipt
	// settlement, and cannot be reconstructed from zero/foreign tokens.
	if _, err := p.observeTransports(&operatorListenerAcceptSettlement{}); err == nil {
		t.Fatal("zero listener token fabricated empty transports")
	}
}

func TestOperatorAcceptEnrollmentActualBlockedAcceptSettlesFromOwnedClose(t *testing.T) {
	p, _ := operatorEnrollmentFixture(t)
	a, err := p.beginAccept()
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		c, err := p.listener.listener.AcceptUnix()
		_, finishErr := p.finishAccept(a, c, err)
		result <- finishErr
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	proof, err := p.retireListener(ctx)
	if err != nil || proof == nil {
		t.Fatal("actual Unix Accept did not settle after exact listener Close", err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatal("actual closed Accept error lost", err)
		}
	case <-ctx.Done():
		t.Fatal("actual Accept result was not joined")
	}
}

func TestOperatorAcceptEnrollmentRejectsMixedClosedResultAndLostConnectionClose(t *testing.T) {
	for _, failure := range []string{"mixed-closed-error", "missing-result", "connection-close-failed"} {
		t.Run(failure, func(t *testing.T) {
			p, name := operatorEnrollmentFixture(t)
			var a *operatorAcceptAttempt
			var server *net.UnixConn
			if failure == "connection-close-failed" {
				a, server, _ = operatorEnrollmentPair(t, p, name)
				if err := server.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				var err error
				a, err = p.beginAccept()
				if err != nil {
					t.Fatal(err)
				}
			}
			_, done := operatorRetireAsync(t, p)
			var acceptErr error
			if failure == "mixed-closed-error" {
				acceptErr = errors.Join(net.ErrClosed, errors.New("another failed result"))
			}
			if e, err := p.finishAccept(a, server, acceptErr); err == nil || e != nil {
				t.Fatal("failed Accept result was exposed", err)
			}
			r := operatorRetireWait(t, done)
			if r.err == nil || r.proof != nil {
				t.Fatal("mixed/incomplete Accept result became settlement", failure, r.err)
			}
			if _, err := p.observeTransports(r.proof); err == nil {
				t.Fatal("failed result supplied transport proof")
			}
		})
	}
}

func TestOperatorAcceptEnrollmentCanceledSettlementCannotBecomeLaterSuccess(t *testing.T) {
	p, _ := operatorEnrollmentFixture(t)
	a, err := p.beginAccept()
	if err != nil {
		t.Fatal(err)
	}
	cancel, done := operatorRetireAsync(t, p)
	cancel()
	r := operatorRetireWait(t, done)
	if !errors.Is(r.err, context.Canceled) || r.proof != nil {
		t.Fatal("unsettled original Accept ignored cancellation", r.err)
	}
	if _, err := p.finishAccept(a, nil, net.ErrClosed); !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	if s, err := p.retireListener(t.Context()); err == nil || s != nil {
		t.Fatal("failed listener/Accept retirement was replayed")
	}
	if _, err := p.observeTransports(nil); err == nil {
		t.Fatal("later empty pending set replaced original failed result")
	}
}

func TestOperatorAcceptEnrollmentRejectsForeignAndReplayedAttemptsWithoutClosingSuppliedFD(t *testing.T) {
	p, name := operatorEnrollmentFixture(t)
	a, server, _ := operatorEnrollmentPair(t, p, name)
	other, otherName := operatorEnrollmentFixture(t)
	b, foreign, client := operatorEnrollmentPair(t, other, otherName)
	if e, err := p.finishAccept(b, foreign, nil); err == nil || e != nil {
		t.Fatal("foreign Accept attempt was adopted")
	}
	e, err := p.finishAccept(a, server, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.finishAccept(a, foreign, nil); err == nil {
		t.Fatal("original Accept result was replayed")
	}
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := foreign.Write([]byte{'y'}); err != nil {
		t.Fatal(err)
	}
	var data [1]byte
	if _, err := client.Read(data[:]); err != nil || data[0] != 'y' {
		t.Fatal("refused token closed another owner's actual connection", err)
	}
	f, err := other.finishAccept(b, foreign, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.closeTransport(e); err == nil {
		t.Fatal("foreign transport Close was adopted")
	}
	if err := p.closeTransport(e); err != nil {
		t.Fatal(err)
	}
	if err := other.closeTransport(f); err != nil {
		t.Fatal(err)
	}
	if _, err := new(operatorAcceptEnrollment).beginAccept(); err == nil {
		t.Fatal("zero owner fabricated an Accept scope")
	}
}

func TestOperatorAcceptEnrollmentCannotReuseAnEarlierTransportUnderNewAttempt(t *testing.T) {
	p, name := operatorEnrollmentFixture(t)
	a, server, client := operatorEnrollmentPair(t, p, name)
	e, err := p.finishAccept(a, server, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.beginAccept()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := p.finishAccept(second, server, nil); err == nil || got != nil {
		t.Fatal("new attempt adopted an earlier original transport", err)
	}
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Write([]byte{'z'}); err != nil {
		t.Fatal(err)
	}
	var data [1]byte
	if _, err := client.Read(data[:]); err != nil || data[0] != 'z' {
		t.Fatal("duplicate result closed the earlier actual transport", err)
	}
	if err := p.closeTransport(e); err != nil {
		t.Fatal(err)
	}
	if proof, err := p.retireListener(t.Context()); err == nil || proof != nil {
		t.Fatal("duplicate Accept result later became settlement")
	}
}

func TestOperatorAcceptEnrollmentFailedListenerCloseRetainsOriginalFailure(t *testing.T) {
	p, _ := operatorEnrollmentFixture(t)
	boom := errors.New("scripted failed listener Close reply")
	calls := 0
	p.listener.closeFD = func() error { calls++; return boom }
	if proof, err := p.retireListener(t.Context()); !errors.Is(err, boom) || proof != nil || calls != 1 {
		t.Fatal("failed original listener Close became Accept settlement", err, calls)
	}
	if proof, err := p.retireListener(t.Context()); err == nil || proof != nil || calls != 1 {
		t.Fatal("failed original listener Close was retried", err, calls)
	}
	if _, err := p.observeTransports(nil); err == nil {
		t.Fatal("failed listener Close supplied transport observation")
	}
}
