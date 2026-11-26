package openshell

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

// The actual pre-remove check calls inactivity twice. Those reads and the
// remove reply wait have separate fail-safes.
func TestNamespaceContainerRemovalStepsHaveTheirOwnFailSafe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const delay = 80 * time.Second
		slow := func(c context.Context) error {
			select {
			case <-time.After(delay):
				return nil
			case <-c.Done():
				return c.Err()
			}
		}
		ok := func(context.Context) error { return nil }
		inactiveReads := 0
		inactive := func(c context.Context) error {
			inactiveReads++
			return runRuntimeWait(c, slow)
		}
		cleanup := context.WithoutCancel(t.Context())
		start := time.Now()
		p, err := runNamespaceContainerRemoval(cleanup, namespaceRemovalHooks{
			beforeRemove: func(c context.Context) error {
				return checkNamespaceBeforeRemove(c, inactive, ok, func() error { return nil })
			},
			removeCID: slow, remaining: ok, removeMetadata: ok,
		})
		if err != nil || !p.completed || inactiveReads != 2 || time.Since(start) != 3*delay {
			t.Fatal("namespace cleanup checks shared one allowance", p, err, inactiveReads, time.Since(start))
		}
		hung := func(c context.Context) error { <-c.Done(); return errors.New("exit status 137") }
		start = time.Now()
		p, err = runNamespaceContainerRemoval(cleanup, namespaceRemovalHooks{beforeRemove: ok, removeCID: hung, remaining: ok, removeMetadata: ok})
		var uncertain *NativeEffectsUncertain
		if !errors.As(err, &uncertain) || !p.removeAttempted || p.removeAcknowledged || time.Since(start) != runtimeFailSafe {
			t.Fatal("a lost remove reply did not end as uncertain", p, err, time.Since(start))
		}
	})
}
