package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

type imageCleanupKey struct{}

func ImageCleanupID(ctx context.Context) string {
	s, _ := ctx.Value(imageCleanupKey{}).(string)
	return s
}

// Cleanup owns a distinct, durable images-domain transaction. It never waits
// for jobs or a native lock while holding that native lock itself.
func (m *ImageJobs) Cleanup(ctx context.Context, run func(context.Context, func() error) error) error {
	scope := &Scope{Writes: []Resource{{Kind: ResourceImages}}}
	if err := m.server.awaitReservations(ctx, "", scope); err != nil {
		return err
	}
	for {
		m.mu.Lock()
		busy := m.cleaning
		for _, j := range m.jobs {
			busy = busy || j.building && !j.finished
		}
		if !busy {
			m.cleaning = true
			m.mu.Unlock()
			break
		}
		changed := m.changed
		m.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	defer func() { m.mu.Lock(); m.cleaning = false; m.notify(); m.mu.Unlock() }()
	sum := sha256.Sum256([]byte(rand.Text()))
	id := hex.EncodeToString(sum[:])
	server := m.server
	server.mu.Lock()
	var r Receipt
	var err, storeErr error
	if server.draining || server.receiptFailure != nil {
		err = errors.New("image cleanup admission is closed")
	} else if ctx.Err() != nil {
		err = ctx.Err()
	} else {
		var h holds
		h, err = server.loadHolds("")
		if err == nil {
			if resources, held, global := h.conflict(scope); len(held) != 0 {
				err = heldError(server.Hello.Space, true, global, resources, held)
			} else {
				r, err = server.Receipts.begin(Request{ID: id, Method: "image.cleanup", Dash: -1}, Principal{}, server.Hello.Generation, server.DiskID, true)
				storeErr = err
			}
		}
	}
	if err != nil {
		// No helper was invoked. Settle local metadata ownership without claiming
		// that a permitted cleanup ran, or inventing a result for another owner.
		if r.ID != "" {
			r.Status = 2
			r.Finished = time.Now().UTC()
			if r.EffectPhase == EffectWaiting {
				r.interrupt()
			} else {
				r.State = "complete"
				r.Completion = &Completion{At: r.Finished, Status: 2}
			}
		}
		if storeErr != nil {
			server.record(r, storeErr, true, false, "")
		}
		server.mu.Unlock()
		return err
	}
	if server.active == 0 {
		server.idle = make(chan struct{})
	}
	server.active++
	server.mu.Unlock()
	// The native helper calls permit only after acquiring its native lock.
	// Publishing this reservation earlier can deadlock a normal worker that
	// holds that lock while waiting for the cleanup reservation to finish.
	permit := func() error {
		server.mu.Lock()
		defer server.mu.Unlock()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if server.draining || server.receiptFailure != nil {
			return errors.New("image cleanup admission changed before permission")
		}
		if r.EffectPhase != EffectWaiting {
			return errors.New("cleanup already permitted")
		}
		h, err := server.loadHolds(id)
		if err != nil {
			return err
		}
		if resources, held, global := h.conflict(scope); len(held) != 0 {
			return heldError(server.Hello.Space, true, global, resources, held)
		}
		r.EffectPhase, r.Scope = EffectStarted, scope
		err = server.Receipts.Save(r)
		if err != nil {
			server.record(r, err, false, false, "")
		}
		return err
	}
	err = run(context.WithValue(ctx, imageCleanupKey{}, id), permit)
	server.mu.Lock()
	defer server.mu.Unlock()
	r.Finished = time.Now().UTC()
	if r.EffectPhase == EffectWaiting {
		r.Status = 0
		if err != nil {
			r.Status = 2
		}
		r.interrupt()
	} else if err == nil {
		r.State, r.Status = "complete", 0
		r.Completion = &Completion{At: r.Finished, Status: 0}
	} else {
		r.State, r.Status = "uncertain", 2
	}
	saveErr := server.Receipts.Save(r)
	server.record(r, saveErr, true, false, "")
	_ = server.refreshRecovery()
	server.notifyReceipts()
	server.active--
	if server.active == 0 {
		close(server.idle)
	}
	return errors.Join(err, saveErr)
}
