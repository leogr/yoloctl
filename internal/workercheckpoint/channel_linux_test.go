//go:build linux

package workercheckpoint

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Ordinary PRIVATE TRANSPORT fixtures only. None constructs a WorkerHandoff,
// SDK source, original-launch witness, native publisher or receipt authority.
func pair(t *testing.T) (*os.File, *Channel) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	peer := os.NewFile(uintptr(fds[0]), "ordinary-checkpoint-fixture-peer")
	c := Take(os.NewFile(uintptr(fds[1]), "ordinary-checkpoint-fixture-owner"))
	fixtureCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	stop := context.AfterFunc(fixtureCtx, func() { _ = peer.Close(); _ = c.Close() })
	t.Cleanup(func() { stop(); cancel(); _ = peer.Close(); _ = c.Close() })
	return peer, c
}
func frame(f *os.File, kind byte) ([]byte, error) {
	var head [5]byte
	if _, err := io.ReadFull(f, head[:1]); err != nil {
		return nil, err
	}
	if head[0] != kind {
		return nil, fmt.Errorf("wrong opcode %d, wanted %d", head[0], kind)
	}
	if _, err := io.ReadFull(f, head[1:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(head[1:])
	if n > MaxFrame {
		return nil, errors.New("fixture frame overflow")
	}
	raw := make([]byte, int(n))
	_, err := io.ReadFull(f, raw)
	return raw, err
}
func reply(f *os.File, raw []byte) error {
	var head [4]byte
	binary.BigEndian.PutUint32(head[:], uint32(len(raw)))
	_, err := f.Write(append(head[:], raw...))
	return err
}
func permitBegin(t *testing.T, c *Channel, peer *os.File) {
	t.Helper()
	c.Gate()
	done := make(chan error, 1)
	go func() {
		_, err := frame(peer, begin)
		if err == nil {
			_, err = peer.Write([]byte{permit})
		}
		done <- err
	}()
	if err := c.Begin(t.Context(), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestClosedTranscriptPreservesOpcodesAndPhase(t *testing.T) {
	peer, c := pair(t)
	c.Gate()
	// No local grant from Gate and no request written before actual Begin ACK.
	if err := c.CheckPIDAnchor(t.Context(), "box"); err == nil {
		t.Fatal("before-permission anchor check succeeded")
	}
	done := make(chan error, 1)
	go func() {
		for _, op := range []byte{begin, expand, proof, nonce, pidAnchor, complete} {
			raw, err := frame(peer, op)
			if err != nil {
				done <- err
				return
			}
			switch op {
			case nonce, pidAnchor:
				if !bytes.Equal(raw, []byte(`{"box":"box"}`)) {
					done <- errors.New("changed native request bytes")
					return
				}
				body := []byte(`{}`)
				if op == nonce {
					body = []byte(`{"incarnation":"` + strings.Repeat("a", 32) + `"}`)
				}
				if err := reply(peer, body); err != nil {
					done <- err
					return
				}
			default:
				if op == complete && !bytes.Equal(raw, []byte(`255`)) {
					done <- errors.New("status not clamped")
					return
				}
				ack := recorded
				if op == begin {
					ack = permit
				}
				if _, err := peer.Write([]byte{ack}); err != nil {
					done <- err
					return
				}
			}
		}
		var b [1]byte
		if _, err := io.ReadFull(peer, b[:]); err != nil || b[0] != setup {
			done <- errors.New("wrong setup byte")
			return
		}
		if _, err := peer.Write([]byte{attached}); err != nil {
			done <- err
			return
		}
		if _, err := io.ReadFull(peer, b[:]); err != nil || b[0] != finish {
			done <- errors.New("wrong finish byte")
			return
		}
		done <- nil
	}()
	for _, call := range []func() error{
		func() error { return c.Begin(t.Context(), map[string]any{}) },
		func() error { return c.Expand(t.Context(), []string{"box"}) },
		func() error { return c.Proof(t.Context(), map[string]string{"key": "value"}) },
		func() error { _, err := c.NativeNonce(t.Context(), "box"); return err },
		func() error { return c.CheckPIDAnchor(t.Context(), "box") },
		func() error { return c.Complete(t.Context(), 999) },
	} {
		if err := call(); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.CheckPIDAnchor(t.Context(), "box"); err == nil {
		t.Fatal("completed source check succeeded")
	}
	if err := c.Release(); err != nil {
		t.Fatal(err)
	}
	if err := c.Release(); err != nil {
		t.Fatal("cached release changed", err)
	}
	if err := c.Finish(false); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestCanceledWaiterLeavesActiveImageAndOriginalOwnerUntouched(t *testing.T) {
	peer, c := pair(t)
	permitBegin(t, c, peer)
	active := make(chan struct{})
	allowReply := make(chan struct{})
	peerDone := make(chan error, 1)
	go func() {
		if _, err := frame(peer, image); err != nil {
			peerDone <- err
			return
		}
		close(active)
		select {
		case <-allowReply:
		case <-t.Context().Done():
			peerDone <- t.Context().Err()
			return
		}
		if err := reply(peer, []byte(`{}`)); err != nil {
			peerDone <- err
			return
		}
		if _, err := frame(peer, pidAnchor); err != nil {
			peerDone <- err
			return
		}
		peerDone <- reply(peer, []byte(`{}`))
	}()
	imageDone := make(chan error, 1)
	go func() { _, err := c.Image(t.Context(), map[string]string{"action": "resolve"}); imageDone <- err }()
	<-active
	ctx, cancel := context.WithCancel(t.Context())
	waiterDone := make(chan error, 2)
	go func() { waiterDone <- c.CheckPIDAnchor(ctx, "box") }()
	go func() { _, err := c.Observe(ctx); waiterDone <- err }()
	cancel()
	for range 2 {
		select {
		case err := <-waiterDone:
			if !errors.Is(err, context.Canceled) || !IsLocalCancellation(err) {
				t.Fatal("untouched waiter cancellation not distinguished", err)
			}
		case <-time.After(time.Second):
			t.Fatal("canceled waiter blocked behind active exchange")
		}
	}
	select {
	case err := <-imageDone:
		t.Fatal("waiter touched/closed active exchange", err)
	default:
	}
	close(allowReply)
	if err := <-imageDone; err != nil {
		t.Fatal(err)
	}
	if err := c.CheckPIDAnchor(t.Context(), "box"); err != nil {
		t.Fatal("same original owner not usable after canceled waiter", err)
	}
	if err := <-peerDone; err != nil {
		t.Fatal(err)
	}
}
func TestActiveCancellationClosesOnlyOwnedPhysicalStream(t *testing.T) {
	peer, c := pair(t)
	permitBegin(t, c, peer)
	read := make(chan error, 1)
	go func() { _, err := frame(peer, pidAnchor); read <- err }()
	// A previous untouched-cancellation error is a hostile caller-supplied
	// cause here. Active loss must not inherit its outer local classification.
	oldCtx, oldCancel := context.WithCancel(t.Context())
	oldCancel()
	_, oldLocal := c.Observe(oldCtx)
	if !IsLocalCancellation(oldLocal) {
		t.Fatal("missing prior local outcome", oldLocal)
	}
	ctx, cancel := context.WithCancelCause(t.Context())
	done := make(chan error, 1)
	go func() { done <- c.CheckPIDAnchor(ctx, "box") }()
	if err := <-read; err != nil {
		t.Fatal(err)
	}
	cancel(oldLocal)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || IsLocalCancellation(err) {
			t.Fatal("active stream loss confused with untouched waiter", err)
		}
	case <-time.After(time.Second):
		t.Fatal("active owned stream did not settle cancellation")
	}
	if _, err := c.Observe(t.Context()); err == nil {
		t.Fatal("closed original revived")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	// A different ordinary physical channel is independent, not a replacement
	// repairing the failed object and not an SDK source.
	otherPeer, other := pair(t)
	permitBegin(t, other, otherPeer)
}
func TestAnchorRepliesAreCanonicalBoundedAndRefusalPreservesFinish(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		body                 []byte
		uncertain, malformed bool
	}{
		{"known-refusal", []byte(`{"error":"denied"}`), false, false},
		{"uncertain-refusal", []byte(`{"error":"storage lost","uncertain":true}`), true, false},
		{"alias", []byte(`{"Error":"denied"}`), false, true},
		{"duplicate", []byte(`{"error":"denied","error":"denied"}`), false, true},
		{"contradictory", []byte(`{"uncertain":true}`), false, true},
		{"unknown", []byte(`{"version":1}`), false, true},
		{"oversize", bytes.Repeat([]byte("x"), NativeFrameLimit+1), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer, c := pair(t)
			permitBegin(t, c, peer)
			done := make(chan error, 1)
			go func() {
				_, err := frame(peer, pidAnchor)
				if err == nil {
					err = reply(peer, tc.body)
				}
				if err == nil && !tc.malformed {
					var b [1]byte
					_, err = io.ReadFull(peer, b[:])
					if err == nil && b[0] != uncertain {
						err = errors.New("uncertain result byte changed")
					}
				}
				done <- err
			}()
			err := c.CheckPIDAnchor(t.Context(), "box")
			if err == nil {
				t.Fatal("refusal/malformed reply succeeded")
			}
			if errors.Is(err, ErrUncertain) != tc.uncertain {
				t.Fatal("uncertainty marker lost", err)
			}
			if !tc.malformed {
				if err := c.Finish(true); err != nil {
					t.Fatal("known refusal closed ordinary result stream", err)
				}
			} else {
				if _, err := c.Observe(t.Context()); err == nil {
					t.Fatal("malformed channel revived")
				}
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestPrewriteMarshalFailureDoesNotCloseOriginalChannel(t *testing.T) {
	peer, c := pair(t)
	if _, err := c.Image(t.Context(), make(chan int)); err == nil {
		t.Fatal("non-JSON request succeeded")
	}
	permitBegin(t, c, peer)
	if _, err := json.Marshal(boxRequest{"box"}); err != nil {
		t.Fatal(err)
	}
}
