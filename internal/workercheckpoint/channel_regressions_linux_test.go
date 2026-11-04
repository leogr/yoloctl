//go:build linux

package workercheckpoint

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

// Ordinary transport only: r1 pair/frame/reply/permitBegin helpers are reused.
// No original-source/SDK/native authority or callback backend is constructed.
func TestFullReceivedImageFrameSurvivesFinalCancellation(t *testing.T) {
	peer, c := pair(t)
	body := []byte(`{"error":"original publisher outcome uncertain","uncertain":true}`)
	done := make(chan error, 1)
	go func() {
		_, err := frame(peer, image)
		if err == nil {
			err = reply(peer, body)
		}
		done <- err
	}()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var received receivedJSON
	// Use the same CLOSED receive/result helpers as Channel.jsonRequest. The
	// internal-only exchange schedule deterministically cancels AFTER full read,
	// BEFORE exchange finalization; no public transport/source injection hook.
	err := c.exchange(ctx, func() (bool, error) {
		if err := c.send(ctx, image, map[string]string{"action": "publish"}, MaxFrame); err != nil {
			return true, err
		}
		if err := received.read(c, ctx, MaxFrame); err != nil {
			return true, err
		}
		cancel()
		return false, nil
	})
	raw, err := received.outcome(err)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("final cancellation missing", err)
	}
	if !bytes.Equal(raw, body) {
		t.Fatal("complete original parent frame discarded", string(raw), err)
	}
	if _, err := c.Observe(t.Context()); err == nil {
		t.Fatal("canceled physical channel revived")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestPartialImageFrameNeverBecomesObservedOutcome(t *testing.T) {
	peer, c := pair(t)
	done := make(chan error, 1)
	go func() {
		if _, err := frame(peer, image); err != nil {
			done <- err
			return
		}
		// Declared frame is incomplete even though a copied prefix looks like
		// uncertainty metadata. No partial payload is exposed to typed decoding.
		_, err := peer.Write(append([]byte{0, 0, 1, 0}, []byte(`{"error":"lost","uncertain":true}`)...))
		_ = peer.Close()
		done <- err
	}()
	var received receivedJSON
	err := c.exchange(t.Context(), func() (bool, error) {
		if err := c.send(t.Context(), image, map[string]string{"action": "publish"}, MaxFrame); err != nil {
			return true, err
		}
		err := received.read(c, t.Context(), MaxFrame)
		return err != nil, err
	})
	raw, err := received.outcome(err)
	if err == nil || len(raw) != 0 {
		t.Fatal("partial frame supplied a completed outcome", string(raw), err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestCompleteGatedUnbegunStableNoWriteIgnoresCanceledContext(t *testing.T) {
	peer, c := pair(t)
	c.Gate()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.Complete(ctx, 0); err != nil {
		t.Fatal("settled no-write completion changed result", err)
	}
	// Successful actual Begin afterward also proves Complete neither wrote a
	// completion frame nor closed the original stream due canceled context.
	permitBegin(t, c, peer)
}
func TestCompleteGatedUnbegunStableNoWriteAfterClose(t *testing.T) {
	peer, c := pair(t)
	c.Gate()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.Complete(ctx, 0); err != nil {
		t.Fatal("closed settled no-write case lost idempotence", err)
	}
	var b [1]byte
	if n, err := peer.Read(b[:]); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatal("settled no-write case wrote bytes", n, err)
	}
}
func TestCompleteAlreadyRecordedStableNoWriteAfterCancelAndClose(t *testing.T) {
	peer, c := pair(t)
	permitBegin(t, c, peer)
	done := make(chan error, 1)
	go func() {
		_, err := frame(peer, complete)
		if err == nil {
			_, err = peer.Write([]byte{recorded})
		}
		done <- err
	}()
	if err := c.Complete(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := c.Complete(ctx, 99); err != nil {
			t.Fatal("known original completion is no longer idempotent", err)
		}
	}
	var b [1]byte
	if n, err := peer.Read(b[:]); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatal("duplicate completion frame", n, err)
	}
}
func TestCompleteCannotShortcutInFlightBegin(t *testing.T) {
	peer, c := pair(t)
	c.Gate()
	begunWire := make(chan struct{})
	allowAck := make(chan struct{})
	peerDone := make(chan error, 1)
	go func() {
		if _, err := frame(peer, begin); err != nil {
			peerDone <- err
			return
		}
		close(begunWire)
		select {
		case <-allowAck:
		case <-t.Context().Done():
			peerDone <- t.Context().Err()
			return
		}
		if _, err := peer.Write([]byte{permit}); err != nil {
			peerDone <- err
			return
		}
		if _, err := frame(peer, complete); err != nil {
			peerDone <- err
			return
		}
		_, err := peer.Write([]byte{recorded})
		peerDone <- err
	}()
	beginDone := make(chan error, 1)
	go func() { beginDone <- c.Begin(t.Context(), map[string]string{}) }()
	<-begunWire
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	completeDone := make(chan error, 1)
	go func() { completeDone <- c.Complete(ctx, 0) }()
	select {
	case err := <-completeDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("in-flight Begin was treated as settled no-permission", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled Complete waiter blocked behind Begin")
	}
	select {
	case err := <-beginDone:
		t.Fatal("canceled Complete touched active Begin", err)
	default:
	}
	close(allowAck)
	if err := <-beginDone; err != nil {
		t.Fatal(err)
	}
	if err := c.Complete(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	if err := <-peerDone; err != nil {
		t.Fatal(err)
	}
}
func TestCompleteStableNoWriteDoesNotWaitForUnrelatedImage(t *testing.T) {
	peer, c := pair(t)
	c.Gate()
	seen := make(chan struct{})
	allowReply := make(chan struct{})
	peerDone := make(chan error, 1)
	go func() {
		if _, err := frame(peer, image); err != nil {
			peerDone <- err
			return
		}
		close(seen)
		select {
		case <-allowReply:
		case <-t.Context().Done():
			peerDone <- t.Context().Err()
			return
		}
		peerDone <- reply(peer, []byte(`{}`))
	}()
	imageDone := make(chan error, 1)
	go func() { _, err := c.Image(t.Context(), map[string]string{"action": "resolve"}); imageDone <- err }()
	<-seen
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.Complete(ctx, 0); err != nil {
		t.Fatal("stable phase was hidden behind unrelated IO", err)
	}
	close(allowReply)
	if err := <-imageDone; err != nil {
		t.Fatal("no-write completion closed active image stream", err)
	}
	if err := <-peerDone; err != nil {
		t.Fatal(err)
	}
}
