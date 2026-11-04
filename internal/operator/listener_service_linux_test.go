package operator

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func operatorServiceFixture(t *testing.T) (*operatorListenerService, string) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "opservice-")
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(root, "control.sock")
	s, err := newOperatorListenerService(func() (*net.UnixListener, error) {
		return net.ListenUnix("unix", &net.UnixAddr{Name: name, Net: "unix"})
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Test-owned exact fixture cleanup, never acceptance evidence. Some
		// negative cases deliberately withhold their normal Close ACK.
		_ = s.listener.listener.Close()
		_ = os.RemoveAll(root)
	})
	return s, name
}

func dialOperatorService(t *testing.T, name string) *net.UnixConn {
	t.Helper()
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: name, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return c
}

func serviceClosedPeer(t *testing.T, c *net.UnixConn) {
	t.Helper()
	var b [1]byte
	if n, err := c.Read(b[:]); n != 0 || err != io.EOF {
		t.Fatal("original service FD was not actually closed", n, err)
	}
}

func TestOperatorListenerServiceSeparatesTransportCloseFromHandlerReturn(t *testing.T) {
	s, name := operatorServiceFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	closed := make(chan error, 1)
	release := make(chan struct{})
	returned := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	if err := s.start(ctx, func(_ context.Context, c net.Conn) {
		defer close(returned)
		// ServeConn's cancellation path and final defer may call Close at
		// once. Both must receive the original one actual FD result.
		results := make(chan error, 2)
		go func() { results <- c.Close() }()
		go func() { results <- c.Close() }()
		closed <- errors.Join(<-results, <-results)
		<-release
	}); err != nil {
		t.Fatal(err)
	}
	client := dialOperatorService(t, name)
	if err := <-closed; err != nil {
		t.Fatal("repeated handler Close retried its actual FD", err)
	}
	serviceClosedPeer(t, client)
	proof, err := s.retire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := s.observe(proof)
	if err != nil || len(observed) != 1 || observed[0].returned || !observed[0].closeDone || observed[0].closeErr != nil {
		t.Fatal("closed transport lost its still-running handler", observed, err)
	}
	if _, err := os.Lstat(name); err != nil {
		t.Fatal("service retirement unlinked its original socket", err)
	}
	if _, err := s.observe(&operatorServiceSettlement{}); err == nil {
		t.Fatal("foreign service settlement adopted observations")
	}
	if _, err := s.retire(ctx); err == nil {
		t.Fatal("service replayed listener retirement")
	}
	if c, err := s.listener.accept(); c != nil || err == nil {
		t.Fatal("closed retained listener returned a typed-nil connection")
	}
	s.mu.Lock()
	handlerReturned := s.handlers[observed[0].connection].returnedNow
	s.mu.Unlock()
	releaseOnce.Do(func() { close(release) })
	<-returned
	// The fixture's returned channel closes inside serve; the service's
	// separate return observation is ordered by its own final defer.
	select {
	case <-handlerReturned:
	case <-ctx.Done():
		t.Fatal("actual handler return was not observed")
	}
	observed, err = s.observe(proof)
	if err != nil || len(observed) != 1 || !observed[0].returned {
		t.Fatal("actual handler return missing from service observation", err)
	}
}

func TestOperatorListenerServiceRetirementAccountsForConcreteAcceptCrossingSeal(t *testing.T) {
	for _, stage := range []string{"accepted", "enrolled"} {
		t.Run(stage, func(t *testing.T) {
			s, name := operatorServiceFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			at := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			cut := func() { close(at); <-release }
			if stage == "accepted" {
				s.afterAccept = cut
			} else {
				s.afterEnroll = cut
			}
			var calls atomic.Int32
			if err := s.start(ctx, func(context.Context, net.Conn) { calls.Add(1) }); err != nil {
				t.Fatal(err)
			}
			client := dialOperatorService(t, name)
			<-at // actual original AcceptUnix has returned this concrete FD
			type retirement struct {
				proof *operatorServiceSettlement
				err   error
			}
			done := make(chan retirement, 1)
			go func() { proof, err := s.retire(ctx); done <- retirement{proof, err} }()
			<-s.enrollment.listenerClosed // actual one listener Close completed
			once.Do(func() { close(release) })
			result := <-done
			if result.err != nil {
				t.Fatal("owned crossing Accept did not settle", result.err)
			}
			serviceClosedPeer(t, client)
			observed, err := s.observe(result.proof)
			if err != nil {
				t.Fatal(err)
			}
			if stage == "accepted" && len(observed) != 0 || stage == "enrolled" && (len(observed) != 1 || !observed[0].returned || !observed[0].closeDone || observed[0].closeErr != nil) || calls.Load() != 0 {
				t.Fatal("sealed service exposed handler or lost concrete disposal", stage, calls.Load(), observed)
			}
		})
	}
}

func TestOperatorListenerServiceOwnedCloseBeforeActualAcceptSettles(t *testing.T) {
	for _, iteration := range []int32{1, 2} {
		t.Run(map[int32]string{1: "first", 2: "later"}[iteration], func(t *testing.T) {
			s, name := operatorServiceFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			at := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			var accepts, handlers atomic.Int32
			s.beforeAccept = func() {
				if accepts.Add(1) == iteration {
					close(at)
					<-release
				}
			}
			if err := s.start(ctx, func(context.Context, net.Conn) { handlers.Add(1) }); err != nil {
				t.Fatal(err)
			}
			if iteration == 2 {
				client := dialOperatorService(t, name)
				serviceClosedPeer(t, client)
			}
			<-at // pending token exists; concrete Accept has not been called
			if iteration == 2 {
				s.mu.Lock()
				var returned <-chan struct{}
				for _, state := range s.handlers {
					returned = state.returnedNow
				}
				s.mu.Unlock()
				select {
				case <-returned:
				case <-ctx.Done():
					t.Fatal("earlier actual handler did not settle")
				}
			}
			type retirement struct {
				proof *operatorServiceSettlement
				err   error
			}
			done := make(chan retirement, 1)
			go func() { proof, err := s.retire(ctx); done <- retirement{proof, err} }()
			<-s.enrollment.listenerClosed // actual owned Close ACK before Accept
			once.Do(func() { close(release) })
			result := <-done
			if result.err != nil {
				t.Fatal("pre-Accept owned Close did not settle concrete listener", result.err)
			}
			observed, err := s.observe(result.proof)
			if err != nil || len(observed) != int(iteration-1) || handlers.Load() != iteration-1 {
				t.Fatal("pre-Accept retirement lost prior handler or admitted a new one", err, handlers.Load(), observed)
			}
			for _, item := range observed {
				if !item.returned || !item.closeDone || item.closeErr != nil {
					t.Fatal("pre-Accept retirement changed original handler result")
				}
			}
			if _, err := os.Lstat(name); err != nil {
				t.Fatal("pre-Accept retirement unlinked original socket", err)
			}
		})
	}
}

func TestOperatorListenerServiceFailedListenerCloseCannotBecomeLaterSuccess(t *testing.T) {
	s, name := operatorServiceFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := s.start(ctx, func(context.Context, net.Conn) {}); err != nil {
		t.Fatal(err)
	}
	// Lose only the reply after the real owned FD Close, never claim that a
	// later state observation or another Close acknowledges the first one.
	s.listener.mu.Lock()
	actual := s.listener.closeFD
	s.listener.closeFD = func() error { return errors.Join(actual(), errors.New("lost listener Close reply")) }
	s.listener.mu.Unlock()
	if _, err := s.retire(ctx); err == nil {
		t.Fatal("lost Close reply supplied service settlement")
	}
	<-s.loopDone
	if _, err := s.retire(ctx); err == nil {
		t.Fatal("failed service retirement became a later success")
	}
	if _, err := s.observe(&operatorServiceSettlement{owner: s}); err == nil {
		t.Fatal("failed service fabricated handler settlement")
	}
	if _, err := os.Lstat(name); err != nil {
		t.Fatal("failed service retirement unlinked socket", err)
	}
}
